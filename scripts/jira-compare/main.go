// Command jira-compare checks the Jira reads used by fetchdata with PAT and OAuth.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	jira "github.com/andygrunwald/go-jira"
	"github.com/jackc/pgx/v5"
	"k8s.io/apimachinery/pkg/util/sets"

	v1 "github.com/openshift/sippy/pkg/apis/jira/v1"
	"github.com/openshift/sippy/pkg/flags"
)

// snapshot stores only fields consumed by the loader, excluding gateway URLs and changelog authors.
type snapshot struct {
	Components map[string]string
	Incidents  map[string]string
	Details    map[string]string
	Failures   map[string]string
}

// comparison lists differences without printing credentials or Jira issue contents.
type comparison struct {
	PATComponents int                 `json:"pat_components"`
	SAComponents  int                 `json:"sa_components"`
	PATIncidents  int                 `json:"pat_incidents"`
	SAIncidents   int                 `json:"sa_incidents"`
	DetailKeys    int                 `json:"detail_keys_checked"`
	DatabaseKeys  int                 `json:"database_unresolved_keys"`
	Differences   map[string][]string `json:"differences"`
	PATFailures   map[string]string   `json:"pat_detail_failures"`
	SAFailures    map[string]string   `json:"sa_detail_failures"`
}

// main accepts only nonsecret flags; credentials use Sippy's existing environment variables.
func main() {
	keysFile := flag.String("incident-keys", "", "Optional file of unresolved incident keys, one per line")
	databaseKeys := flag.Bool("database-incident-keys", false, "Read unresolved incident keys from SIPPY_DATABASE_DSN in a read-only transaction")
	timeout := flag.Duration("timeout", 20*time.Minute, "Maximum duration for the comparison")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	err := run(ctx, *keysFile, *databaseKeys, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run shares each authentication client's token source across all GETs and compares both accounts.
func run(ctx context.Context, keysFile string, databaseKeys bool, output io.Writer) error {
	keys, err := readKeys(keysFile)
	if err != nil {
		return err
	}
	dbCount := 0
	if databaseKeys {
		dbKeys, err := unresolvedKeys(ctx)
		if err != nil {
			return err
		}
		dbCount = len(dbKeys)
		keys.Insert(dbKeys...)
	}
	patFlags := flags.NewJiraFlags()
	pat, err := patFlags.GetJiraClient(ctx)
	if err != nil {
		return fmt.Errorf("PAT client initialization failed")
	}
	if pat == nil {
		return fmt.Errorf("set JIRA_TOKEN or JIRA_TOKEN_BASIC for the PAT comparison")
	}
	saFlags := flags.NewJiraFlags()
	saFlags.ServiceAccount = true
	sa, err := saFlags.GetJiraClient(ctx)
	if err != nil {
		return fmt.Errorf("SA initialization failed; check client credentials, cloud ID, and token endpoint access (response body omitted)")
	}
	fmt.Fprintln(os.Stderr, "Reading components and all incident search pages with PAT and SA...")
	a, err := readSnapshot(ctx, pat)
	if err != nil {
		return fmt.Errorf("PAT: %w", err)
	}
	b, err := readSnapshot(ctx, sa)
	if err != nil {
		return fmt.Errorf("SA: %w", err)
	}
	for key := range a.Incidents {
		keys.Insert(key)
	}
	for key := range b.Incidents {
		keys.Insert(key)
	}
	fmt.Fprintf(os.Stderr, "Reading issue details and changelogs for %d keys with each account...\n", keys.Len())
	for _, key := range sets.List(keys) {
		readDetail(ctx, pat, key, &a)
		readDetail(ctx, sa, key, &b)
		if ctx.Err() != nil {
			return fmt.Errorf("comparison canceled or timed out")
		}
	}
	report := comparison{PATComponents: len(a.Components), SAComponents: len(b.Components), PATIncidents: len(a.Incidents), SAIncidents: len(b.Incidents), DetailKeys: keys.Len(), DatabaseKeys: dbCount,
		Differences: map[string][]string{"components": differentKeys(a.Components, b.Components), "incident_search": differentKeys(a.Incidents, b.Incidents), "issue_details": differentKeys(a.Details, b.Details), "detail_access": differentKeys(a.Failures, b.Failures)}, PATFailures: a.Failures, SAFailures: b.Failures}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("write comparison report: %w", err)
	}
	for _, differences := range report.Differences {
		if len(differences) > 0 {
			return fmt.Errorf("PAT and SA results differ; inspect the report")
		}
	}
	for _, failures := range []map[string]string{report.PATFailures, report.SAFailures} {
		for _, failure := range failures {
			if failure != "HTTP 403" && failure != "HTTP 404" {
				return fmt.Errorf("detail comparison is incomplete due to request failures; inspect the report")
			}
		}
	}
	fmt.Fprintln(os.Stderr, "Compared results match. Shared access failures, if any, are listed in the report.")
	return nil
}

// readKeys supplies previously known incidents that a current search may omit.
func readKeys(path string) (sets.Set[string], error) {
	keys := sets.New[string]()
	if path == "" {
		return keys, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read incident keys: %w", err)
	}
	keys.Insert(strings.Fields(string(data))...)
	return keys, nil
}

// unresolvedKeys performs only a SELECT inside a PostgreSQL read-only transaction.
func unresolvedKeys(ctx context.Context) ([]string, error) {
	dsn := os.Getenv("SIPPY_DATABASE_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("set SIPPY_DATABASE_DSN to include unresolved database incidents")
	}
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(dbCtx, dsn)
	if err != nil {
		return nil, fmt.Errorf("database connection failed (connection details omitted)")
	}
	defer conn.Close(dbCtx) // Closing the connection releases its read-only transaction on any failure.
	tx, err := conn.BeginTx(dbCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin read-only database transaction failed")
	}
	defer func() {
		if err := tx.Rollback(dbCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			fmt.Fprintln(os.Stderr, "Read-only transaction cleanup failed; closing its connection.")
		}
	}()
	rows, err := tx.Query(dbCtx, "SELECT key FROM jira_incidents WHERE resolution_time IS NULL ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("read unresolved Jira keys failed")
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("read unresolved Jira key failed")
		}
		keys = append(keys, key)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("read unresolved Jira keys failed")
	}
	return keys, nil
}

// get decodes only GET responses and deliberately omits response bodies from errors.
func get(ctx context.Context, client *jira.Client, path string, into interface{}) error {
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := client.NewRequestWithContext(requestCtx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("construct GET request failed")
	}
	response, err := client.Do(req, into)
	if err != nil {
		if response != nil {
			return fmt.Errorf("HTTP %d", response.StatusCode)
		}
		return fmt.Errorf("request failed without an HTTP response")
	}
	return nil
}

// readSnapshot mirrors the loader's endpoints, JQL, fields, and pagination contract.
func readSnapshot(ctx context.Context, client *jira.Client) (snapshot, error) {
	result := snapshot{Components: map[string]string{}, Incidents: map[string]string{}, Details: map[string]string{}, Failures: map[string]string{}}
	var components []v1.Component
	if err := get(ctx, client, "rest/api/2/project/OCPBUGS/components", &components); err != nil {
		return result, fmt.Errorf("read components: %w", err)
	}
	for _, c := range components {
		value, err := json.Marshal([]string{c.ID, c.Name, c.Description, c.Lead.DisplayName, c.Lead.Name})
		if err != nil {
			return result, fmt.Errorf("encode component comparison: %w", err)
		}
		result.Components[c.ID] = string(value)
	}
	tokens := sets.New[string]()
	next := ""
	for {
		query := url.Values{"jql": {`labels = "trt-incident" AND updated >= -60d`}, "fields": {"summary,created,labels,resolutiondate"}, "expand": {"changelog"}}
		if next != "" {
			query.Set("nextPageToken", next)
		}
		var page v1.SearchResponse
		if err := get(ctx, client, "rest/api/3/search/jql?"+query.Encode(), &page); err != nil {
			return result, fmt.Errorf("read incident page: %w", err)
		}
		for _, issue := range page.Issues {
			value, err := issueValue(issue)
			if err != nil {
				return result, err
			}
			result.Incidents[issue.Key] = value
		}
		if page.IsLast {
			break
		}
		if page.NextPageToken == "" || tokens.Has(page.NextPageToken) {
			return result, fmt.Errorf("invalid or repeated incident pagination token")
		}
		next = page.NextPageToken
		tokens.Insert(next)
	}
	return result, nil
}

// readDetail compares access to the union of searched and previously known incidents.
func readDetail(ctx context.Context, client *jira.Client, key string, result *snapshot) {
	var issue v1.Issue
	if err := get(ctx, client, "rest/api/2/issue/"+url.PathEscape(key)+"?expand=changelog", &issue); err != nil {
		result.Failures[key] = err.Error()
		return
	}
	value, err := issueValue(issue)
	if err != nil {
		result.Failures[key] = err.Error()
		return
	}
	result.Details[key] = value
}

// issueValue ignores account metadata and URLs while retaining loader fields and changelog transitions.
func issueValue(issue v1.Issue) (string, error) {
	labels := append([]string{}, issue.Fields.Labels...)
	sort.Strings(labels)
	history := []string{}
	for _, h := range issue.Changelog.Histories {
		for _, item := range h.Items {
			value, err := json.Marshal([]string{h.Created, item.Field, item.FromString, item.ToString})
			if err != nil {
				return "", fmt.Errorf("encode changelog comparison: %w", err)
			}
			history = append(history, string(value))
		}
	}
	value, err := json.Marshal(struct {
		ID, Key, Summary, Created, Resolution string
		Labels, History                       []string
		ChangelogTotal                        int
	}{issue.ID, issue.Key, issue.Fields.Summary, issue.Fields.Created, issue.Fields.ResolutionDate, labels, history, issue.Changelog.Total})
	if err != nil {
		return "", fmt.Errorf("encode incident comparison: %w", err)
	}
	return string(value), nil
}

// differentKeys reports missing or changed records without leaking their field values.
func differentKeys(a, b map[string]string) []string {
	keys := sets.New[string]()
	for key := range a {
		keys.Insert(key)
	}
	for key := range b {
		keys.Insert(key)
	}
	result := []string{}
	for _, key := range sets.List(keys) {
		av, ap := a[key]
		bv, bp := b[key]
		if ap != bp || av != bv {
			result = append(result, key)
		}
	}
	return result
}
