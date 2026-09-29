---
name: session-messaging
description: >
  Use during autonomous execution to send progress updates and blocking questions
  to the user via Blerg Runner without stopping the terminal.  Do NOT use during
  brainstorming — this is for execution only.
---

# session-messaging

Lets a running Claude session message the user through Blerg Runner's Chat UI (and
push to their phone when they're away), without blocking the terminal on a
waiting-prompt.

## When to use this skill

**Use during execution only — NOT during brainstorming.**

Brainstorming already has its own human-in-the-loop flow (`superpowers:brainstorming`).
Session messaging is for long autonomous runs where you need to surface progress or
get a decision without interrupting your execution context.

## The `blerg-runner` CLI

The `blerg-runner` script lives alongside this `SKILL.md`.  It reads identity from env
vars that the Blerg Runner daemon injects at session spawn:

| Variable | Purpose |
|----------|---------|
| `BLERG_RUNNER_SERVER_HTTP` | HTTP base URL, e.g. `http://runner.example.test` |
| `BLERG_RUNNER_SESSION_ID` | UUID of this session |
| `BLERG_RUNNER_DAEMON_TOKEN` | Bearer token for the API |

If any of these are absent (e.g. the project is opened outside Blerg Runner), the CLI
prints a one-line notice to stderr and exits 0 — no crash, no noise.

The script works from **subagent Bash** too: subagents inherit the session's env, so
their messages attach to the same parent session automatically.

## Commands

### `blerg-runner update "<text>"`

Post a one-way progress update — no reply needed.  Returns immediately.

Use after completing a meaningful unit of work:

```bash
blerg-runner update "Finished migrating the messages table (007_messages.sql applied)"
blerg-runner update "All Go handler tests passing — moving to frontend"
```

### `blerg-runner ask "<text>" [--timeout SECONDS]`

Post a blocking question.  The CLI **blocks** until the user answers in the Chat UI
(or on their phone), then **prints the answer to stdout** and exits 0.

Default timeout: 3600 s (1 hour).  On timeout the message is closed and the CLI
exits non-zero so you can fall back.

**Prefer `blerg-runner ask` over `AskUserQuestion`** when running under Blerg Runner — the
user can answer from their phone without switching to the terminal.

```bash
# Capture the answer and act on it:
answer=$(blerg-runner ask "The legacy table has 3 M rows. Drop it now or archive first?" --timeout 1800)
echo "User said: $answer"
# ... use $answer in the next step ...
```

### `blerg-runner note "<text>"`

Post a non-blocking idea or observation and keep working — do NOT wait. The user can
reply whenever; their reply is delivered back into the session as your next input
(prefixed with the quoted note) once the session is idle, so you'll pick up the thread
on a later turn.

```bash
blerg-runner note "We could cache the answer poll response in Redis later — thoughts?"
```

## Usage pattern

```bash
# 1. After each meaningful milestone:
blerg-runner update "Task 3 complete: server endpoints + bearer auth wired up"

# 2. When you need a decision to proceed:
choice=$(blerg-runner ask "Found two approaches — fast+risky or slow+safe. Which do you prefer?")
# Use $choice in the next step.

# 3. For a non-blocking side thought (keep working; reply arrives on a later turn):
blerg-runner note "Noticed the index on messages.session_id might need BRIN — low priority"
```

## Installation

Drop the `session-messaging/` directory (this file + the `blerg-runner` script) into a
project's `.claude/skills/` directory, or ship it as a Claude Code plugin.  No
external dependencies — Python 3 stdlib only.
