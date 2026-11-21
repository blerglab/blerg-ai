---
name: product-phase
description: Scale planning, testing and review to the declared product phase and risk.
---

# Product phase

## Read the declaration

Read the workspace or project's AGENTS.md and blackboard next to the vision before
planning, coordinating or testing. Use a person-set declaration such as
`Product phase: PoC / pre-1.0; release discipline: candidate gates and supported upgrades.`
Main may propose a phase or change; the person sets it. Keep one authoritative
declaration and link to it from the other location. Do not invent a new settings UI
or claim a blackboard field exists. A line in AGENTS.md or the blackboard is enough.

Do not infer phase from version alone. If absent, state that the phase is undeclared
and have Main propose one with evidence; continue authorized reversible work with
focused checks and the security baseline, preserving existing gates. Never silently
declare PoC or downgrade a project's release discipline. Read [[planning]],
[[coordinating]] and [[test-driven-development]] alongside this skill.

## Keep risk independent of phase

Keep secrets, authorization, privacy, data integrity and safe failure always on.
For identity, privacy, auth or money, write the spec first: state the failure rule,
inventory every entry point, parser, token and store, and have a different model
attack the spec before code. Keep adversarial implementation review, negative tests
and mutation proof at every phase. Small audiences do not reduce these obligations.
Follow person-required approvals for destructive changes and external effects.

Scale process and performance work, not security. Honor existing CI, release, legal and contractual gates.
Separate task checks, batch review and release qualification. Explicit release
discipline can require production upgrade tests even for a pre-1.0 product.

## Use the phase

For each phase, choose the smallest scope that proves the next value. Apply the
security baseline above throughout.

### PoC / pre-1.0 with few users

- Scope: prove one user journey or learning hypothesis. Don't boil the ocean.
- Tests and gates: run focused tests per task while building; run full suites for
  the touched region once per batch before review. Rerun affected checks after fixes;
  repeat expensive suites only for a relevant regression, shared boundary change or
  unresolved risk. Do not rerun multi-minute Go suites after every small edit.
- Review: keep one focused implementation review per batch; apply the mandatory
  spec-first and adversarial path above to sensitive changes.
- Browser matrix: use one primary browser for routine task checks; run the supported
  browser matrix once at the batch gate, plus a targeted second-browser reproduction
  when the behavior depends on that engine.
- Migrations and compatibility: keep data recoverable; validate existing records and
  backup/restore for persistent data. Allow disposable resets only with explicit
  authorization. Preserve promised upgrades and public contracts.
- Documentation and planning: keep a short hypothesis, acceptance list, phase line
  and setup/recovery notes. Use small batches rather than a speculative roadmap.
- Performance: measure a visible bottleneck before optimizing.
- Don't do yet: speculative abstraction, broad browser permutations per edit,
  enterprise compliance programs, exhaustive scale tests or premature tuning.

### Beta

- Scope: stabilize the core journeys with representative users and explicit feedback.
- Tests and gates: add regression and integration coverage for real failures and
  persistent data; run the region suite and supported browser matrix per review batch.
- Review: inspect user-facing failure paths, telemetry and migration risks; retain
  adversarial review for sensitive rules.
- Migrations and compatibility: rehearse upgrades on populated beta data, keep a
  recovery plan and communicate breaking changes.
- Documentation and planning: publish known limits, onboarding and short milestones.
- Performance: measure representative data sizes and fix observed latency.
- Don't do yet: unsupported platforms, speculative service splits or certifications
  without an actual requirement.

### 1.0 / GA

- Scope: fulfill a clear support and stability promise.
- Tests and gates: require regression, integration and release end-to-end gates;
  cover supported browsers at batch review and qualify supported upgrades at release.
- Review: inspect compatibility, operations, security and release evidence.
- Migrations and compatibility: version public contracts, test supported upgrades,
  plan rollback or roll-forward, and announce deprecations.
- Documentation and planning: maintain user, operator and recovery documentation,
  support policy, release notes and bounded release plans.
- Performance: set and measure user-facing latency and capacity budgets.
- Don't do yet: unmeasured rewrites or support commitments without demand.

### Scaling

- Scope: remove measured capacity and reliability constraints.
- Tests and gates: add load, concurrency, failure and recovery proofs around observed
  bottlenecks; keep routine task checks focused and full gates at batch/release boundaries.
- Review: inspect ownership, isolation, operational failure and capacity evidence.
- Browser matrix: maintain the supported matrix; expand it using usage and incident data.
- Migrations and compatibility: stage large changes, verify mixed-version operation
  and measure migration impact on realistic volumes.
- Documentation and planning: maintain runbooks, service objectives, ownership and
  capacity plans tied to measured demand.
- Performance: profile before changing architecture; verify improvements and cost.
- Don't do yet: global distribution, sharding or broad rewrites without evidence.

### Enterprise

- Scope: deliver explicit customer, regulatory and contractual obligations.
- Tests and gates: add required audit, tenancy, policy and disaster-recovery evidence;
  automate repeatable controls while retaining focused task checks.
- Review: require appropriate security, operations and compliance expertise for
  changed obligations; preserve independent adversarial review.
- Browser matrix: qualify contracted platforms and accessibility requirements.
- Migrations and compatibility: honor support windows, staged rollout, retention
  and recovery obligations; prove tenant isolation through upgrades.
- Documentation and planning: maintain auditable decisions, operator guides,
  contractual support policies and obligation-based milestones.
- Performance: validate contracted service levels and tenant resource isolation.
- Don't do yet: bespoke controls or certifications unrelated to an actual obligation.

## Flag process that exceeds the phase

Have Main surface phase-inappropriate scope as a fact with a one-line suggestion:
"SOC 2 certification exceeds the declared PoC scope; defer until a customer or
regulatory obligation requires it." Do not turn routine deferral suggestions into
blocking questions or remove committed scope without authority. State the cost and
evidence; retain an item when risk or an explicit promise justifies it.

Read [examples](examples.md) when choosing a batch or reconciling phase with release
discipline. Revisit the declaration when audience, obligations or support promises
change; propose the new phase rather than silently expanding every task's gates.
