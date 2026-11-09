# BLG-459: desktop scheduling evidence

Measured November 6, 2026 on this Linux desktop host. This is local development evidence, not deployment or native macOS acceptance.

## Behavior

Desktop sessions default to lower priority. Linux/WSL uses a probed user systemd scope (`CPUWeight=10`, `IOWeight=10`), nice 5 and idle I/O. macOS selects taskpolicy background mode and nice 5. Available controls degrade individually when missing or refused, with safe cause logs. The desktop setting persists with the repos-root setting and applies to new sessions. Docker needs its own CPU/I/O weights and in-container wrappers; lowering the Docker client alone is insufficient. Cluster launch configuration stays unchanged.

The actual desktop driver boundary launches a shell and grandchild and observes both at lower CPU priority and the grandchild at idle I/O priority. The daemon process remains at its original nice value. A fixed systemd scope description avoids putting command arguments in unit descriptions.

## Responsiveness under worker load

Four CPU-bound Python workers and normal-priority Chromium share four logical CPUs of the 32-CPU host. The before workers run normally; after workers use the Linux policy above. Chromium renders Blerg's existing 50,000-event workspace with three panels and live worker updates. Native input is timestamped before renderer delivery. These are automated input samples, not a person physically typing. Other host workloads were not stopped. The same focused browser cases and budgets are used before and after.

| Measurement | Before, ms | After, ms | Budget, ms |
| --- | ---: | ---: | ---: |
| 390 px worker event delivery median | 12.60 | 11.50 | 100 |
| 390 px synchronous key median | 2.05 | 2.05 | 16 |
| 390 px native key-to-paint median | 66.85 | 48.10 | 100 |
| 3840 px worker event delivery median | 111.20 | 48.05 | 100 |
| 3840 px synchronous key median | 2.50 | 2.30 | 16 |
| 3840 px native key-to-paint median | 131.30 | 70.30 | 100 |
| Long-workspace switch median | 1834.90 | 1596.30 | 2000 |

Before: two cases passed and the wide typing case failed its event-delivery budget. After: all three cases passed. The after run took 41.30 s in Vitest, versus 58.47 s before. Suite duration includes setup and is not the independent test/gate throughput comparison below. Native maxima remain diagnostic: 294.8 → 141.4 ms at 3840 px. This is an improvement in medians, not a guarantee that every input stays below 100 ms.

The command, from `runner/frontend`, was:

```sh
npm run test:performance -- \
  src/components/LongSession.performance.browser.test.tsx \
  src/components/SessionSwitch.performance.browser.test.tsx \
  --maxWorkers=1 \
  -t 'budgets typing in a long live workspace|budgets switching into a long live workspace'
```

Both Vitest and its browser were launched with `taskset -c 0-3`. Four separate `python3 -c 'while True: pass'` workers used the same affinity. Only those workers received scheduler wrappers for the after run. Process IDs were retained and every load process was terminated and waited on after each run.

## Test and focused gate wall time

| Condition | Valid samples, normal / lowered | Tests normal → lowered, s | Change | Focused gate normal → lowered, s | Change |
| --- | ---: | ---: | ---: | ---: | ---: |
| No foreground measurement browser | 5 / 5 | 3.776 → 3.717 | -1.6% | 5.133 → 5.094 | -0.8% |
| Scripted typing/switching | 4 / 3 | 4.147 → 4.310 | +3.9% | 5.947 → 5.709 | -4.0% |


Warm caches, unchanged source, alternating normal/lowered order, five attempted samples per policy and condition. The foreground-active condition runs the same Blerg native typing/switch cases at normal priority alongside worker verification commands. Samples where that browser exited before measurement finished are excluded from the foreground-active comparison and retained in the raw table. “Idle” means no measurement browser; this shared host was not made otherwise idle.

The test command runs the existing engine/driver and sandbox-argument region five times with uncached test execution (`-count=5` is intentional for timing):

```sh
go test ./internal/daemon \
  -run '^(TestChooseAgentDriver|TestSandboxRunArgs.*|TestClaudeCodeDriverTurn|TestCodexDriverTurn|TestHermesDriverTurn|TestOpenclawDriverTurn)$' \
  -count=5 -timeout=3m
```

The focused gate consists of that test command, then `go vet ./internal/daemon ./internal/server ./internal/protocol ./cmd/daemon ./cmd/server`, then `go build -o <temporary-output> ./cmd/daemon`. Every measured command uses the pinned Go toolchain, `GOWORK=off` and four-CPU affinity. Lowered commands additionally run inside the same systemd/nice/ionice policy as desktop workers. This is deliberately a regional gate, **not** `scripts/pre_merge.py` or the full repository gate.

The pilot produced idle medians of 5.572 → 6.190 s for tests and 7.062 → 7.861 s for the focused gate; foreground-active medians were 4.763 → 4.574 s and 6.217 → 6.813 s. A source change during that pilot caused a 45.36 s cache miss; background verification also overlapped. Those observations do not establish steady-state overhead. They are retained here rather than hidden. The steady-state run above was made once after source edits and heavy verification stopped.

| Condition | Lowered | Sample | Test seconds | Focused gate seconds | Browser remained running |
| --- | --- | ---: | ---: | ---: | --- |
| No foreground browser | False | 1 | 3.776 | 4.903 | None |
| No foreground browser | True | 1 | 3.789 | 4.980 | None |
| No foreground browser | True | 2 | 3.710 | 5.221 | None |
| No foreground browser | False | 2 | 4.099 | 5.133 | None |
| No foreground browser | False | 3 | 4.086 | 6.030 | None |
| No foreground browser | True | 3 | 5.127 | 5.094 | None |
| No foreground browser | True | 4 | 3.699 | 4.939 | None |
| No foreground browser | False | 4 | 3.732 | 5.154 | None |
| No foreground browser | False | 5 | 3.654 | 4.678 | None |
| No foreground browser | True | 5 | 3.717 | 5.117 | None |
| Foreground active | False | 1 | 4.116 | 5.851 | True |
| Foreground active | True | 1 | 3.993 | 6.109 | True |
| Foreground active | True | 2 | 4.310 | 5.555 | True |
| Foreground active | False | 2 | 4.078 | 5.303 | False |
| Foreground active | False | 3 | 4.178 | 6.221 | True |
| Foreground active | True | 3 | 4.342 | 5.709 | True |
| Foreground active | True | 4 | 4.138 | 5.348 | False |
| Foreground active | False | 4 | 4.456 | 6.042 | True |
| Foreground active | False | 5 | 3.815 | 5.110 | True |
| Foreground active | True | 5 | 3.989 | 4.965 | False |


## Exact verification and limitations

All Go commands ran from `runner`, using `$HOME/go/bin/go`, `GOWORK=off`, and the dedicated test database supplied for this session. No unfiltered server-package run was made.

```sh
go test -json ./internal/daemon ./internal/server ./internal/protocol \
  -run '^(TestDesktopWorkerPriority.*|TestWorkerPriority.*|TestClusterEngineLaunchBoundary|TestChooseAgentDriver|TestUnknownAgentEngineNeverFallsBackToClaude|TestHostClaudeAgent.*|TestSandboxRunArgs.*|TestStartSandboxContainer.*|TestManagerPassesSandboxOptionsToBothKinds|TestAgentHostSandboxed.*|TestSandboxedAgent.*|Test.*ReposRoot.*|TestSessionPrivacy_EveryRouteHasADecision)$' \
  -timeout 3m
go test ./internal/runner -run '^TestClusterPreparedWorkspaceStartsAgentHost$' -timeout 3m
go vet ./internal/daemon ./internal/server ./internal/protocol ./cmd/daemon ./cmd/server
go build -o <temporary-daemon> ./cmd/daemon
go build -o <temporary-server> ./cmd/server
GOOS=darwin CGO_ENABLED=0 go build -o <temporary-macos-daemon> ./cmd/daemon
golangci-lint run --allow-serial-runners --config=../.golangci.yml \
  ./internal/daemon ./internal/server ./internal/protocol ./cmd/daemon ./cmd/server
```

Results: **127 Go tests/subtests passed, one opt-in containment test skipped**; daemon 5.767 s, server 8.025 s, protocol 0.019 s. The separate cluster-consumer test passed in 0.131 s. Vet, Linux daemon/server builds, Darwin cross-build and regional lint passed (zero lint issues). The Darwin build is compile evidence, not a native runtime test.

From `runner/frontend`:

```sh
npm run test:unit -- src/components/WorkerPrioritySetting.test.tsx \
  src/components/EngineStatusSection.test.tsx src/SettingsPrimitives.test.ts --maxWorkers=2
VITE_SETTINGS_SHOTS=.cache/blg459-settings npm test -- --project browser \
  src/components/WorkerPrioritySetting.browser.test.tsx --maxWorkers=1
npm run lint
npx tsc --noEmit
npm run build
```

Results: **47 unit checks passed** (7.03 s in the final run); **four browser layout/theme cases passed** (10.36 s). Screenshot PNG dimensions were checked as 390×844 and 3840×2160, in dark and color-blind dark themes; the images were inspected at native scale. Frontend lint and types passed. The production frontend build passed; Vite completed bundling in 8.22 s with its existing large-chunk warning.

Repository checks: `git diff --check` and `scripts/scrub.sh` passed. The optional repository-wide `scripts/check_links.sh` scan was stopped after several minutes; no pass is claimed. The changed guide and evidence file introduce no relative Markdown links.

Timing analysis used a locally built `runner/cmd/test-timing` against the focused `go test -json` output, because the installed CLI runtime lacked `blerg-test-timing`. Only one local report exists, so it cannot establish a historical test-time trend.


- Initial red proof: `TestDesktopWorkerPriorityInheritance` observed worker/child nice 0 and normal I/O. The implemented boundary passes. Mutation proof: temporarily forcing `yieldToPerson := false` reproduced nice 0 and normal I/O; the source was restored and the expanded focused checks passed.
- The real ptrace containment integration fails on this host with `PTRACE_SEIZE: operation not permitted`, both with and without scheduling wrappers. It is retained as `TestDesktopWorkerPriorityContained`, opt-in with `BLERG_PRIORITY_HELPER` pointing to a built daemon. This environment failure is not a passing containment result.
- Native macOS and real Docker image/runtime acceptance were not run. Platform command selection, fallback selection, Docker argument placement and unchanged cluster defaults have focused tests. No cluster pod policy was changed or deployed.
- Browser fixtures emit existing safe `session_mismatch` attention diagnostics. Vite reports existing native-config/dependency and chunk-size warnings. No budget was relaxed.
- The first settings test run caught a missing shared toggle class; the corrected checks pass. Go fixture compilation caught an unused import; it was corrected. Lint initially used the old system Go and was rerun with the pinned toolchain. A concurrent linter lock was handled by serializing the regional lint run.
- The full repository gate, production deployment, real-person interaction, physical-disk latency benchmark and native platform acceptance remain outside this evidence. I/O priority inheritance is verified; actual storage scheduling depends on kernel/device/filesystem support.
