# Session switch measurement (BLG-306)

Measured on 2026-11-06 against main `3db0ff82785800c2b91d17af047a3cb31b1aebad`
(chat 1.59.52; Node 26.8.2, Vitest 4.1.11, Playwright 1.60.0,
Chromium 153.0.8010.12 on Linux). Both requested fixes already exist on this main:

- `41bb90afaf1a390d5f4755b5be47419786fa5e70` caches rendered Markdown, bounded by
  4 million source/key characters, with a 200,000-character per-text limit.
- `4473024b18bc19c1762f35cdef1627b2437e17e0` moves the strip's initial measurement
  into ResizeObserver delivery. Later review fixes retain that behavior.

This change adds measurement and regression coverage; it changes no production
rendering or interaction. The old defects did not reproduce on current main.

## Reproduce

From the repository root, after `npm ci` and installing Playwright's Chromium:

```sh
VITE_SWITCH_TIMINGS="$PWD/.cache/session-switch.json" \
  npm run test:performance -w runner/frontend -- \
  src/components/SessionSwitch.performance.browser.test.tsx \
  src/components/SessionTabSwitch.performance.browser.test.tsx \
  -t 'reuses finished Markdown|defers tab strip' --maxWorkers=1
```

The route test uses the actual SessionDetail and transcript, 600 distinct finished
Markdown messages (300 mounted), and a 1440×900 viewport. It alternates five warm
switches with five cache-cleared switches. The latter is a control using the
existing `forgetMarkdown()` API; it is not an old release or a change to main.
Each sample starts after switching to the empty session and ends after returning
to the long session and two animation frames. Rendered transcript text must stay
identical. The warm path must parse zero messages; each cold path parses at least
300. This proves reuse through the app's actual route, beyond component remounts.

`workspaceTaskTime` reads Chromium CDP `Performance.getMetrics` before and after
the interaction. `TaskDuration` deltas, converted from seconds to milliseconds,
measure renderer main-thread task time, including script, style and layout;
idle waits are excluded. Script, style and layout deltas are also retained, but
are not added to TaskDuration (they overlap it). No CPU profiler is enabled.
The renderer also runs React development code and the Vitest harness, including
DOM queries. Task time is not paint latency, pure application CPU time, or a
production-browser benchmark. This shared host has substantial scheduling noise.

The separate strip test mounts 30 tabs alongside 3,000 container-query elements,
switches seven times at each of 390 and 3840 px, and asserts no commit-time strip
`clientWidth` reads, no observer loop errors, and correct active-tab visibility.
Its reported synchronous commit duration is a different metric from TaskDuration.

## Results

The first isolated current-main run measured a **1304.8 ms** warm-switch median
and **1831.7 ms** cache-cleared median. Warm samples were
1304.8, 1449.9, 1930.2, 1019.4 and 942.8 ms; cold samples were
1831.7, 1836.2, 1968.8, 1641.6 and 1673.2 ms. Every warm switch parsed zero messages;
every cold switch parsed 300. The strip measured 94.8/69.9 ms median commits at
390/3840 px, with zero commit-time width reads at both widths.

These synthetic development-build numbers cannot be compared directly with the
owner's historical approximately 400 ms Firefox profile. No new production
speedup is claimed. The controls and exact reparse/read counts establish that
both original sources of unnecessary work are absent.

The final complete touched-region run measured **1148.9 ms** warm and **1534.1 ms**
cache-cleared median task time. Warm samples were 1009.9, 1098.1, 1195.2, 1148.9
and 1347.0 ms; cold samples were 1379.5, 1593.8, 1533.4, 1722.9 and 1534.1 ms.
The before/after current-main warm medians are therefore 1304.8/1148.9 ms.
Production code is identical in both runs: that difference is run variance,
not an improvement introduced by this commit. The final within-run cache-cleared
control used 385.2 ms more median task time. Exact counts remained zero versus
300 reparses per switch. Strip commits were 109.6/139.1 ms at 390/3840 px with
zero commit-time width reads.

## Regression sensitivity

Temporarily bypassing the Markdown cache hit and calling `measure(); reveal(element)`
in the strip's layout effect made all three regression cases fail as intended:
300 reparses on every warm route switch, and 21 commit-time width reads across
seven switches at each viewport. Restoring main made all three pass. Neither
mutation is included in this change.

## Verification

All commands run from the repository root. No backend tests, full frontend gate,
full browser suite, or unrelated performance files were run.

| Command | Result |
| --- | --- |
| `npm test -w packages/chat -- src/components/Markdown --maxWorkers=1` | 6 files, 145 tests passed; 23.66 s wall time (12.45 s test time) |
| `npm run test:unit -w runner/frontend -- src/components/SessionTabBar.test.tsx src/hooks/useSessionTabs.test.tsx --maxWorkers=1` | 2 files, 48 tests passed; 7.37 s |
| `npm test -w runner/frontend -- --project browser src/components/SessionTabBar.browser.test.tsx src/components/SessionTabOverflow.browser.test.tsx src/components/WorkspaceSessionLabels.browser.test.tsx --maxWorkers=1` | 5 browser/file combinations, 90 tests passed in Chromium and Firefox; 40.88 s |
| `VITE_SWITCH_TIMINGS="$PWD/.cache/blg306-final.json" npm run test:performance -w runner/frontend -- src/components/SessionSwitch.performance.browser.test.tsx src/components/SessionTabSwitch.performance.browser.test.tsx --maxWorkers=1` | 2 files, all 6 tests passed, no skips; 58.45 s |
| `npm run lint -w runner/frontend` | Passed, zero warnings |
| `npm run build -w runner/frontend` | TypeScript (`tsc -b`) and Vite build passed; existing large-chunk warning |
| `scripts/scrub.sh` | Passed (`scrub: clean`) |
| `git diff --check` | Passed |

The mutation run used the reproduction command above and failed all three selected
cases for the intended assertions (three unrelated cases skipped by the filter).
The restored focused run passed all three, also with three filtered skips.

An initial attempt could not start because dependencies were absent (`vitest not
found`); `npm ci` resolved it. The first existing heavy-workspace test exceeded
its unchanged 2000 ms paint budget (2327.5 ms median). The final full touched-region
rerun passed it at 1140.5 ms. This is timing variability evidence, not proof of a
flake or a product speedup. The shared fixture logs `session_mismatch` for mocked
attention reads; jsdom logs its existing canvas/localStorage diagnostics. No new
failure suppression or timing budget change was made.

## BLG-431 follow-up: group geometry (2026-11-06)

Baseline tree: `52389776e`. Same Chromium development harness, on a shared,
already busy host (one-minute load about 18); this is not a quiet baseline.
A single warm-switch CPU/timeline trace is available by setting
`VITE_LAUNCH_SHOTS_DIR` in the reproduction command. Only the first warm
switch is traced. Do not compare its task time with unprofiled samples.
Tracing all ten switches exceeded the harness WebSocket payload limit;
narrowing to one succeeded (14 MB trace, kept outside tracked files).

The trace's largest task was 4856.5 ms with profiling enabled. CPU samples
attribute 611.5 ms inclusive to `SessionGroups`' initial `measure`, which
reads `clientWidth`/`clientHeight` during the mount layout effect and forces
layout of the freshly mounted transcript. The trace includes 425.5 ms of
layout and 188.0 ms of style update in that task. React development
`jsxDEV` is also prominent (1845.5 ms self samples); these sampled times
include profiler overhead and must not be added to the unprofiled totals.
Markdown reparses remained zero. Store delivery is not the dominant stack.

The group now waits for its ResizeObserver's initial delivery, as the tab
strip already does. A new performance regression test failed with three
commit-time size reads at each of 390/3840 px, then passed with zero reads
and verified the resulting group fills its container at both widths.

Unprofiled five-warm/five-cold runs, one cause changed:

| Metric | Before | After group geometry |
| --- | ---: | ---: |
| Warm median main-thread task ms | 1106.3 | 1070.6 |
| Cache-cleared median task ms | 1291.1 | 1393.9 |
| Warm reparses per switch | 0 | 0 |
| Cold reparses per switch | 300 | 300 |

The 3.2% warm difference is within prior run variance; this fix removes
an early synchronous layout boundary, not the cost of mounting the DOM.
It does not establish a large total CPU improvement.

### Text links: avoid an unused image portal destination

The trace also samples `MarkdownLink` (164.2 ms inclusive) and
`MarkdownLinkInner` (211.7 ms inclusive, overlapping the former). Every
link registered a DOM destination in React state for a possible linked-image
Retry button, even when it contained only text. Registering 300 destinations
caused a nested commit after mounting the transcript. The link now allocates
that destination only if its parsed subtree contains an image. Image Retry
remains outside the enclosing anchor, including sanitized HTML documents.
No sanitized HTML or context-dependent rendering is cached as DOM.

A Profiler regression test first observed `mount, nested-update` instead of
`mount`. It passes with only `mount`; temporarily routing all links back
through the destination component reproduces the failure. The complete
Markdown region passes (6 files, 146 tests, 22.73 s, including linked-image
Retry behavior).

With only this cause changed after the geometry commit, the same unprofiled
switch test measured warm **1070.6 → 1029.5 ms**, cache-cleared
**1393.9 → 1320.6 ms**; reparses remained 0/300. These small differences are
not distinguishable from shared-host variance. The deterministic improvement
is removal of the unnecessary nested commit for text links; no large
production-browser speedup is established. Most remaining work is mounting
300 message rows and their Markdown DOM through development React, then
style/layout. Retaining whole session trees or reducing the transcript
window would require separate correctness work for focus, scroll, live
subscriptions and memory; neither is silently changed here.

### Daemon: release acknowledged payload backing storage

The pending-event FIFO removed acknowledged entries by shortening its slice
without clearing the now-unused backing slot. Out-of-order acknowledgments
could leave every completed payload reachable for the remaining lifetime of
the emitter. A new regression emits 300 distinct 64 KiB messages, acknowledges
a middle entry (checking survivor order), then acknowledges the rest in reverse
order. It failed with **19,709,290 raw payload bytes** still held, despite zero
pending events. Removing the fix reproduces the same failure.

The acknowledgment now shifts survivors, clears the vacated slot and shortens
the slice under the existing lock. Persistence, replay order and backpressure
are unchanged. The same workload measured:

| GC-normalized heap bytes | Before fix | After fix |
| --- | ---: | ---: |
| Before events | 3,327,528 | 3,327,608 |
| Events queued | 25,721,000 | 25,646,760 |
| All events acknowledged | 25,415,328 | 3,366,032 |
| Retained raw payload bytes in backing storage | 19,709,290 | 0 |

This is an in-process event-buffer measurement, not a whole-daemon day-long
soak. The regression asserts payload ownership and order rather than a noisy
process-wide heap threshold. The daemon must be rebuilt/reinstalled to use it.

### Board: worker telemetry must not redraw 400 cards

The native-input harness exposed another concrete cost: 30 worker telemetry
broadcasts caused **12,000 `BoardCard` renders** (400 per broadcast), even
without a board poll or a changed worker badge. Both `SessionBoardTabs` and
`BoardTab` subscribed to the entire session array but used it only for each
live child's `worker_kind`. A new isolated test failed with 30 board renders
and a **26.3 ms** median synchronous broadcast. This isolates the cause from
the harness's separate board-poll work.

Both subscriptions now select only the requested children's glyph kinds,
with shallow equality. Titles, statuses, ticket assignments and asking state
continue to come from the live snapshot. The same test passes with **zero
board/card renders** and a **7.7 ms** median broadcast (71% less synchronous
work in this run). Changing a worker kind still redraws the board; a unit
test also checks rebinding to another child, removal, and stable identity
through unrelated telemetry/title changes. Reintroducing session-array
identity into the selection makes the regression fail again.

## Acceptance measurements after the four fixes

All samples below are synthetic development-browser measurements, not a
production deployment acceptance run. Requests use the existing canned APIs.
The final quiet/loaded comparison uses four-CPU affinity; the quiet run began
at one-minute host load 3.02 and ended at 4.22 (the measurement itself adds
load). The loaded run adds four CPU-bound Python workers on the same CPUs,
with idle I/O priority and nice increased by 5. The executor itself inherits
nice 5, so those workers run at nice 10. Processes were terminated and waited
on in `finally`. Other people's workloads were neither stopped nor changed.

| Measurement (ms) | Quiet | Added CPU load | Gate |
| --- | ---: | ---: | ---: |
| 390 px native key → paint, median of 30 | 48.15 | 72.35 | 100 |
| 3840 px native key → paint, median of 30 | 48.20 | 87.45 | 100 |
| 3840 px synchronous worker event delivery | 9.80 | 8.90 | 100 |
| 3840 px synchronous composer input | 2.00 | 2.00 | 16 |
| Long workspace switch → paint, median of 5 | 778.70 | 1128.70 | 2000 |
| Warm Markdown route main-thread tasks, median of 5 | 926.23 | 1071.61 | 1600 |
| Cache-cleared route main-thread tasks, median of 5 | 1156.61 | 1529.64 | diagnostic |
| First long Markdown route mount → paint | 1411.10 | 1568.60 | diagnostic |
| 400-card board: first card → paint | 283.50 | 393.90 | 1000 |
| 400-card board: all cards → paint | 325.00 | 439.50 | diagnostic |
| Insights open → paint | 177.80 | 352.80 | 1000 |
| Graph open → paint (67-commit fixture) | 70.30 | 127.20 | 1000 |

Board/Insights/graph now wait for the same two-animation-frame paint
opportunity used by switching/typing before recording their endpoint. Their
old 15-second smoke limits are replaced by one-second view budgets. Tab-strip
commits also have a 250 ms median budget, while exact zero commit-time reads
and overflow visibility remain enforced. No existing typing budget was raised.
The first-load number starts at mounting the already-imported route; it does
not include network transfer, JavaScript boot or signing in.

Both comparison commands passed the timing cases but failed the new board
counter, which initially counted unrelated asynchronous initial loads/clock
commits between frames. The counter now attributes renders immediately around
each synchronous delivery, matching the existing typing harness's boundary.
The old broad-store selection still fails with 12,000 card renders when
mutated back in; the restored selection passes. These earlier timing samples
are retained, not silently relabeled as fully green suite runs.

### Loaded native input: alternating original/fixed tree comparison

Because the loaded result above exceeds the historical approximately 70 ms,
a further three-pair comparison alternates `before/after`, `after/before`,
`before/after`. Before is an isolated archive of `52389776e`, with the same
installed dependencies; after is this branch. Only the existing 3840 px native
typing case runs. Four CPUs that were idle during a one-second observation
are selected, then both browser and four lowered-priority load processes use
that same affinity throughout all pairs. This is a controlled synthetic
comparison, not proof of an otherwise idle host.

| Pair | Original tree native median ms | Fixed tree native median ms |
| --- | ---: | ---: |
| 1 | 123.50 | 89.65 |
| 2 | 112.80 | 69.05 |
| 3 | 101.30 | 78.40 |
| Median of the three run medians | 112.80 | 78.40 |

Every original case fails its unchanged 100 ms native-input budget; every
fixed case passes. The paired median improves 30.5%, but **70 ms or better is
not certified**. The earlier loaded measurement before the board fix was
70.75 ms, and after runs range 69.05–89.65 ms; host/run variance is material.
These runs inherit nice 5 for Chromium, unlike the foreground-browser setup
in [BLG-459's evidence](desktop-priority-evidence.md). A normal-priority
foreground measurement is still needed. A user-systemd foreground-service
probe failed because the executor has no user bus; no priority or machine
configuration was changed to work around that boundary.

### Polling demand

A new real-clock browser test waits for the third board response (initial
read plus two automatic polls), rather than sleeping or accelerating timers.
One visible long workspace with board and Insights panels recorded 33.14 s:
3 board reads, 8 permission reads, 2 activity reads, 2 Insights reads and
9 live reads. Board intervals were 15.374 and 15.007 s. Counts include initial
mount reads and only the selected endpoint families, not all traffic.
The board test enforces one poll chain, not one per card or retained worker.

This is observed browser → runner demand with canned answers; it is not a
measurement of daemon filesystem work or a multi-browser server load test.
The source still bounds `/live` starts by a 5 s minimum gap per active session,
Insights refreshes every 30 s while visible/enabled, and a board refresh
performs a permission read plus paginated card reads, backing off on failure.
Multiple visible boards can multiply those reads; no live-install sampling
or production-wide "slowest route" ranking is claimed.

### Server route and retained-heap measurements

The focused Go measurements use isolated test databases, real authenticated
handlers and the existing 100k-event fixtures. The simple heap fixture is
intentionally separate from the richer subagent/task/ask fixture; their route
times answer different questions. Four-CPU control/loaded runs use the same
load policy as above. `-count=1` is intentional for fresh timing, not a
routine cache bypass. All four selected tests pass in both conditions.

| Server measurement | Control | Added CPU load |
| --- | ---: | ---: |
| 1000 sessions / 750 visible: connect → initial_state median of 3 | 118.99 ms | 186.80 ms |
| Data queries per initial_state sample | 12 | 12 |
| Rich 100k-event `/live`, best of 3 | 379.78 ms | 311.56 ms |
| Rich 100k-event `/topics`, best of 3 | 173.36 ms | 153.70 ms |
| Simple 100k-event `/live`, ten-read batch upper medians | 51.48–54.94 ms | 51.43–53.40 ms |
| GC-normalized heap after warm-up | 3,683,920 bytes | 4,221,840 bytes |
| GC-normalized heap after 50 additional reads | 3,670,352 bytes | 3,677,848 bytes |
| Goroutines throughout heap samples | 14 | 14 |

The server heap samples show no increasing retained-heap trend in this short
workload. They do not establish a day-long process-wide leak guarantee.
The rich `/live` fixture remains above its optional 200 ms target; its normal
2 s gate passes. It is an open finding on BLG-431, not a speedup from these
frontend/daemon fixes. Reducing additional database round trips needs its own
query profile and privacy/correctness regression; it was not folded into an
unrelated browser commit.

### Bundle size and first-load limitation

The original tree was archived separately and built with the same installed
Vite/dependencies and default build options. Final runner and board consumer
builds include TypeScript checks; the isolated baseline invokes Vite directly
for byte comparison. A first baseline attempt omitted shared brand CSS and
failed; including the tracked `brand` directory fixed it.

| Runner asset | Original raw bytes | Final raw bytes | Original gzip bytes | Final gzip bytes |
| --- | ---: | ---: | ---: | ---: |
| Entry JavaScript | 3,208,245 | 3,208,549 | 964,469 | 964,524 |
| Entry CSS | 376,174 | 376,174 | 66,272 | 66,272 |

Gzip values use the same local Node `zlib.gzipSync` defaults on both files;
they can differ from Vite's displayed compression estimate. Entry JavaScript
increases 304 raw / 55 gzip bytes; no bundle-size improvement is claimed.
The entry remains large. A production first-navigation/transfer benchmark
and an enforceable built-asset size budget remain open; synthetic route-mount
timing does not substitute for them. Lazy-loading more editor/viewer code
requires an entry-path/focus/style correctness audit rather than silently
removing features. Chat's package revision is 1.59.53 because its changed bytes
must not reuse the immutable 1.59.52 tarball identity.

## Final regional verification for BLG-431

No full repository gate, ordinary browser suite, unrelated frontend region,
unfiltered server package, deployment, merge or push was run. Go uses
`$HOME/go/bin/go`, `GOWORK=off`, and the supplied dedicated test database in
both `TEST_DATABASE_URL` and `DATABASE_URL`. Commands below are from the
repository root unless marked `runner/`.

| Command | Result |
| --- | --- |
| `npm test -w packages/chat -- src/components/Markdown --maxWorkers=1` | 6 files, 146 passed, no skips; 22.73 s |
| `npm run test:unit -w runner/frontend -- src/hooks/useSessionTabs.test.tsx src/hooks/useSessionFocus.test.tsx src/lib/sessionGroups.test.ts src/components/SessionTabBar.test.tsx src/hooks/useBoardWorkerKinds.test.tsx --maxWorkers=1` | 5 files, 55 passed, no skips; 10.22 s |
| `npm run test:unit -w runner/frontend -- src/components/SessionBoardTabs.test.tsx --maxWorkers=1` | 1 file, 17 passed, no skips; 7.86 s (72 distinct runner unit cases across the two commands) |
| `npm run test:performance -w runner/frontend -- src/components/LongSession.performance.browser.test.tsx src/components/SessionSwitch.performance.browser.test.tsx src/components/SessionGroups.performance.browser.test.tsx src/components/SessionTabSwitch.performance.browser.test.tsx src/components/BoardGraphInsights.performance.browser.test.tsx --maxWorkers=1 --reporter=default --reporter=json --outputFile.json="$PWD/.cache/blg431-performance-final.json"` | 5 files, all 44 passed, no skips; 187.38 s |
| `VITE_LAUNCH_SHOTS_DIR="$PWD/.cache/blg431-layout" npm run test:performance -w runner/frontend -- src/components/LongSession.performance.browser.test.tsx -t 'keeps the 400-card board asleep' --maxWorkers=1` | 1 passed, 32 filtered skips; 12.66 s. Screenshots inspected at encoded 390×844 and 3840×2160; composer, transcript and wide board/Insights panels remain present |
| `runner/: go test ./internal/daemon -run '^(TestEmitterAckReleasesPayloads\|TestAgentHost.*\|TestHostStopBypassesFullPendingEventBuffer\|TestStopNoticesKeepPendingReserveBounded\|TestEndToEndAgentSession)$' -v -timeout 3m` | 38 top-level cases (40 including subtests) passed, no skips; package 8.480 s |
| `runner/: go test ./internal/server -run '^(TestGetLiveOnALongSession\|TestGetTopicsOnALongSession\|TestLivePollingRetainedHeap\|TestInitialStateSeededConnect)$' -v -timeout 5m` | All 4 passed, no skips; package 30.368 s |
| Same server filter with `-count=1`, four-CPU affinity, control then added CPU load | All 4 passed in each condition, no skips; 30.28 / 34.02 s command wall time. Uncached execution is intentional for timing |
| `npm run lint -w packages/chat`; `npm run lint -w runner/frontend` | Passed with zero warnings; after the final counter-only edit, `npm exec -w runner/frontend -- eslint src/components/LongSession.performance.browser.test.tsx --max-warnings 0` also passed |
| `npm run typecheck -w packages/chat`; `npm exec -w runner/frontend -- tsc -b` | Passed |
| `npm run build -w packages/chat`; `npm run build -w runner/frontend`; `npm run build -w board/web`; `npm run pack:manifest -w packages/chat` | All passed; existing large-chunk warnings. Local immutable package manifest/tarball is 1.59.53 |
| `runner/: go vet ./internal/daemon ./internal/server` | Passed |
| `runner/: PATH="$HOME/go/bin:$PATH" GOWORK=off "$HOME/go/bin/golangci-lint" run --allow-serial-runners --config=../.golangci.yml ./internal/daemon ./internal/server` | Passed, zero issues, pinned v2.13.1 |
| `runner/: go build ./cmd/daemon ./cmd/server` | Passed |
| `scripts/scrub.sh`; `git diff --check` | Passed |

The exact three-pair loaded typing filter is
`src/components/LongSession.performance.browser.test.tsx -t 'budgets typing in a long live workspace at 3840px' --maxWorkers=1`.
Each original-tree command fails its native timing assertion (31 other cases
filtered); each fixed-tree command passes (32 filtered). These are controls,
not failures hidden from the final-tree result. The broader comparison uses
`LongSession`, `SessionSwitch`, `SessionTabSwitch` and `BoardGraphInsights`,
with `-t 'budgets typing in a long live workspace|budgets switching into a long live workspace|reuses finished Markdown|measures opening|defers tab strip|keeps the 400-card board asleep'`;
8 timing cases passed in each condition, and the counter failed before its
attribution correction. That correction's mutation fails for the original
12,000-render reason; its restored focused run and final complete region pass.

For a CPU/timeline trace, set `VITE_LAUNCH_SHOTS_DIR` and run the original
`reuses finished Markdown` reproduction filter above; the first warm switch
writes `markdown-switch.trace.json`. Trace runs are diagnostics and do not
enforce the unprofiled 1600 ms budget. Keep trace files private/outside tracked
files: development traces contain source locations. The real-clock polling
case intentionally exceeds the usual ten-second implementation-test starting
budget: observing two real 15-second poll intervals took 33.30 s. It awaits
a response-count signal with a failure deadline, not a timing sleep.

The test-timing wrapper could not find its installed reporting binary. The
repository's read-only fallback succeeded:
`runner/: GOWORK=off go run ./cmd/test-timing --vitest-json runner-performance=../.cache/blg431-performance-final.json --vitest-json runner-unit=../.cache/blg431-unit-final.json`.
Only current regional JSON is available here; it cannot establish a historical
gate-speed trend. It runs no tests.

Expected red/mutation failures cover group geometry, ordinary-link nested
commits, acknowledged-payload retention, and board telemetry subscriptions.
An initial daemon-fix command used the wrong working directory and reran the
unfixed failing case; the corrected path and restored region passed. Other
environment/tool failures were the oversized trace WebSocket message, missing
brand CSS in the first baseline archive, old system Go selected by the first
lint attempt, missing installed timing-report binary, and unavailable user
systemd bus. The daemon restart interrupted the board consumer build; its
rerun passed. No delivered mutation, skipped required regional check, raised
existing timing limit, or suppressed product failure is included.

### Still required before closing BLG-431

- Verify native typing at **70 ms or better** with a foreground-priority
  browser on the intended quiet/loaded benchmark host. Current controlled
  comparisons prove improvement and pass the existing 100 ms gate, not that
  tighter requirement.
- Profile production first-navigation/boot and establish a built-asset size
  budget. The route-mount and bundle byte measurements above are narrower.
- Retain a sustained whole-server/daemon soak with heap snapshots; the short
  100k-event polling and event-buffer tests do not cover every long-lived cache
  or engine lifecycle.
- Profile the rich `/live` route's remaining over-200 ms work, and the remaining
  development transcript DOM mount/style/layout cost. Whole-session retention
  needs separate focus, scroll, privacy and memory correctness evidence.
- Independent/person review of the concrete diff before the 0.6.0 cut. This
  branch is committed for review, with no merge or push and no claim that the
  full card's acceptance list is complete.

## November 7 foreground follow-up

The normal-priority quiet-machine run and full 30-minute production soak are
now measured in [the foreground report](performance-acceptance-2026-11-07.md),
including exact commands, baselines, failures, skipped tests and cleanup.
Warm Markdown main-thread work is 663.44 ms quiet / 827.12 ms loaded, with zero
reparses. Wide native typing is 81.85 / 97.55 ms, so the 70 ms requirement remains
unmet. The three production boot trials also exceed the 3 s script budget, and
three render-count assertions fail in each browser condition. All five focused
route/daemon tests and the full 1,800.101 s soak complete; memory-growth budgets
pass. These results supersede the earlier "not measured" status for boot,
bundle and sustained soak, without reclassifying acceptance as passed.

The [final November 7 run](performance-acceptance-final-2026-11-07.md) supersedes
those follow-up results: main's render fix passes the unchanged assertions;
native typing is 48.30/48.35 ms quiet and 48.50/38.75 ms loaded (390/3840 px).
Warm switch work is 960.769/1017.538 ms, slower than the earlier foreground run
but below the original 1.1–1.3 s finding. The 3 s boot value is a target;
equivalent readiness measures 465 ms median versus v0.5.4's 8,137 ms. The final
aggregate passes, with the production typing-mark p95 exception documented
separately; this does not claim every recorded budget passes.
