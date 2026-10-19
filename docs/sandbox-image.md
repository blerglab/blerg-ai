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

## Coding in a sandboxed child

A local sandbox mounts the child's checkout at `/workspace`. For a linked Git worktree, it also
mounts the verified shared Git directory read-write at the path the checkout's `.git` file uses.
The child can create a local commit on its own branch; the parent sees that commit immediately.
This shares the repository's refs, objects, configuration and hooks with its other worktrees.
It is containment from the rest of the workstation, not isolation between branches of that repo.
Only same-owner metadata with a matching worktree backlink is accepted; world-writable,
foreign-owned or malformed metadata and external object alternates are refused. Custom layouts
outside a `.git` or bare `*.git` directory need a normal clone/worktree instead.

The checkout and shared Git metadata must be trusted same-user/group storage. Blerg validates
ownership, metadata identity and backlinks again immediately before launch, but pathname-based
Docker binds are not a boundary against a host actor who can race modifications to that storage.
Do not use an attacker-writable checkout or share its writable Git metadata with an untrusted user.

The daemon does not mount the host's Git credential files, SSH keys/agent or Git home configuration,
and does not forward Git hosting tokens. Repository-local configuration remains shared: do not put
push credentials in repository URLs, hooks or config. Children should return commit IDs and build/test
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
