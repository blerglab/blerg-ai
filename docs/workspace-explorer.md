# Workspace explorer runtime seam

The P4 explorer consumes the immutable workspace manifest and P0b's prepared
`WorkspaceRuntimeBinding`. The manifest's legacy `Root` is never file authority.
An absent binding, an unsupported descriptor backend, or a changed root is
unavailable. Existing human owner/authorized administrator viewer permissions
apply; another person's private session remains absent.

macOS supports the explorer's descriptor traversal and bounded Git status
backend, but 0.5.0 does not admit workspace sessions or owner shells that run
directly on macOS hosts. Those launches refuse before allocation or journaling;
see [platform support](workspace-integration.md#macos-host-support-in-050).
Sandbox sessions on a Mac keep their Linux runtime reader.

## Launch and runtime integration

The trusted lifecycle coordinator (P1/P2/P3, integrated by P6) must:

1. Authorize the launch, fence the old writer, and call `db.BeginWorkspaceRuntime`.
2. Bind its returned `WorkspaceRuntimeRef` to the authenticated daemon/pod
   connection using `DaemonConn.AuthorizeWorkspaceRuntime` before dispatch.
   Reconnect requires trusted reauthorization; a runtime-prepared frame cannot
   authorize its own generation.
3. After creating the actual public directory, pin its identity. In the owning
   runtime process, construct `daemon.NewLocalExplorer` and register it with
   `Manager.BindWorkspaceExplorer` or `AgentHost.BindWorkspaceExplorer`. Those
   methods also accept fake/remote `WorkspaceExplorer` implementations. A runtime
   that provides files implements `WorkspaceExplorerFileReader` as well.
4. Send `workspace_runtime_prepared` on that same authenticated connection. The
   server validates connection, session, trusted runtime reference, manifest
   digest and P0b persistence fencing. Root registration is write-once per
   runtime generation. The authenticated WebSocket read pump holds the desktop
   ownership or runtime launch fence while registering the root; the handler
   must not reacquire that non-reentrant fence. Desktop replies also revalidate
   persisted owner/revision/connection generation under the ownership fence
   immediately before HTTP delivery.
5. Cancel and join outstanding explorer operations before freezing, replacing
   or releasing runtime storage. `WorkspaceExplorerRouter.Unbind` joins that
   session's readers/Git workers; `Close` joins all workers. This proves reader
   cleanup only, not containment of agent or shell writers. Close each local
   explorer's descriptors after unbinding it.

For sandbox traversal, build `runner/cmd/workspace-explorer` for the Linux runtime
and make it available inside the image. Start it **inside the container**, with
fixed argv `workspace-explorer --public-root <trusted-public-root>`, over private
stdin/stdout pipes owned by the coordinator. P4 does not create Docker containers
or implement P2's lifecycle/identity checks. The production sandbox image packages `blerg-workspace-explorer`; the daemon
registers its private Docker-pipe adapter after authenticated runtime binding. Pods can embed the same engine in their existing outbound
connection; no Kubernetes exec or new RBAC is needed.

The helper's first JSON line is private `daemon.WorkspaceExplorerStart`:
session ID, immutable manifest, trusted runtime reference and optional private
Git pins. It pins the newly created root and emits `WorkspaceRuntimePrepared`.
Subsequent newline-delimited requests are `workspace_explorer`,
`cancel_workspace_explorer`, `read_workspace_file` and `cancel_workspace_file`.
Every request/result carries the frozen `WorkspaceRuntimeRef`. Configuration
must come from trusted launch state, never agent files or browser-supplied
manifest fields. The coordinator owns helper termination, private pipe closure,
process containment and forwarding. Do not connect its pipes to an owner shell.

`NewLocalExplorerWithGitPins` accepts exact private `.git` and object-root
identities plus exact alternates-file contents from P2's clone validator.
Missing, switched or unexpected alternates fail closed. These private pins never
appear in the public protocol. Host registered worktrees have their metadata
registration and external descriptors pinned at preparation; later `.git`
pointers cannot establish a new exception. Sandbox/pod gitdir pointers do not
receive that host exception.

Host unrelated selections use `NewVirtualHostExplorer`: the coordinator supplies
a pinned private namespace allocation and individually pinned selected sources.
The virtual tree never enumerates the namespace's files or a common filesystem
ancestor. Search shares its visited-entry/hit/reply/time budget across all sources.
Folder refresh discovers repositories for badges only; it never changes launch
membership, result provenance or project authority.

## Browser proxy

`GET /api/sessions/{id}/workspace` accepts `op=list|search|status`, `path`, `filter`,
`cursor` and optional `member`. Empty path denotes the pinned public root. The
server supplies the trusted runtime reference, not the browser. The response has
`result`, public manifest `members`, and semantic `runtime`. Failures are explicit
`error_code` states; error never means clean. Counts describe the visible paths
in a returned status and are partial when truncated.

Status copies bounded HEAD/index/refs into a private temporary metadata view;
repository configuration and info attributes are not copied. Git reads the pinned
worktree and object directory by inherited descriptors, with fixed safe config,
no optional locks, no rename/submodule work, a two-second deadline and 256 MiB
worker memory budget. Linux retains its inherited address-space limit. macOS does
not use `RLIMIT_AS`/`ulimit -v`: a supervisor samples the entire Git process group's
resident memory with the system `/bin/ps -o rss= -g <pgid>` every 20 ms, summing
Git and its helpers in KiB. Each sample has a 100 ms deadline and 64 KiB output
cap. Exceeding 256 MiB kills the whole group and returns `hostgit.ErrMemoryLimit`
(refusal code 126); an observation failure also kills the group and returns
`hostgit.ErrMemoryMonitor` (code 125). The supervisor is canceled and joined on
completion, timeout or cancellation. This is an observed RSS budget: memory can
overshoot between samples, and short-lived allocations between samples may go
unobserved. Bounded macOS calls must use `Run`/`Output`/`OutputBounded`; the raw
`Command` API refuses `MemoryLimit` or `DirFile` on Darwin because these require
the supervised launch APIs.
On macOS the bounded private metadata snapshot also copies
the object store. Worktree access uses a held directory descriptor: a disposable,
locked OS thread sets its thread-local cwd with `pthread_fchdir_np`, then starts
Git with an empty command `Dir`. Darwin's fork inherits that cwd; the daemon's
process cwd remains unchanged. The thread exits locked and is retired on both
success and failure. `/dev/fd/N` can duplicate an open descriptor on macOS but
cannot be used as a searchable cwd or as a directory prefix. Linux retains its
`/proc/self/fd/N` cwd and inherited object descriptors. Repository-defined
clean/process filters cannot run. Counts use
this safe configuration and may differ from an interactive Git command that
honors custom filters. The coordinator must protect private temporary storage
from agent writes, keep it outside the public tree, and account for its bounded
metadata allocations within the runtime quota. Temporary metadata is removed
after each worker; original repository metadata is not rewritten.

Directory pages have at most 500 entries and a 1 MiB reply. Signed cursors bind
launch/runtime, directory identity/content metadata, filter and one-minute
expiry. Search is name-only: depth 32, 10,000 visited entries, 200 hits, 1 MiB,
and two seconds shared per operation. Names the existing viewer cannot safely
address (invalid UTF-8 or control-bearing paths) are omitted rather than
normalized to a different file. Status treats NUL-delimited Git paths as hostile,
filters protected paths and deduplicates visible counts.

On macOS, a directory listing retains its full descriptor ancestry for the
operation and opens each child relative to that directory. Every child open
revalidates ancestor metadata, named child identities, parent relationships and
device boundaries; final delivery rechecks the listing and its named directory.
This avoids repeating a complete deep walk for every entry while keeping the
two-second deadline. Linux continues to use `openat2` for each child.

Explorer admission permits two global operations and one per person/daemon,
inside the existing four-global/two-per-person/two-per-daemon viewer limits.
Thus explorer work cannot consume the viewer's reserved capacity. Navigation,
disconnect, replacement and stop cancel outstanding work. Status is requested
only for expanded repositories, at most once per session, cached for five
seconds without polling. The existing workspace-file route keeps its viewer
size limits; v2 reads use the runtime file-reader seam and both generation fences.
Legacy sandbox file reads use the same in-container helper and translate only
trusted launch aliases; there is no host-clone parsing fallback.

## Platform release gates

Linux requires `openat2` with `RESOLVE_BENEATH`, `RESOLVE_NO_SYMLINKS` and
`RESOLVE_NO_XDEV`; missing support is unavailable, including on macOS-hosted
Linux sandboxes. macOS host traversal uses a per-component no-follow descriptor
walk with device/identity checks. Symlinks, hard links, FIFOs, devices, sockets
and mount crossings cannot supply file content or child metadata.

Status now has Linux and macOS implementations with a requested 256 MiB
worker limit. macOS uses a bounded private object-store copy and inherited cwd,
without Linux descriptor paths. The former unconditional refusal and status-test
skips are removed. Native macOS functionality, memory enforcement and traversal
containment remain release gates: neither cross-compilation nor the shell
accepting a limit proves them. The workflow below requires actual native passes
without non-Docker skips. The exact Docker-pending exception below is not full
platform acceptance. Independent adversarial security review remains required.
Linux mount tests run in a private user/mount namespace so bind/tmpfs boundaries
are actually exercised.

## Native macOS acceptance

The retained descriptor walk validates every opened parent/child inode and the
child's `..` relationship against the pinned root before advancing and before
returning. Opened ancestors stay held through the entire walk. Final file and
directory delivery repeats the named open and identity checks. Device changes,
symlinks, replaced ancestors and an opened ancestor moved outside the root fail
closed. Descriptor pinning does not prevent the owner from changing files.

Status integration tests install a private diagnostic sink. Unavailable results
print the failing stage, underlying error, and up to 4 KiB of Git stderr in test
output; snapshot errors identify the copy/setup step. The process-helper test
forwards these diagnostics on stderr, separate from its JSON pipe. Private paths
and stderr are never added to explorer protocol replies or production logs.

`.github/workflows/workspace-explorer.yml` runs the common adversarial matrix on
Linux and macOS. The macOS gate requires all status, worktree, filter, memory,
helper, special-file, real disk-image mount, deterministic ancestor-move and
exhaustion tests to pass without skips. It uploads JSON test evidence. The native
harness collects all three package logs and records their exit codes even when
an earlier package fails; any failed command still refuses acceptance. Go tool
stderr is retained in companion `.stderr.log` files so download/build diagnostics
do not corrupt the JSON event streams. In
particular, `TestWorkspaceStatusMemoryBoundary` verifies that a native worker
can start with a small allocation, a 300 MiB allocation succeeds without a cap,
and the same allocation fails with the exact 256 MiB budget. The probe holds
touched pages resident across several sampling intervals; macOS must report the
specific RSS-overrun refusal, rather than an unrelated startup failure. A separate
Git test
requires bounded Git to start and rejects an unbounded reader before its timeout.
A shell accepting `ulimit` alone is not acceptance evidence.

The harness records `docker_present` from the isolated test environment's PATH.
When Docker is present, it sets `BLERG_EXPLORER_DOCKER_TEST=1`; the production
sandbox image test must run and pass. An installed but unavailable Docker daemon
or missing production image is a failure, not an absence waiver.

When Docker is absent (as on GitHub's hosted macOS runners), the only permitted
skip is the exact daemon test `TestWorkspaceExplorerProductionDockerImage`:
**pending: requires Docker Desktop on macOS and the locally built production
sandbox image**. The checker records each pending case's package, test, reason
and `accepted: false` in metadata and prints it as **NOT accepted**. All other
tests must pass, and all three packages must complete. A passing host gate sets
`native_host_accepted: true`; overall `accepted` remains false until every Docker
case also passes. Any unlisted skip, listed subtest skip, or listed skip with
Docker present fails the gate. Missing Docker presence or test evidence also
fails. The checker updates metadata on both success and failure so an old
acceptance cannot survive revalidation.

`TestWorkspaceQuotaGuest` and its Docker launcher,
`TestP2DockerPersistentHostFilesystemQuota`, are Linux-tagged tests. The guest
executes the Linux-only quota backend with privileged ext4 loop mounts and
kernel mount/device identity checks through `/proc` and `/sys`. Its launcher
mounts the current Linux test executable into the privileged guest. These tests
belong to the Linux guest acceptance campaign; they are not native macOS host
cases, even when Docker Desktop is installed. On Linux, run the launcher with
`BLERG_P2_DOCKER_TEST=1` and a prepared production sandbox image. It sets
`BLERG_P2_QUOTA_GUEST=1` only inside the disposable guest. The macOS Docker
pending allow-list remains limited to the production explorer image test.

After the integration branch is available to GitHub, run:

```sh
gh workflow run workspace-explorer.yml --ref feat/workspace-p4-050
gh run list --workflow workspace-explorer.yml --branch feat/workspace-p4-050
gh run watch <run-id> --exit-status
gh run download <run-id> --name workspace-security-macOS
python3 scripts/check-workspace-macos-gate.py --metadata <evidence>/metadata.json --commit <tested-commit> <evidence>/workspace-daemon.json <evidence>/workspace-hostgit.json <evidence>/workspace-policy.json
```

The equivalent native commands, from `runner`, are:

```sh
GOWORK=off go test -race -json -p 1 -timeout 45m ./internal/daemon -run TestWorkspace -count=1 > ../workspace-daemon.json
GOWORK=off go test -race -json -p 1 -timeout 45m ./internal/hostgit -count=1 > ../workspace-hostgit.json
python3 ../scripts/check-workspace-macos-gate.py ../workspace-daemon.json ../workspace-hostgit.json
```

This Linux-host fix round does not supply a native macOS passing run. The current
GitHub credential returns 404 for workflow and branch lookups;
the task prohibits pushing. Cross-compilation and running the shared descriptor
implementation on Linux are additional checks, not substitutes for native
acceptance. P6 must retain the macOS release gate until the above native tests
pass and the run URL/artifacts are recorded. The native memory gate must prove
that macOS observes and refuses an over-budget process group; cross-compilation
cannot validate the system RSS sampler.

## Production desktop registration

Host launches consume P1's held selection descriptors and obtain an acknowledged
runtime generation/binding through the desktop authority adapter before starting
the engine. Physical folders register `LocalExplorer`; unrelated selections
register `VirtualHostExplorer` and start the engine in the first selected source.
Both check current authority at dispatch and delivery. Live connection recovery
reauthorizes the same binding. Reconstructing host admission after a complete
daemon process crash remains part of the broader lifecycle/recovery gate.

Sandbox launches register a lifecycle-serialized remote explorer after P2 binds
the actual runtime. Trusted source object targets travel only on private helper
input; the helper pins their actual runtime identities. Its private prepared reply
also records member/Git identities, retained across cancellation/restart. Browser
requests cannot supply or replace them. Manager forwards agent explorer/viewer
requests to AgentHost; terminal sandboxes use their registered legacy reader.

Sync cancels/joins pending reader work while keeping the pinned registration.
New work must acquire the session mutex without waiting and refuses during
freeze/stop, avoiding a nested reader-join/lifecycle lock. This is an explorer
coordination guarantee; P5 supervisor/lease and complete runtime cleanup proofs
remain separate release obligations. See [sandbox image](sandbox-image.md) for
packaging, helper privilege separation and the opt-in real-image test.

## Explorer layout

The session header's folder control and the **Toggle explorer** palette command
open the workspace tree. There is no global explorer shortcut: Ctrl+Shift+E is
reserved by Firefox and editable/terminal input keeps its keys. Quick open
remains Ctrl/Command+P, with its existing bounded search provider and shared viewer.

Desktop order is session list, explorer, chat, session panel. The explorer uses
the session list's background and a compact search row. The edge chevron, header
toggle, palette command, or dragging the divider to its left edge collapses it.
The header toggle or palette brings it back. Open state and width are remembered
per person under `blerg-runner.explorer.<encoded-account-id>` in localStorage.

Default width is 20% of the viewport (at least 280 px); minimum grows from 220 px
to 8% of the viewport. The maximum is 40%, bounded by actual session-column space
after the session panel and a **320 px chat minimum**. If the minimum columns do
not fit, the session columns scroll horizontally; chat, panel and session list
retain usable widths. Saved widths are clamped on resize without overwriting
them until the person chooses another width. Divider arrows resize by 20 px
(80 with Shift), Home/End choose the minimum/maximum.

The search combobox filters loaded names as the person types and controls a
listbox of matching paths. Up/Down selects an option while input focus stays in
the combobox; Enter opens that selection. Enter without a selection or
**Search all files** performs one bounded workspace search. Escape dismisses the
listbox and returns to the filtered tree; another Escape can close the explorer.
The small refresh icon preserves focus while its asynchronous read replaces the
rows. The tree uses the Files list's shared file glyphs, matching folder glyphs,
indent guides and Git letters with tooltips. Row announcements include the
expanded status meanings, including folder rollups. Visual indentation stops
growing after four guide levels; actual ARIA nesting levels are preserved, and
truncated names retain their full-name tooltip. Letters apply to visible changed
files and roll up onto folders. Untracked-directory status applies to descendants;
deleted paths remain visible as deleted rows. Status is still fetched only for
expanded repositories, with explicit partial/unavailable states, not polled.

Status replies add optional `files: [{path, code, directory?}]` alongside the
existing bounded `paths` and counts. Paths are repository-relative. Codes are M
(modified), A (added), U (untracked), D (deleted), C (conflict), T (type changed)
and R (renamed). The runtime applies the existing visibility/path policy before
emitting codes. The proxy requires one recognized code per matching path when
this additive field is present; older count/path-only replies remain accepted
and do not invent per-file status. The encoded aggregate reply budget includes
both representations. Collapsed untracked directories preserve the existing
`--untracked-files=normal` status command and decorate loaded descendants.

Below 900 px the explorer is a full-screen sheet, with its shared viewer above
it. The underlying session is inert, focus stays within the sheet and refresh
retains its stable control. Closing a file restores its row; Escape or the edge
chevron collapses the sheet and restores the header toggle. The phone's More
menu holds Star/Unstar. The panel's Files tab keeps published artifacts, versions,
downloads and uploads. No explorer behavior changes runtime release gates.
