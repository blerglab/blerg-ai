# Mid-turn steering for Claude Code agent sessions

Status: design v2, 2026-10-01 (revised after two adversarial reviews). Scope: the Claude Code engine driver
(`runner/internal/daemon/claudecode.go`, small helpers in `sandbox.go`) and the chat view. Other engines are
untouched.

## Problem

A message sent while an agent is working waits for the **whole turn** to end. The driver runs one
`claude -p "<text>"` process per message and only then starts the next, so a long turn blocks every message
behind it. Esc is the only way in, and it kills the turn. People steer an agent mid-turn constantly; the
terminal Claude Code does this natively and ours should too.

## What the CLI does (verified by spikes and by an independent reviewer, Claude Code 2.1.286)

`claude -p --input-format stream-json --output-format stream-json --verbose --replay-user-messages` keeps one
process reading one JSON user message per stdin line.

| Scenario | Observed |
|---|---|
| Messages one after another when idle | Same process, same `session_id`; each turn = `system/init` … `result`. |
| Message written while a **tool call** runs | Held until the tool finishes, then delivered at that step and folded into the **same** turn (one `result`). |
| Message written during **pure text generation**, or within ~1 s of the first message | Not folded: the turn finishes (`result`), then a new turn starts (`init`, replay, `result`). So delivery "at the next step" only happens when the turn has a tool step. |
| Replay line | `{"type":"user","message":{"role":"user","content":"<string>"},"isReplay":true,"uuid":"<our uuid>",…}` emitted when the CLI **consumes** the message, ~1–1.7 s **after** the stdin write (the turn's `init` comes first). The `uuid` we put on the stdin line is echoed back. Tool results are also `type:"user"` but have array content and no `isReplay`. |
| Slash commands (`/model`, `/cost`, `/clear`, `/compact`, …) | Interpreted by the CLI (as with today's `-p`). Some produce no replay or rewritten replays; `/clear` mints a **new** `session_id`; with `--disable-slash-commands` (restricted sessions) they are just text. Empty text replays as `(no content)`. Newlines, unicode, JSON-looking text and 200 KB text replay exactly. |
| `control_request {interrupt}` | Always answered at once with `control_response success`, **before** the tool-result/`[Request interrupted…]` lines and the `result`. Mid-tool, mid-text and double interrupts all end the turn with `result error_during_execution is_error:true` and keep the process alive. If a message is queued it then starts its own turn. At idle: only the `control_response`, no `result`. Sent in the 1–1.7 s gap before the replay, it still cancels the message's turn. |
| Close stdin mid-turn (even with an unconsumed queued message) | The turn(s) finish, including the queued message, then exit 0. |
| SIGTERM/SIGKILL mid-tool, then `--resume <sid>` in stream mode | Process exits non-zero with no `result`; resume works, same `session_id`, context kept, the model notes the killed command. |
| Bad `--resume` id | A non-JSON line "No conversation found…", `result error_during_execution`, no `init`, exit 1 within ~1 s. |
| Two live processes resuming the same session | No complaint; they fork silently (separate contexts, one interleaved transcript file). |
| Unknown flag | `error: unknown option '…'` on stderr, exit 1, no stdout. |
| `result.usage` | Per turn. `total_cost_usd` is cumulative and unused. |
| Hardening flags (`--tools=…`, `--strict-mcp-config`, `--disallowedTools=…`, `--setting-sources=`, `--append-system-prompt`) | Enforced identically in stream mode. |
| Idle process | ~230 MB RSS, little CPU; still answers after minutes. |

## Design

### Per-session process, explicit state machine

A `ccProc` struct per process: `cmd`, `stdin`, `done` channel, `gen`, flags `intentionalClose`,
`killedByDriver`, `sawInit`. Driver states under one mutex: `none | starting | live | closing`. A new
process is started only when the previous one's `done` is closed (a replacement that overlapped its
predecessor would fork the session). The mutex is never held across I/O.

Argv = today's `ccTurnArgs` minus the `-p <text>` prompt, plus `-p --input-format stream-json
--replay-user-messages` (`ccSessionArgs`; `ccTurnArgs` stays for the legacy path). Model, effort, `--resume`,
MCP grant flags, hardening flags and the session guide are fixed for the life of the process.
`mcpFile.Ensure()` runs when a process starts (not per message); an error there fails the message with an
error event, as a failed turn does today.

### Messages: one queue, one writer, uuid correlation

* The queue becomes mutex + slice + wake channel (push-front is needed). `Enqueue` stays non-blocking and
  caps queue+awaiting at 64 (queue-full error as today).
* Run loop: take a message, ensure a process, then **append to `awaiting` (keyed by a fresh uuid) under the
  mutex, then hand the JSON line (`{"type":"user","uuid":…,"message":{"role":"user","content":text}}`) to the
  process's writer goroutine**. One writer goroutine per process owns stdin (a blocked pipe never blocks
  `Run`, `Interrupt` or the mutex). A failed write removes the entry and counts as a process failure.
* **When the driver is idle at write time it emits `status_changed running (reason "turn")` immediately**
  (today's behaviour at dequeue), so the UI/server show busy during the 1–1.7 s write→replay gap and Esc is
  meaningful.
* The reader matches a replay by **uuid**, never by position or text. On a match: pop the entry and emit
  `user_message{text: stored text, source: stored source}`. A replay with no entry (resume replay, hook
  injection, expired entry) and string content emits `user_message{text: echoed, source: "chat"}`.
* **Settle rule** (replaces the old "watchdog drops"): when `result` arrives with `awaiting` non-empty, keep
  `running` and arm an 8 s timer tagged with (process gen, turn counter). A replay or `init` before it fires
  is the normal "next turn started". When it fires still-unconsumed entries are assumed consumed without an
  echo (slash commands): emit `user_message` for each from the stored text, then idle. **Messages are never
  silently dropped;** a failed message still produces its `user_message` followed by an `error`.

### Reading (one reader goroutine per process)

Same decoding as `runTurn`, with fixes: `message.content` is `json.RawMessage` decoded by shape (string =
replay text, array = blocks); `ccLine` gains `isReplay`, `uuid`, `request_id`. Lines are read with a
`bufio.Reader` (no 4 MB scanner cap; a line over 32 MB is discarded; any reader error kills the process).

* `system/init`: session id + capabilities (every turn). `ccSessionID` is updated on every `init`/`result`
  (`/clear` changes it).
* `init`/`assistant`/replay while not running = an **autonomous turn** (a background task or monitor woke the
  agent): emit `status_changed running (reason "agent turn")`.
* `result`: usage as today; `turn_done`; if `awaiting` empty then `status_changed idle`. Error rule: an
  `is_error` result emits an `error` event as today **except** when an interrupt was requested for this
  turn, where it is `turn_done{StopReason:"interrupted"}` with no error card.
* `control_response`: only cancels the interrupt's kill timer (it arrives *before* the `result`).
* EOF: see failure handling.

### Interrupt

`Interrupt()` acts when `running` **or** `awaiting` is non-empty or a process is `starting`; otherwise it is a
no-op. It records `interruptPending` (expires after 10 s; cleared by the next `result`; suppresses the
error card only if the process emitted `init` this turn, so a bad `--resume`'s `error_during_execution` is
never mistaken for it), arms a 5 s kill timer tagged (gen, turn), and enqueues the `control_request` on the
writer ahead of messages. If the timer fires with the same (gen, turn) still running: kill the process
(`killedByDriver`), emit `turn_done{interrupted}` + idle once, and the next message restarts it. Queued and
awaiting messages are kept.

### Model / effort change, idle shutdown

`SetModel` records values as today and marks the process `stale`. When the driver is idle (not running, no
awaiting, queue empty), a stale process is closed by closing stdin (`intentionalClose`, never counted as a
crash); the check also runs on every transition to idle. An idle process is closed after **10 minutes**
(var, test-overridable); the next message restarts it with `--resume`. Documented consequence: background
tasks started by the agent die at such a restart/shutdown (they used to die at every turn end).

### Failure handling

* Unexpected exit (not `intentionalClose`/`killedByDriver`, context alive): if a turn was running, `error`
  "claude-code exited: …" (existing wording, last non-JSON line quoted) + `turn_done{error}`. Entries still
  in `awaiting` were never consumed: put them back at the front **once**; a message that fails a second time
  gets its `user_message` and an `error` quoting the last noise line. No automatic restart without a message.
  `running` is reset to idle.
* Process exited before any `init`: if the output matches `unknown option`, switch this driver to the legacy
  per-turn path for good and resend the message.
* A driver kill (context cancel/`Kill`) never emits or requeues (checked via `ctx.Err()` first).
* The fallback kill emits exactly one `turn_done`; the EOF that follows is suppressed.

### Kill switch

`BLERG_CLAUDE_STEERING=0` (read once at driver construction) selects the legacy `runTurn` path, which stays
intact. Default on.

### Sandbox

`docker exec -i` forwards stdin. The PID file is per process generation
(`/tmp/blerg-turn-<gen>.pid`); `commandPID`/`interruptPID`/`clearPID` variants are added to `sandboxExec`
and the old per-turn helpers keep their signatures for the other drivers. `clearPID` only removes that
process's file. Interrupt uses the control request; the PID is for the fallback kill only.

### Frontend

No protocol change. `user_message` still appears when the agent picks the message up, and the queued bubble
resolves then. Wording (delivery is "when the agent reaches a step", not guaranteed mid-turn):
composer hint `Working — Enter sends it · Esc interrupts`; queued bubble `Sent — the agent picks it up as
soon as it can`. Also: `turnStartTimes` resets `start` only after a `turn_done` (several folded messages =
one turn), and a mid-turn `user_message` must not reset live tool calls (`liveCalls`) or split a tool group.

## Not doing

* Cutting a tool that is already running (Esc does that). A message waits for the current tool to finish.
* Other engines; protocol/server changes; per-session setting.

## Risks and containment

| Risk | Containment |
|---|---|
| Turn accounting with turns the driver did not start | status from events + awaiting count; autonomous-turn rule; settle timer |
| Message lost / mis-attributed | uuid correlation; never drop silently; requeue-once |
| Interrupt not honoured / races | control request first, tagged 5 s kill fallback, single turn_done |
| Stale timers | every timer tagged (gen, turn), checked under the mutex |
| Session fork | never two live processes for one session; wait for `done` |
| Stale flags after model change | restart at the next idle point |
| Memory | 10 min idle shutdown |
| Older CLI | kill switch + unknown-option fallback |

## Changes after the code review (v2.1)

* Messages and control requests share ONE ordered channel to the process, pushed under the state lock, so an
  interrupt can never overtake the message it was sent for. (v2 had a separate priority channel.)
* An acknowledged interrupt (`control_response`) swaps the 5 s "not honoured" timer for a 30 s "result
  overdue" timer, and a timer only kills a process that is really inside a turn; every `init` bumps the turn
  counter so timers armed for an earlier turn are stale.
* Output of a process being killed is ignored; the kill is SIGKILL (inside the container too), and a process
  that does not exit within 10 s of its stdin being closed is killed.
* The oldest queued message is taken from the queue only after a live process is secured, and the messages a
  dead process never consumed go back to the queue before its exit is announced, so order is preserved.
* A `--resume` that does not exist reports one error, clears the stored conversation id and retries fresh.
* A model change that lands while a process is starting leaves that process stale (generation counter); each
  `turn_done` names the model its process ran.
* The settle timer does nothing while a turn is in progress. PID files are per process and per driver.
* Wording: "Enter sends it to the agent" / "Queued — the agent picks it up at its next step" only for the
  Claude and native engines; the others keep the old text. The turn clock ignores a message that never
  started a turn (idle without `turn_done`).
* Verified, no change needed: the CLI delivers a mid-turn message only after all tool results of the current
  step, so `liveCalls` (which stops at a user message) never loses a running call.

## Test plan

A fake `claude` (Go test helper re-exec'd through a PATH shim) speaking the protocol: reads user lines,
echoes replays with the supplied uuid (after a configurable delay), init/assistant/result, honours
`control_request`, scripted slow tools, slash commands without replay, crash/exit/unknown-option modes,
autonomous turns. Tests (run with `-race`): decode of a string-content replay; two turns one process; mid-turn
fold with one `result`; message in the write→replay gap; interrupt (no error card, `interrupted`, queued
message runs next, flag cleared); interrupt unanswered (kill fallback, one `turn_done`, restart with
`--resume`, never two processes); late `result` not killed by a stale timer; crash with awaiting (requeue
once then user_message+error); slash command with no replay (settle emits `user_message`); two identical
texts; autonomous turn status; model change restarts at idle and not mid-turn; idle shutdown; stdin close
racing Enqueue; kill-switch legacy; unknown-option fallback; bad `--resume`; oversized line; PID file
ownership across restart; `Kill` mid-turn emits nothing. Existing `claudecode_test.go` runs under
`BLERG_CLAUDE_STEERING=0` and a streaming equivalent. Frontend tests for the new wording, `turnStartTimes`
and `liveCalls`.
