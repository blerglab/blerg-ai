# Command and Git policies (BLG-253)

Status: design and admission hardening only. Command, Git, and path policies are
not available yet. This change does not complete BLG-253.

## Current boundary

Proposal approval applies to selected MCP writes, not shell commands or Git.
Claude's existing `PreToolUse` Bash hook records progress; it is not an access
control. For unrestricted sessions, the Codex driver starts threads with
`approvalPolicy: never` and `sandbox: danger-full-access`. Restricted sessions
use separate tool and sandbox restrictions. The restart deny-list decides whether to resume
after interruption; it does not intercept execution. No engine currently
advertises complete task policy enforcement.

Git hooks alone cannot protect refs against a process that owns the repository:
`--no-verify`, `-c core.hooksPath=...`, `update-ref`, alternate indexes, and direct
writes to Git metadata bypass ordinary pre-commit/pre-push checks. Blerg's own
trusted Git operations disable repository hooks to avoid executing untrusted
repository code. Replacing that protection with checkout-controlled hooks would
introduce a separate vulnerability.

## Safe reversible decision

Keep policy enforcement unavailable until a trusted boundary can enforce the
requested guarantees. Do not silently drop restrictions or degrade `ask` to
`allow`. Requests with unknown fields inside launch `permissions`, including
nested MCP tool entries, return 422 before workspace selection, credentials,
session creation, or dispatch. Top-level unknown fields retain the existing
400 response. Existing supported permissions retain their behavior. These are
permission grants, not a command policy schema; do not place rules there.

This is an explicit compatibility tightening: older clients sending unknown
permission fields must remove them. The error must not echo field names or
values, which may contain credentials. No command decision or approval is made
by this admission check, and it does not claim to audit command execution.

## Proposed implementation contract

Use a versioned policy document separate from permission grants. A person sets
it through an authenticated owner-only endpoint. Snapshot card rules into a
session at launch; card prose is never executable policy. Account, card, and
session layers intersect: a narrower layer cannot override a deny or widen a
path scope. Persist the effective revision before dispatch. Children inherit
that revision and may only narrow it. Resume must revalidate enforcement
capabilities before starting an engine.

Rules use explicit deny/ask/allow actions and deterministic matching, with no
model evaluation. Compile bounded patterns on write and reject invalid syntax.
Use deny over ask over allow across matching rules, then the explicit default
for unmatched requests. Match raw command text as text, without claiming that
regexes understand arbitrary shell execution. Git rules operate on actual ref
transactions and tree changes, not command spelling. Path scopes are
repository-relative and validated against traversal, symlinks, renames, alternate
indexes, submodules, and multiple workspace repositories. Distinguish permitted
committed paths from filesystem write containment; Git tree checks cannot
restrict arbitrary filesystem writes.

Each registered engine/runtime must report separate capabilities for every
shell invocation, Git ref changes, filesystem scopes, and pending approvals.
Reject a nonempty policy if any requested capability is missing. A native
approval prompt that sees only some commands is insufficient. Claude hooks
must fail closed on missing policy, timeout, malformed input, or backend failure;
Codex needs a verified interception path before being marked supported. Other
engines remain unavailable for policy-bound sessions until independently tested.
Hook configuration, policy files, credentials, and the decision service must
be outside the agent's writable namespace. Host execution remains unsupported
where the agent can alter those controls.

For sandboxes and pods, keep authoritative refs and push credentials outside
the agent's filesystem. A trusted Git broker validates actual proposed object
trees and ref updates, including Blerg's automatic commits and import/merge
paths. Hooks can provide earlier feedback but are defense in depth, not the
sole boundary. Never give the agent a way to bypass the broker by editing refs
or selecting another credential helper.

An `ask` creates a durable Needs you item bound to account, session, policy
revision, operation digest, repository, and exact refs/trees. Approval authorizes
one identical operation once; changed arguments, expired approval, denied or
canceled request, restart, and policy revision changes require revalidation.
Only a live owner can approve; agents cannot self-approve. Waiting must not
hold a database transaction or a Git lock. Unattended operation stays held.

Persist every evaluation and approval transition before execution, including
actor, session/card, policy revision, engine/runtime, operation identity,
repository, decision and reason. Audit failure denies execution. Do not persist
raw shell commands or environment values in ordinary logs: commands can contain
secrets. Define a protected, redacted operation description and an opaque
identity for approval matching. Never claim a complete audit when a runtime
cannot report every operation.

## Remaining delivery and questions

The owner direction is retained: static rules, engine interception, independent
Git enforcement, pending approvals, and audited decisions. The reversible
choice here is to refuse unsupported requests rather than ship a partial
security claim. Still required: policy persistence/API and card integration,
capability negotiation, protected decision service, engine adapters, trusted
Git broker, approval inbox, audit integration, and adversarial real-path tests.

Before enabling the feature, settle the pattern grammar and limits, whether
path scope means committed trees or all writes, and the supported containment
runtimes. Those choices can narrow the first supported profile; they must not
weaken its advertised guarantees. Acceptance tests must cover shell indirection,
Git plumbing/hook bypass, nested engines, automatic commits, multi-repository
sessions, crash/restart, replay, approval revocation, and unavailable audit or
policy services.
