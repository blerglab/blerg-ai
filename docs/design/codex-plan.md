# Codex native plans

Codex root-agent plans now populate the same `/live` `plan` object as Claude Code
TaskCreate/TaskUpdate and TodoWrite. The existing panel shows Plan when there is no
explicit Blerg checklist. No frontend change or planning-tool enablement is needed.

The native source is Codex app-server `turn/plan/updated`:
`{threadId,turnId,explanation,plan:[{step,status}]}`. Installed CLI 0.162.1 generated
TypeScript types supplied the schema; synthetic native stream fixtures replay it.
The adapter maps `inProgress` to `in_progress`, forwards only step/status, and omits
an active form because the native schema has none. It never forwards explanation.

The shared durable event is `plan_updated`, with payload
`{items:[{text,status,active?}]}`. Status is pending, in_progress or completed.
Explicit `items: []` clears; absent/null items, malformed or oversized lists, unknown
statuses, blank cleaned text and child markers are ignored. Wire decoding is
bounded to 256 KiB before allocation; at most 100 items and 200 runes per text field
reach the plan. The server sanitizes before persistence and fanout even when a
sender marks the event transient. Generic session privacy applies unchanged.

Native notifications must belong to the current app process, root thread and
accepted active turn. A private turn-start epoch captured by the reader prevents
queued idle or prior-turn events from becoming a later turn's plan. A separate
startup flag retains valid early notifications until the RPC response binds the
accepted turn. Restricted Codex still disables update_plan and ignores its plan
notifications. A subagent's plan cannot replace its parent's.

Migration 058 adds a dedicated latest-snapshot partial index. Replays find the
latest valid snapshot and read only Claude task deltas after it. Normally this
requires one indexed probe; malformed historical rows use keyset fallback under
a ten-second context. The existing migration 055 task index is preserved.
Full snapshots replace previous plans. An unmatched Claude TaskUpdate cannot
address anonymous native rows; a later valid TaskCreate starts a fresh Claude
plan, and TodoWrite remains a full replacement. Sequence order decides precedence.

Tests reproduce the initial missing-plan and transient-validation failures before
the fixes. Independent adversarial tests cover child/stale/idle plans, early native
notifications, explicit clears, malformed updates, source switching, task-ID
isolation, sequence ordering and bounded decoding. Real-Postgres tests verify
private replay, historical invalid fallback and the partial-index query plan.
Validation: the full Runner suite passed in 222.98 seconds; Runner vet, all four
modules’ pinned Go lint, scrub and link checks passed. Full Codex race tests,
20 repetitions of the independent adversarial races, and the final real-Postgres
server/database/live-state/protocol race run passed. Migration 058 is reserved by
this branch. No frontend, messaging CLI or server checklist code changed.
