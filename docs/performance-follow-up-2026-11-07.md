# BLG-431 regression follow-up — November 7, 2026

This follows the [final foreground report](performance-acceptance-final-2026-11-07.md).
Only fast production scenarios and focused fixtures run here, without another
full acceptance or sustained soak. Normal-priority services, four Go build
workers, a temporary Go cache, and the same disposable database setup. Disk is
checked before each launch and during execution; every reading so far exceeds
50 GiB. No other heavy work is started by this session during a measurement.

## Production typing: separate keydown from the driver

The previous 49.3 ms p95 started its clock before Playwright located and focused
the textarea and delivered the key. Diagnostics found, for example, **31 ms
before keydown and 1 ms from keydown to commit**, reported as a 32 ms sample.
The earlier `30f29dbb0` tree also fails 16 ms in the current environment:
with its original driver, three p95 values are 24.10 / 22.50 / 21.20 ms;
with the frozen current driver, early runs reach 35–43 ms. A stable p95
good/bad boundary was therefore not established; noisy samples are not labeled
as a causing product commit. Main stopped the typing bisect and directed the
measurement correction.

The driver now captures the actual composer keydown, before React handles it,
and waits for a `key-committed` mark at or after that event. It continues to
inject the same native Playwright key. The gated `clientSamplesMs` now means
**keydown → commit**. `preKeydownSamplesMs` and `commandToCommitSamplesMs`
preserve the setup/protocol delay separately in raw scenarios and consolidated
JSON. Missing or unordered timestamps fail closed. Each sample removes only
its own event listener and protocol handle. The aggregate's strict 16 ms p95
ceiling still checks every calibration, including a first run without a release
baseline. No assertion or threshold was relaxed.

One fast production invocation on unmodified product tree `28708e80c`, using
the corrected driver from this commit: three calibrations × 11 measured keys
after a discarded warm-up. No soak or boot-only trials. The ordinary eight
scenarios also completed. The source override builds the selected product;
the tool's candidate commit field identifies the driver checkout, so the
product SHA is recorded explicitly here. No baseline was promoted.

| Typing measurement, ms | Run 0 median / p95 | Run 1 median / p95 | Run 2 median / p95 |
| --- | ---: | ---: | ---: |
| Keydown → commit, gated | 1.70 / 2.60 | 2.40 / 6.40 | 1.60 / 2.50 |
| Before keydown, diagnostic | 19.40 / 38.90 | 29.80 / 63.70 | 28.60 / 34.50 |
| Command → commit, diagnostic | 21.70 / 41.20 | 33.00 / 70.10 | 30.20 / 36.10 |

All corrected p95 values pass 16 ms. This is a measurement-definition
correction, **not** a claim that application typing became 49.3 → 2.6 ms faster.
Historical command-clock values cannot serve as keydown-clock baselines.
The separate native key-to-paint median budget of 70 ms is unchanged; no product
typing code was modified and that browser suite was not rerun here.

Exact focused validation:

```sh
node --test runner/frontend/scripts/release-perf-acceptance.test.mjs runner/frontend/scripts/release-perf-acceptance.browser.test.mjs runner/frontend/scripts/redact.test.mjs runner/frontend/scripts/serverTiming.test.mjs
GOWORK=off "$HOME/go/bin/go" -C scripts/verification test ./cmd/performance-acceptance
GOWORK=off "$HOME/go/bin/go" -C scripts/verification vet ./cmd/performance-acceptance
GOWORK=off "$HOME/go/bin/go" -C scripts/verification build -o <temporary-executable> ./cmd/performance-acceptance
golangci-lint run ./cmd/performance-acceptance/...
node --check runner/frontend/scripts/release-perf-playwright.mjs
npm run lint -w runner/frontend
scripts/scrub.sh
git diff --check
```

Go uses the temporary `GOCACHE`; Go lint runs from `scripts/verification/`.
Node **17 passed, 0 failed, 0 skipped**; verification package **16 top-level tests
passed, no skips**; vet/build/syntax/lint pass, focused Go lint zero issues.
No frontend TypeScript source changed in this measurement fix, so a new frontend
type/build gate is not claimed. The Node/Chromium driver tests exercise its
real browser boundary. The clock regression test first failed by returning
32 ms instead of 1 ms. The numeric-diagnostic test first failed because the
collector discarded both diagnostic arrays; both fixed focused reruns passed.

Fast production command (built current `runner/cmd/release-perf`, dedicated test
database set privately, `GOWORK=off`, temporary cache):

```sh
release-perf -go "$HOME/go/bin/go" -version keydown \
  -source-root <28708e80c-worktree> -logs <private-evidence-directory> -n 11
```

Evidence remains in `.cache/blg431-typing-corrected/`; the service exited 0.
Original/inconclusive measurements remain in `.cache/blg431-fast-*`,
`.cache/blg431-native-30/`, and `.cache/blg431-typing-bisect-*`. They are not
silently reclassified as passing the corrected gate. Typing bisection is stopped.

## Warm switch: defer inline export controls

The unchanged Markdown switch fixture bisects `30f29dbb0..28708e80c` to
**`811eb3d9a90d35850873f008201ceaa42653cd84`**, the inline export controls.
The intermediate good medians were 782.79, 804.48, 760.37 and 793.03 ms;
the merge introducing the controls measured 921.12 ms and the first bad
commit itself measured 1,016.41 ms. Warm reparses remain zero. The transcript
still has to mount its React elements and DOM after returning to the session.
An instrumented long-task trace on the eager tree attributes substantial work
to React development element construction (`jsxDEV`, 1,110 ms self samples),
`SessionGroups` (441 ms), geometry measurement (237 ms), and `getClientRects`
(129 ms). These are profiler diagnostics, not additive unprofiled timings;
the trace's largest task was 3,112 ms. Reusing parsed Markdown does not avoid
constructing hundreds of print/download controls and their SVGs.

Inline tables and code blocks now initially mount one focusable, labeled
ellipsis trigger. Print/download controls and SVGs are constructed only on
first opening and then retained while closed. Native Enter/Space and Tab work;
Escape closes, consumes the key and restores trigger focus; an outside pointer
closes the controls. The initial 34 px action row and the table's height stay
the same before opening, while open and after closing. Pop-out viewer header
actions remain immediately available. Printing still removes the action row.
Inline print/download now takes two clicks instead of one (open actions, then
choose the action). Main explicitly accepted that tradeoff on review and merged
`2739f9ca1` at `b56a325c`, including the chat package bump to 1.59.66 that was
missing from the submitted commit. Future chat changes require that bump.

The same focused unprofiled switch command, one worker, five warm and five cold
switches per invocation:

| Warm main-thread median | Eager | Lazy trigger |
| --- | ---: | ---: |
| First local before/after pair, ms | 943.015 | 821.825 |
| Final lazy-trigger repeat, ms | — | 904.900 |

The paired reduction is **121.190 ms (12.9%)**. The final repeat is only 4.0%
below that eager reading: scheduler/layout variability remains substantial.
The earlier full acceptance measured 960.769 / 1,017.538 ms quiet/loaded;
the earlier reference was 663.443 / 827.119 ms. **This does not establish a
return to 663 ms**. A focused loaded repeat (affinity 0–3, four competing own
Go CPU workers at nice 5) measures **1,035.791 ms**; this does not demonstrate
an improvement over the previous loaded 1,017.538 ms. All warm switches still
parse zero blocks; all cold switches parse 300. Content equivalence and the
existing timing/render assertions remain unchanged.

Exact validation for this UI change:

```sh
npm test -w packages/chat -- src/components/Markdown src/components/InlineContentActions.test.tsx src/components/ContentExport.test.tsx src/components/ViewerToolbar.test.tsx --maxWorkers=1
npm test -w packages/chat -- src/ButtonGuard.test.ts --maxWorkers=1
npm run test -w runner/frontend -- --project browser src/components/ContentExport.browser.test.tsx src/components/PopOutMessage.browser.test.tsx --maxWorkers=1
npm run test:performance -w runner/frontend -- src/components/SessionSwitch.performance.browser.test.tsx --maxWorkers=1 -t 'reuses finished Markdown'
npm run test:performance -w runner/frontend -- src/components/LongSession.performance.browser.test.tsx --maxWorkers=1 -t 'budgets typing in a long live workspace'
# Under the four-worker load described above, with affinity 0–3:
npm run test:performance -w runner/frontend -- src/components/LongSession.performance.browser.test.tsx src/components/SessionSwitch.performance.browser.test.tsx --maxWorkers=1 -t 'budgets typing in a long live workspace|reuses finished Markdown'
npm run lint -w packages/chat
npm run lint -w runner/frontend
npm run typecheck -w packages/chat
npm run build -w packages/chat
npm run build -w runner/frontend
```

Chat region: **9 files, 155 passed**, 0 failed/skipped, 23.58 s wall time
(12.12 s test time). Button guard: **2 passed**, 0 failed/skipped, 3.64 s wall time.
Export consumer browser files: **10 passed**, 0 failed/skipped, 8.98 s wall time.
Switch: **1 passed, 4 excluded** per focused invocation.
Native typing: **2 passed, 31 excluded**, 15.16 s wall time; key-to-paint medians
**48.20 / 48.25 ms** at 390 / 3840 px, within the unchanged 70 ms requirement.
At 3840 px event delivery is 9.80 ms and synchronous key handling 1.80 ms;
SessionDetail and SessionBoardTabs do not render on keys. This is the quiet
consumer check. The focused loaded invocation passes **3 tests, 35 excluded**;
native typing medians are **60.60 / 65.45 ms** at 390 / 3840 px, still below
70 ms. It is not a repeat of full acceptance or soak. Only its four owned
load-worker processes are stopped afterward.
Both lints, chat typecheck and both production builds pass; the runner build
includes its TypeScript check. Existing bundle-size warnings remain warnings.

The new structural test first failed because the trigger did not exist.
Removing the lazy-construction guard makes it fail with **900 initial SVGs
instead of zero**; restoring the guard passes both focused tests. An early
consumer run caught a real boundary error: making shared CodeActions lazy hid
the pop-out header's Print action (1 failed, 9 passed). Laziness is now explicit
for inline blocks only; the final 10-test browser rerun passes. Existing export
tests now open the trigger before checking the same exports; no export checks
or performance thresholds were removed. Screenshots at encoded 390 / 3840 px
were inspected in default dark and color-blind dark themes, including a narrow
content pane in the wide viewport. Raw traces and screenshots stay private in
`.cache/blg431-warm-trace/` and `.cache/blg589-shots/`.

## Board opening: two different paths

The production `board-tab` scenario performs a full navigation to `/boards/:id`
and waits for the board-rendered mark. The browser fixture instead opens a
400-card board tab inside an already mounted idle session. They measure
different paths and cannot share a causing-commit claim.

### Production board page: no stable bad endpoint

Fast production measurements, each with three calibrations and 11 measured
opens per calibration, after the discarded warm-up:

| Selected product / driver | Run 0 median / p95, ms | Run 1 median / p95, ms | Run 2 median / p95, ms |
| --- | ---: | ---: | ---: |
| `30f29dbb0`, frozen follow-up driver | 229.90 / 258.60 | 249.50 / 266.80 | 235.00 / 260.20 |
| `30f29dbb0`, original driver | 243.70 / 277.00 | 235.10 / 253.50 | 251.70 / 269.60 |
| `28708e80c`, frozen follow-up driver, first run | 260.50 / 301.40 | 275.90 / 290.50 | 269.30 / 308.00 |
| `28708e80c`, corrected typing driver, later run | 242.10 / 264.80 | 245.30 / 281.60 | 248.30 / 266.70 |

The same product tree's later result overlaps the old tree. Midpoint production
measurements also fail to supply a stable boundary: `fd35ae88e` board medians
255.0 / 252.2 / 242.3 ms and `b32c8f479` 247.9 / 243.7 / 247.4 ms (five opens
per calibration). Those runs' typing diagnostics are described above; their
board observations remain valid data, not evidence of a typing product bug.
The original/full-final production primary medians were 236.3 / 250.1 ms,
with measured noise 27.6 / 11.6 ms. The 13.8 ms difference is smaller than
the earlier noise and the existing 15% median regression margin. No release
baseline is promoted here, and this is not a new pass declaration for a
previously failed field. **A stable production board-page regression could not
be reproduced, so a causal production bisect result is not claimed.**

### Session board tab: route-history cost identified

The unchanged `BoardGraphInsights.performance.browser.test.tsx` fixture has a
repeatable smaller increase. Three independent quiet invocations per tree:

| Tree | First-card samples, ms | Median first card / fully revealed / Insights, ms |
| --- | --- | ---: |
| `30f29dbb0` | 271.4, 287.5, 277.7 | 277.7 / 306.0 / 200.9 |
| `fd35ae88e` | 287.1, 271.5, 283.9 | 283.9 / 312.0 / 202.9 |
| `b32c8f479` | 265.0, 290.0, 270.7 | 270.7 / 300.6 / 197.6 |
| `cdcdd701c` | 292.0, 311.7, 322.4 | 311.7 / 333.6 / 221.5 |
| `03a5eb34b` | 309.3, 302.9, 315.0 | 309.3 / 344.8 / 230.6 |
| **`9a3a38c15`**, first bad | 293.3, 293.7, 304.6 | 293.7 / 328.6 / 227.8 |
| Its parent, `a4451d993` | 264.7, 266.4, 273.6 | 266.4 / 303.0 / 204.9 |
| `28708e80c` endpoint | 301.8, 293.5, 297.0 | 297.0 / 324.9 / 222.2 |

Bisect receipt: `30f29dbb0..28708e80c`, restricted to runner frontend and chat,
classifying the median of three first-card observations above 290 ms as the
diagnostic bad boundary. This classification does **not** change the fixture's
existing one-second assertions. The first bad commit is
**`9a3a38c15b7410cd3c28224aabe3116fc0f4332c`**, workspace header/conversation
route history. It adds a `useSessionRoute` layout effect which navigates when
the active workspace tab changes, committing router history before paint.
The immediate parent comparison adds **27.3 ms (10.2%)** to first-card time.

As a diagnostic counterfactual only, omitting that hook on `2739f9ca1` gives
first-card samples **271.9 / 260.9 / 281.6 ms**, median **271.9 ms**;
fully revealed median 303.7 ms and Insights median 203.1 ms. This supports the
route synchronization attribution. **That omission was restored and is not
delivered**: removing history synchronization loses the correctness introduced
by BLG-588. No route-history assertion was loosened. The lazy export change
alone does not remove this cost (one first-card observation was 295.3 ms).
This is a report of the identified remaining cost, not a board optimization.
It does not explain the standalone production board-page fluctuations above.

Exact focused command in each selected worktree:

```sh
npm run test:performance -w runner/frontend -- src/components/BoardGraphInsights.performance.browser.test.tsx --maxWorkers=1 -t 'measures opening the board tab'
```

Each historical/counterfactual invocation passes the unchanged first-card,
fully revealed and Insights budgets: **1 passed, 1 excluded**, no failures.
The graph case is excluded by the filter. Both bisects are reset; none of the
historical checkouts or diagnostic omissions changes the deliverable source.
The final restored-source invocation passes with first card **293.5 ms**,
fully revealed **324.5 ms**, Insights **233.0 ms** (1 passed, 1 excluded).
Across the nine three-invocation source/diagnostic groups and two additional
current-source checks, **29 invocations passed**, each with the one unrelated
graph case excluded. These are comparisons across trees, not 29 current-tree
release tests.

Final documentation checks: `scripts/scrub.sh` and `git diff --check`, both
pass. No additional Go tests are needed for this documentation-only board
report; the earlier tooling tests are listed in the typing section. No full
acceptance, soak, historical boot rerun or unrelated test suite ran again.
The temporary Go cache, compiled benchmark helpers, disposable PostgreSQL
container, and generated service binaries/assets from the fast runs are
removed. Private measurement JSON/logs remain; transient account material is
removed. Final C: availability is **62,422,003,712 bytes (62.42 GB)**,
above the 50 GiB stop floor.
