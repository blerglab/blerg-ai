# Test Performance

Open **Test performance** from Insights to keep suite-time trends in their own tab.
Choose a project, suite and time window; expand **Recorded runs** for commits,
outcomes and measured test counts. Commitless runs are retained with a null commit
and labeled **Uncommitted**. Missing counts remain unknown. Times are wall
seconds; suites running in parallel can overlap.

Gate history and completed Go/Vitest/pytest/generic reports can populate the tab. The tools never run a project's tests.
Build the Go importer from the runner module:

```sh
GOWORK=off go build -o test-performance ./cmd/test-performance
./test-performance -history .superpowers/gate-runs.jsonl -member MEMBER_ID
```

Run it inside an authorized agent session. It reads the existing
`BLERG_RUNNER_SERVER_HTTP`, `BLERG_RUNNER_SESSION_ID` and
`BLERG_RUNNER_SESSION_TOKEN` environment variables; do not pass credentials as
arguments. `MEMBER_ID` is a selected workspace member ID. Legacy scalar repository
sessions use `repository`. The tool reads JSONL without changing the history,
imports records sequentially and prints how many expired records it skipped.
A failed import stops with its line number, typed cause and a known public server
message. Superseded revisions are counted and skipped; imports immediately evicted
by retention are counted as **not retained**. Earlier accepted rows
remain stored; rerunning is idempotent.

Each history line must have a run UUID, a valid commit SHA or null, UTC start and finish times,
wall seconds, completeness and suite rows. Imports go to
`POST /api/sessions/{id}/test-runs/import` as
`{"member_id":"MEMBER_ID","revision":1,"record":{...}}`.
The server derives workspace and owner from the authenticated reporting session.
Both it and the workspace root must select the named member. A repeated revision
with identical normalized content succeeds; different content at the same revision requires a higher
`-revision`. An older revision returns a successful superseded receipt. Corrections replace the previous record rather than creating another
trend point.

Runner stores a typed projection in a separate `test_runs` Postgres table. Host
metadata, command output, logs and unrelated fields are discarded. At most 128
suites and the 50 slowest reported tests per suite are kept. Test counts remain
null when not explicitly reported. Gate `changed` names a comparison revision;
it does not establish the selected test region, which stays unknown. Custom reports may explicitly identify a relative selected region. The server
validates that claim but does not infer it from a working directory. Layers,
flakes and project budgets are not inferred.

Retention is 180 days or the newest 2,000 runs per workspace member, whichever
keeps fewer. Imports prune the member immediately; the service's hourly retention
job removes expired rows from inactive projects. Reads exclude expired records,
require an account-wide human sign-in and recheck ownership, ancestry and current
selected membership. The read API `GET /api/test-performance[?workspace=ID]`
returns authorized runs with `next_cursor` for keyset pagination. The default
page has 200 runs; `limit` accepts 1–2,000. Pass the opaque cursor back unchanged.
A `member=ID` filter requires `workspace=ID`; project inventory is independent
of the current run page, so quiet projects stay selectable. Inventory is capped
at 2,000 visible projects and reports its own truncation.

Member attribution is an explicit reporting claim, not an inference from the
working directory or an attestation of which commands ran. Name the selected
member that produced the report. Both the reporting session and its current
Main must authorize that member; moving a source beneath another Main hides
reports attributed to its previous root.

Charts show up to eight series and the newest 200 suite observations, with an
explicit coverage note when bounded. Run details page through 20 runs at a time.
Slowest-test records are retained in storage; the initial trend API omits those
arrays to bound the response.

The tab uses Blerg's native chart renderer and theme tokens, including color-blind
themes. It shows skeletons while fetching. Suite findings show reported budgets,
overruns and comparable per-suite changes.

Project inventory is included only on the first page, ordered by workspace UUID
and member. Keep that inventory while appending subsequent pages. Each run-page
scan examines at most 603 stored rows. If authorization filtering exhausts
that scan budget, `scan_limited` is true and `next_cursor` points after the last
examined row, even if no visible runs were returned. Continue with that cursor;
this is not an empty-history result. `truncated` means a further authorized run
was observed, rather than merely that the scan budget was reached.

## Report a project suite

After a Go, Vitest, pytest or generic suite, report the output from the same authorized session:

```sh
# Save output when running your already-chosen, focused test command.
go test -json ./internal/example -run '^TestExample$' > .cache/go-tests.json
blerg-runner test report --format go --file .cache/go-tests.json --member MEMBER_ID --suite go-unit

# Vitest's JSON reporter supplies wall-time bounds and per-test measurements.
npx vitest run src/example.test.ts --reporter=json --outputFile=.cache/vitest.json
blerg-runner test report --format vitest --file .cache/vitest.json --member MEMBER_ID --suite web-unit --root .
```

Report failed suites too. Preserve the test command's exit status separately from
the reporting command's result. The reporting command only reads an existing
file; its success says the measurements were retained, not that the tests passed.
The Go implementation ships in both the host daemon and the pod runtime through
the existing `blerg-runner` dispatcher, including the local sandbox helper.
Update both the Runner server and runtime before using the new report mode.
A standalone build also supports
`test-performance report` with the same flags.

A report creates one run containing one named suite. `--commit SHA` and
`--region RELATIVE_PATH` are optional; omitted values remain null. Suite names
use lowercase letters, digits, underscores or hyphens and start with a letter
(up to 64 characters). The command derives a stable run UUID from the report bytes, member and suite,
then prints it before uploading. Retrying the same file retains the same identity
even at another path. Changing member, suite or report bytes creates a new run.
Use `--run-id UUID` to override that identity (including corrections to an earlier
report), and a higher `--revision N` for corrections.
Superseded and immediately evicted imports print their status successfully.
The private refreshed session token is preferred over the initial environment token.

Wall seconds use the first measured Go package start through the last measured
package outcome, or
Vitest's earliest file start through its last file end, excluding reporter and
worker setup before execution begins. Cached Go packages are excluded
from both timing and test counts; `cached_packages` records how many were omitted
and any cached package makes the run incomplete. An entirely cached report is
refused because it contains no new measurement. Never rerun tests just to fill
history. Parallel test times are never
summed into suite wall time. Package and file/setup failures fail the suite even
when all reported assertions passed. Counts describe reported test outcomes
(including repeated Go attempts); retries do not establish flakes. Missing test
durations remain absent from the slowest list. At most 50 measured tests are sent.
Vitest filenames become relative to `--root` (the current directory by default);
files outside that root are refused. No raw output or failure prose is uploaded.

Long pytest parameter IDs keep the relative file and function/class identity;
when the test name exceeds 512 bytes, the parameter suffix becomes a stable
SHA-256 fingerprint. Function names and relative file identities must still fit
their existing bounds. Raw long parameters are not uploaded.

The normalizers refuse truncated reports, missing wall-time bounds, unknown
outcomes and malformed JSON. Input is limited to 32 MiB (Go events to 256 KiB),
and normalized imports to 256 KiB. Use a complete JSON report, not terminal output
mixed with JSON. The API accepts `source: "custom"` and an optional relative
`region` alongside the existing authenticated import envelope. Its optional
`cached_packages` count must be nonnegative; a positive count requires the
record to declare `complete: false`. Gate imports retain
their original `source: "gate"` behavior and do not claim a selected region.


## Pytest and generic JSON reports

`--format pytest` reads an existing [pytest-json-report](https://github.com/numirias/pytest-json-report) file:

```sh
pytest tests/test_example.py --json-report --json-report-file=.cache/pytest.json
blerg-runner test report --format pytest --file .cache/pytest.json --member MEMBER_ID --suite python-unit
```

The optional pytest plugin must already be available in the project's environment;
Blerg neither installs it nor runs pytest. Summary-only reports are refused: the
adapter needs explicit test outcomes and checks their count against summary.total.
The plugin's `created` timestamp is the finish observation; session wall time is
`duration`, so the start is `created - duration`. Per-test measured time sums the
reported setup/call/teardown phases only when every present phase has a duration;
missing durations remain unknown. This does not sum parallel tests into wall time.
Setup and teardown failures fail their test. `xfailed` is skipped; non-strict
`xpassed` is passed, matching pytest's outcome. Interrupted/internal-error/usage-error
reports are marked incomplete and failed; a no-tests exit is skipped. Unknown
outcomes are refused. Environment, root, captured output and tracebacks are ignored.
A bare retry-plugin outcome such as `rerun` is refused. A project hook can
supply explicit `blerg_flake_reruns` evidence (below); the last reported attempt
then resolves a `rerun` marker. Ordinary repeated executions alone do not prove
a flake.

For any other test command, write one version-1 JSON report and use `--format json`:

```json
{
  "version": 1,
  "started_at": "2026-11-01T12:00:00Z",
  "finished_at": "2026-11-01T12:00:02Z",
  "complete": true,
  "outcome": "pass",
  "tests": [
    {"package": "tests/unit", "test": "example", "outcome": "pass", "seconds": 0.5},
    {"package": "tests/unit", "test": "not measured", "outcome": "skip"}
  ]
}
```

```sh
blerg-runner test report --format json --file .cache/tests.json --member MEMBER_ID --suite custom-unit
```

All envelope fields above are required, including an explicit tests array (which
may be empty). Test package/name/outcome are required; seconds is optional, with
zero distinct from missing. Outcomes are pass/fail/skip. A failed test fails the
suite even if its reported suite outcome was pass. A suite declared skip is refused if any test passed or failed. Package identities must be
relative, without parent traversal. Unknown fields and versions are refused.
Counts derive from outcomes, and the slowest list keeps the 50 largest measured
tests. Wall duration always comes from the explicit bounds. These adapters use
the same bounded, authenticated import, stable retry identity and retention rules
as Go/Vitest; their output contains no raw runner logs.

## Project budgets

The reporting command reads `.blerg/test-budgets.json` under `--root` (the working
directory by default), when present. `--config FILE` selects an explicit override.
A missing default means unknown budgets; a missing explicit file or malformed
configuration fails the report. Nothing executes this file or edits gate policy.

```json
{
  "version": 1,
  "suites": {
    "python-unit": {"layer": "2", "budget_seconds": 10},
    "database": {"layer": "3b", "budget_seconds": 120}
  }
}
```

Suite names match `--suite` exactly, without wildcards. Unmapped suites remain
unknown, even when other suites have budgets. Layers are `1`, `2`, `3a`, `3b`, `4`
and `5`; budget_seconds must be finite and positive, at most seven days. Both
fields are required per entry. Version, suites and entry keys are strict; the
file is capped at 64 KiB and 128 suite entries. Empty suites is allowed.

The reported suite stores its configured layer and budget with the measurement.
Changing a project's file cannot rewrite historical budgets. To correct an
existing import, reuse its run ID with a higher revision. These are project-owned
reported facts, not an assertion that the server verified the checked-out file.
There is no inferred budget for historical gate runs or unknown suites.

The gate-history import command reads this same default under `-root` (the
working directory by default), with `-config FILE` as an override. Existing
reported suite metadata is preserved; configuration fills only suites with no
reported layer/budget. Re-importing a run with newly supplied budgets is a
correction and requires a higher `-revision`. Gate verdict policy is unchanged.

This repository maps actual names from scripts/pre_merge.py and gate telemetry.
Mixed suites use their highest documented dependency layer from
docs/testing-audit.md: contracts-go and the frontend/CLI suites use 3a/30 s;
core-go, board-go, runner-go, daemon-race and the real-service wire suites use
3b/120 s; runtime-race uses 2/10 s. These BLG-396 starting development targets
intentionally expose current overruns; they are not gate timeouts. Quality gates
have no behavioral layer target. Layer 4/5 acceptance is outside this gate's
ordinary matrix, so no invented suite names are added for those targets.


## Suite findings

The tab shows each latest workspace/member/region/suite measurement, with its
reported layer and budget. An overrun is measured suite wall time minus that
measurement's positive configured budget. Unknown budgets are not zero.
Partial runs remain labeled; their measured duration is still a lower-bound
budget observation, not a claim that the whole command completed.

Per-suite window change compares the latest sample with the oldest complete,
uncached, passing sample within the selected window, in the same workspace,
member, region, suite and reported layer. A missing or zero baseline, failed
latest run or incomplete latest run stays unknown. Added coverage can explain
a change; the percentage alone is not a causal regression finding. Up to 50 suite
findings are displayed, with a coverage note and the existing filters for more.
These facts appear below the chart and above the established recorded-run detail.


## Reported flakes

The adapters and gate-history importer preserve explicitly reported retry
evidence. Generic JSON accepts optional `flake_reruns`; pytest-json-report hooks
can add the same array under `blerg_flake_reruns`:

```json
[
  {"package": "tests/test_example.py", "test": "test_one", "outcomes": ["fail", "pass"]}
]
```

Packages and test identities use the same relative, bounded identities as the
reported test outcomes. In generic/pytest reports every flake identity must also
appear in the report's tests. Evidence must contain a fail followed by a pass;
2–20 normalized pass/fail/skip outcomes are accepted per identity. There are at
most 50 distinct evidence records; duplicates, unsafe identities, unknown
outcomes and oversized arrays are refused. A bare pytest `rerun` is not evidence;
when matching explicit evidence exists, its final normalized outcome supplies
the test's final outcome. The standard pytest plugin does not produce this
extension automatically: a project must supply known retry evidence through its
`pytest_json_modifyreport` hook or normalize it into generic JSON.

Absent/null evidence means unknown. An explicit empty array means zero reported
flakes. Duplicated Go test names, retry counters or separate failed/passed runs
are never inferred to be flakes. A suite's reported count is the number of its
validated evidence records, not the number of attempts. The API stores bounded
evidence but omits its arrays from trend reads, keeping the reported count.
Flakes remain reported evidence, not a server verification that retries ran.

Suite findings show that count or Unknown. Over-budget suites sort first and use
inline accessible budget meters: latest measurement, budget marker, warning-token
overrun segment and the measured overrun text. The trend uses Blerg's chart renderer. Meter entrance runs only on first
appearance in that mounted tab, without replay on filter return, refresh, resize
or theme change; reduced motion suppresses it.
