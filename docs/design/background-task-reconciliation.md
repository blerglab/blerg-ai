# Background tasks across daemon restarts

A task whose daemon process ended must leave both the session's running count and
its live panel. Recovery records remain useful, but they are not the authority for
finding every abandoned task: a crash or a second restart can lose them.

## Contract and authority

New daemons send `background_task_inventory` in their hello and heartbeats:

```json
{"lifetime":"00000000-0000-4000-8000-000000000001","sessions":{"00000000-0000-4000-8000-000000000002":["task-id"]}}
```

The lifetime is a random identifier generated once for the daemon client process;
reconnecting retains it. The inventory is a full snapshot of observed live task
IDs, without descriptions, outputs, URLs or credentials. An empty object for
`sessions` is known-empty. An absent, null, invalid or over-limit inventory is
unknown and cannot trigger cleanup. Old peers retain their existing behavior.

The server accepts the lifetime from a valid hello. Later inventories must match
it and come from the currently registered connection. Connection replacement,
heartbeat reconciliation, background ingestion and child ingestion share a fence
keyed by the stable daemon ID. Old and replacement sockets for that daemon use
the same fence; unrelated daemons never wait for it. The hub lock protects only
fence bookkeeping and is never held while waiting for a fence or database I/O.
Waiters pin their fence, and the final release removes it so disconnected IDs do
not accumulate. The fence covers ownership checks and durable reconciliation or
work appends; acknowledgements, task notifications, token healing and input replay
run after release. Follow-up token and input delivery rechecks the current socket
atomically with its nonblocking enqueue; a connection superseded during database
work receives neither, and an undelivered fresh token is revoked.
A connection that was once registered cannot regain authority after its
replacement disconnects, even when inventory is unknown. Database row locks also
recheck session ownership; a pod is confined to its authenticated session.

Incoming task events get their origin from that connection. They cannot supply
reserved origin or start-target markers. Starts from the current lifetime always
survive reconciliation, including starts that race an older snapshot or await
outbox replay. A prior-lifetime task explicitly reported alive is rebound to the new lifetime
with a deterministic ordinary start event, preserving its original start time.
Its genuine completion can then close it normally. The original causal start
sequence is retained so this association cannot adopt a newer foreground child.
Daemon outboxes are memory-only: they cannot replay an old process's start after
a process restart. The recovery path's persisted input receipts are not task events.

## Durable closure and counts

Reconciliation appends a normal `background_task_done`, `status: stopped`, with the
fixed summary `the daemon restarted`. Its deterministic event ID and target start
identity prevent repeat restarts or sweepers from closing an incarnation twice.
A reused task ID is a new incarnation; an older targeted completion cannot close it.
For an orphaned background agent, a companion final `subagent_done` closes its
card, scoped to its original child start. Normal child ingestion uses the same
row lock, so a newer child start cannot race that decision.

Task append and exact running count commit together under the session row lock.
Two starts describing the same live task count as one task, and repeated completion
events cannot decrement an unrelated live task. The live-state fold and database
accounting share this identity logic. Existing tests of two simultaneous tasks use
two distinct task IDs and preserve their original `[1, 2, 1]` assertions.

Committed synthetic events use normal subscriber fanout and count broadcasts.
Partial reconciliation failures still publish changes committed for earlier sessions.
No migration or frontend change is required.

## Validation

Tests and independent review cover real socket hellos, two restarts, old peers,
continuing tasks, stale inventories, reused IDs, concurrent completion and cleanup,
wrong ownership, and transaction rollback. Independent tests reproduce the stale-heartbeat failure, origin-mismatch
completion failure and reused-child failure before the fixes. Final validation: full Runner tests after rebasing onto the release branch
passed in 217.79 seconds. Runner vet, all four modules’ pinned Go lint, scrub
and link checks passed. Repeated protocol/daemon races, real-Postgres integration
races, and ten repetitions of all independent server/database/live-state
adversarial races passed. Negative controls failed when incarnation/causal-start
checks were removed. No migration is required.

## Review follow-up: daemon isolation

The real-Postgres `TestBackgroundBlockedDaemonDoesNotStallOtherDaemonEvents`
locks one session row and waits until its daemon's reconciliation is blocked on
that row. While that lock remains held, a different daemon must register, persist
both background-task and subagent starts, and receive both acknowledgements.
The test fails with the original Hub-wide lock.
`TestDaemonWorkFencePinsWaitersAndReclaimsEntries` checks that a queued waiter
retains the same fence across release, unrelated IDs remain independent, repeat
release is harmless, and unused fences are reclaimed. Existing stale-heartbeat
and superseded-socket tests continue to protect connection authority.

After the review fix and rebase onto the release branch containing guide parity
and test hygiene, the full Runner suite passed in 302.89 seconds. The background
and fence tests passed 20 repetitions under the race detector. Independent stale
heartbeat metadata and stale follow-up dispatch tests passed three race
repetitions; restoring the old dispatch failed the negative control. Runner vet,
all four modules' pinned lint, scrub and link checks passed.
