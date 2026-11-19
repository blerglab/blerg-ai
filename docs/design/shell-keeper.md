# Shell keeper (BLG-271)

**Owner rule (2026-11-02): agents must never depend on the keeper.
Keeper or bridge failure costs shells only.** The daemon and agent turns keep
running when keeper state is missing, the keeper cannot start, or its bridge
disconnects. Host shells refuse with a plain reason until authenticated keeper
service returns. Reconnection runs independently of agent recovery; shell
adoption never gates agent admission. Shell readiness is scoped to the exact
session, so one workspace's suspended shells cannot fence another workspace.

The boundary has three rules (owner's round 8):

1. **Installation never depends on the keeper.** Replacing the daemon image
   performs no keeper IPC, locking, initialization, or custody decision. Open
   shells, hangs, damaged state, directory modes and builds cannot veto a daemon
   install or restart. A running keeper retains its executable inode, including
   `/proc/self/exe` for helpers. Compatibility is the authenticated protocol
   version, never exact build equality. A compatible daemon adopts its shells;
   an incompatible keeper remains alive, with a plain notice that shells need
   reconnecting or restarting. Background maintenance replaces an old keeper
   only after its custody is empty; installing never waits for that maintenance.
2. **Retirement is durable intent.** Before host Stop, discard, or legacy close,
   the daemon first attempts to fsync an idempotent workspace retirement record in its own state,
   outside keeper identity state. It is keyed by workspace, with no mutable
   manifest digest veto. Only the authenticated installation can submit it.
   A failed store write logs its cause and retains intent in memory: it never
   blocks agent Stop or Kill. Recovery retries persistence and delivery with
   exponential backoff. Invalid records are quarantined individually with safe
   diagnostics; an owned directory with unsafe modes is repaired to 0700.
   A store failure does not disable unrelated shells or launches. Keeper retirement detaches and fences the launch immediately;
   physical cleanup remains tracked separately until proved, so a pending
   cleanup cannot consume one of the 128 active registration slots. A daemon
   restart cannot forget intent or revive the retired launch.
3. **Liveness is observed.** Heartbeats have a dedicated server path outside
   ordinary request queues and call slots. Each has a 500 ms deadline; three
   consecutive misses spanning at least two seconds mark the bridge unavailable,
   close that transport and reconnect. A successful heartbeat resets misses.
   Snapshot revisions prevent a stale heartbeat from overwriting an acknowledged
   shell open. Deadline errors remain distinct from custody refusals.
   Agent lifecycle methods only update local state and queue shell control;
   they never hold a workspace lock across keeper IPC. Shell operations report
   and log `keeper_unavailable` while unavailable. Session adoption policy
   refusals never masquerade as transport loss or rebind unrelated sessions.

While the keeper is unavailable, or the workspace has not yet been observed in a
heartbeat snapshot, shell occupancy is unknown, never proven empty.
Membership uses the distinct `keeper` wait code on unknown occupancy, never the
claim that an open shell exists. It waits five seconds for observation. Only a
signed fresh workspace journal recording `host_keeper_never_opened=true` can
then allow membership without keeper service. The daemon clears and fsyncs that
flag before forwarding any host open, under membership admission. Old journals
without the flag stay unknown; neither a deadline nor a missing registration
proves emptiness. No workspace mutex spans keeper IPC. Already committed
membership replay updates the local launch binding while offline; reconciliation
proves the old custody empty before replacing its registration. Live custody
refuses that reconciliation rather than being retired implicitly. A persisted Stop intent
always reconnects through terminal Stop recovery, never live shell recovery.

Status: implementation in slices; owner decisions recorded 2026-11-02 below. Builds on
[Persistent owner shells](persistent-shells.md), whose `#271` section
already sketches a keeper; this works out the mechanics. #269 (same
daemon/runner, sleep, transport reconnect) is implemented; #271 (daemon
restart/reinstall) is implemented through slice 3 below; #274 (runner restart,
durable custody) remains separate. The owner's installed-host drill remains pending.

## The hard part: who is the tracer when the daemon dies

Before keeper supervision, daemon exit caused instant teardown, by design.
Each shell's root is seized with `PTRACE_SEIZE` plus `PTRACE_O_EXITKILL`
(`processcontainment/tree_linux.go:855`): the kernel kills every traced
task the instant its tracer thread exits, no grace period. The tracer is a
per-shell **helper**, `Pdeathsig: syscall.SIGKILL` bound to the daemon
(`shellruntime/parent_linux.go:19`); the daemon's unit uses `KillMode=
mixed` (`blerg-runner-daemon.service.tmpl:17`), killing anything left in
its cgroup on stop. Previously: daemon exits → kernel `SIGKILL`s every
helper via `Pdeathsig` → `EXITKILL` fires on each seized tree → every
shell is gone before a new daemon runs a line of code.

Because of `EXITKILL`, containment cannot survive by attaching a new
tracer after the old one is gone — the kernel kills the tree first. The
tracer has to never die with the daemon, so it must live in a third,
separately supervised process: the keeper. #271 is a supervision problem,
not a tracer hand-off protocol — keep the tracer(s) alive and reachable
across a daemon restart, and let the restarted daemon prove it may talk
to them again. The tracer algorithm stays unchanged; cleanup reuses its
before-execution process ledger through a dedicated keeper-owned channel.

**Measured, `/tmp/cx/outside.sh` (real ptrace, owner's run):** a seized
shell died within 3.9 ms of `kill -9` on the daemon and 10.7 ms of
`systemctl restart`; no survivor seen either way. That times process
death only, not proof the reap loop or cleanup (ring flush, socket close,
cgroup teardown) finished — a follow-up measurement should assert cleanup
completion, not just "dead fast."

## Ownership: the keeper is a broker, not a second seizer

Earlier drafts had the keeper itself call `PTRACE_SEIZE` for every shell.
That doesn't fit the existing tracer: `Tree.Run` must execute on "the
helper's sole child-reaper thread" (`tree_linux.go:219`) and reaps with a
wildcard `waitTask(-1, ...)` (`tree_linux.go:570`). `PR_SET_CHILD_
SUBREAPER` is process-wide, not per-thread, so one process running
`Tree.Run` for two shells could have either tree's wildcard wait reap the
other's tasks — no way to scope `wait4(-1)` per subreaper across several
trees in one process. The design keeps **per-shell tracers** unchanged:
each shell still gets its own helper process running `Tree.Run`.

What moves is who parents and reaches those helpers. The keeper is one
broker process per host install: it becomes each helper's `Pdeathsig`
target instead of the daemon; holds the custody index, PTY handles, and
scrollback rings from persistent-shells.md; runs the lifetime/identity
monitor with no daemon attached; and answers the daemon's hello/inventory
request. It never calls `PTRACE_SEIZE` or changes the helper's reaper algorithm.

**Owner decision (2026-11-02):** each helper's `Pdeathsig` stays bound to
the keeper. A keeper crash kills every brokered shell. Helpers do not
outlive the keeper; further decoupling is outside this work.

## Mutual authentication, singleton ownership, and epochs

Reaching a socket is not proof of keeper or daemon identity. A
self-minted keeper generation is not proof either — nothing stops an
impostor from creating the socket first and announcing "generation 1."
Three changes close that:

- **Mutual signatures.** The daemon hellos as before; the keeper now signs
  back over a daemon-issued nonce, using an identity key generated once at
  install and persisted at mode `0600`, never regenerated by a reinstall
  (`install.sh:404` swaps `$BIN_PATH` atomically but must not touch it).
- **Atomic singleton ownership.** The keeper takes an exclusive `flock` on
  a lock file beside the socket *before* binding it, held for its
  lifetime; the kernel drops it the instant the holder exits, so a second
  keeper gets an immediate answer, not a guess. No lock, no bind.
- **Daemon-authenticated epoch.** The epoch is minted and signed by the
  daemon, not self-asserted, and persists across the daemon's own
  restarts. A known, correctly-signed prior epoch is a continuation;
  anything else is cold start, with no implicit custody.

**0.6.0 threat model (owner confirmed 2026-11-02). Sandbox sessions, cluster sessions,
and other host UIDs are in scope. A same-UID host process is out of scope.**
A same-UID host process can read the private key files (including the old
`identity.json` format), kill the keeper, or read process memory. This protocol
does not sandbox that host process. Sandbox and cluster sessions do not gain
this exemption even if a runtime maps their account to the host UID: they must
not receive host keeper state, sockets, keys or process access through their
mounts, descriptors or namespaces. Their existing runtime isolation remains
part of the boundary. Kernel UID checks, private files and mutual signatures
are independent defenses, not a claim to stop arbitrary same-UID host code.
On success the
keeper returns its generation and a bounded inventory (no bytes, no
secrets) — a *candidate* list the daemon still runs full server-side
adoption against, same as a #269 welcome; an unmatched candidate goes to
Closing.

## Lease semantics: a lapse cuts I/O, never widens access

Same rule #269 applies to access epochs, one layer down. The keeper
enforces its own short lease per shell, independent of the daemon
connection: no daemon attached ⇒ Suspended, no input/resize/output/replay,
only the local ring keeps filling. **Suspending client I/O does not stop
the shell's own processes from writing to the workspace** — Suspended
gates the keeper's socket, not the traced tree's execution. A reattaching
daemon gets no implicit access; it runs the #269-style adoption
transaction (challenge, fresh epoch, consent recheck), resolving two
daemons claiming to be current by epoch order, no grace period for a
stale or spoofed one.

## Collection must fence on reconciliation, not just on I/O

The daemon's turn-end sync orders sync → freeze writers → collect → thaw
(`workspacehostlifecycle.go:162-193`), and `retained_service.go:340-366`
shows freeze/thaw is already a per-child RPC sent before the daemon trusts
a snapshot. A Suspended shell under a detached keeper sits outside that
RPC entirely — neither frozen nor known to the daemon taking a snapshot.
A restarted daemon must not run collection, replace an allocation, or
admit a new writer until **every** retained allocation it can see has
either completed adoption (so it can be frozen like any owned shell) or
is explicitly fenced as unreconciled-closing; anything in neither state
blocks that workspace's next sync rather than being skipped. Fencing as
closing requires proven writer quiescence or finished cleanup: a closing
label on a shell that may still write blocks collection like any other.
Seeing an allocation in an inventory fences nothing by itself.

So independent supervision without the protocol is not a safe
intermediate state — an unfenced shell is a collection hazard, not merely
"unreachable." They ship together in slice 2 below.

## Keeper upgrade path

Compatible: new binary installed, old keeper serves until a quiet point
(no shells, or terminate-with-cleanup), then the daemon starts the new
keeper. Incompatible: refused at hello via a version check, both
directions — a keeper crash-restarted onto a newer binary (reinstall
swaps `$BIN_PATH` atomically, `install.sh:404`, so even a crash-restart
loads new code) refuses to spawn helpers at a version its inventory
doesn't recognize, rather than mixing generations under one broker.

## WSL / systemd supervision

The keeper gets its own systemd unit, `KillMode=mixed`, no
`PartOf=`/`BindsTo=` to the daemon unit, so a daemon restart's cgroup
cleanup can't reach it; `Restart=on-failure` for recoverability, at the
cost of its shells. **The daemon unit keeps `KillMode=mixed` unchanged** —
an earlier draft dropped it to `process` once the keeper took over as
`Pdeathsig` parent, but that's premature: `mixed` is the only guarantee
the daemon's own crash doesn't leave *other* descendants behind, and
nothing here proves every non-shell descendant has its own cleanup path.
The keeper also needs `mixed`: privileged shell cgroups belong to its unit,
and stopping that unit must clean those descendants too. Its singleton lock
stays held through runtime shutdown. On a cold start, before publishing a
socket, it sweeps stale shell cgroups and proves the installed keeper unit's
root contains only itself. Direct development launches share a caller's scope
and cannot use that whole-unit proof; they still sweep their own shell cgroups.
The keeper unit gets limits before survival ships. Everything it parents
inherits them: `TasksMax` counts threads and all descendants, so it must
cover real shell workloads under the per-shell process limit, and
`MemoryMax` likewise covers what shells run, not only scrollback. Sandboxing
such as `ProtectSystem=strict` would also apply to shells, which must still
write their workspaces, so it can't be limited to the keeper's socket and
state directories. Choose these budgets and writable paths from real shell
execution before they ship; `NoNewPrivileges` and similar settings are
inherited the same way. WSL2 already
requires `systemd=true` for the daemon; the keeper has the same
requirement. macOS (launchd) is deferred: shells on macOS still end on a
daemon restart. The Linux/WSL
keeper is a sibling service, never a child of the daemon.

**Owner decision (2026-11-02): build restart-only on WSL.**
`persistent_clock_linux.go:17-19` already excludes any kernel
release containing `microsoft`/`wsl`/`linuxkit`/`docker-desktop`, because
`CLOCK_BOOTTIME` there is unverified against real host sleep. The owner's
desktop is WSL. The sleep refusal stays until BLG-400 proves the clock
safe. A daemon *restart* doesn't need a WSL shutdown, so restart survival
is in scope while suspend persistence stays excluded. `wsl
--shutdown` destroys the keeper and its shells; what a real Windows sleep
does to them is unverified. The keeper is a restart story, not a suspend
story, and on WSL it can only be the former until that evidence exists.

## Failure modes

| Event | Result |
| --- | --- |
| Daemon crash/restart, keeper alive | Shells survive, Suspended; daemon reattaches and runs #269-style adoption, fenced until reconciled. |
| Daemon reinstalled, same local install identity / invalid local identity | Treated as a restart / keeper refuses the inventory request. Upstream daemon credential and consent revocation must also pass the existing server-side adoption checks before any shell access. |
| Keeper crashes, or keeper+daemon both die (reboot, OOM) | Every brokered helper dies (`EXITKILL` via `Pdeathsig`) — a chosen blast radius, see the ownership decision above. Reboot survival isn't a goal. |
| Two keeper processes running, or a new keeper with no prior epoch | `flock` is single-owner, second never binds; no prior epoch is just cold start. |
| Socket permissions widened, or clock jump/NSS outage while detached | Signatures catch a spoofed peer, not a compromised same-UID process (honest boundary above); lifetime/identity logic runs as #269 specifies, just relocated. |

## What is refused rather than kept

No mutual credential proof ⇒ no inventory. Unmatched inventory, or a
protocol/version mismatch in either direction ⇒ Closing/refused, never
degraded. Dropping `PTRACE_O_EXITKILL` or weakening the seccomp exec
barrier to buy survivability ⇒ refused outright, same as a different UID
or unverifiable namespace. Reload-in-place of a live keeper ⇒ not
offered, only drain-then-replace. Collection/replacement/new-writer
admission against an unreconciled allocation ⇒ blocked, not skipped.

## If containment can't be held across the gap: options

1. **Recommended: separately supervised keeper**, as above. Survives
   daemon crash/restart/reinstall; not its own crash or a host reboot.
   Per-shell tracers unchanged, matches the persistent-shells.md sketch.
2. **No keeper.** Zero new code, but #271 stays unimplemented and every
   daemon update/crash costs every open shell, as now.
3. **Durable keeper state (custody on disk/DB).** Helps diagnosis and
   audit, but a new keeper still can't resume tracing a tree it never
   seized — as dead as option 1 on keeper death. Better scoped to #274.

Recommendation: 1 — the only option changing the outcome, without
touching `processcontainment` or weakening containment.

## Test plan (real binaries, via `/tmp/cx/outside.sh` — needs real ptrace)

1. Restart the daemon (`kill` and `systemctl restart`): socket, PTY, tree
   survive; measure death timing and separately confirm cleanup
   completion, not just death.
2. Reattach with correct credential: adoption succeeds, ring bytes replay
   without duplication/gap. Wrong credential/UID/signature/epoch:
   refused, shell stays Suspended.
3. Kill the keeper directly: every brokered helper is gone (`EXITKILL`)
   promptly; daemon marks them Closing/Closed, not hanging.
4. Collection fencing: restart the daemon with a Suspended shell still
   writing to its workspace; the next sync blocks collection on that
   workspace until adoption completes.
5. Upgrade and race drill: new keeper binary while shells are open, old
   keeper drains first, daemon refuses to mix generations; two keepers
   back to back, only one takes the lock; stopping the daemon doesn't
   touch the keeper's cgroup, stopping the keeper's own unit kills its
   shells (expected, documented loss).

## Slices

1. Extract per-shell helper supervision behind a standalone keeper process
   (a subcommand bundled in the daemon binary, not a separate artifact)
   the daemon starts and reaches (no survival win, no change to
   `Tree.Run`/`PTRACE_SEIZE`); proves extraction compiles, tests pass.
2. Independent supervision *and* the hello/inventory protocol together:
   systemd unit with resource limits/hardening, `Pdeathsig` moved to the
   keeper, mutual signatures, `flock` singleton ownership, daemon-
   authenticated epochs, version refusal both directions, and the blocking
   collection fence so sync stops on unreconciled allocations — merged
   because supervision without the fence is a collection hazard, not a
   safe intermediate state. Daemon can see/version-check a keeper, not use it.
3. Wire adoption into the existing #269 reconciliation/rekey path —
   delivers restart survival.
4. Remaining hardening: drain-based upgrade drill, socket-race drill, full
   test-plan pass (version refusal/limits already shipped in slice 2 —
   hardening precedes survival, not the reverse).

Each slice gets its own review boundary, as #269/#271/#274 already do.

### Slice 1 implementation boundary

The daemon binary includes `shell-keeper --stdio`, a private inherited-FD
supervision carrier for a single helper. The Linux creating-thread/Pdeathsig
primitive is shared between the existing daemon launcher and the keeper.
The carrier never traces or interprets helper traffic. Exit 3 (identity
invalidation) is preserved and failures use `contracts/failurelog`.

Existing shell launches still use their original helper directly. Moving the
live custody owner and its durable helper PID journal is deferred to the
adoption slice; the extraction does not advertise survival or change cleanup
proofs. The independent broker in slice 2 must not accept shell launches until
adoption and collection fencing are wired. The stdio carrier is transitional,
not the independently supervised host-wide service.

### Slice 2 implementation and review boundary

`shell-keeper init` provisions separate `shell-keeper/daemon-key.json` and
`shell-keeper/keeper-key.json` beneath the configured daemon state directory.
Each contains only its role's private Ed25519 key and the peer's pinned public
key. The keeper never reads the daemon private key; the daemon never reads the
keeper private key. The directory is `0700`, files are `0600`; symlinks, hardlinks,
wrong owners, wide modes and partial/corrupt state are refused. Reinstall
does not regenerate missing keys or epochs. Initial provisioning is deliberately
not self-repairing for preexisting partial or old-format state. Initial files
are fsynced in a private staging directory and published as one no-replace
directory rename. A crash before publication leaves no partial target; a crash
after publication leaves the complete set. Individual state-file publication
uses rename, never a temporary hardlink. Normal daemon epoch advancement
uses file fsync, atomic rename and directory fsync while holding a separate
daemon singleton flock through shutdown. After each flock acquisition, fstat
must still match the named lock's device/inode and link count; replacement
refuses ownership. The installer has an exit trap that cancels an unfinished
update handoff, including failures during initialization, update preflight or
keeper service enablement. Refusals print repair instructions: preserve keys
and live custody, restore a verified backup, and never delete live state to
force a reinstall. Quarantining incomplete state requires first proving that
no keeper or shells exist; there is no automatic destructive repair.

`shell-keeper serve` takes the keeper singleton flock before touching the
socket. It implements bounded, deadline-limited Unix-socket hello and inventory
only. Both sides check kernel peer credentials. Protocol version 2 signatures
bind both nonces, both ephemeral X25519 public keys, both running-build digests,
keeper generation and daemon-signed epoch. An HKDF-SHA256 session key authenticates
the one-shot inventory frame with a direction/sequence-domain HMAC; after that
frame the connection closes. There is no unauthenticated post-handshake command
stream for a relay to inject into. The final inventory also has its own signature
over the transcript and bounded candidate identifiers. Epoch rollback,
equal-number forks and unbounded wire jumps are refused: network claims must
match current signed durable local authority. The step predicate allows only
the same epoch or a single increment, never MaxUint64 jumps. Legitimate missed
daemon restarts are refreshed from verified disk state, not a peer's assertion.
A cold keeper has a fresh generation and no inherited
custody. No private key, terminal content or bootstrap secret crosses this
socket. These local install keys do not by themselves prove current upstream
consent: inventory is observational, and slice 3 must recheck server authority.
The only mutating operation before slice 3 is the explicit empty-keeper drain
described below; it does not authorize launch, attach or collection.

Pre-authentication peers have a 250 ms deadline and a 16-connection cap. Clients
retry transient closure/starting sockets, including I/O closure after the
challenge, for at most two seconds; signature, UID and version proof failures
are terminal. All accept errors except `net.ErrClosed` log their typed cause
and back off from 10 to 250 ms, retrying until cancellation rather than ending
the keeper. A stale crash-before-chmod socket is removable under flock after
type and owner checks regardless of its mode. Clients still require mode 0600.
The keeper sets `PR_SET_DUMPABLE=0` before loading any key material.

The installer writes `blerg-shell-keeper.service` using the same bundled
daemon executable. `enable --now` preserves any running keeper. Pathname replacement
never interrupts its executable inode or shells; independent background maintenance
replaces it only once empty. The keeper has no
ordering race with daemon startup: `Type=notify` announces readiness only
after its authenticated listener is bound. It has no
`PartOf`/`BindsTo` relationship to the daemon; daemon `KillMode=mixed` stays.
Uninstall explicitly stops both services. Initial limits are `TasksMax=8192`
and `MemoryMax=8G`, with private umask and core dumps disabled. These include
future shell descendants. Real shell workload sizing remains required before
slice 3 enables launches; slice 2 exposes no launch endpoint. Inherited
filesystem/privilege restrictions that would prevent workspace writes or
change interactive shell behavior are intentionally absent.

The deliberate OOM policy is `OOMPolicy=continue`, `MemoryOOMGroup=no`: one
child's OOM death must not tell systemd to stop or kill the whole unit. This
does not guarantee that the kernel can never select the keeper itself; keeper
death still kills its helpers under the chosen Pdeathsig/EXITKILL boundary.
The resource limits still need real workload sizing before launches ship.

Helper exec and build hashing use /proc/self/exe, pinning the running inode
across atomic pathname replacement. Both build digests are signed audit metadata;
compatibility depends on the custody protocol version, never exact build equality.
Installation does not inspect, drain, lock or validate keeper state. Both development
Make targets and the installer replace the pathname even while an old keeper has
custody, is hung, or has broken state. Initialization and enable failures warn but
never prevent the daemon and agents from installing or restarting. Keeper startup
is queued with `--no-block`; daemon units have no `After=` ordering on keeper
readiness, so a stuck notify/start job cannot delay agent startup.

The old keeper retains its executable inode and launches helpers through it. A
compatible new daemon adopts that keeper and its shells. An incompatible daemon
refuses host shells with a plain reconnect/restart reason while agents proceed.
Background maintenance, independent of installation, requests authenticated drain
only after inventory contains no live or uncertain custody. An empty old keeper
exits and Restart=always launches the installed image. A manual keeper launch
requires the operator to start its replacement after this clean exit.

Maintenance update and drain use frozen frame version 2 independently of the
custody protocol version. Their transcript layout and signature/session MAC domains
remain stable when custody changes. Drain rechecks durable epoch and emptiness,
stops admission and exits even when its acknowledgement is lost. No unauthenticated
peer can drain a keeper. Installation never waits for this maintenance operation.

The signed snapshot includes protocol and generation; the daemon pins all subsequent
shell operations to that generation and durable epoch. Missing or incompatible state
refuses shells only. Unknown custody blocks admission only in its own session until
adoption or proven physical cleanup. The keeper owns orphan cgroup cleanup under its
singleton lock; agent startup never sweeps live keeper custody. macOS supervision
remains deferred: its shells still end on daemon restart.

Different-UID kernel proofs run by default when Linux Docker is available,
using a disposable static Go fixture and an imported empty image
(no pull/network/build cache). The pre-merge gate sets `BLERG_KEEPER_UID_REQUIRED=1`, making
an unavailable fixture an error rather than a skip. Images and containers are
removed by test cleanup; `BLERG_KEEPER_UID_IMAGE` can reuse an existing image.

No live shell launches are redirected to the independent service in slice 2.
The keeper remains empty; existing shells still have the daemon's teardown
behavior. The stdio carrier proves keeper-parented helper supervision, including
Pdeathsig on keeper death, without changing `Tree.Run`, `PTRACE_SEIZE`, the
durable helper PID journal or live custody ownership prematurely. Restart
survival is delivered by slice 3, after the owner's adversarial review of this
boundary. WSL's sleep-clock refusal is unchanged pending BLG-400.

Review `runner/internal/shellkeeper/`, the Linux keeper entry point and startup
guard in `runner/cmd/daemon/`, and the two systemd templates plus installer.
The remaining acceptance evidence includes real shell workload limits, the
owner-run reinstall drill, and slice 4's installed-host upgrade and race drills.

### Slice 3: native custody and restart adoption

Host shells now run through the independently supervised keeper. It owns the
existing shellruntime coordinators, per-shell helpers, rings, identity checks
and leases. Helpers execute the pinned `/proc/self/exe` image, with Pdeathsig
bound to their keeper parent. No tracer is transferred and no containment
rule is weakened. Sandbox and cluster sessions retain their isolated local
paths; a failed host bridge never falls back to daemon-owned host helpers.

After the signed inventory fence, the daemon pins protocol compatibility and keeper
generation before opening a service stream. Every frame has a direction-bound
HMAC, strict sequence and bounded size. The stream checks current durable
daemon authority on each operation. The authentication deadline is cleared
only after proof; buffered frames remain on the same scanner. The fixed
operation allowlist exposes native shell operations, not arbitrary executables.

The protocol's host `ShellLinkHello.Daemon` identifies this keeper incarnation,
so custody survives a daemon process replacement. Each upstream connection
still gets a new challenge and server-authorized access epoch. Disconnect joins
old output and suspends access; a new local daemon must rebind exact workspace
identity, accept a fresh upstream welcome, and complete the existing inventory,
prepare and commit transaction. Upstream output failure suspends its access
epoch while preserving the local control channel for reconnect. Late failure
notifications cannot suspend a newer epoch.
Survival requires the existing persistent v2 custody and consent contract.
Legacy v1 shells close on rebind; local install authority does not grant them
offline persistence consent.

The shell startup fence permits authenticated candidates. Until a session's live
candidates are adopted or physically cleaned, the native keeper refuses new
shell admission for that session. Agent driver admission is independent.
Existing freeze/collect/thaw calls run in the keeper against the same retained
coordinators. A crash during freeze can thaw only after exact workspace pins
are reproved, leaving shell I/O suspended until adoption. Merely marking a
candidate closing never grants writer admission. Drain closes native execution
admission, joins in-flight operations, then inspects custody and rechecks the
durable epoch before committing. Aborted drain reopens admission.

`BLERG_HOST_SHELL_CGROUP` is rendered identically into daemon and keeper units.
The default unprivileged helper path remains available; privileged containment
still uses the existing cgroup proof. macOS remains deferred: its shells still
end on daemon restart.

WSL uses an explicit restart-only helper profile and capability probe. The
existing sleep-safe kernel predicate still refuses WSL until BLG-400. The
restart profile samples wall and elapsed clocks, latches failure on rollback,
read failure, disagreement or an unobserved interval over five seconds, and
closes rather than granting post-gap access. This is no Windows sleep or WSL
shutdown guarantee. Linux uses the existing verified persistent clock.

Tests exercise actual helper tracing, a separate daemon process killed with
SIGKILL, same-image reinstall into an isolated destination, fresh adoption of
the same shell state, stale access refusal, and keeper death killing its owned
helper and shell. These are disposable fixtures, not the owner's real reinstall
drill. Tests requiring a tracer run through `/tmp/cx/outside.sh`.

### Slice 3 review corrections

The daemon watches its bridge. Loss sends a bounded terminal-access closure
notice, refuses host shell operations, and reconnects in the background. It
never exits the daemon or interrupts an agent because of keeper failure.
Normal daemon shutdown cancels reconnection before closing its bridge.
Reconnection checks protocol compatibility, durable epoch and generation again. The
existing authenticated desktop stream carries a shell-only hello/welcome
refresh; runtime-token pods and unenrolled peers cannot use it. This refresh
does not reconnect the agent transport. Trusted local registrations are retried
independently; a failed registration leaves shells fenced, never agent recovery
paused. Access still requires the server's normal inventory/prepare/commit
transaction and fresh grants.
Keeper rebinds, capability updates and welcome processing run off the agent
connection loop. Agent hellos use cached shell metadata. Missing keeper support
cannot stall that loop; the recovery worker re-probes persistence and refreshes
its capability when the keeper returns.

Agent host-workspace sync, status reopening, Stop and server rebind use the
daemon's pinned local launch binding. Their lifecycle adapter can acknowledge
only its own empty shadow, with its own connection nonce; it refuses if that
shadow owns any helper/coordinator history. This is never a keeper custody
cleanup receipt and cannot admit or close an individual shell. Lifecycle updates
commit to the local shadow and queue reconciliation without keeper
IPC under workspace locks. Stop and discard first attempt to fsync a workspace-keyed
retirement record outside keeper key state, even after an offline manifest update.
Store failures log and retain memory intent without blocking the agent; persistence
is retried. Successfully persisted records survive daemon restart and retry with
exponential backoff. Authenticated retirement
unconditionally removes that workspace from the active launch index; physical custody
remains inventoried and fenced in a separate cleanup ledger until its tree is proven
dead. Neither pending cleanup nor keeper availability can block an agent lifecycle.

Installer failures in keeper initialization or enable are shell-only warnings. Missing
kernel containment stops only the keeper with configuration status 78, avoiding its
restart loop; the daemon remains independent. Preserve private keys and custody when
repairing state. Never delete a live keeper's directory to make an install succeed.

Local retained tracers record a pidfd for each process before allowing it to
run, using the containment ledger's existing kernel evidence. After an unclean
helper exit, custody settles only after helper reap, intact ledger EOF, every
recorded process's exit, PTY closure and output join. A missing or broken ledger
refuses cleanup. Privileged shells retain their independent cgroup empty proof.

Bridged requests use the daemon's existing per-session queues. Their two-second
budget includes queue wait, IPC writer wait and keeper dispatch; an expired
request cannot begin later. Private calls retain separate correlation IDs at
each layer. The keeper dispatches independent session calls concurrently under
the same authority/rebind barriers, with a separate reader for delivery receipts.
Output has sixteen shared delivery credits. A producer waits for its upstream
delivery receipt before reclaiming a credit; cancellation must join delivery.
Replies have an independent dispatch path, so a stalled terminal cannot fill
the control queue or block agent traffic.

SourceLive, SourceState and atomic admission remain daemon authority. Source
pins are rechecked inside admission immediately before forwarding, and the
admission callback spans the actual open result. Confirmed source replacement
invalidates legacy consent too; restoring an alias cannot restore old access.
An inventory candidate without a current daemon registration cannot prove its
source pins. Restricted host launches can register their workspaces; registration
does not grant persistent shell consent to a child launch.

Signed inventory also lists empty launch registrations, using distinct bounded
metadata markers. A restarted daemon can retire those registrations before
replacing a launch. Maintenance filters only these exact non-custody markers;
live or uncertain helpers refuse drain, while installation always proceeds. A
helper-exit
observer settles naturally ended or expired custody through the existing exact
cleanup proof and output join, even if its session never returns. An unproved
helper crash remains fenced and logs its cleanup cause.

Regression evidence includes removal checks for the authority guards, graceful
SIGTERM and SIGKILL adoption with real tracers, repeated bridge FD lifecycles,
130 output producers behind a blocked writer, and terminal-content log safety.

## Owner decisions (2026-11-02)

- WSL is restart-only; the sleep refusal remains until BLG-400 proves
  the clock safe. WSL shutdown and host reboot survival are not promised.
- Every per-shell helper's `Pdeathsig` is bound to the keeper.
- macOS supervision is deferred. Shells on macOS still end on a daemon restart.
- The keeper is bundled in the daemon binary and installed by the same
  script. There is no separate keeper artifact.
- Slice 2's mutual-authentication/epoch boundary requires an adversarial
  review before merge. Stop and report when slice 2 is ready; the owner
  starts that review. Do not proceed to slice 3 before this boundary.
- Do not touch the real installed daemon. The owner runs the real reinstall
  drill on this host; isolated tests do not replace that acceptance evidence.

### Round 9 failure handling

New native retirements wake cleanup immediately and reset its backoff. Stop
remains unconditional even if the retirement journal is unavailable; memory-only
intent is retried, but cannot survive a daemon crash until persistence succeeds.
An incompatible keeper holding live shells stays alive. The existing shell-start
notice explains that the keeper service and shells need restarting, which ends
those old shells while agents continue. Once all old shells close, signed
maintenance drains the empty keeper and its service starts the installed image.
Installation never waits for this replacement.

### Round 10 membership and call boundaries

Per-call deadlines, including an expired server request or an expired writer wait,
report `keeper_unavailable` for that call only. They never request a reconnect;
heartbeat misses own liveness. Heartbeats have a separate pending budget from the
128 ordinary client calls as well as bypassing ordinary server queues and slots.
`workspace_shells_open` is allowed during authenticated recovery so changed
manifest reconciliation can obtain its negative proof.

The repository panel offers explicit host-shell consent withdrawal while waiting
on the keeper. It uses the existing session- and login-scoped authenticated
withdrawal routes, with their durable server denial and audit records; no new
permission or destructive endpoint was introduced. It does not kill agents or
other workspaces, and other sign-ins must withdraw their own consents. A request
never masquerades as physical cleanup: membership still waits for all writers
to be proven gone. An empty consent list or failed confirmation grants nothing.

Ordinary shell refusal says only that the keeper is reconnecting. Authenticated
maintenance's existing restart-required result reaches the UI separately as
`keeper_incompatible`, with the restart remedy. No generic reconnect advises
ending shells.
