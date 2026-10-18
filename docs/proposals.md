# Proposals

A proposal is an action an agent wanted to take but that waits for your decision. It is how an
unattended agent can "reply to this mail" or "add this calendar entry" without being trusted to do
it on its own.

## How a write call becomes a proposal

When you give a session or a cron a tool from an [MCP connection](mcp-connections.md), you choose a
mode for each tool: **allow** (the agent calls it and it runs), **propose**, or off. Propose is
offered for any tool, and is the right mode for anything that changes something outside Blerg.

When an agent calls a propose-mode tool, the call is **not sent** to the server. Blerg freezes the
tool name and the exact arguments, queues them as a pending proposal, and tells the agent the action
was queued, with the proposal's id and, where the runner knows its own address, a link to it. In a
board workflow the agent typically puts that link in a card so you can find it.

## What you approve

The page shows the **frozen arguments**: that is what will run, not the agent's description of it.
The agent's own summary is shown beside it, labeled as the agent's words. Read the arguments. If
they do not match the summary, reject.

- **Approve** runs the stored call, exactly as frozen, with your credentials and no agent involved.
  Only you, the owner of the session, can approve. Approving twice runs it once.
- **Reject** discards it. Nothing is sent.
- Before it runs, Blerg checks that the connection still points at the same server and that the
  tool's definition has not changed since the agent's call. If either changed, the approval is
  refused with an explanation; ask the agent to try again.
- **Unknown.** If the call may have reached the server but the outcome is unclear (a timeout, or the
  runner restarted mid-approval), the proposal is marked **unknown** and is never retried
  automatically, because a retry could do the action twice. Check at the other end what happened,
  then record it as done or failed.
- A call that definitely failed is marked failed, with the reason.

## Filing a Board card from a session

A session can list the standalone Boards its owner can access, search for duplicates, and propose
an issue with `blerg-runner ticket boards`, `ticket search`, and `ticket file`. Claude and Codex
also receive the built-in `blerg_ticket_file` tool for these three operations, including sessions
whose tools are restricted. Listing and searching are reads; filing creates a pending proposal.
No card is written until the person approves it.

A Board filing proposal has an editable draft. On **Proposals**, the session owner can change its
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
configured, choose the target repositories explicitly. The originating session's repository is
never assumed to be a target repository.

A sensitive filing restricts the card's **body** to principals with `board.admin` access to its
target board. The card's title, restricted marker, source metadata and other ordinary fields stay
visible. This does not redact the original conversation or the private proposal: take care with
what was already shared there. See [Board's sensitive card details](../board/README.md#sensitive-card-details-and-origin).

A completed filing returns the card reference. An **unknown** result is not an instruction to file
again: Blerg never retries it automatically. Check the target board and resolve the existing
proposal before proposing another card.

## Plugin requests

`blerg-runner request-plugin <name> "<why>"` queues a private request for a plugin or its bundled
skills from an allowed marketplace. The built-in `blerg_request_plugin` tool (`{name, reason}`)
provides the same approval-only request without a shell, including restricted sessions. Neither
path runs an installer. The session owner sees the
plugin, marketplace and agent's reason beside the chat, in the session panel, and on this page.
Read the exact arguments and choose **This session only** or **Always on**, then confirm.
Ordinary MCP proposal arguments remain frozen; a plugin request's scope is a separate human
choice, checked again at approval. Requests cannot be edited or retargeted.

Session-only loading leaves account settings unchanged. A supported host Claude session waits
for foreground and owned background work to become idle, then resumes with the updated snapshot.
Other runtimes offer an explicit always-on change for new sessions. Approval is complete before
a queued live reload finishes; its application state stays **queued** until the daemon reports
success or failure. Rejection and expiry leave the session and account settings unchanged.
Unattended sessions wait for the person's decision in the same way.

## Expiry and limits

- A pending proposal **expires after 7 days**. Decided proposals are removed after 30 days.
- The frozen arguments may be at most 64 KiB; a larger call is refused and the agent is told why.
- At most 50 proposals may be pending per account at once; further calls are refused until you
  decide some. Queued calls also count against the session's tool call budget, so a looping agent
  cannot flood the queue.
- Proposals are private: only the account that started the session sees them.

## Tool changes

The same page lists **tool changes**: an MCP server changed a tool a session had pinned, or
offers a new one. The tool is off in that session until you decide. You see the tool's name, the
connection, the session, and the **exact old and new definition** (description and input
schema) as a diff; characters that would be invisible are shown as `⟨U+…⟩` and the card says so.
Accept re-pins the session in place (a changed tool keeps its mode; you may tighten it to
propose; a new tool defaults to propose). Keep off leaves it off, and later changes to that tool
in that session are listed without a badge. Changes expire after 7 days. See
[`mcp-connections.md`](mcp-connections.md#tools-are-off-by-default) for the per-connection
setting that accepts them automatically.

## Where to manage them

The runner UI has a **Proposals** page, with a count badge in the sidebar that counts proposals
and tool changes waiting for you. It lists pending
proposals first; open one to see its arguments and approve or reject it. A link an agent leaves in a
card opens that proposal directly.

See also [`crons.md`](crons.md) for using propose mode in unattended runs and
[`SECURITY.md`](../SECURITY.md) for what a hostile message can still do.
