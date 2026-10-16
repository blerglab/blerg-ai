# A desktop agent session survives a daemon restart

*2026-10-07. Runner daemon and server. Revised after two adversarial reviews, one of the design
and one of the code (their findings are folded in; what was deliberately left is listed at the
end).*

## The problem

A desktop agent session lives in the daemon's memory: the Claude Code session id it resumes
with, its options, its queue. Nothing is written to disk. When the daemon process restarts (an
update through `install.sh install`, a crash, a reboot) the engine children die with it, the new
daemon does not know the session existed, and the server leaves the row in `error` until it
finalises it as `daemon_lost` thirty minutes later. The session cannot be continued. A message
sent to it meanwhile is dropped, and the contract's `send_message` answers `{"ok": true}`.

Terminal (tmux) sessions have an on-disk record and a recovery pass. Cluster pods are resumed by
the next message. Desktop agent sessions are the gap, and they are the sessions a person works
in. It also makes the daemon impossible to develop from a desktop session: the update that tests
a change ends the session that made it.

## What "survives" means

After a daemon restart the same Blerg session continues: same id, same transcript in the app,
same Claude conversation (`claude --resume <id>`), same model, effort, interaction mode and
session token, and the same plugins reinstalled. Unavoidably lost: the turn that was in flight
(its tool call was killed), messages still queued in memory, events the server had not
acknowledged, and streaming text.

Checked against the real CLI (2026-10-07): a streaming `claude -p` process killed with SIGKILL
while a tool call was running resumes with `--resume <id>`, keeps the same session id, and its
transcript is `~/.claude/projects/*/<id>.jsonl`.

## Scope

Recovered: host-runtime agent sessions on the Claude Code driver that are not restricted and
hold no MCP grant. The decision is made inside `AgentHost.spawn`, after the driver is chosen.

Not recovered in this round, and ended as today (`error`, then `daemon_lost` after 30 minutes):

- **Restricted and grant sessions** (a cron, a session with MCP connections). Recovering one
  means writing its gateway credentials to disk. They are unattended and short.
- **Sandboxed agent sessions.** The engine inside the container dies with the daemon and the
  restart sweep removes the container. A follow-up.
- **Other engines** (Codex, Hermes, OpenClaw, the native API loop).

## The record

One JSON file per recoverable session, `<daemon state dir>/agents/<session id>.json`, 0600 in a
0700 directory owned by the daemon's user, written atomically with a unique temp name. The state
directory (`~/.blerg-runner-daemon`), not the repos root: the repos root can be changed from the
app, and a daemon older than this change reads `<repos root>/.blerg-runner/sessions/` and would
recreate anything there as a terminal session. A daemon with no state directory keeps no records.

The record is an allow-list, not the spawn message:

| Field | Why |
|---|---|
| `session_id`, `repo`, `work_dir` | identity; `work_dir` is the resolved workspace |
| `interaction`, `assist`, `board_id`, `ticket_id` | what the session is |
| `session_token`, `board_token`, `extra_env` | the session's environment (secrets, as in the terminal records) |
| `plugins` | the spec only; the snapshot is rebuilt |
| `model`, `effort` | as last changed by the person |
| `claude_session_id` | as last reported by the engine; it can change when a resume fails |
| `turn_active` | a turn was running when this was last written |
| `tool_running`, `open_calls` | the agent's own tool calls that had started and not returned, each with its tool and what it ran (a command and its description, a file, a pattern); absent in a record from an older daemon |
| `waiting` | the session was `waiting` for the person; absent (false) in a record from an older daemon |
| `open_tasks`, `open_subagents` | the background tasks and subagents running |
| `auto_continues` | how many times recovery has continued a cut-off turn by itself since a turn last finished |
| `engine_pid`, `engine_started` | the engine process and its start time as `ps` prints it |

Never recorded: the initial prompt, git token, clone source, provider, the new-repository and
no-repository flags, an MCP gateway, sandbox and restrict flags (a session with either has no
record).

Each hosted session holds its record in memory behind a mutex; every change rewrites the file
(synced to disk before the rename). Changes come from: the session's creation; the host's
existing event observer (`status_changed` sets `turn_active`, `turn_done` resets
`auto_continues`, `model_changed` updates model and effort); and two driver callbacks (the
Claude session id changed; an engine process started or went away). A write failure is logged
and never fails the session.

**One daemon process owns the records.** The daemon takes an exclusive `flock` on the records
directory at start and holds it for its life. A second daemon run by the same user (a build
started by hand, a daemon for another server) does not get it: it keeps no records and adopts
nothing, so it can neither host the first one's sessions nor stop its engines.

**The shutdown must not erase what recovery needs.** Under systemd the stop signal reaches the
engine too; its exit makes the driver report the turn over, which would clear `turn_active`
just before the daemon dies. Three guards:

- going idle clears `turn_active` only when the turn that just ended ended by itself. A turn
  that ended in `error` because its engine died was cut off, whoever killed the engine, and
  stays recorded as such until a later turn finishes;
- the daemon freezes all record writes as its first act on SIGTERM or SIGINT;
- the unit gets `KillMode=mixed`, so the daemon is signalled first and the engines are killed
  only after it has gone.

Deleted: when the session is killed (`AgentHost.Kill`); on `kill_session` for a session the
daemon is not hosting (a stop that arrives before or instead of adoption); when adoption fails.

## Adoption

Once, at process start (`Manager.AdoptAgentSessions`, called from `main`). Never on a reconnect:
the sessions are in memory then, and a pass per connection would race a spawn in progress.

It has two halves. **Claiming** happens before the daemon connects and is instant: the records
are read and their sessions are reported as this daemon's from the first hello on, so the server
keeps their rows. **Hosting** then runs in the background, one session after another, because
reinstalling a session's plugins can take minutes and the daemon must not stay offline for that.
While a session is claimed but not hosted yet, a message for it is held and delivered, in order,
once it is; a `kill_session` for it means it is never hosted. For each record:

1. **Validate.** The file is a regular file owned by the daemon's user in a directory only that
   user can write, its name matches its session id, the work dir is an absolute path that still
   exists, and the driver this daemon would choose today is still Claude Code. (The work dir is
   not required to be under the current repos root: the root can be changed from the app while
   sessions keep the folder they started in.) A model or effort that no longer validates
   falls back to the default instead of failing.
2. **Stop a leftover engine.** If `engine_pid` is alive and `ps -o lstart= -p <pid>` still prints
   the recorded start time, it is the same process: SIGTERM, then SIGKILL after two seconds.
   (systemd kills the control group anyway; this covers a daemon run from a terminal or launchd.)
3. **Rebuild.** A dedicated adoption path through the host: the same environment, tools and
   driver as a spawn, but no `session_started` (it would reset the row to `starting` and reopen
   the start panel), no start-stage events, no initial prompt. Plugins are reinstalled from the
   recorded spec; if that fails the session continues without them and says so.
4. **Resume.** The driver's Claude session id is preset to the recorded one when its transcript
   exists; otherwise the conversation starts fresh.
5. **Tell the person.** One retryable `error` transcript event, which the chat renders and which
   closes the tool call left spinning: "The session's host restarted. The conversation was
   restored." plus, as applicable, "The step that was running was cut off." / "The earlier
   conversation could not be restored: the agent starts fresh, with the files as they were." /
   "Its plugins could not be reinstalled." / "1 background task and 2 subagents were stopped."
   Then each background task and subagent the record lists as running (`open_tasks`,
   `open_subagents`, kept current by the driver as they start and end) is ended as its own end
   would have been: `background_task_done` with status `stopped` and summary "the daemon
   restarted" (which moves the server's `background_tasks` count down through the ordinary ingest,
   so the sidebar drops to 0), and a final `subagent_done` "stopped: the daemon restarted". The
   adopted record starts with none. (When the engine process ends while the daemon runs, its
   tasks and subagents are ended then, "the engine process ended", so none is left open in the
   record for a later restart to report.)
6. **Tell the model, once, without it showing as the person's words.** The driver carries a
   one-shot adoption note that is prepended to the next message **only on the line written to
   the engine**; the `user_message` event keeps the person's own text. It survives the driver's
   retry, and is recomputed if the resume turns out to be refused:
   - a turn was cut off: "[system] The process hosting this session was restarted while you
     were working. The conversation is intact, but the step that was running was cut off: a
     command may not have finished and a file may be half written."
   - the conversation is gone: "[system] The process hosting this session was restarted and the
     earlier conversation could not be restored. The files in the workspace are as they were.
     The person can still see the earlier transcript, but you cannot: ask them for what you need."
   - resumed with nothing cut off: no note.
   - why the daemon restarted ends the note of a resumed conversation (and the list below, when
     that is all there is): the daemon reads it once at start, before adopting (`restartcause.go`).
     `install.sh install` writes `restart-planned` (`update` or `reinstall`, by whether the new
     build differs from the running one) into the state directory right before it restarts a
     running daemon; the daemon writes `stopped-cleanly` there on SIGTERM or SIGINT (on the
     signal and again before it exits). Both are removed when read, and an installer marker older
     than 15 minutes is not believed. Either one: "The daemon was updated and restarted. Nothing
     you ran caused it. Continue where you left off; check the state of anything that was mid-run
     first — never re-run anything destructive without checking." ("reinstalled and restarted",
     or "restarted" for a stop with no installer marker; "Nothing you ran caused it." is dropped
     when a cut-off Bash call matches the installer/service-restart entry of the deny-list or,
     for a stop with no installer marker, runs `kill`, `pkill` or `killall`). Neither: "The daemon
     stopped unexpectedly. A test run, a build or ordinary commands cannot cause that; only
     re-check a command that could affect the daemon itself (an install, a service restart)
     before running it again. Continue where you left off; check the state of anything that was
     mid-run first." The old note said the restart "may have been caused by a command you ran:
     do not run it again", and an agent cut off in its own unit tests took that as its cause and
     stopped.
   - something was cut off and the session is not continued (step 7): the list below follows
     the note.
7. **Continue, or wait.** Whatever a restart cut off is picked up again by itself, so that
   nobody has to go through every session after a restart: a turn (mid-generation, or in one of
   the agent's own tool calls), background tasks, subagents. The session is continued once
   ("Continue the task you were working on.", source `system`, with the note) if
   `auto_continues` is 0, unattended or interactive alike. Two exceptions. Any session cut off in
   a call on the restart deny-list (`destructive` in `open_calls`, set when the call starts from
   the whole command; for an older record, its `detail` is matched): a force push, `rm -rf`, a
   migration, a deploy, the installer or a service restart, `DROP`/`TRUNCATE` (the list is in
   DAEMON.md). Deterministic, not left to the model. And, for an `interactive` session only: it
   was waiting for the person. That is `waiting` (a permission prompt, a native
   question), an open call that is a question (`blerg-runner ask` in a Bash command, or
   `AskUserQuestion`), or, for a record that cannot say (an older daemon kept neither
   `open_calls` nor `waiting`; a missing `waiting` reads as not waiting), an open ask the server
   holds: after each hello the server sends the daemon `open_asks`, the sessions among those it
   reported that have an ask open, and the continue checks it once connected. A session that
   waits shows the list below at once as a `system` `user_message` (one the server's asks stop
   says so with a second notice, adds the cut-off question to the agent's note, and its continue
   is not counted), the agent gets it with the person's next message (after the note), and the
   daemon reports it to the server as `session_state_changed` `idle` and then `session_paused`
   (reason `restart`; below). So is a session it had already continued once (`auto_continues`
   1) and one whose conversation could not be restored. A session cut off before the engine
   named its conversation is neither continued nor reported paused: a continue would reach an
   agent that does not know its task, so the notice asks the person to send it again. In either kind of
   session, a message from the person that reaches the driver before the continue (held while
   the daemon was down, or written during the wait) replaces it: the continue is not sent, that
   message carries the note and the list, and the session is not reported paused. The list:
   "[system] The process hosting this session restarted, and work that was running was cut off."
   then "Your own tool calls that were cut off before they returned:" (each by tool and what it
   ran: "- Bash: make deploy (Deploy to staging)"), with "Check its state first: it may not have
   finished, or may have half written a file. Never re-run anything destructive without
   checking." (and, for a question, that the message that follows may
   answer it, or to ask again); then "Background tasks that were cut off:" and "Subagents that
   were cut off:" (kind, description, id; a background subagent's agent id), then: check the
   state of their worktrees and outputs before trusting anything they did, resume a subagent by
   sending a message to its agent id, re-run a background command that did not finish, do not
   re-run anything destructive without checking; then why the daemon restarted. The counter is
   set to 1 before anything runs and only a turn that finishes with no background task left
   running resets it (a task that restarts the daemon, an install run in the background, is not
   picked up again on every start). A turn that restarts or crashes the daemon is therefore
   re-entered at most once, with the note telling the model to re-check a command that could
   affect the daemon itself before running it again. The continue waits until the daemon has connected and the server has had a few seconds to answer
   the hello: that answer is where a session stopped while the daemon was down is killed, and
   where its open asks are named. With no server, nothing is continued.

A failed adoption deletes the record and reports `session_ended` with a non-zero exit code once
the daemon is connected, so the row ends with a reason instead of flapping.

Because the claim precedes the connection, the hello already lists the claimed ids and the
server's existing reconcile keeps (or revives) their rows. The daemon asks for an immediate
heartbeat as soon as it has connected (the client kicks one right after the hello) and again
when hosting is done (`AdoptAgentSessions`' callback), so each row goes to its real state
(`idle`, or `running` for a continued turn) at once rather than at the next 30-second heartbeat.
The reconcile path sets it without an unread badge or a push.

## The server

- **A desktop daemon's disconnect waits out a grace period** (20 seconds) before anything is
  marked. Marking the sessions `error` the instant the socket closed told everyone watching
  that they had ended, a second or two before the daemon reported them alive again, and some
  watchers act on it for good: the board comments "Session ended in error" on the card, revokes
  the session's board token and stops following it; a broker reading `/result` sees `terminal`.
  With the grace period a daemon that is back in time was never away. One that is not is
  handled exactly as before, 20 seconds later. A cluster pod is not held: its sessions go to
  `disconnected`, which is not an ending.
- **A message typed in the browser during the grace period is kept** and handed to the daemon
  when it registers again. If the daemon does not come back, it is dropped and the chat shows
  it against the session's `error` status, as before.
- **A stop made while the daemon was away is honoured.** Today a hello that lists a session
  revives its row whatever ended it. A row that is terminal with an end reason of
  `stopped_by_user`, `stopped_by_agent` or `auto_stopped` (or `stopped` after `daemon_lost`:
  cleared from the app after the sweep had ended it) is no longer revived: the server sends
  the daemon `kill_session` for it instead (after the hello's registration, and from a
  heartbeat). `daemon_lost` and `daemon_unreported` stay revivable, as they are today for a
  laptop that slept.
- **A disconnect that has been overtaken does nothing.** If a newer connection for the same
  daemon has registered by the time the disconnect is processed, neither the daemon nor its
  sessions are marked and browsers are not told it left.
- **A message that cannot be delivered is refused.** `send_message` (HTTP and MCP) with no
  daemon connected for the session: a `disconnected` cluster session resumes as before; a
  session that is live, or was marked lost by its daemon's disconnect, answers 503 with
  `Retry-After`, "the session's host is not connected"; any other session has ended and
  answers 409.
- **A session a restart left waiting is marked.** The daemon's `session_paused` (reason
  `restart`) sets `sessions.paused_reason` (migration 043) on its own session that has not ended,
  whatever its status (the row may still read `running` from before the restart), and is
  broadcast as `session_paused_changed`; the sessions list carries `paused_reason` while the
  session is `idle` or `waiting`, and the next `running`, an end or a revive clears it. So does a
  message accepted for the session (forwarded or held), with a `session_paused_changed` of "",
  so a second tab or a reload cannot resume it twice.
- **A message for a session whose daemon is away is held, never taken as an answer.** The browser
  and API paths offer a message to the session's open ask only while its daemon is connected:
  with the daemon away the `blerg-runner ask` is dead, and the held message reaches the session
  hosted again, where it replaces recovery's continue.
  The session's open asks are closed then: the `blerg-runner ask` that waited for their answers
  died with the restart, and a message the person sends must reach the agent instead of
  answering an ask nobody reads. Their cards show the waiting "?" with the tooltip "paused by a
  restart" (a banner count with a "Resume all" button was tried and removed for 0.2.0; it may
  come back in a better form).
- **A daemon is told which of its sessions have a question open** (`open_asks`, after each
  hello), for the continue rule above.
- **A daemon that does not have a session it is asked to message says so**, with a retryable
  `error` event in that session's transcript, unless the session is a terminal one or its spawn
  is still being prepared (those are dropped silently, as they always were).

## What a restart costs, after this

| | Before | After |
|---|---|---|
| The session | ended, cannot be resumed | continues |
| The Claude conversation | lost | resumed |
| The turn in flight | lost | cut off; the model and the person are told, and it is continued once unless it was waiting for the person |
| Messages queued in the daemon's memory | lost silently | lost |
| A browser message sent while the daemon is down | dropped | delivered when it returns (within 20 s) |
| A contract message sent while the daemon is down | answered ok, dropped | refused, retryable |
| What the board and brokers see | the session ended in error | nothing, if the daemon is back within 20 s |
| The session token | revoked after 30 minutes | kept, if the daemon is back within 30 minutes |

Updating the daemon from inside a desktop session becomes: the session runs the installer, its
turn is cut off as the daemon stops (the installer's own later steps do not run: it is killed at
its restart line, which is its last meaningful one), the new daemon adopts the session and
continues it once, the model knowing it was interrupted and told not to run the install again. The
installer asks first when the restart would cut work off (`blerg-runner-daemon status
--open-work`, from the records); the asking session's own turn is left out of that list, and an
agent runs the installer with `--yes` (it has no terminal to answer the question).

One restart is not covered: the one that installs this change. The old daemon wrote no records,
so the sessions running at that moment end as they do today.

## Known limits, not addressed here

- A daemon away for longer than the grace period has its sessions marked `error`, and the board
  then writes a card's session off for good (its board token revoked) even though the daemon
  will host it again when it returns. An unattended board session in that state is still
  continued once. Teaching the board that "daemon disconnected" is not an ending is a separate
  change.
- A session that was idle is unaffected by how long adoption takes, but one whose plugins take
  minutes to reinstall answers nothing until they have (its messages wait).
- The per-turn engine (`BLERG_CLAUDE_STEERING=0`, or an old CLI) has no fallback for a resume
  the CLI refuses; adoption only presets an id whose transcript exists.
- A daemon away for more than 30 minutes returns to a row already finalised: it is revived, as
  today, but its session token has been revoked.
- A downgrade leaves `agents/*.json`, with session tokens, in the state directory.
- `open_asks` is best effort: it is sent after the hello's database round trips, dropped (and
  logged) if the daemon's send buffer is full, and must arrive within the continue's few seconds.
  Only a record from an older daemon (no `open_calls`) depends on it, for one restart.
- `session_paused` is sent once; if the connection drops at that moment the row is not marked.
- A session whose row was deleted (not stopped) while the daemon was down is hosted again
  unseen. The server cannot safely tell "no row" from "row not created yet" for a reported id,
  so it sends no kill for one.
- Killing a leftover engine does not reach the commands it had started; under systemd the
  control group takes them.

## Tests

- Record: round trip, 0600 in a 0700 directory, the never-recorded fields absent, a foreign or
  misnamed file refused.
- Lifecycle: a Claude Code spawn writes one; restricted, grant, sandboxed and non-Claude
  sessions write none; a changed Claude session id, a model change and turn start/end update it;
  frozen, nothing is written; kill deletes it; `kill_session` for an unhosted id deletes it.
- Adoption (with the fake `claude` the daemon tests use): `--resume <recorded id>` when the
  transcript exists and not otherwise; no `session_started`, no initial prompt; recorded model,
  effort and interaction; the note reaches the engine once and never the `user_message` event;
  the rule on its own (`TestAdoptPlan`: every cut-off case continues except an interactive
  session waiting for the person, including a record from an older daemon); an interactive
  session cut off mid-run in its own tool call, mid-generation, or from an older record
  continues once, the notice naming the call and what it ran; one waiting for the person (status
  `waiting`, an open `blerg-runner ask`, or an ask the server reports for an older record) waits
  and is reported idle and then `session_paused`; a session cut off in a deny-listed command
  (`TestRestartDenyList`: matches and non-matches on the whole command) pauses, unattended too;
  a message held by the server and delivered at the hello prevents the pause; a session cut off
  before its conversation is not paused; a cut-off session continues once and not twice; a
  missing workspace or a changed driver deletes the record and reports the end.
- Leftover engine: a live process with the recorded start time is terminated; a live process
  with another start time is not.
- Claiming: claimed sessions are reported before they are hosted; a message for one is held and
  delivered; a stop for one means it is never hosted. The records lock is exclusive.
- Server: a daemon back within the grace period leaves its sessions unmarked and gets the held
  message; one that stays away has them marked a grace period later; a cluster pod is not held.
  A hello listing a session stopped by its person does not revive it and sends `kill_session`;
  one listing a `daemon_lost` session revives it; `send_message` answers 503 for a session
  waiting for its daemon and 409 for an ended one; an overtaken disconnect marks nothing.
  `session_paused` from the session's own daemon sets `paused_reason` on an idle session (not on a
  running one, nor from another daemon), closes its asks and not its notes, and is broadcast
  once; a row still `running` is paused too and shown once idle; running again, an end and a
  revive clear it; `open_asks` names the sessions with an ask open. A message for a session whose
  daemon is away is held and leaves its ask open (browser and API); a message accepted for a
  paused session clears the pause and is broadcast once.
- Web: a paused card shows "?" with "paused by a restart"; the store follows
  `session_paused_changed` and drops the reason when the session runs.
- The real CLI, opt-in (`BLERG_REAL_CLAUDE=1`): a session is killed mid-tool, adopted by a second
  host, and the resumed conversation remembers the first and knows it was interrupted.
