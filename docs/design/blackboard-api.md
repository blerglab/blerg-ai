# Blackboard API handoff

The runner owns the record. All browser requests use the existing bearer authentication
and `?session=<workspace-session-id>`. Every read, including history and snapshots,
checks the linked workspace, its current board grant and live Core board access.
Responses are `Cache-Control: no-store`. A revoked grant must clear any previously
rendered card text in the client. Error responses use the runner's standard error shape.

## Record and plan (Task 2)
- `GET /api/blackboards`: `{blackboards:[{id,name,milestones}]}` for the linked workspace.

- `POST /api/blackboards`: human workspace owner only. Body:
  `{board_id,name,vision,goals:[string],milestone_name,release_id,thrusts:[{name,goal_index}]}`.
  Returns the new `Record` (201). This creates structure, never approved membership.
- `GET /api/blackboards/{id}`: `{record: Record,pending_proposals: number}`.
  Proposal details are omitted for readers other than the owner. Object and field
  shapes are the JSON tags in `runner/internal/blackboard/record.go`.
- `POST /api/blackboards/{id}/proposals`:
  `{milestone_id,changes:[{id?,operation,item:{board_id,card_number,thrust_id,reason,destination?}}]}`.
  Operations: `add`, `remove`, `move`. Removal/move needs a destination; addition needs
  a thrust in that milestone. Returns `{proposal_id,status:"pending"}` (201).
  Omitted change IDs are generated once and returned in the owner's record read.
- `POST /api/blackboards/{id}/commit`: human owner only, never an agent or standing grant.
  `{milestone_id,proposal_id,revision,selected:[change_id]}`. Selection is exact;
  unselected changes in that proposal are rejected. Empty selection rejects the whole
  proposal. Returns `{status:"committed"}`. A stale revision returns 409. No expiry.

Each committed revision appends both a membership copy and a `plan_set` event with the
server-authenticated person, server observation time, exact proposal and selection.
Claimed person attribution in imported or agent text carries no plan authority.

## Follow-on interfaces

Tasks 3 and 4 add the live slice, Final, timeline and build-event routes here. The
screens worker can start against the record above; it should avoid depending on
uncommitted Go struct fields. Task 7 can use `blackboard.Store` and
`db.BlackboardStore{Pool: pool}` for setup/import, and append provenance events through
`Store.Update`. It must never manufacture `plan_set` events or approved revisions.
Task 8 can describe `plan show|history|propose`; no agent command commits a plan.

## Live slice and Final (Task 3)

- `GET /api/blackboards/{id}/milestones/{milestone}/slice` returns `SliceView`:
  `{milestone,cards:[{card,membership,stage,needs_reason,drift,deleted,live?}],counts,
  observed_at,complete,frozen}`. Stage keys: `done`, `to_test`, `in_work`, `next`,
  `needs_you`, `moved_out`. Counts also include `all` and the overlapping `unplanned`.
  Card summaries use the board's JSON names and contain no body. Card identity is
  always `(board_id,number)`. Board reads page through the entire release.
- `POST /api/blackboards/{id}/milestones/{milestone}/final`:
  `{revision,next_release_id}`. Human owner and Full board release-cut access only.
  Returns `{status:"final"}`. A durable `Finalizing` intent blocks plan changes
  before the board cut. Retry the identical request after a lost response; it reads
  the board's release status before deciding whether another cut is needed.

`Mapping` lives on `Record`, with optional column IDs and tag overrides. Defaults
use terminal columns, `to-test`, in-progress column names, and `needs-discussion`.
Visible open person-facing questions linked to a card also produce `needs_you`.
Incomplete reads never invent moves. An observed move without a recorded reason
sets `needs_reason`; it does not invent an actor or rationale. Frozen counts and
pre-cut titles remain fixed; drift and deleted-card tombstones are separate flags.

## Small workspaces (design update)

A workspace starts with no blackboard. Creating one needs only `name`; `board_id`,
`release_id`, `milestone_name`, thrusts and board links are optional. Workspace
identity is `record.workspace_id`, independent of board links. With no linked
board, workspace visibility is sufficient; no board gateway is contacted.

`POST /api/blackboards/{id}/plan` is the human owner's one-click action:
`{milestone_id?:string,revision,changes:[...]}`. It proposes and approves the exact
batch atomically, leaving no pending proposal. Agent proposals still queue.
Omitting `milestone_id` addresses the workspace plan (`record.plan_revision`).
Plain items use `{id?,title,goal_id?,progress?}` without board/card identity or a
thrust. Plain progress defaults to `next`. Milestones without release links can
be marked done by their person without any board cut. `GET /api/blackboards/{id}/slice`
reads the workspace plan. Hide levels and software-specific chrome when their
objects are absent; an empty milestone ID in this response is not a visible level.

## Timeline, builds and daily observations (Task 4)

- `GET /api/blackboards/{id}/milestones/{milestone}/timeline`:
  `{events:[Event],snapshots:[Snapshot]}`. `/api/blackboards/{id}/timeline` reads the
  workspace-level timeline. Missing dates are absent, never interpolated.
- `POST /api/blackboards/{id}/events`: authenticated human or linked session token.
  `{id?,milestone_id?,kind,claimed_at?,payload:{...}}`. Kinds: `opened`, `alpha`,
  `rc`, `date_moved`, `decision`, `correction`, `shipped`. `alpha` requires a commit;
  alpha/RC numbers are assigned under the record lock, independently per milestone.
  A stable caller event ID makes a retry idempotent. `date_moved` requires the owner
  and `payload.target` in YYYY-MM-DD. Generic events cannot set a plan or Final.
  Agent `by:person` attribution stays an assertion; authenticated actor and source
  are server fields. Agent responses mark the event as `untrusted_data`.

The server observes milestones at startup and hourly, once per UTC day, with a
30-second observation deadline. No milestone means no snapshot/chart job. Stored
workspace board proof is rechecked against current access. Incomplete reads store
an explicitly incomplete sample; a restart never duplicates or upgrades it. Final
milestones stop collecting samples. Post-Final corrections append evidence and
cannot alter frozen membership or counts.

## Agent CLI (Task 5)

`blerg-runner plan show|history|propose [flags] [milestone]` uses the installed Go
runtime, including the sandbox helper. Flags precede the optional selector:
`--blackboard ID`, `--cursor N`, `--limit 1..100`. Propose additionally requires
`--changes '[{"operation":"add","item":{"title":"Try an idea"}}]'`.
History accepts `board#number` in place of a milestone and searches all milestones.
Omitting the blackboard selects the sole record in the workspace/ancestor scope;
multiple records require an explicit ID. No record means a clear refusal, no creation.

The transport is `GET/POST /api/sessions/{session}/plan`, with the session's own
messaging credential. Query `operation` is show/history for GET and propose for
POST; POST body is `{changes:[...]}`. Propose returns only a pending proposal.
The server checks the exact token session, workspace ancestry, current board
permission and inherited worker caps. Pending proposal contents are owner-only.

Agent responses contain `untrusted_data`, bounded rows, `total`, `next_cursor`
and `truncated`. CLI output encodes the response as a JSON string inside
`untrusted_blackboard_data`, so even quotes and closing delimiters remain data.
The 32 KiB cap includes escaping and metadata. If a page itself must be clipped,
`retry_with_smaller_limit:true` asks the caller to repeat with fewer rows; no cursor
is supplied that could silently skip the clipped rows. No agent route or command
can commit, approve, apply a human plan, or Final a milestone.

Schema handoff: apply migrations **132–134**. Migration 132 retains the original
Task 2 shape; 133 upgrades existing numbered plans and makes board/release/milestone
references nullable, with a separate revision scope for workspace-level plain items.
Migration 134 scopes event IDs to their blackboard, indexes workspace lookup and
permits a later complete observation to repair an incomplete daily snapshot.

### Review hardening

Human record, slice and timeline reads accept `cursor` (an integer offset) and
`limit` (1–100, default 50), and return `next_cursor`. Record collections and the
two timeline collections use that offset independently; follow the cursor until
empty to read all collections. Frozen milestone evidence is returned by the slice,
not embedded again in the record. Workspace lists use the same paging contract.

`POST /api/blackboards/{id}/milestones/{milestone}/final/abort` takes
`{"revision":N}`. Only the signed-in owner can abort a pending Final. It releases
the intent and records `final_aborted`; it does not undo a remote cut. It needs no
board call, so a broken or revoked board connection cannot trap the owner.
Final validates a nonempty destination release before recording its intent.

Board observations never alter approved `items` or revisions. Membership drift
and newly discovered cards appear in slices and append-only events. Card identity
is the pair `(board_id, number)`. Deleted cards remain tombstones.

Migration 135 makes proposal, event, revision and snapshot rows authoritative;
the plan header no longer duplicates those arrays or Frozen cut evidence. Latest
board observations have their own rows; cut evidence belongs to the milestone row
and is loaded only for Final completion/retry or a frozen Slice. Lifecycle writes
load only the selected proposal and Final intent,
so agent history has no share of the person's plan capacity. Agent proposal
admission permits at most 20 pending proposals and 256 KiB per authenticated agent
per milestone (including project scope); rejection and acceptance release that admission capacity. Human
Apply and decisions are exempt. Rejected/decided proposals remain audit history.
Migration 137 adds aggregate admission for the durable workspace and milestone:
80 pending agent proposals / 1 MiB and 32,000 agent events / 4 MiB. Creating new
worker sessions does not reset these limits. Person lifecycle writes are exempt.

Migration 136 keys agent event budgets by authenticated actor. Agent events have
a separate per-actor, per-scope admission partition; board observations have their
own per-scope partition. Each is bounded by 9,000 entries and 1 MiB. Person lifecycle events do not spend
those budgets. The 16 MiB plan-header bound applies only to the person's plan.
Unchanged slices neither write nor take write locks. Changed observations are
batched, with one latest-evidence index; card comparisons do not serialize the
record. Budget skips and failed observation writes are logged and cannot make a
successful board read fail. Exhaustion refuses the agent atomically instead of
deleting audit evidence. Human writes skip admission locks. Final captures its
live slice without persisting observations. A shared per-pool semaphore admits
agent writes before acquiring connections, reserving at least half the pool for
person requests and other traffic; excess writes return 409 for retry. Pools with
only one connection refuse agent writes. Permits cover only database writes,
after authentication, board checks and complete body decoding. Body reads have a
five-second deadline and a 128 KiB limit; failures close the connection while
retaining its read deadline. Requests must contain exactly one JSON value.

Within each blackboard, automatic writes take an exclusive advisory try-lock;
person writes wait for the active writer. Automatic row locks are NOWAIT, and
full budgets are checked before the milestone lock. New automatic writers cannot
extend a waiting person's delay by overlapping milestone share lockers. The
tradeoff is one active automatic transaction per blackboard, independently of
the production connection-pool size. Refused agent commit assertions are recorded
as untrusted, bounded assertion events and logged with typed diagnostics; if
admission is exhausted, the persistence refusal is logged too.

Public record and timeline reads page history in SQL (1–100 rows), with a 512 KiB
JSON budget per history collection. Oversized legacy rows retain bounded identity
metadata and `metadata_omitted: true`; cursors still advance. Frozen cut payloads
are not loaded by these reads. Full Store.Load is reserved for trusted maintenance
and export callers. Per-change
strings are bounded in bytes (titles and destinations 200; reasons 1,000; IDs 100
or 200). A later complete observation may repair an incomplete UTC-day snapshot;
a complete snapshot remains immutable.

Agent reads omit oversized record metadata with `metadata_omitted: true` inside
`untrusted_data`, retaining the bounded rows and the normal pagination cursor.
The complete encoded response remains below 64 KiB, including its wrapper.

Automatic writers for the same blackboard first queue in process, before pool
admission; cancellation removes the waiting request. This applies backpressure
instead of letting fast SQL lock refusals spin through all available CPU. The
SQL try-lock remains authoritative across processes. Person writers bypass this
queue and acquire the exclusive database intent before locking the header.
Blackboard migrations are numbered 132–137 in order; 131 is reserved for
BLG-424's insights timing cache.

Automatic admission permits at most 32 waiting requests per blackboard and waits
at most 250 ms. Full or expired admission returns the existing conflict response.
Observations and refused-commit audits use try-only admission, skip an exhausted
pool, and bound the entire persistence attempt to 250 ms. Reads do not join the
automatic queue; failed observation persistence is logged and leaves a valid slice.
Only the internal refused-commit path can create `assertion` events, always with
`operation: plan_commit` and `accepted: false`; public event input rejects this kind.
Replaying an existing event ID still validates the original actor and contents
when admission is full. Only new event rows consume capacity.

Migration 138 rebuilds aggregate event budgets using the same workspace fallback
as access checks: explicit workspace, then the first link's workspace, then the
record ID for an unlinked record. Proposal admission uses that same key. Prior
migrations 132–137 retain their order and contents; migration 131 stays reserved.
