package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	jira "github.com/andygrunwald/go-jira"
	v1 "github.com/openshift/sippy/pkg/apis/jira/v1"
	"github.com/stretchr/testify/require"
)

// TestReadSnapshot checks complete pagination and rejects responses that would hide missing issues.
func TestReadSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, secondToken string
		last              bool
		wantError         string
	}{
		{"all pages", "", true, ""},
		{"missing token", "", false, "invalid or repeated incident pagination token"},
		{"repeated token", "second", false, "invalid or repeated incident pagination token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				switch r.URL.Path {
				case "/rest/api/2/project/OCPBUGS/components":
					_, err := w.Write([]byte(`[{"id":"1","name":"Test Framework"}]`))
					require.NoError(t, err)
				case "/rest/api/3/search/jql":
					require.Equal(t, `labels = "trt-incident" AND updated >= -60d`, r.URL.Query().Get("jql"))
					require.Equal(t, "summary,created,labels,resolutiondate", r.URL.Query().Get("fields"))
					require.Equal(t, "changelog", r.URL.Query().Get("expand"))
					page := v1.SearchResponse{Issues: []v1.Issue{{Key: "TRT-1"}}, NextPageToken: "second"}
					if r.URL.Query().Get("nextPageToken") == "second" {
						page = v1.SearchResponse{Issues: []v1.Issue{{Key: "TRT-2"}}, NextPageToken: tc.secondToken, IsLast: tc.last}
					}
					require.NoError(t, json.NewEncoder(w).Encode(page))
				default:
					t.Errorf("unexpected URL %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client, err := jira.NewClient(server.Client(), server.URL)
			require.NoError(t, err)
			result, err := readSnapshot(context.Background(), client)
			if tc.wantError != "" {
				require.EqualError(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Components, 1)
			require.Len(t, result.Incidents, 2)
		})
	}
}

// TestIssueValue compares loader data while ignoring gateway URLs and changelog author identity.
func TestIssueValue(t *testing.T) {
	a := v1.Issue{ID: "1", Key: "TRT-1", Self: "https://site/issue/1", Fields: v1.Fields{Summary: "incident", Labels: []string{"trt-incident", "other"}}, Changelog: v1.Changelog{Histories: []v1.History{{Created: "2026-10-15T00:00:00.000+0000", Author: v1.User{DisplayName: "person"}, Items: []v1.Item{{Field: "status", ToString: "MODIFIED"}}}}}}
	av, err := issueValue(a)
	require.NoError(t, err)
	b := a
	b.Self = "https://gateway/issue/1"
	b.Fields.Labels = []string{"other", "trt-incident"}
	// Unmarshal a copy so changing changelog identity does not modify a's slice.
	data, err := json.Marshal(b)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &b))
	b.Changelog.Histories[0].Author.DisplayName = "another person"
	bv, err := issueValue(b)
	require.NoError(t, err)
	require.Equal(t, av, bv)
	b.Changelog.Histories[0].Items[0].ToString = "ON_QA"
	bv, err = issueValue(b)
	require.NoError(t, err)
	require.NotEqual(t, av, bv)
}

// TestReadDetail retains denied-access evidence without logging Jira response contents.
func TestReadDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "changelog", r.URL.Query().Get("expand"))
		w.WriteHeader(http.StatusForbidden)
		_, err := w.Write([]byte(`{"errorMessages":["private response contents"]}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	client, err := jira.NewClient(server.Client(), server.URL)
	require.NoError(t, err)
	result := snapshot{Details: map[string]string{}, Failures: map[string]string{}}
	readDetail(context.Background(), client, "TRT-1", &result)
	require.Equal(t, map[string]string{"TRT-1": "HTTP 403"}, result.Failures)
	require.Empty(t, result.Details)
}

// TestDifferentKeys includes missing records even when their stored comparison value is empty.
func TestDifferentKeys(t *testing.T) {
	require.Equal(t, []string{"added", "changed", "removed"}, differentKeys(map[string]string{"same": "1", "changed": "old", "removed": ""}, map[string]string{"same": "1", "changed": "new", "added": ""}))
	require.Empty(t, differentKeys(map[string]string{}, map[string]string{}))
}

// TestReadKeys preserves the incident-key input contract and deduplicates whitespace-separated keys.
func TestReadKeys(t *testing.T) {
	keys, err := readKeys("")
	require.NoError(t, err)
	require.Empty(t, keys)
	path := filepath.Join(t.TempDir(), "keys")
	require.NoError(t, os.WriteFile(path, []byte("TRT-1\nTRT-2\nTRT-1\n"), 0o600))
	keys, err = readKeys(path)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	_, err = readKeys(path + "-missing")
	require.Error(t, err)
}
