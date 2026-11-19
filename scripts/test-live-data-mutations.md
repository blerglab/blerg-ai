# Live-data predicate mutation audit

Build the standard-library-only Go command in the existing Runner module:

```sh
GOWORK=off "$HOME/go/bin/go" -C runner build -o /tmp/live-data-audit ./cmd/live-data-audit
/tmp/live-data-audit backend --output /private/backend.json
/tmp/live-data-audit web --output /private/client.json
```

Configure identical `DATABASE_URL` and `TEST_DATABASE_URL` values before backend
audits, including `--list`. The audit refuses before compilation unless both are
explicit PostgreSQL URLs targeting `localhost` or `127.0.0.1`, with dedicated test
port `55440` or a database name containing a delimited `test`/`tests` component
(for example `blerg_test`). Query parameters overriding the connection target are
refused. Refusal messages never include the URLs. Never use a production database.
The command defaults to `$HOME/go/bin/go`; `--go` overrides it. `GOWORK=off`
is enforced for backend subprocesses. No new Python or JavaScript tooling is used.
The existing `scripts/live_data_guard_inventory.go` supplies Go AST positions.

Both runners accept `--list` and `--ids-file` (a JSON array of stable guard IDs).
Backend `--filter` selects a file substring or ID; `--test-run` overrides the
recorded test selector for targeted reproofs. Records are saved after each mutant.
Exit 0 means every selected mutant was assertion-detected; 1 means survivors or
inconclusive outcomes; 2 means an invalid selection, baseline or harness failure.
Catalogue-only mode exits 0 without executing tests.

The runners archive HEAD into disposable directories. Commit selected source and
test changes first: archive/source mismatches refuse the run. Production sources,
rollout flags and installed dependencies are never changed. Backend binaries are
compiled once; each fresh process disables exactly one predicate. Baseline SQL
stays unchanged. Migration predicates use a custom GUC only in fixture connections.
The epoch increment, redirect refusal assignments, queue window and fair-owner
ordering have explicit side-effect mutations. Tagged switches transform all sibling
cases so focused selection preserves the unmutated baseline.

Backend deadlines are three baseline runtimes plus five seconds, bounded to 10–60
seconds. A timeout is inconclusive. Client audits use fresh Vitest processes with
existing local dependency symlinks (no installation or network), a 90-second
process deadline, and a final restored baseline. Assertion failures are `killed`;
success is `survived`. Panic, runtime and harness errors remain separate. DOM
matcher failures count as assertions; assertion output containing a runtime error
does not count as killed. Records contain safe test names and metadata, never raw
process output, database URLs or provider credentials.

The Go client position lexer indexes the fixed hook/transport/viewer surfaces;
comments, strings, templates and regex literals cannot introduce guards. Its 56
current entries match the previous TypeScript-parser inventory field for field.
It is not a general TypeScript parser: control flow inside template interpolation,
arbitrary JSX text, regex/division ambiguities or newly introduced syntax require
inventory review. Explicit markers refuse the audit if their source disappears.

The catalogue discovers Go conditions, simple tagged/tagless switch choices and
boolean return predicates in both directions; shared JWT/TLS and selected identity
and secret-release dependencies; SQL conditions and active migration trigger
conditions. It is a review aid, not exhaustive semantic authorization certification.
Type switches, complex switch selectors, unlisted dependencies and mandatory side
effects outside the named mutations need separately indexed proofs. A survivor is
neither automatically a vulnerability nor automatically equivalent.

Join phases in chronological order, later reproofs superseding earlier observations:

```sh
/tmp/live-data-audit backend --list --output /private/backend-catalog.json
/tmp/live-data-audit web --list --output /private/client-catalog.json
/tmp/live-data-audit summarize \
  --catalog /private/backend-catalog.json --catalog /private/client-catalog.json \
  --record /private/backend.json --record /private/client.json \
  --record /private/targeted-reproof.json --output /private/full-ledger.json
```

Repeat `--record` for each phase. Joining validates current source hashes, rejects
failed baselines, preserves earlier observations and reports missing evidence as
`not_recorded` (exit 1). Stale/duplicate catalogues refuse with exit 2. Publish the
full ledger as a session artifact; do not commit it. Keep outcome counts and every
surviving/inconclusive guard's name, location and assessment in the proof index.
