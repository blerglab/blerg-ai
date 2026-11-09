# Isolated sandbox pack serving

At turn end, the daemon still pauses the session container before checking its private clone,
resetting its object alternates from the daemon's record, and fetching its single session
branch. The session is unpaused after the fetch, including when a check or fetch fails. A
checked-out destination branch still receives work on its `.incoming` branch.

The fetch now starts a separate disposable container to serve the clone's exact `.git`
directory with `git upload-pack --strict`. It uses the existing local sandbox image, with
`--pull=never`, `--network=none`, a read-only root filesystem, no capabilities, no privilege
escalation, 512 MiB aggregate memory, and 64 processes. Its only host mounts are the clone
and, when borrowed, the original object store, both read-only. It receives no host credentials,
Docker socket, engine home, or repository worktree. Serving Git runs with an empty environment
and the same hardened configuration used before, with one packing thread.

The Docker client starts outside the host Git address-space limit. Two anonymous pipes carry
protocol bytes between it and a fixed shell relay used as the fetch's upload-pack command.
The host fetch never receives a clone path as its remote: its synthetic file URL is handled
only by that relay. `fetch.unpackLimit=0` forces `index-pack`, with `fetch.fsckObjects` and
`transfer.fsckObjects` enforcing strict validation on receipt. The existing host memory and
time bounds remain. There is no host upload-pack fallback when Docker or the image is missing.

The disposable server has a random name, `--rm`, and a server-side timeout bounded by the
fetch budget. The daemon also attempts a bounded forced removal in the same pinned Docker
context on completion or cancellation. The server-side timeout remains a final bound if the
Docker client disconnects or cleanup cannot reach Docker. A failed fetch keeps the clone.
Existing metadata checks remain as defense in depth, even though the serving parsers now run
inside the container.

The selected Docker regression inspects the production container settings, fetches three
successive turns while a real source container is paused, checks unpause and cleanup, and
reports each whole turn-end duration. These small-repository timings measure startup and
cleanup overhead, not large-history throughput. Run it with:

```sh
cd runner
BLERG_SANDBOX_CHILD_TEST=1 GOWORK=off go test ./internal/daemon -run TestSandboxPackServerInDocker -v
```

Build the sandbox image first as described in [the image guide](sandbox-image.md). Ordinary
unit fixtures use a fake Docker CLI that executes the same Git protocol peer and check the
isolation arguments separately; the opt-in test verifies the real container boundary.

Workspace copy uses two disposable, no-network pack containers as well. The server
mounts only the sandbox allocation read-only, with no source checkout or source Git
directory. It runs the hardened `hostgit.ServeCommand("upload-pack")`. The writer
mounts the source checkout read-only and its common Git directory writable, with no
sandbox allocation or private launch record. Its hardened fetch receives the pack
through a fixed relay and strictly indexes it. Neither peer has both data mounts.

The validation/settlement control worker mounts only the writable sandbox allocation
and its private launch record, with no source checkout, source object store or common
Git directory. Clone checks, alternates reset and settlement run there. The daemon
checks source identities and uses hardened host Git for checked-out-branch queries,
the destination ref and streamed tree/blob digests. Matching digests and local byte
verification prove the saved tree without exposing both stores to a container. It asks the
daemon for each transfer over bounded JSON control messages (at most 3 MiB per line).
The daemon checks the clone, destination and branch against its recorded request before
starting the peers. Anonymous OS pipes and a fixed-size streaming copy connect their
Docker clients: pack bytes never accumulate in memory. The server-to-writer protocol
stream has a 1 GiB byte cap, including Git protocol framing. Exceeding it cancels both
peers, reports a size-limit error and retains the allocation. Kernel backpressure bounds memory.
Transfer failure leaves that member pending while other members can still sync.

Both peers retain the worker's pinned Docker context, entry security policy, read-only
root, dropped capabilities, 512 MiB memory and 64-process bounds. Each has an independent
in-container timeout of at most 60 seconds, additionally bounded by the copy deadline.
They share the operation's recovery label; completion and cancellation remove their
exact random names and prove absence. Unproved cleanup keeps the worker recovery fence.
Neither a missing image nor a failed transfer falls back to a host parser.

The daemon accepts the allocation worker's clone digest, byte-verification result and
`SettledClean` verdict. It does **not** independently recompute the clone digest through
daemon-side hostgit. This is an accepted trust assumption here: allocation Git parsers
stay inside the bounded container, including on Docker VM volumes that the daemon cannot
read directly. The daemon independently hashes the destination tree, but a compromised
allocation worker could lie about its own tree or cleanliness and cause premature cleanup.
Mount isolation limits access to the person's repositories; it does not make this verdict
independent. An independent release proof remains future work.

All control-worker actions and both pack peers pass the same Docker mount validator
before launch. Unknown actions are refused. Membership checks, discards and retirements
receive no source mounts, just as copy and release do. Source mounts are permitted beside
the allocation only during initial preparation, before a sandbox runtime has existed:
the allocation then contains only daemon-created clones, not sandbox-controlled data.
Live membership additions clone through a source-only staging container. Its private
token-scoped directory sits beside `tree` on the allocation's quota filesystem; only
`tree` is visible to the sandbox. A separate container with no source mounts verifies
the receipt, plain metadata, absence of config includes and active hooks, and strict
full fsck before moving the independent clone into the workspace. Source-free helpers
create and clean staging. Unknown cleanup outcomes retain the fence and journal.
The regression inventory lists every worker action and compares it with the dispatch
cases, then drives the production Docker arguments, so adding an action requires coverage.

Workspace preparation now dissociates every member's object store before launch, so
serving never needs even a read-only mount inside the person's Git directory. This
uses additional allocation space, included in the existing workspace budget checks.
Older allocations still borrowing source objects first try a migration inside the
allocation-only control worker: clear alternates, check all local objects with strict
fsck, and dissociate. This succeeds only when all required objects already exist
locally; it never reads or mounts the source store. A failure restores the recorded
alternate and retains the allocation, with instructions to start a new session for
future work and recover this stopped allocation into an independent repository before
importing its commits. Do not delete the retained allocation. Successful migration is
saved in the private member record. Preparation accounts independent objects against
the aggregate Git budget before launch and removes partial clones on dissociation failure.
For a root member, cleanup empties the clone while preserving the allocation directory
and its mount. Exclude-file installation failures remove the newly prepared clone too.
Migration failure messages name the retained path and distinguish missing objects from
historical commits that may fail strict validation; neither case deletes the old history.
Gitlink identities participate in the tree digest without requiring external commit
objects. Missing or empty submodule directories can settle; populated submodules retain
the allocation because the parent repository's gitlink does not save their local files.
Stop-time fetch retains the session's Docker context through container
removal and final pack serving.

The mount regression inspects real Docker argv for the control, server and writer
containers of a copy; peer assertions also cover bind and volume-backed allocations. The real-image workspace
test builds a Linux worker into a disposable sandbox image and exercises VM-backed sync:

```sh
cd runner
GOWORK=off go test ./internal/daemon -run 'TestWorkspaceCopyAllContainerMounts|TestWorkspacePackPeersHaveDisjointMounts|TestWorkspacePackControlIsBounded'
BLERG_P2_DOCKER_TEST=1 GOWORK=off go test ./internal/daemon -run TestP2DockerImageWorkerRunsWithVMQuotaStorage -v
```
