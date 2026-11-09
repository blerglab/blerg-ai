# Blackboard

Open a workspace's blackboard with the blackboard icon beside its Board button.
The tab opens on the current milestone. Project, milestone and thrust navigation
stays in that tab; Duplicate in the tab menu preserves an independent location.

Empty thrusts show “No cards yet”. “All shipped” appears only when at least
one card exists and every card is done or to test.

On a first visit, a skeleton reserves the release layout while the record loads.
Card counts and progress bars remain placeholders until the slices arrive; missing
data never appears as zero. The first loaded summary and thrust bars grow into
place in about half a second. Reduced motion disables both growth and shimmer.
Revisiting a workspace shows its last complete blackboard immediately and refreshes
in the background, without a loading line or another animation. The bounded cache
is scoped to the blackboard and session query, invalidated on revisions and
reconnects, and cleared when the signed-in identity changes. Refresh keeps the
visible view while reading; errors still show the failure and retry control.

When Main prepares a setup draft, keep the Blackboard tab open: the draft appears
there for review without reopening the tab. Main asks with
`blerg-runner ask --person --draft ID --revision N "Create this blackboard?"`.
Only the owner's targeted answer in chat or Needs you decides: yes creates the
exact immutable draft, no discards it. Revisions cancel the previous question;
Main asks again. Create, Edit and Discard controls and their old write routes are
retired. Replacements and answered decisions in another tab or device update the
open view too. Reconnecting catches up on
changes made while the browser was offline.

Milestones show daily observations and events on one time axis. A missing day is a
gap, and an incomplete observation is marked as such. The stage filters also form
the chart key. Thrusts open on Active; choose All to include completed work.
Cards use the Board's shared presentation and open its existing ticket dialog.
Project tiles summarize milestones without listing cards in hover previews.

Plan proposals are read-only on the blackboard. Main creates a saved proposal,
then asks through `blerg-runner ask --person --blackboard ID --plan-proposal ID
--revision N "Commit these changes?"`. The owner replies to that question in
chat or Needs you. The server commits the exact proposal and records the answer
in one transaction, even when Main's daemon is away. An untargeted yes does not
commit. A stale revision or changed proposal requires a fresh ask; other readers
cannot decide it. Plain items are also read-only: discuss changes with Main.
Permission failures hide the loaded record.

## Release evidence

The blackboard is read-only. Main asks the owner to authorize an exact release
and next release in the existing person-required question. The server records
that answer and a durable execution entry together, then performs the guarded
board cut. Final follows the immutable board receipt for the linked release;
“Shipped” uses the board's release date, separate from runner freeze time.

The Import writer and `cmd/blackboard-seed` are retired. Tags and names do not
finalize milestones. Existing imports and old Finalizing records remain
historical evidence and show “Released on board; evidence incomplete.” An
unlinked milestone stays open until the owner approves an explicit pre-cut link.
No history is backdated, and unlinked Final remains deferred.

## UI captures

From `runner/frontend`, run
`VITE_BLACKBOARD_SHOTS_DIR=.cache/blackboard-shots npm test -- --project browser src/components/BlackboardTabs.browser.test.tsx --maxWorkers=1`.
The tests use the real workspace tab path, dark theme and 150 fixture cards. The
capture flag gives the outer browser enough room to preserve native 390 and 3840
pixel widths. Files are written under the test file's `.cache/blackboard-shots`.

Draft changes refresh an open Blackboard tab in place over the browser socket. Bursts
of changes settle for 200 ms before refetching; only the affected workspace's cached
reads are invalidated. A browser ignores its own action's echo and uses the action's
normal refresh instead. Public workspace viewers receive no draft content and skip
owner-only draft refetches. If a refresh fails, the tab reports the failure and allows a read retry; it
cannot restore a consumed draft or record a decision.

For first-load, pending-slice, loaded and animation-frame captures at both widths
in dark and color-blind dark themes, run from `runner/frontend`:

```sh
VITE_BLACKBOARD_SHOTS_DIR="$PWD/.cache/blackboard-loading" npm test -- --project 'browser (chromium)' src/components/BlackboardLoading.browser.test.tsx
```
