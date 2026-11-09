# BLG-431 final foreground run — November 7, 2026

Measured tree: `28708e80c8239617042dbbb11a3559c20ff7c4c9`, including main's
BLG-584 render-isolation fix. One complete final run, no retries, from
13:40:58.567 to 14:17:40.962 UTC: **2,202.395 seconds (36 min 42.395 s)**.
The normal-priority systemd user unit exited 0. The acceptance JSON reports
`passed`, with an empty failures list. **This is not a claim that every recorded
production budget passed:** the typing-mark exception below needs Main's review.

Nice 0, fixture CPU affinity 0–3, one Vitest worker; loaded fixtures add four
native CPU workers at nice 5. Initial host load was 0.97/1.98/2.20. No other
heavy work was started by this session during measurement. Production follows
the synthetic load. Results are comparisons between trees/environments, not
isolated causal speedups. The earlier foreground report is
[preserved here](performance-acceptance-2026-11-07.md).

## Browser fixtures

Milliseconds; native typing is the median of 30 keys, switches five samples.
“Earlier” means the preceding foreground report, not v0.5.4.

| Measurement | Earlier quiet | Final quiet | Earlier loaded | Final loaded |
| --- | ---: | ---: | ---: | ---: |
| 390 px native key → paint | 64.90 | 48.30 | 64.40 | 48.50 |
| 3840 px native key → paint | 81.85 | 48.35 | 97.55 | 38.75 |
| 3840 px synchronous event delivery | 47.60 | 11.60 | 65.85 | 9.60 |
| 3840 px synchronous input handler | 1.90 | 1.90 | 1.75 | 1.75 |
| Long workspace switch → paint | 706.20 | 669.40 | 1003.90 | 986.20 |
| Warm Markdown main-thread tasks | 663.443 | 960.769 | 827.119 | 1017.538 |
| Cache-cleared Markdown main-thread tasks | 1080.479 | 1198.384 | 1161.317 | 1374.833 |
| First long Markdown mount → paint | 1097.30 | 1241.40 | 1271.30 | 1441.10 |
| 400-card board first card → paint | 235.80 | 281.40 | 288.50 | 343.80 |
| 400-card board fully revealed → paint | 264.80 | 310.90 | 330.50 | 368.90 |
| Insights open → paint | 174.60 | 173.00 | 192.20 | 183.50 |
| Graph open → paint, 67 commits | 99.10 | 76.50 | 98.30 | 69.50 |
| Board telemetry | 41.15 | 7.60 | 50.35 | 7.30 |
| Tab commit, 390 px | 99.50 | 70.30 | 123.50 | 106.50 |
| Tab commit, 3840 px | 95.00 | 67.70 | 84.30 | 76.40 |

Both browser runs: **10 passed, 0 failed, 32 filtered/skipped**, with the existing
assertions unchanged. Zero SessionDetail renders per typing sequence and zero
SessionBoardTabs renders during 400-card telemetry now pass, versus 30 each in
the earlier run. All four native medians are below 70 ms. Native maxima are
62.40/88.40 ms quiet and 67.80/110.30 ms loaded (390/3840 px); this is a median
budget, not a worst-case guarantee. Tab commits retain zero layout reads and
stay below 250 ms. Poll intervals are 15.0528/15.0039 s quiet and
15.0617/15.0027 s loaded. Warm Markdown switches reparse zero messages.

The original card's warm main-thread finding was about 1,100–1,300 ms. The final
960.769/1017.538 ms is below that range but **slower than the earlier foreground
run**; board opening and cold/first Markdown mounts also increased. These
remaining costs are not called resolved by a passing aggregate. The trace work
and rejected geometry optimization remain documented in
[session-switch-performance.md](session-switch-performance.md). Initial layout
geometry was preserved; the reverted deferred-geometry change was not restored.

Main's `00ff1d883` BLG-584 change stabilizes the attention snapshot instead of
subscribing question consumers to whole backing stores; subsequent main fixes
preserve runtime/start fields. The unchanged fast fixture first confirmed the
two render regressions were gone. Main then stopped historical bisection and
authorized this final run; no additional product patch or relaxed assertion was
needed on this branch.

## Boot and bundle

Both versions use cache-disabled navigation to an actual transcript event row
or folded tool group, then two animation frames. All three historical services
and frontends came from unmodified v0.5.4 (`2ca05a604`); the served runner index
was byte-checked against that build. Historical tool groups lack newer event
attributes, so the injected observer recognizes both native DOM forms.

| Boot | Three samples, ms | Median, ms |
| --- | --- | ---: |
| v0.5.4 | 3487 / 8237 / 8137 | 8137 |
| Final | 465 / 444 / 469 | 465 |

Final median is **7,672 ms lower (94.29%)** than v0.5.4. The 3,000 ms value is
an informational target, now met, and only the measured historical comparison
is the timing regression gate. Three trials do not establish statistical
confidence. The earlier 5,411 ms boot used different readiness; it is **not** a
like-for-like baseline for this new observer. The historical common-schema boot
seed has 446 sessions and 176,000 events, without newer-only card links; the
ordinary full current seed remains unchanged. See
[baseline JSON](boot-v0.5.4-2026-11-07.json) and
[harness details](performance-acceptance.md#historical-boot-comparison).

| Entry asset | Earlier | Final | Delta |
| --- | ---: | ---: | ---: |
| JavaScript raw bytes | 3,218,531 | 3,279,665 | +61,134 |
| JavaScript Go gzip bytes | 970,970 | 989,760 | +18,790 |
| JavaScript Node gzip bytes | 966,848 | 985,729 | +18,881 |
| CSS raw bytes | 378,070 | 394,733 | +16,663 |
| CSS Node gzip bytes | 66,653 | 69,355 | +2,702 |

Go gzip remains below the 1,100,000-byte entry-JavaScript gate. CSS Go gzip is
69,379 bytes. One entry script and stylesheet; no static preload chunks. Lazy
assets are excluded. Compressor-specific values are kept separate.

## Server, production, and soak

Five selected server/daemon tests passed, no skips; command wall time 55.614 s.

| Measurement | Earlier foreground | Final |
| --- | ---: | ---: |
| Initial state, median of three, ms | 180.674289 | 172.151109 |
| Initial-state queries per sample | 12 | 12 |
| Rich 100k-event live route, best of three, ms | 208.610310 | 200.544513 |
| Rich 100k-event topics route, best of three, ms | 101.748220 | 98.512215 |
| Simple live route, batch medians, ms | 48.996–49.844 | 47.812–50.037 |
| GC-normalized heap before/after 50 reads, bytes | 4,264,552 / 3,717,712 | 3,826,600 / 3,765,720 |

Initial-state samples: 185.767959 / 171.550242 / 172.151109 ms; 1,000 sessions,
750 visible. Goroutines remain 14. Daemon 300 × 64 KiB acknowledged payload
backing bytes remain zero; heap before/queued/acknowledged is
2,879,632 / 25,188,616 / 2,916,784 bytes.

Three production scenario runs, 11 measured samples each, eight scenarios:
264 measured samples plus 24 warm-ups. Primary run figures below; calibration
noise is the range of run medians. No official production baseline was promoted.

| Scenario | Earlier median / p95, ms | Final median / p95, ms | Final noise, ms |
| --- | ---: | ---: | ---: |
| blackboard | 54.800 / 80.900 | 112.800 / 130.100 | 6.000 |
| board-tab | 236.300 / 261.400 | 250.100 / 260.400 | 11.600 |
| cold-load | 5335.500 / 5345.400 | 382.000 / 408.300 | 3.000 |
| insights-7d | 166.300 / 198.900 | 187.100 / 203.400 | 6.000 |
| search | 224.600 / 242.400 | 220.100 / 240.500 | 9.500 |
| transcript-open | 141.100 / 204.800 | 143.900 / 162.300 | 8.400 |
| typing | 8.300 / 15.000 | 27.700 / 49.300 | 15.500 |
| workspace-switch | 119.800 / 298.700 | 108.700 / 173.200 | 12.900 |

**Production `key-committed` p95 exceeds its recorded 16 ms budget.** The
first-run candidate says `clean` without a prior official baseline; the
acceptance aggregate checks the 70 ms native medians but does not enforce this
candidate field. Therefore its `passed` status is narrower than “all budgets
pass.” This exception and Blackboard's increased median are sent to Main for
review, with no second full run. Insights p95 remains below 500 ms. Main's
Blackboard reset/setup behavior changed, so do not infer a product regression
from these two Blackboard numbers alone. Cold-load product/readiness changes
also prevent attributing its large improvement to this harness branch alone.

Soak: **1,800.080 s, 61 samples**. First/last are medians of the first/last three
samples. No process exceeds 64 MiB RSS growth; browser heap stays below 16 MiB.

| Bytes | First | Last | Growth |
| --- | ---: | ---: | ---: |
| Browser GC-normalized heap | 18,862,980 | 23,574,320 | +4,711,340 |
| Runner RSS | 62,070,784 | 70,049,792 | +7,979,008 |
| Core RSS | 34,377,728 | 34,127,872 | −249,856 |
| Board RSS | 27,627,520 | 29,057,024 | +1,429,504 |
| Existing daemon RSS | 60,743,680 | 59,809,792 | −933,888 |

300 soak native inputs: median 33.25 ms, p95 39.60 ms, maximum 84.10 ms at the
production default viewport. Zero recorded long tasks, no truncation. First-cycle
CPU sampling includes 241.401 ms of GC; minified frames do not establish
source-level causes. The existing daemon is sampled read-only, without synthetic
engine launches; RSS sampling is not a retained-heap profile or day-long soak.

| Completed request family | Count | Mean ms | Maximum ms |
| --- | ---: | ---: | ---: |
| board | 628 | 7.714 | 38.860 |
| live | 9847 | 28.620 | 2305.840 |
| other_api | 2016 | 16.638 | 39.335 |
| permissions | 421 | 22.842 | 39.837 |

Live requests average 5.470/s. The 2,305.840 ms maximum is retained. These are
browser request-family aggregates, not a global API ranking. The production
board seed is 150 cards; the separate fixture covers the required 400 cards.

## Exact validation and failures

Final fixture command, once quiet and once loaded:

```sh
npm run test:performance -w runner/frontend -- \
  src/components/LongSession.performance.browser.test.tsx \
  src/components/SessionSwitch.performance.browser.test.tsx \
  src/components/SessionTabSwitch.performance.browser.test.tsx \
  src/components/BoardGraphInsights.performance.browser.test.tsx \
  --maxWorkers=1 \
  -t 'budgets typing in a long live workspace|budgets switching into a long live workspace|reuses finished Markdown|measures opening|defers tab strip|measures polling demand|keeps the 400-card board asleep' \
  --reporter=default --reporter=json --outputFile.json=<private-evidence-file>
```

Each 10 pass, 0 fail, 32 excluded; quiet 82.149 s, loaded 89.792 s. Server command
from `runner/`, with the dedicated test database variables set privately,
`GOWORK=off`, temporary `GOCACHE`, and the pinned Go:

```sh
$HOME/go/bin/go test ./internal/server ./internal/daemon \
  -run '^(TestGetLiveOnALongSession|TestGetTopicsOnALongSession|TestLivePollingRetainedHeap|TestInitialStateSeededConnect|TestEmitterAckReleasesPayloads)$' \
  -json -count=1 -timeout 10m
```

Final orchestration used the built Go `performance-acceptance` command with
`--daemon-pid <existing-daemon-pid>`
`--boot-baseline docs/boot-v0.5.4-2026-11-07.json`
`--output <private-results.json>`. Production builds all three frontends
and builds all three Go servers; it ran in 1,974.831 s. No unrelated suites ran.
The orchestrator invokes Vite directly and does not run `tsc`; these builds
must not be reported as new type-check evidence.

Focused tooling validation before that run:

```sh
node --test runner/frontend/scripts/release-perf-acceptance.test.mjs runner/frontend/scripts/release-perf-acceptance.browser.test.mjs runner/frontend/scripts/redact.test.mjs runner/frontend/scripts/serverTiming.test.mjs
go -C scripts/verification test -json ./cmd/performance-acceptance
go -C runner test -json ./cmd/release-perf
go -C runner test -json ./internal/releaseperf
go -C scripts/verification vet ./cmd/performance-acceptance
go -C runner vet ./cmd/release-perf ./internal/releaseperf
go -C scripts/verification build -o <temporary-executable> ./cmd/performance-acceptance
go -C runner build -o <temporary-executable> ./cmd/release-perf
golangci-lint run ./cmd/release-perf/... ./internal/releaseperf/...
golangci-lint run ./cmd/performance-acceptance/...
npm run lint -w runner/frontend
node --check runner/frontend/scripts/release-perf-playwright.mjs
node --check runner/frontend/scripts/release-perf-acceptance.mjs
scripts/scrub.sh
git diff --check
```

Go means `$HOME/go/bin/go`, `GOWORK=off`, temporary cache; lint runs from its
respective module. Node 15/15, verification Go 14/14, command Go 8/8 (cached),
internal releaseperf Go 47/47; zero failures/skips. Vet/build/syntax/lint passed,
focused Go lint zero issues. After the main merge, the earlier Node 14/14,
verification 12/12 and release-perf 3/3 checks also passed. Tests grew with the
historical compatibility changes; these counts are not distinct full-suite runs.

Test-first failures covered legacy readiness, cache environment forwarding,
portable seed card links, historical working-directory isolation, informational
3 s policy, baseline-field privacy, and rejection of the wrong served index.
Their restored/fixed focused reruns passed. Historical setup attempts failed
before an accepted measurement: database readiness; newer schema dependency;
Main versus Chat/Pinned accessible labels; missing old sidebar tooltip; legacy
runner ignoring the web-dist override; and folded tool groups without event IDs.
The accepted v0.5.4 run followed those harness fixes; no historical product
changes were made. The final full run had no test/process failures or reruns.
Receipt import initially rejected abbreviated commit IDs; full SHA imports were
retained. This reporting failure did not rerun any benchmark.

## Cleanup and evidence

Initial C: availability 66,052,542,464 bytes; lowest periodic observed reading
63,105,265,664 bytes; after cache/database cleanup 63,102,197,760 bytes. Every
reading stayed above 50 GB (the launcher conservatively requires 50 GiB).
The final temporary Go cache and every historical-attempt cache were verified
absent. The owned PostgreSQL tmpfs container was stopped/removed; the real daemon
was left running. Private launchers containing test connection settings and
temporary executables were deleted. Earlier launcher code discarded child Go
cache controls; this branch corrects that, so the final builds inherit the
temporary cache and worker limits. The older report's wrapper-cache cleanup
does not prove its child builds used that cache.

Private evidence: `.cache/foreground-final-20261107/`, including consolidated
`results.json`, browser receipts, route JSON log, production samples and soak.
Published consolidated JSON omits raw service logs and host fingerprints. Main
handles cross-model review and merge; this report does not close BLG-431 or
claim review approval for the new historical harness changes.

Implementation commits since the approved tooling: merge `2fc31523a`;
cache controls `aeeca9b44`, `0de9b9f0b`; historical boot and compatibility
`0443b728f`, `21a7d6763`, `ce772e7e5`, `655867e86`, `866fd09de`, `f996840e6`,
`54395dec8`, `df99741a3`; baseline policy `1aaa26960`; merge `28708e80c`.
The final measurement source is the last of these; the subsequent report commit
changes documentation only.
