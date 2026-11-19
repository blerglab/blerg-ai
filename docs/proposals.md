# Proposals

A proposal is an action an agent wanted to take but that waits for your decision. It is how an
unattended agent can "reply to this mail" or "add this calendar entry" without being trusted to do
it on its own.

## How a write call becomes a proposal

When you give a workspace or a cron a tool from an [MCP connection](mcp-connections.md), you choose a
mode for each tool: **allow** (the agent calls it and it runs), **propose**, or off. Propose is
offered for any tool, and is the right mode for anything that changes something outside Blerg.

When an agent calls a propose-mode tool, the call is **not sent** to the server. Blerg freezes the
tool name and the exact arguments, queues them as a pending proposal, and tells the agent the action
was queued, with the proposal's id and, where the runner knows its own address, a link to it. In a
board workflow the agent typically puts that link in a card so you can find it.

## What you approve

Needs you, the notification bell, and the Proposals page show a readable preview of the stored
request. Its first line is what approving runs: the tool and its connection, as the runner recorded
them. A title the agent wrote into the request is shown below it, labeled as the agent's, so it
cannot pose as a description of the action. A Board filing shows its title, type, repositories, and
complete rendered body.
**Show exact request** opens the full **frozen arguments**: that is what will run, not the agent's description of it.
The agent's own summary is shown beside it, labeled as the agent's words. Read the arguments. If
they do not match the summary, reject.

- **Approve** runs the stored call, exactly as frozen, with your credentials and no agent involved.
  Only you, the owner of the workspace, can approve. Approving twice runs it once.
- **Reject** discards it. Nothing is sent.
- Before it runs, Blerg checks that the connection still points at the same server and that the
  tool's definition has not changed since the agent's call. If either changed, the approval is
  refused with an explanation; ask the agent to try again.
- **Unknown.** If the call may have reached the server but the outcome is unclear (a timeout, or the
  runner restarted mid-approval), the proposal is marked **unknown** and is never retried
  automatically, because a retry could do the action twice. Check at the other end what happened,
  then record it as done or failed.
- A call that definitely failed is marked failed, with the reason.

## Filing a Board card from a workspace

A workspace can list the standalone Boards its owner can access, search for duplicates, and propose
an issue with `blerg-runner ticket boards`, `ticket search`, and `ticket file`. Claude and Codex
also receive the built-in `blerg_ticket_file` tool for these three operations, including workspaces
whose tools are restricted. Listing and searching are reads; filing creates a pending proposal.
No card is written until the person approves it.

A Board filing proposal has an editable draft. On **Proposals**, the workspace owner can change its
board, title, body, type, sensitive flag, and target repositories before approving. Discovery must
verify the selected board before an edited draft can be saved; an unavailable board stays visible
instead of being silently replaced. Source session, repository, commit and engine are recorded by
Blerg and cannot be edited as draft fields. A repository with no verifiable HEAD has no commit.

Saving and approving both compare the exact stored draft with the version being reviewed. If
another edit wins, the stale operation is refused: load and review the current draft and confirm
again. Retargeting a draft does not preserve authority to its former board. Approval checks the
approving person's current access to the chosen board and writes only that exact draft. Ordinary
MCP proposals keep their frozen arguments; this editor applies only to built-in Board filings.
Rejection is always available and writes nothing.

Target repositories are separate from the source repository. Use repeated `--repo` values, or edit
the comma-separated list in the draft, when a target needs several repositories. On a board that
requires a repository, its sole configured repository can be the default; when several are
configured, choose the target repositories explicitly. The originating workspace's repository is
never assumed to be a target repository.

A sensitive filing restricts the card's **body** to principals with `board.admin` access to its
target board. The card's title, restricted marker, source metadata and other ordinary fields stay
visible. This does not redact the original conversation or the private proposal: take care with
what was already shared there. See [Board's sensitive card details](../board/README.md#sensitive-card-details-and-origin).

A completed filing returns the card reference. An **unknown** result is not an instruction to file
again: Blerg never retries it automatically. Check the target board and resolve the existing
proposal before proposing another card.

## Proposing a new board

`blerg-runner board create "<name>" [--repo R ...] [--template T | --columns A,B,C]
[--description D] [--access read|file|full]` proposes a new Board for the person to approve. The
preview shows the board's name, description, repositories, columns or template, and the access
the workspace asks for on it (Full by default). The proposal cannot be edited; on **Proposals** and in
Needs you, the person may lower the access before approving, or reject it.

- **Approve** creates the board with the approving person's authority: Core issues a one-use
  credential bound to the exact approved draft, only to an administrator who is signed in, and Board
  consumes it once. A workspace never holds board administration. Approving twice creates one board.
- The proposing workspace, and no other, is then given access to the new board at the approved level,
  as if the person had set it in the workspace's Permissions. A workspace another workspace started is
  capped by that workspace's level, which is Propose on a board it did not create.
- **Reject**, or expiry, creates nothing. "Allow for this workspace" never applies.
- The same command repeated with its idempotency key (or the same draft without one, within 15
  minutes) is the same proposal.

## Proposing board metadata changes

`blerg-runner board update <board id or name> [--key KEY] [--name NAME]
[--description DESCRIPTION] [--patch JSON]` queues a frozen request in the existing proposal flow.
The built-in `blerg_board` tool also accepts `tool: "propose_metadata"`, a `board`,
and `arguments` containing the metadata patch below. The workspace needs Read access or higher. Every request through this command requires the workspace owner's
approval, including at Full access; **Allow for this workspace** does not apply.
The preview names **Board metadata → update_board_metadata**; **Show exact request**
shows the target board ID and every field that will change. Omitted fields stay as
they are; `--description ""` clears the description.

Approval requires a live administrator sign-in and rechecks the session's access
and the Board destination. Core signs a short-lived credential bound to that board
and exact patch; Board consumes it atomically with the change. The session never
receives administration authority. Rejection and expiry write nothing. Repeating
the same request within 15 minutes returns the same proposal. An unknown outcome
is never retried automatically; check Board first.

Keys follow Board's existing rules: 2–5 uppercase letters or digits, starting with
a letter. Old keys remain reserved aliases for the same board. This does not rewrite
historical text, commits, or saved chips.

Use `--patch` for configuration fields; it takes a JSON object (the built-in tool
takes the same object as `arguments`). Every field, including every operation in
a list, is frozen in the proposal. All changes commit together, or none do.

- `repos: ["team/app", "web"]` replaces the ordered repository list; `[]` clears it.
  Existing cards keep their repositories.
- `require_repo: false` changes whether new card writes need a repository.
- `deploy_url: "https://example.com"` sets the deployment link; `""` clears it.
- `include_component_version: false` changes whether filing instructions ask agents
  to collect the installed component versions.
- `field_schema: [{ "key": "area", "type": "text" }]` replaces the declared custom
  fields using Board's existing schema rules; `[]` clears them. Existing card values
  are preserved; future edits use the new schema.
- `columns: [{ "name": "Review", "is_terminal": false }]` adds a column.
  Include `id` to rename an existing column or change its terminal flag.
  `before_id` places it before another column on the same board; `""` moves it
  to the end. Omit `before_id` to preserve position (new columns append).
  `{ "id": "<column UUID>", "delete": true }` removes an empty column.
  Move its cards separately first; deletion refuses while any cards,
  including archived cards, still refer to the column. Column changes retain an audit.
- `tags: [{ "from": "old", "to": "new" }]` renames a tag on matching cards
  of this board, including archived cards. An existing destination tag is merged.
  `{ "from": "old", "delete": true }` removes it. Operations run in list order
  against the cards present at approval time, increment card versions, and record
  card events. Tags have no separate catalog: add a new tag by updating a card.

For example: `blerg-runner board update Product --patch
'{"require_repo":false,"columns":[{"name":"Review"}]}'`.

Execution and automation settings, admission policies, external lifecycle ownership,
board deletion, and moving cards as part of column deletion are outside this command.
The existing Full-access column commands still use that grant directly; this
proposal path always asks. Whether those existing commands should also require
approval is a separate product decision.

These policies need separate product decisions as Board becomes plain state; a metadata
approval grants no execution authority.

## Plugin requests

`blerg-runner request-plugin <name> "<why>"` queues a private request for a plugin or its bundled
skills from an allowed marketplace. The built-in `blerg_request_plugin` tool (`{name, reason}`)
provides the same approval-only request without a shell, including restricted workspaces. Neither
path runs an installer. The workspace owner sees the
plugin, marketplace and agent's reason beside the chat, in the workspace panel, and on this page.
Read the exact arguments and choose **This workspace only** or **Always on**, then confirm.
Ordinary MCP proposal arguments remain frozen; a plugin request's scope is a separate human
choice, checked again at approval. Requests cannot be edited or retargeted.

Session-only loading leaves account settings unchanged. A supported host Claude session waits
for foreground and owned background work to become idle, then resumes with the updated snapshot.
Other runtimes offer an explicit always-on change for new sessions. Approval is complete before
a queued live reload finishes; its application state stays **queued** until the daemon reports
success or failure. Rejection and expiry leave the session and account settings unchanged.
Unattended sessions wait for the person's decision in the same way.

## Expiry and limits

- A pending proposal **expires after 7 days**. During its last 24 hours, Needs you and the
  notification bell warn that it expires soon. **Extend 7 days** adds seven days to the reviewed
  deadline without changing the request or its creation time; the extension records who made it, and
the Proposals page lists it. An extended proposal can be approved, and a Board filing edited, until
its new deadline. A proposal stays open **at most 30 days** from when the agent proposed it: the
last extension stops there, after which **Extend** is unavailable ("Proposals can stay open up to 30
days") and the proposal expires at its deadline. The warning is in the app only (Needs you and the
bell), never a push notification: workspaces are short-lived, and a proposal open that long means
something is wrong.
  A decided or expired proposal cannot be extended. Decided proposals are removed after 30 days.
- The frozen arguments may be at most 64 KiB; a larger call is refused and the agent is told why.
- At most 50 proposals may be pending per account at once; further calls are refused until you
  decide some. Queued calls also count against the workspace's tool call budget, so a looping agent
  cannot flood the queue.
- Proposals are private: only the account that started the workspace sees them.

## Tool changes

The same page lists **tool changes**: an MCP server changed a tool a workspace had pinned, or
offers a new one. The tool is off in that workspace until you decide. You see the tool's name, the
connection, the workspace, and the **exact old and new definition** (description and input
schema) as a diff; characters that would be invisible are shown as `⟨U+…⟩` and the card says so.
Accept re-pins the workspace in place (a changed tool keeps its mode; you may tighten it to
propose; a new tool defaults to propose). Keep off leaves it off, and later changes to that tool
in that workspace are listed without a badge. Changes expire after 7 days. See
[`mcp-connections.md`](mcp-connections.md#tools-are-off-by-default) for the per-connection
setting that accepts them automatically.

## In the chat

A proposal from a workspace also shows in that workspace's chat, as a card like the one a workspace's
request to start another workspace gets: what the workspace wants (file a card on a board, or run an
MCP tool), the board or connection, and the title and a short summary. Only you, the workspace's
owner, can read the proposal and decide; anyone else who can see the workspace sees only that it waits
for you. Once a card is created, though, everyone who can see the workspace sees its compact ticket
(board, number, title, type, priority and column, never the body) in the chat and in History. A
private workspace is visible to you alone. The card offers:

- **Approve**: the same approval as on the Proposals page, of exactly the draft or arguments shown.
- **Edit** (board cards): the Proposals page's draft editor in a sheet over the workspace. Save the
  draft, review it, then approve it from the sheet.
- **Reject**: nothing is written or run.
- **Allow for this workspace** (card filings and MCP calls only): approves this one and lets this workspace's later proposals of exactly
  the same scope be approved without asking: for a board card, cards on that one board; for an MCP
  call, that one tool of that one connection. Another board or another tool still asks.

Once decided, the card says how: approved, rejected, failed, unknown, or approved by a workspace
permission. A card that was created follows as a compact ticket (board, number, title, type and
priority, column) with **Open**, which opens it on its Board page. The card also appears as a chip
in the workspace panel's History tab, in the topic that was open when it was filed. Approving or
rejecting in the chat or on the Proposals page is the same decision: whichever comes first wins and
the other shows it.

## Workspace permissions

A workspace permission is what **Allow for this workspace** creates; you can also give one in the
workspace panel (**Live → Permissions → Manage**) or before the workspace starts (the launch sheet's
**Permissions…**). It is narrow and short-lived on purpose:

- One scope: one board, or one connection's one tool, in one workspace. It is never broader than the
  proposal or toggle that created it. A board's permission is its access level (0.4.4): filing
  without asking is level **File without asking**, and **Full** includes it; **Allow for this
  workspace** on a filing raises the board to that level and never lowers a Full board. See
  [Board access](talking-to-an-agent.md#board-access-from-the-permissions-dialog).
- Only you, signed in, can create or revoke it; the agent cannot, and neither can a token.
- It ends with the workspace, and discarding a Main ends its children's permissions too.
  **Revoke** (the × on its chip under Live → Permissions, or Revoke in the Permissions dialog) stops it at once, also for a proposal already queued.
- An automatic approval is your approval, made with the sign-in you gave the permission from: every
  check above still runs (your access to the board, the connection's address and the tool's
  definition), and a refusal leaves the proposal pending for you. Signing out stops automatic
  approvals for permissions given from that sign-in. Each automatic approval records the permission
  that allowed it, and the Proposals page says "Approved by a workspace permission".
- Cron workspaces, restricted workspaces, board workspaces and workspaces an agent token started cannot hold
  one: their proposals always wait for you. Plugin requests are never allowed for a workspace.
- A permission for an MCP tool holds the tool's definition it was given on. If the server changes
  the tool (even when the workspace accepts the change), the permission stops approving and the panel
  says "tool changed": allow it again to approve the new version.
- One permission approves at most 20 proposals an hour (`BLERG_RUNNER_GRANT_HOURLY_CAP` changes
  the number). Past that, proposals wait for you, and the card and the agent say why.
- The panel lists a permission whose sign-in has ended under **Not approving now** ("Needs you signed
  in"); allow it again to renew it from your current sign-in.
- If you choose **Allow for this workspace** and the approval itself is then refused, the permission
  still stands; the card says so and links to where you revoke it.

The same draft filed again from the same workspace within 15 minutes is the same filing: the agent
gets the earlier proposal and where it stands (pending, filed, or not known yet), never a second
card.

When a permission approves an agent's filing at once, `blerg-runner ticket file` prints the card it
filed instead of "Ticket proposed", and an MCP call returns the tool's result.

## The agent hears the outcome

When a proposal from a workspace is decided (by you, by a workspace permission, or when an approval ends
failed or not known), the workspace's agent gets a message that starts "Proposal <id>": approved, with
the card it created (`board "Launch Night" #6 "Live check" created`); rejected, with your note when you gave
one; failed; or could not be confirmed (do not file it again). The agent can check at any time with
`blerg-runner ticket status <id>` (exit 0 done, 3 still waiting, 4 not done, 5 not known yet), which
reads only its own workspace's proposals. An MCP tool's result is not put in the message; the status
shows it.

## Where to manage them

Every pending proposal has its own card in the originating workspace's **Needs you** and in the
notification bell, including requests from earlier turns. The list refreshes every 30 seconds,
when you return to the tab, and after a decision or extension. These notifications are in the app;
they do not depend on enabling operating-system notifications. A deadline warning also appears
as an in-app notification while Runner is open.

The **Proposals** page opens on decision history and audit: the stored request, outcome, who
decided it ("you": a proposal is only ever its owner's), the exact workspace permission used for an
automatic approval (its board or tool, when and where you gave it, and a later revocation), and
every extension of its deadline. Its Pending tab remains available, and a proposal link opens that request directly.
Only the owning account can read or decide a proposal.

See also [`crons.md`](crons.md) for using propose mode in unattended runs and
[`SECURITY.md`](../SECURITY.md) for what a hostile message can still do.
