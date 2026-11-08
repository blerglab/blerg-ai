# Persistent owner shells

Status: design v3 accepted by the coordinator for #269 implementation on October
30, 2026, after cross-model review. This authorizes the #269 slice only; #271 and
#274 remain separate implementation and review boundaries. The coordinator's
provisional defaults, pending the owner, are a finite seven-day desktop lifetime,
terminal login expiry and sign-out, and withdrawal in #269 scope. Code requires
its own security review before release.

Implementation: the #269 custody/access split, same-process reconnect,
per-shell host consent and offline withdrawal, bounded daemon-loss hold, private
replay, and independent sync watchdog are implemented on this branch. Selection
requires a correlated personal desktop welcome and a positive installed-helper,
clock, and prepared-member probe. Only host shells can use v2; sandbox shells
retain v1 behavior. The kernel-name denylist rejects recognized WSL and Docker
Desktop kernels; it does not identify every virtual machine or prove host-sleep
accounting for guests such as Lima or UTM. CLOCK_BOOTTIME covers Linux kernel
suspend; pausing a VM from its host is not verified by this check.
The implementation is undergoing its required test and code security review;
this is not release approval. #271 and #274 remain unimplemented.

## Goal and scope

A desktop shell should keep its process, working directory, jobs, and bounded
scrollback when the person closes the browser, loses the network, or sleeps the
computer. Returning to the same session should reconnect the existing shell after
fresh authorization. A short access grant must not be the process's lifetime.

The work is split into three gates:

| Card | Survival boundary | Required custody |
| --- | --- | --- |
| #269 | Browser absence, sleep, and transport reconnect, with the same daemon and runner processes | Authenticated in-memory records at both ends |
| #271 | Daemon restart or reinstall | A separately supervised keeper and authenticated local custody |
| #274 | Runner restart | Durable authenticated custody with durable denial and recovery fences |

#269 must work without claiming either restart guarantee. Its reconnect protocol
must carry the identities and epochs needed by the later gates. A restart while
only #269 is installed retains today's cleanup behavior. Release capability
negotiation must distinguish these guarantees. The older text on #271 mentions
runner restart under #269; #274's explicit split is the scope used here.

Read this alongside [Owner-shell channel](../owner-shell.md),
[Owner-shell supervisors](../owner-shell-supervisors.md), and
[Workspace integration](../workspace-integration.md). Those documents describe
existing contracts, but some descriptions of replay, queue handling, and lease
cleanup are stale. The code inventory below is the implementation baseline.
This proposal changes grant-expiry and reconnect rules only after review and
implementation; the operational docs must be reconciled when that work ships.

## Production v1 baseline and integration constraints

`ownershell.RuntimeLease` permanently closes a process after grant expiry;
renewal cannot revive it. The helper timer and output relay also terminate on a
failed lease check. `Service.Rebind` closes helpers, changes its connection nonce,
and clears host consent before the next hello. The manager's `live` check closes
handles on resolver failure, expired grants, or a changed connection. Remote
output pipes and their readers currently treat a disconnected stream as an end.
All of these paths must change together for #269. Also change manager input and
renew failure handling, `Manager.Unavailable` and `SetPolicy`, the core control
stream interruption path, `Service`'s `transport_lost` cleanup, the helper request
loop, consent handlers/lookups, daemon-loss finalization/revival, configure/recover/
retire, natural-exit diagnostics, and the workspace sync watchdog. A transport or
authority outage must not reach a generic close branch through one of these paths.

#276 already changed desktop idle and detached defaults to unlimited. Clusters
retain five minutes detached and 30 minutes idle. These inactivity timers are
separate from the lifetime proposed below.

The existing host consent database binds acceptance to a connection and
permanently revokes prior connections or identity digests when it observes a
change. It cannot safely be reused merely by omitting the connection predicate.
It records a login but the current acceptance lookup does not filter by login,
and `remoteShell.Resolve` passes an empty login. The proposed adoption consent
requires a separate v2 table and exact-login validation; it cannot reuse the
current lookup or revocation predicates.

Containment, root pinning, writer admission, and cleanup proofs remain mandatory.
No change to `runner/internal/processcontainment` is proposed. In particular,
`PTRACE_O_EXITKILL`, subreaping, descendant tracking, and the helper's physical
cleanup obligations remain intact. Keeping the daemon's helpers alive through a
network outage does not require changing their parent-death behavior. Moving
ownership to a keeper is separate work under #271.

## Threat model

Trust the authenticated core authority, the runner's execution and private keys,
the enrolled daemon credential, and the runtime's installed helper and OS
identity checks. For #274, also trust an independent custody verifier with durable
anti-rollback state outside the runner database writer's control. Availability of
any of these services is not assumed.

Treat browser input, repository contents, agent instructions, terminal bytes,
connection messages from stale peers, and persisted runner rows as untrusted
inputs. A database writer may insert, edit, remove, reorder, or restore runner
custody rows. It must not gain shell access or undo a recorded denial. A machine
label, daemon ID, PID, socket pathname, or copied database row is not proof of
custody. An attacker may delay/replay traffic, race reconnect against close or
revocation, and crash either side at every protocol boundary.

This protocol does not protect against root or arbitrary hostile code running as
the trusted daemon's OS user that can read its private memory and credentials.
Host-shell code runs with that user's ordinary authority. Container boundaries
and the existing filesystem protections still apply; this proposal must not claim
that a same-user keeper socket alone isolates hostile host processes.

The security properties are:

1. Only the original exact human login may regain access to an existing shell.
   Administrator privileges, another login for the same person, and agent tokens
   provide no adoption exception.
2. Access requires a fresh, short grant for the current transport and access epoch.
   Retaining a process grants no permission to read output, replay, type, resize,
   or take over an attachment.
3. A definitive denial is terminal for that shell generation. Reconnection,
   re-enablement, an old acceptance, a restored row, or a changed clock cannot
   reverse it.
4. No uncertain cleanup permits replacement writers or storage release. Neither
   transport loss nor an empty map proves physical cleanup.
5. Memory, disk, output, admissions, and recovery attempts remain bounded.

There is a deliberate security tradeoff: an isolated shell keeps executing code
and can perform its existing filesystem and network work. Cutting terminal access
does not revoke permissions, credentials already in a process, or its network
connections. Immediate remote process termination during a partition is
impossible. On a connected runtime, a verified revocation starts cleanup at once;
on an unreachable runtime access is denied at the authority immediately. The
denial is delivered before any later adoption and enforced physically when
reachable or at the
local lifetime deadline. This limitation must be reviewed explicitly.
With an unlimited lifetime and a permanent partition, remote revocation alone
cannot ensure eventual physical termination. Choosing unlimited explicitly accepts
that consequence; it must not be the finite desktop default.

## Separate custody, access, and attachment

Use three distinct identities instead of making connection ID own the process.

**Custody identity** binds a random shell generation and random custody nonce to:
owner, exact login, session, selection launch, physical runtime generation,
runtime kind, manifest digest, pinned root identity, enrolled daemon credential
ID and generation, local machine identity, and runtime account identity. Host
identity includes UID, account, absolute shell, home, and cwd, with the pinned
root/member relationship checked independently. Sandbox identity additionally
binds the exact container incarnation and session account. The custody record
also holds its own host consent acceptance and identity epoch, policy revision,
creation time, lifetime deadline, and its current terminal/recovery state.

Custody has an unpredictable runtime-process incarnation for #269; #271 adds a
keeper incarnation. A fresh daemon process cannot present itself as the earlier
one simply by using the same enrollment credential. PID identity is local evidence
only and must include an authenticated allocation and kernel start identity when
used for reconciliation. Do not export executable paths or secrets as browser
adoption inputs.

**Access identity** binds custody to a runner incarnation, authenticated current
daemon connection, audience, and monotonically increasing launch access epoch.
A launch custody identity binds session, selection launch, physical runtime,
prepared root/manifest, daemon process incarnation, and enrolled credential;
each shell custody identity is a child of it. Host consent ID and acceptance
are per-shell custody fields, not launch binding fields. Connection, audience,
key, and policy pins move together through a launch-level rekey barrier. A successful
adoption replaces this identity, including the pinned grant key when
necessary. It never changes custody identity. There is only one active access
epoch per shell, even if old and new sockets overlap.

**Attachment identity** binds a fresh authenticated browser attachment to that
access epoch, with independent input and resize sequence counters. The runtime
also resets its input/resize counters atomically when committing the new epoch;
old-epoch frames are refused without killing the process. Reconnect never resends
unacknowledged input automatically: a lost acknowledgment could otherwise execute
a command twice. Takeover remains explicit and owner-only.
Old attachment messages cannot be translated into new-epoch operations.

Bump the shell persistence capability, grant envelope version, and RPC version
explicitly; v1 grants cannot authorize v2 adoption. Old peers must keep their
old conservative behavior; absence of a new field must never select persistence or allow adoption.

## State and lifetime

Process state and access state are separate. A shell can have a running process
while terminal access is suspended.

| State | Process | External terminal access | Exit condition |
| --- | --- | --- | --- |
| Active | Running | Current grant and attachment only | Lapse, disconnect, terminal event, natural exit |
| Suspended | Running | None, including replay | Verified adoption or terminal event |
| Adopting | Running | None | Commit fresh access epoch or stay suspended |
| Closing | Being cleaned up | None | Verified physical cleanup |
| Closed | Gone, proven | None | Terminal tombstone retained |
| Cleanup uncertain | Unknown | None; writer/storage fence retained | Independent exact cleanup proof |

A suspended shell still counts toward the four-shell session limit and existing
process budget. It cannot be replaced to bypass those budgets. Unknown protocol,
identity conflict, or invalid incoming frames grant no access and never clear a
fence. They must not turn an unauthenticated request into authority to kill some
other shell.

The provisional desktop lifetime is seven days from process creation, independent
of human activity, renewals, sleep, and browser attachment. An administrator may
select an explicit finite lifetime or “until the session ends.” Keep existing
idle/detached settings as additional, independently enforced limits. This is a
new lifetime setting, not a reinterpretation of `idle` or `detached`. Clusters keep
today's defaults and expiry-kills behavior unless an administrator explicitly
enables a separately negotiated persistence mode. Cluster support is not required
for #269; do not silently enable it through a desktop default.

The runtime receives lifetime authorization at creation, separately from access
grants. Access renewal cannot extend that deadline. A local suspend-aware elapsed
clock must count laptop sleep (on Linux, an appropriate boot-time clock); Go's
ordinary monotonic elapsed clock alone is insufficient. Also check an absolute
UTC deadline. Either deadline reaching its bound ends the process. Backward wall
clock movement cannot extend the elapsed deadline, and a forward clock jump may
end it early. After a keeper restart, inability to establish remaining lifetime
from authenticated state must refuse recovery, never restart a fresh seven days.
An unlimited lifetime still ends on session termination and definitive revocation.

Lifetime and inactivity settings use a separate versioned configuration contract,
not `shell_policy` mutation. Setting a lifetime must not bump the access policy
revision: that would terminalize shells before the shortening grace can apply.
Changing enablement/security policy still changes that revision and is terminal.

An administrator shortening lifetime gives a running shell the lesser of its old
deadline and ten minutes from the change; lengthening applies only to future
shells. Setting unlimited must not revive expired shells. These are proposed
policy semantics for review. A disconnected runtime cannot apply a new limit
until it learns it; the previously installed finite deadline still applies.

## Grant lapse and output

Keep access grants short: at most 30 seconds, renewed every 10 seconds, with the
existing bounded issuance skew and strict expiry. All immutable custody and
current access fields must be authenticated. Renewal is monotonic within an
epoch. A signed fresh grant may restore access to a suspended shell only through
the adoption transaction; ordinary renewal must not implicitly resurrect an
expired epoch. Lapse latches locally: wall-clock rollback cannot make an old
access epoch valid again. Core outages on a healthy socket use this same suspension
and adoption protocol, with a fresh challenge and higher launch epoch but no
requirement to reconnect the socket. Explicit close, policy change, revocation,
or lifetime expiry cannot be repaired by renewal or adoption.

Expiry, transport loss, and unavailable authority suspend access. They do not
prove a terminal security event. The runtime revokes the access epoch locally,
stops accepting input/resize, stops external output and replay, and discards
queued input. Every queue entry carries its epoch. The final operation and output
writer check that epoch and the grant immediately before committing to transport
or PTY input; checking only when a frame is enqueued is insufficient. Serialize
this check with local suspension/close so operations cannot commit after the
fence. Bytes already written to a network socket cannot be recalled; the server
and browser also reject obsolete epochs. Revocation is not a guarantee that a
remote person forgets bytes already delivered.

The helper may drain its PTY into a bounded private volatile scrollback while
suspended. That is local custody, not external output authorization. Use a fixed
byte cap (proposed: 256 KiB per shell) and a monotonically increasing byte offset;
oldest bytes may be evicted. No output is written to the runner database, artifacts,
History, logs, or disk. Expose a fixed truncation indicator after authorization
when a requested offset has fallen out of the ring. Keep the existing per-session
rate and frame limits on external delivery. A slow connected consumer retains
today's detach behavior. A full bounded ring must not kill a suspended shell.

On adoption, replay and live output share one atomic offset handoff. Runtime and
server rings must identify the same shell and offsets to avoid duplicates or
silent gaps across a reconnect. #268's attachment replay remains useful, but its
server-only ring cannot preserve bytes the runtime produced while disconnected.
Do not route these bytes through the agent channel. Credit frames must also be
epoch-bound; old credit cannot reopen output or create an unbounded send window.

The helper timer, identity monitor, decoder, and cleanup worker must continue
independently of a blocked writer, replay, NSS lookup, and terminal consumer.
Change the current lease-check cancellation paths deliberately; merely removing
the teardown callback would leave other helper paths terminating on lapse.

## #269 reconnect and consent protocol

The same runner and daemon retain in-memory custody and terminal tombstones.
Reconciliation inventories both sides even in #269; it is not deferred to #271.
The authenticated connection still receives a fresh nonce. Before hello, suspend
all old access locally without destroying custody. After authenticated welcome,
compare the runner incarnation, negotiate persistence, then reconcile inventory
and rekey each retained launch. A missing or changed incarnation closes all
retained #269 shells before any new open is admitted; see finding 5 below.
Do not keep an old connection ID to avoid implementing adoption.

1. Fence the departing access epoch and cancel/join its pending operations and
   senders. Retain custody, process budget, and bounded local output. A stale
   socket cannot resume data delivery even while the new socket authenticates.
2. Authenticate the new connection with the same enrolled daemon credential
   generation. Obtain a fresh runtime challenge response bound to the new
   connection, both process incarnations, custody nonce, and candidate shell.
   A name or inventory supplied by the browser is never evidence.
3. Resolve persisted session state and the actual prepared runtime independently.
   Recheck exact owner/login with core, enabled policy and unchanged revision,
   admission phase, runtime generation, manifest, root, machine/account identity,
   consent, and lifetime. Missing/busy authority leaves access suspended; an
   authoritative revoked/ended/replaced answer enters Closing. A pending terminal
   fence wins over a successful slow resolver response.
4. Under the launch rekey barrier, reconcile every shell and reserve one new
   launch access epoch. New opens, configure, and admission are blocked. The
   runtime verifies immutable custody, lifetime, and identity for each claimed
   shell and prepares connection, audience, key, policy, and lifecycle mappings
   together. It acknowledges the prepared launch with all access still closed.
   An old grant cannot authorize this operation.
5. The runner rechecks revocation/policy/runtime epochs after the prepare response,
   commits the launch access mapping, and sends a challenge-bound commit containing
   fresh grants for the eligible children. The runtime commits launch pins and
   per-shell counters once; only the included children gain access. Excluded
   terminal or unclaimed children stay in cleanup. A matching acknowledgment is
   required before server output, browser attachment, or new-shell admission.
6. A lost prepare/commit/ack is retried with the same transaction ID or reconciled
   by querying its exact state. Conflicting prepares refuse. No timeout rolls back
   to the old epoch. If activation cannot be confirmed, suspend and negotiate a
   higher epoch; input is never replayed as part of this reconciliation.

The runtime must reject old-link control replies, input, output credits, and
lifecycle events after epoch replacement. Use cancellation plus epoch checks,
not ordering assumptions about independent network queues. A failed data send
means suspension, not process exit; a natural helper exit still means exit.

Host consent needs a versioned stable scope: original human login, session,
launch and physical generation, enrolled machine credential generation, actual
identity digest, and a monotonic identity/consent epoch. For #269 this acceptance
can authorize only adoption of the exact shell already held in memory at both
ends. Every new host shell requires its own fresh, single-use challenge and
acceptance, even when another tab already has valid adoption consent. A transport reconnect
alone may carry this acceptance forward only after both sides verify this entire
scope. No pending challenge transfers to a new connection. Existing v1
connection-bound consent must require a new explicit affirmation before entering
the new mode; do not silently upgrade old rows.

Withdrawal permanently revokes the acceptance and associated shell generations.
Changing machine, credential generation, account/UID, home, shell, cwd/root, or
observing an identity transition invalidates adoption. Returning to old labels
cannot restore the old epoch. Host consent remains affirmative even for an
administrator; “same person” is not permission to execute on another machine.

`daemon_lost` can remain recoverable beyond 30 minutes only while this runner
process holds eligible nonterminal shell custody for that session and its bounded
absence hold has not expired. Session rows or capability flags alone confer no
exemption. Finding 1 defines the deadlines, including a finite bound for unlimited
shell lifetimes. A real end, archive, discard, replacement, or revocation remains
terminal; reconnect must not clear it.

## Terminal events and cleanup

Explicit shell close, session end/discard/replacement, withdrawn consent, revoked
login/account/enrollment, human login expiry or sign-out, policy disable or revision change, observed identity
invalidity, and lifetime expiry make that custody generation terminal. Paused
workspace behavior remains the existing close behavior. A different peer cannot
adopt it; conflict cannot transfer ownership. Unknown identity due to an outage
suspends access, whereas a verified replacement invalidates prior custody.

On the authority, install a terminal fence before dropping any lock or waiting
for network cleanup. Cancel in-flight adoption, access operations, and browser
attachments. On the runtime, install the fence before starting physical teardown.
Cleanup may be retried without a current access grant: it uses authenticated
custody-scoped lifecycle authority, never an arbitrary expired grant supplied by
a caller. Ordinary input/resize/replay remains denied.

Construct this local cleanup authority at helper bootstrap: the trusted daemon
creates a random 256-bit per-shell cleanup key and gives it to that helper over
a private socket transport, after both processes set Linux dumpable to zero, bound to the exact custody nonce and helper
incarnation. Keep it only in daemon/helper memory, never in grant envelopes,
environment, database rows, browser state, logs, or published diagnostics. Cleanup
frames use a domain-separated MAC over protocol version, operation (`close`),
full custody identity, helper incarnation, and unique cleanup transaction ID.
The helper checks the MAC and exact scope, latches terminal state before teardown,
and treats duplicate transactions idempotently. Fresh custody always gets a new
key, so an old close frame cannot close a replacement. Domain-separated control
MACs also cover every helper request field and a strictly increasing control
sequence, including prepare, commit, suspend, freeze, thaw, and credit. A valid
control MAC never replaces a signed access grant or the daemon's fresh adoption
checks. Epoch advances are positive and at most 1,024 per prepare; rejected jumps
cannot poison the last accepted epoch. Native host Unix socket descriptors cannot be reopened via
`/proc` like pipe descriptors. Docker exec substitutes pipes inside the sandbox;
a same-UID agent can open those pipes before the helper becomes nondumpable,
steal the bootstrap key, and inject authenticated frames. Sandbox v2 is disabled
at capability negotiation, runner selection, daemon admission, and helper
bootstrap/lease creation. Sandbox persistence requires running the helper under
a separate UID and a new security review; v1 sandbox behavior is unchanged. The helper acknowledges process protection before
bootstrap secrets are sent. Its child resolves its own inherited directory
descriptor before exec, preserving both directory pinning and tracer operation.

Authenticated remote lifecycle requests are checked by the daemon against its
custody ledger before it constructs that local close frame. Local runner-mismatch
cleanup uses the same key without a live runner or access grant. Losing the key
on daemon restart does not imply cleanup or permit recovery: use the independent
orphan reconciliation described in finding 5. Keeper key custody and protected
recovery are #271's separate review boundary; #269 writes no cleanup key to disk.

A runtime cleanup receipt must bind the stable custody identity, shell generation,
cleanup transaction, and current authenticated responder. It proves containment
empty, PTYs closed, descendants reaped, and all output senders stopped/joined.
Historical cleanup receipts can settle only their own generation. Existing
connection-bound `SupervisorCleanup` must not be reinterpreted or copied onto a
new connection. Introduce a versioned receipt/adapter for retained custody while
preserving existing workspace physical-incarnation proof checks.

Cleanup errors, dead helpers, missing rows, dead sockets, dead Docker clients,
and timeouts are not success. Keep a tombstone and physical writer/storage fence
until exact independent reconciliation proves cleanup. Terminal state must
survive all retries and supersede any prepared adoption. Do not remove custody
merely to let a fifth shell open. Browser tab close requests terminal cleanup;
if the request cannot reach the server, retain the existing local close intent
and retry it before automatic attachment on return. A UI must not claim remote
termination was confirmed when it was only queued locally.

Workspace syncing still requires a separate bounded freeze/copy/thaw transaction.
An access lapse must not make a successful frozen runtime look cleaned up. Nor
may persistence remove the existing unfrozen watchdog: retain an independently
enforced sync deadline and exact-container cleanup on a failed transaction. Any
change to `FrozenUntil` must distinguish the workspace safety deadline from a
short access lease and be tested with a frozen helper unable to run its timer.
The watchdog must also allow a sync that begins while terminal access is already
suspended; finding 8 specifies the local barrier and independent deadline.

## Security review resolutions

The first cross-model review of `37c867bf` identified eight implementation
blockers. The following rules resolve them in the proposed design; they have not
been implemented or runtime-verified. They are requirements for #269, not optional
follow-up work. Each numbered subsection corresponds to the review finding.

### 1. Recoverable daemon loss is not session termination

Introduce an explicit recoverable `daemon_lost` condition for desktop sessions
with eligible shell custody held by this runner process. It may be a separate
availability field rather than a new session status, but it must carry a typed cause and absence generation independent
of terminal lifecycle state. `daemonLostGrace` may change availability after 20
seconds; `daemonLostWindow` must not set `ended_at`, finalize the session, run
terminal cleanup, or emit an authoritative end after 30 minutes while the
bounded custody hold below remains eligible.
Neither shell authorization nor downstream session-end consumers may interpret
that recoverable condition as an actual end. Do not simply allow every `error`
status through the resolver.

An eligible record is a positively allocated, nonterminal host v2 shell
with persistence negotiated and `DesktopBinding != nil`, still held in this
runner's in-memory custody ledger. A capability flag, persisted row, pending open
without a known allocation, terminal tombstone, or cleanup-uncertain/Closing
record does not qualify. Sessions without such shells keep today's 30-minute
sweep. A runner restart has no eligible records under #269; restored rows cannot
recreate the exemption.

For each absence generation, anchor a fixed deadline to its original
`daemon_lost_at`. The proposed maximum daemon-loss hold is seven days for every
shell, including “until the session ends.” Each eligible shell's hold ends at the
earlier of its finite process-lifetime deadline (if any) and that absence time
plus seven days. A session is exempt only while at least one such record remains
within its hold. Neither renewals, rekey attempts, configuration changes, nor
partial reconnects extend this deadline. Clear the absence generation only after
authenticated runtime reconciliation and confirmed session return; merely seeing
a socket or hello cannot reset it. Access authorization remains separate.

When the last eligible hold expires or custody becomes terminal, remove the
exemption and resume the normal sweep using the original loss timestamp, not a
fresh 30-minute window. If that timestamp is already over 30 minutes old, finalize
on the next sweep. Session finalization terminalizes all remaining shell claims
and queues cleanup, even when physical cleanup is unreachable or uncertain.
Thus an unlimited process lifetime cannot keep its absent session open forever.
Cleanup fences remain until exact proof; they do not postpone the broker's
terminal session result or claim that physical cleanup succeeded. The seven-day
absence cap remains part of the accepted design. With the coordinator's finite
seven-day default, custody lifetime also bounds every initial #269 hold to seven
days or less. Any future longer or unlimited lifetime still needs this explicit
finite absence cap; reconnect must not extend either deadline.

`markDaemonSessionsLost`, the lost-session sweep in `jobreconcile.go`, database
finalization, persisted shell resolution, and session revival must use this
classification. Cluster and legacy nonpersistent sessions keep their existing
policy. Session end/discard/archive, real runtime loss, replacement, and login
revocation still install terminal fences immediately. Updating availability or
clearing an absence marker must compare its generation and must never clear an
independent terminal marker, `ended_at`, or terminal fence installed concurrently.
Do not grandfather already finalized rows as adoptable based on an error string;
#269 adoption requires matching live in-memory custody and no real terminal event.

Acceptance: sleep beyond 20 seconds, 30 minutes, and overnight without a real end
preserves custody and permits fresh adoption within lifetime. Race the sweep and
revival against explicit end/discard; the explicit terminal event always wins.
An ordinary engine error is not automatically classified as recoverable absence.
Also test no-shell sessions, last-shell exit/close, finite lifetime expiry, unlimited
lifetime reaching the seven-day absence cap, repeated partial reconnects, and
runner restart with only persisted rows. Each loses the exemption and allows the
normal sweep/broker completion without clearing physical cleanup uncertainty.

### 2. Typed authority outcomes and outage recovery

Introduce typed `Allow`, `Deny(reason, scope)`, and `Unknown(reason, retryable)`
results across core authorization/control snapshots, persisted session resolution,
remote runtime discovery/RPC, local identity resolution, and manager operations.
An absent value or generic `ErrDenied` cannot mean both denial and outage. The
public error text may remain short; the internal classification must survive all
adapters. Invalid request credentials deny that request; they do not themselves
authorize destruction of a shell named in an untrusted frame.

A core denial requires a versioned typed deny body from the authenticated core
service, bound to the request nonce, exact owner/login, and a fixed reason/scope;
the body must pass schema, size, and correlation checks. A bare HTTP status is not
this evidence: today's plain-text `http.Error` 403 and every untyped 403 or 401
are `Unknown`, not irreversible login revocation. Extend core's response contract
before using such responses to terminalize custody. A valid typed denial, a
verified disabled/revised policy, an explicit session end, or a confirmed identity
replacement is `Deny` for the corresponding custody scope. Network failure, 503,
malformed/unverifiable responses, canceled calls, busy resolution, unavailable database/NSS, and a
missing daemon connection are `Unknown`. Treat a proxy/unrecognized response as
unknown, not evidence that a login was revoked. Definitive missing records from
the authoritative store deny recovery; inability to read the store is unknown.

Change `shellAuthority.Authorize`, `shellResolver`, `remoteShell.describe`,
`Manager.live`, input/renew failure branches, helper request handling, and
`Service`'s `transport_lost` path accordingly. Core stream loss calls a distinct
suspend operation. `Manager.Unavailable` must no longer destroy persistent shells;
returning to available with the same policy revision in `SetPolicy` must not
count as a revision change. Disabled policy or a genuinely different revision
still closes them. Runner shutdown uses a separate terminal operation, below.

Suspension detaches browser access and latches the epoch closed. Continue ticking
suspended and Closing records: local lifetimes, configured inactivity deadlines,
login expiry, authority/revocation sweeps, inventory retries, and cleanup must not
be skipped because `live` returned unknown. Fresh authority permits the launch
adoption transaction, even on the same healthy daemon socket; it does not renew
the suspended epoch. A cached positive snapshot cannot fill an unknown answer.

Acceptance: test a correlated typed deny versus raw/malformed/wrong-nonce 403,
401, and 503; test core stream interruption/recovery with unchanged
and changed policy, runtime absence, and each input/renew/send failure. Unknown
preserves the process and denies access; a verified scoped denial closes it.
Authority and lifetime checks continue throughout suspension.

### 3. Launch-wide rekey, lifecycle identity, and failure scope

`Coordinator` currently pins connection, audience, key, and policy per launch,
and its `launch()` includes the connection. Therefore adoption is a launch-wide
transaction with per-shell eligibility, not a set of independent shell rekeys.
Use the existing per-launch command serialization and lifecycle ordering to
reserve one launch access epoch, close all old access lanes, inventory children,
prepare all new pins, recheck authority, and commit the launch mapping atomically.
New opens, admission, and ordinary configure wait or refuse as busy until this
barrier completes. A new tab cannot call reconfigure and `CloseLaunch` over
retained siblings. Repeated configure for an identical committed mapping is
idempotent; a different access mapping requires rekey. True replacement requires
an independently verified physical launch/root/generation change or explicit
terminal lifecycle, never a new per-shell consent ID. Adding another shell cannot
be interpreted as replacement.

Separate initial orphan recovery from adoption. `RecoverLaunch`'s current
empty-containment prerequisite remains necessary for initial/replacement recovery;
it is not the adoption path and cannot use an empty map or a different daemon
incarnation to clear a retained-custody fence. After a verified daemon restart,
independent exact orphan reconciliation may settle old generations as specified
in finding 5; it grants no adoption rights. Rekey preserves reservations, helper
ownership, terminal tombstones, and cleanup uncertainty. `Binding`, lifecycle dispatch, `Notify`, `CloseLaunch`, and
`Retire` must refer to one committed physical custody registration plus the
current access mapping. Keep historical connection receipts only as settlement
for their exact old identity; never validate one as a new proof. Retirement must
clean every child of the stable launch, including children excluded from rekey,
before releasing storage.

Classify failures before fencing: a grant lapse or old-epoch frame suspends only
the affected shell's access; transport loss or rekey uncertainty suspends the
whole launch. Identity/root replacement, terminal lifecycle, or uncertain physical
cleanup fences the physical launch and blocks new writers. None of the first two
classes may set the existing irreversible generic `c.fenced` cleanup flag. An
ambiguous prepared commit leaves all launch access closed until transaction
reconciliation; it never falls back to the old pins. Terminal events cancel rekey
without waiting behind its network calls and win at commit revalidation.

Acceptance: reconnect with multiple suspended siblings, open a new tab during
prepare, lose each acknowledgment, then retire the launch. No sibling is killed
by configure; all exact lifecycle proofs remain obtainable; uncertain cleanup
prevents new opens and storage release across connection changes.

### 4. Two-way inventory and custody-keyed cleanup fences

Inventory is mandatory for #269 on every reconnect or uncertain launch rekey.
Both peers enumerate bounded in-memory open intents, allocations, live/suspended
shells, Closing records, terminal tombstones, and unresolved cleanup receipts.
Bind the exchange to the fresh challenge, authenticated enrollment, both process
incarnations, launch custody, and transaction ID. Inventory completeness needs a
bounded snapshot revision/count and a final acknowledgment; a partial snapshot
never proves absence. Detect concurrent changes with the revision and retry or
merge terminal changes before commit. The browser never supplies the claim set.

Only a matching nonterminal claim at both ends may be considered for adoption.
A runtime-held allocation with no server claim enters Closing; it is never silently
adopted or left unreachable. A server close intent wins over a runtime live claim.
A server-held claim with no runtime allocation requires exact cleanup/reconciliation
proof; an absent inventory entry alone is not proof of reaping. Mismatched custody
cannot be repaired by copying fields. If inventory is incomplete, keep access and
new admissions closed and retry. Lifetime enforcement continues locally.

Separate active handles from the authoritative custody ledger in memory.
`closeBatchLocked` may remove public handles immediately, but must retain a
Closing/cleanup-uncertain record, budget reservation, and close intent until
proof. A failed open retains its intent/allocation identity until exact settlement,
including when the start acknowledgment is lost. Fences and reservations use
stable physical launch/shell scope, not a connection-bearing launch key. Index
physical cleanup fences by session, selection launch, runtime kind and physical
generation, pinned root/manifest, and enrolled desktop identity. Keep a set of
unsettled custody generations (including old daemon incarnations) beneath that
scope; changing daemon incarnation cannot hide them or allocate fresh slots.
Reconnect does not reset uncertainty, forget a DELETE, or create four fresh slots. Enforce counts
at both peers and conservatively reconcile disagreement before admission.

After cleanup, exchange and acknowledge tombstones without making forgotten
records adoptable. An unclaimed helper under unlimited lifetime is still ordered
to close on reconciliation; partitioned cleanup may remain pending, explicitly
subject to the offline tradeoff above.

Acceptance: disconnect during DELETE, before/after open acknowledgment, and after
handle removal but before cleanup proof. Inject partial inventories and changed
snapshot revisions. Every allocation is either mutually claimed or in bounded
cleanup, counts toward its budget until proven gone, and remains closable.

### 5. Detect runner restart after welcome; close on shutdown

Add a random runner-process incarnation to the authenticated, negotiated welcome
and pin it locally when custody is first established. Bind welcome to the fresh
hello challenge on the authenticated connection. Current `DaemonWelcome`
does not provide this; `DesktopGeneration` is not a substitute. Before hello,
the daemon fences old access and retains custody provisionally. It does not yet
know whether this is a transport reconnect or a runner restart. After welcome,
the same pinned incarnation permits inventory and rekey. Under #269 a changed,
missing, unsupported, or untrusted welcome never permits adoption. A changed or
legacy welcome initiates cleanup of all retained #269 shell custody, even if the
new runner has no records and never requests it. An incomplete handshake keeps
access suspended; finite local deadlines still run. Do not assume absence of a
server request means the process is safe to retain across a confirmed restart.

Cleanup is local and independently tracked by the daemon; inability to deliver
receipts keeps new admission/storage release fenced. The new runner cannot mint
replacement custody onto that physical launch until reconciliation proves the
old shells gone. An old welcome or late message from a replaced link cannot
restore the pinned incarnation or access epoch.

Daemon restart with the same runner is a separate branch: a changed authenticated
daemon-process incarnation moves every old claim for that physical scope to
Closing, cancels adoption, and denies transfer to the new process. Its empty
inventory proves nothing. Preserve the old custody IDs, reservations, and
physical fences across the new registration; do not overwrite them with a new
incarnation or let the new incarnation inherit their cleanup keys.

Only the runtime owner's existing independent orphan reconciliation
(`command.Recovery` / `launch.reconcile`) can settle those exact old generations.
Run it against the recorded physical incarnation and authenticated allocations,
not arbitrary PIDs or the new daemon's empty map. It must prove containment empty,
PTYs closed, and helpers/descendants reaped, and join/retire old transport output
before the runner accepts the exact cleanup receipts. Host reconciliation must
validate recorded helper/kernel identities; sandbox reconciliation must validate
the exact container identity. Failure or missing evidence keeps the old physical
scope fenced and storage retained; successful exact proof settles its old claims
and permits ordinary fresh registration/admission. No old shell is adopted.
A legitimate new physical generation can be registered only through the existing
replacement protocol after old storage/writer obligations are settled. #271 may
later preserve custody through a keeper; #269 cannot claim that exception.

Graceful runner shutdown must explicitly terminalize and close shell custody
before abandoning its transport, with bounded cleanup and retained uncertainty.
It must not rely on `Manager.Unavailable`, which now suspends. Crash shutdown is
handled by the post-welcome incarnation mismatch and local deadlines. Only #274's
reviewed durable protocol may eventually adopt across runner incarnations; a
capability bit alone cannot waive its custody checks.

Acceptance: test same-process reconnect, graceful shutdown, abrupt restart with
an empty manager, legacy/missing welcome fields, and stale welcomes racing a new
link. A restart cannot leave retained helpers forgotten or admit new writers over
them. No old access becomes usable before the post-welcome decision. Restart
only the daemon while retaining the runner and old claims; test successful orphan
reconciliation, missing journal/evidence, and cleanup failure. Old generations
cannot be adopted or forgotten; proven cleanup allows new shells and storage
retirement, while failure keeps the exact physical fence.

### 6. Adoption-only consent, exact login, and withdrawal

Move `Consent` and `HostConsentValid` out of the v2 launch `Binding` and shared
`Command`. Launch configure equality, rekey decisions, `Coordinator` launch pins,
and `Manager.live`'s launch-binding comparison must exclude them. `Resolve`
returns only launch facts; a separate exact-login, per-shell custody check loads
and validates that child's consent. Do not replace `b.Consent` with another
launch-wide “current acceptance” field.

Each shell custody record and helper bootstrap holds its own consent acceptance,
ID, identity epoch, and login. Its signed grant carries that per-shell consent
ID, verified against that child's custody rather than `c.binding.Consent`.
`Manager.live` still validates the shell's own nonrevoked acceptance on every
operation/adoption; excluding launch equality is not skipping consent checks.
New-shell affirmation populates only the new child's bootstrap/custody. Two
siblings may have different acceptance IDs on one launch without reconfigure,
launch rekey, replacement, or sibling cleanup. Withdrawal scopes cleanup to the
acceptance's child records; a real launch identity change still invalidates all
children. Test a second tab with a fresh consent while its sibling is active and
while suspended, plus withdrawal of just that tab's acceptance.

For #269, a v2 acceptance is only evidence for adopting the specific host-shell
custody already held in both processes. It never authorizes a new shell from a
persisted row. Every new host-shell allocation needs a fresh single-use challenge,
exact-login affirmation, and live runtime identity check; the runtime must retain
its own matching accepted challenge/allocation evidence, not merely compare
fields of a supplied consent object. The runner's acceptance handler records the
fresh authenticated affirmation against its in-memory pending challenge before
sending the runtime a custody-bound acceptance. Database lookup cannot recreate
that affirmation or authorize a new allocation after its one permitted use.
A restored acceptance or revoked-at value
cannot generate that missing in-memory evidence. A restart loses eligibility
unless the later custody protocols are installed.

Public v2 acceptance explicitly carries `version: 2`. An unversioned cached
browser cannot affirm the new offline lifetime with the old launch-wide dialog;
its request is refused without consuming the challenge. The v1 body stays
unchanged when a current browser talks to a v1 runtime.

Use a separate versioned table and handlers for v2 acceptance. Old
`shellConsentIdentity` connection-change updates must not read, revoke, or promote
v2 rows, and v1 lookups cannot find them. Mixed-version/rollback deployments
refuse the new persistence capability and do not reinterpret v2 consent as v1.
All v2 lookup, attach, inventory, and adoption paths require the original nonempty
login explicitly; `Resolve` may not fetch consent using `""`. Neither another
login for the same owner nor an administrator may inherit it. Expiry and sign-out
of the original human login are terminal, including while access is suspended;
re-login requires new shells and new consent.

Add an authenticated, same-origin, exact-owner-and-login withdrawal endpoint and
an accessible “Withdraw host shell access” action in #269's UI scope. Withdrawal
is available while the desktop is offline: authorize against the session and
custody ledger without requiring a live describe. It installs a permanent
acceptance/identity-epoch tombstone and Closing intents for all associated shell
generations before responding, immediately detaches access, then retries physical
cleanup. It is idempotent and cannot clear its tombstone by accepting a new
challenge. A delayed accept/adopt rechecks the epoch after withdrawal. The response
must distinguish accepted denial from confirmed physical cleanup. #269 may keep
its authoritative shell fences in memory, but persists the withdrawal marker;
runner restart still closes all old custody by finding 5. #274 supplies the
independent durable anti-rollback authority before restart adoption is enabled.

Acceptance: restore an old row and revoked marker, switch login, call old lookup
code, withdraw offline, and race withdrawal against challenge acceptance, open,
and rekey. No new allocation or adoption follows from the restored row; the
withdrawn generations never become usable again.

### 7. Natural exit and identity observations have distinct outcomes

Replace the current “unexpected exit means identity dirty” coupling with typed
helper outcomes: natural exit with physical cleanup proof; verified identity
change; identity unknown; helper/control failure with cleanup unknown. A normal
shell `exit` closes and joins that shell, retains its exact receipt, and frees
only its reservation after proof. It does not set `consentDirty`, rotate launch
pins, or close siblings. A helper crash is not a natural exit proof: fence the
physical launch while independently reconciling uncertainty, without falsely
claiming that the OS identity changed.

Define stable desktop machine identity as the authenticated enrollment binding:
`DesktopBinding.DesktopID`, `OwnerID`, ownership `Revision`, and credential
`TokenID`, pinned with the daemon's random process incarnation. Here “credential
generation” means this credential ID plus ownership revision, not the per-link
`DesktopGeneration`. It is conservative: credential replacement/re-enrollment
requires new custody. Gate persistence positively on `DesktopBinding != nil` and
validation of that enrolled binding. Testing only `!LegacyDesktop` is insufficient:
compatibility desktops without that binding also refuse persistence. A verified
token expiry/revocation is terminal; failure to query its authority is unknown. Hostname is display metadata,
not machine identity or a consent digest input. Sandbox custody also pins the
physical container identity; copying enrollment labels into a container proves
nothing without the authenticated desktop connection and trusted preparation.

Split local identity resolution into confirmed match, confirmed mismatch, and
unknown. Pin UID/account, actual absolute shell/home, cwd and root/member kernel
identities. NSS failure or stat I/O failure is unknown: suspend access without
dirtying consent or killing siblings. Failure to resolve a display hostname is
ignored for authority and cannot suspend or invalidate custody.
An authoritative missing/replaced pinned directory or a successfully observed
account/shell/home change is mismatch: invalidate the launch identity epoch,
close its shells, and require new consent. Never adopt on an unknown result.
Once a transition is observed, returning to earlier values cannot restore its
epoch. The helper identity monitor and `refreshHostIdentity` must implement the
same classification rather than using `err != nil` as identity change.

Acceptance: exit one of several shells, crash its helper, inject transient NSS
and stat failures, change the display hostname on wake, and observe identity
A → B → A. Normal exit and label changes preserve siblings; unknown denies
access without permanent identity invalidation; verified change is terminal.

### 8. Sync while suspended uses a local barrier and its own watchdog

P2's local `Notify(syncing)` must fence shell access, cancel/join pending writes,
and install the phase under the launch lifecycle lock before it returns. Remote
`Signal` delivery is a best-effort notification after that local barrier; a dead
link must not fail the local phase transition or set the physical cleanup fence.
Record the phase for the next authenticated snapshot, without queueing terminal
bytes or making transport delivery a prerequisite to freezing. Genuine failure
to establish the local barrier still refuses sync and retains writer fencing.

The freeze watchdog gets an independently measured workspace sync deadline from
`DefaultWorkspaceBudget().SyncSeconds` and the actual transaction start, not the
minimum access-grant expiry. Start that budget before the local barrier; slow
setup cannot reset it. `FrozenUntil` must return the remaining workspace deadline,
not a past grant expiry. This applies when every shell was already suspended
before sync starts. Access stays suspended throughout; successful thaw must not
restore a lapsed grant. If finite shell lifetime expires during freeze, mark it
terminal and use exact physical container cleanup when the helper cannot enforce
it; that lifetime deadline may shorten the watchdog, whereas an access lapse may
not. Expired lifetime remains distinct from an invalid access grant.

The unfrozen daemon monitors the deadline, confirms unpause and current root/
incarnation before resuming writers, and performs the existing exact-container
fence/cleanup on timeout or failed thaw. Keep processcontainment unchanged.
A missing network notification alone must never cause `fencePhysical` to kill a
healthy agent container. Conversely, a failed local barrier or actual workspace
safety timeout must not be hidden as an access-only suspension.

Acceptance: start sync with a dead link, with expired grants on a healthy link,
and with all shells already suspended; complete copy/thaw within the workspace
budget without killing the agent container. Separately exceed the independent
budget, expire a finite lifetime while frozen, and fail unpause/root validation;
verify the exact existing physical cleanup and storage fences remain effective.

### Second security review disposition

The re-review of `132b2f28` found three interactions requiring clarification:

| Finding | Resolution in this proposal |
| --- | --- |
| A: per-shell consent versus launch-wide binding | Finding 6 moves consent into child custody/grants; finding 3 excludes new consent from rekey/replacement triggers. |
| B: daemon restart with surviving runner claims | Findings 4–5 preserve physical fences and old generation records, enter Closing, and require independent exact orphan cleanup before fresh admission. |
| C: unbounded daemon-loss exemption | Finding 1 limits eligibility to this runner's allocated nonterminal custody, bounds holds by lifetime and a seven-day absence cap, and resumes the original 30-minute sweep otherwise. |

These findings were resolved in design v3, which the coordinator accepted for
#269 implementation. The defaults in the status above apply pending the owner;
they do not authorize #271/#274 implementation or substitute for code review.
The additional re-review tightenings are incorporated into findings 2, 7, and 14,
local cleanup authority above, and the lifetime configuration contract.

### Additional review findings 9–14

These supporting requirements also belong to #269:

- **9, stale frames:** reset runtime input/resize counters at epoch commit; reject
  stale/invalid grants or sequences with a scoped refusal, never shell teardown.
  Repeated malformed traffic may close that transport and suspend access. Replace
  helper `close`'s expired-grant acceptance with authenticated custody-scoped
  lifecycle authority. No ordinary stale frame can invoke it.
- **10, output integrity:** the helper assigns stream offsets before any forwarding
  and keeps bytes in its bounded ring until acknowledged by the server, subject
  to explicit oldest-byte eviction. Add offset, acknowledgment, epoch, and gap
  fields to the versioned wire contract; downstream stages preserve offsets.
  Credits authorize capacity only; they are not a receipt for data retention.
  Dropped coordinator/server queues cannot silently advance an acknowledgment.
  On reconnect, report the helper's retained range, reconcile the server's last
  contiguous offset, and replay from that point. A gap caused by eviction is
  explicit, including when the ring is no larger than an in-flight window.
- **11, clocks:** latch access lapse independently of wall time. Before advertising
  persistence on Linux/WSL or Docker Desktop, test real host sleep as well as
  clock steps. A VM clock that pauses across host sleep cannot satisfy the finite
  lifetime guarantee by assumption. A runtime without a verified sleep-aware
  time source must refuse persistence capability, not silently use Go monotonic
  time. Platform evidence is an implementation gate, not a result of this review.
- **12, suspended tick:** run lifetime, inactivity, authority, and cleanup sweeps
  over all custody records regardless of access liveness, as required in finding 2.
- **13, same-link recovery:** a core outage uses launch prepare/commit on the
  existing authenticated socket with a fresh challenge and higher epoch; sequence
  reset, inventory, revocation checks, and output handoff are unchanged.
- **14, enrollment:** use the explicit enrollment tuple in finding 7; legacy
  desktops refuse persistence, and connection generations never stand in for
  credential generations.

## #271: keeper ownership and restart adoption

The keeper owns helpers, PTYs, containment tracking, lifetime timers, rings,
identity monitoring, terminal fences, and cleanup. The daemon owns the server
transport. Daemon death suspends access without killing the keeper; keeper death
continues to fail closed through the installed containment backend. Do not remove
parent-death protection from helpers and leave them unsupervised.

A private local authenticated channel must bind the daemon's enrollment identity,
OS peer identity, keeper incarnation, and fresh challenge. Socket ownership alone
is insufficient; peers must prove custody using material inaccessible to an
unrelated runtime/container. Local protected custody records and terminal journals
must be authenticated, versioned, atomically installed, and checked before serving
requests. No secrets or raw terminal output belong in those records.

A restarted daemon inventories candidates from the keeper, then uses the same
server adoption protocol. A candidate is not permission to execute. If #274 is
not installed, the original runner must still retain its matching custody record.
Reinstall without the original protected identity material requires fresh consent
for new shells and must not adopt old ones by matching labels. Keeper upgrades
may wait for shells to finish or explicitly terminate them with cleanup; migration
of live tracer ownership is outside this design. The version handshake must refuse
unsupported recovery rather than silently start a replacement keeper over writers.

## #274: durable custody and denial

The runner's database becomes an index of candidates, not authorization. An
opening record must be authenticated by a custody verifier outside that database
writer's control. Its authenticated envelope covers immutable custody, consent
scope, policy revision, lifetime, record version, and authority epoch. An HMAC
stored beside the row, or a signature that accepts an old valid snapshot, does
not meet this requirement.

The verifier needs an independent, durable, monotonic ledger of opening state,
terminal denials, cleanup uncertainty, and committed adoption epochs. A proposed
home is a core custody service with authenticated internal calls and a separately
protected ledger. This is a required new trust boundary, not a claim that today's
core endpoints or another table in the writable runner store provide it. The
review must settle its storage, backup/restore, and replica-fencing implementation
before #274 code begins. Compromise or rollback of this independent authority is
outside the runner-database-writer threat model and must be documented operationally.

Every adoption consults the current verifier state and current login, policy,
session, and runtime authority; verifying an opening signature alone is
insufficient. Session-end/consent-withdrawal/revocation acknowledgments must follow
commit of their denial at this authority, not just a runner row update. If that
commit is unavailable, deny access immediately and retain a pending terminal
fence; do not acknowledge a durable end or permit recovery based on a stale row.
Verifier failure keeps access closed. Deleted records cause denial and reconciliation,
never a new custody allocation for the surviving process.

The recovery transaction must handle these crash points explicitly:

| Crash point | Required recovery |
| --- | --- |
| Opening intent committed, process not started | Reconcile the reserved generation; do not assume a process exists |
| Process started, start acknowledgment lost | Runtime reports authenticated allocation; no duplicate start; deny access until exact reconciliation |
| Open finalized, runner row missing | Recover only from verifier plus runtime evidence; a replacement row grants nothing |
| Close/revoke committed, cleanup not sent or acknowledgment lost | Terminal denial survives; retry exact cleanup; no adoption |
| Adopt prepared, commit absent | Access stays closed; resolve transaction at verifier before a higher epoch |
| Adopt committed, acknowledgment lost | Query exact committed epoch; never reactivate the previous one |
| Cleanup proved, row deletion incomplete | Retain terminal verifier tombstone; idempotent cleanup settlement |
| Old database backup restored | Reject obsolete authority/access epochs and terminal generations |

The verifier must fence concurrent runner replicas so only one adoption epoch can
commit. Durable compare-and-swap and fresh runner-incarnation authorization must
replace reliance on a process-local mutex. Old runners and restored snapshots
cannot keep issuing accepted grants after authority transfer. Runtime epoch/key
fences are part of that transfer, and unresolved transfer means no access.

Never automatically forget terminal verifier entries on a time-to-live alone.
Compaction requires proof the generation can no longer be replayed or adopted,
including retired runtime/keeper credentials and backup restoration boundaries.
Bound creation/adoption attempts and index scans. The exact retention and ledger
compaction format are #274 review requirements, not shortcuts to defer until after
shipping recovery.

## What remains closed on failure

- Unknown capabilities, protocol versions, owner/login, policy, consent, root,
  identity, incarnation, epoch, or custody evidence never permit access.
- Busy/unavailable authority suspends; it does not renew from cached approval.
  A verified denial is permanent and starts cleanup, not suspension with revival.
- A new transport, process, machine, or enrollment cannot inherit authority by
  copying IDs. Cross-runtime and cross-account adoption refuse.
- Old browser handles are lookup tokens only. Every attach and replay needs fresh
  exact-login authorization. A new login cannot inherit the earlier login's ring.
- No successful timeout, empty map, or output EOF substitutes for cleanup proof.
- No persistence setting bypasses workspace admission, lifecycle fences, process
  budgets, host consent, capability negotiation, or containment checks.

## Implementation and verification gates

The cross-model review must approve the offline-execution tradeoff, stable consent
scope, custody/access separation, epoch commit protocol, lifetime policy, and the
independent durable verifier boundary. Resolve findings in this document before
changing security-sensitive code. #271 and #274 require their own detailed storage
and local-channel review before enabling those restart guarantees.

For #269, write failing tests before implementation across `ownershell`,
`shellruntime`, daemon connection handling, consent persistence, and server remote
shell transport. Include real runtime integration where containment is material:

- Sleep/time jumps beyond grant expiry preserve desktop processes but deny every
  access operation until adoption; cluster grants still expire destructively.
- Renewal cannot revive an expired access epoch; explicit adoption can, while
  terminal generations remain terminal. Deadline expiry counts suspend time and
  cannot be extended by reconnect, renewal, backward clocks, or repeated adoption.
- Same daemon/runner reconnects with fresh connection/key/epoch and original valid
  consent. Changed login, credentials, identity, root, manifest, session state,
  generation, or consent refuses. Observed identity change then restoration also
  refuses. v1 consent is never silently promoted.
- Close/revoke/disable races every resolver, prepare, commit, acknowledgment, and
  first input/output. Revocation wins, including replies from replaced sockets.
- No queued stale input, resize, credit, or output crosses an epoch. Lost input
  acknowledgment does not cause resend. Blocked writers cannot hold expiry,
  identity monitoring, or cleanup. Replay offsets have no unexplained gaps or
  duplicates; eviction is explicit and rings stay bounded during long absence.
- Four suspended shells consume four reservations. Cleanup uncertainty blocks
  replacement/storage release; proven natural exit frees only its own reservation.
- Frozen sandbox helpers, sync timeouts, unpause failure, and reconnect during
  sync preserve the independent watchdog and exact physical cleanup obligations.
- Mixed versions select conservative behavior; daemon or runner restart without
  the corresponding recovery capability does not claim successful adoption.

For #271, add keeper/daemon crash, stale local record/socket, reinstall, peer
spoofing, upgrade mismatch, and helper parent-death tests. For #274, inject crashes
at every row in the recovery table, plus forged/edited/deleted/replayed rows,
restored backups, concurrent replicas, verifier outage, and failed denial commits.

Run touched Go packages normally and with `-race`, pinned lint, documentation link
checks, and scrub. UI changes require unit/browser coverage and 390 px and
3840 px layout checks. Capability advertisement requires the #269 runtime gates
to pass. Code security review remains required before release.


## #269 implementation evidence

The following tests exercise the accepted boundaries; final suite results and
commands belong in the builder's verification report.

- `TestManagerServiceReconnectKeepsSameShellAfterThirtyMinutes` uses a real traced
  helper and the public Manager API. It advances both clocks by 31 minutes,
  reconnects through complete inventories and a fresh key/epoch, reads the same
  shell variable, and verifies that only one helper started. Existing custody can
  reauthorize after a transient installation-probe failure; replacement helpers
  remain refused until positive admission capabilities return.
- `TestPersistentHostConsentOpenAndOfflineWithdrawal` exercises the HTTP, database,
  and Manager integration. A valid v1 acceptance does not satisfy v2. A fresh
  acceptance admits one child, cannot serve a second tab, remains discoverable
  offline, and can be withdrawn without describing the daemon. Its real
  daemon-loss path keeps a positively allocated shell's session past 30 minutes;
  Closing custody loses that exemption. A provisional `ended_at` on daemon loss
  is distinguished from a final end reason.
- `TestManagerRetainedRevocationWinsReconnectAcknowledgments` blocks inventory,
  prepare, and commit acknowledgments independently. Revocation removes the
  handle before those acknowledgments can restore control. Old streams cannot
  cross rekey in `TestManagerRetainedOldStreamCannotCrossRekey`.
- `TestPersistentHelperRequiresFreshIdentityForAdoption` refuses prepare during
  an account-resolution outage and accepts a higher epoch after fresh resolution.
  The helper checks identity at control/adoption boundaries as well as in its
  independent monitor. Discovery of retained helpers uses private inspection,
  not the legacy hostname comparison or whole-launch recovery.
- `TestRetainedColdCleanupNeedsIndependentProofAndCannotKillLiveSibling` refuses
  to turn absence into proof or recover a namespace containing a live sibling.
  `TestRecordedShellProofIncludesRetainedCurrentAndHistoricalCustody` prevents a
  nil v1 coordinator from hiding retained writers during storage settlement.
  `TestRetainedNewIdentityEpochNeedsClosedProofAndNewConsent` allows a fresh
  identity epoch only after every old child has cleanup proof, without clearing
  native terminal fences or reviving an old acceptance.
- `TestPersistentWatchdogCountsSleepAndRejectsBackwardClock` exercises the
  unfrozen owner's elapsed-clock watchdog. Access expiry does not supply its
  deadline, and suspended custody is still subject to the watchdog and lifetime.

Consent listings contain denial metadata, never terminal bytes or adoption
credentials. They are paginated and restricted to the original owner/login.
Withdrawal status distinguishes a committed denial from physical cleanup. A
missing process ledger or restored database row cannot report cleanup complete;
completion requires current in-memory cleanup proof. The UI offers this path even
when no terminal or desktop connection can be restored, and labels replay
history eviction separately from terminal bytes.

Cleanup retries are bounded and paced. A Closing child still consumes its slot
and fences replacement writers, but complete fresh inventories can reauthorize
healthy existing siblings. An approval that started before an observed core
control outage cannot authorize access after recovery; a new typed approval is
required even when the policy revision is unchanged.


### Code security review follow-up for #269

- A missing welcome suspends access and leaves custody alive; an invalid current
  welcome or changed custodian is terminal. Each reconnect runs `Rebind` before
  hello. A callback bearing an old hello cannot invalidate the current welcome.
- The daemon and helper independently enforce the seven-day desktop ceiling.
  The runner's shorter original login and enrollment deadlines still apply.
- Display machine names are optional presentation data outside identity digests.
  If the label cannot be read, consent says “Desktop.”
- Consent challenge requests have a four-per-minute owner bucket, 32 pending
  challenges per owner, and the existing global bound. Expired unaccepted rows
  are pruned under serialized per-owner database admission. Accepted withdrawal
  tombstones remain denial metadata.
- Closing clears runner terminal history. A cleanup proof releases only its own
  allocation; a sibling's unsettled cleanup keeps the physical launch fenced.
- Custody and physical scope include the prepared container identity, supplied
  by the trusted runtime command; a changed container cannot match old custody.
- Audit events cover consent acceptance, withdrawal, and adoption outcomes with
  the exact login, digest, and access epoch. Before allocation, `custody_kind` is
  `consent_pins` and epoch is zero; after allocation it is `allocation`. This
  avoids pretending that acceptance has already created a process. Audit records
  contain no terminal bytes, display names, directories, or bootstrap secrets.
- A helper's closed frame alone is insufficient for cleanup: the daemon joins
  helper exit and output forwarding and requires a clean exit before constructing
  a transport-correlated receipt. This retains the existing trusted-helper image
  assumption for legacy v1 sandbox shells. Sandbox v2 is disabled independently
  of the separate L2 owner decision.

### Verification follow-up: native transport and clock scope

Starting the first v2 host helper makes the daemon nondumpable for the rest of
that daemon process, as well as protecting each v2 helper before bootstrap.
This disables core dumps and ordinary same-UID `gdb` or `strace -p` attachment;
`lsof` cannot inspect its protected descriptors. Operators need a daemon restart
to restore the original dumpability setting; closing the shell does not undo it.
This does not disable the helper's tracing of its own contained shell children.

The kernel-name check is a denylist, not a virtual-machine detector or a complete
host-sleep verification gate. Recognized WSL and Docker Desktop kernels are
refused. Lima, UTM, and unrecognized guests may pass; their host-pause behavior
is not covered by the Linux CLOCK_BOOTTIME guarantee. This slice makes no sleep
survival or elapsed-time enforcement claim for those host-paused guests.

### Verification follow-up: retry and terminal cleanup

The runner fixes custody at seven days, or the earlier original login/enrollment
expiry. Desktop admission allows five seconds of wall-clock skew, matching grant
issuance tolerance; it never changes the signed deadline. The helper enforces
that deadline with wall and suspend-aware elapsed clocks. Refusal at the daemon's
clock check happens before consent use, registry reservation, or helper creation.
Only its transport-correlated, explicit no-allocation reply lets the runner drop
its still-Reserved slot and release the unused acceptance. Lost replies, failed
bootstraps, and uncertain cleanup cannot use this path. A database-read failure
also releases the broker's preallocation reservation; a withdrawn acceptance
remains denied. Other initialization failures release consent before returning
if no custody has been reserved.

An incomplete welcome suspends every launch even if one output join fails.
Legacy recovery cannot clear a physical fence while any retained child there
remains Closing. Withdrawal denial and its opaque audit snapshot commit in one database
transaction. Repeats preserve the original snapshot and still drive cleanup;
a crash before the optional log mirror cannot lose the durable event. Busy adoption requests emit no
adoption event, and revocation during adoption emits a denied adoption event
separately from any actual terminal cleanup. A sandbox reconciliation probe that
cannot inspect a same-UID process because of permissions refuses to prove the
runtime empty, without producing a Python traceback.


### Real-wire verification gate

The Linux `persistent_shell_test` build tag enables only the named Go test clock
seam. Ordinary builds exclude it; an accidentally tagged executable also cannot
activate it without Go testing flags. The helper subprocess retains real identity
resolution, bootstrap, private transport, ptrace containment, lease verification,
and lifecycle control. The test clock substitutes the kernel probe and advances
wall and elapsed time together; it does not assert real host-sleep behavior.

`TestPersistentShellRealWireReconnectAndWithdrawal` uses the real HTTP API,
`ServeDaemon` websocket reader and `handleDaemonFrame`, daemon `WSClient`, Service,
and helper. It covers consent challenge and acceptance, authenticated
AllocationRefused retry, open, browser input/output, a 31-minute virtual absence,
reconnect to the same shell process, and withdrawal with a durable allocation
audit. It runs outside the session only because the real helper needs ptrace.
V2 results and output must cross the same ingress as v1; early refusals must
retain the request version. Legacy lifecycle phase/credit traffic remains v1.

### Shell names and reports (#328, #324)

Shell names are optional presentation metadata, stored by shell generation, session,
owner and original login in migration 104 (`shell_names`). They are independent of
handles, grants, retained custody and terminal output. The live owner may rename a
shell through `PATCH /api/shells/{handle}` with `{ "name": "Test watcher" }`; the
open endpoint accepts the same optional field. Empty names restore the default and
delete the stored row. Closing a shell deletes its stored name. Names and takeover
remain scoped to the same browser sign-in.
Names contain at most 120 characters and no control, format, line-separator or
paragraph-separator characters. Inventory reloads
names from Postgres, including after retained-shell adoption by a new runner.
Agent credentials still cannot open owner shells; the optional open name is ready
for an authorized caller if that capability is introduced.

Double-click a shell label, use its actions menu, or choose **Rename shell** in
Ctrl+K. Enter saves, Escape cancels. Names follow tabs into splits, overflow,
command search and ended views. Program-supplied OSC titles do not replace names.

**Copy details** on an ended or disconnected shell copies a plain-text report.
The browser keeps the last 20 structural events in memory with UTC and local times.
It records connection states, inventory probe results, visibility, network changes,
page wake notifications and explicit control handoffs. Handoffs carry a coarse platform category and an optional random browser-profile
label shared by that profile’s tabs, never a hardware fingerprint, User-Agent or
network address. The label is presentation only and grants no authority. Browser-profile labels remain in memory for handoff notices and never enter server
logs. Runner logs record
attach/detach by shell generation; owner-shell audit logs record takeovers with the
actor, login, shell and outcome, and terminal end reasons. `GET /api/shells/{handle}/details` retrieves a bounded
end-reason tombstone for the exact original login (24 hours, at most 1024 entries),
so copying can recover a reason missed by the browser. Restarting the runner clears
these tombstones; audit logs remain the source for older events.

Reports have no slot for terminal contents, typed input, environment, paths,
authentication, or arbitrary error text. Unrecognized WebSocket close reasons are
omitted. Shell names and desktop names have no slot in reports; IDs identify the
shell, session and desktop. This supersedes the card expectation that the name
appears in the shell report.
Unavailable version, exit-code, signal and device metadata is labeled
unknown or unavailable rather than inferred. The current runtime wire does not
expose shell exit status separately from runtime closure. Browser wake events are
best-effort page lifecycle observations, not proof of operating-system sleep.
