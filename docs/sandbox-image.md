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

- **Writable:** its clone, mounted at `/workspace`; the engine logins mounted for the engine
  (`~/.claude`, `~/.claude.json`, `~/.codex`, Hermes' two files, see
  [`DAEMON.md`](../install/desktop/DAEMON.md#local-sandbox-runtime)); and the container's own
  filesystem.
- **Read-only:** your repository's objects directory (so the clone can read history), the
  messaging CLI, and an always-on plugin snapshot when there is one.
- **Not mounted at all:** your working tree, the rest of your `.git` (`config`, `hooks/`, refs,
  `packed-refs`, `worktrees/`), your other repositories, your Git credentials, SSH keys and Git
  configuration, and everything else on the machine.

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

**How the fetch is kept safe.** The fetch runs on the host with hooks, `core.fsmonitor`,
`core.sshCommand` and submodules switched off, without system or global configuration, and checks
the objects it receives, so the clone's own configuration and hooks (which the session controls)
do not run on the host. The container is paused for the whole check-reset-fetch step (and always
unpaused after; a failed unpause is retried once and logged loudly), so the clone cannot be swapped
while the host reads it. If the container is not known at the end of a turn, the fetch is refused
rather than run against a clone that could be changing. The fetch reads the clone's `.git` only
(never its working folder), through `git-upload-pack --strict`, and is refused if that `.git` is
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
