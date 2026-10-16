# Talking to a running agent

How messages, interrupts and pausing work in the chat view of a Claude Code session. (The
engine-specific notes are at the end.)

## Interactive and unattended

Every agent session is started in one of two modes, and the agent is told which:

- **Interactive**: you are reading the chat as it happens. The agent talks a plan over before it
  acts on anything large, says what it is doing between tool calls, and when it needs a decision it
  asks and waits for your answer.
- **Unattended**: nobody is watching. The agent makes the call itself, notes the assumptions it
  made, carries the work through to the end, and finishes with a summary of what it did. It does
  not stop to wait for an answer in the chat.

Who gets which, unless the start says otherwise:

| Started by | Mode |
| --- | --- |
| You, from the app's launch sheet | interactive |
| A tool, through the agent contract or MCP | unattended, unless it asks for interactive |
| A cron | always unattended |
| A board card's **Run**, and the board's reviews | unattended |
| A board card's **Discuss**, and the board session | interactive |

To choose it yourself: tick **Unattended** on the launch sheet (it is offered for an agent session,
not a terminal one), or send `"interaction": "interactive"` or `"interaction": "unattended"` in the
body of `POST /api/runner/start`.

The mode is fixed for the session's life: it cannot be changed after the start. A session started
unattended says **Unattended** under its title on the session page. You can still write to one;
the message is delivered as usual.

## When the app calls you

The runner tells you when a session needs you: a toast in the runner page you have open and, when
you have not used it for a few minutes, a notification on your phone or desktop.

- **Waiting for your input**: the agent stopped mid-turn for you.
- **Waiting for you**: the agent's work is done. A turn can end while its background subagents or
  background tasks carry on; then you hear nothing until the last of them finishes, and then once.
  A background task that may never finish by itself (a dev server, a log watch) holds it for 30
  minutes at most; then you are told the agent is waiting for you while a task still runs.
- **A question** the agent asked with `blerg-runner ask`.

Sessions nobody watches never call you: an unattended one, a cron's, and one a tool started
without asking for you in the chat. Their questions still show on the Messages page. A board
card's **Discuss**, the board session and a board's **Assist** have you in them and do call you.

## Sending a message while the agent works

Press Enter and the message goes straight to the agent; you do not wait for the turn to finish. The
agent picks it up **at its next step**: when the tool call it is running finishes, the message is
delivered and the agent adjusts without losing the turn. Until then it shows as a **Queued** bubble in
the chat, and becomes a normal message of yours once the agent has taken it.

The server saves accepted chat messages before sending them to the daemon. Their queued bubbles
survive switching sessions and reloading the page. If the daemon restarts before taking a message,
the server delivers it again when an updated daemon returns; retained client-id receipts
prevent replay of known-consumed messages. Older daemons receive the original send but are
not sent automatic retries: upgrade the daemon for reconnect recovery. A session may have
64 pending messages, up to 4 MiB together; each serialized message is limited to 256 KiB.
A session that ends reports remaining queued messages as not delivered.
A queued message stays visible until the agent records it or reports that it could not take it.

Two things to know:

- A message waits for the tool call in progress. If the agent is in a ten-minute build, your message
  is delivered when that step ends. To cut a running step, interrupt it (below).
- A message sent while the agent is writing a long answer (no tool call involved) is taken after that
  answer, as a new turn.

Several messages sent in a row are delivered together at the next step.

## The working line

While the agent works, a line above the message box says what it is on, with the time the turn has
run. Once a tool call has run two seconds it adds "· waiting on Bash · Run the unit tests 4:12": the
tool, what the call says it does (a Bash call's description, else the start of its command; an
Agent's or a Monitor's description; nothing more for other tools) and how long it has run. The
suffix changes at most every three seconds, so a run of short calls does not make it flicker. With
no call in flight it says "Thinking…" while the model thinks (Claude Code and the native engine say
when a thinking block opens and closes; the thinking itself still arrives as one card when it is
over), and "Retrying the model (attempt 2)…" while the engine retries a failed API call, until the
model answers; the muted retry line stays in the transcript. Otherwise it says "Working…", or names
the background tasks running. "Thinking…" is known live only: a page opened mid-think says
"Working…" until the next step. Both need a daemon from this release (reinstall the desktop daemon)
and, for Claude Code, a CLI that takes `--include-partial-messages`.

## Pointing at what you mean

When the feedback is about a file the agent published, open the file and use **Review** (a
markdown or PDF file: select a passage, ask for a change, edit the markdown yourself) or **Mark
up** (an image: draw on it, drop numbered pins with notes). Both send one message the agent can
act on line by line. See [Review and mark-up](artifacts.md#review-and-mark-up).

## When the agent asks, updates or leaves a note

An agent reaches you with `blerg-runner ask`, `blerg-runner update` and `blerg-runner note`, and
they now show in the session's chat as they are sent:

- An **ask** is a card with the question and its own reply box. While it is open, the message box
  under the chat says that what you send answers it. You can answer in the card or simply type in
  the message box and press Enter: that message goes to the agent as the answer, not as a new
  instruction, and it appears in the chat as your answer to that question. The agent is waiting
  inside the ask, so it could not have read an unrelated message anyway; send that one after
  answering. The card shows that the question was answered, or closed if it ran out of time. It
  stays open for as long as the question is open, even when the agent's `ask` command was cut off
  or ran in the background: a message you send then still answers it. A daemon restart is the
  exception: the `ask` that waited for your answer is gone. A message you send while the daemon
  is away is kept for it and reaches the agent when it is back; otherwise the question is closed,
  the session is paused by the restart (below), and your next message goes to the agent, which
  is told its question was cut off.
- An **update** and a **note** are system cards. You can reply to a note; the agent reads the reply
  with a reference to the note it answers.

The board's own session chat has no reply box on an ask card: type the answer in its message box,
which answers the open question the same way. So does a message sent through the runner's API
(`POST /api/runner/sessions/{id}/message`, or the MCP `send_message` tool) by a person or an API
caller; a board's own automation never answers a question.

Every `blerg-runner` command authenticates with the session's own token, which the daemon gives
the session when it starts (`BLERG_RUNNER_SESSION_TOKEN`). If the server finds a session running
without a working one (a start that raced a daemon heartbeat or reconnect could leave it revoked),
it mints a new one and the daemon writes it to
`$BLERG_RUNNER_DAEMON_STATE_DIR/session-tokens/<session id>` (by default under
`~/.blerg-runner-daemon`; in a sandbox, under the container's own home), private to the daemon's
user and removed when the session is killed (an agent session that only finishes keeps it, since it
can be resumed). The CLI reads that file before the variable, so the session
recovers without a restart, at the latest at the daemon's next heartbeat. Until then, a refused
token reads "token revoked or expired — restart the session or wait for a refreshed token".

## What the chat shows as what it is

Text you did not type is not drawn as yours, and a failure is not drawn as the model speaking:

- The note added when a session comes back after a restart ("Session resumed after a restart") is a
  system note.
- The output of a slash command such as `/cost` or `/context` is a system card, in a fixed-width font.
- When the model's API fails, the chat shows an **API error** row with the reason, not an agent
  message. When the agent retries ("retrying the API (attempt 2 of 10) after overloaded_error"), a
  muted line says so and the working indicator keeps running; only a failed turn ends it. A turn that
  fails reports the reason the engine gave, and the session's result reports no answer for it.
- A Claude Code compaction reads "Conversation compacted (manual, 151k tokens before)".
- A slash skill you typed (`/review x`) is recorded as the text you typed, not as the skill's long
  instructions.
- A message you sent that the session dropped (its queue was full) says it was not delivered, and
  its "Queued" bubble goes away; a delivered one never shows twice.

These come from the daemon, so on a machine still running an older daemon you keep the old
behaviour until you reinstall it: the restart note and a command's output are missing or drawn as
yours, an API failure reads as the agent's message, retries and compactions show as before, and a
slash skill shows its instructions. Asks, updates and notes from `blerg-runner`, and an answer
typed in the chat, work with any daemon.

If the browser falls behind a very busy session and the server has to drop live events for it, the
chat is told and fetches them again; you may see the transcript fill in a moment later, not lose a
line.

## Interrupting

**Esc**, or the Stop button, cancels what the agent is doing right now. The conversation and the
session stay as they are, and any message you had already queued runs next, as its own turn. It does
not restart the agent: its context is kept.

## Pausing a cluster session

A cluster session costs a pod while it runs. **Pause** (next to Kill in the session header, for a live
cluster session) frees the pod and keeps the session: its conversation is kept and it shows as having no
pod. Send it a message and a new pod resumes it, exactly as after an eviction. A paused session waits 24
hours for a message and is then ended ("not resumed"). Whatever the agent was doing when you paused is
cut off.

**Kill** ends a session for good: an ended session cannot be resumed, the chat says so, and the message
box is locked.

## Long conversations

Opening a long session shows the most recent part of the conversation at once and loads the earlier
messages in the background; a "Loading earlier messages" line is at the top while that runs, and your
place is kept as they arrive.

The chat follows new output while you are at its end. Scroll up, even a little within the newest
card, and it stops following, so what you are reading stays put; a small "↓ New output" pill at the
bottom of the conversation then says more has arrived below, and tapping it takes you to the end.
Following starts again when you reach the end or send a message.

## Thinking and subagents

When the model thinks before it answers, the transcript keeps that thinking as an ordinary message
card in the same plain text, captioned "thinking" where a message shows its time. Untick "Show
thinking" in the session panel's Settings ([below](#the-session-panel)) to leave it out (the choice is
kept in your browser). Thinking is never the agent's answer: the answer is the message under it.

When a Claude Code agent hands part of the work to a subagent (its `Agent` tool), that subagent
gets one row of its own in the conversation, as quiet as a row of tool calls: ↳, the task it was
given, and one glyph for how it stands at the right (a spinner while it works, ✓ done, ✗ error, …
when it has no result). Hover the row for its type, the model it runs on and the tokens it has used,
and for a failed one the first line of its error (a screen reader hears them in the row's name); the session panel shows the full detail. Click the
row to open it: a line says what it is doing (the command it runs, the file it reads, or the start of
its latest message) or, once it is over, how it went, how many tool calls and messages it made, its
tokens and how long it took, with the first line of its report; under it are the prompt it was given, its tool calls, thinking and
messages (no longer shown as the main agent's), and at the foot the report it returned. A row you
open stays open while you scroll. A subagent started in the
background shows a muted spinner and ⏱ ("running in the background") until it really finishes,
and its row is drawn with a dashed border until then (so is its row in the session panel). In a long session whose
subagent started further back than what is loaded, its work is still gathered in one row, titled
"Subagent · started earlier" and with no clock, that fills in once the earlier messages load. The subagent's own messages
and thinking need a Claude Code recent enough to forward them (`--forward-subagent-text`); with an
older one the row holds its tool calls and report. A session recorded by an older daemon shows its
subagents' work as it always did. When the agent resumes a subagent that had finished (it sends it a
new message), the new run is a row of its own at that point in the conversation, titled as the
first with a muted "resumed" marker beside the title, running until it reports back; the panel's Running now marks it
"resumed" and History lists it as a row of its own, "(resumed)", and while the agent waits on it the
working line says "waiting on" its title. A subagent from before a daemon restart that the daemon
no longer knows is titled "resumed agent". This needs a daemon from this release (reinstall the
desktop daemon). Not yet followed: a run Claude Code starts on its own, without a message from the
agent (a background subagent woken by its own background work reporting back); its work folds into
the finished row.

## The session panel

An agent session's page has a panel on its right with what is live in the session and what it has
done. The panel has four tabs: **Live** (Needs you, Running now, Session and Settings, below),
**History**, **Files** and **Capabilities** (below); the tab you chose is remembered per browser, and a new
question shows Live without changing that choice. The chat's toolbar above the conversation keeps
the model and effort pickers, Rules and Stop; what belongs to the session as a whole is in the
panel. On a wide
screen (1024 px and up) it is a column beside the chat, and the chat makes room for it; on a narrower
screen or a phone it is a drawer that slides over the chat, and Escape, the ✕ or a tap beside it
closes it. The button at the right end of the session header opens and closes it; its badge counts
what needs you and what is running. On a wide screen the panel is open until you close it, and
then it stays as you left it, in this browser. Once you have closed it, it opens by itself again
only when the agent asks a new question, once.
Tool changes waiting on you do not reopen it: their count is read when the session opens and again
when you open the panel or its Capabilities tab. The drawer opens only when you ask for it.
Terminal sessions have no panel.

- **Needs you**: the agent's open questions, each with how long it has been waiting and a box that
  answers it (the same answer as the ask card's), and the MCP tool changes waiting on your decision
  for this session, with a link to the Proposals page showing that session's proposals and tool
  changes (with a link back to all of them).
- **Running now**: the subagents at work, each with its task, type, model, tokens, what it is doing
  and how long it has run; and the background tasks.
- **Session**: where it runs, the engine and model, when it started and how long it has lived. The
  session's Insights open from the Insights button in the session header.
- **Settings**: two checkboxes for the chat, kept in your browser. **Show thinking** draws the
  model's thinking in the transcript; **Compact tool calls** folds a run of tool calls into one
  summary row. Both are on until you untick them.
- **History** is the panel's second tab: the pieces of work the agent marked, newest first
  ([History](#history), below).
- **Files** lists every file the session holds, as the Files dialog over the chat used to: published
  by the agent or attached by you, newest first, a file's earlier versions under it, each with
  Download, View (it opens in the chat's viewer; in the drawer, the drawer closes first) and Delete,
  and **Attach files** to attach some to your next message ([files from a session](artifacts.md)).
  It is read again each time the tab comes on screen. The tab's badge counts the files (names, not
  versions), and is hidden when there are none.
- **Capabilities** shows what the session loaded (skills, plugins, MCP servers, slash commands, tools and
  subagents), with a filter, as the Skills & plugins dialog used to; it asks the server only while it
  is on screen. A terminal session keeps its Skills & plugins button in the header, which says why
  it has nothing to show. The tab's badge counts what is enabled (skills, plugins that are enabled and
  MCP servers that are connected), and is hidden at zero.

"Show in conversation" under a row scrolls the chat to that question, subagent card or call and
flashes it; in the drawer it then closes the drawer. A row whose place is further back than the
chat has drawn says so, and the drawer stays open; scroll up in the chat to reach it. An answer you
were typing in the drawer is still there when you open it again.

### What is live is read from the whole session

Live, the header badge and the panel opening by itself read what is live from the runner server,
which goes through the session's whole history: open questions, subagents and background tasks
still running, and your tool changes waiting on the session. So a page you refresh in the middle of
a long session lists all of it at once, including a subagent or a task that started long before the
part of the conversation the chat has loaded, and Running now agrees with the sidebar's "⏱ N". The
server is asked again when something starts, ends or asks (at most every five seconds), and what
happens in between moves the list at once. A runner server older than this reads only what the chat
has loaded, as before: the lists fill in as the history loads.

### History

The panel's second tab lists the pieces of work the agent marked (topics), newest first. The agent
marks one with `blerg-runner topic "<title>"` when it starts something you asked for that takes
several steps, and `blerg-runner topic --done "<outcome>"` when it is finished or dropped, naming the
commits the work landed with `--commit <sha>`; the chat shows each as a thin divider. Only the agent
makes topics: nothing is guessed from your messages. Everything done outside a topic (before the
first, between two, or in a session where the agent marked nothing: an older daemon, a cron,
another engine) is one line after the cards, "Unmarked work · 42 agents · 8 files", that opens onto
those files, with their version chips, and one line per subagent (its status, title, model and
tokens). With no topics the tab says "No topics yet" and how the agent marks one, above that line.

A card gives the title, the outcome, when it ran ("10:42–11:30 · 48 min") and how many files,
subagents and commits it has. Open it for its timeline: your message that led to it (when you sent
it in the same turn; otherwise the marker), each file the topic published with every
version of it (a filled chip was published in this topic, an outlined one elsewhere, a struck one was
deleted; a chip opens the file), each subagent with its status, model, tokens and time, and how the
topic ended, with a chip per commit the agent named (its first seven characters; the full sha on
hover). A chip links to the commit when the runner knows the session's repository (the one it
cloned, or the folder's origin as the daemon reports it); otherwise a click copies the sha. "Show in conversation" jumps to a row the chat has loaded; an older row shows its time
instead (scroll up in the chat to load it). A loaded row older than the rows the chat has drawn says
so when you pick it; scroll up to reach it.

Tokens follow the Insights rule: only the session's owner and an administrator see them. The tab
asks the server only while it is on screen. A runner server older than the tab says "History needs a
newer runner server".

### Background tasks

A Claude Code agent can leave work running while it goes on: a command run in the background, a
Monitor watching a log or a deploy, a subagent started in the background. The panel's Running now
lists each one with its kind (⏱ a command, 👁 a monitor), what it is for, when it started and how
long it has been running; a subagent started in the background is listed once, as a subagent (or
as a 🤖 task when the call that started it is further back than what is loaded). A
finished task shows a check (or a cross) for ten seconds and goes; one that failed stays, with what
the engine said, until you dismiss it. When the agent has nothing else to do, the working line says
what it is waiting on ("Waiting on a background task: Watch the deploy"), and the session's row in
the sidebar carries "⏱ N" for as long as tasks run, whether or not the session is open. Such a
session is not shown as idle: while its turn is over and tasks run, its sidebar card shows "⏱ N"
where "Zz" would be, it is never counted as waiting for you, and
the session header, the panel's Session facts and Insights say "working in background". The sidebar lists the active sessions starred first, then by start time, newest first, and
nothing else: a session changing state (running, idle, waiting) never moves its card, only its glyph
changes; a new session appears at the top. A task the
agent left running ends with the session, or when its Claude Code process ends. A daemon restart
kills them: they are marked stopped ("the daemon restarted"), and so are the subagents that were
running, and the agent is told what was cut off (its own tool calls by what they ran, the tasks
and subagents by description and id), why the daemon restarted (an update or reinstall, or a stop
of the service: nothing it ran caused it; or an unexpected stop, which a test run, a build or an
ordinary command cannot cause either), and asked to check it and pick it up again. It does that by
itself, once, whatever it was doing (mid-generation, in a command of its own, waiting on that
work), in an interactive session too, so that you do not have to look into every session after a
restart. The exceptions: an interactive session that was waiting for you (an open
`blerg-runner ask`, a permission prompt); any session cut off in a command on the daemon's
deny-list (a force push, `rm -rf`, a migration, a deploy, the installer or a service restart,
`DROP`/`TRUNCATE`; [the list](../install/desktop/DAEMON.md#commands-a-restart-never-continues));
and one already continued once. Such a session is **paused by a restart** and waits for your
message. Their cards show the waiting "?" (its tooltip says "paused by a restart"); send one a
message to pick it up. A
message accepted for a paused session, from any tab, ends its pause everywhere. A
message you send first, even while the daemon is down, takes the place of that continue
([DAEMON.md](../install/desktop/DAEMON.md#what-a-daemon-restart-does-to-running-sessions)). Sessions
on an older daemon show none of this. The panel lists every task still running, however long ago
it started ([what is live](#what-is-live-is-read-from-the-whole-session), above). The working line
knows only the ones in the part of the conversation you have loaded. The board's chat has no panel: there only the working line
says what runs. A session that goes idle with tasks still running keeps its agent process alive for
them, up to about an hour; after that the process is closed and the tasks are marked stopped.

## Engine notes and the kill switch

This applies to Claude Code sessions. The other engines (Codex, Hermes) still take one message at a
time: a message sent while they work waits for the whole turn, and the bubble says only "Queued".

Under the hood a Claude Code session keeps one long-lived `claude` process and writes each message to
it as it arrives; an idle session's process is closed after 10 minutes and started again with the same
conversation when you next write. Set `BLERG_CLAUDE_STEERING=0` in the environment of the runner or
daemon to go back to starting one process per message (an older Claude Code that lacks the streaming
input mode falls back to that by itself). The design is in
[`design/mid-turn-steering.md`](design/mid-turn-steering.md).

## Resume after a usage limit

For a Claude or Codex agent session you launched, turn on **Resume when limit
resets** in the session panel's Settings. The runner records an actual provider refusal and waits
until one minute after the reported reset before sending a continuation. You can
turn it on after a refusal too, provided that interrupted turn is still current.
A usage meter reaching 100% does not by itself schedule work. If the provider does
not report a reliable reset time, the control asks you to continue manually.

The setting is off by default and belongs to one session. A normal message
cancels its pending retry and starts a new retry budget; interrupting, pausing,
stopping or disabling automatic resume cancels it. Up to three automatic attempts
are allowed before manual continuation is required. Sessions resume one at a time,
at least 15 seconds apart across an install.

The schedule is stored on the runner server, so it survives a server restart.
A sleeping workstation waits until its daemon reconnects. A disconnected cluster
session can recreate its pod, using your authorization retained when you enabled
the setting. Core still checks that authorization and MCP access; if your sign-in
expired or was revoked, sign in and continue manually. Automatic pod resumes never
fall back to an operator's provider credentials. Pending retries retain resumability
for at most eight days from the original refusal.

A dispatch is attempted once. If the server crashes between recording the dispatch
and delivering it, the control shows that manual continuation may be needed; it
never silently sends the same attempt twice. Terminal sessions are not supported.

If a session hit its limit before its daemon or pod was upgraded, enable the
setting and send one continuation message so the updated driver can record the
refusal and reset time. Older drivers did not publish this scheduling signal.

Desktop sessions need an updated workstation daemon to report provider refusals.
After updating your checkout, rebuild and restart it with
`install/desktop/daemon/install.sh install`. A cluster deployment updates the runner
server and the image for new pods; already running pods need replacement to use
updated engine drivers.
