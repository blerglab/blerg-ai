# Card time and review rounds (BLG-444)

Design only. No product code in this change. Scope: report each card's total
worker time and review-round count to Main, with a budget nudge, using
infrastructure that already exists: Insights' per-session timing
(`runner/internal/activity/timing.go`, `runner/internal/db/insights_activity.go`,
branch `wip/blg-424-insights-trends-and-context-4148f8e6`, head `e9b80cfb` as
of this revision), card attribution (`session_card_links`), review/failure
marks (`agent_events kind='mark'`), and the plan-drift nudge pipeline
(`runner/internal/server/plansync.go`).

Revision note: this version folds in the owner's answers to the five open
questions (now decided, §5) and a design review's findings (H1/H2/M3-M7/L8/L9,
folded into §2, §3 and §4 below, each marked with its finding id where it
corrects the first draft).

## 1. The failure rule

**What this feature may cost, at most: a stale or missing number.** It must
never cost a session, a turn, a turn's tools, delivery of a notice of any
*other* kind, or the Insights page.

Concretely:

- Computing a card's total or round count never runs inside a request that
  also does real work (starting a turn, delivering a message, moving a card).
  It is read-only aggregation over already-recorded events, on the same kind
  of path Insights already uses (`InsightActivityTimings`, which is itself
  best-effort and returns `partial: true` under a deadline rather than
  blocking).
- A failure in this feature's own code (a query error, a bad total, a nudge
  that can't be written) is caught at its boundary, logged with its cause
  ([[feedback_log_every_failure]]: every failure path logs why), and turns into
  **no number**, never a wrong number and never a propagated error. A card
  with a total we're not sure of shows nothing, the same rule the acceptance
  criteria already states for a card with no linked workers.
- **(M3)** The card-time check has its *own* error boundary, separate from
  the existing plan-drift fact computation. `checkPlanTurn` today computes
  every drift fact through one `planDrift` call that returns one error for
  the whole turn (a single failure drops every fact for that turn). Card-time
  crossing detection must be a second, independent call in `checkPlanTurn`,
  with its own `log.Printf`-and-return-nothing failure path, so a card-time
  timeout or query error can never suppress "your checklist is all ticked" or
  any other unrelated drift fact for the same turn, and vice versa.
- The plan-drift turn-end check (`checkPlanTurn`) already runs with a bounded
  timeout (`planCheckTimeout`, 45s) off the hot path, and a failure there is
  logged and swallowed (`log.Printf("plan drift %s: %v", ...)`); card-time
  crossing checks ride in the same function and inherit the same isolation.
  They must not add synchronous work to `onPlanTurnEnded`'s caller.
- **(M4)** Card-time computation never runs inside `preparePlanNudgeMode`'s
  delivery path. That path waits (up to `planCheckTimeout`) on the turn-end
  flight closing before it can attach anything to the next outgoing message —
  it is the one piece of this pipeline that sits in front of a real user
  interaction (the next turn being sent). Worse, for a still-running session
  BLG-424's own cache never hits (`insights_activity.go`'s cache condition
  requires `s.ended_at IS NOT NULL OR computed_at > now()-1 minute`), so a
  full recompute there would be both slow and on the critical path. Card-time
  crossing detection happens only in `checkPlanTurn`'s own goroutine (already
  off that path), and keeps a running per-card total (§3) so each check is an
  incremental upsert of one session's own total, not a full cross-session
  recompute. Every query the check makes is bounded (indexed lookups, no
  unbounded scans over a card's full history).
- The settings-panel read/write for the budget interval is its own small
  endpoint, independent of everything else; it failing affects only that
  control.
- Insights' own totals (the page the card-time number must reconcile with)
  are computed by code this feature does not modify. Card-time reads the same
  underlying timing, through a shared helper, so a bug in the *aggregation
  across sessions for one card* cannot also break Insights' per-session view
  — the two are the same bytes, not duplicated logic that could diverge.

What must keep working regardless of this feature's state: starting and
running sessions, the messaging/notice pipeline for every other notice kind,
review and failure marks themselves (they are the source data, not consumers
of it), and the Insights activity endpoints.

## 2. Failure matrix

Layers below use the doctrine in `docs/testing-audit.md` (1 contract, 2
implementation, 3a canned cross-service, 3b real services, 4 end to end, 5
staging).

| Scenario | Expected behavior | Proof |
| --- | --- | --- |
| Daemon restart mid-turn | The turn's events already written to `agent_events` are final; any event lost because it was never durably written (the daemon never got to send it) is simply absent from the total — same as it's absent from Insights today. No double-count, no half-interval. The running per-card ledger (§3) reruns its upsert on the next `turn_done` and sees the same durable events. | Layer 2: `activity.Split`/`SplitOrdered` given an event stream that ends mid-span (no matching `tool_result`) already closes the open span at `end` (see `timing.go`'s "for _, c := range calls { addMeta(c.at, end, ...) }"); a card-time contract test feeds a truncated event stream (started tool, no result) and asserts the total stops at the window end, not infinity. Layer 3b: kill the daemon process mid-turn in a real Postgres-backed server test, resume, and assert the recomputed per-session ledger row only grows (monotonic), never shrinks or duplicates the pre-restart interval. |
| Server restart | Totals are a read over `agent_events`/marks plus the small ledger table, not in-memory server state. A server restart loses nothing already committed; a crossing check in flight simply did not run and reruns on the next turn-end. | Layer 3b: real Postgres, kill and restart the `server` process between two turns of the same session, assert the next computed ledger row matches what a non-restarted run would have produced for the same event history. |
| **(H1)** A session linked to more than one card | Matches Insights' own rule (`Shared: len(Cards)>1` in `insightTimingSession`): a session linked to more than one card is **excluded from every card's total**, not split and not double-counted. Its time genuinely can't be attributed to one card over another from the recorded events, so it counts nowhere rather than being guessed at. | Layer 1 contract: a session with two `session_card_links` rows for two different cards; assert neither card's total includes it. Layer 3b: real schema, same fixture, assert both cards' totals equal the sum of their other (non-shared) linked sessions only. |
| **(H1)** Main or a workspace coordinator session is linked to a card | Main pins nearly every card it coordinates, and would otherwise dominate every card's total with its own cross-card session time. Card time counts only sessions **directly** linked to the card (a `session_card_links` row for that `(board,card)` on that session itself — pin, a bare `#N` or full `BOARD#N` title match, or a checklist assignment) that are **not** the account's root/coordinator session for that work (no `parent_session_id`). It never walks the lineage/ancestor chain the way `InsightActivityScope`'s `insightOwnedScope` CTE does for Insights' own "which sessions are in scope at all" question — that answers a different question (is this session mine to see) than "did this specific session do the work" the card total needs. | Layer 1 contract: a root session (no parent) pinned to card #1 and a worker child of it directly linked to card #1; assert card #1's total is the child's time only, never the root's. Layer 3b: real schema reproducing a Main with five pinned cards and one worker per card; assert each card's total equals only its own worker's time, and reconciles with what Insights reports as that worker session's own active time (owner's decision 1). |
| Unlinked work later linked | A session that worked before it had any `session_card_links` row for a card, then later gained a direct link (pinned afterward), attributes its *entire* own session timing to that card once linked — the link is on the session's own row, not inherited from an ancestor, so this needs no special-casing once H1's "direct link only" rule is implemented: the session's own total is read in full regardless of when the link was written (`linked_at` only orders "latest" for display). | Layer 1 contract: a session with events before `linked_at` and a card-time query that must include all of them. Layer 3b: real schema, assert totals match whether the link was written at session start or after the work. |
| A card re-pinned | Re-pinning (same `(board,card)`, a fresh `pin` row via `ON CONFLICT DO NOTHING` on `session_card_links`'s composite primary key) does not create a second attribution of the same session's time — the primary key already prevents a duplicate `(session_id,board_id,card_number,source)` row, and the ledger upsert (§3) is keyed by `(board,card,session_id)`, not by link row. | Layer 1 contract: insert the same link twice (simulating `blerg-runner pin` run twice) and assert the ledger row for that session is unchanged. |
| A worker discarded | A discarded session's already-recorded timing still counts (discarding stops the session, it doesn't erase its history) — consistent with how Insights treats ended sessions. A discarded session must not be *double*-counted if discard also emits a closing event that resembles a new span. | Layer 2: feed a timing split an event stream ending in whatever `discard` emits (if anything) and assert no extra span appears past the session's real `ended_at`. Layer 3b: discard a real session mid-card and assert the card total freezes at the discard point and never grows afterward. |
| Clock skew between daemon and server | All event timestamps for timing purposes are server-assigned (`ts`/`at` columns set by the server on insert, per `AppendAgentEventTx`; `timing.go`'s `Split` also special-cases `mark` events to use their own `at` payload field when parseable, else falls back to the recorded `At`). Card time must use the same authority, never a daemon-local wall clock, so a skewed daemon cannot shift totals or create negative intervals. | Layer 1 contract: feed an event whose payload claims an `at` in the future and assert the span is clamped to `[start,end]`, matching `addMeta`'s existing clamp (`if a.Before(start) ... if b.After(end) ...`). |
| Overlapping workers on one card | Acceptance criteria: "overlapping workers count once per worker, never doubled within one session" — this is about not double-counting a single session's own overlapping tool spans (already handled by the sweep-line `Split`/`SplitOrdered`, which assigns every instant to exactly one class/phase). Two *different*, directly-linked, non-shared sessions genuinely working the same card concurrently are two workers and their seconds both count (real parallel work, not double-counting one worker). | Layer 1 contract: two sessions with overlapping `[start,end]` windows, each directly and solely linked to the same card; assert the card total is the sum of each session's own (non-overlapping-within-itself) total. |
| Review marks for the wrong board | `HandlePostMarks` already refuses a `review`/`failure` mark whose `(board_id,card_number)` the posting session's family has no `session_card_links` row for and no board access to (`allowed` check, lines 161-184 of `marks.go`) — so a mark cannot attach review rounds to a card the session has no real relationship to. Card-time's round count must read marks through the same `(board_id,card_number)` key the mark was *accepted* under, not a looser match (e.g. card number alone across boards). | Layer 1 contract: two boards with the same card number, each with its own `review` mark; assert the per-card round count for board A's card never includes board B's mark with the same number. |
| **(L8)** A review round number skips or repeats | Rounds are counted as the number of **distinct accepted review marks** for `(board,card)`, never `max(round)` — a round field that skips (an import, a retried post) must not under- or over-count. | Layer 1 contract: three review marks for one card with `round` values `1, 1, 3` (a duplicate post and a gap); assert the round count is 3 (three distinct marks), not `max(round)=3` coincidentally matching, by also testing `1, 2` → round count 2, not `max=2` — i.e. prove the implementation counts rows, doesn't read the `round` field at all. |
| Budget interval changed between crossings | The interval is a single account-level setting (owner's decision 3); a multiple is recorded once, keyed by `(account_id, board_id, card_number, multiple)` (§3). Changing the interval changes which multiple a given total maps to going forward, but every multiple already fired keeps its own permanent dedup row, so lowering or raising the interval can only ever add newly-reached multiples, never re-fire or retroactively un-fire one already recorded. | Layer 1: a dedup-key unit test proving two different multiples for one card are independent "fired" rows, and that recomputing "which multiple does this total represent" after an interval change does not touch already-fired rows. Layer 3b: real table, change the interval setting after one multiple has fired, cross a new multiple under the new interval, assert exactly one new row and one new notice. |
| Many cards crossing at once | The turn-end check runs per card the ending turn's own session is linked to (bounded — at most the small number of direct links a single session can have), never a scan over every card the account has ever touched. A burst of many cards crossing in the same window (e.g. several workers finishing turns close together) must not flood a Main with one notice per card in one message — nudges already batch ("Your plan drifted..." lists multiple facts in one text, `planNudgeText`); card-time crossings are additional facts in that same batched nudge the next time that Main's own next input is prepared. | Layer 2: `planNudgeText` given N card-time facts renders one message listing N lines, not N messages. Layer 3b/4: cross five different cards' thresholds from five different workers under the same Main in the same window and assert exactly one plan-drift delivery to that Main containing all five, each named only by board/number/multiple. |
| **(H2)** Notice delivery target | A card-time crossing is detected while a **worker's** turn ends, but the notice must reach that worker's **Main** (its root/coordinator ancestor — the session with no parent, found by walking `parent_session_id`), never the worker itself (workers are not who reads plan-drift nudges in any useful sense, and per H1 are never the ones a human is driving turn-by-turn). The plan fact is written against the root session's own `session_plan_nudges`, not the worker's. | Layer 3b/4 (through the real notice path, not a unit stub): a real Main with a real child worker; the child's turn ends and crosses a threshold; assert the plan-drift fact lands in `session_plan_nudges` keyed to the **Main's** session id, and that the Main's *next* input (through `preparePlanNudgeMode`) carries the nudge text — not the worker's next input, if it even has one. |
| **(M5)** Cross-account leakage | Every card-time query filters by `spawning_account_id`, exactly like `InsightActivityScope`/`InsightActivityTimings` already do (`insightOwnedScope`'s `WHERE spawning_account_id=$1`). The dedup/ledger keys include the account id, so two different accounts' workers on cards that happen to share a board/number combination (a shared team board two separate accounts both have sessions against) never see or dedup against each other's time. | Layer 1 contract: two accounts with sessions linked to the same `(board,card)`; assert each account's computed total includes only its own sessions' time. Layer 3b: real schema, same fixture, assert account A's Main is never notified about account B's crossing and vice versa, and the dedup row for account A does not suppress account B's otherwise-identical crossing. |
| **(M7)** Restart between recording "fired" and recording the notice | The dedup insert (`card_time_threshold_alerts`, keyed `(account_id, board_id, card_number, multiple)`) and the plan fact (`session_plan_nudges`) for that crossing are written in **one transaction**. A restart between them is impossible by construction: either both are durable or neither is, so a crash can never fire a notice with no dedup row (which would re-fire on retry) nor record "fired" with no notice (which would silently swallow it). | Layer 3b: real Postgres, inject a failure/kill between the two inserts in a test harness that can observe mid-transaction state is never visible, then assert after recovery exactly one of {both rows exist} or {neither exists} — never a dedup row with no corresponding plan fact or vice versa. |

## 3. Data: where the per-card total comes from

**Reuse, don't re-derive.** The attribution query already exists in
`InsightActivityScope`/`InsightActivityTimings`
(`runner/internal/db/insights_activity.go`, BLG-424 branch, head `e9b80cfb`)
and in `activity.SplitOrdered`/`SplitOrderedWithHours`
(`runner/internal/activity/timing.go`). Per **(H1)**, card-time's own
attribution query is *narrower* than Insights' account-scope query, not a
reuse of its ancestor-walking CTE — see below.

- **Which sessions attribute to a card (H1, revised from the first draft).**
  A session counts toward `(board_id, card_number)` only when:
  1. it has its own `session_card_links` row for that exact `(board_id,
     card_number)` — no walking `parent_session_id` to find an ancestor's
     link, unlike `InsightActivityScope`'s `insightOwnedScope` CTE (that CTE
     answers "is this in my account's scope at all," a different question);
  2. it is **not** a root session (`parent_session_id IS NULL`) — Main and
     workspace coordinator sessions are excluded even if directly linked,
     since they are not "a worker" in the sense this feature means; and
  3. it is linked to **exactly one** card — a session linked to more than one
     is excluded from all of them, matching Insights' own `Shared` flag
     (`len(Cards)>1` in `insightTimingSession`), because the recorded events
     don't say which seconds were "for" which card.
  4. every read is additionally scoped by `spawning_account_id` **(M5)**,
     matching `insightOwnedScope`'s own `WHERE spawning_account_id=$1`.
- **Per-session total.** `activity.SplitOrdered(start, end, events, waits)`
  returns a `Timing{Seconds: map[string]float64, ...}`; the "worker time" for
  one session is the same figure Insights' own card-active-time display uses
  for that session (owner's decision 1: "count exactly what Insights calls
  card active time, and test the reconciliation" — whatever bucket selection
  Insights' UI sums for its headline number, card-time sums identically,
  through the same shared Go value, not a re-derived subset of `Seconds`).
  `InsightActivityTimings` already computes this per session, cached in
  `session_insight_timings` keyed by a content fingerprint for *ended*
  sessions; for a still-running session that cache is intentionally short-
  lived (**M4**), so card-time's own incremental ledger (next) is what keeps
  the check cheap without relying on that cache being warm.
- **A running per-card ledger, updated incrementally (M4).** A small table:

  ```sql
  CREATE TABLE card_time_session_ledger (
    account_id    text    NOT NULL,
    board_id      text    NOT NULL,
    card_number   integer NOT NULL,
    session_id    uuid    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seconds       double precision NOT NULL,
    computed_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, card_number, session_id)
  );
  CREATE INDEX card_time_session_ledger_card_idx ON card_time_session_ledger(account_id, board_id, card_number);
  ```

  At a worker's own turn-end (inside `checkPlanTurn`, never inside
  `preparePlanNudgeMode`), if that session is eligible per H1 (directly
  linked, not root, linked to exactly one card): recompute **only that
  session's own** current timing (bounded — one session's event history, the
  same call `InsightActivityTimings` already makes per session), and
  `INSERT ... ON CONFLICT (board_id,card_number,session_id) DO UPDATE SET
  seconds=..., computed_at=now()`. The card's current total is then `SELECT
  sum(seconds) FROM card_time_session_ledger WHERE account_id=$1 AND
  board_id=$2 AND card_number=$3` — an indexed, bounded read over however
  many distinct sessions that card has (not a re-aggregation of every
  session's full history on every check). A session no longer eligible (it
  gained a second card link, becoming "shared") has its row deleted rather
  than left stale — the trigger that adds a second `session_card_links` row
  for a session also deletes any `card_time_session_ledger` rows for that
  session's now-invalidated prior single-card attribution.

- **Review rounds (L8).** `count(*)` of accepted `agent_events` rows with
  `kind='mark'` and `payload->>'kind'='review'` for `(board_id,card_number)`
  — a count of distinct marks, never `max(round)` (a round number can skip or
  repeat on a retried/imported post; the row count is the ground truth of
  "how many review passes were actually recorded"). The existing index
  `agent_events_review_card_idx` already supports this read.
- **Tooltip detail: latest verdict and open finding counts (owner's decision
  2).** From the single most recent accepted review mark for `(board,card)`
  (`ORDER BY ts DESC LIMIT 1` via the same index): its `verdict` and the
  count of its `findings` entries equal to `"high"` and `"medium"`
  respectively (`agent.MarkPayload.Findings []string`, each one of
  `high|medium|low` per `topics.CleanMark`'s validation). "Open" means "as of
  the latest round" — an earlier round's findings are superseded by the next
  round's mark, not accumulated; a card with no review marks yet shows no
  verdict/finding detail (same "shows nothing, not zero" rule).
- **Failure marks** (`payload->>'kind'='failure'`) remain a separate signal,
  indexed the same way (`agent_events_failure_card_idx`), and are not folded
  into the review-round or finding counts this feature surfaces.

### Recording a budget crossing exactly once, across restarts (owner's decision 3; M5, M7)

One account-level setting: an interval (default 4h). A card's budget fires
once at every multiple of that interval its total reaches (4h, 8h, 12h, ...),
never re-firing for a multiple already recorded. Modeled on
`session_failure_alerts`' durable-row pattern (migrations
`097_session_failure_alerts.sql`/`114_failure_alert_recovery.sql`/
`128_failure_notices.sql`; Go side `recordSessionFailure` in
`runner/internal/server/session_failure_alerts.go`, which checks existence
before inserting, with the row itself as the durable outbox):

```sql
CREATE TABLE card_time_threshold_alerts (
  account_id   text    NOT NULL,
  board_id     text    NOT NULL,
  card_number  integer NOT NULL,
  multiple     integer NOT NULL CHECK (multiple > 0),
  fired_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, board_id, card_number, multiple)
);
```

After the ledger upsert (previous section) updates a session's row, the
check recomputes the card's current total and interval setting, computes
`reached := floor(total_seconds / interval_seconds)`, and for every integer
`m` from `1` to `reached` not already present, attempts `INSERT INTO
card_time_threshold_alerts (...) VALUES (...) ON CONFLICT DO NOTHING
RETURNING multiple`. **(M7)** This insert and the corresponding
`db.AddPlanFact` call for the Main that should hear about it (next section)
happen in the **same transaction** as the ledger upsert that triggered the
check: either all three (ledger row, dedup row, plan fact) commit together or
none do, so a restart between them is structurally impossible, not merely
unlikely. Changing the interval setting only changes which multiples *future*
totals map to; it can never undo or re-fire a `multiple` already in the
table (the "budget interval changed between crossings" row in §2).

## 4. Surfaces

**The plan_drift notice to Main (H2).** Exactly the existing pipeline in
`runner/internal/server/plansync.go`, but — unlike the general drift facts
`planDrift` computes for the session whose own turn just ended — a card-time
fact is written against the **root ancestor** of the session that crossed
the budget (walk `parent_session_id` to the session with none; that is the
workspace's Main or coordinator), via `db.AddPlanFact(ctx, tx, rootSessionID,
key, text)` inside the same transaction as §3's dedup insert. It is still
delivered the same way thereafter: held in that root session's own
`session_plan_nudges`, attached to its next input by `preparePlanNudgeMode`,
batched with any of its other drift facts by `planNudgeText`.

The text names **only ids and numbers (L9)**: a full ticket key with its
board context (e.g. `BLG#147`, not a bare `#147` that could be ambiguous
across boards the Main or its workers touch) and the multiple reached — no
worker titles, no session ids, no prose copied from any mark, and no file or
repository path (the earlier draft's reference to this design doc's own path
was wrong and is removed). For example: `"BLG#147 has reached 8h of worker
time across 3 review rounds."` This satisfies the rule already written into
`plansync.go`'s own comment ("facts wait for the next input and never start a
turn") and the brief's "notice delivery... must never block a turn."

**A quiet glyph on the pinned card row and the checklist item.** Pattern:
`runner/frontend/src/components/SessionRail.tsx`'s `PinRow` (~line 1023,
rendered by `PinLines` ~line 1003), whose own doc comment states the rule
this feature must follow: *"its state as one glyph (✓ done, ◐ in progress, ○
not started, all one weight; the words in its tooltip...)"*, drawn from
`STATE_GLYPH[v.state]` (re-exported in `runner/frontend/src/lib/pins.ts` from
`packages/chat/src/model/worklist.ts`'s `PIN_STATE_GLYPH: Record<PinState,
{glyph, words}>`) into a `<span className="rail-pin-state ..." title={words}>`.
Per [[feedback_sidebar_cards_glyphs]] ("state as one glyph, words only in
tooltips/aria/header"), a new glyph is a sibling `<span>` beside that one, not
a replacement of it or a second state system — the pin's done/in-progress/not-
started state and its time/rounds state are two independent facts, each one
glyph. The checklist-item row (`PlanChecklist.tsx`'s `Row`, ~line 110) already
has the same shape for assignees (`Assignees`/`glyphOf`, lines 33-56: `🤖` /
`↳`, name and engine only in `title`/`aria-label`) and for commits
(`CommitChip`, lines 66-108: a `±` glyph, detail in `title`). A new `⏱` glyph
for a checklist item whose text names a card
(`cardRefs`/`ChecklistRefs.tsx`, the same `#N` parsing `plansync.go` already
uses) follows that same shape. In both places: shown whenever the card has
linked work with a known total (a card with no linked work, or whose total
isn't known yet — pending or disqualified — shows nothing extra, matching
"no linked workers shows nothing, not zero"), regardless of whether a budget
threshold has fired yet, with `title`/`aria-label` carrying the total, the
latest verdict, and the open-finding counts (owner's decision 2) — e.g.
"6h42m worker time · 2 review rounds · changes requested · 1 high, 2 medium
open". Only the plan-drift *notice* (above) is gated on crossing a multiple;
the glyph itself is not.

**The budget interval setting in the existing settings panel (owner's
decision 4: account-level, in the existing panel — no per-workspace
setting).** Pattern: `runner/frontend/src/components/SettingsDialog.tsx`
mounts separate sub-components per section (e.g. `<UsageNoticeSettings />`);
the closest existing analog for an install/account-wide numeric setting is
`UsageNoticeSettings.tsx` itself — it fetches `/api/settings/usage-notices`,
renders a plain `<input type="number" min max>` with client-side validation
and a `PUT` to save and a status message, backed server-side by
`runner/internal/server/usage_notice_settings.go`'s
`HandleUsageNoticeSettings` (`a.authDaemonOrCoreCap(w, r, "account.manage")`,
values persisted in the generic `runner_settings(key, value, updated_by)`
table). A new `CardTimeBudgetSettings` component follows that shape exactly:
one numeric input (hours, default 4), natural width, no stretched dropdown
([[feedback_use_the_space]]'s sibling rule and the worker UX rules above),
one `runner_settings` key (e.g. `card_time_budget_interval_hours`), one
GET/PUT endpoint mirroring `HandleUsageNoticeSettings`'s auth and persistence
shape exactly.

## 5. Decisions (previously open questions)

These were open in the first draft; the owner has decided all five.

1. **Which seconds count as "worker time."** Decided: exactly what Insights
   calls card active time for a session — the same Go value, not a
   re-derived subset. Build a reconciliation test asserting this feature's
   per-session figure equals Insights' own displayed figure for the same
   session and period.
2. **Findings in the tooltip.** Decided: the latest round's verdict, its open
   high and medium finding counts, plus the total round count (count of
   distinct marks, per L8) — not a cumulative count across all rounds.
3. **Budget shape.** Decided: one account-level interval setting (default 4h),
   firing once at every multiple reached (4h, 8h, 12h, ...), dedup key
   `(account_id, board_id, card_number, multiple)`. No tiered/escalating
   schedule, no per-card override.
4. **Setting scope.** Decided: account-level, in the existing settings panel
   (the owner's standing rule: settings live there). No per-workspace
   setting, despite Insights itself being workspace-scoped — the two are
   independent choices and this one follows the settings-panel rule, not
   Insights' own scope.
5. **Does a card's total freeze at a terminal column?** Decided: no, keep
   accumulating. A closed card with no further linked work simply stops
   growing on its own; nothing freezes it explicitly.
6. **BLG-424 branch stability.** The owner is building against branch head
   `e9b80cfb`'s shape behind a small interface (an adapter package isolating
   this feature's dependency on BLG-424's per-session timing call and
   caching, so a rename there before it merges is a one-line change to the
   adapter, not a ripple through this feature's own code) and will merge
   BLG-424 before this feature.

## The coordinating skill

The `managing-tickets` skill (and any card-time-specific follow-up skill)
should say plainly: **card time and review rounds arrive through plan-drift
notices to Main; there is no manual tracking step, and workers never see
these notices themselves (H2) — only the coordinating Main does.** An agent
working a card does not compute or report its own time budget — a Main reads
the notice when one arrives, the same way it already reads "this pinned card
closed" facts, and otherwise proceeds exactly as before. The skill text
should not introduce a new `blerg-runner` subcommand for "check my card's
time" — that would be the manual tracking this feature exists to avoid.
