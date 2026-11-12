# Blerg on your desktop

Get a session's recorded branch with `blerg-runner session result <id>` and use
its `branch` value wherever `<work-branch>` appears below. For a child, pass the
child's ID. Branch names stay fixed after a rename; never derive them from IDs
or titles. Workspace results also report each member's branch.

One developer, one machine, Docker. The stack (core, board, runner, Postgres) runs in Docker
Compose; a small daemon runs on your machine, as you, and launches workspaces in a sandbox
container. A team sharing one Blerg belongs on the [Kubernetes install](../k8s/README.md).

## Requirements

- Linux, macOS, or Windows with WSL2. On WSL2, systemd must be enabled: put `[boot]` and
  `systemd=true` on two lines in `/etc/wsl.conf`, then run `wsl --shutdown` from Windows and
  reopen the distribution.
- Docker with Compose v2 (Docker Desktop with WSL integration turned on for your distro, or the
  docker engine on Linux with the `docker-compose-plugin` package from Docker's own apt repository;
  `blerg-up.sh` prints the exact steps if `docker compose` is missing), plus `git`, `curl` and
  `openssl`, and Python 3 (for desktop enrollment). On Debian/Ubuntu, install Python with
  `sudo apt install python3`. The daemon is built with your host Go 1.26 or newer if you have it, and inside a
  container if you don't.
- **Claude Code or Codex installed and logged in before your first session.** The installer does
  not do this for you. Run `claude` (or `codex`) once and sign in, or run `claude setup-token` and
  export the result as `CLAUDE_CODE_OAUTH_TOKEN` before `./blerg-up.sh`. Sessions use that login
  and therefore your subscription or budget.
- On Linux your user must be in the `docker` group. **Membership of the `docker` group is
  equivalent to root on that machine**: a sandbox session can start any container with any mount.
  Add only accounts you would trust with root.
- Disk: the sandbox image is roughly 6–7 GB and takes several minutes to build.

## Install

```
cd install/desktop
./blerg-up.sh
```

What that does, once: generates `install/desktop/.env` with random secrets, builds the three
service images, builds the local-sandbox workspace image (`blerg-runner-sandbox:latest`, roughly
6–7 GB; this step takes several minutes, longer on a slow connection), and prepares the runner
daemon. Once enrolled to your account, it runs as a background service (systemd on Linux,
launchd on macOS). The installer prints the next steps and opens
`http://localhost:8081/setup` on a fresh install (`http://localhost:8081` once you have an account).
Run it in a terminal so it can ask where your repositories live. For an unattended install,
set `BLERG_RUNNER_REPOS_ROOT` in `.env` first.

On a fresh install, desktop enrollment needs the account you create next. The stack is ready,
but the installer exits with code 7 until you finish account setup and run
`./daemon/install.sh install` again. Exit 0 means installation completed; any other nonzero
exit needs the repair shown in the output.

## First login

`blerg-core` issues the login every component accepts (board and runner have no passwords of
their own). A fresh install has no account yet: `./blerg-up.sh` opens
`http://localhost:8081/setup`, the **Create your account** page, where you choose your username
(lowercase letters, digits, `.`, `_` and `-`) and a password of 12–72 characters. That account is
the administrator, and you are signed in straight away. There is no password to look for in a
log. You can change the username later under **Settings → Account**.

The page needs no setup code because the desktop stack publishes core on `127.0.0.1` only
(`BLERG_SETUP_ALLOW_LOCAL` in `docker-compose.yml`). That also means that **until setup is done,
any user, process or container on this machine can claim the admin account** (including a
host-network container, and on Docker Desktop any container through `host.docker.internal`), so
finish setup right after installing.

Until then, do not put a relay in front of core: anything that forwards to `localhost:8081`
from this machine (nginx's default `proxy_pass`, an `ssh -L`/`-R` tunnel, socat, a tunnel
service) lets anyone the relay reaches create the admin without a code, and so does publishing
core's port any way other than `BLERG_BIND_ADDR` (a compose override, an edited `ports:` line).
A proxy that adds `Forwarded` or `X-Forwarded-*` headers is refused, but many add none. If you
need a relay before setup, set `BLERG_SETUP_ALLOW_LOCAL=false` in `.env` and re-run
`./blerg-up.sh`: then `/setup` always needs a setup link, as on a cluster.

If you set `BLERG_BIND_ADDR` to publish core beyond this machine, the page asks for a setup link: `./blerg-up.sh` prints a fresh one when run in a terminal, or run
`docker compose exec blerg-core /blerg-core setup-link`. Once the first account exists,
`/setup` is closed for good; forgot the password later? `./blerg-up.sh --reset-admin` prints a
one-time one (or `docker compose exec blerg-core /blerg-core users set-password --subject
<username>`).

Log in at exactly `http://localhost:8081`. The desktop stack turns the cookie `Secure` flag off for
that origin so every browser (Safari included) works; `127.0.0.1` or a LAN IP will not.

## First workspace

Open **Runner** (`http://localhost:8083`) → **+ New** → pick a repo under your repos root, or type
any `owner/name` or paste a repository URL (a public one like `torvalds/linux`, or, in the Local
sandbox, a private one your GitHub/GitLab token in Settings can reach; it is cloned into your
repos root first, see [DAEMON.md](DAEMON.md#cloning-a-repository-you-name)) → Launch.

The button reads **Launch Workspace** for a repo already in your repos root, **Clone & Launch →**
when the repo has to be cloned first, and **Create & Launch →** when you are starting a new folder
(or, with Cluster pod selected, a **New repository** the runner creates on GitHub or GitLab with
your token, see [CLUSTER-RUNTIME.md](../k8s/CLUSTER-RUNTIME.md#new-repository)).

The Run column shows every choice at once; on a desktop install it defaults to an **Agent**
workspace in the **Local sandbox**, a container on your daemon. Agent workspaces stream their work
into the chat. Workspaces the Board starts (a card, the board chat,
**Run board**) are started by the board through the runner's API and get the same default: an agent-kind
workspace in the Local sandbox when your daemon has the sandbox image, on the host only when it
doesn't, or when you opt out (below).

## Before the Board can start workspaces

**Run this card works out of the box on a single-user install.** A board starts workspaces as the
person whose automation token it holds, and the board gets that token for you: a new board has
**Run this board's cards as me** checked when you create it, and a board without a token offers
**Run as me** in its settings panel (the **model** chip in the header → **Automation**) and next
to **Run this card**. One click mints a `run-sessions` agent token for you in core and saves it on
the board; nothing is copied or pasted, and the token never reaches your browser. It shows up in
core **Settings → Agent tokens** as "Board <name> automation", where you can revoke it like any
other. Pick the engine (Claude, Codex or Hermes) in the same panel. To use a token of a service
account instead, choose **Use a different token…** and paste one with preset `run-sessions`.

Every workspace that board starts then runs as you: on a cluster install on the engine credential
you connected in Settings (for Hermes, your own endpoint), on the desktop on your daemon's engine
login. The token expires (90 days by default; the panel says when, and offers **Run as me**
again). Anyone who can write cards
on that board can spend your credential and could get an agent to reveal it, so only connect a
token you're comfortable with every card-writer effectively having. Details:
[`board/docs/CONFIG.md`](../../board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

To disable new **Run as me** tokens entirely, set `BLERG_CORE_BOARD_MINT_DISABLED=true` in the desktop `.env` and rerun `./blerg-up.sh`. Existing tokens can still be revoked; this setting does not revoke them.

## What runs as you

Every session, sandboxed or not, uses the engine login on your machine (`claude`, `codex`) and
therefore your subscription or budget. The sandbox mounts your `~/.claude` and `~/.codex`
read-write so the engine can use them, so it does NOT protect those credentials or your host
Claude settings. A repo you launch a session in is code the agent will run: don't point it at
something you wouldn't run yourself.

Workspaces from the launch sheet default to the sandbox, so the
agent sees `/workspace` (a clone of its own of the repo: never your checkout or its `.git`; its
commits land on `<work-branch>` in your repo) and your mounted logins and nothing else of the
machine. **This machine,
unsandboxed** is the third card in its Run column: it runs on the host as you with full filesystem
access (and, for an Agent workspace, no permission prompts), it is never a default, and Launch stays
disabled until you tick the acknowledgement.

Workspaces the board starts through the runner's API get the same default: the sandbox when the daemon has its image, where the agent reaches the board and runner
over the `blerg-sandbox` network but has no git credentials (it can commit, not push). Opt out
onto the host with `runtime: daemon`; a board runs on a host only where its owner chose it in Where it runs. A v1
start never *asks* to bypass permission prompts, but an Agent workspace runs non-interactively and
its engine bypasses them regardless, on either runtime.

**Usage meters.** The sidebar shows how much of your Claude and Codex limits the account on this
machine has used. For Claude the daemon reads the same account usage endpoint the CLI's own usage
view reads (`api.anthropic.com/api/oauth/usage`), with the CLI's sign-in from
`~/.claude/.credentials.json`: it only reads that file, takes the access token (never the refresh
token), never refreshes or rewrites it, and sends the token nowhere but Anthropic. Only the
percentages and reset times reach Blerg. A work or enterprise account has a monthly spend limit
instead of the five-hour and weekly windows: its row shows "month" with the share used (never the
amount, the limit or the currency). The endpoint and that file are the CLI's internals, not a
documented API: they may change, and the meter then goes empty (the daemon logs it once; nothing
else breaks). On macOS the CLI keeps its login in the Keychain, not in that file, so there is no
Claude meter there (the daemon says so once in its log); a daemon started with
`CLAUDE_CODE_OAUTH_TOKEN` may run sessions on a different account from the one the meter reads. To
turn it off, add `BLERG_RUNNER_CLAUDE_USAGE=0` to
`.env` and run `./daemon/install.sh install`; Claude then shows only a "capped" row when it refuses
a call for its limit.

**Stop everything:** `systemctl --user stop blerg-runner-daemon` (macOS:
`launchctl bootout gui/$(id -u)/dev.blerg.runner-daemon`), then
`docker rm -f $(docker ps -q -f name=blerg-sandbox-)` to remove running Local sandbox
containers. This stops their engine processes and any tools still running inside them.

## Day to day

- **Stop:** `docker compose down` (from `install/desktop`). The daemon keeps running and
  reconnects on its own; `./daemon/install.sh uninstall` removes it.
- **WSL2 after Windows restart:** setup offers an optional Windows logon task to start and
  keep the distro running at sign-in, including on battery (default No; `--yes` does not opt in).
  Enable it with `./daemon/install.sh wsl-startup enable`; inspect or remove it with
  `wsl-startup status` / `wsl-startup disable`. The enabled daemon service and user lingering
  handle distro boot separately. See [WSL startup prerequisites and diagnostics](DAEMON.md#wsl2-return-after-windows-restart).
- **Start again:** `./blerg-up.sh` (rebuilds by default so a `git pull` takes effect; `--no-build`
  to skip, `--no-open` to keep the browser closed).
- **Upgrade:** `./blerg-up.sh --upgrade` (or `./blerg-up.sh --upgrade vX.Y.Z`). This backs up the existing install, selects a published stable release, and rebuilds the stack,
  database migrations run automatically, and it reinstalls the daemon too. Stop live sessions
  first; the upgrade refuses to interrupt them. On a large database the runner's first start after an upgrade can take a while:
  migrations 041, 042 and 049 build indexes on its event table, and the daemon's events wait until
  they are done. `./daemon/install.sh install` reinstalls just the daemon, for example after
  `blerg-up.sh --no-daemon`. The daemon matters on every upgrade: much of what a session shows comes from it (the
  working line's thinking and retries, resumed subagents, picking work up after a restart and
  saying why it restarted, Codex agent sessions, resuming after a usage limit, the usage meters),
  and a session left with a revoked messaging token is given a new one only when both the stack
  and the daemon are updated;
  `docker build -f ../../runner/sandbox/Dockerfile --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest ../..`
  rebuilds the sandbox image (keep the `UID` argument, or files the sandbox writes into your repos
  may end up owned by the wrong user). The engines inside it are pinned, so a rebuild only changes
  them when the pins changed; [`docs/sandbox-image.md`](../../docs/sandbox-image.md) covers
  changing the image and moving to a newer engine.
- **Backup:** `docker compose exec -T postgres pg_dump -U blerg -d blerg_core > core.sql` (and
  `blerg_board`, `blerg_runner`), plus a copy of `install/desktop/.env`. **Losing
  `BLERG_CORE_LOCAL_KEY` loses every credential stored on the Settings page.** Full procedure for
  both installs: [`docs/backup-and-restore.md`](../../docs/backup-and-restore.md).
- **Ports taken?** Edit `BLERG_PORT_*` in `.env` and re-run `./blerg-up.sh`.
- **Forgot the admin password?** `./blerg-up.sh --reset-admin` prints a new one-time password
  (every session is signed out).
- **Add a teammate:**
  `docker compose run --rm --no-deps -T --entrypoint /blerg-core -e "DATABASE_URL=postgres://blerg:blerg@postgres:5432/blerg_core?sslmode=disable" blerg-core users create --subject alice --role member`,
  then hand them the printed one-time password; they'll be asked to change it. One core per
  developer machine is the supported desktop shape; a shared core belongs on the
  [Kubernetes install](../k8s/README.md).
- **Run a service from source**, or the frontends in dev mode with hot reload:
  [Running from source](../../CONTRIBUTING.md#running-from-source).

Managing the daemon, other platforms, engines and `--no-daemon`: [`DAEMON.md`](DAEMON.md).

## Uninstall

From `install/desktop`, `./uninstall.sh` does the steps below for you (`--purge` also removes the
daemon's state folder, this stack's `.env` and the sandbox image; `--yes` skips the question). By
hand: first *Stop everything* (above), then `docker compose down -v --rmi local`
(the containers, the database volume and the three `desktop-blerg-*` images),
`./daemon/install.sh uninstall` (the service, and the `~/.local/bin/blerg-runner` and
`managing-tickets`, `coordinating` and `test-driven-development` skill links under `~/.claude`, `~/.codex`, `~/.agents` and `~/.hermes` that the daemon made), `docker rmi blerg-runner-sandbox:latest`
and `rm -rf ../../bin`. These are left for you to remove by hand, on purpose:

- `<repos root>/.blerg-runner/` and `~/.blerg-runner-daemon/`: session recovery records (they
  contain session tokens, which stop working once the database volume is gone) and the daemon's
  saved repos-folder setting. `install.sh uninstall` never deletes anything under your repos root,
  and it keeps the daemon's settings so that a reinstall comes back pointed at the same folder.
- `install/desktop/.env`: it holds `BLERG_CORE_LOCAL_KEY`, the stack's secrets and, if you gave
  one, your Claude OAuth token. Delete it once you no longer need a backup; it is the only copy of
  that key.
- The base images the builds pulled (`postgres:16`, `golang:1.26.9`, `golang:1.26.9-bookworm`,
  `node:22-alpine`, `debian:bookworm-slim`, `gcr.io/distroless/static:nonroot`) and Docker's build
  cache (`docker builder prune`). Other projects may use them.
- `~/.claude`: if it did not exist before Blerg and you don't use Claude Code, it can go;
  otherwise leave it, because it holds your Claude login and settings. If you opted into
  `BLERG_RUNNER_PROVISION_CLAUDE_MD`, remove the block between the `blerg-runner-messaging`
  markers in `~/.claude/CLAUDE.md`.
- Repositories cloned into your repos root: they are yours and stay.

## One-step desktop upgrade

From this checkout's `install/desktop` directory:

```sh
./blerg-up.sh --upgrade            # newest published stable release
./blerg-up.sh --upgrade vX.Y.Z     # a specific published stable release
```

Finish or stop all live sessions first, including idle sessions. Keep the terminal
open until the command finishes. Do not start an upgrade from an agent session on
this install: restarting its daemon would interrupt the worker. The web release
notice still links to release information; starting an upgrade from the UI is not
implemented yet. Kubernetes remains on its documented deploy command.

The command requires the official public repository as `origin`, a clean checkout
(including staged and untracked files), Git, and Go 1.26 or newer or Docker. It
accepts only published, non-draft stable releases; candidates and downgrades refuse.
It fetches into isolated refs without replacing local tags, keeps the original
branch pointer, and installs the resolved commit in detached HEAD. Do not combine
`--upgrade` with other installer flags, including `--yes` or `--no-daemon`.
Installations predating this command must first update their checkout to a release
that includes it, using the previous manual procedure.

Before changing the checkout or running migrations, the worker stops the three
services and daemon, dumps all three databases, and copies `.env` plus available
daemon settings and personal desktop credentials. Backups and the private
`operation.json` live under `~/.blerg-upgrades/<install-id>/`, outside the checkout;
the command prints the recovery-record location. A `complete.json` file written
last identifies a complete backup. Backup failure restarts the old services and
refuses the upgrade. Backups are retained after success. Runner data volumes and
retained workspaces are preserved; keep your usual backups of those files too.

The selected release's installer rebuilds the stack, applies migrations on startup,
and reinstalls the daemon automatically. Success requires service version checks,
the daemon's exact build commit, and a new accepted daemon connection. Installer
output is withheld because it may contain credentials; progress reports phases.

If installation or checking fails, inspect `operation.json` for the old commit,
branch, selected commit and backup location. A killed worker leaves its lock and
phase for recovery. Do not remove that lock until you have confirmed the worker
is gone and completed recovery. Never restart old binaries against migrated
databases. Follow [backup and restore](../../docs/backup-and-restore.md), restoring
all three dumps with the matching `env`, daemon settings and any saved
`desktop-credential.json`, then reinstall the recorded old commit. Restore discards
writes made after the backup. Preserve the recovery record before clearing it for
a new attempt. There is no automatic rollback or automatic retry of an uncertain
installation.

The Linux/macOS previous-release upgrade and restore drills are still required
before release; fake-service tests do not prove a real backup can be restored.

### Upgrade failure records

Upgrades keep `operation.json`, a private install outcome log, and a migration
boundary marker in the private upgrade state directory. Installer stdout and
stderr are redacted in full because they can contain setup credentials. The log
records whether installation completed or failed; it is not a full diagnostic
transcript. SIGINT, SIGTERM, and SIGHUP cancel the worker through its recovery path.

Before migrations begin, a failure restores the old Git checkout and restarts
its existing service containers and daemon. Once the installer marks that
migrations may have begun, automatic restart of old binaries is refused: use the
matching database/settings backup and the restore runbook. An interrupted
operation's marker also records this boundary even if `operation.json` was not
updated before exit. Installers without migration tracking are refused.

Each dump must have PostgreSQL's completion trailer. Successful upgrades keep
the current complete backup plus two complete predecessors. Incomplete backups
and backups from an unresolved recovery operation are not automatically pruned.
Remove incomplete backups manually after inspecting recovery state.

### Scoped MCP internal transport

Remote MCP sign-in control-plane requests carrying the shared internal key require
HTTPS, except for literal loopback addresses, `localhost`, qualified Kubernetes
Service names (`service.namespace.svc` or `service.namespace.svc.cluster.local`),
and the exact built-in Docker service aliases: `blerg-core` for gateway-to-Core
requests and `blerg-runner` for Core-to-Runner authority requests. Arbitrary short
names, public plaintext hosts, user information, query strings and fragments are
refused before the key is sent; refusal logs contain a classified cause.
Existing account MCP clients keep their current operator configuration behavior.
Production session authority and lifecycle wiring remain disabled until the
BLG-145 lifecycle increment passes its security review.

Scoped MCP internal clients always bypass environment and configured standard HTTP
proxies, including `HTTP_PROXY` and `HTTPS_PROXY`; credentials never rely on
`NO_PROXY`. Kubernetes Service suffixes permit plaintext only when the operator
sets `BLERG_MCP_CLUSTER_DEPLOYMENT=true` (default false). The Kubernetes ConfigMap
and Core/Runner deployments set and consume this flag; desktop installations
leave it unset and use their exact Docker aliases or loopback. Authority wiring
must explicitly pass this deployment flag to the Core authority client.
