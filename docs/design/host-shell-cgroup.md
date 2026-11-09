# sudo in host shells: a cgroup instead of the tracer

*2026-11-01. BLG-303, `runner/internal/shellruntime`. Builds on
[sound-process-freeze.md](sound-process-freeze.md) and [process-limit-refusal.md](process-limit-refusal.md).
Status: owner accepted best-effort freeze on 2026-11-01 for 0.6.0, on the terms below.
Privileged shells require an explicit per-shell human choice and the host availability switch.*

## Owner ruling (2026-11-01)

- The person chooses **New privileged host shell** when opening each privileged shell and confirms
  the risk. Ordinary shells retain the tracer, even when `BLERG_HOST_SHELL_CGROUP=1`.
  Agent credentials cannot open or request a privileged shell. The existing human-only open route
  binds the choice into the signed grant and, for persistent shells, immutable custody.
- The tab and consent dialog identify the shell as privileged and **not frozen at turn end**.
  Its processes keep running during sync. Ordinary shells keep their existing freeze guarantees.
- While a privileged shell is open, the turn's result carries a plain note: the copy may include
  in-flight writes. This is an accepted limitation, not a successful physical freeze proof.
- A failed or unavailable physical proof remains unproven. Unreadable process status cannot prove
  either process death or the absence of escaped writers. The cgroup-only kernel proofs below remain.
- The helper installs the hard `RLIMIT_NPROC` ceiling before starting the shell, so its first exec
  and any immediate fork inherit it. Root remains outside this limit.
- Startup sweep kills only a demonstrably dead owner: a missing process or a different kernel
  start time. Permission, I/O and malformed-status errors preserve the shells and are logged,
  along with cleanup errors, even when the availability switch is off.

## The problem

`sudo ls` in a host shell fails with "no new privileges". Removing that flag is not enough. Two things
each stop a setuid exec, and both are the tracer:

1. The seccomp filter needs `PR_SET_NO_NEW_PRIVS`.
2. The kernel ignores the setuid bit for a ptraced task unless its tracer has `CAP_SYS_PTRACE`
   (`LSM_UNSAFE_PTRACE`). The daemon runs as the person and does not.

So a shell can have sudo or the tracer, not both. Everything the tracer does for a shell must come from
somewhere else, or be given up.

## The design

The daemon makes a cgroup v2 per explicitly privileged host shell. The shell starts in it through `CLONE_INTO_CGROUP`, so it
never runs a first instruction outside. There is no tracer, filter, or `no_new_privs`. The tracer stays
for ordinary host shells, agent workspaces, sandbox shells and cluster shells, whatever this switch says.

```
<cgroup2 mount>/<daemon's own cgroup>/blerg-shells-<pid>.<starttime>/<random>
```

`cgroupTree` has the same four methods as `processcontainment.Tree` (`Run`, `Control`, `Stop`, `AtLimit`),
so the shell, its PTY, grants, leases and both helpers don't change.

| Need | Tracer | cgroup |
|---|---|---|
| Freeze | `PTRACE_INTERRUPT`, every task reports | `cgroup.freeze`, acknowledged only by `frozen 1` in `cgroup.events` and no live task of the shell outside the cgroup |
| Kill, including root tasks | SIGKILL through pidfds | `cgroup.kill`: kernel side, needs write access to that file, not rights over the task |
| Proof it is empty | Ledger, wait statuses | `populated 0` in `cgroup.events`, the cgroup and its subgroups removed, and no live descendant of the helper left |
| Reaping | Subreaper | Subreaper |
| Helper dies | `PTRACE_O_EXITKILL` | The daemon kills the cgroup when it reaps the helper |
| Daemon dies too | `EXITKILL` | The next daemon sweeps cgroups of a dead daemon at start |
| Task limit | Refused with `EAGAIN` and a line | Refused with `EAGAIN` and a line, through `RLIMIT_NPROC` (below) |

A sudo child is the reason the left column can't simply be kept: the daemon cannot signal a root task
at all. `cgroup.kill` and `cgroup.freeze` can, and both were proven on this kernel.

When the root shell exits, the rest of the cgroup is killed, as the tracer ends the tree. Killing tasks
removes no cgroup, so a shell's own subgroups go deepest first, on close and in the sweep.

A direct cgroup freeze proof (used by the kernel tests) reports unproven on failure. At turn end,
privileged shells instead receive a distinct sync-admission operation, which suspends terminal access
and installs the sync watchdog without pretending to freeze the processes. The copy carries the note
above. Persistent shells still close when their independent lifetime or sync deadline expires.

### Tasks that leave

Nothing short of a user namespace stops a same-user task writing its PID to the writable parent, and
`frozen 1` speaks only for what is still inside. So the cgroup is not the proof by itself. The helper is
the subreaper, so whatever the shell starts stays under it in the process tree, wherever it moved or
however it was orphaned. After `frozen 1` the helper reads the process tree from `/proc` and every live
descendant must be in the cgroup or beneath it. One that isn't refuses the freeze. A reap during the read
means something outside was running, so the read is taken again until the deadline.

At the end of a shell the same read finds what left, and a pidfd, checked to be the process that was read,
kills it. A root task that left can't be signaled, and the end reports unproven.

### The limit

Without the `pids` controller, the one thing that makes the kernel refuse a process for a program this
daemon doesn't trace is `RLIMIT_NPROC`. It counts every task of the user, so it works in two parts:

- **The ceiling**, the hard limit, given to the shell as it starts: three quarters of the lowest of
  `threads-max`, any `pids.max` above the daemon on either hierarchy, and the daemon's own hard limit,
  and at most 65536. So a runaway stops with a quarter of the machine's allowance left for other
  sessions and for cleanup, whichever cgroup it ran in. No task can raise it. A shell started near that
  allowance still gets 1024 above the current count.
- **The limit**, per shell. The helper counts the threads of the cgroup and its subgroups every 10 ms. At
  `TasksPerShell` it freezes the tree for a moment and sets each task's soft limit to 1, under any count, so
  its next `fork` fails with `EAGAIN`. The root keeps its own until 64 past the limit, as the tracer's
  reserve does, so the person can run `ps` and `kill`. The refusal is renewed while the tree stays full,
  and is lifted 16 under the limit. The usual line reaches the terminal.

The count can pass the limit by what forks in one tick. A shell loop and a real fork bomb peaked at 1450 on
32 CPUs. The tracer is exact, this is not; the ceiling is what holds.

The sweep goes by owner, because agent sessions and test daemons share the service's cgroup. Only a
directory whose `<pid>.<starttime>` no longer names a live process is killed.

### When the cgroup is used

Ordinary shells always use the tracer. For a privileged shell all of these must hold:
the switch `BLERG_HOST_SHELL_CGROUP=1`, runtime `host`, Linux, a `0::` line in `/proc/self/cgroup`, a
writable cgroup2 mount, and `cgroup.kill` (Linux 5.14+). A start failure past that point is a visible start
failure, not a silent fallback, since an unexpected downgrade would hide what changed.
An unavailable privileged mode is also a visible refusal; it never silently changes the selected mode.

## What this gives up

These are accepted limitations of the explicitly privileged mode.

**1. A limit that isn't exact.** The ruling for BLG-326 is that a tree stays bounded and refuses at the
limit. It does, with the overshoot above. A task can raise its own soft limit back up to the ceiling, and
a task that left the cgroup isn't counted, so either is bounded by the ceiling and not by 1024. Root tasks
don't count against `RLIMIT_NPROC`: a fork bomb under `sudo` has no bound (item 3). `pids.max` would be
exact, but on WSL's hybrid layout `pids` lives on the v1 hierarchy and can't be delegated at all, and on
native v2 the daemon would have to leave the service's root cgroup, which touches every agent.

**2. A same-user process can leave.** Writing its PID to the parent's `cgroup.procs` works (proven). A
cgroup namespace would stop that, but needs a user namespace, which breaks setuid. This is the reason
[the freeze design](sound-process-freeze.md) left the cgroup freezer out. A freeze now refuses when it sees
one in the direct kernel proof. Privileged turn-end copies instead carry the in-flight-writes note. Innocent programs do this:
`systemd-run --scope`, or a `tmux` built with cgroup support, moves itself out, and from then on each turn
end would fail a strict physical freeze proof until that process is gone. Two things still get past the read, and neither is an accident: a
child of the service manager (`systemd-run --user`, which leaves a traced tree too), and a program built to
hop, forking and exiting under a subreaper of its own while the read runs. A helper killed outright also
loses its subreaper role, so a task that had left outlives it; that end already reports no clean proof.

**3. Root can do anything.** A root process can move itself, start a system service, or mount over the
workspace. Blerg can't contain that and must not claim to. Workspace root pinning and writer admission
don't protect against root.

**4. Remote sudo.** Host shells are reachable from a browser. On a machine with passwordless sudo, or a
sudo timestamp still valid on this terminal, a stolen Blerg login becomes root on that machine, where today
it stops at the account. The consent copy says so. Removing the guarantee in
[owner-shell-supervisors.md](../owner-shell-supervisors.md), "privilege gain through setuid executables is
suppressed", is the ticket's stated goal. It is listed because it is the largest change here.

**5. An unattached shell can be read by a same-user process.** A traced shell can't be ptraced by anything
else. An untraced one can, where Yama allows it, so a same-user process could drive a shell whose owner just
used sudo. That matches a person's own terminal. Agents keep `no_new_privs`, but this was never a wall on a
host, again because of `systemd-run --user`.

## Other hosts

| Host | Result |
|---|---|
| Native Linux, systemd user service | Should work by file ownership, untested here. Add `Delegate=yes` to the unit when turning this on: it is systemd's documented contract for a subtree, and left out for now so a switched-off install changes nothing |
| WSL2, systemd on | Proven: cgroup2 at `/sys/fs/cgroup/unified`, freeze and kill work, no `pids` |
| WSL2, systemd off; a shell not in a user service | Privileged startup is refused if the cgroup isn't writable; ordinary shells retain the tracer |
| Sandbox shells, rootless Docker | Always the tracer: the container is `no-new-privileges` by design, so sudo stays off |
| Cluster shells | Always the tracer |
| macOS | No cgroups. Host shells aren't offered there, so nothing changes |
| A daemon that itself has `no_new_privs` (started from an agent session) | The cgroup works, sudo still doesn't: the flag can't be cleared |

## Deferred choices

The owner accepted the best-effort per-shell mode above for 0.6.0. Exact `pids.max` enforcement and
transient systemd scopes remain separate future work. The availability switch remains off by default;
enabling it offers the explicit privileged choice and never changes an ordinary shell.

## Proof

`runner/internal/shellruntime/cgroup_linux_test.go`, through a real shell, a real PTY and the real kernel
files: a counter stops while frozen and resumes on thaw, under a fork storm and with vfork; a stop kills
detached and `setsid` children; a root exit does too; a helper's crash is cleaned by the daemon's path; the
sweep spares a live owner; `no_new_privs` is 0 and `sudo` reaches its password prompt; a freeze refuses a
running child and an orphan that left, and a stop ends both; nested cgroups go on a stop and in the sweep;
four bombs, one in a subgroup and one in the root shell itself, are refused with the line, and the shell
answers and gets its processes back. They need `clone3`,
no `no_new_privs`, and a test process no tracer ran in, so they skip unless asked for:
`BLERG_TEST_CGROUP_SHELLS=1 go test ./internal/shellruntime -run '^TestCgroup'`, outside a session. CI skips
them too: its cgroups aren't writable.

There is no passwordless sudo on the test host, so no fully root task was frozen or killed. `sudo` at its
password prompt is a setuid-root task and was. Native cgroup v2 has not been run. The ceiling's value is
tested; a bomb that left the cgroup and ran up to it has not been, since that takes tens of thousands of
tasks on a shared host.

## Left out

- `pids.max`.
- Removing the tracer from host shells for good. Ordinary shells retain it.
