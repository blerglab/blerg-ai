# Local pre-merge verification

Run `make pre-merge` from a checkout with Go 1.26+, pinned golangci-lint,
Node dependencies, Chromium, Firefox, pytest and PowerShell installed.
Set `DATABASE_URL` and `TEST_DATABASE_URL` to a dedicated test administration
database whose user can create databases. The script refuses a full run without
both variables: database tests otherwise skip while reporting success.
Set `GOLANGCI_LINT` to the pinned binary if it is outside PATH.

For ordinary merge verification, give the change base:

```sh
scripts/pre-merge.sh --changed BASE --list  # inspect RUN/SKIP selection
scripts/pre-merge.sh --changed BASE         # run selected complete regions
```

`--changed BASE` enables region selection by default whenever that base is
provided. It compares BASE directly with the current checkout, including committed,
staged, unstaged, deleted, renamed (both paths) and untracked files. It does not
use a merge-base implicitly. Without a base, the default remains the complete
matrix. Selection is printed before execution. `summary.json` records every
excluded suite in `skipped_suites` with `outcome: skip` and a reason, without a
passing exit code. `complete` means all selected suites finished; `full_matrix`
remains false when any matrix entry was excluded.

| Changed paths | Complete suites selected (plus scrub and links) |
| --- | --- |
| `core/web/`, `board/web/`, `runner/frontend/` | That app's lint, types, all unit/browser/performance tests and build; all three image builds |
| `packages/chat/` | Chat, runner frontend and board web; all three image builds |
| `packages/settings/`, `brand/` | Every web app, including chat; all three image builds |
| `core/`, `board/`, `runner/` outside the frontend and CLI paths | That Go module's vet, tests and lint; lint-pin and gofmt on affected modules; runner also includes tracing, runtime smoke, races and wire suites |
| `core/Dockerfile`, `board/Dockerfile`, `runner/Dockerfile*` | All three image builds |
| `contracts/` | All four Go modules and their supporting checks |
| `runner/.claude/skills/session-messaging/` | CLI unittest and pytest |
| `install/desktop/` | Desktop script checks |
| Markdown files or `docs/` | Scrub and links only |
| Module/workspace manifests, lockfiles, `scripts/`, `contracts/cmd/image-build/`, `.github/`, or any unmapped input | Everything |

`package-lock.json` (root) and `core/web/package-lock.json` are the two
exceptions: when the only change to one is the `version` field of a workspace
entry (`board/web`, `packages/chat`, `runner/frontend`, or `core/web`'s own
root entry), `--changed` diffs the lockfile against the base and selects that
workspace's region instead of everything — a `@blerglab/chat` release bump no
longer forces the full matrix for a CSS-only fix. Any other lockfile change (a
dependency added, removed, upgraded or moved, an added/removed workspace, or a
diff that cannot be parsed as JSON) stays global, fail closed. `--list` and
`summary.json`'s `selection_reason` name the workspace and changed packages
when this applies.

The `core-image`, `board-image` and `runner-image` suites build the shipping
Dockerfiles from the repository root using the Go command in
`contracts/cmd/image-build`. Each uses a disposable Docker Buildx
container builder with one active build step, loads and removes its test image,
and removes the builder and its cache volume. No layer cache is exported.
They never prune shared Docker state. Docker or daemon unavailability records
a named `skip` with elapsed time and a nonzero gate result, never a pass.
Buildx is required; build, builder setup and cleanup failures fail the suite.
Image suites have a one-hour deadline and use the normal per-suite timing logs.

Frontend selection always uses complete suites and the final browser phase,
including every configured width and Firefox. It never selects individual test
files. This retains coverage for interactions such as ExplorerDock. Multiple
regions select their union. With no changed paths, scrub and links still run.
Markdown outside gate/CI tooling is documentation even within a module.
`--release` always selects everything, ignoring region and `--suite` filters,
and retains three daemon race passes. Do not use it for a focused race check.

The older `--changed-since` mode below remains available for focused development
checks; it cannot be combined with `--changed` and does not replace region gate
verification.

### Historical selection examples (2026-11-01)

These are actual path lists from `git diff-tree --no-commit-id --name-only -r
COMMIT`, passed to `select_changed(matrix(...), paths)`. No checkout or product
suite execution was needed. Each comparison uses only that commit's delta;
`--changed COMMIT^` on today's checkout would include all subsequent changes.
There are 44 matrix entries after splitting desktop, scrub and links from the
remaining script checks.

| Commit | Selected suites | Skipped | Estimated suite time avoided |
| --- | --- | ---: | ---: |
| `24f215ef` (frontend only) | runner lint, types, unit, browser, performance, build; scrub; links | 36 | 2,899.297 s (48.32 min) |
| `325ff375` (Go only) | host-tracing-preflight; runner vet, Go, lint; runtime-smoke-local; daemon-race; runtime-race; both persistent-shell-wire suites; lint-pin; gofmt; scrub; links | 31 | 1,259.223 s (20.99 min) |
| `58e5b801` (docs only) | scrub; links | 42 | 3,429.640 s (57.16 min) |

Savings sum the skipped suites' **After seconds** from the 2026-10-31
[split baseline measurement table](pre-merge-measurements.md#per-suite-evidence).
They are estimates of avoided suite work, not new measured wall-clock speedups:
parallelism, cache state and host load affect elapsed time. The old combined
`scripts` timing receives no savings credit because its desktop/scrub/link
portions lack separate measurements. The newer runtime smoke likewise has no
baseline timing here. No product suites were run for this selection change.

During a round, use `scripts/pre-merge.sh --changed-since COMMIT` for touched
verification. `COMMIT` is the base of the change; committed changes, the index,
working files and untracked files are included. Add `--list` to inspect suite
names before running. This mode always records `full_matrix: false`.

Go selection includes changed packages and transitive importers, including test
imports across the four modules. Fixtures and embedded files belong to their
nearest package. Module manifests or an unrecognized package directory select
the whole module. Frontend source changes use `vitest related --run`, including
the chat package's consumers. Deleted sources, styles, setup files and app
configuration select the app's complete tests. Unknown shared build inputs
select all ordinary tests. Affected modules retain full-module lint; affected
apps retain lint, type checking and builds. Changes affecting Runner frontend also
retain its complete serial browser performance suite, including every viewport;
backend-only changes omit it. Release race repetitions remain in the final gate;
select race suites explicitly when needed.

Touched frontend runs set `VITE_BLERG_TEST_PHASE=touched`: runner tests use
Chromium and the first width in matrices wrapped by `browserWidths`. Standalone
fixed-viewport tests still run as written. Default/final runs retain every width
and the existing Firefox coverage. The final gate explicitly sets the phase to
`final`, overriding an inherited touched setting. For a manual selected-file
round, set this variable on the Vitest command; omit it for full verification.

Focused `--suite` selections run only the named suites: a nontracing selection
does not run the tracing preflight. On a restricted Linux session, separately
run tracing-dependent suites on a host that permits tracing and retain both
reports. Combining focused reports is measurement evidence, not a single full
gate pass. A region report establishes only its selected coverage; release verification always requires the complete matrix.

Vitest scheduling metadata (`results.json`) is shared between worktrees in the
repository's user cache under `blerg/vitest-results`. `XDG_CACHE_HOME` selects
the cache parent. Local scheduling results take precedence. Transformed code
and Vite dependency caches stay private to the checkout; every selected test
still executes. Cache access failures never change test outcomes. Only complete
browser/performance suites acquire the UI lock; selected files, related tests
and unit projects do not.

On Linux, a tracing preflight must pass before the matrix starts. A sandbox
that denies `PTRACE_TRACEME` cannot run the host-shell security tests; the gate
reports an incomplete failure instead of skipping those tests. A denied probe is
an environment limitation, not a test pass; focused runs do not establish that
the full gate passes.

The matrix includes all four Go modules (vet and tests), one daemon race
pass (three with `--release`; #290 separately requires a five-pass proof),
the ownershell/shellruntime/processcontainment race tests, all four web
packages (lint, type checks, tests and builds), CLI unittest and pytest, and the
remaining `make check-scripts` checks. CLI tests run once, with pytest's slowest
30 timings recorded. Runner browser performance runs after other browser workers
have exited.

`runner-go`'s plain `./...` run excludes `internal/daemon`, `internal/ownershell`,
`internal/shellruntime` and `internal/processcontainment` whenever `daemon-race`
and `runtime-race` are also selected (the full matrix and every `--changed`
region that includes `runner-go`, since they share its region): the race build
runs the same tests, plus race detection, so the plain pass added build time
without adding coverage. A `--suite` or `--changed-since` selection that excludes
either race suite keeps the full package list, since nothing else would cover
those packages.

The Linux ptrace gate also runs `persistent-shell-wire-go` and
`persistent-shell-wire-race` with `-tags=persistent_shell_test`, `-count=1`, and
an exact selector for `TestPersistentShellRealWireReconnectAndWithdrawal`.
Both suites require a JSON pass for that test exactly once: missing or skipped
coverage fails even if Go exits successfully. They cover consent, allocation
refusal retry, terminal I/O, 31 minutes of virtual absence, same-shell reconnect,
and withdrawal with a real server websocket, daemon Service, and helper. The
clock seam remains excluded from production builds.

The runner entry of CI's `go-module` job invokes these same two suites on
`ubuntu-latest`, with its PostgreSQL service, `TEST_DATABASE_URL`, and `GOWORK=off`.
The board entry does not run them. CI needs no outside-session wrapper because
its host permits ptrace. Ordinary `make test` does not enable this test-only tag;
use `make pre-merge` for the complete local gate, or focus on both suites with
`scripts/pre-merge.sh --suite persistent-shell-wire-go --suite persistent-shell-wire-race`.
A focused run still requires a ptrace-capable Linux host and a configured test
database; it never counts as a full matrix.

`BLERG_TOOL_WORKERS` sets the total CPU budget (fallback two). Each suite
uses at most six workers, and Go test parallelism remains four. The scheduler
starts at most `floor(total workers / suite workers)` independent suites,
further limited by available memory: reserve 2 GiB for the session and 4 GiB
per concurrent suite. Linux available memory is bounded by cgroup memory
headroom when present; unknown memory falls back to one suite. For example,
32 workers with 31 GiB available permits five suites; a two-worker session
runs one. The summary records concurrency and detected memory headroom.
Core and board Go tests retain their measured two-package cap.

The longest measured suites start first and free slots refill immediately.
The tracing preflight completes before any other suite starts. Tools in the
same frontend directory run separately to protect build output and scheduling
caches. The chat build also waits for frontend consumers reading its output.
Full UI suites and Go lint processes each retain their shared lock.
Runner performance runs alone after every ordinary suite exits. On resource
failure the scheduler admits no more suites and collects results from those
already running. A Ctrl-C kills active suite process groups and preserves an
incomplete report. The summary and final table retain matrix order.

The merge gate runs daemon race coverage once, explicitly uncached. Use
`scripts/pre-merge.sh --release` for the release matrix. The existing
`make test-daemon-race` command passes `--release --suite daemon-race`; because
release ignores suite filters, that command now also runs the complete matrix
with three daemon race passes. Existing Go, lint, npm and browser caches are retained. Ordinary
Go tests use normal cache invalidation; race repetitions and tagged wire timing
proofs remain explicitly uncached.

Every suite has a hard deadline: 90 minutes for Go and race suites, 30 minutes
for runner browser and performance suites, and 15 minutes for all others.
On timeout the gate kills the suite process group, names the suite in its log
and summary, records exit 124, and continues with the remaining suites. Suite
stdin is closed so fixture helpers cannot wait for terminal input.

Every suite writes its full output to a separate log. The final table shows exit
status and elapsed seconds, and `summary.json` records machine-readable results
and total elapsed time. A failure does not hide later suite results. A resource
exhaustion or containment diagnostic in a failed suite stops the matrix before
starting another suite, records the UTC finish time, and marks it incomplete.
A suite killed with SIGKILL or exit 137 also stops the gate, even with no output.
Go JSON kill diagnostics from tests that passed are treated as expected child
cleanup. Explicit lifecycle records with `driver_kill=true shutdown=true` also
prove an owned shutdown, even when a late log is attached to a failing test.

Every run also appends a telemetry record (per-suite seconds, test counts, host
load and the slowest 30 tests per suite) to `.superpowers/gate-runs.jsonl`, keyed
by hostname via `BLERG_GATE_TELEMETRY` to point elsewhere. Before writing
`summary.json`, the gate compares this run against the same host's last ten
records for each suite and prints a `WARN` line (also recorded in
`summary.json`'s `growth_warnings`) when a suite's time grows more than 25% over
its rolling median. It tells added coverage apart from a real slowdown: if the
test count grew by a similar margin, the warning says so instead of calling it a
regression; if an individual test's own time grew past its rolling median, that
is flagged even when its suite's total did not. A suite needs at least three
prior same-host runs before it is judged at all, so a new suite or a fresh
history file never produces a false warning.
The exact bounded task-limit notice from a passing Go test is also expected
containment output, provided no repetition of that test failed. Unattributed
or unproven task-limit notices and other concrete resource diagnostics still
stop the gate. Generic
app-server startup failures alone do not prove resource exhaustion. Failure
records and console output name data races, failed tests, or the detected
resource cause.
The gate does not retry failed suites. Logs can contain local paths and test
diagnostics; inspect and scrub them before sharing.

Use `scripts/pre-merge.sh --logs /path/to/new-directory` to choose the output
location, `--list` to inspect the matrix, or repeat `--suite NAME` for focused
work. A focused run is never reported as a full matrix. Do not reuse an output
directory: preserving earlier logs makes comparisons auditable. `GO` selects a
Go binary; by default the script prefers `$HOME/go/bin/go`.

The Go JSON logs retain test and subtest events; each Go suite summary includes
pass/fail/skip event counts, its 30 slowest tests and `cached_packages`.
Cached test events retain their original timings; the suite elapsed time is the
current wall time. Go lint runs have individual
logs and timings for all four modules. The gate keeps the normal lint caches and
always checks every module; it does not infer reduced coverage from a diff.

See the [measurement record](pre-merge-measurements.md) for per-suite timings,
exact commands and counts, the five-pass daemon proof, and two successful full
gate runs.

## Runtime smokes (BLG-31)

From `runner/`, build the Go release command and run all three runtimes:

```sh
GOWORK=off go build -o /tmp/blerg-runtime-smoke ./cmd/runtime-smoke
/tmp/blerg-runtime-smoke -logs /path/to/new-private-evidence
```

The command builds this checkout's daemon once, runs named Go acceptance tests
serially, and writes private JSONL logs and `summary.json`. Go test caching is
explicitly disabled here because a release smoke must execute live. It honors
`BLERG_TOOL_WORKERS` (default two, capped at six like other gate suites). Exit 0 means every selected runtime passed,
1 means failure/missing test receipt, and 2 means incomplete coverage because a
selected test skipped. Exit 3 means the cluster passed with RBAC not enforced. A skipped test is printed as SKIP with its exact test
name; its log states the missing prerequisite. Package-level success, missing
or duplicate receipts, and unrelated test passes cannot satisfy a runtime.
`-runtimes host,sandbox` selects just the local checks.

Requirements: Linux, Go, Git, tmux, ptrace, `TEST_DATABASE_URL` with CREATEDB,
Docker and an already built local sandbox image (`blerg-runner-sandbox:latest`,
or `BLERG_RUNNER_SANDBOX_IMAGE`). No image is pulled automatically. Missing
Docker/image/database produces a named skip, hence an incomplete release run.
The local tests start the shipped daemon binary, cross its real websocket and
public HTTP session/preview/message/transcript/stop routes, and require a reply
after the acknowledged client message, a successful turn, ended lifecycle and
a durable runtime final-stop receipt. Core identity is a fixture and server
handlers run in the test process, as in the existing cluster smoke; this does
not certify deployed Core login or browser behavior.

The existing scripted Claude wire fixture is reused; the BLG-342 process-shape
stub is a workload rather than an API-launchable engine. Host and sandbox tests
use private HOME/state/repositories and fixture credentials, never an engine
login or model endpoint. Sandbox uses real Docker and an owned image with this
checkout's helpers and scripted engine; owned fixture resources are removed.

For a release controller, run all three with the existing isolated Kubernetes
controller from the same Go command:

```sh
/tmp/blerg-runtime-smoke -logs /path/to/new-private-evidence \
  -cluster-context "$SMOKE_CONTEXT" -cluster-namespace "$SMOKE_NAMESPACE" \
  -cluster-values "$SMOKE_VALUES"
```

Prepare disposable images from this checkout and controller values following
[the cluster runbook](../install/k8s/acceptance/RUNBOOK.md). Python 3 and kubectl
are needed only for this existing controller path. The controller retains its
namespace ownership, placement, RBAC and exact test-receipt checks. It leaves
cluster state in `cluster-state` for inspection and explicit runbook teardown;
there is no automatic namespace deletion. `PASS-RBAC-NOT-ENFORCED` remains a
separate outcome if the controller reports that known authorization limitation;
it is never relabeled plain PASS and exits 3. The controller's other documented storage
and Core/provider acceptance limits still apply.

Without controller flags, `TestAcceptanceClusterSmoke` runs directly. It skips
unless `P6_ACCEPTANCE_SMOKE=1`; enabling it requires the controller's in-cluster
namespace, image, repository parameters, fixture TLS/Service and permissions.
Setting the switch alone does not provision those prerequisites. The cluster
smoke now also checks the assistant reply through the public transcript route.
No cluster execution was available in the builder environment.

A tiny paid host Claude check is separately opt-in: set
`BLERG_RUNTIME_SMOKE_REAL=1` and provide `ANTHROPIC_API_KEY` privately, then run
`-runtimes real`. It uses the installed Claude CLI, an isolated HOME, and one
request to reply `BLERG_SMOKE_OK` without tools or file changes. It is off by
default and is not part of pre-merge. Real-engine acceptance was not run here.

`runtime-smoke-local` is included in `scripts/pre_merge.py`: host and sandbox
completed in 6.70 s and 30.56 s respectively; the initial command including Go
build/test startup and the cluster skip took about 81 s with warm build caches.
The wired pre-merge suite then passed in 61.8 s. Cold builds and sandbox
base-image preparation can take longer. The cluster
controller is release-only because its provisioning time is unmeasured here
and its test deadline alone is 12 minutes. A full pre-merge run now also needs
Docker and the local sandbox base image; a skip fails this suite as incomplete.
Evidence is nested under `runtime-smoke-local-evidence` in the gate log directory.

`TestAcceptanceHostSmoke` needs its own ptrace tracer. When running inside a
traced session, use the installation's external test launcher for that test
or the command containing it. The local
sandbox smoke is included in the same serial command; this exception does not
apply to ordinary command unit tests, lint or builds.

Local smoke budgets allow three minutes for the daemon build and five minutes
per Go test (plus 15 seconds each for startup), within the 15-minute suite.
Before sandbox execution, containers labeled `blerg.test.p6` older than one hour
are swept to recover resources left by killed test processes.

## Release performance check (BLG-453)

A separate, occasional tool, not part of `pre-merge.sh` and not run on every
gate: it runs once near a release cut, against a seeded dataset shaped like
real usage, comparing this release's timings to the last release's recorded
baseline. See [the design](design/release-performance.md) for the full
scenario list, dataset, statistics, baseline format and comparison rule.

```sh
TEST_DATABASE_URL=postgres://... systemd-run --user --scope \
  --unit="blerg-release-perf-$(date +%s)" \
  --property=CPUWeight=10 --property=IOWeight=10 --collect \
  env TEST_DATABASE_URL="$TEST_DATABASE_URL" \
  /path/to/release-perf --logs /path/to/new-evidence-dir \
  --version 0.6.0 [--baseline docs/performance-baselines/0.5.4.json]
```

It refuses to start (exit 2, "no verdict") when the UI suite lock
(`scripts/ui_suite_lock.py`'s lock — the same one `pre-merge.sh`'s
browser/performance suites take) is held, when load average or available
memory fail the quiet-host check, or when a requested port or data directory
collides with something already live. A refusal is the correct outcome on a
busy host, not a result to work around.

A clean run writes a candidate baseline
(`docs/performance-baselines/<version>-candidate.json`) and a Markdown report
with a chart per scenario; it never overwrites the official, committed
baseline by itself. Promotion happens only after the owner accepts the
release:

```sh
release-perf-accept --candidate docs/performance-baselines/0.6.0-candidate.json \
  --out docs/performance-baselines/0.6.0.json --release-tag v0.6.0
```

First build (this commit) implements the refusal/isolation/Server-Timing
primitives, the seeded dataset, calibration and noise, baseline
compare/accept, and the report generator, each with its own tests (see
`runner/internal/releaseperf`). The full three-service (core, board, runner)
Playwright-driven run is implemented but not yet verified end to end on a
quiet host — this machine was busy with other work throughout; the first
official run happens before the 0.6.0 cut.

## Server sharding and reproducible timing (BLG-298)

`make test` uses the gate's eight Go vet/test suites, retaining all four modules.
It shares the worker budget, normal Go cache policy, logs and failure receipts.
A selected explicit full-module vet check replaces `go test`'s duplicate implicit
vet subset. Vet remains a required, separately reported result. A test-only
selection retains implicit vet.

With two or more workers, `runner-go` discovers all selected packages and the
server's top-level tests, examples and fuzz targets without running their bodies.
Other selected packages run first with the suite's worker budget. Then up to four
server processes run disjoint, anchored selectors; their total `GOMAXPROCS` stays
within that budget. Every subtest and fuzz seed stays under its original parent.
Discovery errors or an empty server inventory fail the suite. Focused `-run`
commands retain their selector. Race and tagged wire suites are unchanged.
Normal Go cache invalidation applies to each stable shard; no `-count=1` is added.

To compare two committed revisions on a quiet, ptrace-capable machine:

```sh
GOWORK=off go run scripts/verification/cmd/gate-timing/main.go \
  BASE HEAD --output /tmp/blerg-gate-comparison
```

Configure both database variables privately, plus `BLERG_TOOL_WORKERS` and any
nondefault `GO`/`GOLANGCI_LINT`. The script retains separate detached worktrees,
installs locked Node dependencies, and measures the complete `pre_merge.py` gate
and `make test` sequentially in each revision. Setup is outside measured wall
time. `--mode gate` or `--mode make` measures just one command. A benchmark lock
prevents concurrent benchmark scripts; load samples disclose unrelated host work.
Normal shared caches remain enabled, so the before-first ordering can warm the
after run. These are routine verification measurements, not cold-cache results.

`results.json` is updated after each command, even a failed one. Named Go and
Vitest receipts are retained separately. Comparison rejects missing names,
changed repetition counts, newly skipped tests, and missing gate suites; added
regression tests are listed. Legacy `make test` receives only the `-json` reporter
flag to expose its named receipts. CLI and shell checks retain their gate logs
and suite results; they do not all emit individual machine-readable test names.
Failed or incomplete gates never establish a successful full-matrix speedup.
The script keeps worktrees and logs for inspection and never pushes.
