# Crons

A cron starts an agent session on a schedule, with nobody watching: "every weekday at 07:30, read
my notes and summarise what needs attention". It is the runner's scheduler plus the
[MCP connections](mcp-connections.md) that give the run something to act on.

Crons live in the runner UI under **Crons**. Every cron belongs to one account, and every route is
for a signed-in person only: an agent token cannot create, read or change one.

## How a cron works

A cron has a name, a schedule, a time zone, a prompt (up to 16 KiB), an optional model and effort,
a runtime, an optional daemon pin and the connections and tools it may use.

1. The runner's scheduler wakes every 30 seconds and finds crons that are due.
2. For each, it records the run, then starts an ordinary agent session with your prompt.
3. The session runs **one turn** and ends. It shows in the session list with a "cron" badge and
   streams like any other session, but only you can see it (see [below](#sessions-are-private)).
4. The run and its outcome (started, held, skipped, failed, ran late) are listed under the cron.
   Run history is kept for 90 days. **Run now** starts one immediately.

Only one scheduler acts at a time (it takes a database lock), and a slot is claimed exactly once, so
a restart between "due" and "started" neither loses nor duplicates a run.

## The target board

A cron may target one **board**, which is where an unattended agent leaves its work: cards it files,
updates and comments on. The run gets the built-in `board` connection (see
[mcp-connections.md](mcp-connections.md#the-built-in-board-connection)) for that board only, with a
fixed set of tools, and cards still pass the board's own admission gate. The cron stores only the
board's id, never a token.

The operator sets the board's address on the runner: `BLERG_RUNNER_BOARD_URL` (the board as the
runner reaches it; the MCP endpoint defaults to `<that>/mcp`) and, only if the MCP endpoint lives
elsewhere, `BLERG_RUNNER_BOARD_MCP_URL`. The desktop and Kubernetes installs set the first to the
in-network board. With neither set, crons have no board.

**The failure card.** When a run cannot start (a connection needs sign-in, the cron token expired or
was revoked, the cluster credential is missing) the run is recorded as failed and the runner posts
one card on the target board: "Cron `<name>` could not run: `<reason>`". It has a fixed identity per
cron, so a repeat updates the same card instead of adding another.

**When the board is unreachable.** The failure card is best effort and never part of the run: if the
board cannot be reached the card is simply not posted (it is logged) and nothing about the scheduler
changes. If the board is what the run needs, the run itself fails with that reason and is visible in
the cron's run list; no card is attempted then, since it would need the same board.

### Focus board template

**New board** offers a **Focus board** template: columns Inbox, Today, This week, Waiting on,
Someday, Proposed and Done, and fields for source (email, calendar, manual), due date and tracking.
The cron form can fill in a **starter prompt** for it. The starter files each item once (it keeps a
stable id per source item, so a rerun updates the card instead of duplicating it), leaves cards you
have moved where you put them, and uses propose mode for anything outward. To track something new,
edit the prompt.

### Propose mode for unattended runs

Give a cron read tools in **allow** mode and any tool that changes something outside Blerg, such as
sending a reply or creating a calendar entry, in **propose** mode. The run queues the action, links
it from a card (for example in Proposed), and you approve or reject it later; see
[proposals.md](proposals.md). Nothing outward happens without you.

## Schedule

A standard five-field expression (minute, hour, day of month, month, day of week) plus an IANA time
zone, with a picker for the common shapes. `@` shortcuts, `@every` and `TZ=`/`CRON_TZ=` prefixes
are rejected, as is a schedule that never occurs. The shortest allowed interval is 15 minutes,
checked over a full year of occurrences.

Daylight saving: a wall-clock time that does not exist on a spring-forward day runs once, at the
first valid time after it; on a fall-back day it runs once, at its first occurrence.

Editing the schedule or resuming a paused cron recomputes the next run from now, so it does not fire
immediately.

## When the machine was off

Blerg on a desktop only runs while the stack is up. Slots that pass while it is stopped are
collapsed: on the next start an overdue cron fires **once**, marked "ran late", not once per missed
slot.

Catch-up is deliberate: a desktop asleep over a weekend does not wake up to a pile of runs.

A run that cannot start yet (the cluster is at its session limit, or no daemon is connected) is
**held** and retried every tick. It waits up to the cron's grace period (one hour by default),
counted from the later of its slot and the runner's start-up, so a machine that was off for a day
still gets its catch-up run once a daemon reconnects. When the grace period ends the run is marked
skipped, with the reason. A slot that arrives while an earlier run of the same cron is still running
or held is skipped.

## What an unattended run can and cannot do

An unattended run reads untrusted text with nobody watching, so it is contained more tightly than an
interactive session (a session you launch yourself keeps its tools even with connections; see
[mcp-connections.md](mcp-connections.md#how-a-session-sees-a-connection-the-gateway)):

- **Where it runs.** A cluster pod or the Docker sandbox. Never the bare host.
- **Tools.** Only file tools (read, write, edit, search) plus the MCP tools you allowed for that
  cron. No shell, web access, sub-agents or other built-ins. The runner sets this list; a session
  cannot change it.
- **MCP.** Only the connections and tools chosen on the cron, each tool pinned to its definition
  (see [mcp-connections.md](mcp-connections.md)). There is no "enable everything" switch.
- **Credentials.** A cluster run uses your own stored engine credential and **never falls back to
  the shared operator credential**: if yours cannot be fetched (the cron token was revoked, the
  credential was removed), the run fails without starting a pod.
- **Limits.** One turn per run, a maximum run time (30 minutes by default; a run over it is
  stopped), a per-session tool call budget, at most 20 crons per account and 2 cron sessions at a
  time per account.
- **Proposals.** At most 50 pending per account; a queued call counts against the tool call budget.
- **Failures.** Three failed runs in a row pause the cron with the reason. Deleting or pausing a cron
  stops any session it is running and revokes its grants.

What it can do with the board: read and write cards on its one board, through the admission gate,
using a token that lasts minutes and is valid for that board alone. With a tool in propose mode it
can only queue an action for your approval.

What it can still do: if an allowed tool reads content an attacker controls (a hostile email, say),
the agent can be steered into misusing the other tools you allowed. Keep the allowed tools narrow,
prefer read-only ones, and do not give a run a tool whose write access you would not give the
sender of that content.

## Sessions are private

A session started by a cron, and any session with MCP connections, is visible **only to the account
that started it**, on every surface: the session list, live updates, transcripts, terminal output,
screenshots, events, push notifications and completion webhooks. Other people on the same
install, including administrators, do not see it, because its content can include the results of
your tools.

## Identity and token expiry

An unattended run has no logged-in person, so creating a cron mints a **cron token** for your
account. It is not a credential anyone can present: only its id and expiry are stored, and it only
authorises the runner to check that you have not revoked it and to fetch your connection
credentials for this cron's runs.

- Deleting or pausing a cron revokes its token.
- Revoking it in Settings, or "log out everywhere", makes the next check fail and the cron pauses
  with the reason "access revoked".
- A token lasts at most 365 days. The cron shows the expiry, and **Renew** issues a new one. An
  expired cron pauses.
- Cron tokens count toward the 50 live agent tokens per account.

## On a desktop

Read this before scheduling a sandbox cron on a desktop install. The sandbox does not use your
account's stored credential: it mounts the **host developer's own Claude login**, so a cron there
runs on that login and its usage. A sandbox cron can also run on **any connected daemon**, and every
account on the install shares the connected daemons. The supported desktop shape is one person per
install; you can pin a cron to a daemon. On Kubernetes a cron uses the credential of the person who
owns it.

## Limits at a glance

| Limit | Value |
|---|---|
| Crons per account | 20 |
| Concurrent cron sessions per account | 2 |
| Shortest interval | 15 minutes |
| Prompt | 16 KiB |
| Default grace for a held run | 1 hour |
| Default maximum run time | 30 minutes |
| Consecutive failures before pause | 3 |
| Cron token life | at most 365 days |
| Run history kept | 90 days |
| Pending proposals per account | 50 (expire after 7 days) |
