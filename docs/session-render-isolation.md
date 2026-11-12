# BLG-584: attention render isolation

The first bad first-parent merge is
`8090385baa582351ab72fbe737b59640ad9c81a8`, **Merge BLG-577: every
count and list of attention items comes from one helper; plan proposals are rows
in the Questions tab and Needs you**. The initial full LongSession run on
`50098da66ca9b6730947438cb72c8f0eb6050e0c` reproduced 11 failures and 22 passes
in 169.77 seconds. Worker telemetry rendered SessionDetail and SessionBoardTabs
30 times per batch; child streaming also rendered unrelated session cards.

## Bisect

`git bisect start --first-parent HEAD 3bd2b896a` used this deterministic probe:

```sh
cd runner/frontend
npx vitest run --project browser-performance \
  src/components/LongSession.performance.browser.test.tsx \
  -t '400-card board asleep'
```

| Revision | Result |
| --- | --- |
| `7b2a3341a` | Pass |
| `8090385ba` | Fail: SessionBoardTabs renders on telemetry |
| `af046bf11` | Pass |
| `826977a98` | Pass |
| `000b42018` (immediate first parent of BLG-577) | Pass |

Each probe ran one case and skipped the other 32 intentionally. The probe's
render assertion has no duration threshold. No timing-only result classified a
revision. The session branch was restored after the bisect.

## Fix

The attention hook previously subscribed to whole session, live-decision,
message and plan stores. BLG-577 added that hook to SessionDetail and every card.
A worker telemetry update consequently woke the whole workspace.

The hook now subscribes to its visible collection and watched membership as one
stable external-store snapshot. Session identity in that snapshot contains only
id and title, the fields attention consumers display. Telemetry and unrelated
decisions do not change the snapshot. Questions, reviews, plans, failure state,
dismissal, renaming and membership still change it. Counting policy is unchanged.

The unit regression first failed with eight renders instead of five. It checks
isolation while a question is pending, then checks renaming, failure, dismissal
and reparenting. Existing shared-count tests cover asks, reviews, plans, failed
and paused workers together.

## Gate bypass

The complete matrix and `--changed` already selected `runner-performance` for
Runner frontend changes. The legacy `--changed-since` path used
`touched_tests.select`, which explicitly excluded all `-performance` suites.
That is a concrete selection bypass; it does not prove which historical gate
command an operator ran.

Affected frontend changes now retain the complete serial performance suite.
Backend-only changes omit it. `pre_merge.py` supplies the final phase to that
suite even in focused mode, preserving every performance viewport. Regression
tests cover frontend/shared-chat selection, backend omission, viewport phase and
propagation of performance failures to a nonzero gate result. No render or
duration budgets changed.

The child performance fixture also lacked `started_by`. Under the current
contract that describes person-created channels, not delegated workers. Its
waiting transition correctly produced new channel attention. With Main's
approval, the fixture now supplies actual worker-origin metadata. Assertions and
budgets are unchanged; a hook regression explicitly preserves waiting-channel
attention. The first fixed run had 24 passes and nine failures: eight such
fixture transitions and one 3840 px native-input duration miss under load.

Temporarily restoring the old hook after implementing the fix made the new
isolation regression fail again (eight renders instead of five). The fixed
source was restored before final verification.

## Verification commands

From `runner/frontend`:

```sh
# Initial reproduction and final proof (final run also writes Vitest JSON):
npx vitest run --project browser-performance src/components/LongSession.performance.browser.test.tsx
# Final command adds:
# --reporter=default --reporter=json --outputFile.json=/tmp/blg584-performance-final.json

npx vitest run --project unit \
  src/hooks/useQuestionItems.test.tsx src/hooks/useSessionAttention.test.ts \
  src/components/QuestionQueue.test.tsx src/components/SessionCard.test.tsx \
  src/components/SessionDetail.test.tsx src/components/SessionRail.test.tsx \
  --maxWorkers=${BLERG_TOOL_WORKERS:-2}

npx vitest run --project browser \
  src/components/PlanAttention.browser.test.tsx \
  src/components/QuestionQueue.browser.test.tsx \
  src/components/NeedsYouDismiss.browser.test.tsx \
  src/components/ProposalAttention.browser.test.tsx \
  --maxWorkers=${BLERG_TOOL_WORKERS:-2}

npm run lint
npx tsc --noEmit
npm run build
# After the fixture/test additions:
npx eslint src/hooks/useQuestionItems.ts src/hooks/useQuestionItems.test.tsx \
  src/hooks/useSessionAttention.ts \
  src/components/LongSession.performance.browser.test.tsx --max-warnings 0
npx tsc --noEmit
```

From the repository root:

```sh
PYTHONPATH=scripts python3 -m unittest scripts.pre_merge_test scripts.tests.test_touched_tests
bash scripts/scrub.sh
git diff --check
```

Regional unit: 198 passed in 22.32 s. Regional browser: 22 passed in 39.81 s
(Chromium, the configured browser for those files). Gate tooling: 89 passed in
14.319 s. Lint, types, production build and scrub passed. The production build
retains its existing chunk-size warning. An initial reproduction could not
start until `npm ci` installed missing dependencies. An initial Python unittest
invocation needed `PYTHONPATH=scripts`; neither startup failure is behavioral
red proof.

This is regional verification, not a full frontend/browser/performance or
repository gate. No Go code changed and no Go tests ran. BLG-497's new Needs you
tab is on another branch; the shared collection is covered here, but that tab's
merged-tree integration still belongs to Main.

Final LongSession rerun: **33 passed, zero failures/skips, 119.67 s wall time**
(110.59 s test time). Native input-to-paint medians were 48.10/50.00 ms at
390/3840 px, under the unchanged 100 ms budget. Synchronous typing medians were
1.65/2.25 ms, under 16 ms. Child-event medians across all eight configurations
were 2.80–7.95 ms; typing medians were 1.25–3.00 ms. Board telemetry rendered
SessionBoardTabs, BoardTab and BoardCard zero times across 30 broadcasts.

The previous native-only duration miss (156.60 ms) passed this lower-load rerun.
Neither render-count failures nor budgets were waived. Shared-host observations
are not a controlled before/after speed benchmark.

For BLG-298 gate costing: the focused path previously spent zero time on Runner
performance. It now adds the complete five-file serial performance project when
Runner frontend or a shared frontend dependency changes. LongSession alone took
**1.99 minutes** in the final run (2.83 minutes in the initial reproduction and
4.07 minutes in the first fixed run under heavier load). The other four files
were not run here; total added gate time is therefore **at least about two
minutes on this observation**, with no claimed full-project estimate. Backend-only
selection remains free of this added suite. No local gate history was available
to attribute a particular historical merge to a recorded invocation.
