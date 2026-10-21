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

## Choose a board at launch

The launch sheet's optional **Board** choice connects the session to a standalone Blerg Board.
Your browser remembers the choice, and the server checks your live access again before starting.
Choose an agent session in a supported local sandbox or cluster runtime. The session keeps its
coding tools and can read and write that board directly through the built-in `board` connection.
It appears in the panel's Capabilities tab; the session's supported `blerg-runner board` and
`ticket` commands use it too. See [board connection details](mcp-connections.md#the-built-in-board-connection).
The board shows in the session's Permissions as **Full**, where you can lower or revoke it like
any other ([Board access](#board-access-from-the-permissions-dialog)).

If a remembered board is unavailable, choose another or explicitly clear it before launching.
Discovery has a timeout and a Retry control; launches without a board do not wait on it.

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
no call in flight it says "Thinking…" while the model thinks, with how long this thinking phase
has run ("Thinking… 1:23"; the clock restarts at each phase, then goes back to the turn's time).
Hermes shows a live token count for the phase instead ("Thinking… 1,234 tokens"), when it reports
one: it starts again at each thinking phase, and a child's count is its output since its phase
began. Claude and Codex always show the clock: neither reports reasoning tokens while the model is
still thinking (Claude's count comes after the thinking block and covers the whole reply; Codex's
comes after the request ends), so a count there would be the wrong one. Counts come from the
engine, never from the length of its text, and like other token numbers only the session's owner
and an administrator see them; anyone else sees the clock. The Running now card uses the same
reasoning line for a child; status glyphs stay compact. The thinking itself still arrives as one
card when it is over. "Retrying the model (attempt 2)…" appears while the engine retries a failed API
call, until the model answers; the muted retry line stays in the transcript. Otherwise the line
says "Working…", or names the background tasks running. The main chat's thinking indication is
known live only: a page opened mid-think says "Working…" until the next live observation.
These indicators need a daemon from this release (reinstall the desktop daemon)
and, for Claude Code, a CLI that takes `--include-partial-messages`.

## Pointing at what you mean

When the feedback is about a file the agent published, open the file and use **Review** (a
markdown or PDF file: select a passage, ask for a change, edit the markdown yourself;
a diff: select patch line numbers and request a change) or **Mark
up** (an image: draw on it, drop numbered pins with notes). Both send one message the agent can
act on line by line. See [Review and mark-up](artifacts.md#review-and-mark-up).

## Files the agent mentions

When the agent's text names a file in the session's folder that it did not publish, you can open it.
A relative link (`[the readme](README.md)`, `[x](docs/a.md#L12)`) or inline code that looks like a
path (`runner/internal/server/artifacts.go:120`, `README.md`, `./scripts/scrub.sh`, `/workspace/x`)
is drawn with a dotted underline and the hint "Open from the session's folder". Only the agent's
text is read this way, never your own messages, a code block, a URL or a host path such as
`/home/...`. A bare `foo.bar` is not taken for a file unless its extension is a known one, and a name
with a slash needs a file-like last part (`and/or` and `src/components` stay text). A bare name with no
folder (`plan.md`) that is also a file published in the session opens that published file instead.

A click reads the file through the session (`GET /api/sessions/{id}/workspace-file?path=`, without any
`:line`, `#L` or query part) and opens it in the same viewer as published files: markdown, text, JSON,
CSV, images and PDF as usual; html in a sandboxed frame with no network, and svg only as an image.
Code files show numbered, highlighted lines, scrolled to the line the text named. The header says
"From the session's folder · as of now"; **Copy path**, **Download** and **Save a copy to Files** act
on what was read (the copy is kept as your own file even after the folder changes). Only the session's
owner sees these links, because the route refuses everyone else. A refusal is one plain sentence, not
the server's text: not found, files that usually hold secrets, other paths Blerg won't open, a session
that isn't running, a busy reader (try again), or a file too large. The board's chat does not open them.

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

Code and files are drawn to be read, not to fill the screen:

- A closed `mermaid` code fence draws a diagram; a `vega-lite` fence draws an inline-data chart
  with tooltips. Use **Source**, **Copy** or **Open** to inspect it or zoom. Invalid or refused
  visual output stays code. The same renderer works in published Markdown previews, the viewer
  and review page, and for `.mmd`, `.vl.json` and Vega-Lite schema JSON files in Files.
  Diagrams and charts use the chat’s colors and fonts, update when its theme changes, and
  fit the message width; Open provides a larger, zoomable view.
  External resources and executable HTML or JavaScript are refused. Charts support single plots
  with `data.values`; custom expressions, transforms and compositions stay as source.

- A code block is colored by its language once the agent has finished writing it (while it is still
  streaming it is plain). A block over 25 lines shows its first 15 with **Show all N lines** and
  **Open**, which shows it large with line numbers and a copy button; **Copy** on the block always
  copies all of it. A `diff` block marks added and removed lines.
- A message you paste that is over 25 lines is compacted the same way in your own bubble.
- A file the agent publishes, or one you attach, shows a small preview in the chat: a markdown file
  rendered, text, code and JSON as their first lines, a CSV as a few rows, a PDF as its first page.
  Click it to open the file. Big files (over 256 KiB, or 5 MiB for a PDF) just show the file card.

These come from the daemon, so on a machine still running an older daemon you keep the old
behavior until you reinstall it: the restart note and a command's output are missing or drawn as
yours, an API failure reads as the agent's message, retries and compactions show as before, and a
slash skill shows its instructions. Asks, updates and notes from `blerg-runner`, and an answer
typed in the chat, work with any daemon.

If the browser falls behind a very busy session and the server has to drop live events for it, the
chat is told and fetches them again; you may see the transcript fill in a moment later, not lose a
line.

## Copying and pasting

The chat copies and pastes the way other products do. **Copy** on a message (hover, or keyboard focus)
copies the message as Markdown, and as formatted text for an editor that takes it. On a touch screen
there are no Copy buttons: hold a message, code block, ticket card or checklist for half a second
(moving your finger or scrolling cancels) and a sheet offers **Copy message**, **Select text** (opens the
text full screen, all of it selected with the phone's own handles: drag them in, then **Copy** takes
what is still selected, or everything) and **Cancel**. A press on a link keeps the
browser's own menu. Selecting part of the chat and copying keeps the structure (headings, lists, links, tables,
code) without the timestamps and buttons; a selection inside one code block copies that code exactly.
With a file, code or ticket open over the page, Ctrl+A (Cmd+A) selects what is in it, not the page
behind it; in a text field it still selects that field.
Pasting formatted text from a web page, Docs, Slack or GitHub into the message box inserts it as
Markdown: images and scripts are dropped, a code block keeps its language. Plain text, code copied
from an editor or terminal, and text that is already Markdown go in as they are. **Ctrl/Cmd+Shift+V**
pastes plain text, and undo (Ctrl/Cmd+Z) takes a paste back in one step.

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
thinking" in Settings ([below](#settings)) to leave it out (the choice is
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
done. The panel has up to five tabs: **Live** (a one-line summary of the checklist, Needs you, Running now
and Session, below), **Plan**, **History**, **Files** and **Capabilities** (below); the tab you chose is remembered per browser, and a new
question shows Live without changing that choice. **Plan** holds the pinned tickets, the checklist (or the engine's own plan) and the diffs the session
published, newest first; click a diff to open it in the viewer. It shows only when the session has one of
them, and its label carries the progress ("Plan 18/23", or a count of tickets and diffs when there is
no list). Live keeps one line at its top for the list ("Checklist · 18 of 23 · Next: ..."), as two controls: the pinned ticket
(key and status, a chip that opens the ticket over the page) and the list (with a short bar beside the
count; it goes to Plan). A long checklist therefore never pushes Needs you and Running now
off Live. The chat's toolbar above the conversation keeps
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

- **Ticket**: the tickets the agent pinned, at the very top when there are any
  ([tickets and checklists](#tickets-and-checklists), below): the current one with its key (a link
  to the tracker), status and title, the others one line each. Clicking a title opens the whole
  ticket over the page, with "Show in conversation" to its card.
- **Checklist**: the agent's checklist, with "3 of 7 done" beside the title, a short progress bar beside the count (green when all are done) and the items
  (ticked ones struck through; you cannot tick them, only the agent does). When the agent set none, a
  Claude Code or Codex session shows the engine's own plan here instead, titled **Plan**, its header saying
  what is in progress ("Writing tests · 3 of 7"). Hidden when there is neither. An item the agent
  ticked with commits (`--commit`) shows a chip ("2 commits") that opens to the short shas: a link to
  the commit when the session's repository is known, otherwise a button that copies the sha.
- **Permissions**: what this session may do without asking, in short: how many are active, a
  **Manage** button, and each active permission as a chip (↳ children, ▦ a board, ⚙ an MCP tool)
  with × to revoke it at once. A permission given from a card in the chat is one more chip. One that
  no longer approves (its sign-in ended, or the tool changed) shows apart with why. **Manage** opens
  the Permissions dialog (a full-screen sheet on a phone), with a search over everything in it:
  - **Child sessions**: start sandboxed or unsandboxed children, or children on the cluster (a
    desktop session gets all three where the install has a cluster, each saying where it runs),
    without asking, with the engines each covers; a control the session cannot use says why.
  - **Boards**: your boards, each with an access level and how many have one
    ([Board access](#board-access-from-the-permissions-dialog)); a chip reads "Product · Full".
  - **MCP write tools**: grouped by connection, each tool you set to approval mode checkable; a tool
    whose definition changed since you allowed it says so.
  - **Active**: everything given now, with how and when (a card in the chat, the panel, launch),
    each with Revoke.

  Large board lists show 25 rows at a time, with Previous, Next, and search. Board links use the
  board address published by the site, which the runner reads for the app. If a save partially fails, **Save results** names the rows
  saved and the rows that failed; **Retry failed changes** retries the remaining edits.

  Nothing changes until **Save**, which applies it and says what it did ("Grant 2, revoke 1");
  Cancel or Esc throws the changes away. Rows Save would change are marked. The dialog works from the
  keyboard: arrows, Home and End move in a list, Space ticks, Tab stays inside, Esc closes. See
  [When the agent starts other sessions](#when-the-agent-starts-other-sessions) and
  [Session permissions](proposals.md#session-permissions). The launch sheet's **Permissions…** opens
  the same dialog to choose them before the session starts (the button counts them); they are given
  as it starts, and one that cannot be given is reported after launch and the session asks as usual.
  The dialog grows with a large screen, and **Maximize** (⤢) gives it the whole window.
- **Needs you**: the agent's open questions, each with how long it has been waiting and a box that
  answers it (the same answer as the ask card's), and the MCP tool changes waiting on your decision
  for this session, with a link to the Proposals page showing that session's proposals and tool
  changes (with a link back to all of them).
- **Running now**: the subagents at work, each with its task, type, model, tokens, what it is doing
  and how long it has run; and the background tasks. When the session or a subagent has a plan,
  its row also shows a short progress bar, the completed/total count and the current step's label
  (or its last completed step). A completed bar is green. Each subagent has its own plan and count;
  its updates never replace the session's plan. No plan or explicit progress means no bar.
- **Session**: where it runs, the engine and model, when it started and how long it has lived. The
  session's Insights open from the Insights button in the session header. For an engine that can
  continue after a usage limit, **Resume when limit resets** is here too: it belongs to this session
  ([Usage limits](#resume-after-a-usage-limit)). Settings that apply to every session are not in the
  panel; they are in the **Settings** dialog (below).
- **History** is the panel's second tab: the pieces of work the agent marked, newest first
  ([History](#history), below).
- **Files** lists every file the session holds, as the Files dialog over the chat used to: published
  by the agent or attached by you (marked "you"), one compact row each with its kind, size, version and
  time. Click a row to open it in the chat's viewer (in the drawer, the drawer closes first); Download
  and Delete are icons on the row (hover or focus; always shown on touch), and a file's earlier
  versions sit under it. With more than six files, a filter box, a sort (newest or name) and an
  origin choice (all, from the agent, from you) appear, and "4 of 21" shows while filtering; Esc clears
  the filter, and the sort and origin are remembered per browser. **Attach files** attaches some to your
  next message ([files from a session](artifacts.md)). It is read again each time the tab comes on
  screen. The tab's badge counts the files (names, not versions), and is hidden when there are none.
- **Capabilities** shows what the session loaded (skills, plugins, MCP servers, slash commands, tools and
  subagents), with a filter, as the Skills & plugins dialog used to; it asks the server only while it
  is on screen. A terminal session keeps its Skills & plugins button in the header, which says why
  it has nothing to show. The tab's badge counts what is enabled (skills, plugins that are enabled and
  MCP servers that are connected), and is hidden at zero. Its MCP servers group shows each server's
  status as it is now (connecting, connected, failed, needs sign-in, disabled), with the tool count when
  connected and why a held first message was let go; before the first update it shows the start-up
  snapshot.

"Show in conversation" under a row scrolls the chat to that question, subagent card or call and
flashes it; in the drawer it then closes the drawer. A row whose place is further back than the
chat has drawn says so, and the drawer stays open; scroll up in the chat to reach it. An answer you
were typing in the drawer is still there when you open it again.

### Progress

For work without a plan, report a script's progress with `blerg-runner progress 3/7 "Run checks"`
and finish it with `blerg-runner progress --done`. The command replaces that caller's progress;
a later native plan replaces it again. Counts must be whole numbers, with a positive total up to
1,000,000, and completed no greater than total. Labels are limited to 200 characters. Progress is
reported explicitly, never guessed from command output.

Claude shell calls receive a per-call scope through a Blerg `PreToolUse` hook; Codex shell calls
supply their native thread identity. The owning daemon resolves those identities to the current
session or subagent run before accepting progress. Unknown or ended calls are refused, including
a background script after its Bash call has returned. Keep the engine environment when invoking
the CLI so it retains its scope. A shell started outside either engine, with no native identity, reports session progress. Upgrade both
the runner and daemon to use this command; an older daemon times out with a clear error rather
than assigning an unknown caller to the session. The hook runs inside the engine's environment,
changes no permissions, and is absent from restricted sessions, which have no shell.

### What is live is read from the whole session

Live, the header badge and the panel opening by itself read what is live from the runner server,
which goes through the session's whole history: open questions, subagents and background tasks
still running, and your tool changes waiting on the session. So a page you refresh in the middle of
a long session lists all of it at once, including a subagent or a task that started long before the
part of the conversation the chat has loaded, and Running now agrees with the sidebar's "⏱ N". The
server is asked again when something starts, ends or asks (at most every five seconds), and what
happens in between moves the list at once. A runner server older than this reads only what the chat
has loaded, as before: the lists fill in as the history loads.

### Add a plugin or skill during a session

Open **Session panel → Capabilities → Add plugin or skill**. Choose an allowed marketplace and
enter its plugin name; skills are supplied by plugin bundles. The available marketplaces come
from the install's policy. Choose **This session only** to leave account settings unchanged, or
**Always on** to add it to the account's engine-specific plugin list as Settings does.

A supported host Claude session loads the plugin between turns, after foreground and background
work is idle. The engine process resumes its conversation with the new snapshot; an active turn
is never interrupted to install it. Codex, container and cluster sessions, and restricted
sessions currently offer always-on settings for **new sessions only**, with an explanation in the
panel. Restricted sessions continue to run without plugins; the setting applies to future
unrestricted sessions. They do not claim to have loaded it into the current session.

The agent can ask with `blerg-runner request-plugin helper@example/plugins "Why it is needed"`.
The built-in `blerg_request_plugin` tool takes `{name, reason}` and offers the same request in
restricted or unattended sessions without a shell. A bare plugin name is accepted only when its
marketplace is unambiguous. This queues a private
approval request, shown beside the chat and in the panel with a link to approve or decline on
**Proposals**. The person reviews the exact plugin, marketplace and reason, chooses its scope,
and confirms. The agent cannot approve its own request or request a marketplace outside policy.
A restricted session can request an always-on setting for future unrestricted sessions; approval
does not install into the restricted session. An unattended session's request waits for the person
too, and expires after seven days like
other proposals. Declining or expiry changes nothing.

Queued, applied, failed and new-session-only outcomes are shown from the server's
state. A failed application is not shown as loaded. See the
[desktop plugin notes](../install/desktop/DAEMON.md#adding-to-an-existing-session) for the daemon
requirement and [proposal rules](proposals.md).

### Tickets and checklists

When the agent works on a ticket (a JIRA issue, a Bugzilla bug, a GitHub issue, a Blerg Board card,
anything a tracker holds) it reads it with the tools it has and pins it with
`blerg-runner pin <file.json>`: it maps what the tracker returned into one shape (`blerg-runner pin
--help` shows it, with a JIRA and a Bugzilla example) and the session draws it the same way whatever
the tracker. The chat shows a ticket card: the tracker's name, the key (a link when the address is
http or https), status, type and priority, the people, labels, the description (long ones start
collapsed, with Show all), extra fields, the acceptance criteria and the latest comments (collapsed).
Pinning it again refreshes it, and the new card says "Ticket updated" and what changed (the status,
a new title). At most five are pinned at once; the newest is the current one, and
`blerg-runner unpin [<source>:<key>]` takes one off (a line in the chat says so). A Board card is
pinned the same way (source `blerg-board`); nothing is pinned by itself.

Pinning copies the ticket's text into the session: whoever can see the session can read it, as
they can read the rest of it (a private session is yours alone). The text is drawn as plain text and
markdown, never as a web page: no scripts, no images, no app cards, and a link only to http or https
addresses. The tracker's name on the card is what the agent said; its tooltip says the agent pinned
it.

The agent keeps a checklist of the work with `blerg-runner checklist set "<item>" …`, then `tick`,
`untick`, `add`, `remove` (one item, the rest untouched) and `clear` (`blerg-runner checklist --help`); `pin --checklist` sets it to the
ticket's acceptance criteria. Each change is a compact row in the chat ("Checklist · 3 of 7 done ·
ticked “Write tests”") that opens to the list; changes one after another with nothing between them
are one row, the latest. The session guide asks the agent for both. A session reloaded later shows the
same ticket and checklist: each event holds the whole state and what changed.

Use `blerg-runner checklist doing <n>` to mark current work (more than one item can be doing),
`pending <n>` to return it to pending, and `tick <n>` to finish it. Each item has an opaque stable `id`;
`--id <id>` selects it through a `rename` or `move`, while `set` replaces the list with new identities.
Legacy items receive deterministic IDs from their original event and position when read, then retain
those IDs in later events; long or malformed event IDs are hashed whole rather than truncated.

First run `blerg-runner checklist workers` to list recorded worker IDs (current work first, at most
100). These display IDs may differ from IDs returned by native engine tools. explicitly connect work with `doing <n> --agent <recorded-subagent-id>` or
`assign <n> --child-session <owned-child-session-id>`; `unassign` removes that connection. The server
accepts only native workers recorded in this session or its directly owned Blerg child sessions,
canonicalizing an unambiguous native call ID to its child ID. Current live state follows a later
canonical ID without changing the original event; unknown or ambiguous native references are omitted
from live state and the next mutation rather than retargeted. Titles are never matched. An item can
have several workers; assigning a worker elsewhere transfers it. Completion retains the connection,
and removal retires the item without rewriting history. The panel's worker reference opens Plan and
highlights the same item, including after reorder or rename. Each item supports 32 assignments and
the list supports 128; the full event is bounded to 128 KiB.

Name the commits that finished an item with `blerg-runner checklist tick <n> --commit <sha>` (or
`add … --commit`; repeat the flag or comma-separate the shas; 7 to 40 hex characters, at most 20 on one
item, the same check as `topic --done --commit`). Ticking again adds more; a repeat is dropped. The
item shows a commit chip in the panel and in the chat row. Run in the repository, the command also
publishes the diff of those commits ([Diffs](artifacts.md#diffs)) and keeps its id on the item: the chip
gets **View diff**, which opens it. When the diff cannot be made (the commits are not in that
repository, or it is over the per-file limit) the command says so and the item keeps its commits
without one.

### Proposing a new Board ticket

To file something discovered during a session, the agent first lists the Boards the session owner
can access and searches the chosen board for duplicates:

```sh
blerg-runner ticket boards
blerg-runner ticket search --board "Example" --query "startup progress"
blerg-runner ticket file --board "Example" --title "Show startup progress" \
  --body "Describe the observed behavior and the expected result." --type bug --repo app
```

`--board` accepts a board name or ID. Repeat `--repo` for multiple target repositories; these are
independent of the session's source repository. If a board requires a repository and has only one,
that repository can be its default; otherwise select the target repositories explicitly.
Add `--sensitive` when the body should be readable only by that target board's administrators.
Keep sensitive details out of the title: the title and source metadata remain visible, and this
flag does not hide anything already in the session's conversation.

These commands do not need a Board selected at launch. The built-in `blerg_ticket_file` tool also
offers `list_boards`, `search` and `propose` actions to Claude and Codex, including restricted
sessions without a shell. It provides this approval-gated workflow without adding arbitrary tools
or changing the session's existing MCP grants.

Filing queues a proposal for the person. On **Proposals**, review it, edit or retarget the pending
draft if needed, then approve the current exact draft or reject it. Blerg records the source
session, repository, verified commit when available, and engine. Nothing is written before
approval, and an ambiguous **unknown** result is never retried automatically. See
[Board filing proposals](proposals.md#filing-a-board-card-from-a-session).

### Board access from the Permissions dialog

A session without a board can still search your boards and propose cards; each proposal asks you.
To let it do more, give a board a level under **Live → Permissions → Manage → Boards** (or in the
launch sheet's **Permissions…**). Each level includes the ones below it:

| Level | The session may |
|---|---|
| **Propose** (every board, the default) | search for duplicates and propose a card, which asks you |
| **Read** | also list, get and search every card |
| **File without asking** | also file its proposals at once (the 0.4.3 board filing permission) |
| **Full** | also create, update, move, link and comment: what choosing the board at launch gives |

Session board access allows 240 HTTP requests per rolling minute. A CLI command can make several
requests: a named card move uses one access lookup and six board-tool calls. Access is checked
fresh for every command. At most four board-tool calls run at once. A rate refusal includes the
remaining wait in seconds and a `Retry-After` header. A refused request is not counted (it never
was), so retrying does not extend the window. Each call's board credential is revoked right after
it; while revokes fail, an account stops starting board calls once 20 unrevoked credentials are
still live, and answers 503 until they expire (at most 10 minutes).

Columns, archiving and board settings are never available to an agent at any level. A board you
chose at launch shows as Full; lower it, or set it to Propose to revoke it, like any other.

The level is checked on every command and call, so a change holds at the agent's next one, with no
restart; lowering a level applies from the next call, and a call already running finishes. The agent
is told when you change a level: a short note from Blerg reaches it at its next step ("Board access
changed: board "Product" is now Full (was Propose)"), and it is asked to check `blerg_board` with
`{"tool":"access"}` (or `blerg-runner board access`) before telling you it lacks access.
`blerg-runner board access` lists the boards and levels the session has. The `blerg-runner board`,
`ticket list|get|create|update|move` and `tag` commands reach a board given here through the runner
with the session's own token: name it (`blerg-runner ticket list Product`), or, in a session without
a board chosen at launch, it is their default when it is the only one. With a launch board, a command
that names no board uses the launch board. Claude and Codex get the same through the `blerg_board`
tool, which every session has from its start, so a level you give mid-session works at the next call. The agent
never receives a board credential: the runner exchanges one for each call (read-only for a read)
and revokes it after. A board chosen at launch also gives the engine the board's own tools, and
those follow the level too: lowering the board refuses its write tools at the next call, though the
engine may still list them until it restarts.

Only you, signed in, can set a level; a level acts with the sign-in you set it from, so signing out
stops it until you renew it (**Renew** on the board's row, or choose another level). A sign-in limited
to one board changes only that board's level. The engine's own board tools of a launch board act with
the sign-in you launched with. Restricted, cron and board-started sessions cannot be given board
access this way; they keep exactly what their launch gave them. Like every session permission, the
levels end with the session: one stopped for good and resumed starts again from its launch.

### Board access for sessions a session starts

A session a session starts (a child) has no board access of its own. The parent passes its own down
with the start, one board at a time and never above its own level for that board:

    blerg-runner session start --engine codex --repo myrepo --board Blerg:full --board Ops:read "work cards 12-15"

The approval card shows it ("with Blerg · Full, Ops · Read"); a rule or a grant you gave can start
such a child without a card, and the start is checked against the parent's levels again when it runs
(a card decided later, a rule or a grant), so a parent you lowered in between cannot pass down what it
no longer has. No new credential is minted: the child gets a level of its own, and its board calls go
through the runner with its own session token like any session's, acting with the parent's sign-in
for that board. Desktop and cluster children, sandboxed or not, read and change cards the same way.

**A desktop child cannot file proposals yet.** Filing (`blerg-runner ticket file`, `blerg_ticket_file`,
and so filing without asking at **File without asking**) needs a board sign-in recorded when the
session starts, and a child started on a desktop has none: its filings are refused with "no retained
Board discovery proof". Passing **File without asking** to a desktop child therefore gives it only
**Read**; pass **Full** if it should create cards (`ticket create` goes through its board access), or
have the parent file. Whether a child may file with its parent's sign-in is an open decision.

**Each child has its own limits.** A child's board calls count against its own limits, not its
parent's: 4 board calls at once and 240 board requests a minute per session, and at most 20 filings
an hour approved without asking per permission. A parent runs at most 3 children at once and an
account at most 6, so with children an account can have up to 24 more board calls in flight, and a
parent at **File without asking** and its children together up to 80 filings an hour without asking.

Every board call of the child is checked against the lower of its own level and the parent's level
at that moment. Lowering or revoking the parent's level lowers every child at its next call, and
raising the parent never lifts a child above its own level. The child's **Permissions** dialog shows
each board's level with the cap ("Acts at Read: capped by *Parent*, which has Read"); there you can
lower a single child, or raise it back up to the parent's level, never above it. The child is told
of each change, the parent's included, by the same "Board access changed" note, and checks
`blerg_board` with `{"tool":"access"}` (or `blerg-runner board access`, which marks the cap) before
saying it lacks access.

The access ends with the parent's: when the parent is stopped for good (or discarded), its children
lose their board access, a child kept running with `--keep-running` included, even if the parent is
resumed and given access again. A child's own access ends with its own lifetime, like any session
permission.

### The first message waits for MCP servers

A new session holds its first message for up to 30 seconds while its MCP servers connect, so the
agent starts with its tools (`BLERG_RUNNER_MCP_PROMPT_WAIT_S`, 0 turns the wait off). While it waits the
chat shows, above the message box, "Waiting for MCP servers before sending your first message · 0:12 /
0:30 · Send now". **Send now** (the session's owner only; others see the wait without the button)
sends it at once. When it is let go, the panel's MCP line says why: the servers were ready, the wait
ran out, or you chose to. Later messages never wait.

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

## When the agent starts other sessions

An agent on your desktop or on the cluster can ask another session to do part of its work. The approval card shows
the engine, model, repository, runtime, prompt, and whether the child should keep running after its
parent ends. A host request warns that it runs outside the sandbox. You can deny it, allow it once,
allow matching starts for this parent session, or create a standing rule for that engine, runtime,
and desktop. **Allow for this session** covers any repository, or no repository, for that engine,
runtime and desktop while the parent session lives. The repository shown on the approval remains
in the audit record; it does not restrict later starts. There is one live session permission per
engine, runtime and desktop, and Revoke removes it for future starts. Standing rules cover any
repository too. Choices that would widen the parent's permissions always ask again.

The child's model is checked against the models its engine actually has where the child will run
(on the cluster, the cluster's catalog; on a desktop, what that desktop reports). The agent can
name it by routing id, by display name, or by a part of an id that matches exactly one model, and
the card and the child record the routing id. A name that matches no model, or several, is refused
with the list of available models; `blerg-runner models` prints that list. Changing a running
child's model resolves the name the same way.

You can also allow child starts under **Live → Permissions → Manage** before the agent asks to start a
child. It covers the engines currently available on this desktop for the selected runtime during
this parent session's life. Sandboxed children and unsandboxed children are separate choices;
unsandboxed approval is unavailable to a sandboxed parent. Enabling this permission can use the
selected engines' provider budgets. Newly installed engines do not acquire permission automatically.
You can change or revoke the session permission later without rewriting earlier approval decisions
or stopping children already running. Turning it off does not remove standing desktop rules;
the controls show those separately, and you can remove them in Settings. A partial state can come
from an earlier **Allow for this session** for one engine. Only your own current browser login can
edit this permission; the agent cannot turn it on for you.

Only sessions you launched directly can use preapproval. Board, cron, private, and agent-started
parents ask for each start; a child cannot start another generation of children. New child starts
need an updated, connected desktop daemon.

### Children on the cluster

A session running on the cluster starts its children on the cluster, as cluster pods; that is the
default there, and `--runtime cluster` asks for it explicitly. A session on your desktop can start a
child on the cluster too, when the install has a cluster runtime: the agent asks for
`--runtime cluster` (its children still run on the desktop by default). On a desktop-only install
that request is refused with "this install has no cluster". A cluster session cannot start a desktop
child. The approval card says **on the cluster**, and its **Always allow** button creates a standing
rule for that engine on the cluster. A pod gets your credentials, which a desktop sandbox child does
not, so a **sandboxed** desktop session asks you for each cluster child: its card offers only **Allow
once** and **Deny**, its Cluster row in the Permissions dialog is shown off with why, and standing
cluster rules do not start its children. An operator can change that with
`BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN=any` ([cluster runtime](../install/k8s/CLUSTER-RUNTIME.md#desktop-sessions-cluster-children)).
A standing cluster rule applies to the cluster children of every session of yours that may use one:
cluster sessions and unsandboxed desktop sessions alike. Cluster rules and session permissions are separate from desktop
ones: a rule for your desktop never starts a cluster child, nor the reverse. For a cluster session the
Permissions dialog shows one cluster control instead of the sandboxed and unsandboxed pair; for a
desktop session it shows all three, each row noting where its children run ("on this desktop" or
"on the cluster"). The child's repository is cloned into its pod the way any cluster launch clones it,
so a desktop session names it as org/name (a desktop folder name works only where the cluster has a
default org), and its engines are the ones the cluster has credentials for. A child pod runs with
your engine credentials, as your own cluster sessions do; they go only into the child's pod, never
to your desktop daemon or the parent agent. A rule or session permission can start one only while
your sign-in behind the parent is still valid (for a desktop session, the browser sign-in you
launched it from, or your latest decision on one of its cards: allowing one cluster child, even once,
makes your current sign-in the one its later automatic cluster starts use, until you sign out);
otherwise the agent's start asks you
on the card, and your decision authorizes it. Each cluster child keeps the sign-in it was started
with: a desktop parent's message to an idle child resumes it with that sign-in, and once it has
ended the parent is told to ask you to open the child and send the message yourself. Limits, Live,
the sidebar, discard and the parent's end treat cluster children as they treat desktop ones; a child
whose pod could not be launched does not count toward the start limits. Hermes on the cluster counts as
local, for the budget and for local-only Hermes rules, when the operator's
[shared Hermes providers](../install/k8s/CLUSTER-RUNTIME.md#shared-hermes-providers) point only at
keyless private-network model servers and you have not saved a Hermes credential of your own (it
could hold a provider key); otherwise a local-only Hermes rule asks you on the card. A child of a
cron session never gets operator-supplied credentials or the shared providers.

The parent's Live tab lists its children and pending requests. Open a child to read its conversation;
Started sessions shows the temporary permissions and their Revoke buttons. Revoking permission
prevents future starts; it does not stop existing children. Settings lists standing rules and lets
you add or remove them, desktop and cluster rules alike (the cluster runtime is offered where the
install has one); a card's **Always allow** button adds one too. The sidebar groups children under their parent, and Insights shows child
usage separately from the parent's own usage.

A parent can have three running children and make twenty starts. Across your account, the limits
are six running children and sixty starts per hour. Approval requests expire after ten minutes.
Pausing or restarting a parent expires its pending requests while preserving existing children and
session permissions. A real end revokes those permissions and stops children, including children
whose daemon reconnects later, unless you approved **Keep running** for them. Resuming the parent
does not revive children stopped by that earlier end.
An engine already running on an offline daemon can only be stopped when that daemon reconnects.

Agents use `blerg-runner budget` to read the capacity their own desktop reports, separately for each
provider. Missing usage information is shown as unknown. A local engine is labeled local; a
Claude budget does not imply the same capacity for Codex or another provider.

In a child session, what the parent agent sends, its answers to the child's questions included, is
labeled "↑ Parent · <parent title>", with a link to the parent session; your own messages and
answers read "You". The runner decides this from the parent's own session token, never from what a
caller claims. The child's header says who started it: "started by Codex agent from <parent>".

The parent is told when a child's turn ends. When a child finishes its turn, asks a question, waits
on an approval only you can give, or fails, the parent gets a short note from Blerg at its next
step (`Child "<title>" finished its turn: "<first line>". blerg-runner session result <id>`), so it
does not need to keep watching the child. The note shows in the parent's chat as a Blerg note. It is
sent once per child turn, never to a parent that has ended, and no caller or agent can write one
(its source, `child_turn`, is refused from every caller like Blerg's other relayed notes).

A sandboxed child works in a clone of its own, never in its parent's checkout, and has no write
access to the repository's `.git` (every Local sandbox session does). Its commits land on the
branch `wip/<child-id>` of the repository on your desktop: the daemon fetches that one branch back
at the end of each of the child's turns and when it stops, with any work it left uncommitted at the
stop committed for it first. A host child (`--runtime daemon`) works in the checkout itself. See [What a sandbox works in](sandbox-image.md#what-a-sandbox-works-in-and-where-its-commits-land).

Host sessions share the daemon's home directory. A host session can therefore read another host
session's token file; session-token API checks do not provide isolation from code running on the
same host. Use the desktop sandbox when you need that boundary. A container also needs an engine
endpoint it can reach; a Hermes endpoint bound only to the host's loopback may be unreachable there.
OpenClaw remains host-only.

## Settings

**Settings** is the entry under **Insights** in the left bar (a tab on a phone). It opens a dialog
over the page you are on: you stay in your session, and **Esc**, the backdrop or **×** close it. On a
phone it fills the screen. It holds what applies across all your sessions:

- **Chat**: **Show thinking** draws the model's thinking in the transcript; **Compact tool calls**
  folds a run of tool calls into one summary row. Both act at once, are on until you untick them, and
  are kept in your browser.
- **Code colors**: pick a theme for dark mode and one for light (Blerg, GitHub, One Dark, Dracula,
  Monokai, Solarized, Nord, Gruvbox), change single colors under *Customize*, and watch real code
  blocks (ts, go, python, json, a diff) change as you do. **Save colors** keeps the choice on your
  account, so it follows you to every browser and to the board's chat; closing the dialog first throws
  the draft away.

Account, tokens and connections are in Blerg's main Settings, which the dialog links to. Per-session
choices, such as Resume when limit resets, stay in the session panel.

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
resets** in the session panel's Session section. The runner records an actual provider refusal and waits
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

### Inline code review

Open a published diff and use **+** beside a line to comment directly below the code.
Select a range with Shift or the touch-friendly range control before pressing **+**
to comment on several lines. Comments stay beside their code in either diff layout.
Use **Add general comment** for feedback on the whole change, then **Submit** to send
the pending comments together as one review.

Comment boxes support Markdown formatting and **Write / Preview** tabs. On new-side
code, **Insert suggestion** prefills an editable replacement for the selected lines.
The suggestion is sent to the agent for checking and applying; the browser does not
edit source files.

Agent replies show **Resolved** or **Rejected**, with their explanation. **Reopen**
queues the same thread for another review round and retains previous replies.
Use **Collapse thread** to keep just its summary, or **Hide resolved** to hide
resolved and rejected threads together. Open requests stay visible; neither control
changes the review or deletes replies.
Publish updated patches under the **same diff title** so the review follows their
versions. Unchanged, unambiguous passages keep their inline threads; changed or
ambiguous passages remain under **Earlier revision comments**, marked **Outdated**,
with the original code and responses intact.


### Reviewing documents and images

For Markdown, open **Review** and edit the rendered document. Insertions and deletions
appear with **Show changes** enabled; **Clean view** hides those marks without undoing
edits. Formatting or structure changes are noted, and all edits are included in the
source diff sent to the agent. **Source** remains available for direct Markdown editing.
Documents with constructs the rich editor cannot preserve (such as embedded HTML,
images, reference links, frontmatter or visual fences), or over 200,000 characters,
use source editing with a preview instead. Merely opening a document or changing
views does not rewrite its Markdown.

Select a passage and click **Add comment**, or press **Ctrl+Alt+M** (Command+Option+M
on a Mac). The composer opens in the margin beside the passage. Saved comments
highlight their text; **Show passage** takes you back to it. On narrow screens,
comments stack below the page. PDF reviews use the same passage and margin workflow,
with page-aware anchors; PDF text itself is not edited. Comments whose original
passage is deleted, whose document version changed, or whose positions cannot be
proved stay visible with **Original passage unavailable**. Source edits conservatively
leave existing comments unavailable rather than attaching them to a different passage.

For images, open **Mark up**. Each pen stroke, arrow or box becomes a numbered
annotation in the sidebar. Select it on the image with **Select**, or click its sidebar
entry, to edit its color, width or comment. Hide/show and delete work per annotation.
**Comment** places a point comment without a drawing. Sending uploads a flattened
annotated copy and the numbered comments; hidden annotations and their comments are
omitted, and the original image is untouched. Editing is paused while sending.
Annotation layers are editable within the open markup view; the submitted PNG is flat.


### Formatting controls and attachment previews

The document formatting bar uses compact icons. Hover for the control name; selected
formatting is highlighted, and screen readers receive the same labels. It includes
bold, italic, strikethrough, heading, lists, quote, code, table, link and undo/redo.
Link editing retains the document selection and accepts HTTP, HTTPS and mail links.

Before sending a message, attached images show thumbnails, PDFs show the first page,
and Markdown/text files show a short plain-text excerpt. The name, upload progress,
errors and Remove control stay visible. Previews are generated locally and are
optional: an unsupported, large or unreadable file keeps its file tile and can still
be attached. SVG and Office files use file tiles rather than active document previews.


### Discarding child workers

A child is a reusable worker. Running, idle and stopped children stay together in Started
sessions and in the parent's sidebar list; they do not collapse or move when their status
changes. Use **Discard** on its panel row or in its sidebar card's Actions menu, then confirm.
Discard stops and archives the child. History, Insights totals and its direct session URL stay
available. A failed stop leaves the child listed so you can retry. The parent can also run
`blerg-runner session discard <child>`; `session stop` by itself keeps the worker listed.
