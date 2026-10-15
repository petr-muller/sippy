# Compare fetchdata Jira access

This diagnostic compares the existing PAT with a service-account credential
without running the data loader or changing its database. It uses Sippy's shared
Jira authentication constructors and the loader's REST paths, JQL, fields, and
pagination. Jira requests are GETs; the OAuth library also acquires access tokens.

Provide the existing `JIRA_TOKEN_BASIC` (or `JIRA_TOKEN`, which takes precedence)
and the RO service account's `JIRA_CLIENT_ID`, `JIRA_CLIENT_SECRET`, and
`JIRA_CLOUD_ID` through your environment. The shared legacy credential aliases
also work. Do not pass credentials as command arguments or paste them into logs.

From the repository root:

```sh
go run ./scripts/jira-compare > jira-comparison.json
```

The tool compares OCPBUGS component fields, every page of the loader's
`trt-incident` search, and issue details/changelogs for the union of issues visible
to either account. It ignores gateway URLs and changelog authors. The report
contains counts, differing IDs/keys, and HTTP access failures, not issue contents
or credentials. Progress goes to stderr. Differences or an incomplete search
produce a nonzero exit code.

To also cover unresolved incidents already in Sippy's database, set
`SIPPY_DATABASE_DSN` and use:

```sh
go run ./scripts/jira-compare --database-incident-keys > jira-comparison.json
```

The database operation is a SELECT inside a read-only transaction. No schema
migration or database writes occur. Alternatively, provide an existing text file
of incident keys with `--incident-keys=/path/to/keys.txt`.

Matching failures on restricted issues are listed even if both accounts agree.
They do not prove access to those issues. Differences can also reflect Jira
updates made during the comparison; rerun affected reads before attributing a
difference to account permissions. This diagnostic does not test Jira writes or
prove renewal if the run finishes before token expiry.

## Recorded comparison

[The 2026-10-15 report](reports/2026-10-15.json) records a live comparison of the
PAT supplied to `dpcr/sippy` fetchdata and the RO service-account credential.
The run included unresolved incident keys selected from Sippy's database in a
read-only transaction. It used `--database-incident-keys` and completed without
Jira or database writes.

The committed report is restricted to aggregate counts and empty difference
and access-failure collections. It contains no incident keys, account identities,
credential values, database connection details, or Jira response contents.
