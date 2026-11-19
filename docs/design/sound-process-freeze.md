# A sound process freeze: SEIZE, INTERRUPT and an exec barrier

*2026-10-31. BLG-344, `runner/internal/processcontainment`. Follows the BLG-339 reviews.*

## The problem

A turn end freezes the writers of a session, copies the workspace, and thaws them. An acknowledged
freeze must mean that no task of the tree can run user code. The tracer rests that on two beliefs
that are false under `PTRACE_TRACEME`:

1. **The auto-attach SIGSTOP is an execution barrier.** It is a pending signal, and generating
   SIGCONT discards a pending stop. A new child can run user code that no `PTRACE_CONT` released
   (verified 100/100 in the BLG-339 work). A freeze's `tkill(SIGSTOP)` is lost the same way.
2. **A tracked TID names one task until the tracer drops it.** When a non-leader thread T execs,
   `de_thread` frees T's TID before the tracer collects the EXEC event. A new task C can take the
   number. The EXEC event then deletes C's entry, and a freeze acknowledges with C running.
   A signal by TID can hit the wrong task in that window, including one outside the tree.

Either can give an inconsistent turn-end copy.

## The design

### 1. Seize the root through a shim

`PTRACE_SEIZE` changes what a new task is: an auto-attached child gets `JOBCTL_TRAP_STOP`, a
job-control trap SIGCONT does not clear, and reports `PTRACE_EVENT_STOP`. Seize is inherited from
the root, and it cannot be turned on for a task already attached with `TRACEME`.

The exec barrier (section 3) also needs its seccomp filter in place before the first untrusted
instruction, installed by the task itself, and `SECCOMP_RET_TRACE` answers ENOSYS unless a tracer
has already set `PTRACE_O_TRACESECCOMP`. A root that execs its target directly cannot satisfy that
order. So `Tree.Run` starts the root through a shim, an `init` hook in this package that runs before
`main` in every binary that links it, so no binary needs a new subcommand:

```
helper                                  root (/proc/self/exe <marker> <path> <argv...>)
  InstallFilter (as today)
  start()  ---------------------------> init hook: install exec filter, stop itself (SIGSTOP)
  wait4(WUNTRACED): stopped
  PTRACE_SEIZE with options
  record root in the ledger
  SIGCONT, PTRACE_CONT  --------------> execve(target)  -> EVENT_SECCOMP -> EXEC event
  ready <- nil at the EXEC event
```

No target code runs unless the seize happened: an exec without `TRACESECCOMP` set fails with
ENOSYS, and the shim exits. A stray SIGCONT during startup therefore fails closed. `/proc/self/exe`
keeps this correct when the binary on disk was replaced by an upgrade.

The tracer's own dumpability does not matter, so a helper protected with `PR_SET_DUMPABLE=0` (the
persistent shell helper) seizes as usual: `/proc/self/exe` resolves for the task itself, and exec
makes the shim dumpable again. The target's does matter. Exec of a binary its user cannot read
leaves the shim nondumpable, and a same-UID tracer without `CAP_SYS_PTRACE` is then refused, where
`TRACEME` was not. An execute-only helper binary therefore fails closed at start, naming the seize.
Installs and images ship the binary readable.

An exec failure (a missing shell) still has to reach `start` as an error, not as a tree that exited.
The shim exits with the errno, and the tracer reports it as a start failure (`ErrStart`) because the
root died before its EXEC event. `shellruntime` keeps staging both this and a refused ptrace as
`exec_shell`. Only `ready` moves later, and the helper's start timeout grows from 5 to 10 seconds
to pay for one more process start on a busy machine.

### 2. Freeze with PTRACE_INTERRUPT

`PTRACE_INTERRUPT` replaces `tkill(SIGSTOP)`. It is not a signal, SIGCONT does not affect it, and it
resolves only tracees of this thread, so a stale number can never reach an outsider. A trap left
over from a cancelled freeze is one harmless `EVENT_STOP`.

SIGSTOP now means only what the tree meant by it. Under seize the tree's own stops are honest group
stops, where they used to be swallowed:

- A signal-delivery stop of a stop signal is passed on, and the kernel then reports a group stop
  (`EVENT_STOP` with that signal).
- A group stop is parked with `PTRACE_LISTEN`. The task runs nothing, no tracer action releases it,
  SIGCONT re-reports it, and only then is it continued. `^Z` and `fg` in a shell behave as before
  on screen, and a stopped job now really is stopped.
- A listening task is held for a freeze. A thaw leaves it listening: a thaw undoes the freeze,
  not the tree's own stop.

An acknowledged freeze means every task has reported a stop, with two exceptions that are inside the
kernel and cannot report one yet. A task past its `EXIT` event only finishes kernel exit work. A vfork
parent sleeps killably in `wait_for_vfork_done` (`D` in `/proc`, or briefly `R` on its way there) until
its child execs or exits, so it can't stop before its child does, and it counts as held once the child
is. It is inside the `vfork` syscall, not in a write, and it next reaches `VFORK_DONE`, a stop, before
any instruction of its own. So a frozen tree can show `D`; a test proves it from `/proc/<pid>/syscall`
(`proveHeld`), not from the state letter. Every other task in `D` keeps the freeze waiting.

### 3. Exec barrier

The filter returns `SECCOMP_RET_TRACE` for `execve` and `execveat`, so the tracer sees every exec at
syscall entry, in a stop, before `de_thread`.

- **Leader exec** (nearly every exec: a fork or vfork child) frees no TID. It is continued at once.
- **Non-leader exec**: T stays stopped. The tracer pauses the tree with `PTRACE_INTERRUPT` until
  every task is held, then steps T alone with `PTRACE_SYSCALL`. T's next report is its EXEC event
  (under the leader's TID, naming T), its syscall-exit stop (the exec failed), or its death. Then
  the tree resumes. Nothing in the tree can allocate a TID between `de_thread` and the EXEC event.
  Tracees cannot lose a TID any other way: an unreaped task keeps its number. The pause holds only
  this tree: a process outside it can still take the freed number, which is why teardown never
  signals a thread by number (section 4).

A SIGKILL can land between `de_thread` and the EXEC stop. The kernel skips that stop, so no EXEC
event comes and T exits under the leader's number. The tracer reads T's `Tgid` before the step, and
the leader's exit status ends the step. A leader reports only after its threads, so T's entry names
nothing by then. Without this the tree would stay paused until the timeout below and be ended.

A freeze and a barrier compose. A task at the seccomp stop is held. A barrier does not start while
frozen. A freeze during a step waits for T's report. Liveness is bounded: a pause that does not
complete within 10 seconds (a task stuck in the kernel, or a client of a FUSE server in the tree) is
abandoned, the tree resumes, T stays at its stop and the pause is retried, so only T waits. If the
step itself does not report within `controlTimeout` (the exec's own file is served from inside the
paused tree) nothing can be proven and the tree is ended, saying why.

Filters of the tree's own run with this one, and the kernel takes the strictest answer. `ALLOW`,
`LOG` and another `TRACE` change nothing. `ERRNO`, `TRAP` and `KILL` stop the exec, which is fine.
`USER_NOTIF` is the exception: it outranks `TRACE`, and a listener that answers
`SECCOMP_USER_NOTIF_FLAG_CONTINUE` runs the exec with no stop at all. So the exec filter refuses
`seccomp(2)` with `SECCOMP_FILTER_FLAG_NEW_LISTENER` with EPERM. The flags are a 32-bit argument the
filter can read directly, `prctl` cannot pass them, and the architecture check already covers
other syscall ABIs.

**What that breaks.** Anything run inside a session that needs its own listener: a runtime
configured for it (runc `listenerPath`, an LXC `lxc.seccomp.notify`, sysbox-style supervisors). It
fails where it installs the filter, with its own message for EPERM ("operation not permitted"); the
tracer logs nothing, because the refusal never reaches it. Default rootless Podman and Docker and
Chromium-style sandboxes install plain filters and are unaffected, and containers run by a daemon
outside the session are not in the tree at all. Seccomp filters without a listener still work,
stacked or synchronized. Nested runtimes that need a tracer of their own never worked here.

A listener that exists before the tree starts cannot be refused. One whose filter passes exec, or
something not imagined here, leaves section 5 as the backstop.

### 4. Teardown through pidfds

The tracer keeps the pidfd it already opens for each process (the ledger's record) and sends the
teardown SIGKILL with `pidfd_send_signal`. With no pidfds (an old kernel, a container profile, or
descriptors exhausted) it falls back to `kill`, by number, **for leaders only**. A leader's number
stays with its process until the tracer reaps it, across any exec. A thread's does not: its exec
frees the number before the EXEC event drops the entry, a stepped exec included, and a process
outside the tree can take it in that window. A process dies through its leader, which stays tracked
while any thread lives, so skipping threads loses nothing. pidfds were already optional and the
ledger's proof fails closed without them.

Leaders are told from threads by `pidfd_open`: EINVAL before Linux 6.16, ENOENT since. Without
pidfds the tracer reads `Tgid` from `/proc`, once, when the task is new and the number is certainly
its own. A task it can't read counts as a thread: never signaled by number, and if that was a
leader the tree does not end by signal, so the stop reports unproven and `EXITKILL` ends it with the
helper.

### 5. Past the barrier

The barrier is not the only line of defense. Every exec that moves a task to its leader's number
reports the former TID, and every barrier knows which task it stepped. An EXEC event naming anything
else means an exec reached `de_thread` in a running tree, by whatever route, and that nothing proves
what happened to the number:

- Every later freeze of this tree fails, as does one still landing. A failed freeze skips that turn's
  copy and leaves the session usable, so this costs copies, not the session.
- The entry is decided by the kernel, not assumed: a tracee under that number is a task of this tree
  that took it, so it stays tracked and is recorded now. Anything else is dropped.
- A creator event naming a tracked TID still counts as `reborn` and fails freezes the same way.
- The ledger cannot wait for those events. A task's reports come in any order, so the first stop of
  a new process on a thread's old number can come before its creator's event and the EXEC event, and
  the number still reads as the old thread. Only a thread's number changes hands, and a new task's
  first report is always `EVENT_STOP`, so every such stop of a thread entry asks `pidfd_open` again.
  A thread still fails it. A process succeeds, is recorded before it is continued, and counts as
  `reborn`. A new thread needs no record: its process's other threads ran.

The old code recognized the stale entry itself from the auto-attach SIGSTOP's siginfo. Under seize a
new task's first stop looks like an interrupt, so that signature is gone; the EXEC event is the
reliable signal, and it comes late, so a freeze refuses rather than guesses. A freeze acknowledged
before that event is still true: the task at its EXEC stop runs nothing, and a task that took the
number counts as held only after a stop of its own.

## What stays the same

The task limit, the ledger and the lost-helper proof, `held`/vfork/`exiting`, `births`
for report ordering, the control protocol and the callers' API, except that callers stop setting
`Ptrace: true`.

## Cost

A normal turn end sends one `PTRACE_INTERRUPT` instead of one `tkill` per running task. Every exec
gains one stop, and every root start gains one Go process start. The tracer also polls for 100 µs
after a report before it sleeps.

Measured on 5.15 (WSL2, 32 CPUs, load average 10 to 20 from other work), alternating old and new, on
`go build -a net/http` under a writer helper with a turn end every 500 ms (`TestMeasureTurnEnd`, six
rounds each):

| | before | after |
|---|---|---|
| turn end, p50 | 6.4–7.9 ms | 6.5–7.8 ms |
| turn end, p95 | 8.6–13.1 ms | 7.5–12.8 ms |
| build | 8.2–11.6 s | 8.7–11.6 s |
| 1000 `/bin/true` from a shell | 6.4–7.0 s | 5.7–7.0 s |
| command start | 7.5–10.3 ms | 13.6–20.5 ms |

Turn ends and builds are the same within this noise; an earlier, quieter pass that favored the new
code does not hold up. A p95 here comes from 16 to 22 samples, so it is nearly the maximum. These
are freeze plus thaw alone, with no copy. Short execs get somewhat faster, and only because of the
poll. Command start is a real cost: about twice as slow.

`TestMeasureReportSpin` runs the same 500-exec shell loop in each helper, with and without the poll:

| helpers | poll | helper CPU per exec | wall |
|---|---|---|---|
| 1 | off / on | 1.31 / 1.36 ms | 3.9 / 2.9 s |
| 4 | off / on | 1.28 / 1.35 ms | 3.9 / 2.9 s |
| 16 | off / on | 1.52 / 1.59 ms | 4.1 / 3.3 s |
| 32 | off / on | 1.51 / 1.69 ms | 4.4 / 4.1 s |
| 64 | off / on | 1.20–1.37 / 1.59–1.61 ms | 5.0–5.4 / 5.3–5.4 s |

So with CPUs to spare the poll costs 4 to 5% more helper CPU per exec and the work finishes 20 to 26%
sooner. At one helper per CPU it costs 12% and gains 5 to 7%. Oversubscribed it costs 15 to 30% and
gains nothing. A busy helper uses about a quarter of a CPU either way.

The poll costs nothing at rest, but the sleep it falls back to does: an idle helper wakes every
millisecond and uses about 130 ms of CPU per second on this kernel, poll or not (16 idle helpers, 2
CPUs). That loop predates this design and is not changed by it.

## Tests

Through the real tracer, observing rather than injecting the first report: SIGCONT storms against
new children (a tracer that declines to continue children shows no child runs), against freezes and
with SIGCONT blocked; fork storms, vfork, thread exit, zombie leaders; exec by a non-leader with and
without failure, while frozen and with the rest of the tree spawning; real TID reuse forced inside a
PID namespace with `ns_last_pid`; SIGSTOP and job control from the tree; ESRCH at every tracer
request; helper loss; shim fidelity (argv, env, cwd, descriptors, signal mask, exec failure); a
cooperating notification listener and stacked `ALLOW`/`LOG`/`TRACE`/TSYNC filters; execs past the
barrier with real reuse in the tree; a stop request landing while a process outside the tree holds a
thread's old number, in a PID namespace.

The listener regression runs on hosts that already hold a listener (WSL2's init does: every process
there gets EBUSY from the kernel), where the difference is EPERM from the filter against EBUSY. Only
CI's kernels can show the listener actually carrying an exec past the stop without the filter.

Shim fidelity is read from `cat`, not from a shell: dash clears its inherited signal mask when it
starts, and blocks every signal around each `vfork`, so a child reading the shell's status sees that
window now and then (37 of 3000 runs here, with no Go involved). The comparison runs from the test's
own state and from a C parent that blocks all signals, ignores some, and changes nice, limits and
`oom_score_adj`. A Go parent on either path passes on the mask minus the signals the runtime needs,
and of the ignored signals only SIGHUP and SIGINT. `no_new_privs` differs on purpose.

The reuse test has a 120-second budget for its 1500 rounds, because a fixed count against a clock
loses on a loaded machine. Whatever it finishes must match the barriers exactly, with reuse seen.

These need their own tracer, so they run outside a session. The kernel matrix is this design's weak spot: everything here is proven on 5.15; 6.16+ is
covered by CI only. Known differences are confined to `pidfd_open`'s errno.

## Doors left open

BLG-326 option D took the refusal point this filter left open: see
[process-limit-refusal.md](process-limit-refusal.md). A tree is no longer ended at the limit.

## Left out

- The cgroup v2 freezer: not delegated in default Kubernetes pods, and a same-user agent can leave
  it on the desktop.
- SIGTRAP is still dropped at signal-delivery stops, as today.
