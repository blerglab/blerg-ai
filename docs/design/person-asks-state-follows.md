# The person asks, state follows

BLG-496 · 0.6.0 design · Revised for the owner’s final ruling: both parts ship through one mechanism. Round-3 requirements incorporated; implementation proceeds in three separately committed increments.

Chat and Needs you preserve the person’s context and attention. Every decision uses an existing person-required `blerg-runner ask`, visible in both places. A bound ask adds a server-held subject; it does not add an approval panel, list or badge. The blackboard only displays the record.

## Failure rule

No false Final, ever. Failure may cost delay, clarification or a fresh ask, never unauthorized scope, fabricated consent, duplicate effects or lost history. Unknown cuts stay open; old cuts and legacy Finalizing records show “Released on board; evidence incomplete” until evidence is sufficient. Every failure logs its typed cause. Ordinary chat, authorized unplanned work, board reads and unrelated questions must keep working.

Diagnostics use the shared failure logger with cause, stage and safe correlation IDs, rate limiting and suppression counts. Never log raw answers, titles, card content, credentials, paths or upstream bodies. Protected audit records retain the person’s answer and its provenance separately from logs.

## One server-bound ask

Only the workspace root returned by `blackboardMainSession` is Main and may raise a bound ask. Channels and ownerless sessions are ineligible (L-c). Children, including sandboxed and cluster workers, must escalate to Main; refuse their creation attempts and log the cause (H2). Do not bypass parent-answer-first routing: `childQuestionPending` is in [child_questions.go](../../runner/internal/server/child_questions.go), called from `messages.go`.

The server resolves ownership and access, stores an immutable subject on the existing ask message, and makes it person-required. Main cannot decide it. Supported subjects are:

| Subject | Exact server-held content | Owner’s answer commits |
| --- | --- | --- |
| Plan proposal | Workspace, blackboard, milestone, proposal ID, base revision, exact selected change IDs and canonical content digest | The selected immutable operations and a new plan revision; decline records rejection without applying changes |
| Blackboard draft | Workspace, draft ID, revision and content digest | Creation from that exact draft, or discard on decline |
| Release cut | Workspace, milestone, board/release ID and next release ID (explicit none when not needed), plus any explicit pre-cut link requested | Cut authorization and its durable execution record, or a declined cut with no execution; unlinked-milestone Final is deferred |

Drafts require a migration for stable IDs/revisions; replacing a draft creates a new revision, retains its old snapshot and invalidates its old ask (L4). Editing a proposal or selection requires a new subject and ask. Never rebind an open ask. Store the owner account, server creation/expiry times, subject digest, ask state, decision receipt and authenticated answer account/login. Enforce one open bound ask per workspace initially, with idempotent creation and explicit audited supersession.

The question starts with the authoritative server summary: exact objects, reasons, destinations, revision and per-operation add/remove/move counts in the title (M3). Link the immutable snapshot through a read-only question-card variant, including the chat package version bump (L3). Omit free-form Main prose for bound questions so it cannot contradict the decision. This remains the existing question surface.

Only the owner’s authenticated reply to that question counts, whether sent from Needs you or as a reply to the question in chat. Require an explicit ask ID; never choose the newest ask. An untargeted yes commits nothing and adds the server note “Not applied: reply to this question” (M1). Main may claim a commit only from the server receipt.

Use deterministic exact answers after trimming whitespace and case folding: yes/approve accepts the complete bound selection; no/decline declines it. Other text is clarification, never a model-classified approval. Preserve the original words as ordinary chat and deliver them to Main with the ask target cleared; leave the question unresolved. Untargeted replies follow that same conversation path and cannot commit. Partial changes require a revised subject and fresh ask.

## Answer and commit boundary

Authenticate the answer at server ingress as the current owner acting as themselves, with a live human login, no `OnBehalfOf`, and current workspace/board permission. Reject session tokens, runner keys, agent API tokens, daemon events, parent/worker messages, claimed human source fields, imported transcripts and `mark decision --by person`. An ownership transfer cancels the open ask; the answerer must be the current blackboard owner (L-d). The socket carries the verified principal’s real kind and OnBehalfOf, never an inferred human identity. A host agent able to read the browser profile, and a board token used outside the runner gateway, are outside this guarantee (L2, M2).

Gather board permissions and display data before opening the write transaction. In one runner database transaction, lock the ask/binding and subject partition; revalidate ownership, board scope, exact IDs/digest, current revision, pending/unexpired state and unfrozen milestone; recheck the verified login against revocation even when no board is linked; apply the subject; write the answer, decision/audit event, result revision or created record, input receipt and binding outcome. A negative decision checks identity only; a changed subject or shipped release must not prevent a decline. Cancellation is separate from decline. Refactor the existing [plan service](../../runner/internal/blackboard/plan.go) and store to participate in this owning transaction, rather than opening a second pool transaction.

The server does this without Main’s daemon (H1). Route all targeted bound replies to this transaction before generic ask delivery, input holds or engine submission. Intercept bound replies before `acceptSessionInput` writes any engine outbox frame. For legacy pending frames carrying a bound ask ID, replay must refuse delivery and settle the row. This includes transaction failures in `answerAskFrom`: the answer must never remain deliverable in `session_inputs`. Preserve `AnswerMessageID` and verified provenance through desktop holds, cluster Job resume prompts and replay. Inject a commit failure and prove replay delivers nothing (round-3 M1). A decision attempt, including a failed write, never becomes engine input. Clarification and untargeted conversation retain their words and use the ordinary chat outbox with no ask target. After commit, durable delivery wakes the existing waiter and sends the recorded outcome; restart retries notification, not the decision. Same ask/input retries return the saved receipt; conflicting replay cannot act again.

Release close lives in the board database, so do not claim cross-service atomicity: the answer transaction commits the exact cut authorization and a durable server execution/outbox record. The server executor then invokes guarded board close with that ask’s proof, without needing a daemon. A failed/unknown board response leaves the cut pending and retries by reading authoritative state. “Cut authorized” and “release closed” are distinct receipt states. Plan/draft commits complete entirely inside the runner answer transaction. Unlinked Final is deferred. External permission reads fail closed when unavailable; never retry using cached broader access.

Closing an unanswered ask cancels its binding, never commits or declines it (M2). Cover session end, discard/archive, dismissal and explicit withdrawal. A CLI poll timeout, host waiter-ended notice (including restart replay), or daemon empty-answer notice only ends the waiter: hold the live bound ask for the person until its 24-hour deadline. They cannot cancel or extend the binding. Named legacy paths must not bypass this rule: `CloseOpenMessagesForSession` in [queries.go](../../runner/internal/db/queries.go), empty answers and `olderLoopClose` in [messages.go](../../runner/internal/server/messages.go), `DismissMessage` in [questions.go](../../runner/internal/server/questions.go), and newest-ask selection in [runner.go](../../runner/internal/server/runner.go) (M4). Test each. A close after commitment cannot undo the decision or erase its receipt.

## Tight failure matrix

| Failure | Required outcome |
| --- | --- |
| Ambiguous or partial answer | Keep the original words as chat to Main; note clarification on the question. No plan mutation. |
| Stale revision or changed proposal/draft | Cancel with a typed cause and recoverable receipt through the existing question notice; Main refreshes and asks again. Never rebase or reuse yes. |
| Different person, forged source, agent/self/parent answer | Refuse; no binding consumption. Server-authenticated owner identity is required. |
| Child creates bound ask (H2) | Refuse at creation, log cause, require escalation to Main. |
| Untargeted yes or two pending asks | No newest-ask selection; note “Not applied: reply to this question.” Database uniqueness prevents overlapping bound asks. A new Main plan ask atomically supersedes its older open plan ask with `superseded_by=<new ask id>` and the existing notice. Main may explicitly withdraw its ask with `ask --resolve`; a waiter timeout never withdraws it. |
| Replay or concurrent replies | One durable result; exact retry returns it, conflicting retry fails. Input predating ask creation is ineligible. |
| Daemon away, desktop hold or cluster resume (H1) | Server commits with ask target intact; no answer becomes engine input. |
| 24-hour expiry, session end/discard/archive, owner withdrawal or dismissal | Cancel the unresolved binding without a decision. CLI timeout, waiter-ended replay and daemon empty-answer notices hold it for the person. |
| Server restart or write failure | Before commit: roll back. After commit: recover receipt/outbox and retry delivery/execution only. |
| Cut without answered cut ask (M-C) | Main cannot close, even with Full access. Direct person board cut is the authenticated fallback. |
| Released target at link time or link timestamp not before cut (M-A) | Refuse link or Final, log `link_after_cut`/`release_already_released`; no backdating. |
| Access expired/revoked or Main ended (M-B) | No reconciliation Final; preserve open or evidence-incomplete state and log the specific cause. |
| Missing/mismatched cut receipt or legacy snapshot | No Final; known old cut/Finalizing shows evidence incomplete. |
| Duplicate cut detection | Return existing Final; no duplicate revision/event or card move. |
| Detection never occurs | Unknown milestone stays open and logs cause; known incomplete evidence stays explicitly incomplete. |

The server clock controls binding expiry (proposed default 24 hours, stated in the question). Ask/subject replacement, answer, cancellation and finalization use a fixed lock order. Concurrent plan mutations and Final serialize; nothing changes frozen history.

## Cut authorization, evidence and reconciliation

**M-C: Full access is not consent.** Before Main’s cut executes, require the answered person-required cut ask naming the exact release and next release. Its ID goes into cut provenance. The close path verifies server-held ask context and authenticated answer, never a caller’s claim or copied answer. Record a canonical card/version digest in the cut subject; compare it before execution. Set a 24-hour execution deadline from authorization. Archive-gate denial, archive-gate hold, active card sessions, ErrStaleVersion/digest change, release already closed without this ask’s receipt, lost_credentials and execution_deadline are typed terminal execution states. Each creates a fresh existing person-required ask in chat and Needs you: “Cut authorized, not executed: <cause>.” Never endlessly retry them; a new decision creates a new cut attempt. A released receipt with this same ask ID is successful replay, not another execution (round-3 M3). The executor uses the owner’s live login captured from the answer, not a synthetic identity; expired/revoked login stops execution as lost_credentials. The server executor is the only runner caller allowed to cut: the gateway refuses `blerg_release_close`, `blerg_release_save` and `blerg_release_delete` for every session grant, including Full. Main’s authorization does not bypass that refusal (round-3 M2). Main also needs Full board access with a person signed in behind it; keep archive admission, active-session and stale-review guards. When that person-behind proof is unavailable, the person cuts through the existing board controls; record that direct authenticated act instead of fabricating an ask. Public publication remains a separately authorized operation.

For linked milestones, 0.6.0 detects only the exact linked board/release reaching `released`. No tag watcher, name/version fallback or card-count inference; link-only satisfies the card for this branch. Retire [Import’s Final-from-tags path](../../runner/internal/blackboard/import.go), including its `cmd/blackboard-seed` caller (L-b); imports cannot set Final or FrozenAt.

**M-A: link before cut.** Direct blackboard creation and milestone construction in [blackboard_plan.go](../../runner/internal/server/blackboard_plan.go), draft validation and consumption in [blackboard_draft.go](../../runner/internal/server/blackboard_draft.go), and any linking path must refuse already-released targets and verify board membership. Store immutable link provenance, revision and server `linked_at` for the exact tuple; a changed tuple gets a new timestamp. At Final, inside the runner transaction, reload/lock the link and require the recorded link to precede the cut intent for the exact receipt ask ID. Order runner cuts by cause (that ask ID), not cross-service clocks. For direct person board cuts only, require `linked_at` before `receipt.released_at` with a conservative five-minute skew margin; fail closed within that margin (L-a). A close/link race, missing/uncertain timestamp or mismatch fails closed. Never backdate.

**L-5: snapshot source.** Add a new immutable board table, proposed `release_cut_receipts`. In the same [guarded close transaction](../../board/internal/db/releases.go) that locks the release/cards, archives Done cards, moves open cards and records released status, insert one immutable receipt: board/release and close-event IDs, released timestamp, authenticated provenance/ask ID, next release, versioned pre-cut card identities/titles/versions/membership/columns/terminality, and archive/move results. Receipt failure rolls back close. Uniqueness prevents duplicate close effects; subsequent edits/restores never rewrite it. Both server execution and the board UI use this guarded path.

Extend read-only `blerg_release_get` with `cut_receipt` and an `evidence_only` compact read (including the active card/version preview), protected by existing board visibility. Freeze the runner’s current plan revision, decisions and observations with the board’s pre-cut evidence, labeling runner freeze time separately from board cut time. Never describe this as an atomic cross-service historical snapshot or invent missing daily history. The runner Final transaction verifies link/evidence, writes frozen history and one derived Final event, and consumes the cut identity uniquely. “Shipped <date>” uses board released time, never retry time.

**M-B: principal and triggers.** Reconcile using the workspace Main’s stored board access, rechecking effective permissions on every receipt read. Never borrow the viewer’s, a child’s or a service-wide grant. No board-to-runner notification exists: poll on an interval (60 seconds), check at startup and check on blackboard read, with bounded deadlines/backoff. Record last observation/time. Expired/revoked/unavailable access or an ended Main leaves the milestone non-Final and logs `reconciliation_access_expired_or_revoked` (Core intentionally returns the same missing-proof response for both), `reconciliation_access_revoked`, `reconciliation_access_unavailable` or `reconciliation_main_ended`. Replacing Main/access requires an explicit authorized change. Remove the write exception in [blackboard_board.go](../../runner/internal/server/blackboard_board.go): the adapter permits only reads, always `ReadOnly: true`, and refuses writes (L-4). Cut execution uses its separate authorized board path.

**Unlinked milestones:** Final is deferred; leave them open, with a typed cause and no manual Final bypass.

**L-2: old evidence.** Old cuts and old Finalizing records display “Released on board; evidence incomplete,” never plain open or Shipped while unresolved. Distinguish verified released state from legacy evidence awaiting verification. Automatically audit and clear legacy transient locks, preserving intent/snapshot. Drop the legacy-snapshot verifier: old snapshots remain history and do not establish new Final. No backfilled receipts or inferred consent.

## First users and rollout

1. **The owner’s 33-card 0.6.0 proposal.** Refresh the proposal and its exact selected change IDs/revision. Raise the bound plan ask and commit only on the owner’s new authenticated reply. “Card proposal sounds correct” in earlier chat is context, not approval migration.
2. **Draft setup.** Save a draft with stable ID/revision, show its immutable summary in the existing question and commit Create/Discard through the answer transaction. Requested edits produce a new draft revision and ask.
3. **The 0.6.0 cut.** The live milestone has no board_id or release_id today. Main resolves the board’s active 0.6.0 release IDs and includes an explicit link of that existing milestone in the cut subject/summary. The owner’s answer transaction records the link and `linked_at` while the target is active, then commits cut authorization/outbox. Confirm that link before executing board close; at Final enforce link-before-released time. Never silently use the unlinked exception for this board-backed milestone, relink an already cut release or recreate its plan.

## Remove bookkeeping controls

In [BlackboardTabs](../../runner/frontend/src/components/BlackboardTabs.tsx) and [BlackboardProposal](../../runner/frontend/src/components/BlackboardProposal.tsx), remove **all state-changing controls** and their direct server routes: Final, Retry Final, Abort and confirmations; next-release selector; proposal selection checkboxes, Commit/Approve and Decline/Reject; draft Create/Edit/Discard; empty-state setup actions and setup form; plain-item add, complete and remove. Retire final/abort, commit/apply, direct create/from-draft/discard routes. Reuse private transaction-aware service logic only behind verified ask subjects or derived Final evidence. Observational writers cannot claim person decisions or Final. Review every writer, including imports and old-client routes.

Keep navigation, filters, sorting, read-only proposal/draft/card links, refresh and read-error retry. Show Shipped only from committed Final evidence. No new attention surface. Keep theme tokens, American English and sentence case. Verify real-app screenshots at 390 px, in a 1024 px pane and at 3840 px; inspect and publish each. Chat changes require its version bump.

## Planning rules and proof

Update [the planning skill](../../runner/skills/planning/SKILL.md) and distributed copies with implementation: Main drafts/proposes then raises a person-required bound ask; only the owner’s targeted authenticated answer commits. The blackboard displays state. Exact items/revision remain mandatory. Main never claims success without a server receipt. Urgent work remains Unplanned until decided. Cut authorization names release/destination, uses the answered ask as provenance, and Final follows the evidence rules above; unlinked Final remains deferred. No manual Final, standing-grant approval, backdating or imported consent.

Before building, review this note against the failure rule. Implementation proof includes contract cases for the matrix, real-database atomicity/races/restart tests, and real-process CLI/Needs you/chat/native/held/replayed answer tests. Prove each authorization guard matters by removing it temporarily and observing the intended test failure. Name and test each legacy close path. Inject failures at answer, subject, audit and outbox writes; use a canary to verify safe diagnostics.

Release e2e runs a real guarded fixture cut, including the answered ask and live-milestone link, automatic Final, board rollover, duplicate detection and restart after close. Cover direct person fallback, Full-without-consent refusal, released-target rejection, link/close races and timestamp checks, unlinked refusal, expired access/ended Main, polling/read triggers and legacy evidence-incomplete status. Assert tags/imports cannot Final and the blackboard adapter cannot write. Never publish a production release as a test.

Run affected suites and regional lint/types/build, theme lint (zero violations), scrub and links; report exact commands/counts and skips. The implementation and its proof follow in the separately committed increments below.

Build and report separately: (1) plan-proposal bound asks and removal of their decision controls, first used for the 33-card proposal; (2) draft bound asks and removal of draft/setup controls; (3) cut execution/reconciliation and removal of Final controls. Each increment needs its own commit, real-process binding matrix (no mocked matrix), and real-app question-card screenshots in Needs you and chat at 390 and 3840 px, plus the 1024 px pane check. Main handles each separate merge.

### Session-end lock order

Session triggers only increment a durable session epoch; they never update or lock questions or bindings. Creation captures the epoch. Answers lock the subject, binding and question before taking a shared session lock, then reject terminal, archived, discarded or epoch-mismatched sessions before any decision. The session lock lasts through commit. An end followed by resume, or ownership transfer followed by transfer back, cannot revive an old ask. The server reconciles invalidated asks at startup and every 30 seconds after lifecycle commits, with a bounded batch timeout, typed `session_invalidated` cancellation and the existing notice/waiter signal. A blocked question can delay delivery, but cannot block session end or permit a commit; reconciliation failures log their cause and retry from durable state after restart.

Implementation uses runner migration 150 and board migration 042. The runner archives its plan, revisions, decisions, observations and daily snapshots in its own repeatable-read Final transaction; the board's pre-cut snapshot remains a distinct earlier observation. Frozen bodies stay out of ordinary paginated slice responses. The question shows board/release names and per-card effects; exact IDs, versions and digest remain in its immutable server-held subject. Terminal failure questions close automatically if a later verified receipt proves the authorized cut actually executed; no answer or consent is fabricated.


### Cut execution retry and read limits

The 24-hour authorization survives access-JWT expiration. Each new execution checks the answerer's original login SID and lineage revocations plus Core's live login-session lookup; the stored JWT's `exp` still gates the original answer, not execution retries. Confirmed lost credentials and ended Main are terminal. Database or Core unavailability defers execution with `db_unavailable` or `core_unreachable` until the existing deadline; it never invents a credential refusal. A changed card digest has its own `card_digest_mismatch` cause. Terminal failure wakes Main's ask waiter and creates the existing fresh person-required question.

Receipt reconciliation checks each milestone at most once every 30 seconds across reads and runner replicas, using an atomic database claim. Pagination slices never trigger it. A newly executed receipt triggers one immediate check. Legacy snapshots and releases known to have no immutable receipt stop polling. Serialization conflicts, concurrent updates and storage failures leave state unchanged and retry; only actual provenance/evidence failures mark evidence incomplete.

## Linking a board after creation (BLG-503)

Main uses a separate one-change plan proposal to link or unlink a board. For example:

```sh
blerg-runner plan propose --changes '[{"operation":"link","item":{"board_id":"BOARD_ID","reason":"Track this board’s work"}}]'
blerg-runner ask --blackboard BLACKBOARD_ID --plan-proposal PROPOSAL_ID --revision REVISION --no-wait "Link this board?"
```

The proposal changes nothing until the owner answers its bound ask. The server
checks the owner's current read access to the target board with the live login at
answer time, then commits the link, proposal decision and revision in the answer
transaction. Main's standing board grant does not substitute for that check.
Decline leaves the links unchanged.

Use `"operation":"unlink"` with the same item shape to unlink. Unlink refuses
while any current or retained plan item or milestone references that board;
references must be resolved before unlinking. It preserves the workspace link.
A board link does not link a milestone to a release or change release provenance.
Propose card changes in a fresh proposal after the link commits. An unlinked-card
refusal names the board and tells Main to link it first; the CLI displays that
server reason, while logs record a typed cause without upstream content.
