# Your own session image

Sessions run in an image Blerg builds: Debian with git, Node.js 22 (at least 22.12), Go 1.26.9
(the version in `go.mod`), Python and build tools, the engine
CLIs (`claude`, `codex`, `hermes`), `tmux`, and a non-root `agent` user. Each install has its
own:

- the desktop **Local sandbox** runs `blerg-runner-sandbox:latest`, built from
  [`runner/sandbox/Dockerfile`](../runner/sandbox/Dockerfile);
- **cluster pods** run `blerg-runner-agent`, built from
  [`runner/Dockerfile.devcontainer`](../runner/Dockerfile.devcontainer), whose entry point is the
  runner's own pod binary (`blerg-runner-pod`) and which also carries the `blerg-runner` CLI the
  agent uses.

Your projects may need more than the defaults: a different language toolchain, a database
client, a browser. Extend the image your sessions use.

## Extend the image

Build your own image **from** Blerg's, so the engines, the user and the entry point stay as the
runner expects them. On the desktop:

```Dockerfile
FROM blerg-runner-sandbox:latest
USER root
RUN apt-get update && apt-get install -y --no-install-recommends postgresql-client \
    && rm -rf /var/lib/apt/lists/*
USER agent
```

For the cluster, the same with `FROM <registry>/blerg-runner-agent:<tag>`, the image `deploy.sh`
pushed. Then tell the runtime to use it, below. Keep `USER agent` last: the daemon runs the
engine as the image's user, and files it writes into your repository take that user's UID.

## Replace the image

You can also start from scratch. Inside the image the runner needs:

- the engine binaries on `PATH` under their usual names (`claude`, `codex`, `hermes`): the daemon
  checks for the chosen engine inside the image before every sandboxed Agent launch, and refuses
  if it is missing;
- `git`, `tmux` and a POSIX shell;
- a non-root user whose UID matches yours on the desktop install, or files the session writes into
  your repository come out owned by someone else. Blerg's own build takes `--build-arg UID=$(id -u)`
  for this;
- for a cluster image, `blerg-runner-pod` and `blerg-runner` copied from Blerg's image into
  `/usr/local/bin`, python3 for the CLI, a `/workspace` home, and Blerg's `ENTRYPOINT` kept.

## Point a runtime at it

**Desktop (Local sandbox).** The daemon looks for `blerg-runner-sandbox:latest` unless
`BLERG_RUNNER_SANDBOX_IMAGE` on the daemon process names something else: a tag you built, or an
image to pull from a registry you are logged in to. The variable goes in the daemon's service
unit, not your shell; [`install/desktop/DAEMON.md`](../install/desktop/DAEMON.md) says where. To
rebuild Blerg's own image after changing `runner/sandbox/`:

```
docker build --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest runner/sandbox
```

**Kubernetes (cluster runtime).** `BLERG_RUNNER_AGENT_IMAGE` in the `blerg-config` ConfigMap names
the image every session pod runs; `deploy.sh` sets it to the image it just built and pushed. Point
it at your own image in a registry the cluster's nodes can pull from, then restart the runner.
Setting it empty switches the cluster runtime off. Details and the preflight checks:
[`install/k8s/CLUSTER-RUNTIME.md`](../install/k8s/CLUSTER-RUNTIME.md).

## Engine versions

The engines inside the image are pinned: Claude Code and Codex in
[`runner/sandbox/engines/package.json`](../runner/sandbox/engines/package.json), Hermes in
[`runner/sandbox/install-hermes.sh`](../runner/sandbox/install-hermes.sh) (`HERMES_TAG`). Rebuilding
gives you the same versions until the pins change. To move to a newer engine, change the pin,
rebuild, and relaunch sessions; running sessions keep the image they started with.

## What a sandbox works in, and where its commits land

Every Local sandbox session works in a clone of its own, never in your checkout: a Terminal or
Agent session from the launch sheet, a board's card run, a cron and a sandboxed child alike. It
has no write access to any part of your repository's `.git` (refs, configuration, hooks, objects,
other worktrees), and none to your working tree.

**What the sandbox can touch.**

- **Writable:** its clone, mounted at `/workspace`; its own sandbox home (below); and the
  container's own filesystem.
- **Read-only:** your repository's objects directory (so the clone can read history), the
  messaging CLI, an always-on plugin snapshot when there is one, and your engines' configuration
  (below).
- **Not mounted at all:** your working tree, the rest of your `.git` (`config`, `hooks/`, refs,
  `packed-refs`, `worktrees/`), your other repositories, your `~/.claude`, `~/.claude.json`,
  `~/.codex` and `~/.hermes` directories themselves, your Git credentials, SSH keys and Git
  configuration, and everything else on the machine.

**The engines' logins and configuration: a sandbox home of its own.** A sandbox never gets your
engine directories. Settings, hooks, plugins or skills written there would run on your machine, as
you, the next time you start Claude Code or Codex outside the sandbox. Each sandboxed session gets
a home of its own instead, kept by the daemon in
`~/.blerg-runner-daemon/sandbox-homes/<session-id>` and removed when the session ends (a home a
crash left behind is removed when the daemon next starts). It holds only the engine the session
runs: a Claude session gets nothing of Codex or Hermes, and the other way round.

| Engine | In the container | What it is | Can the session change it? |
| --- | --- | --- | --- |
| Claude | `~/.claude` | the session's own directory: transcripts, todos, caches | yes, only its own |
| Claude | `~/.claude/.credentials.json` | your current access token; the refresh token is replaced by a placeholder, and your MCP servers' tokens are left out | its own copy only; the daemon replaces it every 15 seconds and never reads it |
| Claude | `~/.claude/settings.json`, `settings.local.json` | copies of yours keeping only behavior settings (permissions, model, plugins enabled, output style and the like); `env`, `apiKeyHelper`, AWS and other credential helpers are left out, and so are your hooks and status line (their commands can carry tokens): **your own Claude hooks do not run in a Local sandbox session** | no (read-only) |
| Claude | `~/.claude/CLAUDE.md`, `keybindings.json`, `agents/`, `commands/`, `hooks/`, `output-styles/`, `personas/`, `plugins/`, `skills/` | yours, read-only (an empty read-only directory when you have none) | no |
| Claude | `~/.claude.json` | only your account state (`oauthAccount`, `userID`, onboarding, `primaryApiKey` if you use one) and `/workspace`'s folder trust | no (read-only) |
| Codex | `~/.codex` | the session's own directory: sessions, logs, caches | yes, only its own |
| Codex | `~/.codex/auth.json` | your API key, or your current ChatGPT tokens with the refresh token replaced by a placeholder and the refresh time set to now (so Codex does not try to renew before the access token expires) | its own copy only; replaced every 15 seconds, never read |
| Codex | `~/.codex/config.toml` | a copy of yours keeping only model, provider (name and address, no headers), approval, sandbox and feature settings; MCP servers, plugins, projects, `notify` and shell environment are left out. A Codex session with MCP connections gets the same allowlisted settings in its private Codex home (plus the session's own MCP bridge and its `/workspace` project setting) | no (read-only) |
| Codex | `AGENTS.md`, `AGENTS.override.md`, `hooks.json` (when you have them), `hooks/`, `skills/`, `plugins/`, `rules/`, `prompts/`, `marketplaces/` | yours, read-only (empty read-only directories when you have none) | no |
| Hermes | `~/.hermes/config.yaml`, `~/.hermes/.env` | yours (they are Hermes's provider login), read-only; the rest of `~/.hermes` is the image's own Hermes install | no |

OpenClaw does not run in the sandbox. Where you have no `AGENTS.md`, `AGENTS.override.md`,
`hooks.json` or `keybindings.json`, a session can create one, but only in its own home.

**Signing in.** Nothing from a sandbox home is ever read back or copied to your machine. A session
gets only the sign-in that works now: it cannot renew it, and it cannot change yours. While it runs,
the daemon copies your current sign-in in every 15 seconds, so when Claude Code or Codex on your
machine refreshes it, the session gets the new one. If your sign-in has expired and nothing on your
machine has refreshed it, the session says "The Claude (or Codex) sign-in on this machine needs
refreshing": run `claude` or `codex` once on this machine, outside the sandbox, or sign in again
there. The daemon does not refresh a sign-in itself.

**The clone.** The daemon makes it with `git clone --shared` into its state directory
(`~/.blerg-runner-daemon/sandbox-clones/<session-id>`), on a new branch `wip/<session-id>` that
starts at your checkout's current commit; the clone holds no other branch. The clone borrows your
repository's objects (Git alternates), which is why that one directory is mounted read-only at its
own path; new objects go into the clone's own store. A folder that is a linked worktree is cloned
the same way. An empty folder that is not a repository yet (a **New folder** or a **No repository**
scratch folder) is made one first (`git init`, no commit), and the session's first commit is the
first on `wip/<session-id>`. If the clone cannot be made (a folder with content that is not a Git
repository, an empty folder inside another repository's working tree, a folder that is not the
top of its repository, a repository that borrows objects from another one, an existing
`wip/<session-id>`), the start is refused; your checkout is never mounted instead. As its last
check before Docker, the daemon refuses to mount anything but the session's own clone with the
daemon's record of it.

**Where the commits land: `wip/<session-id>` in your repository.** At the end of each turn
(before the turn's end is reported, so a parent told a child finished finds the commits), and when
the session stops, the daemon fetches that one branch from the clone
(`+refs/heads/wip/<session-id>:refs/heads/wip/<session-id>`, nothing else). A Terminal session's
turn end is when its screen goes from running to idle. Nothing is merged into your branch and
nothing is pushed: check out, merge or delete `wip/<session-id>` yourself. The session's checked-out
branch, your `main` and every other branch stay where they were. While the session runs, prefer
merging `wip/<session-id>` or looking at it from another worktree: if you have it checked out, the
daemon does not move it under your working tree, and brings the session's commits to
`wip/<session-id>.incoming` instead (the session's transcript says so once).

**Work left uncommitted when the session stops** is committed for it, inside its container, onto
`wip/<session-id>` (subject "Uncommitted work when the session stopped (committed by Blerg)",
with the clone's hooks switched off), and that commit comes back with the last fetch. Untracked
files the session hid through Git settings (`status.showUntrackedFiles`, `.git/info/exclude`,
`core.excludesFile`) are part of it, and the check that decides whether the clone can be removed
ignores those settings too. Files a `.gitignore` in the work tree excludes are not part of it (so
build output stays out). That `.gitignore` is the session's own: only work hidden by it can be
left out of the snapshot and removed with the clone, and that is the session's own choice. Work on another branch or a stash in the clone is not
moved: the clone is then kept (see below).

**How the fetch is kept safe.** The fetch runs on the host through the daemon's one hardened Git
runner, and the clone's `upload-pack` and `pack-objects` (which also run on the host, in the clone)
carry the same overrides on their own command line, since Git does not hand them on. Neither side
reads system or global configuration, and nothing the clone's configuration names runs: hooks,
`core.fsmonitor`, `core.sshCommand`, an external diff, submodules, an alternate-refs command. Only
the file transport is allowed, and the fetch runs under a memory limit, writes no commit-graph and
starts no maintenance.

What the host still parses from the clone: its `.git/config`, refs, `packed-refs`, `shallow`, pack
indexes and the objects themselves. The objects it receives are checked (`fsck`). Not parsed: the
clone's commit-graph, multi-pack index and bitmaps (switched off). Refused instead, before Git runs
and read as plain files: a `.git/info/attributes`, `.git/info/grafts` or `.git/objects/info/grafts`
file, a `.gitattributes` anywhere under `.git` (any case: the host's `pack-objects` reads them
there), a `.git/config.worktree`, and any `[include]` or `[includeIf]` section in the clone's
`.git/config`. Everything under `.git` counts toward a size cap (the objects toward their own). The session's transcript then says which one to remove, and the commits come back at
the next turn's end once it is gone.

The container is paused for the whole check-reset-fetch step (and always
unpaused after; a failed unpause is retried once and logged loudly), so the clone cannot be swapped
while the host reads it. If the container is not known at the end of a turn, the fetch is refused
rather than run against a clone that could be changing. The fetch reads the clone's `.git` only
(never its working folder), through `git upload-pack --strict`, and is refused if that `.git` is
missing or not a valid repository. Before fetching, the daemon walks the clone's `.git` and refuses
the fetch if anything under it is a symbolic link or not a plain file or directory (so a session
cannot point `objects/pack`, a loose-object directory or its ref at another repository), if the
object store is over a size cap, or if a nested or `http-alternates` chain is present; it then
rewrites `objects/info/alternates` from its own record, so the fetch reads objects only from the
clone and your repository. The whole turn-end sync is bounded (about 80 seconds, with each docker
call and Git step bounded too, and a cap on how many entries the `.git` may hold). A fetch that is
refused or fails is said in the session's transcript, and for a child in the parent's turn-end
note.

**The daemon never runs Git on the host in the clone** except that hardened fetch (and the
commands that create the clone, before anything ran in it). Whether the clone still holds work is
asked inside the container; the commit a filing names is read from the clone's `HEAD` as plain
files. Diffs (`blerg-runner diff`) and published files are made inside the container from the
clone, the files panel reads the clone's files (never its `.git`), and the commits a History chip
names are on `wip/<session-id>` in your repository from the end of the turn that made them.

**When the session stops or is discarded**, its branch is fetched one last time. A branch with no
commit of its own is then deleted from your repository; a branch with commits is always kept. The
clone is removed only when the check inside the container, just before it is removed, finds
nothing that is not on `wip/<session-id>` (no uncommitted change, stash, other branch, or a HEAD
elsewhere); otherwise, or if that check could not run, the clone is kept and the daemon log names
it. After a daemon restart nothing hosts a sandboxed Agent session again: the daemon finds its
container, commits its leftover work, removes the container and brings its branch back the same
way. A sandboxed Terminal session is reattached to the same clone. A running Terminal sandbox
that the daemon has no clone record for, or whose container mounts anything but that clone (one an
older daemon started with your folder mounted), is stopped when the updated daemon reattaches;
a session the daemon is running keeps running, and a missing recovery record (a failed write, a
changed repos root) never stops one, and a recovery record that names your
folder is not recovered; both say why in the session, once. Every docker call is bounded (the
sandbox's `docker run` uses `--pull=never`, so a missing image fails at once instead of pulling), and a container whose tmux does not answer is left alone for that
pass rather than holding it. If a container cannot be removed at the stop, its clone is kept and
not fetched (the fetch only runs unpaused once the container is gone).

`blerg-runner session result <id>` names the branch for every sandboxed session in a repository and
for a cluster session; for a host session or a session with no repository it is empty.

The daemon does not mount the host's Git credential files, SSH keys/agent or Git home configuration,
and does not forward Git hosting tokens. The clone's `origin` is the local folder of your
repository, which the sandbox cannot reach, so a sandboxed session cannot push. Children should return commit IDs and build/test
results; the parent reviews, merges and pushes only with the person's authorization. Do not copy
push credentials into a child to work around a failed push.

The default image includes Go 1.26.9, Node.js 22/npm, Python, Git, make and the C/C++ build tools.
Project dependencies still need to be installed and may need network access. Browser/system
libraries, databases, Docker-in-Docker and other language toolchains are not promised; extend the
image when your project needs them. Changing the Dockerfile does not change a running session:
build the image and start a new child. The same image override described above applies to children.

For a credential-free Docker regression test after building an image:

```sh
cd runner
BLERG_SANDBOX_CHILD_TEST=1 GOWORK=off go test ./internal/daemon -run '^TestSandboxChildWorktreeCommitAndBuild$'
```

The test uses a disposable repository and container, commits on the child branch, verifies that
the parent branch stays unchanged, and runs small dependency-free Go and Node tests. It does not
use an engine login or push a branch.

## Workspace v2 shared contract (0.5.0 P0)

`protocol.WorkspaceManifest` is immutable launch state, persisted by migration 076.
`workspace_kind` defaults to `repo` for existing and old-writer sessions;
`GetSessionWorkspace` returns a nil manifest for those sessions. Scalar fields keep
their single-repository meanings; never choose an arbitrary first member for them.
Folder persistence clears repo/git_url rather than retaining a first-member scalar.
Manifest runtime values are host/sandbox/cluster; persistence maps existing
session daemon/docker values to host/sandbox for binding validation. Member destination
is the authorized destination identity, inherited by children; each child's wip
branch is allocated separately, not supplied as a replacement destination.

Negotiate `WorkspaceCapabilities` at both hops and call `Require` before dispatch.
Absent or unknown versions refuse folder/multi-repo and shell. P0 adds schemas,
not runtime support advertisements. P1–P5 must wire enforcement before enabling
features. Public source/common-directory identities must be opaque, never private
paths. Private mounts and credentials stay in trusted runtime state. Validation
caps members at 20 and checks unique identities and disjoint safe paths; P1 still
must resolve canonical roots, authorize destinations and discover submodules.

`WorkspaceMemberRef` fences diff/commit/artifact operations by member and launch.
`WorkspaceMemberResult` carries per-member base/head/branch, pending, sync/push
errors and owner-authorized recovery locations; validate provenance before delivery.
`DefaultWorkspaceBudget` applies once per session. Folder children may choose a
parent-manifest subset through `ValidateWorkspaceChild`, retaining existing child
authorization and allocating each member's own wip/<child-id> branch. Shell grants
are never inherited. Unqualified project tools refuse folder sessions: member-aware
routes must verify member/project and generation against trusted manifest state;
explorer discovery never confers project authority.

`contracts/shellpolicy` defines `ShellPolicy`, `ShellGrant`, `HostShellConsent`
(with runner protocol aliases) so core and runtime share P5 control-plane schemas.
Grants bind audience, human owner/login, connection, launch/shell generation,
runtime and policy revision: maximum 30-second leases, renewed every 10 seconds.
Host grants require exact challenge-bound consent. P5 implements authenticated issuance,
validation, revocation, policy authority and supervisor containment; these types
alone grant no shell access. Unknown policy and peer capabilities fail closed.

## Local workspace lifecycle primitives (0.5.0 P2)

The daemon's `PrepareLocalWorkspace` accepts an authorized immutable manifest,
trusted canonical `WorkspaceSource` records and an enforced `WorkspaceStorage`
allocation. It clones every selected repository through the existing hardened
`--shared` preparation path. Loose source-folder files are never copied. A
private, versioned, authenticated manifest pins sources, common directories,
clone Git/object directories, bases and per-member destinations. The existing
single-session clone-root gate still rejects arbitrary descendants.

`DockerMountPlan` is private daemon data: mount its public root at `/workspace`,
mount only the recorded source object directories read-only, and apply its exact
owner/session/launch/context labels. Never forward this plan to an agent or browser.
After creating the runtime, call `BindRuntime` with the immutable Docker ID and
pinned Docker context. The returned root identity comes from inside that runtime;
it is separate from the host allocation identity and must be bound to the launch
before file/shell operations are enabled. P4 status workers must use
`WithRuntimeMember` to share the lifecycle gate. The explorer notice is **Only selected
repositories were copied.**

Call `Sync` at turn boundaries. It drains status work, reads member heads inside
the running container and skips freezing when there are no new commits. Otherwise
it freezes the whole container once, validates metadata, resets exact alternates
and copies members with the existing strict `.git` upload-pack path. It never
executes inside a paused container. The shell adapter receives **Syncing the
agent's commits…** during this operation. Each round rotates its first member and
shares aggregate metadata/object budgets. Member refusals retain that member's
work; shared-record corruption or an unproved freeze prevents all copying. A
failed thaw fences the workspace for recovery.

`Stop` fences admission through P0b's all-launch supervisor cleanup proof,
then stops and removes the exact immutable runtime. A fresh, memory-capped helper
snapshots leftover work without reading agent Git configuration. Clean, saved
members may be released independently; uncertain members retain recovery paths.
Durable preparation/removal/member/allocation intents reconcile process crashes.

`LocalWorkspaceService` connects Manager/AgentHost dispatch, child authorization,
turn-end sync, stop, recovery and results. Its selection, authenticated runtime
binding/CAS, current-incarnation, result persistence and P5 supervisor adapters
are mandatory; P6 connects the real package implementations. Runtime results must
use `SetWorkspaceRuntimeResults` through that authenticated adapter. The legacy
SessionEnded result handler cannot authorize a generation-bound report.
`WithBoundRuntimeMember` checks generation and current authority at entry and
delivery; the older selection-only member API refuses bound workspaces.
Capability support remains unadvertised pending integration/security signoff.

The default storage authority keeps a private persistent ext4 image in Docker's
Linux VM on both Linux and macOS. A trusted, labelled quota keeper mounts only
its own image; a second labelled local volume exposes that exact device's public
`tree` subdirectory. Daemon state contains authenticated allocation and release
journals, never an unbounded ordinary-directory fallback. Host source pins and
worker-namespace clone pins are separate. Recovery locations for VM allocations
are logical worker paths; resolve them through the authenticated private volume
journal rather than opening them as host paths. The public checkout need not exist as
a host filesystem path, and the host needs no mount privilege.

Kernel byte/inode ceilings combine with a mandatory per-write entry policy:
`link` and `linkat` return EXDEV in every workspace runtime and Git worker.
Directory names therefore cannot alias one inode to evade the entry ceiling.
The policy preserves Docker's default syscall restrictions; Git can use its
normal rename/copy fallback. The daemon checks the runtime's exact policy and
workers independently probe the kernel's hardlink denial before work.

Preparation, copy and settlement run in a Linux Docker container with one
512 MiB memory/swap budget, 64 PIDs and no network. Linux can mount its native
helper executable; macOS uses the matching Linux helper image, never a Mach-O
binary or host cgroup. The desktop installer builds/checks that image even when
the base sandbox image already exists. To build it manually from repository root:

```sh
docker build -f runner/Dockerfile.workspace-worker -t blerg-workspace-worker:latest .
```

Settlement seeds a fresh trusted index from the current session commit. It never
reads the agent's index/config. Tracked files remain tracked under newly added
ignore rules. Raw checkout bytes must match the snapshot and the actual fetched
source blobs before a saved-tree proof permits release. Ignored untracked files
remain in retained recovery storage; they are not force-added. Attribute
conversions that would change raw bytes also retain the checkout.

Runtime start tokens are journalled before generation allocation or Docker
creation. `WorkspaceRuntimeAuthority.Launch` must be idempotent for that token;
CAS intent is journalled before `Bind`. `Abort` must independently authorize and
fence a pending token (or an explicitly identified historical immutable runtime)
before acknowledging cleanup. Interrupted/unacknowledged starts can then return
to prepared state without discarding clones. `Recover` authenticates a new
connection to the same runtime generation and physical binding before replay.
These are P2 adapter requirements, not authority conferred by the token itself.

Finalization journals an immutable result payload and terminal message. A failed
result persistence keeps the private/agent recovery records and sends no terminal
event. `Results` acknowledges durable, idempotent replacement of the exact
runtime/member payload before terminal submission. Acknowledged results are not
resent during normal terminal retries. Crash/lost-ack replay can repeat the same
idempotent persistence or terminal submission; transport acceptance is not an
exactly-once wire-delivery promise. Current authority is checked again before
terminal submission. P6 connects the real authenticated adapters and their
server-side acknowledgements; capability advertisement remains disabled.

Run the credential-free P2 Docker tests with a locally built sandbox image:

```sh
cd runner
BLERG_P2_DOCKER_TEST=1 GOWORK=off go test ./internal/daemon -run '^TestP2Docker' -count=1
```

Tests use unique container labels and remove only their own containers. They use
fake trusted selection/quota/status/supervisor peers; they do not enable another
package's discovery, explorer or shell implementation.
### Prepared runtime roots and supervisor lifecycle (P0b)

Manifest membership and its launch generation remain immutable. `WorkspaceManifest.Root`
is legacy metadata, grants no file authority, and may be zero before preparation.
For host, sandbox and cluster, the trusted server calls `db.BeginWorkspaceRuntime`
with the expected prior generation before dispatch. It must authorize the session
and fence the old physical writer first. This compare-and-set increments the runtime
generation and clears the binding without changing membership or results.

The runtime pins its actual public directory and sends `WorkspaceRuntimePrepared`
(`workspace_runtime_prepared`) over its existing authenticated connection. The server
calls `BindWorkspaceRuntime` with the runtime reference from trusted connection
state, never authority supplied by the frame. Binding checks manifest digest/launch,
runtime generation and physical root; it is write-once per generation with exact
retries. Pending binding means unavailable. No new transport or capability is enabled.

Explorer request interfaces carry `WorkspaceRuntimeRef`, checked against current
trusted binding before dispatch and delivery in addition to viewer authorization
and descriptor checks. Replacement requires a new runtime generation even if inode
numbers are reused. `SetWorkspaceRuntimeResults` fences member reports against this
reference. The legacy results API refuses writes once a runtime generation exists.
Replacement preserves results; callers merge successful outcomes and retry failures.

`contracts/runtimelifecycle.Supervisor` (runner protocol aliases in `supervisor.go`)
is the shared in-process seam implemented by P5 and faked by P2/P3. It specifies
admission, phase notifications, shell/launch close and orphan recovery. Runtime
coordinators serialize admission/fencing with settlement under one session mutex;
cleanup failures/cancellation keep admission fenced and storage retained. Exact
launch/connection/runtime-generation proofs require empty containment, closed PTYs
and reaped processes. A shell-specific proof cannot prove launch-wide cleanup.
The four shells share session rate/resource budgets. This interface does not
implement containment, policy, grants or transport; those remain P5/runtime work.

### Cluster workspace runtime (P3)

Cluster workspace-v2 jobs mount only the project volume at `/workspace/projects`;
`/workspace` remains HOME. Members use their stable ID as the manifest-relative
path, so duplicate display names do not collide. The runtime guide lists each path
and its `wip/<session-id>` branch. All members must be prepared before agent spawn.

The trusted selection service supplies `SessionJobSpec.Workspace` plus private
`WorkspaceMembers` contexts. These are not browser/agent manifest fields. The
launcher compares the persisted immutable selection, checks the live owner, fetches
each exact provider credential, and prechecks every member before allocating
storage or creating a Job. There is no operator Git-token fallback. Clone access
does not grant push authority; read-only members report a push refusal. Private
launch JSON travels through a per-session Kubernetes Secret and is consumed/unset
before any engine starts. Tokens are absent from clone configuration, Job literals,
public manifests and durable workspace-context records.

Workspace claims request 11,520 MiB in one of two modes, decided per session at its
first launch and stored. **Bounded** (`BLERG_RUNNER_WORKSPACE_STORAGE_CLASS` set): the
class must provide a dedicated filesystem with an enforced byte ceiling no greater than
11.25 GiB and an enforced inode ceiling no greater than 700,000; the pod validates the
actual statfs limits and a separate mount device and refuses unbounded
shared-directory/local-path provisioners. Smaller enforced limits are allowed.
**Standard** (the setting empty): the claim uses `BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS`
or the cluster default, and the statfs check is skipped, so there is no physical bound
per workspace. In both modes the aggregate checkout/object/metadata budgets are checked
during preparation and settlement. Linux openat2/no-symlink/no-cross-mount support is
required; no weaker traversal is used.

Migration 079 stores immutable token-free contexts, owner and PVC name/UID, plus
prepared member identities. Resume refuses missing/replaced volumes or switched
Git/object directories. It fences the old Job and every old Pod before dispatch,
revokes old runtime authority before advancing generation, and binds each socket
to one runtime generation. Monotonic report revisions and result updates commit
atomically, so reordered turn/reconnect reports cannot overwrite newer successes.
Existing result successes survive replacement; failed pushes remain pending and
are retried. Settlement snapshots bounded regular object
files into an isolated bare directory, ignoring live config/index/includes/remotes,
and pushes explicit verified heads to authorized destinations under the existing
force-push policy. Git workers share a rotating single-worker 80-second round and
a 512 MiB aggregate memory ceiling. Workspace-v2 currently enforces that ceiling
on the entire runner container (including the engine and all Git descendants),
while preserving any tighter existing limit. P6 must provide a dedicated enforced
Git cgroup before allowing a larger engine container budget. Integer byte, IEC
and decimal K/M/G/T resource quantities are supported; unknown quantities refuse
the workspace launch. Partial preparation removes only proven unused clones;
uncertain allocations and local work remain retained. Volumes are conservatively
retained for recovery rather than garbage-collected with Jobs; owner-authorized
recovery/reclamation must use the persisted owner and exact claim UID.

`ClusterWorkspaceChild` validates an already-authorized parent-manifest subset and
returns token-free contexts for a separately credentialed child Job. P6 wires the
selection/approval/CLI producer to this runtime entry point; existing delegation
authorization remains mandatory. No shell grants are inherited. P5's shared
supervisor can be connected before preparation, with admission serialized against
stop and exact launch-wide cleanup proofs required. Shell capability remains absent
until P5 supplies its full implementation. No Kubernetes exec permission is added.

### Integrated desktop completion

The production daemon constructs the workspace service and its authenticated
control adapter. Local runtime generation allocation, binding, abort, recovery,
Current and result acknowledgments use the enrolled desktop connection and
persisted owner/connection fences. Migration 083 stores idempotency tokens and
final completion digests. Exact results, terminal outcome and receipt commit
atomically; an identical final transaction can reconcile after reconnect/end.
Socket submission never acknowledges durable completion. Terminal notifications
preserve the committed result and original end timestamp.

Workspace v2 remains disabled until the real explorer/supervisor lifecycle and
release gates pass. Missing lifecycle registration refuses cleanup. See
[workspace integration](workspace-integration.md) for explicit outstanding work.

### Production workspace explorer helper

The local sandbox image packages the statically built, root-owned executable
`/usr/local/bin/blerg-workspace-explorer`. Rebuild the image after upgrading the
daemon; an older image without the helper returns workspace unavailable. Build
from the repository root, so the helper and shared contracts are included:

```sh
docker build -f runner/sandbox/Dockerfile -t blerg-runner-sandbox:latest .
```

The daemon starts the helper with fixed argv over private Docker exec pipes,
using the launcher's pinned Docker context and immutable container ID. List,
search, status and viewer reads execute inside the container. Legacy single-repo
sessions use the same reader for relative references, `/workspace/...` and the
recorded clone-path alias; the original checkout is never a viewer fallback.
The legacy wire contract retains its zero runtime reference. Workspace-v2 uses
the authenticated prepared binding and checks authority before and after reads.
Manager routes agent-session requests to AgentHost's registered reader.

Agent engines remain non-root, with no effective capabilities and
`no-new-privileges`. The container permits `DAC_OVERRIDE` for the trusted root
helper to read user-owned mode-0700 workspace directories. The helper runs with
an empty environment except a fixed PATH and temporary directory, and creates
its own mode-0700 temporary directory outside the public tree. A separate
256 MiB/200,000-inode tmpfs at `/run/blerg-explorer`, owned by root with mode
0700 under the root-owned `/run` namespace, bounds these snapshots. Agent-owned
`/tmp` remains separate from the trusted helper snapshot namespace. Agent processes
cannot read or modify that directory or the helper's private descriptors. The
helper retains the descriptor traversal/protected-name policy; extra filesystem
read permission never expands its public root.

The reader shares the session lifecycle mutex with sync and stop. Admission
refuses while that mutex is held, so reader cancellation/join cannot deadlock
freeze. Navigation cancellation closes private input, drains output and waits
for helper exit before releasing reader ownership. Restart requires the same
root, member, Git and object identities; it cannot adopt replacement directories.
Unbind/stop closes registered descriptors and helpers. Unproven helper cleanup
fences further reads and sync, retaining work for recovery. Reconnect retains
pinned runtime readers and reauthorizes the workspace authority; a complete
process-crash/platform acceptance campaign remains a release requirement.

Credential-free explorer/image acceptance (with a unique local test tag):

```sh
docker build -f runner/sandbox/Dockerfile -t blerg-sandbox-explorer-test .
BLERG_RUNNER_SANDBOX_IMAGE=blerg-sandbox-explorer-test \
BLERG_EXPLORER_DOCKER_TEST=1 GOWORK=off \
go -C runner test -p 1 -timeout 45m ./internal/daemon \
  -run '^TestWorkspaceExplorerProductionDockerImage$' -count=1 -v
```

The test creates uniquely labelled containers and removes only its own immutable
IDs after checking ownership. It verifies actual list/search/status/viewer replies,
helper ownership/mode, non-root engine capabilities and private temporary storage.
Workspace and owner shells are enabled for verified Linux/WSL hosts, local
sandboxes and cluster pods. Native macOS host remains disabled (#181); the full
failure and measurement campaign is deferred to #180. See the [runtime policy](workspace-integration.md).
