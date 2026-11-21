# Pre-merge measurement record (#298 and #290)

## 2026-10-31 parallel scheduler focused comparison (#298)

The parallel scheduler is implemented; **the approximately 20-minute full-gate
target remains unverified**. Builder instructions reserve the full browser and
performance suites and integrated gate for Main. This round ran only focused
checks in the session, with no outside-session wrapper. It did not repeat the
61.41-minute full baseline above or claim a new full-matrix pass.

A real six-suite warm-cache comparison used the original serial scheduler at
`8f580ca357ae1e23c55b2036dd4dc32980de5b36` and the updated scheduler:

| Suite | Before, seconds | After, seconds | Result in both |
| --- | ---: | ---: | --- |
| contracts-vet | 0.368 | 0.372 | pass |
| contracts-go | 0.420 | 0.424 | pass |
| core-vet | 0.571 | 0.537 | pass |
| board-vet | 0.569 | 0.569 | pass |
| runner-vet | 1.372 | 1.170 | pass |
| lint-pin | 0.319 | 0.256 | pass |
| Wall time | 3.631 | 1.548 | 6/6 suites |

Both runs replayed 193 passing contract test events from the same 16 cached
packages, with zero failures or skips. These are cached events, not 193 fresh
test executions. The after run admitted four concurrent suites, six workers
each, with about 19.5 GiB available memory. This small warm comparison proves
real overlap; it does not predict full-gate runtime or peak memory. An initial
cache-warming pair took 24.9 and 2.4 seconds and is excluded from this comparison.

Exact commands (set `GOLANGCI_LINT` to the installed pinned binary first):

```sh
export PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=scripts BLERG_TOOL_WORKERS=32 GOWORK=off
# Before: execute the baseline source with imports and ROOT from this checkout.
python3 -c 'import subprocess; __file__="scripts/pre_merge.py"; exec(compile(subprocess.check_output(["git","show","8f580ca357ae1e23c55b2036dd4dc32980de5b36:scripts/pre_merge.py"]), __file__, "exec"))' --suite contracts-vet --suite contracts-go --suite core-vet --suite board-vet --suite runner-vet --suite lint-pin --logs /tmp/blg298-before-warm
# After:
scripts/pre-merge.sh --suite contracts-vet --suite contracts-go --suite core-vet --suite board-vet --suite runner-vet --suite lint-pin --logs /tmp/blg298-after-warm
```

Focused regression commands:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p pre_merge_test.py
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -p 'test_*.py'
./scripts/scrub.sh
./scripts/check_links.sh
git diff --check
```

The first regression run failed as expected (30 tests, two failures and three
errors). The updated regressions pass: 37 orchestration tests and 29 script tests,
zero failures. They cover real concurrent execution, slot refill, frontend cache
and lint locks, chat build consumers, cgroup memory limits, performance/preflight barriers, stop
admission and drain, process-group cancellation, and release flag wiring.
No Go or frontend source changed, so no UI suite or frontend build was run.
Python remains here as explicitly requested; no Python files were added.

Main still needs one integrated run with both database settings, a tracing-capable
host, installed frontend dependencies, and the pinned linter. Retain its
`summary.json` and memory observations, compare against the full baseline, and
investigate server-package splitting only if runner-go remains the bottleneck.


## 2026-10-31 split baseline and optimization (#298)

**The 15-minute target is not met.** All 40 selected matrix entries passed in the
completed after profile: **61.41 minutes** (25.67 vanilla +
35.74 tracing). The raw before profile took 74.87 minutes and failed.
Replacing its interrupted script-lock measurement with the separately measured
repaired script check gives **72.24 minutes**, a reconstructed comparison;
the observed after profile is 15.0% shorter. This is operational evidence,
not a controlled cold-cache speedup or a single `full_matrix: true` run.

The owner explicitly authorized `--list`/`--suite` to split execution after the
initial tracing denial recorded below. All 34 nontracing entries ran vanilla
inside this session. Only `runner-go`, `daemon-race`, `runtime-race`,
`persistent-shell-wire-go`, and `persistent-shell-wire-race`, plus their tracing
preflight, ran through `/tmp/cx/outside.sh "$REPO" <command>`. No other suite
used that wrapper. An explicit suite selection already excludes the preflight
unless named; no nontracing check was blocked or disabled.

Runs used Go 1.26.9, `GOWORK=off`, both required database variables configured,
pinned golangci-lint 2.13.1, installed frontend dependencies and PowerShell.
Before used two tool workers; after used four. Both used `-parallel=4` for
ordinary Go tests and retained the existing race repetition counts. Each segment
ran under `BLERG_UI_SUITE_LOCK="$gate_lock" flock "$gate_lock"`; only one heavy
suite ran at a time. The host has 32 cores and about 31 GiB RAM. It was not fully
idle: sampled one-minute load reached 34.43 before and 29.71 after. The available
memory minimum observed was 12.06 GiB before and 12.68 GiB during the after
vanilla segment, including browsers. Sampling began partway through the baseline
and had a gap during the tracing restart; these are observed minima, not complete
peak-memory measurements. No process/memory exhaustion diagnostic occurred.

### Changes and coverage

- Ordinary Go tests reuse Go's normal cache; race repetitions and wire timing
  proofs remain fresh. The after tracing restart reused 37 successfully completed
  runner packages from an interrupted attempt. Replayed Go events retain their
  original durations and are identified as cached; they are not fresh runtime.
- `--changed-since COMMIT` selects affected Go packages and transitive importers
  across modules, related frontend tests, and required lint/types/build checks.
  Fixtures, deleted files, manifests and unknown shared inputs have conservative
  fallbacks. It never reports a full gate and never substitutes for the final gate.
- Vitest shares validated scheduling `results.json` hints between worktrees.
  Every selected test still runs; transformed code stays local. Worker budgets
  honor `BLERG_TOOL_WORKERS` up to six, with a fallback of two.
- Touched browser runs use representative widths and Chromium; default/final
  runs preserve all existing widths, including 390 and 3840 px, and Firefox.
  The final gate forces the final phase even if the caller inherited touched.
- Removed 13 duplicate browser push lifecycle cases whose exact mocked cases
  already run in unit tests. Added four width-helper cases. No assertion or
  performance threshold was weakened, and packages/chat code was unchanged.
- Fixed the Main dock label for person workspaces. Updated stale permission and
  recovery expectations to current production contracts; the denied archive check
  also verifies that the ticket remains unarchived. Browser checks now follow
  actual dock focus, scope announcements correctly and await image readiness.

### Failures surfaced and fixed

The baseline had seven runner unit failures, 19 browser failures and four Go
failure events (one is an aggregate parent). The script suite waited on its own
inherited lock; only that stuck unittest child was interrupted at
2026-10-31T22:27:38Z. Its original 416.803-second failed result remains evidence;
its repaired standalone two-worker run passed in 259.025 seconds. The comparison
above substitutes only that duration, without treating the original gate as green.

The first after tracing attempt exposed a new harness defect: forwarding the
gate control descriptor into Go violated `TestShimIsInvisibleToTheCommand` in
`own_state` and `changed_state`. It was stopped at 2026-10-31T23:48:10Z after
594.7 seconds. Go commands now close that descriptor, including custom Go binary
names; nested non-Go gate controls retain it. The exact kernel assertion passed
in both normal and fresh race focused runs (three events each), then all six
tracing entries passed on restart. This discarded attempt is additional effort,
not part of the completed after profile. Workers and assertions stayed intact.

Synthetic orchestration tests now record to a temporary telemetry file instead
of polluting real gate history. Figures here come from complete segment summary
files and full per-suite logs, not the shared history or a tail of test output.

### Per-suite evidence

Counts are **pass/fail/skip**. Go counts include parent/subtest events and race
repetitions; CLI unittest counts are top-level tests. A dash means a command
check without a test-case count. Baseline performance/CLI counts were read from
complete logs because their older reporter did not supply structured counts.
The scripts row retains its original failed baseline; the repaired timing is
shown above. All after rows passed.

| Suite | Before seconds | Before P/F/S | After seconds | After P/F/S |
| --- | ---: | --- | ---: | --- |
| `contracts-vet` | 0.787 | — | 0.335 | — |
| `contracts-go` | 6.270 | 193/0/0 | 3.308 | 193/0/0 |
| `core-vet` | 5.831 | — | 0.571 | — |
| `core-go` | 97.758 | 768/0/6 | 129.165 | 768/0/6 |
| `board-vet` | 7.190 | — | 0.515 | — |
| `board-go` | 100.820 | 836/0/0 | 135.635 | 836/0/0 |
| `runner-vet` | 25.728 | — | 4.067 | — |
| `lint-pin` | 0.191 | — | 0.225 | — |
| `gofmt` | 1.788 | — | 1.016 | — |
| `contracts-go-lint` | 9.112 | — | 1.116 | — |
| `core-go-lint` | 16.890 | — | 1.371 | — |
| `board-go-lint` | 18.019 | — | 1.355 | — |
| `runner-go-lint` | 105.248 | — | 21.293 | — |
| `core-lint` | 9.762 | — | 9.550 | — |
| `core-types` | 4.389 | — | 4.271 | — |
| `core-tests` | 15.511 | 240/0/0 | 8.483 | 240/0/0 |
| `core-build` | 5.035 | — | 5.024 | — |
| `board-lint` | 17.371 | — | 17.673 | — |
| `board-types` | 11.174 | — | 10.777 | — |
| `board-tests` | 36.946 | 305/0/0 | 23.289 | 305/0/0 |
| `board-build` | 16.080 | — | 13.972 | — |
| `chat-lint` | 24.465 | — | 23.200 | — |
| `chat-types` | 13.080 | — | 12.807 | — |
| `chat-tests` | 189.467 | 2742/0/3 | 100.285 | 2742/0/3 |
| `chat-build` | 10.331 | — | 12.185 | — |
| `runner-lint` | 35.589 | — | 37.197 | — |
| `runner-types` | 0.474 | — | 0.462 | — |
| `runner-unit` | 213.985 (failed) | 2609/7/15 | 118.258 | 2620/0/15 |
| `runner-browser` | 433.662 (failed) | 1699/19/0 | 257.743 | 1705/0/0 |
| `runner-performance` | 93.060 | 31/0/0 | 87.621 | 31/0/0 |
| `runner-build` | 29.681 | — | 29.062 | — |
| `cli-unittest` | 199.933 | 420/0/0 | 193.801 | 420/0/0 |
| `cli-pytest` | 20.902 | 122/0/0 | 20.192 | 122/0/0 |
| `scripts` | 416.803 (failed) | — | 254.226 | — |
| `host-tracing-preflight` | 0.015 | — | 0.015 | — |
| `runner-go` | 1030.548 (failed) | 8517/4/109 | 1072.735 | 8521/0/109 |
| `daemon-race` | 827.879 | 7104/0/276 | 757.712 | 7104/0/276 |
| `runtime-race` | 348.038 | 762/0/5 | 304.225 | 762/0/5 |
| `persistent-shell-wire-go` | 37.767 | 1/0/0 | 3.659 | 1/0/0 |
| `persistent-shell-wire-race` | 52.194 | 1/0/0 | 5.470 | 1/0/0 |

### Reproduction commands

Set `GO` to the required Go binary and `GOLANGCI_LINT` to the pinned binary.
Database values are intentionally omitted. To reproduce the authorized split:

```sh
scripts/pre-merge.sh --list
# Vanilla: repeat --suite for every listed name except the six tracing entries.
BLERG_TOOL_WORKERS=4 BLERG_UI_SUITE_LOCK="$gate_lock" flock "$gate_lock" \
  scripts/pre-merge.sh --logs "$vanilla_logs" \
  --suite contracts-vet --suite contracts-go --suite core-vet \
  --suite core-go --suite board-vet --suite board-go \
  --suite runner-vet --suite lint-pin --suite gofmt \
  --suite contracts-go-lint --suite core-go-lint --suite board-go-lint \
  --suite runner-go-lint --suite core-lint --suite core-types \
  --suite core-tests --suite core-build --suite board-lint \
  --suite board-types --suite board-tests --suite board-build \
  --suite chat-lint --suite chat-types --suite chat-tests \
  --suite chat-build --suite runner-lint --suite runner-types \
  --suite runner-unit --suite runner-browser --suite runner-performance \
  --suite runner-build --suite cli-unittest --suite cli-pytest \
  --suite scripts

# Tracing only, preserving the configured environment:
/tmp/cx/outside.sh "$REPO" env BLERG_TOOL_WORKERS=4 \
  BLERG_UI_SUITE_LOCK="$gate_lock" DATABASE_URL="$DATABASE_URL" \
  TEST_DATABASE_URL="$TEST_DATABASE_URL" GOLANGCI_LINT="$GOLANGCI_LINT" \
  PATH="$PATH" flock "$gate_lock" scripts/pre-merge.sh --logs "$trace_logs" \
  --suite host-tracing-preflight --suite runner-go --suite daemon-race \
  --suite runtime-race --suite persistent-shell-wire-go \
  --suite persistent-shell-wire-race
```

For the before profile, the captured matrix used `-p=2`, worker flags of two and
`-count=1` on ordinary Go tests. The following after command inventory records
every suite command from that completed profile, before the two-package tuning
below; invoke each in its listed directory. `GO` and the lint
configuration path are portable placeholders. The runner adds JSON/retry
reporters to Vitest commands and sets worker/environment limits automatically.
Focused frontend commands below also used `--reporter=default --reporter=json`
and `--outputFile` pointing to a separate JSON file for complete counts.

| Suite | Directory | After command |
| --- | --- | --- |
| `host-tracing-preflight` | `.` | `scripts/pre-merge.sh --suite host-tracing-preflight` |
| `contracts-vet` | `contracts` | `$GO vet -p=4 ./...` |
| `contracts-go` | `contracts` | `$GO test -json -p=4 -parallel=4 -timeout=45m ./...` |
| `core-vet` | `core` | `$GO vet -p=4 ./...` |
| `core-go` | `core` | `$GO test -json -p=4 -parallel=4 -timeout=45m ./...` |
| `board-vet` | `board` | `$GO vet -p=4 ./...` |
| `board-go` | `board` | `$GO test -json -p=4 -parallel=4 -timeout=45m ./...` |
| `runner-vet` | `runner` | `$GO vet -p=4 ./...` |
| `runner-go` | `runner` | `$GO test -json -p=4 -parallel=4 -timeout=45m ./...` |
| `daemon-race` | `runner` | `$GO test -json -race -count=3 -p=4 -parallel=4 -timeout=45m ./internal/daemon` |
| `runtime-race` | `runner` | `$GO test -json -race -count=1 -p=4 -parallel=4 -timeout=30m ./internal/ownershell ./internal/shellruntime ./internal/processcontainment` |
| `persistent-shell-wire-go` | `runner` | `$GO test -json -count=1 -tags=persistent_shell_test -p=4 -parallel=1 -timeout=5m '-run=^TestPersistentShellRealWireReconnectAndWithdrawal$' ./internal/server` |
| `persistent-shell-wire-race` | `runner` | `$GO test -json -race -count=1 -tags=persistent_shell_test -p=4 -parallel=1 -timeout=5m '-run=^TestPersistentShellRealWireReconnectAndWithdrawal$' ./internal/server` |
| `lint-pin` | `.` | `./scripts/check_lint_pin.sh $GOLANGCI_LINT` |
| `gofmt` | `.` | `scripts/pre-merge.sh --suite gofmt` |
| `contracts-go-lint` | `contracts` | `$GOLANGCI_LINT run --concurrency=4 "--config=$REPO/.golangci.yml" ./...` |
| `core-go-lint` | `core` | `$GOLANGCI_LINT run --concurrency=4 "--config=$REPO/.golangci.yml" ./...` |
| `board-go-lint` | `board` | `$GOLANGCI_LINT run --concurrency=4 "--config=$REPO/.golangci.yml" ./...` |
| `runner-go-lint` | `runner` | `$GOLANGCI_LINT run --concurrency=4 "--config=$REPO/.golangci.yml" ./...` |
| `core-lint` | `core/web` | `npm run lint` |
| `core-types` | `core/web` | `npx --no-install tsc --noEmit` |
| `core-tests` | `core/web` | `npm test -- --maxWorkers=4` |
| `core-build` | `core/web` | `npm run build` |
| `board-lint` | `board/web` | `npm run lint` |
| `board-types` | `board/web` | `npx --no-install tsc --noEmit` |
| `board-tests` | `board/web` | `npm test -- --maxWorkers=4` |
| `board-build` | `board/web` | `npm run build` |
| `chat-lint` | `packages/chat` | `npm run lint` |
| `chat-types` | `packages/chat` | `npx --no-install tsc --noEmit` |
| `chat-tests` | `packages/chat` | `npm test -- --maxWorkers=4` |
| `chat-build` | `packages/chat` | `npm run build` |
| `runner-lint` | `runner/frontend` | `npm run lint` |
| `runner-types` | `runner/frontend` | `npx --no-install tsc --noEmit` |
| `runner-unit` | `runner/frontend` | `npm test -- --project unit --maxWorkers=4` |
| `runner-browser` | `runner/frontend` | `npm test -- --project browser --maxWorkers=4` |
| `runner-performance` | `runner/frontend` | `npm run test:performance` |
| `runner-build` | `runner/frontend` | `npm run build` |
| `cli-unittest` | `runner/.claude/skills/session-messaging` | `/usr/bin/python3 -m unittest test_blerg_runner test_blerg_runner_review_request` |
| `cli-pytest` | `runner/.claude/skills/session-messaging` | `/usr/bin/python3 -m pytest -q --durations=30 blerg_runner_board_test.py` |
| `scripts` | `.` | `make check-other-scripts` |

### Focused regression checks

All passed. No full browser/performance rerun was used for subsequent control
helper fixes; these were validated by focused tests.

- Runner Go: `go test -json -p=4 -parallel=4 -run='^Test(AuthzRoutes|ParentWatchEndsWhenChildRuntimeStops|RestrictedAndCronSessionsCannotHoldPermissions)$' ./internal/server`:
  13 passed, zero failed/skipped; 28.719 seconds wall including build.
- Runner frontend: `npm test -- --project unit src/components/SessionDockWindows.test.tsx src/test/browserWidths.test.ts src/lib/push.test.ts --maxWorkers=4`:
  42 passed, zero failed/skipped; 7.393 seconds.
- Runner browser, final phase: `npm test -- --project browser src/components/ExplorerDock.browser.test.tsx src/components/WorkersDock.browser.test.tsx src/components/PaletteClaim.browser.test.tsx src/components/SessionSwitchCache.browser.test.tsx src/components/ImageZoom.browser.test.tsx src/components/MarkdownHtml.browser.test.tsx src/components/HostShellConsentDialog.browser.test.tsx --maxWorkers=4`:
  187 passed, zero failed/skipped; 59.891 seconds. Both configured engines ran.
- Normal and race ptrace regression: in runner, `go test -json -p=4 -run='^TestShimIsInvisibleToTheCommand$' ./internal/processcontainment`,
  then the same with `-race -count=1`, through the authorized wrapper and outer
  gate lock: three passed, zero failed/skipped each.
- `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p pre_merge_test.py`:
  24 passed. `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -p 'test_*.py'`:
  29 passed. The completed script matrix had the then-current 22 orchestration
  tests plus these 29 helpers; the added descriptor and package-budget regressions
  were checked afterward.

### Final package-concurrency tuning

After the complete split profile, one fresh timing experiment compared the
core/board suites at `-p=2` with their four-package profile measurements.
`GOMAXPROCS=4` and `-parallel=4` stayed fixed. The command in each module was
`$GO test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...`, vanilla in this
session, with the same database configuration. Here `-count=1` is the explicit
timing exception. Core passed 768 events with six existing skips in 78.005
seconds; board passed 836 events with zero skips in 82.631 seconds; neither had
a failure. Combined time was 160.636 seconds versus 264.800 at four packages,
a 39.3% improvement. Source and assertions in these modules were unchanged.
This sequential comparison is subject to host load and warm build caches.

The pre-recorded adoption criterion was at least 10% combined improvement,
no module more than 5% slower, and identical event counts. It passed, so the
final script caps only core/board Go tests at two concurrent packages (or the
smaller configured budget). CPU budget, test parallelism, other modules and
race workers stay unchanged. The new policy regression failed for budgets four
and six before the fix; afterward all 24 orchestration and 29 helper tests passed.
The full 61.41-minute profile predates this final policy adjustment. No new full
profile is claimed, and the experiment's savings are not subtracted from it.

### Implementation commits

- `e858f2ca` — docs: record blocked test-speed baseline (#298)
- `56cf5af7` — tests: select affected checks during development (#298)
- `e82cb39d` — tests: honor verification budgets up to six workers (#298)
- `52c84f6b` — runner: label person workspace roots Main in the dock (#298)
- `cd7d6979` — tests: preserve cache accuracy and nested gate locks (#298)
- `60011de9` — tests: keep full browser coverage in the final gate (#298)
- `c56ca754` — tests: align gate checks with current permission and recovery contracts (#298)
- `90dc1192` — tests: verify dock isolation before workspace shortcuts (#298)
- `851f0134` — tests: keep gate descriptors out of Go commands (#298)
- `a9223004` — tests: preserve custom Go binary isolation (#298)
- `77c98cdd` — tests: keep synthetic gates out of measurement history (#298)
- `e062b62d` — tests: use the measured package budget for database modules (#298)

The final measurement-document commit follows these implementation commits.
The dependency symlinks used for local verification were removed; no push or
merge to main was performed. Final `scripts/scrub.sh` and `git diff --check` pass.

### Remaining work toward 15 minutes

The runner Go suite still takes 17.88 minutes by itself, despite 37 cached
packages in the restart. Its server package took 1,056.767 seconds versus
828.346 seconds in the failing before run; the normal Go suite did not improve.
Fresh daemon/runtime race checks take 17.70 minutes
combined. Normal server package time, process-containment stress and CLI
subprocess fixtures remain material costs. The kernel stress iterations, real
offline grant and race repetitions were preserved: for example, final runtime
race recorded 84.73 seconds for `TestTIDReuseAcrossPendingExec`, 49.54 seconds
for `TestFreezeHoldsThroughChaos`, and 29.75 seconds for the real offline grant.
Further work needs measured server/package decomposition and fixture/runtime
changes. This run does not justify dropping proofs or running memory-heavy
suites together. The implementation improves development selection and browser
cost, but cannot substantiate a 15-minute final gate.

## 2026-10-31 telemetry-first baseline blocked

Baseline checkout: `77ed6a01` (gate telemetry on main). The complete matrix was
attempted inside the session under the UI suite lock, with both database
variables configured and the pinned lint binary selected:

```sh
BLERG_UI_SUITE_LOCK="$gate_lock" flock "$gate_lock" scripts/pre-merge.sh --logs "$baseline_logs"
```

At **2026-10-31T21:49:17.869853Z**, the tracing preflight exited 1:

```text
Host tracing denied; full shell verification requires PTRACE_TRACEME
```

Telemetry recorded **0.020 seconds**, two workers (the current gate cap),
load averages **9.086 / 11.011 / 12.129**, `complete: false`, and
`stopped_reason: host-tracing-unavailable`. No test suites ran: zero passing,
failing or skipped test events. This is not a usable performance baseline.

The run stopped without retries, bypasses or coverage changes. No before/after
speed comparison or 15-minute result is available. Resume in a session that
permits tracing, measure the full baseline under the lock, then select
optimizations from its suite/test timings. The outside-session exception for
individual ptrace-dependent tests does not supply an inside-session baseline
for the complete matrix.

## Continuation on the BLG-339 mainline

The four rescued commits were merged onto main at `8d201b5e`; only the
changelog conflicted, and both sets of entries were retained. The earlier
measurements below describe the previous builder's environment, not the
continuation's external systemd test units.

The first external full daemon proof reached 5,862 passing and 213 skipped
test/subtest events before a delayed idle callback panicked with
`close of nil channel` at 630.055 package seconds, during repetition three.
The MCP-only fix was insufficient: transcript, thinking, background-work,
streaming and other synthetic process fixtures still lacked lifecycle channels
and cleanup. `TestLineFixtureCanIdleShutdown` reproduces the panic immediately
before the fix. `TestLineFixtureStopsTimers` checks teardown directly.

A shared fixture helper now initializes lifecycle channels and stops all driver
timers during cleanup for every synthetic Claude process. Production shutdown
code and existing assertions are unchanged. The focused race command passes
twice: 16 passed, zero failed or skipped test/subtest events in 1.328 package
seconds. It selects `TestLineFixture`, `TestSandboxCloneFixtureIsolated`,
`TestClaudeMCPReplacementCanClose`, and
`TestClaudeMCPRejectsReplyAfterProcessReplacement` with `-race -p 2 -parallel 4
-count=2`. The initial three-test proof also passed twice (10 events, 1.299
package seconds).

The gate's resource diagnostic matcher also missed `admission closed`,
`process limit exceeded`, and `fork: Cannot fork`. A new test reproduced all
three misses before broadening the matcher; all ten orchestration tests now
pass. This ensures these failures stop the matrix without retries.

The corrected external full proof passed `go test -json -race -p 2
-parallel 4 -count=5 -timeout=45m ./internal/daemon`: **11,385 passed,
zero failed, 470 skipped** test/subtest events across all five repetitions.
The skips retain the suite's existing optional integration prerequisites.
This supersedes the previous builder's incomplete full-daemon proof.

PowerShell was absent. A local PowerShell 7.6.9 installation, verified against
the official release asset's SHA-256 digest, supplies the existing prerequisite;
no WSL regression is skipped. The external gate uses the available current Node
binary instead of the system's obsolete Node. These installations are outside
the repository.

### First successful complete external matrix

After the WSL gate correction, the next full matrix passed **all 38 suites** in
**3,248.149 seconds (54.1 minutes)**. Go suites recorded **17,010 passing,
zero failing, 382 skipped** test/subtest events. Runner Go retained identical
counts (7,846 / 0 / 102) and took 957.3 seconds. The final script suite passed
in 102.662 seconds. The complete repeat also exercised PowerShell and all ten
orchestration tests. The subsequent full stability repeat also passed all 38 suites; see the paired results below.

### First external matrix and WSL gate correction

The first full matrix completed in **3,464.491 seconds (57.7 minutes)**:
37 suites passed and the final script suite failed. All Go, race, lint, web,
browser, build and CLI suites passed. The failure was an `IndexError` in the
WSL regression's Makefile inspection: it assumed `check-scripts:` had no
prerequisites, whereas the new gate delegates to `check-cli` and
`check-other-scripts`. The assertion now inspects `make -n check-scripts`,
following prerequisites and verifying the WSL command is actually scheduled.
The focused full script suite then passed in **98.701 seconds**, including
PowerShell, all ten orchestration tests, scrub, and link checks.
This attempt is retained as a failed full gate, not a successful stability run.

The full gate's PATH also enables three existing native Codex plugin snapshot
subtests that the narrower standalone daemon proof skipped. Its daemon race
counts therefore include nine additional passes and nine fewer skips across
three repetitions. No tests or assertions were removed.

| Suite | First attempt seconds | Result | Go pass / fail / skip events |
| --- | ---: | --- | --- |
| host-tracing-preflight | 0.016 | Pass | — |
| contracts-vet | 0.405 | Pass | — |
| contracts-go | 4.049 | Pass | 171 / 0 / 0 |
| core-vet | 3.562 | Pass | — |
| core-go | 78.518 | Pass | 755 / 0 / 5 |
| board-vet | 4.949 | Pass | — |
| board-go | 84.913 | Pass | 819 / 0 / 0 |
| runner-vet | 13.126 | Pass | — |
| runner-go | 1092.108 | Pass | 7846 / 0 / 102 |
| daemon-race | 745.446 | Pass | 6840 / 0 / 273 |
| runtime-race | 112.613 | Pass | 579 / 0 / 2 |
| lint-pin | 0.098 | Pass | — |
| gofmt | 1.099 | Pass | — |
| contracts-go-lint | 6.951 | Pass | — |
| core-go-lint | 11.136 | Pass | — |
| board-go-lint | 13.520 | Pass | — |
| runner-go-lint | 58.770 | Pass | — |
| core-lint | 9.277 | Pass | — |
| core-types | 4.316 | Pass | — |
| core-tests | 14.874 | Pass | — |
| core-build | 4.663 | Pass | — |
| board-lint | 16.229 | Pass | — |
| board-types | 10.235 | Pass | — |
| board-tests | 32.384 | Pass | — |
| board-build | 11.622 | Pass | — |
| chat-lint | 20.392 | Pass | — |
| chat-types | 11.142 | Pass | — |
| chat-tests | 161.920 | Pass | — |
| chat-build | 9.153 | Pass | — |
| runner-lint | 31.048 | Pass | — |
| runner-types | 0.380 | Pass | — |
| runner-unit | 184.041 | Pass | — |
| runner-browser | 351.530 | Pass | — |
| runner-performance | 82.415 | Pass | — |
| runner-build | 25.985 | Pass | — |
| cli-unittest | 178.639 | Pass | — |
| cli-pytest | 18.308 | Pass | — |
| scripts | 54.340 | Fail | — |

### Two successful complete runs

Both runs used Go 1.26.9, `GOWORK=off`, a worker budget of two,
`-parallel=4`, configured test databases, existing caches, pinned lint, current
Node, Chromium, Firefox, tmux, pytest and PowerShell. Each whole gate ran in an
external systemd unit, one heavy suite at a time. No resource-exhaustion or
containment failure was observed. Tests were fresh (`-count=1`); daemon race
used three repetitions. Each run saved 38 full suite logs and `summary.json`
in a separate private directory. Raw logs are not committed or published.

The exact gate command, after setting both database variables and the lint
binary, was:

```sh
BLERG_TOOL_WORKERS=2 GOWORK=off ./scripts/pre-merge.sh --logs <new-directory>
```

The historical approximately 40-minute baseline is not a controlled comparison:
it used four browser workers and did not separately itemize the three-pass
daemon race gate now included here. Whole-matrix speedup is not established.
All times below are seconds, including subprocess startup.

| Historical group | Supplied baseline | Successful run 1 | Successful run 2 |
| --- | ---: | ---: | ---: |
| Runner Go | ~810 | 957.258 | 828.781 |
| CLI unittest + pytest | ~360 | 198.464 | 195.722 |
| Runtime race | ~240–300 | 111.506 | 109.516 |
| Go lint, four modules | ~240 | 4.654 | 4.720 |
| Runner browser, two workers here | ~180, four workers | 346.067 | 339.111 |
| Web builds | ~180 | 51.539 | 51.025 |
| Board + core Go | ~120 | 151.030 | 150.367 |
| Browser performance | ~78 | 81.239 | 77.724 |
| Web unit tests | ~120 | 383.147 | 382.059 |
| Three-pass daemon race | not separately supplied | 754.924 | 746.871 |
| Whole matrix | ~2,400 | 3248.149 | 3092.690 |

Both complete runs passed every suite. Counts below are identical between runs.
Go counts include test/subtest events and repeated invocations; Vitest counts
are tests, not files. The CLI unittest count excludes successful subtest counts.
Existing optional integration skips remain visible.

| Suite | Run 1 seconds | Run 2 seconds | Result/count in each run |
| --- | ---: | ---: | --- |
| host-tracing-preflight | 0.015 | 0.015 | Pass |
| contracts-vet | 0.221 | 0.191 | Pass |
| contracts-go | 3.215 | 3.146 | 171 passed, 0 failed, 0 skipped |
| core-vet | 0.312 | 0.289 | Pass |
| core-go | 72.961 | 72.621 | 755 passed, 0 failed, 5 skipped |
| board-vet | 0.336 | 0.314 | Pass |
| board-go | 78.069 | 77.746 | 819 passed, 0 failed, 0 skipped |
| runner-vet | 0.724 | 0.764 | Pass |
| runner-go | 957.258 | 828.781 | 7,846 passed, 0 failed, 102 skipped |
| daemon-race | 754.924 | 746.871 | 6,840 passed, 0 failed, 273 skipped |
| runtime-race | 111.506 | 109.516 | 579 passed, 0 failed, 2 skipped |
| lint-pin | 0.100 | 0.102 | Pass |
| gofmt | 1.110 | 1.109 | Pass |
| contracts-go-lint | 0.853 | 0.846 | Pass |
| core-go-lint | 0.944 | 0.960 | Pass |
| board-go-lint | 0.996 | 1.009 | Pass |
| runner-go-lint | 1.861 | 1.905 | Pass |
| core-lint | 8.146 | 8.396 | Pass |
| core-types | 4.169 | 4.228 | Pass |
| core-tests | 13.826 | 13.989 | 243 passed |
| core-build | 4.386 | 4.406 | Pass |
| board-lint | 15.500 | 16.087 | Pass |
| board-types | 9.810 | 10.202 | Pass |
| board-tests | 29.959 | 30.374 | 293 passed |
| board-build | 11.097 | 11.528 | Pass |
| chat-lint | 19.774 | 21.345 | Pass |
| chat-types | 11.326 | 11.322 | Pass |
| chat-tests | 160.291 | 159.960 | 2,684 passed, 3 skipped |
| chat-build | 8.814 | 9.079 | Pass |
| runner-lint | 30.226 | 30.044 | Pass |
| runner-types | 0.380 | 0.386 | Pass |
| runner-unit | 179.071 | 177.736 | 2,479 passed, 15 skipped |
| runner-browser | 346.067 | 339.111 | 1,474 passed |
| runner-performance | 81.239 | 77.724 | 29 passed |
| runner-build | 27.242 | 26.012 | Pass |
| cli-unittest | 180.167 | 178.168 | 401 passed |
| cli-pytest | 18.297 | 17.554 | 118 passed |
| scripts | 102.662 | 98.569 | Pass |

Each run recorded **17,010 passing / 0 failing / 382 skipped Go events**,
**7,202 passing / 18 skipped web tests**, 401 CLI unittests and 118 pytest tests.
The script suite additionally passed 4 enrollment, 5 WSL interop, 5 demo,
5 video-build and 10 orchestration Python tests, 38 demo shell assertions,
and the installer, PowerShell, scrub, release-public and link checks. Kubernetes
dry-run retains the documented `BLERG_SKIP_KUBECTL=1` exclusion; Docker Compose
validation ran. No test selection or assertion was weakened.

#### Exact suite commands

Commands run from the directory in the second column. `go` resolves to Go
1.26.9; `golangci-lint` resolves to pinned v2.13.1. `$REPO` denotes the checkout
root. The preflight is the Python `PTRACE_TRACEME` probe in
`scripts/pre_merge.py`; it passed before each full run.

| Suite | Directory | Command |
| --- | --- | --- |
| contracts-vet | `contracts` | `go vet -p=2 ./...` |
| contracts-go | `contracts` | `go test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...` |
| core-vet | `core` | `go vet -p=2 ./...` |
| core-go | `core` | `go test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...` |
| board-vet | `board` | `go vet -p=2 ./...` |
| board-go | `board` | `go test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...` |
| runner-vet | `runner` | `go vet -p=2 ./...` |
| runner-go | `runner` | `go test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...` |
| daemon-race | `runner` | `go test -json -race -count=3 -p=2 -parallel=4 -timeout=45m ./internal/daemon` |
| runtime-race | `runner` | `go test -json -race -count=1 -p=2 -parallel=4 -timeout=30m ./internal/ownershell ./internal/shellruntime ./internal/processcontainment` |
| lint-pin | `.` | `./scripts/check_lint_pin.sh golangci-lint` |
| gofmt | `.` | `python3 -c 'import subprocess, sys; r=subprocess.run(['"'"'gofmt'"'"', '"'"'-l'"'"', '"'"'./contracts'"'"', '"'"'./core'"'"', '"'"'./board'"'"', '"'"'./runner'"'"'], capture_output=True, text=True); print(r.stdout, end='"'"''"'"'); print(r.stderr, end='"'"''"'"', file=sys.stderr); sys.exit(r.returncode or bool(r.stdout))'` |
| contracts-go-lint | `contracts` | `golangci-lint run --concurrency=2 --config=$REPO/.golangci.yml ./...` |
| core-go-lint | `core` | `golangci-lint run --concurrency=2 --config=$REPO/.golangci.yml ./...` |
| board-go-lint | `board` | `golangci-lint run --concurrency=2 --config=$REPO/.golangci.yml ./...` |
| runner-go-lint | `runner` | `golangci-lint run --concurrency=2 --config=$REPO/.golangci.yml ./...` |
| core-lint | `core/web` | `npm run lint` |
| core-types | `core/web` | `npx --no-install tsc --noEmit` |
| core-tests | `core/web` | `npm test -- --maxWorkers=2` |
| core-build | `core/web` | `npm run build` |
| board-lint | `board/web` | `npm run lint` |
| board-types | `board/web` | `npx --no-install tsc --noEmit` |
| board-tests | `board/web` | `npm test -- --maxWorkers=2` |
| board-build | `board/web` | `npm run build` |
| chat-lint | `packages/chat` | `npm run lint` |
| chat-types | `packages/chat` | `npx --no-install tsc --noEmit` |
| chat-tests | `packages/chat` | `npm test -- --maxWorkers=2` |
| chat-build | `packages/chat` | `npm run build` |
| runner-lint | `runner/frontend` | `npm run lint` |
| runner-types | `runner/frontend` | `npx --no-install tsc --noEmit` |
| runner-unit | `runner/frontend` | `npm test -- --project unit --maxWorkers=2` |
| runner-browser | `runner/frontend` | `npm test -- --project browser --maxWorkers=2` |
| runner-performance | `runner/frontend` | `npm run test:performance` |
| runner-build | `runner/frontend` | `npm run build` |
| cli-unittest | `runner/.claude/skills/session-messaging` | `python3 -m unittest test_blerg_runner test_blerg_runner_review_request` |
| cli-pytest | `runner/.claude/skills/session-messaging` | `python3 -m pytest -q --durations=30 blerg_runner_board_test.py` |
| scripts | `.` | `make check-other-scripts` |

#### Server profile and decisions

The second complete attempt's server package passed in 779.229 seconds. The
refusal fixture took 0.01 seconds versus its earlier 10.01-second timeout.
The slowest top-level cases remained recipient/delivery (16.66 seconds),
desktop attach policy timeout (15.93), ambiguous-create cleanup (13.59),
private delivery failures (9.69), delegation matrix (9.07), and grant refusals
(9.04). These cover real deadline behavior or multi-case matrices; their
assertions and production timeouts remain intact. Profiling evidence supports
the mutex fix, not a broad claim that all package timing variation came from it.

- Keep bounded package parallelism (`-p=2`) in the new gate: it passed all runner
  tests repeatedly with identical counts. The historical `-p=1` comparison is
  not controlled, so the legacy `make test` limit is unchanged.
- Keep shorter HTTP fixture shutdown polling: the prior same-environment CLI
  comparison fell from 349.0 seconds to 201.0/202.0 seconds (about 42%); complete
  gates here took 198.464/195.722 seconds. No xdist dependency is necessary.
- Keep the ordinary lint caches and all four module checks. Warm repeat Go lint
  takes under five seconds; changed-module-only lint was not adopted.
- Heavy suites remain sequential under the explicit task resource constraint.
  Inter-suite parallelism was not implemented or benchmarked. Browser workers
  remain at two, and performance runs separately. CI workflows are unchanged.
- Two full successful gates now establish repeatability in the supported external
  environment. The earlier failed matrix, focused script repair, and additional
  delayed fixture panic remain documented above.

### Review commits

All work is on the session branch. The final report commit is identified in the
completion message; its ancestors are listed here. No push or mainline merge
was performed.

| Commit | Change |
| --- | --- |
| `20517c78` | runner: isolate repeated daemon test fixtures (#290) |
| `de33f894` | cli: shorten verification fixture shutdown waits (#298) |
| `797cd446` | scripts: record bounded pre-merge verification evidence (#298) |
| `3e42ac38` | runner: avoid timeout waits in server verification (#298) |
| `88fb5af8` | tests: integrate repeatable fixtures and pre-merge gate (#298) |
| `5bd95327` | scripts: stop verification on all reported resource failures (#298) |
| `7aec9338` | runner: clean up every synthetic Claude process fixture (#290) |
| `5eeb1e9e` | docs: record the additional delayed fixture panic (#290) |
| `aefa5c4c` | docs: record five passing daemon race repetitions (#290) |
| `3cdab9e4` | tests: follow delegated script checks in the WSL gate (#298) |
| `ae88854e` | docs: preserve full matrix timings and WSL gate correction (#298) |
| `2d64963f` | docs: record the first successful complete pre-merge matrix (#298) |

## Previous builder's measurements

This work starts from the rescued changes, rebased onto the 0.5.2 mainline.
Measurements below use Go 1.26.9, workspace mode off, package concurrency two,
and test concurrency four. Heavy suites run sequentially. The worker-budget
variable was unset, so the gate uses its documented fallback of two.
Database settings were supplied for Go runs; connection strings are not recorded.

## Verification limits

This execution environment refuses `PTRACE_TRACEME` with `EPERM`.
Two final full gate attempts stopped at the tracing preflight and exited nonzero
(exit 1, 0.021 and 0.022 seconds).
Neither is a successful full matrix, and no whole-matrix speedup is established.

The WSL regression command also exits nonzero because `pwsh` is absent:
`wsl_startup_test: pwsh is required (Linux PowerShell; all Windows APIs are mocked)`.
The full gate requires Linux PowerShell as documented; this check was not skipped.

The full daemon command was also attempted:

```sh
cd runner
GOWORK=off GOMAXPROCS=2 "$HOME/go/bin/go" test -json -race -p 2 -parallel 4 -count=5 -timeout=45m ./internal/daemon
```

It encountered host-shell permission failures beginning at 01:25:50 UTC on
2026-10-30: `permission denied starting shell (paths and details redacted)`
and `helper_reply: shell runtime unavailable`. It was stopped after 183.326
package seconds, with 1,675 passing, six failing and 36 skipped test/subtest
events. These are interrupted-run counts, not completed five-pass coverage.
The failures include legacy workspace shell, busy shell and cluster shell tests.
No process/thread exhaustion or writer containment failure was observed.

## Fixture regressions (#290)

Before the sandbox fixture fix, this new regression fails on the reused clone:

```sh
GOWORK=off GOMAXPROCS=2 "$HOME/go/bin/go" test -race -p 2 -parallel 4 -count=2 -run '^TestSandboxCloneFixtureIsolated$' ./internal/daemon
```

Before initializing the replacement process channels, adding direct shutdown to
`TestClaudeMCPRejectsReplyAfterProcessReplacement` reproduces
`panic: close of nil channel` in `finishStdinClose`.

After the fixes, the narrow three-test race check passes twice (ten passing
test/subtest events). The broader focused proof passes five times:

```sh
GOWORK=off GOMAXPROCS=2 "$HOME/go/bin/go" test -json -race -p 2 -parallel 4 -count=5 -timeout=15m -run '^(TestSandbox|TestClaudeMCP)' ./internal/daemon
```

Result: 525 passing and 45 skipped test/subtest events in 58.577 package seconds;
390 passing and 30 skipped top-level invocations. Docker-dependent cases retain
their existing skips. No assertions or production shutdown behavior changed.
The full-package `-race -count=5` proof remains required on a host that permits
shell tracing.

## Timing comparison

The supplied 2026-10-28 baseline was approximately 40 minutes, sequentially.
It is historical context, not a same-environment controlled comparison.
The new gate also includes CI's three-pass daemon race check; the supplied
baseline has no separate timing for that check. Whole totals need matched
coverage and a supported environment before comparison.

| Suite | Supplied baseline | Current evidence |
| --- | --- | --- |
| Runner Go | ~13.5 min (`-p 1`) | Server profile below; full runner gate blocked |
| CLI unittest + pytest | ~6 min | Focused comparison below |
| Runtime race | ~4–5 min | Blocked by tracing preflight |
| Go lint, four modules | ~4 min | Focused comparison below |
| Runner browser | ~3 min, four workers | Focused suite table below |
| Web builds | ~3 min | Focused suite table below |
| Board + core Go | ~2 min | Focused suite table below |
| Browser performance | ~1.3 min | Focused suite table below |
| Web unit tests | ~2 min | Focused suite table below |
| Whole matrix | ~40 min | Two incomplete preflight failures; no speedup claim |

## CLI fixture comparison

Only HTTP stub shutdown polling changed, from the default 500 ms to 10 ms.
Request handling, subprocess deadlines, test selection and assertions are the
same. No xdist dependency or subprocess isolation change was needed.
The baseline pytest duration report repeatedly showed 0.48–0.49 second teardown
waits; these are absent from the first updated run's slowest-30 report.

All measurements use the same focused gate command:

```sh
./scripts/pre-merge.sh --suite cli-unittest --suite cli-pytest --logs <new-directory>
```

The underlying commands run from `runner/.claude/skills/session-messaging`:

```sh
python3 -m unittest test_blerg_runner test_blerg_runner_review_request
python3 -m pytest -q --durations=30 blerg_runner_board_test.py
```

| Suite | Before (seconds) | After run 1 (seconds) | After run 2 (seconds) | Results per run |
| --- | ---: | ---: | ---: | --- |
| CLI unittest | 296.3 | 182.3 | 183.5 | 401 tests passed |
| CLI pytest | 52.7 | 18.7 | 18.5 | 118 tests passed |
| Combined | 349.0 | 201.0 | 202.0 | Both suites passed |

These are orchestrator wall times, including interpreter startup. The unittest
runner does not report successful subtests separately. Both updated runs passed
with the same test counts. Combined time fell by about 42%; this is a CLI result, not a whole-matrix estimate.

## Lint cache measurements

All four modules were checked sequentially with pinned golangci-lint v2.13.1
and `--concurrency=2`. Both runs retained the ordinary shared lint cache; neither
purged it. The first run is not described as a cold-cache benchmark.
No lint checks or modules were removed.

```sh
./scripts/pre-merge.sh --suite lint-pin --suite gofmt --suite contracts-go-lint --suite core-go-lint --suite board-go-lint --suite runner-go-lint --logs <new-directory>
```

| Module | Run 1 (seconds) | Immediate repeat (seconds) | Result in both runs |
| --- | ---: | ---: | --- |
| contracts | 9.7 | 1.1 | Pass, zero issues |
| core | 16.6 | 1.2 | Pass, zero issues |
| board | 20.4 | 1.3 | Pass, zero issues |
| runner | 96.6 | 2.3 | Pass, zero issues |

Including the version pin and formatting checks, totals were 145.2 and
7.5 seconds. Retaining the default cache makes repeat verification cheap;
this does not establish a whole-matrix speedup.

## Supported suite measurements

The remaining supported suites ran once, sequentially, using the gate's focused
suite selection. All 24 suites passed in 1257.4 seconds. This is a focused
matrix: it excludes runner Go, daemon/runtime race tests, and script checks.
Go tests used fresh `-count=1` execution with both database variables supplied.
Web suites used two workers. The measured browser run is slower than the
historical four-worker run; no browser or unit-test speedup is claimed.
Core Go's five skips require a separately configured Vault, not the database.
Chat and runner retain their existing skipped tests.

| Suite | Seconds | Result | Test/subtest events or Vitest test counts |
| --- | ---: | --- | --- |
| contracts-vet | 0.6 | Pass | — |
| contracts-go | 4.1 | Pass | 171 passed, 0 failed, 0 skipped |
| core-vet | 1.6 | Pass | — |
| core-go | 72.6 | Pass | 755 passed, 0 failed, 5 skipped |
| board-vet | 1.7 | Pass | — |
| board-go | 76.1 | Pass | 819 passed, 0 failed, 0 skipped |
| core-lint | 8.9 | Pass | — |
| core-types | 4.0 | Pass | — |
| core-tests | 14.9 | Pass | 243 passed, 0 failed, 0 skipped |
| core-build | 4.6 | Pass | — |
| board-lint | 16.4 | Pass | — |
| board-types | 10.0 | Pass | — |
| board-tests | 30.9 | Pass | 293 passed, 0 failed, 0 skipped |
| board-build | 11.7 | Pass | — |
| chat-lint | 20.1 | Pass | — |
| chat-types | 11.8 | Pass | — |
| chat-tests | 209.8 | Pass | 2684 passed, 0 failed, 3 skipped |
| chat-build | 12.4 | Pass | — |
| runner-lint | 35.6 | Pass | — |
| runner-types | 0.4 | Pass | — |
| runner-unit | 217.2 | Pass | 2479 passed, 0 failed, 15 skipped |
| runner-browser | 367.4 | Pass | 1474 passed, 0 failed, 0 skipped |
| runner-performance | 91.6 | Pass | 29 passed, 0 failed, 0 skipped |
| runner-build | 32.7 | Pass | — |

Exact commands and per-suite output are retained in the focused run logs. Go
commands are the module-local `go vet -p=2 ./...` and
`go test -json -count=1 -p=2 -parallel=4 -timeout=45m ./...`. Web commands are
`npm run lint`, `npx --no-install tsc --noEmit`, `npm test -- --maxWorkers=2`,
and `npm run build`. Runner tests additionally select `--project unit` or
`--project browser`; performance uses `npm run test:performance` in its own
process.

## Server profile and refusal fixture

The full server package passed before the fixture change: 3,489 passing and four
skipped test/subtest events (1,842 passing and four skipped top-level tests).
Package execution took 867.831 seconds; total wall time including build/startup
was 873.29 seconds. These constrained measurements are not a controlled
comparison with the historical ~10-minute server baseline.

Both profiles use this command from `runner/`, with the test database configured:

```sh
PATH="$HOME/go/bin:$PATH" GOWORK=off GOMAXPROCS=2 "$HOME/go/bin/go" test -json -p 2 -parallel 4 -count=1 -timeout=30m ./internal/server
```

The slowest top-level passing tests were:

| Test | Before (seconds) |
| --- | ---: |
| `TestP6MemberResultsRecipientAndDelivery` | 16.23 |
| `TestDesktopPersonalAttachPolicyTimeout` | 16.22 |
| `TestLaunchAmbiguousSecretCleanup` | 14.28 |
| `TestDelegationMatchMatrix` | 11.81 |
| `TestBoardLimitsAreReadFromCoreWithTheInternalKey` | 10.01 |
| `TestResolveGrantRefusals` | 9.52 |
| `TestP6PrivateHTTPDeliveryFailures` | 9.33 |
| `TestPostSessionsRejectsBadModelOrEffort` | 8.06 |
| `TestReviewBExplorerCannotReadForeignDesktopAsAdmin` | 7.10 |
| `TestLimitResumeAtRecordedReset` | 6.27 |
| `TestEverySpawnSiteAttachesOrRefusesTheGrant` | 6.22 |
| `TestRereviewNewOwnerMustNotReceiveEarlierOwnerSources` | 5.89 |
| `TestMCPDefaultsSave` | 5.57 |
| `TestClusterWorkspaceTrustedCleanupRefusesUncertainState` | 5.36 |
| `TestBusyDesktopIsNeverAFenceOrRevocation` | 5.09 |

`TestBoardLimitsAreReadFromCoreWithTheInternalKey` held the request-counter mutex
while issuing its final HTTP request. The handler needed that mutex, so the
client's ten-second timeout supplied the expected error without exercising the
core's authorization refusal. A new refusal-notification assertion reproduces
the defect: the test fails after 10.04 seconds because core never handled the
refused request. Releasing the counter mutex before that request fixes both the
wait and the false-positive assertion. Existing assertions and client deadlines
are retained.

The focused fixed test passes twice under the race detector, in 0.04 and 0.02
test seconds, using `-race -p 2 -parallel 4 -count=2 -timeout=5m` and
`-run '^TestBoardLimitsAreReadFromCoreWithTheInternalKey$'` on the same package.
The full server repeat will be recorded after it completes.

Other top costs include multi-case authorization/delivery matrices and real
five-second desktop-fence and ten-second ambiguous-create deadline coverage.
Those deadline tests and their assertions remain intact. The four server skips
are existing production-stack/real-cluster integration cases.

## Decisions and remaining work

- Keep fresh Go test executions, full module linting and unchanged CI workflows.
  Each module's Go lint now has its own timing and log. Existing lint caches
  remain enabled; changed-module linting was not adopted without evidence.
- Keep heavy suites sequential under the explicit resource constraint. The new
  gate bounds package and browser workers; inter-suite parallelism is not
  implemented or measured here.
- Preserve the existing `make test` package limit pending comparative runner
  measurements. The new gate uses bounded `-p 2`, but no runner speedup has been
  established in this restricted environment.
- The server profile passed before fixture changes. The core-refusal test lost
  its accidental timeout wait; integration deadline coverage remains intact.
- Run the complete gate twice successfully on a tracing-enabled host and record
  each suite's timing and exact counts before calling #298 complete. Install the
  documented PowerShell prerequisite for the WSL regressions as well.

## Other checks

- `python3 -m unittest discover -s scripts -p pre_merge_test.py`: nine tests pass.
- `scripts/check_links.sh`: passes.
- `scripts/scrub.sh`: passes.
- `bash install/desktop/workspace_worker_image_test.sh`: passes.
- `scripts/release-public_test.sh`: passes all 11 checks, using temporary local
  repositories and no external pushes.
- `bash install/desktop/wsl_startup_test.sh`: fails because `pwsh` is missing.

## BLG-298 continuation, 2026-11-06

The in-session full baseline stopped at `host-tracing-preflight`: this session
already has a tracer, so `PTRACE_TRACEME` is denied. Its recorded gate elapsed
time was 0.041 s; this is an incomplete failure, not a full baseline. An
in-session legacy `make test` attempt was interrupted after 370.25 s before
runner execution, after Main confirmed that tracer-dependent tests belong in
its external launcher. Neither attempt establishes a whole-matrix speedup.

The worker variable was unset, so the gate uses its documented two-worker
fallback. Node dependencies were installed from both lockfiles. Shared host
load varied substantially while other sessions ran; no quiet-host claim applies.

The current server discovery inventory contains 2,635 top-level test, example
and fuzz-target names. The two exact selectors contain 1,318 and 1,317 names;
every discovered name matches exactly one selector. This proves planned
selection, not successful execution of those 2,635 cases. A real Go fixture
additionally compares the complete before/after pass receipts for tests,
a subtest, an example, and both fuzz seeds (seven events), with exact equality.
A mutation replacing the selectors with a match-all regex fails the regression
on duplicate test receipts; the implementation was restored and rerun.

The external comparison is provided by `scripts/gate-timing.sh BASE HEAD
--output NEW_DIRECTORY`. Main executed it through the approved external
launcher and returned its JSON evidence. The completed external results below supersede this pending measurement;
the approximately 15-minute target remains unmet.

Tracer-dependent coverage requiring the external launcher includes the complete
`internal/processcontainment` kernel-tracing proofs, real traced shell cases in
`internal/shellruntime`, `internal/ownershell`, and `internal/daemon`, and the
server's `TestAcceptanceHostSmoke` and tagged
`TestPersistentShellRealWireReconnectAndWithdrawal`. Representative exact cases
are `TestNewChildRunsNothingBeforeItsContinue`, `TestFreezeHoldsThroughSIGCONT`,
`TestTIDReuseAcrossPendingExec`, `TestTracedLaunchStages`,
`TestTracedShellRefusesProcessesAndSaysSo`, `TestTracedShellJobControl`,
`TestKeeperBridgeRestartAdoptsSameTracerAndShell`,
`TestKeeperUncleanHelperDeathEventuallySettlesSpawnedObserver`,
`TestProcessLimitNoticeNeverStartsATurn`, and `TestSessionEndToEndProcessShapes`.
This list identifies the environment boundary; it is not a reduced gate filter.
The comparison script runs the entire matrix and all Go modules.

Focused verification commands for this continuation (no product Go or UI source
changed):

- `PATH="$HOME/go/bin:$PATH" python3 -m unittest discover -s scripts -p pre_merge_test.py`:
  77 passed in 14.432 s, after fixing a synthetic-command wrapper regression.
- `PATH="$HOME/go/bin:$PATH" PYTHONPATH=scripts python3 -m unittest discover -s scripts/tests -p 'test_*.py'`:
  44 passed in 1.751 s, including the real Go coverage fixture and receipt parser.
- `python3 -m py_compile scripts/go_test_shards.py scripts/gate_timing.py scripts/pre_merge.py`:
  passed.
- `bash -n scripts/gate-timing.sh`, `scripts/scrub.sh` and `git diff --check`:
  passed. The full documentation link check also passed.
- `GOWORK=off "$HOME/go/bin/go" run ./cmd/test-timing --summary SUMMARY_JSON`
  from `runner/` generated the timing report for the failed preflight. The
  installed `blerg-runner report test-timing` wrapper could not find its runtime;
  the repository command was used instead.

Expected red proofs were observed for the legacy serial Makefile, unsharded
runner execution, missing failure causes, missing receipt detection, and duplicate
selection mutation. The first complete orchestration rerun failed one synthetic
non-Go-command test; restricting preparation to actual `go test` commands fixed
it. No focused verification tests were skipped.


## Quiet external comparison and failure analysis, 2026-11-06

Main ran the committed benchmark through its approved systemd user unit on a
quiet machine, with no other workers, `BLERG_TOOL_WORKERS=8`, and the same two
revisions: before `8a15b8e71204622a471e4e11a08d6b8c9dd3e92f`, after
`3a5e8443c9cf22979c2f2e3920e0c6a47a87ce1b`. Both full matrix attempts completed
execution and exited 1. Both failed the same three suites: `daemon-race`,
`runner-browser`, and `desktop-scripts`. These are timings of failed matrix
attempts, not a successful full gate or proof of complete browser coverage.
The earlier disk-limited interrupted run is not used. Main ran gate mode only;
`make test` before/after wall times remain unmeasured.

| Measurement | Before seconds | After seconds | Reduction |
| --- | ---: | ---: | ---: |
| Full command wall time | 5,395.053 | 4,068.341 | 1,326.712 s / 24.591% |
| Wall time less each aborted browser suite | 5,242.342 | 3,981.779 | 1,260.563 s / 24.046% |
| runner-go | 2,473.691 | 1,209.383 | 1,264.308 s / 51.110% |
| runtime-race | 520.810 | 547.825 | Increased 27.015 s |
| daemon-race | 489.727 | 475.597 | 14.130 s |
| runner-browser (aborted) | 152.711 | 86.562 | Not comparable complete coverage |

The subtraction is descriptive arithmetic, not a second measured gate. Both
reports record one concurrent suite, making it useful for isolating the browser
abort's timing contribution. Whole command wall time fell from 89.92 to 67.81
minutes; the approximately 15-minute target is still far away. Normal caches
were retained and before ran first, so cache warming remains a confounder.

### Requested receipt comparison

The original strict comparator remains unchanged and reports `preserved: false`.
For this analysis only, exclude `runner-browser` and the 36 UUID-renamed Go
subtest names on each side. Keep the three stable UUID-containing names.
The remaining **21,587 events across 21,542 names match exactly**, including
outcomes and repetition counts: **21,327 pass, 259 skip, one fail** in each run.
No names are missing or added in that restricted comparison. The 36 apparent
new Go names replace 36 old names with different generated UUIDs; they are not
additional test cases. Normalizing UUIDs instead of excluding those names
retains **21,623 events**, also with exact outcome/repetition equality.

The original comparator found 599 missing names: 563 browser names absent after
its earlier abort, plus 36 UUID-renamed Go subtests (22 board, 14 runner). All 62
changed repetition counts are browser names. None of these exclusions changes
the gate's selection or turns either failing run into a pass. CLI/shell checks
are represented by their suite results and logs, not a complete individual named
receipt inventory; exact named parity here covers emitted Go/Vitest receipts.

All **105 new skips** are common browser names that were `pass` before and
`skip` after. There are **no new Go skips**. The after browser JSON contains
105 pending assertion records, matching this set; it also warns that tests were
still running when JSON was generated. These are incomplete execution receipts
following the browser abort, not intentional new skip annotations.

| Browser file | Newly skipped names |
| --- | ---: |
| ExplorerDock.browser.test.tsx | 72 |
| PaletteOwnership.browser.test.tsx | 14 |
| InboxApp.browser.test.tsx | 7 |
| HistoryScroll.browser.test.tsx | 5 |
| ChartLifecycle.browser.test.tsx | 4 |
| TabScroll.browser.test.tsx | 3 |

### Failure causes

- **daemon-race: real content/assertion mismatch.** Both logs fail only
  `TestCoordinatingRetainsOperationalRules` at `layeredskills_test.go:111`:
  `missing coordination rule: without private history`. The test searches the
  embedded coordinating skill for required text. This failure does not involve
  ptrace, systemd services, or a reported data race. The benchmark did not create
  it; it is present in both revisions.
- **desktop-scripts: environment prerequisite missing from the unit's PATH.**
  Both logs reach `wsl_startup_test` and fail with `pwsh is required (Linux
  PowerShell; all Windows APIs are mocked)`. PowerShell is installed under the
  session's user-local bin directory and resolves inside this session, so Main
  should expose that executable on the external unit's PATH and rerun the check.
  The preceding worker-image failure sentence is an expected negative fixture:
  it is followed by `workspace worker installer: PASS`. It is not the suite's
  failure cause. Systemd itself is not established as incompatible with the test.
- **runner-browser: assertions plus an unhandled harness error.** Both runs fail
  the three SessionBoardButton BLG-505 cases at 3840 px; Main reports BLG-571
  has since fixed those on main, outside these measured snapshots. The after
  run's unhandled error is failure to run
  `CompactFoldersReview.browser.test.tsx`: **Cannot connect to the iframe**,
  with **Received URL: unknown due to CORS**. Vitest suggests checking unintended
  navigation/form submission and missing `event.preventDefault()`; that is a
  diagnostic hint, not a proven root cause. The before log contains **11**
  unhandled errors, including `SyntaxError: JSON.parse: unexpected character
  at line 1 column 1 of the JSON data` attributed to WorkersDock browser cases.
  The before and after unhandled-error sets are therefore not identical.
  These require browser-fixture/harness diagnosis; no evidence here attributes
  them to a ptrace refusal or merely to being run by systemd.

### Next large cuts

The requested eight-worker budget becomes six workers per suite, and
`floor(8 / 6) = 1` suite at a time. Around 21 GiB of reported headroom does not
impose that one-slot limit; the CPU allocation policy does. Allowing, for example,
two four-worker independent suites, or allocating workers per suite, is the
next gate-level opportunity. Keep the UI/output resource locks and the final
performance phase isolated. Even deleting all of after's runner-go time would
leave about **47.65 minutes**, so optimizing runner alone cannot meet 15 minutes
under this serial schedule.

Within runner, server package execution fell from **2,403.728 s** to four shards
of **972.712, 947.901, 726.789 and 637.128 s**. The 335.584-second shard spread
supports replacing alphabetical round-robin with measured-duration balancing,
while proving the same disjoint root selectors. Other packages currently finish
before the server shards begin; budgeted overlap is another opportunity.

The four slowest server roots are real process/HTTP/Postgres matrices:

| Root test | Before seconds | After seconds |
| --- | ---: | ---: |
| TestBoundCutAskProcess | 291.340 | 273.220 |
| TestBoundCutExecutionProcess | 261.380 | 192.360 |
| TestBoundDraftAskProcess | 239.180 | 215.880 |
| TestBoundPlanAskProcess | 212.280 | 196.500 |

Their aggregate falls from **1,004.180 to 877.960 s**; those sums are work across
roots, not additional gate wall time. Source inspection shows each matrix starts
real child processes per scenario, initializes database fixtures, and cut roots
build a board test binary. Instrument readiness, database setup, body and cleanup
separately next. Reusing a compiled board binary and a migrated template with
isolated per-scenario database clones is a candidate, not a measured speedup.
Keep the separate-process/public-HTTP/authentication assertions. Existing restart
cases already support attaching the child to a parent-provisioned database.
There is no evidence here that replacing sleeps alone explains this cost.

Secondary costs are runner DB (**431.365 to 200.886 s**), runtime race (~9 min),
daemon race (~8 min), and CLI unittest (~211 s). The DB flood proof alone is
~60 s in both runs; preserve its one-minute real-service semantics unless a
separate lower-layer proof can retain that boundary. Fix the known failures and
rerun the complete browser suite before asserting full coverage or a passing gate.

This follow-up changed documentation only. The receipt analysis asserted exact
filtered and normalized equality and checked that the per-file new-skip counts
sum to 105. No product tests were rerun or skipped for this analysis. The two-run
repository test-timing command generated a report from the existing summaries;
it did not execute tests. The earlier 77 orchestration and 44 script test results
remain the implementation verification, not fresh results for today's snapshots.

## Go tooling port requested in review, 2026-11-06

Claude's review of `f4b6a5c0` requested Go for the new tooling under the owner's
language rule. The sharder and benchmark are now standard-library-only Go main
files under `scripts/verification/cmd/`; their Go tests replace the two new Python
test files. The Python tools and `gate-timing.sh` entry point are removed. The
existing `pre_merge.py` invokes the Go sharder as a subprocess, preserving its
module working directory, supplied Go command, worker budget and process group.
The benchmark keeps the positional CLI, JSON result/receipt schema, both modes,
retained worktrees, ordinary caches and failure propagation; its legacy make
reporter proxy is a retained Go executable rather than generated Python.

The port has behavioral red proofs for missing/disjoint selection, discovery
failure, missing receipts, repetitions and newly skipped cases. Mutations making
every shard select all names, or allowing invalid receipt comparison, failed
those Go assertions; the mutations were restored. A real dependency-free Go
fixture retains exactly the same seven test/subtest/example/fuzz-seed events.
A fully faked benchmark lifecycle exercises both revisions and modes, including
persisted failed runs and the actual Go reporter proxy, without external services.

The historical wall times above measure the earlier Python implementation. This
port does not establish new full-gate or `make test` timings. Final focused
verification after merging the requested main revision is recorded below.

### Verification after main integration

Port commit: `c621457d0251972e2bb5da4a29dd1598d3f6be89`.
Main integration: `6053fd083a85bee6cab1c82eefd2a5f85f3855f7`, merging the
requested `84340f187` into this branch without conflicts. This is a merge into
the work branch, not a merge to main or a push. The imported main changes are
broader than the tooling region; their product suites were not rerun here.

| Check | Result on integrated tree |
| --- | --- |
| `PATH="$HOME/go/bin:$PATH" PYTHONPATH=scripts python3 -m unittest discover -s scripts -p pre_merge_test.py` | 77 passed, 12.121 s, no skips |
| `PATH="$HOME/go/bin:$PATH" PYTHONPATH=scripts python3 -m unittest discover -s scripts/tests -p 'test_*.py'` | 36 passed, 0.325 s, no skips |
| From `scripts/verification`: `GOWORK=off "$HOME/go/bin/go" test -json -p=2 ./...` | 12 top-level tests / 21 pass events, zero failures or skips; package times 0.449 s and 1.143 s |
| Same module: `GOWORK=off "$HOME/go/bin/go" vet -p=2 ./...` | Passed |
| Same module: `GOWORK=off "$HOME/go/bin/go" build -p=2 ./...` | Passed |
| Same module, home Go first on PATH: `GOWORK=off "$HOME/go/bin/golangci-lint" run --concurrency=2 --config=../../.golangci.yml ./...` | Zero issues |
| `python3 -m py_compile scripts/pre_merge.py` and `gofmt -l scripts/verification` | Passed / no formatting output |
| `scripts/scrub.sh`, `git diff --check`, `scripts/check_links_test.sh`, and `scripts/check_links.sh` | Passed |

The Python discovery count falls from 44 to 36 because the eight sharder/timing
Python tests moved to Go; their coverage remains and the Go suite adds CLI,
proxy and benchmark lifecycle checks. Both `make check-other-scripts` and the
gate's `scripts` suite include the Go module tests, so the port does not leave
them outside future script verification.

Expected Go red proofs and selector/comparison mutations failed for their
intended assertions. Initial validation attempts also exposed incorrect module
working directories/configuration paths, an old Go on PATH, and new Go lint
requirements (checked diagnostics, error assertions, context APIs and explicit
local command/file access). Those were corrected; the final vet, build and lint
commands above passed. No full gate, frontend suite, production Go module suite,
or new benchmark was run for this port. Prior failed-gate measurements and their
coverage limits remain unchanged.

## Mandatory verification-module wiring, 2026-11-06

Merged the requested main `52cfe4f5` first, as work-branch merge
`36c56ecaacf972426dbe400bec8bc1c84854d527`. Main reported its candidate gate
at 31m11s versus 39m02s previously, with the module-wiring omission as its only
NO-GO reason. Those are Main's supplied results, not a new benchmark run here;
they are not substituted into the earlier controlled comparison above.

The verification module now participates in every requested entry point:

- `go.work` lists `scripts/verification` alongside contracts and core.
- A shared Makefile module list drives `make fmt`, `make check-format`, and
  all five module checks in `make lint`. Worker counts come from
  `BLERG_TOOL_WORKERS`, default two.
- `make test-verification` runs full vet, tests, and executable builds. Tests
  omit only duplicate implicit vet after the explicit full check succeeds.
  `make test` and `make check-scripts` both reach this single shared target.
- The pre-merge formatter includes the module. Its full matrix and legacy
  touched-tooling selection include a named `verification-go-lint` suite;
  the script suite invokes the shared vet/test/build target.
- CI's existing format step calls `make check-format`, its lint matrix has a
  verification entry using the common pinned configuration, and a dedicated
  setup-go job calls `make test-verification`. The dependency-free module's
  cache key uses its `go.mod` rather than a nonexistent `go.sum`.
- Test fixtures prefer the local home Go installation but fall back to PATH,
  so GitHub setup-go does not need to create `$HOME/go/bin/go`.

### Red/green proof and final checks

Before the wiring fix, new Go regression tests demonstrated that lint accepted
an unformatted verification file and that `go.work` omitted the module. A new
orchestration assertion demonstrated the missing pre-merge format directory.
Setup-go portability tests also reproduced the hardcoded-home-tool assumption.
All are green after the fixes. The maintained lint test uses the real Makefile,
formatter and pin checker in a temporary fixture, with only the expensive linter
replaced by a Go helper that records the visited modules.

The requested **actual `make lint` mutation proof** used the repository itself,
without fake tools. Changing `func main()` to valid but unformatted
`func main( )` in `scripts/verification/cmd/go-test-shards/main.go` made
`make lint` exit nonzero at `check-format`, print `gofmt needed:`, and name that
file. The original source was restored in a guaranteed cleanup block. Then:

```sh
PATH="$HOME/go/bin:$PATH" BLERG_TOOL_WORKERS=2 \
  GOLANGCI_LINT="$HOME/go/bin/golangci-lint" make lint
```

passed with five zero-issue Go lint results, all four frontend lint checks,
and the theme audit. Recorded wall time was **393.78 s**. The deliberate
mutation was never committed.

Other final verification:

| Command/check | Result |
| --- | --- |
| `PATH="$HOME/go/bin:$PATH" BLERG_TOOL_WORKERS=2 make test-verification` | Full module vet, tests, and builds passed |
| Verification module: `GOWORK=off go test -json -vet=off -p=2 ./...` after explicit vet | 16 roots / 25 pass events; zero failures or skips |
| Verification module, pinned shared lint config, concurrency two | Zero issues after the portability/test changes |
| `PYTHONPATH=scripts python3 -m unittest discover -s scripts -p pre_merge_test.py` | 78 passed, 11.811 s, zero skips (one new format-wiring regression) |
| `PYTHONPATH=scripts python3 -m unittest discover -s scripts/tests -p 'test_*.py'` | 36 passed, 0.470 s, zero skips |
| Workspace `go list ./scripts/verification/...` | Both command packages resolve from the repository root |
| `make -n test`, `make -n check-scripts`, and combined invocation | Shared verification vet/test/build appears exactly once per invocation |
| CI YAML parse and structural checks | Go setup, verification lint, shared format, and vet/test/build target confirmed |
| `scripts/check_lint_pin.sh`, `scripts/scrub.sh`, `git diff --check` | Passed |

The GitHub-hosted jobs and a new full pre-merge matrix were not run in this
session. The CI steps' shared local target was executed successfully. Existing
production tests were not dropped or rerun for this wiring change; the complete
`make lint` run above was explicitly required by the review.
