# Talking to a running agent

How messages, interrupts and pausing work in the chat view of a Claude Code session. (The
engine-specific notes are at the end.)

## Sending a message while the agent works

Press Enter and the message goes straight to the agent; you do not wait for the turn to finish. The
agent picks it up **at its next step**: when the tool call it is running finishes, the message is
delivered and the agent adjusts without losing the turn. Until then it shows as a **Queued** bubble in
the chat, and becomes a normal message of yours once the agent has taken it.

Two things to know:

- A message waits for the tool call in progress. If the agent is in a ten-minute build, your message
  is delivered when that step ends. To cut a running step, interrupt it (below).
- A message sent while the agent is writing a long answer (no tool call involved) is taken after that
  answer, as a new turn.

Several messages sent in a row are delivered together at the next step.

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

## Engine notes and the kill switch

This applies to Claude Code sessions. The other engines (Codex, Hermes) still take one message at a
time: a message sent while they work waits for the whole turn, and the bubble says only "Queued".

Under the hood a Claude Code session keeps one long-lived `claude` process and writes each message to
it as it arrives; an idle session's process is closed after 10 minutes and started again with the same
conversation when you next write. Set `BLERG_CLAUDE_STEERING=0` in the environment of the runner or
daemon to go back to starting one process per message (an older Claude Code that lacks the streaming
input mode falls back to that by itself). The design is in
[`design/mid-turn-steering.md`](design/mid-turn-steering.md).
