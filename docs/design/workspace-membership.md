# Live repository membership (BLG-321)

Status: implemented for host and sandbox; cluster is split off (see "Where this
landed"). Related: BLG-318 and BLG-347.

## Existing boundary

[Workspace selection](../workspace-selection.md) authorizes membership once at
launch. `InsertSessionWorkspace` in `runner/internal/db/workspace.go` is
insert-only. `WorkspaceManifest` and `ValidateWorkspaceChild` in
`runner/internal/protocol/workspace.go` bind children to an authorized subset.
Runtime roots, shell bindings, result provenance and private recovery journals
also bind the manifest digest and launch generation. Updating only the member
array would leave these consumers disagreeing about authority.

BLG-321 requires host, sandbox and cluster support, explorer/search updates,
per-member turn-end delivery, shell integration and shared workspace membership.
BLG-347 additionally requires explicit adoption of a repository initialized in a
previously empty host folder. Discovery alone must never grant membership.

## Security requirements

The server authorizes every change against the live person, workspace ownership,
current grants, runtime identity and expected membership revision. An agent may
propose a change; it cannot approve its own expanded authority. An approval must
bind the exact proposed sources, paths, operation and expected revision. Changes
to that proposal require fresh authorization. No new push credentials are implied.

Keep launch manifests immutable. Introduce append-only membership revisions and
a server-owned current-revision pointer instead of changing launch history.
Member identity and historical results remain addressable after removal. Every
runtime binding, read, shell lease, child selection, sync and completion must
identify its membership revision as well as its existing runtime incarnation.
An old peer without explicit membership-change support refuses the operation.
Existing runtime support restrictions remain in effect.

## Proposed transaction

1. Resolve and pin the requested source using the existing owner-bound selection
   mechanism. Revalidate source/common-directory identity, protected paths,
   overlapping repositories, normalized path collisions, member limits, grants
   and aggregate storage budgets. Preserve existing member paths and IDs.
2. Under a server workspace lock, compare the expected revision and journal a
   unique operation ID, authorizing principal, approval reference, old and proposed
   digests, affected runtime incarnations and cleanup obligations. A retry with
   the same ID must match the exact original request.
3. Fence new turns, child launches, shells and lifecycle operations across the
   affected workspace. Cancel and join readers. Obtain affirmative containment
   evidence for every engine, shell and other registered writer through each
   runtime's lifecycle coordinator. A disconnected runtime, missing registration
   or failed proof blocks the change. A process exit or empty in-memory map is
   not sufficient evidence. Preserve the existing coordinator lock order.
4. Prepare additions privately with runtime-specific identity and allocation
   receipts. Verify removal settlement under the selected product policy. Host
   paths use pinned descriptors; sandbox allocations use their isolated storage;
   cluster allocations bind exact claims and object UIDs. Do not delete user
   source directories or Git branches as a side effect of membership removal.
5. Persist preparation receipts, then atomically publish the new authorized
   revision, member results and transaction outcome. Record History with actor
   and affected members. Runtime installation acknowledgments bind the operation,
   revision, root and incarnation. Distributed installation is not a database
   transaction: keep all writers fenced until the runtime has the committed
   revision and validates its shell and sync bindings. Its acknowledgment follows
   the reopening, because a sandbox reader can only be rebuilt in a running
   container and an acknowledgment ends the server's retries.
6. Resume admission only after those checks. Notify threads and refresh explorer
   and search from the new revision. Retire stale shell leases and cached reads;
   old revision requests fail explicitly. Deliver each member to its own existing
   per-session branch; never infer a default member for ambiguous Git operations.

Journal phases distinguish authorization, fencing, preparation, commit,
installation and cleanup. Before commit, rollback restores the old revision only
with verified allocation cleanup and old-binding restoration. After commit,
recovery completes installation of the committed revision; it must not silently
restore earlier membership. Lost acknowledgments reconcile exact receipts.
Unknown outcomes retain storage and keep affected writers fenced. Startup and
reconnect reconciliation precede any agent or shell admission. Cleanup retries
use recorded allocation identity, never a pathname alone.

## Empty-folder adoption

Adoption is an explicit authorized add against the existing host folder's pinned
root identity. Reopen and verify the root and newly initialized Git common
directory while writers are fenced; a replaced root, symlink escape, nested
conflict or linked-worktree alias refuses. A repository needs a commit before it
can supply an isolated host child. Never initialize, commit, move files or change
the current branch implicitly during adoption.

Persist a fresh membership revision and private selection snapshot, and install
all runtime, reader, shell, child-selection and result bindings through the same
transaction. Only then can a host child select the adopted member and allocate
its own worktree. Failed adoption leaves the original zero-member authority;
there is no fallback to running a child in the parent's directory.

## Owner rulings (2026-10-31)

1. **Removal and retention.** Refuse removal with unfinished or undelivered work
   and show why on the row. Keep branches. Deleting a checkout is a separate
   explicit action, not part of removal.
2. **Existing threads and workers.** Additions are available to any chat from then
   on. Running workers keep what they have. Removal waits until no worker uses
   the repository.
3. **Interruption.** Never interrupt. A change waits for a safe turn boundary and
   shows as pending. It stays pending while a runtime is disconnected. No mutation
   or resumption happens without writer proof.
4. **Last repository and anchors.** Sandbox and cluster refuse removing the last
   repository; a host workspace can go empty. If a chat is anchored in the
   repository, removal is refused until it is moved or closed.

5. **Beside a root member.** A workspace with a member at the root (path `.`) can
   add a repository. The first keeps `.`, so existing paths never change. The new
   one goes in a reserved folder, `.workspace/<name>/`, on host and sandbox. The
   folder is listed in the first repository's `info/exclude`, so its Git never sees
   it and nothing is committed. The location shows only in a shell.

Surface: the Repositories tab in the dock. Main can request a change, which the
person approves. Slices: read-only tab, then Add, then Remove.

## Where this landed

The server journals an operation in `workspace_membership_operations`
(`requested`, `authorized`, `committed`, `installed`, `denied`, `refused`) and
publishes revisions to the append-only `session_workspace_revisions`.
`session_workspaces.manifest` stays the launch selection. The current-revision
pointer is `membership_revision`; `GetSessionWorkspace` reads through it. The
runtime journals `fencing`, `prepared` and `committed` in its signed workspace
record (`runner/internal/daemon/workspacemembership.go`).

A revision keeps the launch and runtime generation. It changes the manifest digest,
so the commit updates `runtime_binding` in the same transaction. Generations were
not advanced because a new generation retires every shell lease, which ruling 3
forbids; instead the change waits until no shell exists, and that absence is the
shell half of the writer proof.

- **Boundary.** The server dispatches only at `idle`, on every turn end, on request
  and from a 15 second sweep. The daemon checks again, then proves no shell exists
  before and after freezing. A failed proof reopens the session and stays pending.
- **Host.** Nothing is allocated. The source is re-pinned while frozen; the reader
  is rebuilt and the binding moved before anything thaws. A resume reads the
  revision from the server and its sources from the signed journal.
- **Sandbox.** Live additions use isolated staging while frozen, bounded to 55
  seconds because the shell freeze has a deadline. A source-free worker creates
  a token-scoped directory in `staging`, beside the allocation's public `tree`.
  Both share the enforced quota filesystem; no sandbox mount exposes staging.
  The preparation container mounts only that private directory and the read-only
  source. After its removal is proved, a second container with no source mounts
  checks the receipt, plain Git metadata, absence of config includes and active
  hooks, and `git fsck --full --strict`, then atomically moves the clone into
  `tree`. The shared mount rule covers both peers and their cleanup helpers.
  Aggregate Git limits are checked against all existing clones before success;
  failure removes only the new allocation. Private staging is cleaned on return
  and after interrupted-worker recovery. Unknown worker outcomes retain the
  journal and keep writers fenced. A running container can't gain
  the read-only object mount a launch clone borrows from, so a member added live
  holds its own objects (`own_objects`), which count against the quota. A path
  already present is refused, never replaced. Rollback removes the clone only if
  its identity matches the receipt. Docker runs no exec in a paused container, so
  the reader helper is replaced after the thaw, with readers still suspended.
  Removal ends membership only: the clone stays in the allocation.
- **Reserved folder (ruling 5).** Manifests allow `.` beside `.workspace/<name>`
  and nothing else; the name is the usual projected name or stable ID, with a
  suffix on a collision. On a host the path is a link to the checkout, since a host
  member is the real checkout: its reader is pinned to the source, not the link. A
  sandbox holds a clone there. Readers show the member by name and hide the folder.
  Workers get real worktrees nested the same way, in their own allocation.

  The entry goes in the common directory's `info/exclude`, so a linked worktree is
  covered. It carries a marker and one line per folder relying on it: linked
  worktrees share the file, so the entry goes only with the last of them. A person's
  own entry is left alone. Anything in the folder that isn't ours, a link at the
  folder or a name already taken refuses the change.

  The clone's `info/exclude` is the agent's file, so sandbox settlement doesn't
  trust it. Settlement excludes the exact paths from the signed journal, or the
  clone would land in the first repository as a gitlink; other files in the folder
  are still saved as work. After Remove, a path stays in `reserved_retired` until it
  is gone. Remove deletes a sandbox clone here, unlike a named member, because a
  clone left behind would keep the folder and its Git entry alive. On a host only
  the link goes.

  Directory identity proves which allocation is deleted, not that its contents were
  delivered, so the proof covers more than Git status. Commits, refs, tracked and
  untracked files must be delivered; index flags (assume-unchanged, skip-worktree)
  and populated submodules refuse as unfinished work. Files Git ignores refuse with
  `local`, and the row names them. **Discard files…** then confirms: it is a new
  removal, from the person only, carrying a digest of those names, sizes and times.
  Any change to the files refuses again. Every deletion repeats the proof, because
  the agent can reach a retired clone until it is gone; a clone that fails keeps its
  record and exclusion, and a later change retries. A startup settle has no storage
  worker, so a sandbox clone waits for the next change. A
  crash between the copy and the journal leaves a clone with no recorded identity;
  it stays excluded and is never removed on a guess, so re-adding that name refuses.
- **Recovery.** A startup or reconnect asks the server for the operation's phase
  before anything is admitted. Committed moves forward; uncommitted clears the
  journal and the server retries. No answer retains the journal. Writers stay
  frozen on an unknown commit outcome until a repeated apply or reconnect settles it.
  A committed change keeps its journal, and the server's sweep keeps applying it,
  until the writers run and the reader is rebuilt. The journal records the thaw so a
  retry skips it; a failure shows as waiting on the pending change. A restart follows
  the same rule: startup moves the commit forward and keeps the journal, recovery
  reopens the runtime, and the repeated apply validates the reader and acknowledges.
  Reserved cleanup runs only while fenced, so after a thaw it waits for the next
  change. A session that ends first closes the change on the server.
- **Old peers.** `WorkspaceCapabilities.Membership` is negotiated at the runtime hop.
  A peer without it gets a plain refusal or a pending state; nothing is attempted.

Limits:

- A physical folder workspace with named members accepts only repositories inside
  that folder. Empty folders adopt into `.`.
- Add takes one repository at a time.
- **Cluster is not implemented.** The runtime plan and its per-member credentials
  are fixed in the pod's Secret at start, and `Prepare` refuses a missing directory
  on resume. A live add needs either a new credential path to a running pod or a
  restart-based install. Neither is rushed here; the route refuses with a plain
  message. See BLG-321 for the follow-up.

## Test-first implementation and acceptance

Tests cover revision CAS,
exact approvals, unauthorized callers, idempotent/conflicting retries, immutable
history and results, stale readers/shells/children and old-peer refusal. Inject
crashes and lost acknowledgments at each journal phase. Test disconnected
participants, unproven writers, source replacement, path collisions, quota
failure, dirty removal, delivery failure and claim cleanup identity.

Exercise add/remove on host, sandbox and cluster, including explorer/search,
shells, per-member turn-end delivery and all thread participants. Regress
zero-member folder -> initialize and commit -> authorized adoption -> isolated
host child, including restart and failed-adoption cases. UI tests cover owner
approval, refusal and pending/recovery states at 390 and 3840 pixels.

Run the touched Go package suites with the database configured, including race
detection; frontend unit and browser suites for UI changes; builds, pinned lint,
privacy scrub and documentation link checks. Report exact commands, counts,
skips and failures. Real-path coverage is `TestMembership*RealWire`: the HTTP
routes, a real server over a websocket and the shipped daemon binary.

Privileged host shells do not provide membership freeze proof: their turn-end
sync leaves writers running. The membership fence therefore waits with the
existing shell-open pending reason until those shells close, even if a turn-end
copy could proceed with an in-flight-write note.

### Attention for Main's requests (BLG-440)

A requested change is one journal operation, presented to its owner in the chat,
Needs you, Questions, and the Repositories dock. The question surfaces reuse the
plain question row and dialog; chat and the dock offer Approve and Decline directly.
Every decision binds the operation's proposal digest and uses the same server
compare-and-set. A full session snapshot clears resolved requests in other browsers.
The owner-only session inventory and live count read the journal, so a reload or
runtime restart does not lose the request. A newly created request sends the usual
question attention push; retries of the same operation do not send another.

Attention audit: engine command approvals already use owner-only ask events;
permission proposals use proposal cards and Needs you; changed MCP tools have a
Needs you link to their canonical Proposals page; file reviews and session-start
requests already have chat cards and Needs you rows. No separate approval records
are introduced for those flows. Review and answer-page counts now also participate
in the Live tab and panel-toggle counts, matching their existing rows.
