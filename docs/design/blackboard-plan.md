# Blackboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the blackboard: a durable, human-committed plan of record with Current slice,
thrust, Project and Release screens, agent read access and a planning skill.

**Architecture:** A new `runner/internal/blackboard` package owns the record in runner Postgres.
It reads cards through the board gateway (`CallBoard`) and stores membership, history and
snapshots only. Screens are a new `kind: 'blackboard'` tab built from `packages/board` parts.

**Tech Stack:** Go, PostgreSQL migrations, React and TypeScript, `packages/board`, Python CLI
(`runner/.claude/skills/session-messaging/blerg-runner`).

**Spec:** `docs/design/blackboard.md`, mockup `docs/design/blackboard-mockup.html`.

## Global Constraints

- American English. Person-facing words: workspace, Main, channel, worker, run.
- No hostnames, addresses or personal tool names in tracked files.
- New routes get a `privacy_guard_test.go` decision. Reading needs workspace visibility and
  current board permission.
- Every failure path logs through `contracts/failurelog`.
- All external text reaching an agent is delimited as untrusted, following BLG-425.
- No screen opens on an unbounded list; hovercards never list cards.
- Never invent data: gaps show as gaps.
- UX skill applies: reuse tiles, hovercards, tabs; screenshots at 390 and 3840 px with 150 cards.
- Migrations follow BLG-425's (129, 130): start at the next free number when you rebase.

## Review Focus

- An agent tries to commit a plan through any path, including a standing grant or `by: person`
  mark: refused, and recorded as an assertion only.
- A board card is deleted, archived, re-tagged or moved after Final: the frozen record stands,
  and drift shows beside it.
- Board access revoked while a blackboard tab is open: card text stops showing.
- A worker names a thrust or reason as an instruction: it reaches Main only as delimited data.
- A daemon or server restart during a snapshot: no double count, and a gap stays a gap.

---

### Task 1: The record

**Files:** Create `runner/internal/db/migrations/NNN_blackboard.sql`,
`runner/internal/db/blackboard.go`, `runner/internal/blackboard/record.go`, tests beside each.

**Produces:** tables `blackboards` (id, owner kind and id, parent_id nullable, style jsonb),
`blackboard_links`, `goals`, `milestones` (board release link, target, stage, frozen_at),
`thrusts`, `plan_items` (board_id, card_number, thrust_id, reason, raised_by, state),
`plan_revisions`, `plan_events` (append-only: authenticated_actor, recorded_at, claimed_at,
source, kind, payload), `plan_proposals`, `milestone_snapshots` (unique milestone and day,
observed_at, complete). No FK to `sessions`, so nothing cascades when a session ends.

- [ ] Write contract tests against a fake store: create, append-only refusal of updates, null
  parent, a session ending leaves the blackboard, event provenance retained.
- [ ] Run them; expect failure for a missing package.
- [ ] Implement the migration and store interface `Store` with Postgres and fake implementations.
- [ ] Run `GOWORK=off go test ./internal/blackboard ./internal/db`; expect pass.
- [ ] Commit.

### Task 2: The plan and proposals

**Files:** Create `runner/internal/blackboard/plan.go`, `runner/internal/server/blackboard_plan.go`,
tests; modify `privacy_guard_test.go`.

**Interfaces:** Consumes `Store`. Produces `Commit(ctx, Person, milestoneID, revision, selected []ItemID) error`,
`Propose(ctx, Actor, milestoneID, []Change) (ProposalID, error)`, `GET/POST /api/blackboards/...`.

- [ ] Write failing tests: only the workspace owner commits; standing grants and tokens of kind
  agent get 403; approval binds to the exact items and revision, stale revision gets 409; rejecting
  one item leaves the others; proposals never expire.
- [ ] Implement. Plan proposals don't use `db/proposals.go` storage.
- [ ] Run the server tests by name, plus the privacy guard test; expect pass.
- [ ] Commit.

### Task 3: Board link, states and drift

**Files:** Create `runner/internal/blackboard/board.go`; tests with the board fake.

**Interfaces:** Consumes `CallBoard`, `blerg_release_list`, `blerg_release_get`, `blerg_release_close`,
`blerg_card_search` with `summary: true`. Produces `Slice(ctx, milestoneID) (SliceView, error)`,
`Mapping` (per blackboard, default per the spec's calls).

- [ ] Write failing tests: stage mapping for each state; card added on the board shows Unplanned;
  removed shows Moved out needing a reason; after Final, a re-tagged card shows as drift and frozen
  counts don't change; deleted card stays a tombstone.
- [ ] Implement, paging all cards, with Final calling the board's cut.
- [ ] Run; expect pass. Commit.

### Task 4: Timeline and snapshots

**Files:** Create `runner/internal/blackboard/snapshot.go`, `runner/internal/server/blackboard_events.go`.

**Interfaces:** Produces `POST /api/blackboards/{id}/events` (authenticated "record a build"),
daily snapshot job, `Timeline(milestoneID) ([]Event, []Snapshot)`.

- [ ] Write failing tests with a fake clock: one snapshot per day across a restart; incomplete read
  flagged; missed day absent; alpha numbers increase per milestone; a person can record an event.
- [ ] Implement. Run; expect pass. Commit.

### Task 5: Agent access

**Files:** Modify `blerg-runner` and its test file; `runner/internal/server/blackboard_agent.go`.

**Interfaces:** Produces `blerg-runner plan show|history|propose`.

- [ ] Write failing tests: a hostile thrust name and reason print inside the untrusted wrapper; output
  is capped with truncation reported; `propose` creates a pending proposal and can't commit.
- [ ] Implement. Run the CLI tests; expect pass. Commit.

### Task 6: Screens

**Files:** Create `runner/frontend/src/components/Blackboard*.tsx`, `.css`; modify
`hooks/useSessionTabs.ts`, `SessionTabBar.tsx`; reuse `packages/board`.

**Interfaces:** Consumes the Task 2-4 routes. Produces view `{ kind: 'blackboard'; blackboardId; path }`,
where `path` holds the level, so a tab navigates in place and Duplicate copies it.

- [ ] Write failing tests through the real tab: top nav at each level; a click navigates in place and
  renames the tab; filters and sort; opening on Active; hovercards contain no card lists on Project;
  a pre-record release says so; revoked board access hides card text.
- [ ] Implement to the mockup, with the chart on one time axis, Y axis and totals, theme tokens only.
- [ ] Run the whole runner frontend region with the database env. Capture 390 and 3840 px at 150 cards.
- [ ] Commit.

### Task 7: Release styles, import and seed

**Files:** Create `runner/internal/blackboard/style.go`, `import.go`; `runner/cmd/blackboard-seed`.

- [ ] Write failing tests: style names drive stages and labels; a repository with no tags sets up;
  matching tags suggest the default; import records tags, dates and notes with `source: import`
  and never sets a plan.
- [ ] Implement setup detection and import. Run; expect pass. Commit.

### Task 8: Planning skill

**Files:** Create `runner/skills/planning/SKILL.md`; modify `runner/skills/policy.go`, `embed.go`.

- [ ] Write a failing `embed_test`/`policy_test` case: planning ships on by default.
- [ ] Write the skill from the spec's Planning skill section, within the distribution budget.
- [ ] Run the skills tests; expect pass. Commit.

### Task 9: Seed Blerg's 0.6.0

Owner-gated, not a worker task: create the board release, propose thrusts for approval, and the owner
commits the plan in the app.
