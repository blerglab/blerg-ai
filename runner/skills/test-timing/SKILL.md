---
name: test-timing
description: Cut a test timing report on demand, so development test time stops growing unnoticed.
---

# Test timing

Development test time compounds silently unless someone watches it. This skill turns the
gate's own history into a report, on demand, so growth gets caught the week it happens, not
the quarter someone finally notices the gate takes twenty minutes.

## When to run it

After each Go, Vitest, pytest or generic suite you run, save its machine-readable output and run
`blerg-runner test report --format go --file REPORT --member MEMBER --suite SUITE`
(or `--format vitest|pytest|json`; see `docs/test-performance.md`) in the same session so the Test Performance tab fills. Report failed
suites too; reuse the printed run ID for retries. This does not add a gate or ask
Main to rerun tests. Never rerun cached tests just to fill history.

- **On request.** Whenever the person (or another agent) asks for test timing, a timing
  report, or "is the gate getting slower".
- **After each gate.** Once `scripts/pre_merge.py` finishes and appends to the history, run
  a report so growth surfaces the same day it happened, not weeks later.
- **Before a release.** Part of the pre-release checklist: a release should never ship a
  measurable, unexplained slowdown in its own gate.

## How to run it

```
blerg-runner report test-timing [--since 7d] [--repo NAME]
```

With no flags it reads `.superpowers/gate-runs.jsonl` in the current repository (BLG-458's
append-only history of `scripts/pre_merge.py`'s summary) and publishes a markdown report with
an inline-data Vega-Lite chart — drawn by Blerg in its own theme tokens, not hardcoded colors.
`--since` only trims the history table; the rolling median that drives growth flags still
uses the full history, or growth would vanish just because the window got narrow.

For a repository with no gate history, or to check a single test runner's own output instead,
pass one of:

```
blerg-runner report test-timing --summary path/to/summary.json          # one scripts/pre_merge.py run
blerg-runner report test-timing --go-test-json suite=path/to/out.json   # go test -json
blerg-runner report test-timing --vitest-json suite=path/to/report.json # vitest's JSON reporter
blerg-runner report test-timing --pytest-durations suite=path/to/out.txt # pytest --durations output
```

Every path above is **read-only history or test-runner output**. The tool never runs tests
itself and never touches the gate; it only reads what already ran. Use `--no-publish` to
print the markdown instead of publishing it, when you need to read it yourself first.

## How to read it

- **Suites** table: last wall time, rolling median, test count, seconds/test. A suite whose
  time grew 25% or more over its own rolling median carries a growth flag.
- The flag's wording tells apart a real slowdown (time and per-test time both grew) from
  added coverage (time grew because the suite gained tests) — read the wording, not just the
  percentage, before deciding what to do about it.
- **Slowest tests**: the 20 slowest individual tests in the latest run, each with its own
  trend against its own rolling median — a suite's total can hold steady while one test
  inside it quietly doubles.
- **What changed since the last report**: the delta between the two most recent gate runs,
  largest moves first. Use it to connect a flag to a specific change.
- Small suites (under ~3 historical samples, or a handful of seconds) will show noisy growth
  percentages from ordinary jitter. Weight a flag by its absolute seconds, not only its
  percentage, before acting on it.

## What to do with a flag

**File a card for any growth you cannot explain.** A flag that traces to a deliberate change
(new tests, a slower but necessary real-service proof) needs no card — say so and move on. A
flag with no known cause is exactly the kind of growth this skill exists to catch: file it
rather than letting it ride.

## For other repositories

The underlying tool (`runner/cmd/test-timing`, Go, no new language) is general: any repository
can point it at its own `go test -json`, vitest JSON reporter, or `pytest --durations` output
even without a `pre_merge.py`-style gate history. The report shape — gate runs, suite stats,
chart, slowest tests, what changed — is the same either way.

See also [[test-driven-development]] for test layering and budgets, and [[coordinating]] for
when Main should be the one pulling the report.

## Go totals and missing runtime helper (BLG-595)

Go reports use terminal package elapsed times, summed across packages, including setup and
cleanup. Without a package terminal event, that package falls back to root test durations.
Counts include root tests once; nested subtests remain in slow-test detail without adding
their overlapping durations to the total. Summed package time can exceed command wall time
when packages run concurrently.

If the installed CLI reports `blerg-test-timing not found`, run the repository tool from a
Blerg checkout: `cd runner && GOWORK=off go run ./cmd/test-timing <report flags>`.
Omit CLI-only `--no-publish`: the repository tool prints Markdown. Alternatively, run
`make build-test-timing` in `runner` and set `BLERG_RUNNER_TESTTIMING_BIN` to the absolute
path of `runner/bin/blerg-test-timing`.
