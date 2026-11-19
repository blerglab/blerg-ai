# BLG-431 foreground measurements — November 7, 2026

Historical failed attempt. See the [final run](performance-acceptance-final-2026-11-07.md)
for main's render-isolation fixes, the measured v0.5.4 boot comparison, and the
single final acceptance. The 3 s boot figure below was subsequently classified
as a target, not a regression gate; the old boot readiness differs. Also see
the final report's correction concerning child Go build-cache inheritance.

Measured from this session worktree. Browser/route tree: `2719023f1`;
production retry tree: `30f29dbb0`. Normal-priority systemd user services
(nice 0), four-CPU fixture affinity, one Vitest worker. Loaded fixtures add
four native Go CPU workers at nice 5 on the same CPUs. No other heavy work
was started while a benchmark ran. The successful fixture attempt started at
one-minute host load 2.37. Production starts after synthetic CPU load stops.

The baseline below is the previous recorded quiet/loaded run in
[session-switch-performance.md](session-switch-performance.md), which inherited
nice 5 and predates main's initial-geometry revert and question-hook changes.
These are environment/tree comparisons, not isolated causal speedup claims.
The original card's warm-switch finding was approximately 1,100–1,300 ms.

## Browser fixtures

All values below are milliseconds. Native input uses the median of 30 keys;
workspace and Markdown switch figures use medians of five samples.

| Measurement | Prior quiet | Current quiet | Prior loaded | Current loaded |
| --- | ---: | ---: | ---: | ---: |
| 390 px native key → paint | 48.15 | 64.90 | 72.35 | 64.40 |
| 3840 px native key → paint | 48.2 | 81.85 | 87.45 | 97.55 |
| 3840 px synchronous event delivery | 9.8 | 47.60 | 8.9 | 65.85 |
| 3840 px synchronous input handler | 2 | 1.90 | 2 | 1.75 |
| Long workspace switch → paint | 778.7 | 706.20 | 1128.7 | 1003.90 |
| Warm Markdown main-thread tasks | 926.23 | 663.44 | 1071.61 | 827.12 |
| Cache-cleared Markdown main-thread tasks | 1156.61 | 1080.48 | 1529.64 | 1161.32 |
| First long Markdown mount → paint | 1411.1 | 1097.30 | 1568.6 | 1271.30 |
| 400-card board first card → paint | 283.5 | 235.80 | 393.9 | 288.50 |
| 400-card board fully revealed → paint | 325 | 264.80 | 439.5 | 330.50 |
| Insights open → paint | 177.8 | 174.60 | 352.8 | 192.20 |
| Graph open → paint, 67 commits | 70.3 | 99.10 | 127.2 | 98.30 |

The 70 ms native-typing target passes at 390 px and fails at 3840 px in both
conditions. Against the previous paired loaded fixed-tree median of 78.40 ms,
the current loaded wide result is 97.55 ms (+24.43%); against the original
112.80 ms it is −13.52%. The paired comparison's environment differs, so those
percentages do not attribute the change to a particular commit.

Tab-switch commit medians (390/3840 px) are 99.50/95.00 quiet and
123.50/84.30 loaded, with zero commit-time layout reads. The existing 250 ms
budget passes; no earlier comparable tab timing is recorded in the baseline table.
Warm switches still reparse zero finished messages. Board polling intervals are
15.0720/15.0043 seconds quiet and
15.0678/15.0038 seconds loaded.

Each browser run reports **7 passed, 3 failed, 32 filtered/skipped** across four
files. Both typing tests fail the zero-SessionDetail-render assertion (30 renders)
and the 400-card telemetry test fails the zero-SessionBoardTabs-render assertion
(30 renders). Board telemetry medians are 41.15/50.35 ms quiet/loaded, versus
the earlier isolated post-fix 7.7 ms. The assertions remain intact. These are
regressions requiring follow-up; this report does not mark acceptance passed.

## Focused server and daemon measurements

Five selected top-level tests pass, no skips; total command wall time 56.926 s,
including compilation into the temporary cache. Timing tests intentionally use
`-count=1`. Production route sampling is complementary to these fixtures.

| Measurement | Prior quiet | Current |
| --- | ---: | ---: |
| 1,000 sessions / 750 visible: initial-state median of three | 118.99 ms | 180.674289 ms |
| Initial-state data queries per sample | 12 | 12 |
| Rich 100k-event live route, best of three | 379.78 ms | 208.610310 ms |
| Rich 100k-event topics route, best of three | 173.36 ms | 101.748220 ms |
| Simple live route, ten-read batch upper medians | 51.48–54.94 ms | 48.996160–49.843673 ms |
| GC-normalized heap at warm-up | 3,683,920 bytes | 4,264,552 bytes |
| GC-normalized heap after 50 reads | 3,670,352 bytes | 3,717,712 bytes |
| Goroutines across heap samples | 14 | 14 |

Initial-state samples: 186.737005, 180.674289, 178.623984 ms. Daemon acknowledged
payload backing bytes remain zero for 300 × 64 KiB events; heap before, queued,
and acknowledged is 2,836,712 / 25,071,968 / 2,873,960 bytes.

## Production and sustained soak

The resumed production service completed in **2,121.304897 seconds** (35 min
21.305 s), including its temporary-cache build, three scenario runs and the
single soak. Each scenario has 11 measured samples per run after one discarded
warm-up: **264 measured samples plus 24 warm-ups** across eight scenarios.
Service exit status was 0. No previous official production baseline is present
in this checkout, so release-perf reports a first-run candidate, not a release
regression comparison. Its candidate says clean; the broader acceptance result
is **failed** for the browser/boot reasons above. No baseline was promoted.

| Production scenario | Primary median ms | Primary p95 ms | Calibration noise floor ms |
| --- | ---: | ---: | ---: |
| blackboard | 54.800 | 80.900 | 1.100 |
| board-tab | 236.300 | 261.400 | 27.600 |
| cold-load | 5335.500 | 5345.400 | 2.400 |
| insights-7d | 166.300 | 198.900 | 5.300 |
| search | 224.600 | 242.400 | 3.600 |
| transcript-open | 141.100 | 204.800 | 5.000 |
| typing | 8.300 | 15.000 | 1.700 |
| workspace-switch | 119.800 | 298.700 | 13.600 |

Production typing's **key-committed** p95 is 15.000 ms against its existing
16 ms mark budget; Insights p95 is 198.900 ms against 500 ms. The key-committed
mark does not replace native keystroke-to-paint measurement. During the soak,
300 native inputs had median 37.350 ms, p95 47.300 ms and maximum 103.200 ms at
the production driver's default viewport (not the loaded 3840 px fixture).

The three cache-disabled boot trials were **5,411 / 5,411 / 5,415 ms** against
3,000 ms maximum: **failed**. The separate production cold-load median is
5,335.500 ms. No earlier production boot measurement is recorded, so a speedup
against a previous production release is not claimed.

| Built entry assets | Prior post-fix | Current | Comparison |
| --- | ---: | ---: | --- |
| JavaScript raw bytes | 3,208,549 | 3,218,531 | +9,982 bytes |
| JavaScript gzip, Node default used by prior baseline | 964,524 | 966,848 | +2,324 bytes |
| CSS raw bytes | 376,174 | 378,070 | +1,896 bytes |
| CSS gzip, Node default | 66,272 | 66,653 | +381 bytes |
| JavaScript gzip, Go collector | no prior Go value | 970,970 | passes 1,100,000-byte budget |

The production HTML has one entry JavaScript and one stylesheet, with no
static preload chunks. Lazy assets are excluded. Go gzip and Node gzip are
reported separately so compressor differences do not appear as bundle growth.

The soak completed **1,800.101 seconds**, with **61 samples** at nominal 30 s
intervals. First/last values below are the medians of the first/last three
samples; maxima remain in JSON. Every process stays below the 64 MiB RSS-growth
limit and browser GC-normalized heap below its 16 MiB growth limit.

| Memory bytes | First | Last | Growth |
| --- | ---: | ---: | ---: |
| Browser GC-normalized heap | 20,120,832 | 23,650,768 | +3,529,936 |
| Runner RSS | 67,051,520 | 73,494,528 | +6,443,008 |
| Core RSS | 30,715,904 | 31,715,328 | +999,424 |
| Board RSS | 30,752,768 | 30,027,776 | −724,992 |
| Existing daemon RSS | 56,115,200 | 58,294,272 | +2,179,072 |

Daemon workload was observed read-only; no synthetic engines were started.
RSS is not a retained Go heap profile, and these 30 minutes do not establish a
day-long leak guarantee. Two long tasks were recorded, both 50 ms, without
truncation. The first-cycle CPU summary includes 152.211 ms of garbage
collection; production minified JavaScript frames are retained in JSON rather
than assigning unsupported source-level causes.

| Completed API family requests during soak | Count | Mean ms | Maximum ms |
| --- | ---: | ---: | ---: |
| board | 597 | 22.529 | 63.423 |
| live | 9569 | 43.713 | 2151.185 |
| other_api | 2016 | 18.731 | 42.580 |
| permissions | 421 | 26.558 | 42.433 |

Live requests averaged 5.316 per second across the observed workload. These are
browser request-family aggregates, not a global server route ranking. The live
maximum of 2,151.185 ms is retained even though the mean is 43.713 ms. The
production seed is unchanged (including its legacy 150-card board); the
400-card requirement is covered by the browser fixture above.

## Failures, exact commands, and cleanup

The first attempt completed both browser stages but route/production setup
failed because the dedicated test database was not listening. A temporary
loopback-only PostgreSQL 18 container was then started from an existing image.
A second launch refused load 4.14 before any measurement. The next attempt
waited below load 4 after compilation and completed the browser/route set.
Its production phase stopped at an obsolete blackboard close-tab selector.
The regression test first failed (one tab remained rather than zero); commit
`30f29dbb0` changes setup to close **Blackboard**, waits for detachment, and
removes the swallowed selector error. The production-only resume then completed
all three runs and the full soak. Earlier failed evidence is retained privately,
not silently reclassified as passing.

The final browser commands, each once quiet and once under four native CPU
workers, were:

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

Each: 7 pass, 3 fail, 32 excluded. Quiet/loaded wall times: 83.324/94.827 s.
Exact selected Go command, from `runner/`, with both database variables set
privately to the dedicated test connection and `GOWORK=off`:

```sh
$HOME/go/bin/go test ./internal/server ./internal/daemon \
  -run '^(TestGetLiveOnALongSession|TestGetTopicsOnALongSession|TestLivePollingRetainedHeap|TestInitialStateSeededConnect|TestEmitterAckReleasesPayloads)$' \
  -json -count=1 -timeout 10m
```

Five pass; zero skips. Machine-readable test receipts were reported to Blerg.
The production command uses the built `runner/cmd/release-perf` executable:

```sh
release-perf -version blg431 -go "$HOME/go/bin/go" -n 11 \
  -acceptance-soak 1800s -acceptance-daemon-pid <existing-daemon-pid> \
  -logs <private-evidence-directory>
```

For the harness fix:

```sh
node --test runner/frontend/scripts/release-perf-acceptance.test.mjs runner/frontend/scripts/release-perf-acceptance.browser.test.mjs
node --check runner/frontend/scripts/release-perf-playwright.mjs
npm run lint -w runner/frontend
scripts/scrub.sh
git diff --check
```

Node: 4 pass, zero skips, 2.569 s. Lint and syntax passed. Each production
orchestrator run builds all three frontends and builds its three Go
servers. No unrelated test suites or additional heavy jobs ran concurrently.
The orchestrator uses Vite directly, so those builds do not establish a new
TypeScript check.

Initial disk availability was **70,093,828,096 bytes** (65.28 GiB), above the
50 GB floor. Periodic readings remained above the floor; the lowest observed
reading was **67,416,338,432 bytes**. At cleanup, free space was
**67,553,878,016 bytes**. All four benchmark build-cache folders and the result
collector's temporary cache were confirmed removed. The temporary database
container was stopped and removed; the existing daemon was not stopped.
Results and failure evidence are retained outside tracked files under
`.cache/foreground-acceptance-20261107*` and
`.cache/foreground-production-20261107/`. Consolidated JSON records both source
revisions; the measured browser and production trees differ only by the harness
selector repair. No performance acceptance or independent code-review approval
is claimed.
