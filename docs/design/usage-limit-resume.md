# Resume sessions after provider usage limits

## Specification

An agent session may opt into automatic continuation after a provider usage limit.
The runner server owns durable retry scheduling, for desktop and cluster sessions.
Terminal sessions are excluded. Default is off. Existing sessions can enable it.

Only an engine's actual usage-limit refusal schedules a continuation. Account usage
percentages alone never do. Claude and Codex normalize a refusal to a provider and
an optional absolute reset timestamp. Unknown reset times are shown but never guessed.

The session displays its pending reset time and offers cancellation. A retry sends
an explicit continuation message through the normal message/resume contract, preserving
session history and owner authority. It must not answer an outstanding question.

Schedule at least one minute after reset. Serialize dispatch and stagger sessions
sharing an owner/provider. Persist pending state, attempts and cancellation generation
in Postgres. Multiple server replicas must not dispatch the same pending attempt.
Daemon or server downtime delays dispatch until connectivity returns. Cluster pod
replacement uses the existing resume contract, including credential and MCP checks;
never substitute operator credentials for an owner's credentials. If unattended
credential renewal is unsupported, expose a blocked reason rather than bypass checks.

A person sending a message, interrupting, pausing, stopping, deleting or disabling
continuation cancels pending work. A stale refusal from before that action cannot
rearm it. A session doing work or waiting for a question must not be poked. A fresh
refusal after automatic continuation may schedule again, up to three attempts;
then require a person. No silent retry of an ambiguous dispatch after a crash.

Record scheduling, cancellation and dispatch in the session transcript without
provider error prose or credentials. This feature applies equally to desktop and
cluster Claude and Codex agent sessions. Installation documentation must explain
that updated desktop daemons are needed to emit normalized refusal events.

## Acceptance

Tests cover provider normalization, unknown reset, opt-in defaults, replay and stale
events, concurrent workers, cancellation races, offline hosts, server restart,
cluster authorization, bounded retries, and UI/API state. Privacy scrub and normal
Go/web checks pass before shipping.

## Adversarial specification review and resolutions

The first review identified missing authority, delivery ambiguity, stale turns,
expiry, ownership, retry accounting, and provider-detection boundaries.

Only the launcher may opt in, using a verified browser session. Retain its `sid`
server-side as the explicit unattended authority reference, never a bearer token;
core must still validate that session on credential/grant fetch. Revoked/expired
proof blocks a replacement pod and never falls back to operator credentials.

Dispatch is at-most-once per attempt, not exactly-once delivery: commit a unique
attempt before sending. A crash between commit and send can require manual action.
Cancellation serializes on the same session retry row. Refusals carry a turn ID;
cancellation records that turn and delayed refusals for it cannot rearm retries.
Unknown reset never schedules; enabling can arm the most recent refusal only if
it is still the current interrupted turn. Cancel disables the setting. Attempts
count committed automatic dispatches only. A new manual message resets the budget.

Retain resumability for a pending limit retry up to eight days, then block and
return to ordinary expiry. Apply normal privacy rules to all state and transcript
updates. Only structured engine error events qualify, never model prose, account
meters alone, transient HTTP 429s, or generic transport errors.

## Upgrade behavior

The new refusal event is optional for wire compatibility. Already-running older
pods and desktop daemons cannot arm retries until upgraded. An existing session
whose refusal predates the upgrade can enable the setting and send one continuation
to obtain a new refusal signal. Historical free-form errors are not retroactively
interpreted as authorization to run work.
