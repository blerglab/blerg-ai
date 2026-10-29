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

Explorer status shows a branch icon and name with a `Branch <name>` tooltip.
Detached HEAD shows the first seven commit characters with a `Detached at <sha>` tooltip.
The optional `head` status field carries Git's full SHA-1 or SHA-256 object ID;
the runtime parser and server validate it. Older runtime replies without it
remain supported and display `detached` without inventing a commit.

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
registration and metadata descriptors pinned at preparation. Both gitdir and
commondir must be inside the explorer's approved public root, with no symlink
components, and the registration must point back to the selected checkout.
A selection containing only a linked checkout whose metadata lives outside that
root refuses status with `metadata_pointer`; select an approved root containing
both checkout and metadata to enable status. Later `.git` pointers cannot establish
a new exception. Sandbox/pod gitdir pointers do not receive that host exception.

Host unrelated selections use `NewVirtualHostExplorer`: the coordinator supplies
a pinned private namespace allocation and individually pinned selected sources.
The virtual tree never enumerates the namespace's files or a common filesystem
ancestor. Search shares its visited-entry/hit/reply/time budget across all sources.
Folder refresh discovers repositories for badges only; it never changes launch
membership, result provenance or project authority.

## Ignored files and search

Name search (including Ctrl/Command+P) prepares Git ignore data once per
repository, using two bounded Git listings rather than per-directory processes.
Nested `.gitignore` rules, negation, and `.git/info/exclude` apply. Host searches
also honor the user's global `core.excludesFile`, including `GIT_CONFIG_GLOBAL`
and the default XDG ignore file. Sandbox and cluster searches never read host or
agent HOME configuration. System-only `core.excludesFile` is unsupported.
Git reads a private metadata snapshot through the pinned member directory, with
bounded output, memory and execution time. Repository configuration and object
history never enter the search process.

Tracked files win over ignore rules. Only tracked paths beneath ignored entries
need an in-memory traversal index. Ignored directories are not enumerated;
search opens only index-named children and paths leading to independent member
roots or previously discovered host repositories. Outer ignore rules cannot
prune those roots or their ancestors, and each repository applies its own rules.
Discovery remains a host-only badge and never grants launch membership. Opening
an ignored folder explicitly searches its contents.

Without usable Git metadata or Git itself, search skips the common dependency,
build, virtual environment and cache directories listed in
`workspaceHeavyDirectories` in `runner/internal/daemon/workspaceignore.go`, while
preserving routes to independent roots. Preparation shares a one-second allowance
across all members within the two-second request deadline. Each Git output is
capped at 8 MiB, with at most 200,000 tracked paths and indexed ancestors. Refusal
falls back to the fixed directory list. The existing per-request traversal,
depth, hit and reply limits still apply across members. A deadline during the
walk returns accumulated matches with `truncated`, even when the last directory
operation reports another error.

Listing and file viewing retain the existing protected-file policy. Ignored
entries remain fully visible, browsable, and available to loaded suggestions;
list never prepares Git ignore data. Ignore classification is internal search
state and is absent from the entry wire format. Search uses the same policy in
the host daemon, sandbox `blerg-workspace-explorer` helper, and cluster runner's
embedded explorer. Reinstall desktop daemons and rebuild the sandbox and cluster
runner images to update every runtime.

## Compact folders

The runner's explorer defaults to compact folders: a loaded directory containing exactly one
child folder shares a row with that child. Empty, unread or partial listings, files, extra entries
and repository/member roots stop the chain. Repository and member roots keep their own rows
and Git badges; accessible paths use the member's display name. Expanding a row follows its
single-child folder listings until it reaches children to show, without prefetching other branches.
Each segment selects its own folder. The chevron or keyboard expands or collapses every segment,
and a compact row stays closed if any segment was collapsed in the plain view. Git decoration
comes from the last folder. Reopening a cached row revalidates its stale intermediate folders
and every expanded descendant before following any newly extended single-child chain. Deleted
paths retained by Git status break expansion chains exactly as they break displayed rows.
Long paths truncate at the start and show the full relative path in a tooltip.

Chat file references, quick open and Git file navigation send explicit, versioned reveal requests.
Switching file tabs and restoring a cached explorer never issue a reveal. A request loads its
ancestors and selects its row, preserving filters, search results and drill-in roots that still show
the target. Completion is cached so remounting does not reopen a folder the person collapsed.
Reveal keeps focus in the file viewer; merging or splitting compact rows during refresh retains
the tree's existing keyboard focus and tab stop.
Loaded explorer search continues to include individual folders and files inside compact rows.
The per-session cache keeps raw listings, folder expansion and selection, so it works with
either display mode. **Settings → File explorer → Compact folders** changes the display at
once, retaining expansion, and saves a boolean `compact_folders` through the same per-account
`GET/PUT /api/me/preferences` storage as code colors. An unset preference means on; the
browser cache is scoped to the signed-in account.

## Browser proxy

`GET /api/sessions/{id}/workspace` accepts `op=list|search|status`, `path`, `filter`,
`cursor` and optional `member`. Empty path denotes the pinned public root. The
server supplies the trusted runtime reference, not the browser. The response has
`result`, public manifest `members`, and semantic `runtime`. Failures are explicit
`error_code` states; error never means clean. Counts describe the visible paths
in a returned status and are partial when truncated.

The main Git directory may contain sibling checkout records at
`worktrees/<name>/gitdir` and `worktrees/<name>/commondir`. Only regular, bounded
files at that exact depth are accepted as inert metadata (including when a
registration directory itself is named `gitdir` or `commondir`): their targets are never
opened, their contents and identities remain in the status fingerprint, and they
consume the existing metadata entry/byte budgets (200,000 entries, 256 MiB).
These records cannot approve an external metadata root. Top-level redirections
and unapproved object alternates continue to refuse status.

Status copies bounded HEAD/index/refs into a private temporary metadata view;
repository configuration, info attributes and sibling worktree records are not copied.
Explicit `GIT_DIR`, `GIT_COMMON_DIR` and `GIT_WORK_TREE` prevent repository discovery.
Git reads the pinned worktree and object directory by inherited descriptors,
with fixed safe config,
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

The native process test also prints per-operation elapsed time and private
status stage timings with remaining worker budget. Stages cover metadata
pinning/validation, snapshot creation, command preparation/start, Darwin cwd
thread setup, RSS sampling/join, process wait/group cleanup and final validation.
These timings distinguish slow work from cleanup or transport waits; they do
not change the two-second worker budget. On worker deadline, the router joins
the worker and sends an explicit cancelled refusal using a separately bounded
two-second delivery context. Navigation, lifecycle cancellation and stale
registration still suppress delivery. A worker deadline must not silently drop
its reply and leave the helper client waiting for its ten-second pipe timeout.

On macOS the daemon resolves the real developer Git once at startup using the
fixed `/usr/bin/xcode-select -p` command and validates `<dir>/usr/bin/git`.
If selection or validation fails, it falls back to `/usr/bin/xcrun --find git`.
Both commands ignore the caller's `DEVELOPER_DIR` through a fixed environment
and run outside request budgets. Standalone explorer helpers initialize it before
their prepared handshake. Discovery uses the supervised process-group runner
with a ten-second timeout per command, 4 KiB stdout and stderr caps, a fixed
environment, and the 256 MiB RSS supervisor. It never runs
the `/usr/bin/git` shim to discover Git.

The canonical result must be an absolute regular executable named `git`, without
set-ID permissions, under `/Library/Developer/` or `/Applications/Xcode*.app/`.
The executable and every canonical ancestor up to and including `/` must be
owned by root or the daemon's effective UID. The executable must have no
group/world write bits. Directory ancestors refuse world write; group write
is permitted only for root-owned directories in macOS's `admin` group, verified
once by both name (`admin`) and number (GID 80). This permits the default
root:admin `0775` `/Applications`: admin members already have root authority
via sudo, so their directory-write access adds no less-privileged writer.
Other groups (including `staff`), non-root owners with group write, and `0777`
remain refused. Ancestors must be directories, with no remaining symlinks.
An Xcode installation owned by the daemon's account still requires no group
or world write. All ancestors, including `/Library`, `/Applications` and `/`,
are checked. No symlink component is waived. Linux never enables the admin
exception and retains its existing strict policy. macOS system firmlinks
appear as directories; their canonical directory chain receives the same checks.
Extended ACLs are not inspected: an ACL may grant another UID executable-write
or directory-entry creation/deletion/rename rights despite safe POSIX ownership
and mode bits. This validation therefore does not prove that another user cannot
modify or redirect Git when such ACL grants exist. Installations must separately
exclude those grants on the executable and every ancestor. Both successful discovery and failure are
cached for the process lifetime; installing or changing developer
tools requires a daemon restart after discovery failure. Each use revalidates
the cached executable and its directory chain and refuses changed symlink
targets or unsafe metadata. Refusals name the resolved Git path, offending
component and failed check (including actual and trusted UIDs for ownership).
Hardened calls execute that absolute binary directly, ignoring later PATH or
developer-directory changes. Resolution failure reports that real macOS
developer Git is unavailable and the Apple shim is disabled; no PATH/shim
fallback is allowed. Linux retains per-call PATH lookup and its existing limits.

On Linux and macOS, supervised Git retains its unreaped leader until group
cleanup and the RSS-supervisor join finish. Linux observes exit with
`waitid(WEXITED|WNOWAIT)`; macOS polls the typed `kern.proc.pid` sysctl for the
unreaped zombie state every 5 ms. Neither observer reaps. The runner then signals
the group, stops/joins the memory sampler and exit observer, and only then calls
`Wait`. No group signal or sample follows reaping. Remote host clones use this
same lifecycle without adding a memory limit.

`Run` output writers must be intrinsically nonblocking memory buffers,
`io.Discard`, directly inherited `*os.File` descriptors, or implement hostgit's
`ContextWriter` contract. A custom `WriteContext` must return promptly on
cancellation; an arbitrary plain `io.Writer` is refused before process startup.
The runner cancels output on interruption and joins `Wait` and all output copies
before returning. Its owned exec pipes close within the 100 ms post-reap drain
bound if a descendant retains them; there is no five-second return-without-join
escape. Caller-owned file descriptors remain the caller's responsibility.

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

## Shared pane controls

The explorer, desktop session list, session panel and shell dock use the same pane
component. Drag the inner edge to resize, double-click it to reset the default size, or use its focused
separator: arrows resize by 20 px (80 with Shift), Home/End select the bounds.
The collapse chevron occupies the same inner header corner. Collapse leaves a
40 px icon rail; clicking the icon reopens the pane. The explorer uses its
folder toggle at the left end of the session tab bar instead of a separate rail.
Collapsing the list keeps its expand control, New, Notifications and session tiles in one rail. Its scrollable
32 px session tiles show title monograms, stable project tints and the card status
glyphs alongside a separate unread dot, with an accent outline on the current session. Starred sessions come first,
then working sessions, then recent sessions. Children appear as a count on their
parent. The shared hovercard reuses the full sidebar session card, including failure
reasons, Terminal kind, automation facts and the cards of its started sessions. Tab and arrow keys navigate tiles; Enter opens a session. New and the bell
stay pinned while tiles scroll. Phones keep the session list and pane sheets.
Desktop panes never collapse automatically when space narrows. The session panel
uses the same phone breakpoint as the list and explorer. Widths clamp to
available bounds without changing the saved choice, and narrow session columns
scroll horizontally while keeping a usable chat width.

Open state and width are stored per person as
`blerg-runner.<pane>.<encoded-account-id>` in localStorage, for `explorer` and
`session-list`. The session list defaults to 300 px, with a 240 px minimum and a
40% viewport maximum. Explorer defaults and bounds are described below.
The session panel defaults to 340 px (280 px minimum, 40% viewport maximum).
Panel and shell sizes are stored per person under the same prefix with a `.size`
suffix. Bottom and right shell docks remember separate pixel sizes; both default
to 35% of the available space and clamp at 75%. The bottom divider uses Up/Down
arrows and its collapsed rail is horizontal. Collapsing shells retains their
terminal screens and running processes. Pop-out and maximized docks keep their
existing behavior; phones keep the shell sheet.

Ctrl/Command+B toggles the desktop session list from ordinary text fields,
including the chat composer and search. Terminals, code editors, controls that
claim the event, and open dialogs keep their shortcuts. The existing Ctrl/Command+backtick toggles the
shell. Other pane actions are in the Ctrl/Command+K palette: **Toggle session
list**, **Toggle explorer**, **Toggle session panel**, **Toggle shell** (when
available), and **Focus mode**. Focus mode maximizes the active tab and collapses
the explorer, session list and panel. Other split groups stay mounted, including
running shells. Toggle it by double-clicking a tab, Ctrl+Shift+Enter (Command on
macOS, also inside terminals), the palette or the tab bar's **Exit tab focus**
control. Exiting restores split sizes, the original group and pane states/widths;
leaving the session or reloading restores the normal layout. Phones already show
one tab, so focus mode only collapses panes. See [tab focus](command-palette.md).
Opening a pane directly exits Focus mode. No Ctrl+Alt+B, Ctrl+J or Ctrl+Shift+E
binding is installed. The session panel and shell use the same shared chrome and pane commands.
The shared component supports left, right and bottom inner resize edges.

## Explorer layout

The folder control at the left end of the session tab bar and the **Toggle explorer** command
in the Ctrl/Command+K palette open the workspace tree. There is no global explorer
shortcut: Ctrl+Shift+E is
reserved by Firefox and editable/terminal input keeps its keys. Quick open
remains Ctrl/Command+P, with its existing bounded search provider and shared viewer.

Desktop order is session list, workspace, session panel. A single-group workspace tab bar sits above the explorer and the active
Chat/file view. With split groups, the first group carries the explorer, panel
and shell toggles; every group has its own tab bar. See [session splits](session-tabs.md#split-groups). The explorer uses
the session list's background and a compact search row. The edge chevron, tab-bar
toggle, palette command, or dragging the divider to its left edge collapses it.
The folder toggle or palette brings it back. Open state and width are remembered
per person under `blerg-runner.explorer.<encoded-account-id>` in localStorage.

Default width is 20% of the viewport (at least 280 px); minimum grows from 220 px
to 8% of the viewport. The maximum is 40%, bounded by actual session-column space
after the session panel and a **320 px workbench minimum**. If the
minimum columns do not fit, the session panel uses its drawer without changing
the saved desktop preference. The workspace retains at least 320 px and the
session area stays contained. Saved widths are clamped on resize without overwriting
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

Below 900 px, **Explorer** in the session navigation bar opens a full-screen sheet.
The underlying session is inert, focus stays within the sheet, and refresh retains
its stable control. Escape, Back, or the edge chevron collapses the sheet and returns
focus to the folder toggle. Opening a file closes the sheet and navigates to its
preview; the Chat composer stays mounted. Phones retain the back stack and **kept** menu, with a full-width scrolling
tab row below the navigation tools and an **All tabs** menu when it overflows. The panel's Files tab keeps published
artifacts, versions, downloads and uploads. No explorer behavior changes runtime
release gates. See [session tabs](session-tabs.md) for preview behavior and the
former More actions destinations.

### Git status refusals

Repository badges retain the specific status failure instead of discarding it.
Current daemons send an allowlisted `status_reason` for identity changes,
metadata safety checks, private snapshot preparation, Git command failure,
unsupported status output, or a deadline. The server rejects arbitrary reason
strings. Paths, Git stderr and underlying errors remain private. Refresh retries
with the existing binding and safety checks; no refusal is treated as clean.
Older daemons without a reason show the existing safe fallback with update advice.
