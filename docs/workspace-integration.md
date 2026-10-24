# Workspace integration and release gates

The 0.5.0 workspace packages are merged, but workspace v2 and owner shells are
**not enabled in production**. Discovery, contracts, runtime primitives and UI
components do not establish release acceptance by themselves. The daemon's
`WorkspaceRuntimeGateVerified` and the server's `RuntimeShellGateVerified` remain
false, with no environment override. Old peers keep scalar single-repository
behavior and refuse multi-repository and shell operations.

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
revocation, and 083 desktop runtime transactions. Core keeps 025 release notices;
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
actual `LocalWorkspaceService`. Before each connection it verifies the complete
local host/Docker stack: authenticated selection and authority adapters, reader
lifecycle hooks, P5 registration, supported Linux containment/traversal, the host
writer/shell helpers, and both fixed helpers in an immutable local Docker image.
The image probe also verifies a non-root agent, zero effective agent capabilities
and root-owned immutable helper executables.
The existing wire capability covers the local stack as a whole: missing any
backend or helper conservatively suppresses workspace v2, including host launches.
Probe containers have no network or host mounts and cleanup checks their unique
ownership labels. Discovery and image presence alone never advertise workspace v2.

The pod provider requires its configured protected storage, contained writers,
launch consumer and reader/shell services. It probes the pod executable's actual
contained helper; pod readers run in-process. Server welcome and stored peer
capabilities are the exact supported-version intersection, independently at both
hops. Launch messages carry that negotiated capability. P5 bindings and shell
admission use the transport result; registration alone grants no peer support.
Capability loss keeps exact lifecycle cleanup available while refusing new shells.

Both release constants remain false. A single **test-only build tag**,
`blerg_workspace_integration`, exercises both gates without changing them. Never
ship binaries built with this tag. Production builds have no environment,
administrator or linker-variable enablement switch.

The enabled production-stack regression lives in
`runner/internal/server/workspace_enablement_integration_test.go`. Build the daemon
with that tag and supply its path as `BLERG_HOST_HELPER_TEST_BINARY`; supply a
normal daemon build as `BLERG_P6_GATED_DAEMON_TEST_BINARY` for the gate-off check.
Use a disposable sandbox image with the current fixed explorer/shell helpers and
the test's scripted CLI fixture, selected by `BLERG_RUNNER_SANDBOX_IMAGE`. These
are test artifact selectors, not gate overrides. Run the server tests with
`-tags blerg_workspace_integration -race -run
'TestP6EnabledProductionStack|TestP6ProductionDaemonGatesOff'` and the supplied
isolated test DB. Tests isolate HOME, daemon state, repositories and tmux sockets;
they exercise the real daemon entry point, enrolled connection, welcome, P1 folder
selection, host/Docker launches, explorer list/search/status, consent/P5 open and
close, member sync and durable stop receipts. The pod image test also exercises
computed hello/welcome negotiation under its verified UID/storage/capability setup.
Run ordinary builds separately to retain the production fail-closed tests.

## Runtime wiring and remaining acceptance

Production desktop explorer registration now uses P1 host descriptors and P2's
bound sandbox helper, including the legacy single-repository sandbox viewer.
Readers serialize against P2's lifecycle mutex and are canceled/joined before
freeze. Registration stays pinned through sync and verified thaw; cleanup failure
fences reads and sync. Host admission and crash recovery retain a durable controller/helper handoff. See the [explorer guide](workspace-explorer.md).

P5 supervisor registration joins the coordinator using its private leaf mutex
beneath the workspace lifecycle lock. A missing registration refuses cleanup
rather than inventing an empty containment proof. Existing containment,
lease-through-freeze and recovery evidence does not replace the remaining full
runtime/platform acceptance campaign.

Cluster startup also requires real agent/shell containment, protected private
storage outside agent authority, the P1 launch consumer, and the P4/P5 service
adapters. An environment-provided pathname or mode 0700 under the same UID is not
this boundary. Keep the existing outbound connection and exec RBAC unchanged.

Release requires independent cross-package security review, the full engine and
home/cache matrix, old-peer/database/private-allocation upgrade and recovery,
quota exhaustion, ordinary and metadata-heavy 1/5/20-member measurements,
cross-feature status/sync/shell races and fair transport load, and UI coverage
including phone paste/IME, ANSI, two terminals, reconnect and 390/3840-pixel layouts.
Physical macOS, Docker engine restart and real-pod CSI/provider/fencing acceptance
remain separate recorded gates. Missing evidence keeps capabilities unavailable.

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
