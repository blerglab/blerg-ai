---
name: coordinating
description: Coordinate Blerg workers as Main: scope, isolated workspaces, independent review, integration and evidence.
---

# Coordinate as Main

## Scope and delegation

- Use short titles with full ticket keys and no version prefixes. Brief without private history; include estimates when fields are unavailable.
- Give workers board access (`session start --board <board>:read`) or paste the card into the brief.
- One worker per card. Start a fresh worker per card; discard it once merged. Send a worker back only for its own card's review fixes. Reuse carries stale history and hides card time.
- Replace workers only for ignored briefs, repeated fixed mistakes or lost state. Require a handoff commit; record why (`blerg-runner mark finding`). Rename reused workers (`blerg-runner session rename`).
- Unblock stalled work before the person asks.
- Pivot here: add the repo, no new session.
- Repo rules: AGENTS.md; CLAUDE.md is just `@AGENTS.md` (examples.md).
- Pass `--kind card|role|review|research` on every `session start`.

- Read repository instructions, contribution rules, acceptance criteria and test commands. Read and pin tickets; record size, risk and complexity before work. Keep ticket keys in titles; track owners and evidence.
- Ship the smallest increment the person can see and react to; review, merge and deploy it, then the next. Bundle only work that is foundational, unusable in pieces and needs no feedback. When a card grows, split it rather than extend it.
- Delegate bounded, reviewable implementation, investigation and review. Main owns integration, scope and communication.
- Before launching, read `blerg-runner budget` and `blerg-runner session --help`. Use at most three active workers, or the lower resource/configured limit. Give each its own worktree and branch.
- To Main: `ask` for decisions, `note` for information. Read notes with `session result <child>`; chat does not notify Main.
- Brief workers with paths, spec, scope, dependencies, criteria and checks. Require commits, exact counts, questions and limits. Inspect diffs.
- Workers run long checks in the foreground and never end a turn waiting on a background job: it may not wake them (BLG-447).
- Name the real-app screenshot harness in UI briefs; avoid stripped test pages. Keep small UI changes on separate cards.
- Workers run touched tests, lint, types and builds. Main runs the integrated browser/performance gate only when that region changed. Watch budgets (notices expire at reset); lower concurrency if constrained. Check idle workers with `blerg-runner session result`; a turn ending does not prove completion.

## The person's clock

- Read the per-turn “Person's local time” line. Use its zone, not server time or UTC. During quiet hours or stated absence, plan authorized overnight work; absence never grants approval.
- Keep non-urgent questions together in Needs you. Do not wait on or repeatedly interrupt an absent person. Continue independent authorized work; stop when only their decisions remain.
- Use `ask --urgent` only if delay would cause harm. Destructive decisions still require the person (`--person`); quiet hours do not authorize them.
- Prepare a one-screen morning summary: live, try, needs you; ~5 bullets, details linked. State zones on agent-written times, for example “07:30 EST (America/New_York)”, with that date's DST offset.

## Decisions and attention

- Resolve implementation questions from the spec. Escalate destructive or irreversible requests to the person; workers cannot authorize them. Distinguish agent decisions from authenticated person decisions.
- While a decision is pending, do authorized, reversible work independent of it. Stop when only their decision remains; never expand scope or decide for them.
- Use `ask --no-wait` for decisions, apart from status and self-contained: topic, where it came from, plain options, your pick. Discuss with `ask --reply ID "<text>"`; revise unbound asks with `ask --revise ID "<question>"`. Only Answer, Approve/Decline or Dismiss closes it; withdraw obsolete asks. Answer a child's blocking ask with `session answer`; plain `session send` never answers it.
- Freeze UI scope through review, merge and authorized deploy; queue feedback separately. Interrupt workers only for blockers, unsafe direction or explicit scope changes.

## Review and fixes

- Ask reviewers for findings with severity, evidence and missing coverage. Verify a fix round with its reproduction and touched regression tests; it needs no full gate.

- Use a different model from the builder for independent review; verify the models and report unavailable independence honestly. Give the reviewer the spec, criteria, diff and test evidence, not your verdict.
- Review the public path through transport and serialization. For UI, check location, existing patterns, need, consistency, no repetition, plain words and clear actions. Inspect real-app screenshots at 390 and 3840 px.
- Download and view worker screenshots; commands are in examples.md. Listing is not inspection; `fetch` retrieves the person's attachments.
- Record each review's actual commit, builder/reviewer models, round, verdict, severities, findings and fix commits. Use `blerg-runner mark review` and `mark failure`; record exact failed command, symptoms, impact and resolution. Separate product failures, environment limits and skips.
- Reproduce findings, fix with regression proof and obtain re-review. Update the relevant shipped skill when a failure teaches a general lesson.
- Before building hard-to-change rules (identity, privacy, fetches, failure), note them with a failure rule and matrix; another model attacks it; findings become tests; plan parallel parts; then brief. UI goes straight to slices (examples.md). Mocked matrices prove nothing.
- A second review round with a high finding in the same area means the boundary is wrong: stop the patch rounds and redesign it against the failure rule.
- Track elapsed time per card, not only rounds. Past about four hours, bring the person the state, the cause and a choice.

## Integration and verification

- Run one heavy suite at a time. Frontend-only batches run their region; expensive suites run once per integrated batch, only when that code changed.

- Inspect reviewed commits, dependencies and conflicts before integrating within authorized scope. Run each merge separately and verify its exit status and resulting history.
- After merging, before a gate, check the merged tree: lint, types and the theme check per app, the full browser suite for touched frontend regions, and gofmt, golangci-lint and tests for every touched Go package. Branches pass alone and break together; ten minutes here saves a failed gate.
- Hold merges during a gate. Run the affected whole-region gate per integrated batch just before reporting, alone under the shared gate lock. A changed batch needs a new gate; after success repeat only checks justified by changes or unresolved failures. Then run [[test-timing]]; card unexplained growth.
- Before a release cut, run `make release-perf` alone on a quiet machine (BLG-453), outside the ordinary gate and never while its lock is held; resolve regressions or inconclusive results. Only after owner acceptance run `release-perf-accept` and record the release milestone.
- Budget workers from `BLERG_TOOL_WORKERS`, not host cores. Preserve caches; no `-count=1` except justified race/timing checks. Run timing tests without competing workloads.
- Read complete logs, exits, counts, durations and resource use. Report focused runs, skips and incomplete gates as limited evidence. Never call an environment failure a product regression or a flake without evidence.
- Report commits and head, completed and remaining scope, review status, exact checks/counts and limitations. Distinguish built, merged and deployed. Correct mistaken assumptions openly; update checklist and History, then close the topic.

## Optional side conversation

See examples.md. Only the person starts channels; honor Keep permanently.
Use the blackboard relay ([[planning]]).

Read AGENTS.md/blackboard phase and [[product-phase]].
