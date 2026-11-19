# Host Main controls cluster workers (BLG-385)

Status: design draft, 2026-11-02; no product changes or new authority approved.
**2026-11-03 update:** [`docs/design/cluster-workers-from-desktop.md`](cluster-workers-from-desktop.md)
(BLG-460, round 2) folds this doc's "Results: data returns" section into the
ordinary published-file path (`blerg-runner publish`/`session files`/`session
download`, plus a new `session import` verb) rather than the bespoke broker
below — see that doc's §4 and open question 1. Treat "Results" below as
superseded for the result-transfer case; this doc's remaining live scope is
"Source transfer" (unpublished/dirty commits going *into* the cluster), still
deferred to 0.7.0.
Baseline: local main `9644e115`; requirements are the supplied card text, with no
board access. Existing card estimates remain L, high risk, high complexity.

## Direction and existing paths

A host workspace Main may start, message, read, stop and discard its cluster
workers. A cluster session must never control the host or sandbox, even when its
ancestor is a host Main. This is a hard server security boundary, not an approval
prompt that an agent can override. Sandbox Main retains its existing limitations.

BLG-45 already supports desktop non-workspace cluster children through cluster
standing rules and Permissions rows. Workspace selection currently pins children
to their parent's runtime in
[workspace_child.go](../../runner/internal/server/workspace_child.go).
[cluster_workspace_child.go](../../runner/internal/server/cluster_workspace_child.go)
projects cluster-parent members and uses private cluster contexts; it cannot simply
reuse host paths as cluster paths. Existing owner-proof, engine, budget, cluster
availability and credential checks remain required. Related BLG-273 and BLG-318
must be checked for contract overlap before implementation; this draft does not
assume their card text or completion state.

## Options and recommendation

| Area | Options and consequences | Recommendation |
|---|---|---|
| Runtime transition | Remove runtime checks globally; special-case host Main → cluster; create a general runtime migration feature. | Explicit host Main → cluster child transition only, keeping existing same-runtime paths. No in-place runtime mutation. |
| Source already on origin | Clone default branch; fetch exact chosen commit; require owner to push first. | Verify the exact selected parent HEAD is fetchable using worker read access; refuse any stale substitution. Owner push is an explicit fallback, never automatic. |
| Unpublished local commits | Refuse all; host pushes temporary upstream ref; host serves Git directly; broker immutable Git bundles. | Broker bundles of committed objects with short-lived scoped transfer. Refusal is the safe first increment until that transfer is implemented. No worker push credentials or inbound host service. |
| Uncommitted files | Copy working tree; auto-commit; use committed HEAD with an explicit warning; refuse. | Refuse dirty selected members, including untracked non-ignored files. Ask Main to commit deliberately; no auto-commit or secret-bearing working-tree upload. |
| Results | Worker pushes upstream; cluster writes host files; broker result bundle imported into allocated refs; manual patch download. | Broker bundles, pulled and validated by the trusted host daemon into allocated wip refs. Manual export remains recovery only. |
| Permissions | Inherit host shell grants; add new approval class; reuse BLG-45 cluster rules/Permissions with immutable selection. | Reuse cluster approvals, keyed to existing cluster identity, engine and owner; revalidate at dispatch. Never inherit host shell grants. |
| Descendants | Disallow all grandchildren; allow cluster-only descendants under existing rules; let ancestry grant host rights. | Cluster-only descendants under existing rules. Every generation is checked against the actual actor runtime. |
| Import integration | Automatically merge/checkout; import wip ref only; manually download all results. | Import wip refs only, with Main deciding when to merge. No checkout, hooks or automatic execution. |

The bundle recommendation introduces a new source-disclosure and object-import
boundary. Owner approval and a security review are prerequisites to shipping it,
not a reason to silently start at an older upstream commit.

## Server authorization boundary

Resolve actor from the authenticated session token and stored session/runtime
identity; never from caller-provided runtime, daemon ID, parent ID or owner field.
Resolve target independently from server records. Unknown, missing or inconsistent
runtime identity fails closed, including legacy records. Human owner access remains
its own authorization path; a session cannot impersonate its owner or exchange its
token for human/desktop authority.

| Actor | Target/action | Policy |
|---|---|---|
| Host workspace Main | Selected cluster child: start/message/read/stop/discard/result | Allowed only by owner, relationship, member subset and BLG-45 permissions. |
| Host Main | Unrelated cluster session | No new access; existing authorization still applies. |
| Cluster session, including host-spawned worker | Host or sandbox: start/message/read/stop/discard, files, shell, result/control APIs | Always refused. A standing rule cannot override this. |
| Cluster session | Cluster child or existing permitted cluster target | Existing relationship, owner and cluster permissions; no broader authority. |
| Sandbox Main | Any transition | Existing limits unchanged; no new grant from this design. |

Use one shared server policy before dispatch, token issuance, event subscriptions
and approval lookup; apply it to REST, MCP, CLI-backed routes and daemon control
requests. Audit all indirect surfaces: list/search/events, telemetry, artifacts,
workspace files, result retrieval, resume/answer, shell, daemon selection and
permission/rule mutation. Cluster actors cannot use those to read host content,
trigger host writes or widen their runtime. A denial should not reveal host
session existence or metadata beyond existing generic refusal behavior.

The actor's current persisted runtime governs each request. Host ancestry is
never a capability. A cluster child's own token cannot control its host parent,
host siblings or sandbox sessions; its cluster child cannot either. Cluster
requests to grant themselves host authority or to change placement refuse before
any desktop lookup, approval prompt or side effect. Replay/delayed approval must
repeat the same checks. Session end/revocation invalidates associated capabilities.

Existing child-to-parent notification/question delivery requires special care:
the trusted server may show a bounded child status, result or question to its
host Main as part of Main's subscription. That is data delivery authorized by
Main, not a cluster token's generic host message/read capability. It must not
resume the host, run a command, open a shell, or let the worker choose an arbitrary
host recipient. Audit existing automatic parent-turn wakeups; disable them across
this boundary unless the owner explicitly approves a separately constrained flow.
Untrusted result text never becomes an instruction to the daemon.

## Source transfer: exact commits without push credentials

1. Main selects only immutable launch members by stable ID or unambiguous selector.
   Host-to-cluster is explicit; omission preserves existing placement. Require a
   host workspace Main, a configured cluster and supported registered repositories.
   Refuse folder-only workspaces and unknown repository identities.
2. Trusted daemon captures each selected member's committed HEAD and clean status,
   source identity and parent launch generation. Build a transfer manifest containing
   owner, parent/child IDs, member IDs, exact bases and a fresh transfer generation.
   Do not copy absolute host paths, `.git/config`, remotes with embedded credentials,
   ignored files, hooks, environment files or host shell grants.
3. If the worker's read-only provider access can fetch every exact base, no bundle
   is needed. Otherwise the host daemon creates a self-contained Git bundle of the
   selected committed history and uploads it to private broker storage. Committed
   secrets remain possible: this is source export, requiring the owner's existing
   repo/cluster consent, not a secret scanner guarantee. No arbitrary directory sync.
4. Bind upload/download capabilities to owner, child, generation, member and digest,
   with expiry, byte/object limits and no reusable credential in agent context.
   Encrypt storage, authenticate downloads, redact URLs/tokens, prohibit public
   links and cross-owner reads, and delete transfers after acknowledged import or
   bounded expiry. The host initiates outbound transfers; cluster has no host socket.
5. Cluster bootstrap fetches/imports in isolated repositories with hooks/config and
   external helpers disabled, verifies object integrity and exact base commits,
   then creates worker branches. All selected members must validate before the
   engine starts. Recheck parent selection and permission after delayed approval;
   a changed HEAD requires a fresh selection/confirmation rather than silent rebasing.

Persist the bases, object digests, selection and launch generation with the child.
Retries reuse this immutable selection or report why it expired. Unknown transfer
completion means pending/recovery, not a second launch. Quotas and disk preflight
apply on host, broker and cluster; never exhaust the host to offload its work.
Submodules, LFS and repositories requiring custom filters need explicit transfer
support; refuse plainly until verified. Never send host Git credentials to make
a private base fetchable. Worker provider credentials, where needed, are read-only
and follow the existing owner-proof lifecycle.

Required stale-base sentence, with safe member label: “Cannot start cluster worker:
selected commit for <member> is not fetchable by the cluster and local commit
transfer is unavailable; publish that commit or enable the approved transfer.”
No default branch fallback, remote-tip substitution or partial workspace launch.

## Results: data returns, host control does not

Worker commits are exported by trusted cluster infrastructure as per-member result
bundles, bound to the recorded base and launch generation. A worker can submit
untrusted objects for its own result slot; it cannot choose a host path, ref name,
credential or command. The daemon pulls from the broker under the previously
recorded Main-authorized child allocation. Cluster has no daemon control token.

Validate sizes, object connectivity, ancestry from the recorded base, repository
identity, generation and expected result commit. Import first into quarantine,
without checking out files, running hooks/filters or following arbitrary URLs.
Only the host's recorded per-member wip branch may advance, using an atomic
expected-old-value ref update. Reject unexpected/non-fast-forward replacement,
symlink/path escapes and arbitrary advertised refs. Do not accept executable Git
configuration or worker-supplied target paths. If the destination changed, retain
result for recovery and report conflict; never force-update another branch.

Main receives exact per-member commit/branch and pending/error status through the
existing result contract. Merge remains a separate Main action. Imported code is
untrusted and does not execute on import. Stop/discard revoke execution and
transfer capabilities; discard removes only the child's proven cluster resources
and transfer objects after result acknowledgement or explicit loss confirmation.
Keep imported wip branches for recovery, consistent with current host-child discard
behavior. Failed import retains a bounded private recovery object, never claims
successful delivery. Restart reconciliation uses durable receipts so duplicate
exports cannot overwrite a later branch tip. Host offline means result pending;
it does not grant the cluster permission to reach into the host.

## Implementation proof required

Use the public API/CLI path with deterministic authorization contracts first,
transport/serialization tests next, then isolated real-service boundaries. Test
all cluster→host and cluster→sandbox start, message, read, stop, discard, resume,
files, events and shell attempts, including forged IDs/runtime fields, omitted
runtime, own session token, grandchild token, owner mismatch, revoked permissions,
expired capabilities and delayed approval. Verify refusal has no daemon request,
credential mint, prompt or resource creation side effect. Preserve sandbox tests.

Transfer tests include unpublished commits, exact upstream bases, dirty checkout,
one bad member among several, HEAD races, malformed/oversized bundles, wrong
owner/repo/generation, divergent result, destination-ref race, offline host,
restart/replay, cleanup and no push credential exposure. Test parent notifications
cannot resume/control a host. Corrupt identity must fail before any data read.

Required real acceptance: host workspace Main with an unpublished local commit
starts an explicitly cluster worker, verifies that exact base there, receives its
new commit on the allocated wip branch, reads/messages/stops as permitted, then
discards cluster resources while preserving the result branch and host checkout.
Also demonstrate the stale-base sentence with transfer disabled. A test requiring
its own host ptrace tracer remains on the host; cluster offload cannot claim to
cover it. No product tests or live-cluster acceptance are claimed by this draft.

## Owner decisions still open

1. Approve broker transfer of unpublished committed history, private storage,
   expiry/quotas and source-export consent; or require explicit upstream publication.
   Recommend bundles, with fail-closed refusal until available.
2. Confirm only host workspace Main receives this transition, with cluster-only
   descendants and unchanged sandbox limits. Recommend that exact scope.
3. Decide cross-boundary child question/notification behavior. Recommend passive
   Main-visible status only, with no automatic host resume or generic host messaging.
4. Confirm wip-only import, ancestry requirement, conflict refusal and retained
   result branches after discard. Recommend these restrictions without auto-merge.
5. Choose initial repository features (submodule/LFS/filter support), retention
   quotas and private-repository bootstrap policy. Recommend refusing unsupported
   features rather than copying host settings or credentials.
6. Accept the server surface audit and real host→cluster→wip→discard evidence
   before enabling the capability. Review BLG-45/273/318 interactions first.
