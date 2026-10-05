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
The agent's own summary is shown beside it, labelled as the agent's words. Read the arguments. If
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

## Expiry and limits

- A pending proposal **expires after 7 days**. Decided proposals are removed after 30 days.
- The frozen arguments may be at most 64 KiB; a larger call is refused and the agent is told why.
- At most 50 proposals may be pending per account at once; further calls are refused until you
  decide some. Queued calls also count against the session's tool call budget, so a looping agent
  cannot flood the queue.
- Proposals are private: only the account that started the session sees them.

## Where to manage them

The runner UI has a **Proposals** page, with a count badge in the sidebar. It lists pending
proposals first; open one to see its arguments and approve or reject it. A link an agent leaves in a
card opens that proposal directly.

See also [`crons.md`](crons.md) for using propose mode in unattended runs and
[`SECURITY.md`](../SECURITY.md) for what a hostile message can still do.
