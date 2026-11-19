# Refusing new processes at the limit

*2026-10-31. BLG-326, `runner/internal/processcontainment`. Builds on
[sound-process-freeze.md](sound-process-freeze.md). Owner ruling 2026-10-30: option D.*

## The problem

A traced tree that passes its task limit (4096 for an agent session, 1024 for a shell) is ended, so a
`make -j` that overshoots kills the engine and the turn with it. The ruling: over the limit only the
command that tried to start one more process fails, with `EAGAIN`, like `RLIMIT_NPROC`. The engine and
the rest keep running, and the agent and the person both read the same plain line. The tracer does
this itself, not a cgroup, so it holds on every host that has the tracer, WSL's cgroup v1 included.
Nothing ends a tree for its count any more.

## The refusal

The exec filter also returns `SECCOMP_RET_TRACE` for `clone`, `fork` and `vfork`. `clone3` already
fails with ENOSYS and `CLONE_UNTRACED` with EPERM in the first filter, and `ERRNO` outranks `TRACE`, so
every task a tree can create passes one stop first. `fork` and `vfork` exist on amd64 and 386 only.

At that stop, in a task that runs nothing:

```
used = tracked tasks + reservations
used < limit                                                     ->  admit
the caller is in the root's process and used < limit + reserve   ->  admit
the caller is making a thread of the root and used < 2 x limit + reserve  ->  admit
otherwise                                                        ->  refuse
```

**Refuse:** set the syscall number to -1 and the return value to `-EAGAIN`, in the seccomp stop. The
kernel skips the syscall and returns that value. This is the documented use of the stop, and what the
kernel's own seccomp selftests do on each architecture. `arm64` writes the number through
`NT_ARM_SYSTEM_CALL`.

**Which syscall** comes from the registers, never from `SECCOMP_RET_DATA`: when a filter of the tree's
own also answers `TRACE`, its data can be the one reported, and it is written by the tree.

## Exact accounting

An admitted creation is not a task yet, and many can be in flight, so each one holds a reservation,
keyed by the caller's TID, until the kernel says how it ended:

| End of the syscall | Report | Reservation |
|---|---|---|
| A task was created | `FORK`/`VFORK`/`CLONE` event | moves to the new entry; continued with `PTRACE_CONT`, so no exit stop follows |
| It failed (kernel `EAGAIN`, `EINVAL`, a restart) | syscall-exit stop, because an admitted caller is continued with `PTRACE_SYSCALL` | released |
| The caller died | `EXIT` event, its wait status, or an `EXEC` under its number | released |

A successful creation costs one more stop than before. Only a failed one pays for the exit stop.
`ERESTARTNOINTR` (`copy_process` saw a signal) is a failure and comes back through the seccomp stop,
which admits it again.

The count is exact from above: `tracked + reservations` never passes the bound at an admission. It can
read high for a moment. A child's first report can come before its creator's event, and until then the
child is tracked and its creator's reservation still stands, so a creation in that window can be
refused one early. A task also counts until the tracer reaps it. It can't read low, except for what
was never tracked. A zombie the tracer reaped still waits for its real parent, and `io_uring` workers
are never traced; both are as before and stay bounded by the kernel's own limits. A filter of the
tree's own that answers `USER_NOTIF` for `clone` could skip the stop, but the tree cannot create a
listener. A task that gets past is tracked at its first report and counted as usual.

## Composition with the freeze

Every decision is made at the stop, at any time, and only the resume differs.

- A stopped caller is held like any stopped task. A freeze holds it, and a thaw continues it into its
  decision: the refused `EAGAIN`, or an admitted creation. A barrier pause holds it the same way.
- A seccomp stop of a creator never enters the exec barrier, even from a non-leader.
- Inside the kernel an admitted caller behaves as before: a pending `PTRACE_INTERRUPT` shows as a
  signal to `copy_process`, so it reaches its exit stop and then its `EVENT_STOP`.
- A refusal changes no task and frees no TID, so the proofs of the freeze design stand. Refusing is
  also idempotent with respect to teardown: a task killed at the stop simply reports its exit.

Any tracer request that fails with ESRCH classifies the task as dying, as everywhere else. A task can
stay at the limit under a storm, and the tracer takes each stop like any other; a freeze still
completes by `PTRACE_INTERRUPT` (tested at the real limit).

## The engine keeps its threads

A refused thread is fatal to many programs (Go aborts; some Rust and C++ runtimes too), so the engine
must not lose a thread to any tree, whoever filled it. Two rules do that.

The root's process may pass the limit by `rootTaskReserve` (64). No descendant can use it. It is what
lets the engine start a tool in a full tree, and its own children spend it like anything else.

Past that, a `clone` with `CLONE_THREAD` from the root's process is still admitted, up to twice the
limit plus the reserve. The flags come from the registers of the held caller, so they are the ones the
kernel reads. Everything except the root's threads stops at limit + reserve, so at that ceiling the
root's process alone holds at least a limit's worth of threads: only an engine that runaway has a
thread refused. The ceiling stays because the root can be anything: a shell can `exec` a thread bomb,
and that must not reach the kernel's limits. The ceiling bounds the tree at about twice the limit.
Threads the root makes past the limit do spend the reserve, so an engine that grows while the tree is
full can lose its tools before it loses anything else.

Surviving is not enough if every thread takes seconds. `wait(-1)` takes the first report in the
kernel's list, and at the limit the tasks ahead of a new thread keep having one. So for 100 ms after a
refusal the tracer asks for the reports of the root's tasks by number first. A fork bomb's tasks are
leaders other than the root, so refusing them needs no `/proc` read. Measured with a bomb at 4096: 19
engine steps a minute before this, and the reserve spent and every check passed in 24 s after.

A descendant's tasks get no such priority. Under a live bomb a tool of the engine makes slow progress,
one that can be seconds for each stop, until the bomb is stopped.

The reserve also lets the engine start a shell while the tree is full. That shell's own children are
refused like any descendant's, so an agent facing a live fork bomb may only reach commands the shell
execs directly. A bomb that never exits leaves the session alive and full until the person stops it
or the bomb is killed. During teardown each recorded task is queued for SIGKILL once; births
reported after teardown starts join that queue too. Resending the signal to every remaining task at
every wait event would make cleanup quadratic at the process limit.

## Telling the agent and the person

The helper's stderr reaches neither. Claude treats it as noise in its stream, and Codex's goes to a
bounded diagnostic. Writing to the refused task's own stderr from the helper would let a trusted
process write into a tree file during a freeze, and could block. So the line travels the control path:

```
tracer: first refusal, or first in a minute  ->  non-blocking signal to the helper's main goroutine
helper: frame {Op: "limit"}                  ->  Group.OnLimit in the daemon
daemon: a system note in the transcript, at once; the line waits for the session's next input, whatever
        that is, and goes to the agent with it, once, in Blerg's marked voice
persistent shell: the line on the terminal, where the person (or agent) driving it reads it
```

The notice is never an input of its own. A background process must not start turns, and no engine can
promise that a message sent during a turn starts no other: the turn can end first, or its last response
can already be on its way, and the queued message then starts one. So an agent in the turn that hit the
limit reads only its command's `EAGAIN` until something else reaches it. Engines with no marker code
(Hermes, OpenClaw) read the same line as plain text.

The tracer never waits for this: a slow daemon loses a notice, not a stop. The refused command still
gets its plain `EAGAIN` ("Resource temporarily unavailable"). The line:

> blerg: process limit reached (4096 tasks); retry with less parallelism (GOMAXPROCS, go -p, make -j,
> test workers).

## Hosts

| Host | Behavior |
|---|---|
| Linux, WSL2 (host workspace and cluster sessions, shells) | as above; no cgroup needed |
| Sandboxed sessions | the tracer sees the container client, not the work; Docker's own `--pids-limit` refuses with `EAGAIN` and no Blerg line |
| macOS | no tracer: containment is unsupported there and no Blerg limit applies. `fork` fails with `EAGAIN` at `kern.maxprocperuid`, the session lives or dies with the engine's own handling, and no Blerg line is shown |

## Cost

One more stop, and one register read, for each created task. Nothing at rest. Measured on 5.15 (WSL2,
32 CPUs, alternating old and new, load average 12 to 37 from other work, `TestMeasureTurnEnd`):

| | before | after |
|---|---|---|
| `go build -a net/http`, quietest pair of rounds | 12.0–12.8 s | 9.2 s |
| `go build -a net/http`, all rounds | 8.5–21.2 s | 9.2–22.0 s |
| turn end, p50 | 6.7–9.0 ms | 6.6–8.9 ms |
| 1000 `/bin/true`, quietest pair | 5.7–5.9 s | 5.6–5.7 s |
| 1000 `/bin/true`, all rounds | 5.6–8.7 s | 5.6–12.3 s |

The load swings each column more than the change does, so a build or a turn end shows no cost that can
be told from the noise. A hostile history does show one: eight processes forking flat out and killed
every few milliseconds took 9 to 15 s before and 10 to 25 s after. The fork-heavy worst case is about
twice as slow; the same history under constant freezes ranges from 23 to 98 s.

## Left out

- A cgroup `pids.max`: kept as a possible hardening, not needed for the ruling.
- A way to clear a full tree short of stopping the session: a product decision, not made here.

## Proof

Through the real tracer (`limit_linux_test.go`, `testdata/limit.c`), outside a session because they
need their own tracer: every creating syscall refused exactly and repeatedly; no reservation leaked by
failed clones, restarts, tasks killed in a creation or a sibling's exec; the same under constant
freezes with the barrier running at the limit; a task killed as it is refused; a tree's own `TRACE`
filter; the root's reserve, with its threads going on past it, from a tree filled by a worker or by the
root itself; a real Go engine at the end of its reserve; a fork bomb at the real 4096 through the helper,
with the root creating threads and spending its reserve, notices, three turn ends and an exact census;
the shell's line; a notice from a real bomb in a turn that then ends, which starts no model work
(`daemon/tasklimit_linux_test.go`). All on x86-64 WSL2 5.15. Native Linux runs the same code
in CI. `arm64` and `386` compile but have never run: the register code there is the part to watch.
