# Owner-shell supervisors

P5 supplies `runner/internal/shellruntime` and consumes P0b without changing its
shared contracts. Runtime enablement is guarded by
`server.RuntimeShellGateVerified`; there is no environment override. Independent
adversarial review and P6's real cross-package integration gates are still required
before setting that constant. A passing fake pod test is not a live pod result.

## Prepared-runtime registration

The daemon and pod runner register the `owner_shell_request` handler on their
existing authenticated outbound WebSocket client. P2/P3 supplies each prepared
launch through:

```
service := client.OwnerShellRuntimeContext(runtimeContext)
service.Register(runtimeContext, sessionMutex, binding, command, reconcileOrphans)
```

`binding` comes from trusted preparation state: persisted owner, session, selection
launch, allocated physical runtime generation, prepared public-root device/inode,
manifest digest, runtime kind, and display folder. Children, cron, unknown owners,
non-live launches and unsupported platforms refuse shell admission. Registration
probes the fixed helper's version before offering shell capability. It does not
run an interactive shell or bypass host consent.

`sessionMutex` protects P5's shell state. In the local workspace integration it
is private to P5: P2 already holds its workspace transaction mutex while waiting
for queued lifecycle calls. Passing P2's mutex here would deadlock the worker.
The order is P2 workspace mutex → per-session lifecycle coordinator → P5 queue →
P5 shell mutex. P5 callbacks must never acquire P2's mutex or call the coordinator.
Call P5 lifecycle methods without already holding their shell mutex. `Notify(syncing)` must succeed before
P2/P3 freezes the container; call `Notify(running)` only after unfreezing. Paused,
stopping, replacement, end and discard fence and close shells. Running alone
never clears an uncertain or terminal fence.

`reconcileOrphans(ctx)` must independently prove all prior shell supervisors of
that physical incarnation are gone. A brand-new runtime namespace or an exact
previous all-launch cleanup proof can supply that evidence. A recovered runtime
cannot use an empty in-memory map, a disconnected socket or a dead Docker client
as proof. The callback must honor its deadline, must not call back into this
service, and must not infer authority from agent files. Failure retains admission
and storage fencing. P2/P3 retains its separate agent/runtime cleanup obligation.

Choose the command from trusted runtime configuration:

- Host: `shellruntime.HostCommand(daemonExecutable, actualIdentity)`; obtain the
  identity with `ResolveIdentity(preparedCwd)`. The daemon's early
  `shell-supervisor` subcommand isolates tracing/reaping from its agent children.
- Local sandbox: `daemon.SandboxShellCommand(container, sessionAccount, identity)`.
  This pins the session's Docker context and executes the fixed
  `/usr/local/bin/blerg-shell-supervisor` inside that container as its session user.
  `identity.Cwd` is the prepared in-container directory. The image includes the
  static helper. The sandbox image now builds from the repository-root context;
  the desktop installer and documented build commands reflect that.
- Cluster: `shellruntime.HostCommand(podRunnerExecutable, actualIdentity)` inside
  the pod. The pod runner has the same early helper subcommand. All controls and
  output use its existing outbound authenticated connection. No Kubernetes exec
  API, permission or RBAC change is involved.

For a folder session, set `command.PublicRoot` to the prepared public root and
`command.Identity.Cwd` to the trusted default member repository. The helper opens
that member through `os.Root` anchored to the public-root identity, then starts
from the pinned directory descriptor. It refuses an escaping/replaced member
symlink. The public-root identity is not confused with the member directory's
inode. An empty PublicRoot means the cwd itself is the public root.

Before a reconnect's hello, `Service.Rebind` closes old helpers, assigns a fresh
connection nonce and clears host consent from the live binding. Old queued
requests, grants, keys and consent cannot transfer. Use `Service.Binding` and fetch
`Service.Lifecycle(session)` again after a connection change; do not cache a
supervisor launch across reconnects.

## Lifecycle and storage proofs

`Service.Lifecycle` implements the merged `protocol.RuntimeSupervisor`, including
before the first interactive shell is configured. Local lifecycle calls share the
bounded command scheduler with authenticated RPC. The server exposes the remote
implementation through `API.RuntimeShellSupervisor()` after the enablement gate.

`Service.Retire(ctx, launch)` obtains an exact all-shell proof before removing the
registration for replacement/deletion. A failed retirement retains registration
and admission fencing. P2/P3 must combine this shell proof with its own
agent/runtime settlement proof before deleting storage or starting a replacement
writer on that storage.

Proofs bind session, selection launch, physical generation, runtime, authenticated
connection and optional shell generation. Empty shell generation means every
supervisor, including pending admissions and detached descendants. Transport close,
HTTP DELETE success, helper/client death, cancellation and timeout are not proofs.
A canceled caller gets an error while independent bounded cleanup continues;
recovery must verify the resulting proof before permitting new admission.

## Containment and local authority

Each interactive shell has a separate helper process, dedicated PTY and process
session. On Linux, a locked tracer thread follows fork, vfork, clone and exec.
The helper is a child subreaper and waits through `ECHILD`, including adopted
zombies, before acknowledging reaping. It kills detached descendants, closes the
pollable PTY master, and reaps independently of the output consumer.

An inherited seccomp/no-new-privileges filter rejects `CLONE_UNTRACED`, refuses
clone3 with ENOSYS for libc's checked clone fallback, and rejects alternate syscall
ABI bypasses. Kernel `PTRACE_O_EXITKILL` kills tracees if their tracer dies. Parent
launch threads remain alive while their helper/client is running so Linux's
thread-bound parent-death signal does not accidentally attach to a retired Go
thread. A helper crash is still not accepted as a reaping acknowledgment.

There are four reservations and one session task budget, partitioned into 32
tracees per reservation. Input/output rates are shared across the session
(16/64 KiB/s). Runtime output uses 4 KiB chunks and small bounded stage queues;
the server's dedicated browser queue retains its 256 KiB cap. A full queue closes
rather than recording/replaying output. Independent helper timers enforce signed
30-second grants even when the parent, browser, writer or consumer stalls.

The helper verifies its pinned public key, full grant binding, monotonic renewal,
actual OS identity and prepared root. The server authorizes every human operation
and output independently. All new shells use the resolved absolute account shell
with fixed `-i`, never login argv, a request command or `$SHELL`. The child gets only
HOME/USER/LOGNAME, vetted PATH, TERM and locale. Provider, Blerg, bridge, preload and
SSH-agent environment variables do not transfer. Startup files and ordinary user
permissions remain part of executing as that OS account. Privilege gain through
setuid executables is suppressed by tracing/no-new-privileges.

The containment implementation is Linux-specific. Other platforms fail closed;
no process-group-only fallback is advertised. The filter permits the helper's
native syscall ABI, not compatibility binaries using a different ABI. Real tests
here cover Linux amd64; other Linux architectures require their own runtime gate.

## Consent, transport and privacy

Host consent uses fresh two-minute, single-use challenges from an authenticated
daemon's actual machine/account/UID/shell/cwd. The browser affirms only a challenge
ID; labels cannot supply identity or command authority. Migration
`081_shell_consent.sql` persists consent bound to owner, login at affirmation,
selection launch, physical generation, connection and actual identity digest.
Accepted consent lasts only for that exact launch/identity/connection. Reconnect,
relaunch and identity changes require a new challenge. Both server and daemon
validate the consent; the helper also resolves its own account and cwd.

RPC has a separate version and correlated request IDs on the existing connection.
Current daemon/pod and exact session checks apply to replies and output. The
bounded writer prioritizes control, then gives shell and agent/file lanes fair
turns; large file writes have a one-second deadline. Input/output and control
queues are separate. Syncing is a scoped control frame with the header
“Syncing the agent's commits…”; it is not PTY output or an agent event. Old runtime
phase messages cannot affect a replacement incarnation.

No new path calls transcripts, History, artifacts, agent event emitters, debug
output or telemetry with terminal bytes. Helper stderr is discarded. The existing
fixed audit schema records identity, generation, policy, event/reason and timing
only. Wire errors are fixed refusal messages, not command/environment payloads.

## Review round 2 lifecycle and consent rules

Registration retains an admission fence and sticky terminal state before the first
interactive configuration. Close/paused state requires an explicit, successful
live-running `RecoverLaunch`; configuration and a later running notification never
clear it. Stopping/replacing/ended/discarded launches require replacement identity.
Cleanup failure retains the fence. These rules survive connection rebinding.

The manager captures identity and runtime revocation epochs before its first
resolver call. A completed revocation rejects that pending open even if its old
resolver result arrives later; exact-incarnation runtime fences survive running
notifications and require verified recovery. Recovery results are also checked
against revocations delivered while their authority calls were in flight.

An observed host identity transition rotates that launch's connection nonce and
audience, clears live consent, and fences admission pending explicit cleanup
recovery and fresh affirmation. Fetch the new Binding/Lifecycle after this event.
Running helpers report identity invalidation over their private stdio control
channel; unexpected helper loss conservatively invalidates consent too. A helper
exit status 3 denotes identity invalidation **after successful physical cleanup**;
it is never used for failed cleanup. Returning to earlier account labels cannot
restore earlier authority. Migration `082_shell_consent_revocation.sql` adds a
permanent revocation marker for persisted challenges and acceptances; serialized
trusted identity observations revoke prior digests/connections, including when no
consent is found for the new identity.

Actual workspace-file and plugin replies now enter the same per-connection fair
writer as shell and agent traffic. File queue wait, actual write budget, TTL and
original connection binding are checked independently. Server input/resize has its
own bounded data queue; close/renew/lifecycle controls receive priority with fair
turns for shell data and agent/file commands. Socket writes have one-second caps;
a stalled file consumer closes the socket and requires fresh authenticated
connection/grants. Queue-depth inspection exposes counts only, never payloads.


## Local workspace coordinator

`daemon/workspacecoordinator.go` registers the concrete P5 service after the
prepared Docker root is authenticated. It maps the server's authority connection
to P5's private nonce only after matching the session, launch, physical generation,
root and manifest. Old authority connections refuse after authenticated rebind.
The fixed helper runs as the sandbox session account in the first selected member,
anchored to the public root. Restricted/child dispatches and old journals lacking
affirmative shell eligibility have lifecycle registration but refuse shell setup.
No release capability is enabled by this wiring.

P2 suspends explorer admission and cancels/joins reads before head probes or
freeze. A reader waiting for P2's mutex uses a cancellable lock acquisition.
`LocalWorkspaceService.CancelStatus`, `ResumeStatus` and `CloseStatus` are the
reader hooks. Production routers preserve their pinned registration across sync;
resume follows verified unpause, root and current authority, and P5's running
acknowledgment. At removal, `Release` requires the reader's bounded
`Close(context.Context) error` or `Close() error`; failed cleanup retains its
suspended registration and prevents storage release. Runtime adapters must own
and join their in-container processes, not just their Docker client.

Before pause, native P5 `Freeze` fences admission and returns the earliest helper
lease deadline. The unfrozen daemon watches that deadline and kills only the exact,
label-validated container if copy/thaw exceeds it. A paused helper's own timer
cannot enforce a wall-clock lease. Unpause is inspected before any exec or running
notification; failed unpause, root/current validation or deadline enforcement
leaves admission fenced and storage retained. Normal thaw joins the watchdog.

Initial reconciliation checks the fixed executable's kernel identity inside the
running namespace; surviving supervisors or an unreadable/paused namespace refuse.
Daemon-process recovery authenticates and fences/removes the old exact container
before registering its physical cleanup proof. It never resumes orphan writers.
An absent registration remains an error. Normal stop joins shell helper processes,
output forwarding and reader cleanup, proves exact container absence, retires P5,
then permits P2 settlement and storage release. Repeated removal uses the retired
coordinator's tombstone. Repeated close rechecks exact absence and returns an
incarnation-bound cleanup proof; it never permits another shell admission.

The focused integration tests cover real host helpers and labeled Docker
containers, including abrupt daemon-process exit while frozen. Full engine/home/
cache combinations, production explorer composition, deployed pod containment,
other-platform acceptance and independent review remain separate release gates.
Both compile-time workspace and shell gates remain false.
