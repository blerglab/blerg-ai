---
name: test-driven-development
description: Develop and fix Blerg code with layered tests, deterministic seams and focused verification.
---

# Test-driven development

Prefer this skill over superpowers TDD in Blerg. Follow `docs/testing-audit.md` for layer definitions and evidence. Budgets below are development defaults, not gate policy.

## Choose the lowest sufficient proof

Higher layers prove boundaries lower layers cannot; do not repeat every policy case through real services.

1. Contract: test the interface with fakes, covering null/0, failure, known bad input and success. A library ships its own fake and runs the same contract suite against fake and real implementations.
2. Implementation: test the subject without separate real dependencies. Real filesystem, kernel or browser work belongs here when it is the subject; fake unrelated services.
3. Cross-service: 3a uses provider-owned canned fakes with real clients, transport and serialization; 3b uses real services, known state and isolated fixtures. SQL, migrations and service integration belong in 3b.
4. End to end: exercise shipped components through public UI, CLI or API across a complete installation.
5. Staging: verify deployed behavior on real hardware, including operations and upgrades in a disposable environment.

## Determinism and budgets

- For policy cases, put seams around OS services: fake time, threads/scheduling, process observation and command execution. Keep real OS proofs when the kernel/runtime is the subject.
- Negative transport tests must fake every fallback path, even when expecting an earlier refusal. A regression must fail an assertion, never reach a real Git host or other external service through an unfaked fallback.
- Never sleep in tests. Advance fake time or await an explicit completion signal. Bounded deadlines guard failure; they never signal progress.
- Compositor streams can replay the preceding capture’s cached pixels. Cross a current-composite boundary before recording; keep witness correlation and blank-frame checks strict rather than discarding unmatched frames.
- For React polling, complete the initial response inside `act` before advancing fake time. Rendered content can precede the passive effect installing its timer.
- For cached authorization expiry, inspect the first committed render after rebinding or route return with a layout-phase observer. A passive purge can hide a stale-data flash from final DOM assertions; display cadence must not extend an authority deadline.
- Starting budgets: layer 1: 1 s; 2: 10 s; 3a: 30 s; 3b: 2 min; 4: 5 min; 5: 15 min. Measure and report actual suite times. Adapt to the project: pure functions and simulations have different costs. Report overruns; fix the seam/scope or change a justified budget openly. Never silently extend waits or drop coverage. Run [[test-timing]] to check whether a suite's own time is growing, not just whether it is within budget today.

## Real paths and evidence

- Every security/authorization guard needs a test that fails when it is removed. Remove it to prove that property.
- Exercise the path people use with real client shapes, transport and serialization. A passing renderer can still be missing from the timeline, tab or route.
- Render controls before lazy-loaded neighbors. Entry paths must supply shared styles/tokens without depending on another chat or dialog opening first.
- Use boundary fixtures: version 0, empty lists and real error payloads, not only happy middle values.
- Confirm required services and settings before trusting red or green. An environment failure proves neither behavior nor a flake; flake claims need repeated-run evidence.
- Assert the property, not timing or implementation assumptions. Build the shipped artifact when packages/imports change.

## Red, fix, regression, region

- Write the smallest behavioral test first and observe the intended missing behavior. Compilation errors and broken fixtures are not red proof.
- Make the smallest implementation change; rerun affected tests.
- Mutate the fix or temporarily undo it. Observe the regression fail for the intended reason, restore the fix and rerun. Never deliver the mutation.
- Run targeted checks during work and expensive proof once, last. Never run heavy suites together on a shared machine/database.
- Before reporting, run the whole touched region, its other tests and affected consumers, plus applicable lint, types and builds. Avoid unrelated regions; widen for shared contract changes. Report exact coverage, failures and skips without claiming the full final gate ran.
- New session routes need a visibility decision in `runner/internal/server/privacy_guard_test.go`; run that test by name.
- If a fix round introduces regressions, stop and redesign the boundary/ownership before another patch round. Keep reproductions as constraints.

## Failures and learning

Log every failure cause using shared HTTP middleware and safe typed diagnostics. Never log raw errors, command output, bodies, queries, credentials, card contents or workspace paths. Rate limit repeats with a suppressed count; prove privacy with a canary secret.

When a failure teaches a general lesson, update this skill in the same change.

Read AGENTS.md or blackboard phase; apply [[product-phase]] cadence and risk rules.
