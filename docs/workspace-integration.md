# Workspace integration and release gates

Workspace v2 and owner shells use explicit per-runtime switches in
`runner/internal/protocol/workspacegate.go`:

| Runtime | Workspace and owner shell policy |
| --- | --- |
| Linux/WSL host | ON, subject to verified installed host helpers and containment |
| Local sandbox (Linux or macOS desktop) | ON, subject to verified Docker storage and image helpers |
| macOS host | OFF (#181: native containment and recovery remain unavailable) |
| Cluster | ON, subject to protected storage, contained writers and verified pod services |

Shell follows the same switches, with additional owner, policy and host-consent
checks. No environment variable enables an OFF runtime. Missing capabilities,
legacy version-only peers and unknown runtimes fail closed for workspace launches;
legacy scalar single-repository sessions remain compatible.

## Interrupted cluster launches

Interrupted cluster Secret creation retains a durable cleanup obligation containing
only invocation metadata. Cleanup uses an independent bounded context, verifies the
invocation and stable UID, and deletes with a UID precondition. The server reconciler
retries unresolved cleanup after a grace period, including after server restart or
session deletion. Kubernetes bodies, object names and transport URLs are excluded
from launch diagnostics. Detailed shell refusal codes are available only to the
session's exact authenticated owner; unrelated humans and administrators receive
the same generic refusal before runtime dispatch.

## macOS host support in 0.5.0

Workspace sessions that run directly on a macOS host, including host folder
sessions and their owner shells, are unsupported in 0.5.0. The macOS daemon
advertises no host workspace v2 or host-shell capability. Host workspace launch
refuses with “workspace sessions are not supported on macOS hosts yet” before
pinning a selection, writing lifecycle state, allocating runtime authority or
starting an agent. Native descendant containment and crash-recovery identity
support are required before these sessions can be enabled. Linux/WSL host
workspace and shell support use their own enabled switch.

The macOS explorer's secure traversal and Git status implementations remain
available to authorized readers and native acceptance tests. They do not prove
agent or shell containment. Sandbox sessions on a Mac continue to use their
Linux runtime; this host admission restriction does not change sandbox startup
or its in-container explorer. Sandbox capability is probed independently of the
unsupported native host backend.

## Desktop control and completion

The daemon constructs `LocalWorkspaceService` with the real P1 selection service
and `WorkspaceAuthority` on its enrolled, authenticated outbound connection. The
server derives the desktop owner, connection generation, immutable manifest and
session relationship from persisted state. A request's session ID, token or
reported root cannot authorize itself. Legacy shared desktop credentials and
session-scoped pod credentials cannot use this control channel.

Generation allocation and its idempotency token commit together. Prepared roots
are write-once for a generation. Abort fences the token and clears file authority;
P2 must independently prove exact runtime removal. Recovery preserves membership,
results and the physical root while authenticating the new connection. Ordinary
Current and Results operations refuse ended sessions.

Completion commits the exact member results, terminal outcome and matching digest
receipt in one database transaction. A new authenticated connection can reconcile
an identical transaction after the session ended. Conflicting outcomes refuse.
Successful socket submission never counts as acknowledgment. A later terminal
notification cannot replace the committed outcome or its original timestamp.

Runner migrations are 076 selection, 077 desktop ownership, 078 runtime roots,
079 cluster storage, 080 cluster settlement, 081 shell consent, 082 consent
revocation, 083 desktop runtime transactions, 084 desktop cleanup, and 085
interrupted cluster Secret cleanup. Core keeps 025 release notices;
desktop credentials and shell policy use 026 and 027. These are forward migrations;
back up the database and update the server and daemon together.

## Explorer, palette and shell

Quick open calls P4's bounded, authorized workspace search endpoint and uses the
shared file viewer. Search limits, protected-name filtering and generation checks
remain runtime responsibilities. There is no per-member search fan-out. A missing
runtime reader returns unavailable.

Shell availability comes from an owner-only, no-store capability endpoint. The
palette, menu and shortcut register only while that capability is present. An
availability hint does not grant shell execution; every operation still requires
live authentication, policy and runtime authority. A loading permission response
leaves commit references pending rather than permanently treating them as absent.

## Capability negotiation

The shipped desktop entry point supplies a capability provider backed by its
actual `LocalWorkspaceService`. Before each connection it checks authenticated
selection and authority adapters, reader lifecycle hooks and concrete P5 registration.
Host advertisement independently verifies Linux containment/traversal and the
host writer/shell helpers. Sandbox advertisement independently verifies Docker
storage and both fixed helpers in an immutable local image, a non-root agent,
zero effective agent capabilities and root-owned immutable helper executables.
Failure of one backend does not suppress another verified backend. Probe containers
have no network or host mounts; cleanup checks their unique ownership labels.
Discovery and image presence alone never advertise workspace v2.

The wire capability carries `manifest_version`, `shell_version` and affirmative
`linux_host`, `macos_host`, `sandbox`, `cluster` flags. Server welcome and stored
peer capabilities intersect exact versions and each runtime flag independently.
Launch admission, P5 bindings and the UI require the selected runtime's flag;
registration or a version alone grants no support. Capability loss keeps exact
lifecycle cleanup available while refusing new shells.

The pod provider additionally requires configured protected storage, contained
writers, the P1 launch consumer and reader/shell services. It probes the actual
contained helper. The server requires the history pool before exposing cluster
capability; workspace storage is bounded or standard ([cluster runtime](cluster-workspace.md)). Cluster workspace and shell
policy is enabled in ordinary production builds following the real-pod happy-path
smoke. The existing **test-only build tag**, `blerg_workspace_integration`, retains
the opt-in acceptance harness; it is unnecessary for production enablement and
never enables macOS host. Never ship binaries built with this tag. Production
builds have no environment, administrator or linker-variable enablement override.

The enabled production-stack regression lives in
`runner/internal/server/workspace_enablement_integration_test.go`. Build the normal
production daemon and supply its path as `BLERG_HOST_HELPER_TEST_BINARY`. Use a
disposable sandbox image with current fixed explorer/shell helpers and the test's
scripted CLI fixture, selected by `BLERG_RUNNER_SANDBOX_IMAGE`. These are test
artifact selectors, not enablement overrides. Run the server test with
`-race -run '^TestP6EnabledProductionStack$'` and the isolated test DB. Tests isolate
HOME, daemon state, repositories and tmux sockets; they exercise the real daemon
entry point, enrolled connection, welcome, P1 folder selection, host/Docker launches,
explorer list/search/status, consent/P5 open and close, member sync and durable stop
receipts. The ordinary pod image test exercises computed cluster hello/welcome
negotiation and shell open/close under its verified UID/storage/capability setup.
The concrete cluster-consumer regression launches parent and subset-child Jobs
without a capability override. Ordinary builds retain macOS-host OFF-runtime
fail-closed tests, including unknown and version-only peers.

## Runtime wiring and remaining acceptance

Production desktop explorer registration now uses P1 host descriptors and P2's
bound sandbox helper, including the legacy single-repository sandbox viewer.
Readers serialize against P2's lifecycle mutex and are canceled/joined before
freeze. Registration stays pinned through sync and verified thaw; cleanup failure
fences reads and sync. Linux freezes use ptrace exit events to account for a zombie
thread-group leader whose final wait notification is delayed until its siblings exit.
A sibling exec clears the old leader's exit proof before the replacement program runs.
Slow freezes log at most 32 unheld tasks after two seconds and on failure, with tracer
bookkeeping and kernel status fields; these diagnostics never supply containment proof.
The tracer reconciles a creator's fork/clone event with its child's own reports in
either order, counting reports per TID, so a child reaped before that event is never
tracked again. A new task that reuses the TID of a thread whose exec is still to be
reported is recognized by its auto-attach stop (SI_USER from PID 0) and recorded as a
birth; that exec then drops only the stale entry. A lost
host helper's tree is proven gone through its pidfd ledger (see
[cluster workspaces](cluster-workspace.md)); the next engine then starts under a
fresh helper, and a thaw never keeps admission closed for a dead helper.
The Linux pthread/clone regression fixtures require a C compiler (`cc`).
Host admission and crash recovery retain a durable controller/helper handoff. See the [explorer guide](workspace-explorer.md).

P5 supervisor registration joins the coordinator using its private leaf mutex
beneath the workspace lifecycle lock. A missing registration refuses cleanup
rather than inventing an empty containment proof. Existing containment,
lease-through-freeze and recovery evidence does not replace the remaining full
runtime/platform acceptance campaign.

Cluster startup also requires real agent/shell containment, protected private
storage outside agent authority, the P1 launch consumer, and the P4/P5 service
adapters. An environment-provided pathname or mode 0700 under the same UID is not
this boundary. Keep the existing outbound connection and exec RBAC unchanged.

Independent cross-package security reviews are signed off. Follow-up acceptance includes the full engine and
home/cache matrix, old-peer/database/private-allocation upgrade and recovery,
quota exhaustion, ordinary and metadata-heavy 1/5/20-member measurements,
cross-feature status/sync/shell races and fair transport load, and UI coverage
including phone paste/IME, ANSI, two terminals, reconnect and 390/3840-pixel layouts.
The full failure-case and measurement campaign is deferred to #180 (0.5.1).
Physical macOS host containment remains #181. The cluster happy-path smoke passed
on real pods with two public members, explorer list/search/status, shell open/close,
fixture-backed sync and stop cleanup. Default hostpath/local-path storage does not
enforce byte/inode quotas; exhaustion acceptance is not claimed. On AlwaysAllow clusters, #182 warns that RBAC rules cannot establish
effective exec denial; smoke results distinguish `pass-rbac-not-enforced`.

For native Mac acceptance use `python3 scripts/test-workspace-macos.py` from a clean
committed checkout and retain its accepted metadata and JSON logs. See the
[explorer guide](workspace-explorer.md), [cluster runtime guide](cluster-workspace.md)
and [shell supervisor guide](owner-shell-supervisors.md) for the exact boundaries.

### Qualified Git reads and outcomes

Workspace-v2 commit summaries, patches and origin reads name `member_id`,
`launch_generation` and `generation` from the prepared runtime. The commit HTTP
routes accept those query parameters; summaries echo them, and patch responses
include `X-Blerg-Member-ID`, `X-Blerg-Launch-Generation` and `X-Blerg-Generation`.
An unqualified read resolves only for a single launch member. Multi-member reads
require an explicit member; the first checkout never supplies a default.

The shared REST/MCP/SSE/callback result includes the authorized immutable
`workspace` and persisted `workspace_results`. Each outcome retains its member
and launch, base/head/branch, pending state, sync/push errors and recovery
location. Folder scalar repository, Git URL and branch fields stay empty.
Desktop member and recovery metadata requires the launching person to remain
that desktop's persisted owner. Cluster authorization remains separate.

Browser commit chips may carry a `member` reference. The transport forwards it
for both summary and patch reads and checks the returned reference; hover caches
and open viewers include the reference in their identity. Unqualified prose
commit references in a multi-member conversation remain unavailable until their
member is known. Ticket filing accepts the same reference and retains it in the
server-derived origin. Child-merge checks use each selected child's persisted
member launch and base, and require every selected member to be merged.

### Sessions from older releases

On reconnect and after agent recovery, capable desktop daemons attach the production
explorer and owner shell to existing single-repository sessions. The trusted session
project directory is the workspace root; the browser cannot choose a directory.
The attachment uses the current desktop owner and connection fences, a write-once
physical root binding, protected-file rules, and the normal shell containment and
host-consent flow. Restricted, cron, and child sessions do not gain owner shells.

This reader attachment preserves the session's existing recovery, child-start,
commit, and completion behavior. It does not create a new workspace selection or
replace the running engine. An old peer still grants no capabilities.

Explorer Refresh retries attachment if recovery was incomplete and announces
progress and the result. Refusals explain whether the daemon is disconnected,
the project is missing or changed, ownership changed, or the runtime is unsupported.
A successful root read also rechecks shell availability in the menu, palette, and
keyboard shortcut.
