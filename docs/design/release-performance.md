# Release performance check (BLG-453)

Design only. No product or gate code changes in this phase.

Revision 2: addresses design review (H1–H4, M5–M8, L9). Revision 1 measured
component tests with stubbed transport, in-process backend timing only, no
host fingerprint, no noise calibration, wall-clock seed timestamps, and left
the systemd unit and isolation unspecified. All of that is corrected below.

## Intent

Today's gate (`runner-performance`, inside `scripts/pre_merge.py`) checks fixed
budgets against synthetic fixtures — e.g. typing median under 16 ms in a
synthetic long session (`LongSession.performance.browser.test.tsx`). That
catches "too slow," not "30% slower than last release." It also runs on every
gate, which is the wrong cadence for a check whose signal is release-over-release
drift on a realistic, expensive dataset.

This check is a separate, occasional tool: run once near a release cut, against
a seeded dataset shaped like real usage, comparing this release's timings to
the last release's recorded baseline. It never runs as part of the ordinary
gate and never blocks normal development.

**It drives the real, built app.** `LongSession.performance.browser.test.tsx`
and its siblings mock `../ws`, `../authClient`, `../apiFetch` and `global.fetch`
(see the `vi.mock(...)` block at the top of that file) — they measure a
component in isolation, not a request ever leaving the process. This check
instead builds core, board and the runner server and frontend, serves them for
real, and drives the result with Playwright (already a dependency of both
`runner/frontend/package.json` and `board/web/package.json`, via
`@vitest/browser-playwright`/`playwright`) issuing real network requests
against the seeded Postgres databases below.

## Isolation (M6)

The tool never touches the live daemon, a developer's checkout state, or user
data. Concretely:

- **Its own data directories.** A fresh temp directory tree per run
  (`$dir/core`, `$dir/board`, `$dir/runner`) passed as each service's `HOME`/
  state/repository root, the same isolation style `cmd/runtime-smoke` already
  uses for host/sandbox acceptance (private `HOME`/state/repositories, no
  shared state with a real install).
- **Its own ports.** Each of the three servers (core, board, runner) is
  started with its listener bound to `:0` (an OS-assigned ephemeral port) and
  the tool reads back the assigned port before wiring the others' config to
  point at it — never a guessed or conventional dev port, so a real dev
  server or daemon running on the same machine is never contended or confused
  with this run's servers.
- **Its own environment.** Every subprocess gets an explicit environment
  slice built from scratch — not `append(os.Environ(), ...)`. It contains
  only `PATH`, the temp `HOME`, each service's own `DATABASE_URL`, and the
  one new `BLERG_RELEASE_PERF_SERVER_TIMING=1` flag (H2, below). No `BLERG_*`
  variable from the invoking shell or the live daemon's environment is
  inherited, so a real install's agent tokens, daemon ID, or sandbox image
  settings can never leak in or be mistaken for this run's config.
- **Its own disposable databases**, one per service: `blerg_release_perf_core`,
  `blerg_release_perf_board`, `blerg_release_perf_runner`, all on the Postgres
  instance named by `TEST_DATABASE_URL`'s host (reusing the existing
  CREATEDB-capable administration connection the gate already requires) but
  as separate databases the tool creates and drops itself — never the shared
  `TEST_DATABASE_URL` database other tests use, and never an install's real
  database.
- **All three builds, not just the daemon.** The tool builds the core server
  binary, the board server binary, the runner daemon+server binary, and runs
  each frontend's production build (`npm run build` in `core/web`,
  `board/web`, `runner/frontend`) and serves the built static output — the
  same artifacts a release actually ships, not a dev server with HMR.

## Seeded sign-in (H1)

Core's local (password) identity provider already has a real bootstrap flow:
`POST /auth/setup` creates the first account (`core/internal/api/setup_handlers.go`,
`handleSetup`), the same endpoint a fresh install's first-run setup page calls.
The seeder calls this endpoint once, for real, over HTTP against the freshly
started core server, with a fixed, checked-in test account
(`release-perf@blerg.test` / a fixed fixture password) — not a hand-written
database row with a hand-rolled password hash, so the account is exactly as
real as any other account and a change to the password-hashing scheme shows up
here instead of being silently bypassed.

Playwright signs in through the real login page (`handleLoginPage`) by typing
that fixed username/password and submitting the form, the same path a person
uses. The resulting session cookie is then reused by Playwright's browser
context for every scenario in the run; sign-in itself is not a timed scenario.

## Timing, in the page and on the wire (H1, H2)

### Frontend: marks the app itself emits

The existing `painted()` test helper (`runner/frontend/src/test/longWorkspace.ts`)
is just "wait two animation frames" — it proves a paint happened, not that
*this view's* data finished loading and rendering. Driving the real app
instead of a component needs a real readiness signal from the app.

A new, tiny, flag-gated module (`src/releasePerfMarks.ts` in each frontend)
exposes one function, `mark(name: string)`, that calls the browser's native
`performance.mark(name)` and is a complete no-op unless
`window.__BLERG_RELEASE_PERF__` is set. The release-perf harness sets that flag
with Playwright's `page.addInitScript(...)` before navigation, so in every
other context — dev, the existing component tests, a real install — the call
compiles away to nothing and ships no new behavior. Seven call sites are added
at the exact points each scenario considers "done":

| Scenario | Mark name | Call site |
| --- | --- | --- |
| Typing | `key-committed` | After the keystroke's state update has committed (same point `LongSession`'s Profiler already observes) |
| Workspace switch | `workspace-switched` | After the newly selected workspace's session list has rendered |
| Transcript open | `transcript-rendered` | After the opened transcript's last fetched page has rendered |
| Board tab | `board-rendered` | After the board view has rendered all fetched columns/cards |
| Insights 7-day | `insights-rendered` | After the Insights page has rendered the 7-day response |
| Search | `search-results-rendered` | After search results have rendered for the submitted query |
| Blackboard | `blackboard-rendered` | After the blackboard view has rendered its fetched content |

Playwright times each scenario as the wall-clock gap between the action that
starts it (click, keypress, navigation) and the corresponding mark appearing
in `performance.getEntriesByType('mark')` (polled via `page.waitForFunction`),
read directly from the real page — not simulated, not inferred from a
component harness.

### Backend: client-observed plus server-reported (H2)

Every scenario's backend call is timed **twice**, from the same request:

1. **Client-observed**: Playwright/the Go HTTP client's own wall-clock around
   the request, which is what a user actually experiences (includes network,
   queueing, everything).
2. **Server-reported**: a `Server-Timing` response header
   (`Server-Timing: total;dur=42.3`, extendable with named phases where a
   handler already tracks them — e.g. Insights' existing per-phase
   `Timing.Phases` map from `insightActivityReport`), written **only** when
   the request carries `X-Blerg-Release-Perf: 1`. Each server checks that
   header and, only then, wraps its handler body in `time.Since` and writes
   the result — normal traffic never pays for or exposes this. This replaces
   the previous design's `pprof.Do`-and-`time.Since` approach, which only
   ever timed an in-process Go test call, never a real request.

The report (below) shows both numbers per backend-touching scenario, so a
regression that's all network/queueing (client-observed up, server-reported
flat) reads differently from one that's all handler work (both up together).

## Host fingerprint (H3)

A baseline recorded on one machine is not valid evidence on another — a
15%-faster laptop would look like a regression-free release for the wrong
reason, and a slower CI box would look like a false regression. The baseline
file therefore records a **host fingerprint**:

```json
"host": {
  "cpu_model": "AMD Ryzen 9 7950X",
  "logical_cpus": 32,
  "memory_gib": 64,
  "kernel": "5.15.167.4-microsoft-standard-WSL2",
  "wsl": true,
  "browser": "Chromium 131.0.6778.0 (Playwright 1.60.0)"
}
```

Before comparing, the tool recomputes this run's own fingerprint the same way
and compares it field-by-field against the baseline's. **Any mismatch is a
refusal, not a regression**: exit 2, print the exact field that differs and
both values (e.g. `host mismatch: kernel baseline="5.15.167.4-..." this
run="6.1.0-..."; no verdict — rerun on the machine that recorded the
baseline`). `cpu_model`/`kernel`/`wsl` are read from `/proc/cpuinfo`,
`uname -r`, and the presence of `/proc/sys/fs/binfmt_misc/WSLInterop` (or
equivalent) respectively; `browser` is read from the Playwright-launched
browser's own version string, not assumed from the installed package version.
A baseline is only ever valid on the exact machine that recorded it; moving
the release-perf host is a deliberate re-baseline (see Baseline acceptance,
M5), not something this tool papers over with normalization.

## Noise calibration (H4)

A single pair of numbers (this release vs. last) can't distinguish "the code
got slower" from "this machine's run-to-run jitter is bigger than 15%." The
tool measures that jitter directly instead of assuming it away:

- **Calibration runs.** Immediately after the timed run on a given commit,
  the tool repeats the full scenario suite **two more times** (3 total
  independent sample sets: the primary run plus 2 calibration repeats), each
  with its own fresh warm-up iteration. For each scenario, the **noise** is
  the maximum absolute difference between any two of the three runs' medians
  (a simple, auditable spread measure — not a modeled distribution). This
  roughly triples the wall-clock cost of a run, which is accepted because this
  check is occasional by design (near a release cut), not a per-gate cost.
- **Noise is stored with the baseline**, per scenario, alongside its median
  and p95:
  `"workspace-switch": { ..., "noise_median_ms": 9.4 }`.
- **A regression is flagged only when both hold**: the median change exceeds
  the 15% margin **and** exceeds the stored `noise_median_ms` for that
  scenario. A change that's large in percentage terms but smaller than this
  machine's own observed jitter is not reported as a regression (though it's
  still shown in the report, unflagged, so a trend across several releases
  stays visible).
- **One rerun before confirming.** A scenario that trips both conditions is
  re-measured once more (one more full N-repetition set, no new calibration)
  before the run's final verdict is decided. If the rerun no longer trips both
  conditions, the scenario is reported as "flagged, not confirmed" (shown, not
  exit-1'd); if it still trips, it's a confirmed regression and the run exits 1.
  This catches a single unlucky set of samples without hiding a real, sustained
  regression.
- A **new baseline's own calibration runs** double as its noise measurement —
  there's no separate "measure noise" mode; every baseline-recording run
  already does the 3-run calibration described above.

## Scenario list

| Scenario | Layer(s) | What's measured |
| --- | --- | --- |
| Cold load | Frontend + backend | `page.goto`-to-`transcript-rendered`-mark ms for a fresh page load: a full SPA boot plus a new WebSocket's `sendInitialState` (BLG-491) — the connect-time cost real reloads, refreshes and phone reconnects all pay |
| Typing in a long session | Frontend | Keypress-to-`key-committed`-mark ms in the seeded multi-thousand-event session |
| Switching workspaces | Frontend | Click-to-`workspace-switched`-mark ms between two seeded workspaces, as a real in-app navigation on an already-open page — never a `page.goto` (that measured Cold load's cost instead, not switching) |
| Opening a long transcript | Frontend + backend | Click-to-`transcript-rendered`-mark ms, same real in-app navigation as Switching workspaces, landing on the long-transcript workspace; backend: transcript-fetch endpoint, client-observed and `Server-Timing` |
| Board tab | Frontend + backend | Click-to-`board-rendered`-mark ms for a board with ≥150 cards; backend: `board/internal/api/boardsnapshot.go`'s handler, client-observed and `Server-Timing` |
| Insights 7-day | Frontend + backend | `/api/insights/activity?range=7d`: client-observed and `Server-Timing`; frontend: request-to-`insights-rendered`-mark ms |
| Search | Frontend + backend | A representative query's dispatch-to-`search-results-rendered`-mark ms; backend: the search endpoint, client-observed and `Server-Timing` |
| Blackboard | Frontend + backend | A real in-app open-to-`blackboard-rendered`-mark ms (closing the tab first, then reopening it — never a reload); backend: the blackboard-fetch endpoint, client-observed and `Server-Timing` |

Revision 3 (owner review, 2026-11-04): the first implementation drove Switching
workspaces, Opening a long transcript and Blackboard with a `page.goto` on
every repetition to force each scenario's mark to refire. That also forced a
fresh WebSocket connection every time, whose `sendInitialState` handler scans
every session on the account — measured directly at 1.7–2.2s against this
seed's 446 sessions, filed as product bug BLG-491 (a real, per-connect cost
that affects every reload, refresh and phone reconnect, not a seed artifact).
A real workspace switch, transcript open or blackboard open never pays that:
the socket from the page's own first load stays open across in-app
navigation. Those three scenarios now drive real in-app interactions instead
(a sidebar click, or a tab close-then-reopen for Blackboard), and a new Cold
load scenario measures the full-reload/reconnect cost on purpose, as its own
named thing — so a connect-time regression like BLG-491 is still caught, just
not conflated with in-app navigation speed.

## Dataset and seed

Three dedicated, disposable Postgres databases (`blerg_release_perf_core`,
`blerg_release_perf_board`, `blerg_release_perf_runner`), dropped and recreated
at the start of every run — never the shared `TEST_DATABASE_URL` database
other tests use. Migrated with each module's existing migration runner.

Seed content, generated by a new deterministic seeder that calls the real
running servers' own APIs wherever a real API exists (the account, via
`/auth/setup`; board cards, via the board API — not raw `INSERT`s into tables
those services own, so the seeded shape stays valid as those schemas evolve),
and direct SQL only for bulk event/timing data too large to seed one API call
at a time (mirroring the `generate_series`-based bulk inserts
`TestInsights7dSeededLatency` already uses for the same reason):

- **~25 workspaces**, so workspace-switching has real breadth to switch across.
- **One long-transcript workspace**: a session with several thousand
  (8,000) `agent_events`, mirroring the existing 400-seeded-worker /
  `generate_series` shape, for the typing and long-transcript scenarios.
- **A week of Insights data at the already-proven scale**: 420 conversations,
  168,000 tool events, 1,000 card references, spread across 6–7 days — the
  exact shape `TestInsights7dSeededLatency` already seeds and measures.
- **A board with ≥150 cards** across several columns and tags, created
  through the board API, in the board database.
- **A populated blackboard** for one workspace, with enough entries that
  opening it is a real render, not an empty state.

### Fixed epoch, not `now()` (L9)

Revision 1 anchored seed timestamps to `now()` at seed time, matching
`TestInsights7dSeededLatency`'s own `now()-interval '7 days'`. That's wrong
for *this* tool: comparing release N to release N-1 only means something if
both ran against byte-identical data, and "relative to whenever this run
happened" is not identical across two runs on different days. The seeder
instead uses a **fixed, literal epoch baked into its source**
(`2026-01-01T00:00:00Z`), and every seeded timestamp is computed as an offset
from that constant, never from `now()`. The seeded week is not "this week" —
it's a fixed, frozen week that looks the same on every run, forever, which is
the property this check actually needs.

All random content (card titles, event payload text) is generated from a
**fixed seed constant** fed to a deterministic PRNG, so the dataset's shape is
byte-identical across runs and across releases.

## Repetitions and statistics

- **Frontend view-level scenarios** (workspace switch, transcript open, board
  tab, Insights render, search, blackboard): **N = 11** repetitions.
- **Typing** (per-keystroke): **N = 30** repetitions, matching the existing
  `LongSession` harness.
- **Backend timings**: taken from the same requests as their paired frontend
  scenario (both the client-observed and `Server-Timing` numbers come off the
  same N requests) — never a separately-repeated backend-only call that could
  drift from what the frontend actually experienced.
- **One discarded warm-up iteration** per scenario, before the N counted
  samples, absorbing first-hit cache/JIT/connection-pool cost. No other
  trimming, no dropped outliers.
- **Median and p95** for every scenario, p95 computed as
  `sorted[ceil(0.95 * N) - 1]` via a named helper (not the hardcoded sample
  index the existing `LongSession` test currently inlines — that test should
  move to the same helper when this is built, so the two don't drift).
- The whole N-repetition set runs **three times per commit** — the primary
  run and two calibration repeats (Noise calibration, above).

## Baseline file format and location

One JSON file per release, committed to the repository at
`docs/performance-baselines/<version>.json` (e.g. `0.6.0.json`).

```json
{
  "version": "0.6.0",
  "commit": "<git sha the run measured>",
  "recorded_at": "2026-11-05T00:00:00Z",
  "accepted_at": "2026-11-06T00:00:00Z",
  "release_tag": "v0.6.0",
  "seed_version": 1,
  "host": {
    "cpu_model": "AMD Ryzen 9 7950X", "logical_cpus": 32, "memory_gib": 64,
    "kernel": "5.15.167.4-microsoft-standard-WSL2", "wsl": true,
    "browser": "Chromium 131.0.6778.0 (Playwright 1.60.0)"
  },
  "scenarios": {
    "typing-long-session": {
      "unit": "ms", "n": 30, "median": 7.8, "p95": 12.1, "budget_p95": 16,
      "noise_median_ms": 1.1
    },
    "workspace-switch": {
      "unit": "ms", "n": 11, "median": 142.0, "p95": 210.5, "noise_median_ms": 9.4
    },
    "transcript-open-frontend": { "unit": "ms", "n": 11, "median": 980.0, "p95": 1340.2, "noise_median_ms": 40.0 },
    "transcript-open-backend-client": { "unit": "ms", "n": 11, "median": 61.0, "p95": 94.0, "noise_median_ms": 5.0 },
    "transcript-open-backend-server": { "unit": "ms", "n": 11, "median": 38.0, "p95": 55.0, "noise_median_ms": 3.0 },
    "board-tab-frontend": { "unit": "ms", "n": 11, "median": 410.0, "p95": 560.0, "noise_median_ms": 20.0 },
    "board-tab-backend-client": { "unit": "ms", "n": 11, "median": 55.0, "p95": 80.0, "noise_median_ms": 4.0 },
    "board-tab-backend-server": { "unit": "ms", "n": 11, "median": 38.0, "p95": 55.0, "noise_median_ms": 3.0 },
    "insights-7d-backend-client": { "unit": "ms", "n": 11, "median": 230.0, "p95": 320.0, "noise_median_ms": 15.0 },
    "insights-7d-backend-server": { "unit": "ms", "n": 11, "median": 210.0, "p95": 300.0, "budget_p95": 500, "noise_median_ms": 12.0 },
    "insights-7d-frontend": { "unit": "ms", "n": 11, "median": 150.0, "p95": 220.0, "noise_median_ms": 10.0 },
    "search-frontend": { "unit": "ms", "n": 11, "median": 90.0, "p95": 140.0, "noise_median_ms": 6.0 },
    "search-backend-client": { "unit": "ms", "n": 11, "median": 40.0, "p95": 60.0, "noise_median_ms": 3.0 },
    "search-backend-server": { "unit": "ms", "n": 11, "median": 25.0, "p95": 38.0, "noise_median_ms": 2.0 },
    "blackboard-frontend": { "unit": "ms", "n": 11, "median": 160.0, "p95": 230.0, "noise_median_ms": 11.0 },
    "blackboard-backend-client": { "unit": "ms", "n": 11, "median": 45.0, "p95": 65.0, "noise_median_ms": 3.0 },
    "blackboard-backend-server": { "unit": "ms", "n": 11, "median": 30.0, "p95": 42.0, "noise_median_ms": 2.0 }
  }
}
```

- `budget_p95` is optional per scenario — set only where a fixed budget
  already exists or is explicitly adopted (typing keeps the gate's existing
  16 ms; Insights 7-day's server number keeps the 500 ms target already named
  in `TestInsights7dSeededLatency`'s log line). Scenarios without one are
  judged on the margin-and-noise rule alone.
- `seed_version` invalidates stale comparisons on purpose: a baseline recorded
  under a different `seed_version` is treated as absent.
- `accepted_at`/`release_tag` are absent until the owner accepts the release
  (Baseline acceptance, below) — their presence is what makes a file the
  *official* baseline rather than a candidate.

## Comparison rule

For each scenario present in both the new run and the baseline:

- **Median regression**: flag only if `new.median > baseline.median * 1.15`
  **and** `new.median - baseline.median > baseline.noise_median_ms` (Noise
  calibration, above). Both the margin and the noise floor are exposed as
  flags/env defaults so they can be tuned later without code changes.
- **p95 budget**: flag if the scenario has a `budget_p95` and `new.p95` is at
  or above it, independent of the median rule.
- A flagged scenario is re-measured once (one rerun, no new calibration)
  before the run's final verdict; a rerun that no longer trips both
  conditions is reported "flagged, not confirmed," not a failure.
- A scenario missing from the baseline (new this release) is recorded but
  never flagged. A scenario in the baseline but dropped from the list is
  reported as removed, not silently disappeared.
- A **host fingerprint mismatch** (H3) short-circuits all of the above: no
  comparison is attempted, the run exits 2.

## Baseline acceptance (M5)

A `make release-perf` run **never overwrites the official baseline by
itself**. Every run writes its numbers to a **candidate** file,
`docs/performance-baselines/<version>-candidate.json` (gitignored — it's
working evidence, not a release artifact), plus the Markdown report. Promotion
from candidate to official baseline is a separate, explicit step,
`make release-perf-accept VERSION=0.6.0`, that:

1. Requires the candidate's run to have been `clean` (no confirmed
   regressions, no inconclusive scenarios, no host mismatch).
2. Copies the candidate to `docs/performance-baselines/0.6.0.json`, stamping
   `accepted_at` (now) and `release_tag` (the release tag being cut).
3. Is run by Main only after the owner has accepted the release — the same
   point other release memory already ties to `blerg-runner mark milestone
   --commit <sha>` at the tag — never automatically from a passing run.

This keeps "the run produced numbers" and "the numbers are now the thing
future releases are judged against" as two separate, owner-gated decisions,
consistent with this project's existing rule that release cuts happen with
explicit approval, not automatically on green.

## Report

A Markdown file, written next to the run's logs, handed to Main to publish
with `blerg-runner publish`. Structure:

- Header: release version, commit, the baseline release compared against,
  `seed_version`, **this run's own host fingerprint** (and, if it didn't
  match the baseline's, that fact in place of any comparison), and the
  overall verdict (clean / regressed / flagged-not-confirmed / inconclusive /
  host-mismatch).
- A summary table: one row per scenario — client-observed and, where it
  exists, server-reported median/p95, this release vs. last, percent change,
  this scenario's stored noise floor, verdict.
- One chart per scenario, as an inline Vega-Lite block (inline data only, no
  external resources, per the session's rich-output rules): a two-point
  comparison (last release vs. this release) for median and p95, client and
  server series shown separately where both exist.
- Per-scenario detail: exact N, discarded warm-up value, the 3 calibration
  medians and the noise floor derived from them, both this-release numbers,
  and which rule (if any) it tripped, including a rerun's result if one ran.
- `inconclusive` and `host-mismatch` scenarios/runs are called out separately
  at the top, since they change what the rest of the report means.

## Failure logging and cleanup (M8)

Every refusal and every failure path prints its cause to stderr **and** to a
structured log line in the run's log file before exiting — "why" is never
left to be inferred from a bare nonzero exit. Cleanup (dropping the three
databases, stopping the three servers, releasing the UI suite lock, removing
the pidfile) runs on **every** exit path, success or failure, via Go
`defer` plus a `signal.NotifyContext`-based handler for SIGINT/SIGTERM — not
only the happy path's explicit teardown call.

At startup, before the precondition checks, the tool looks for its own stale
leftovers from a crashed prior run:

- **Pidfile present, PID not alive**: stale — remove it and continue.
- **Pidfile present, PID alive**: a real concurrent run — refuse (this is the
  M6/H4-relevant "another heavy run" case, distinct from a crash).
- **`blerg_release_perf_*` databases already exist with no matching live
  pidfile**: stale from a crash — drop and recreate them rather than refusing;
  a half-seeded leftover database is cleaned up automatically, not treated as
  evidence of anything.

## Keeping the seed load out of the quiet-host check (M7)

Seeding (migrating three databases and inserting the dataset above) is itself
a burst of CPU/IO/checkpoint work; checking load average immediately
afterward would see the tool's *own* seeding, not host noise, and could
falsely refuse its own run. After seeding completes, the tool:

1. Runs `VACUUM ANALYZE` and `CHECKPOINT` on each of the three seeded
   databases, settling Postgres' own background autovacuum/checkpoint I/O
   from the bulk inserts instead of letting it bleed into the timed run.
2. Polls load average every 5 seconds, waiting for it to drop back under the
   precondition threshold (0.5 × logical CPUs) before starting the timed
   scenarios, with a bounded wait (10 minutes); if it never quiets down, the
   tool refuses with `"host did not quiet after seeding"` rather than
   measuring on top of its own settling work.
3. Seed time is itself logged and timed (`seed_seconds` in the run log) so a
   future slowdown in seeding — as opposed to in the app — is visible and not
   mistaken for scenario regression. This design does not commit to a
   specific expected duration; the first implementation run records it as the
   initial reference point.

The same load/lock re-check described under "Noise during the run" (Failure
rule) continues through the three repeated N-sets, so a host that gets noisy
*during* calibration or the rerun is caught too, not just between seeding and
the first sample.

## The one command

`make release-perf`, implemented as a new Go command
(`runner/cmd/release-perf`, module `runner`) — new tooling stays in Go, no new
Python or shell tool is introduced. Flow:

1. Clean up stale leftovers from a crashed prior run (M8); check preconditions
   (lock, load, memory, pidfile); refuse with a named, logged reason if any
   fail.
2. Take the UI suite lock and write the pidfile; install the signal-based
   cleanup handler.
3. Drop/recreate the three databases, migrate, seed via each service's real
   API plus bulk SQL for event volume (fixed epoch, fixed PRNG seed).
4. `VACUUM ANALYZE`/`CHECKPOINT`, then wait for the host to quiet (M7).
5. Build and start core, board and runner (server + daemon), each on an
   ephemeral port, isolated environment and data directories (M6); build and
   serve each frontend's production build.
6. Create the fixed test account via `/auth/setup`; sign in through the real
   login page with Playwright to get a session.
7. Run the full scenario set (frontend via Playwright against the real pages,
   backend timings taken from the same requests, client- and server-observed)
   **three times** — primary run plus two calibration repeats — rechecking
   host quiet before each.
8. Compute per-scenario median/p95/noise; load the official baseline for the
   previous release at this `seed_version` (if present); check host
   fingerprint; compare; rerun any flagged scenario once; write the candidate
   file and the Markdown report.
9. Tear everything down (defer-based, plus the signal handler for an
   interrupted run); exit with the documented code (0 clean, 1 regressed,
   2 no-verdict — inconclusive, host-mismatch, or refused-to-start).

No sleeps: every wait is on an explicit readiness signal (the servers'
existing startup/health readiness, as `runtime-smoke` already requires; the
app's own `performance.mark` readiness signals above for frontend scenarios)
or a bounded poll with a stated interval and timeout (load-average quieting),
never a fixed delay standing in for "probably done by now."

### The systemd unit (L9)

A single transient unit, created fresh per run rather than a unit file checked
into the repo (matching how `docs/pre-merge-measurements.md` describes the
existing gate running "in an external systemd unit, one heavy suite at a
time"):

```sh
systemd-run --user --scope \
  --unit="blerg-release-perf-$(date +%s)" \
  --property=CPUWeight=10 \
  --property=IOWeight=10 \
  --collect \
  env BLERG_TOOL_WORKERS="${BLERG_TOOL_WORKERS:-2}" \
      TEST_DATABASE_URL="$TEST_DATABASE_URL" \
  /path/to/release-perf --logs /path/to/new-evidence-dir
```

Corrected during implementation (the two things above read plausibly but
don't run): `--scope` already runs the command as the scope's own foreground
leader and blocks until it exits, so `--wait` (which only applies to
`--unit`/service-mode invocations) conflicts with it and `systemd-run`
refuses to start at all. And this `systemd-run`'s `CPUWeight` rejects the
literal value `idle` (`Invalid argument`) even though some systemd versions
accept it; a low numeric weight (`10`, out of the default 100) is the
portable way to ask the scheduler to yield.

- `--scope` + `--collect` give a transient unit that's automatically cleaned
  up from systemd's bookkeeping once it exits, with no unit file to maintain,
  and blocks in the foreground so the wrapping shell's own exit code is
  exactly the tool's documented 0/1/2 — nothing extra is needed to forward it.
- `--property=CPUWeight=10`/`IOWeight=10` ask the scheduler to favor
  anything else on the host rather than compete with it — belt-and-suspenders
  alongside the load-average precondition, not a replacement for it (a unit
  property can't refuse to start the way the precondition check does).
- Logs and the report go to an explicit `--logs` directory outside the unit's
  own journal, the same convention `scripts/pre-merge.sh --logs` already uses,
  so evidence survives after the transient unit is collected.

## Where this gets documented

- **`docs/pre-merge.md`**: a new short section, "Release performance check
  (BLG-453)," distinct from the main gate matrix, stating it is not part of
  `pre-merge.sh`, giving the `make release-perf` / `make release-perf-accept`
  commands and the systemd-run invocation above, and linking this design doc
  and the baseline directory.
- **`runner/skills/coordinating/SKILL.md`**: a line near its existing gate
  doctrine (today: "Main runs the integrated browser/performance gate only
  when that region changed... Hold merges during a gate... alone under the
  shared gate lock") adding that *before a release cut* Main also runs
  `make release-perf` once, reads its verdict, and treats a `regressed` or
  `inconclusive`/`host-mismatch` result as something to resolve before
  cutting; and that `make release-perf-accept` runs only after the owner has
  accepted the release, at the same point other release work is marked with
  `blerg-runner mark milestone --commit <sha>`.

## Open questions for Main

- Exact budget values for scenarios that don't yet have one (board tab,
  transcript open, search, blackboard, workspace switch) — this design
  records them in the baseline on first run but doesn't propose fixed budgets
  beyond the two that already exist (typing, Insights 7-day's server number).
  Main should decide whether any of these need a hard budget now or can start
  margin-and-noise-only.
- Whether `blerg_release_perf_*` run against the same local Postgres instance
  used for `TEST_DATABASE_URL`, or a separate instance — this design assumes
  the same instance (different database names), which means the check also
  refuses to run if that instance is under load from something else. If
  that's too coarse, a dedicated instance is a follow-up.
- Tripling run time for calibration (H4) makes this meaningfully slower than
  a single pass; Main should confirm that's acceptable for an occasional,
  near-release check, or say if 2 total runs (one calibration repeat) is
  preferred as a cheaper floor.
- A path local to Main's own session was named as a reference for "how
  Main runs gates as systemd units," but it isn't checked into this
  repository. The `systemd-run --scope` invocation above is this design's
  own proposal, reconstructed from `docs/pre-merge-measurements.md`'s description rather
  than copied from that script; Main should confirm it matches or replace it
  with the actual template.


## Cold-load diagnosis (BLG-492)

Set `RELEASE_PERF_DIAGNOSE=cold-load` when invoking `make release-perf` to print
Navigation Timing, script/fetch Resource Timing, paint, socket lifecycle,
initial-state, replay-complete and ready marks for the first counted sample of
each matrix. Every counted cold-load sample also retains these timings and
document response timings in `scenarios-N.json`, under `coldLoads`. Document
records keep only the path, status, top-level/frame distinction, response time
relative to the action, and request/response-start offsets; token fragments and
refresh query strings are excluded. This distinguishes a full-page auth bounce
from silent renewal in a frame. The report's client samples still use the app's
ready timestamp, rather than the harness's 25 ms polling observation time.

Blackboard setup closes the outer **Blackboard** tab when present, waits for its
removal, and discards cached views through the existing identity-change event.
Transcript setup waits for the switch-away render before discarding retained
views. Both wait for the resulting socket's initial state before starting the
clock; neither includes reconnect setup in the measured open. Setup failures
stop the run instead of silently timing a reselected view.

Runner cold loads now try silent renewal before the guarded full-page redirect.
The redirect limit and the fallback on failed renewal are unchanged. This avoids
booting the bundle twice and, after repeated reloads have spent the redirect
budget, avoids waiting five seconds for banner recovery despite an already
successful silent API renewal. See [the measurement report](../cold-load-performance.md).
