# Cold workspace load (BLG-492)

Measured November 6, 2026, after BLG-491, from base commit `9292ad44c`.
The harness serves freshly built production frontends and real services against
its disposable seeded databases. These measurements use normal CPU and I/O
weights (100), one performance run at a time. The checked-in Make recipe's
weights of 10 were replaced in a temporary copy of the Makefile for these runs;
no scheduling policy change is part of this patch.

## Cause and product change

A reload loses runner's in-memory access token. Previously its WebSocket startup
immediately tried a full-page refresh, spending one of three redirects allowed
per 120 seconds. Once that budget was spent, the page showed the recovery banner.
Parallel API requests silently obtained a token around 250 ms into the load, but
the WebSocket did not connect then. It waited for the banner's five-second retry,
which silently renewed the token a second time before reconnecting.

Runner now joins silent renewal immediately and connects as soon as it succeeds.
Foreground events share the pending attempt. Failure retains the existing guarded
full-page redirect; renewal errors log a fixed cause without raw error details.
Background token renewal and person-time preferences also initialize when startup
receives its first token asynchronously. Tokens remain in memory; refusal and
redirect limits are unchanged.

## Before measurement

The first exploratory counted load reached ready at **622 ms**. That sample had
not yet spent the redirect budget. Its final-document timing, relative to the
scenario action:

| Boundary | Time |
| --- | ---: |
| Final document fetch start | 200 ms |
| Final document first byte | 211 ms |
| Final document response end | 213 ms |
| Production script download | 221–282 ms |
| WebSocket construction | 381 ms |
| WebSocket open | 399 ms |
| First contentful paint | 414 ms |
| Initial state received | 478 ms |
| Workspace render mark | 521 ms |
| Transcript replay complete | 584 ms |
| Painted transcript ready | 622 ms |

The script transferred 1,157,673 bytes. The 99 ms between its response end and
socket construction bounds final-document parse/evaluation/startup together;
it is not an isolated V8 evaluation measurement. Initial state arrived 79 ms
after the socket opened. The full-page refresh caused a second document boot.
Navigation redirects through core do not necessarily commit a core document,
so frame-navigation counts alone do not count auth bounces reliably.

The subsequent **11-sample cold-load baseline had median 5,481.9 ms**. Its first
sample took 616 ms with one full-page auth bounce; the other ten used zero
full-page bounces but **two hidden-frame refreshes each**. Their first refresh
completed around 230–300 ms; the redundant one completed around 5.2 s. The
redirecting sample's core request-to-first-byte time was 17.2 ms. Each sample's
document records retain the individual refresh times.

The five-second wait is product recovery behavior, exposed by the harness's
repeated reloads. The harness polls readiness every 25 ms but records the app's
mark timestamp, so that polling does not add to the reported duration. There is
no deliberate multi-second harness delay inside the cold-load clock.

## Harness corrections and limitations

The exploratory n=1 run failed during a later blackboard calibration: setup
looked for **Close Project**, while the outer tab is **Close Blackboard**. Its
swallowed locator timeout left the same mounted tab open. Closing now waits for
removal and fails loudly on timeout. A cached blackboard mount already emits its
ready mark; no product readiness change was necessary.

The n=11 baseline completed cold-load, Insights, workspace switch, search and
board-tab, then failed the transcript-open ready mark. Transcript setup now
waits for the switch-away render and discards retained views before measuring a
fresh open. Both transcript and blackboard setup use the existing identity-change
cache invalidation and finish the resulting socket initialization outside the
measured action. Ready marks and timing budgets are unchanged.

The baseline is partial, not a successful complete release-performance gate.
Main accepted retaining its completed cold-load samples. No official release
baseline has been promoted.

## After measurement

The first complete after matrix has **cold-load median 469.8 ms**, p95 516.1 ms:
a **91.4% reduction**, below the 1.5 s target. Every one of its 11 counted cold
loads uses **zero full-page bounces and one hidden-frame refresh**.

These are the median samples' boundaries (ms relative to the action), not sums
of independently measured phase medians:

| Boundary | Before | After |
| --- | ---: | ---: |
| Document first byte | 14.6 | 7.2 |
| Script download start–end | 29.6–86.7 | 22.7–80.8 |
| DOM content loaded | 199.7 | 185.5 |
| First contentful paint | 237.3 | 226.5 |
| WebSocket construction | 5,234.1 | 243.7 |
| WebSocket open | 5,239.4 | 250.0 |
| Initial state received | 5,307.1 | 322.5 |
| Workspace render mark | 5,361.0 | 374.4 |
| First transcript replay complete | 5,409.0 | 429.1 |
| Painted transcript ready | 5,481.9 | 469.8 |

The after sample's core refresh first byte arrives at 219 ms (13.6 ms
request-to-first-byte); its frame returns at 228 ms. The socket starts within
16 ms of that return. Script transfer is 1,157,794 bytes, about the same size as
before; this change removes waiting and double booting rather than splitting
bundles. Script-response-end to DOM-content-loaded is 104.7 ms after versus
113.0 ms before, an inclusive parse/evaluation/startup interval, not isolated
JavaScript CPU time. Initial state takes 72.5 ms after socket open in this sample.

All comparable primary-matrix medians are shown below. Secondary movements are
observations on a shared machine, not isolated causal claims. No official
baseline comparison or noise-adjusted release verdict was requested.

| Scenario | Before (11 samples) | After (11 samples) | Change |
| --- | ---: | ---: | ---: |
| Cold load | 5,481.9 ms | 469.8 ms | −91.4% |
| Insights 7-day | 220.4 ms | 239.9 ms | +8.8% |
| Workspace switch | 157.3 ms | 106.9 ms | −32.0% |
| Search | 282.9 ms | 272.7 ms | −3.6% |
| Board tab | 372.2 ms | 343.6 ms | −7.7% |
| Transcript open | Not completed | 204.7 ms | — |
| Typing | Not reached | 28.5 ms | — |
| Blackboard | Not reached | 136.9 ms | — |

The completed before samples are retained in the private evidence directory
`blerg-release-perf-logs-20261106-112958`; after samples are in
`blerg-release-perf-logs-20261106-113817`. The exploratory n=1 directory is
`blerg-release-perf-logs-20261106-112525`. Raw service logs remain private.

Server-reported primary medians were 27.576 → 32.440 ms for Insights and
10.605 → 15.937 ms for board-tab. Blackboard reported 21.154 ms after (no
completed n=11 before sample). Cold-load, workspace switch, transcript open,
search and typing have no corresponding REST server-duration metric; their
WebSocket/client timings must not be replaced with an unrelated API duration.

For reproducibility, with the administration database environment configured:

```sh
sed 's/CPUWeight=10/CPUWeight=100/; s/IOWeight=10/IOWeight=100/' Makefile > /tmp/blg492-normal-priority.mk
PATH="$HOME/go/bin:$PATH" RELEASE_PERF_DIAGNOSE=cold-load make -f /tmp/blg492-normal-priority.mk release-perf
```

## Focused verification

- `npm test -w runner/frontend -- --project unit src/ws.test.ts src/main.test.tsx`:
  initial red had three intended failures; with the fix, 14 tests passed in 1.63 s.
  Restoring the old startup reproduced all four newly added behavioral assertions
  (including the later rejection/log-privacy case); the original ten tests passed.
- `npm test -w runner/frontend -- --project unit src/ws.test.ts src/main.test.tsx src/authClient.test.ts src/apiFetch.test.ts src/refreshGuard.test.ts src/signIn.test.ts src/lib/personTime.test.ts src/lib/releasePerfMarks.test.ts`:
  84 tests in eight files passed in 8.00 s after restoring the fix.
- `npm test -w runner/frontend -- --project 'browser (chromium)' src/SilentRefreshLock.browser.test.ts --maxWorkers=1`:
  one test passed; 22.67 s total, including the intentional 15 s lock deadline.
- `node --test runner/frontend/scripts/redact.test.mjs runner/frontend/scripts/serverTiming.test.mjs`:
  ten tests passed, no skips.
- `npm run lint -w runner/frontend`, `npm exec -w runner/frontend -- tsc --noEmit`,
  and `npm run build -w runner/frontend`: passed. The build retains the existing
  large-chunk advisory; no chunk-size claim is made by this change.

No Go source changed, so backend unit suites and Go vet were not run. The full
browser suite and final merged-tree release gate remain Main's responsibility.

The full after `make ... release-perf` run exited **0**: all eight scenarios
completed in each of three 11-sample matrices (one discarded warm-up per
scenario per matrix). Cold-load medians were **469.8, 472.4 and 544.4 ms**;
all 33 counted cold loads had zero full-page bounces and exactly one silent
refresh. The cold-load median spread was 74.6 ms. The tool recorded an unpromoted
candidate with no prior-baseline comparison. Because the worktree was not yet
committed during measurement, the generated report stamps the base commit; the
after measurement includes this patch.

`scripts/scrub.sh` and `git diff --check` passed after the report was written.
