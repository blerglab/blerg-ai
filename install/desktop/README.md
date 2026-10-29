# Blerg on your desktop

One developer, one machine, Docker. The stack (core, board, runner, Postgres) runs in Docker
Compose; a small daemon runs on your machine, as you, and launches sessions in a sandbox
container. A team sharing one Blerg belongs on the [Kubernetes install](../k8s/README.md).

## Requirements

- Linux, macOS, or Windows with WSL2. On WSL2, systemd must be enabled: put `[boot]` and
  `systemd=true` on two lines in `/etc/wsl.conf`, then run `wsl --shutdown` from Windows and
  reopen the distribution.
- Docker with Compose v2 (Docker Desktop with WSL integration turned on for your distro, or the
  docker engine on Linux with the `docker-compose-plugin` package from Docker's own apt repository;
  `blerg-up.sh` prints the exact steps if `docker compose` is missing), plus `git`, `curl` and
  `openssl`. The daemon is built with your host Go 1.26 or newer if you have it, and inside a
  container if you don't. Unsandboxed terminal sessions use the host's `tmux`.
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
service images, builds the local-sandbox session image (`blerg-runner-sandbox:latest`, roughly
6–7 GB; this step takes several minutes, longer on a slow connection), installs the runner daemon
as a background service (systemd on Linux, launchd on macOS), then prints the next steps and opens
`http://localhost:8081/setup` on a fresh install (`http://localhost:8081` once you have an account).
It exits back to your shell when everything's up; that is success.

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

## First session

Open **Runner** (`http://localhost:8083`) → **+ New** → pick a repo under your repos root, or type
any `owner/name` or paste a repository URL (a public one like `torvalds/linux`, or, in the Local
sandbox, a private one your GitHub/GitLab token in Settings can reach; it is cloned into your
repos root first, see [DAEMON.md](DAEMON.md#cloning-a-repository-you-name)) → Launch.

The button reads **Launch Session** for a repo already in your repos root, **Clone & Launch →**
when the repo has to be cloned first, and **Create & Launch →** when you are starting a new folder
(or, with Cluster pod selected, a **New repository** the runner creates on GitHub or GitLab with
your token, see [CLUSTER-RUNTIME.md](../k8s/CLUSTER-RUNTIME.md#new-repository)).

The Run column shows every choice at once; on a desktop install it defaults to an **Agent**
session in the **Local sandbox**, a container on your daemon. Pick **Terminal** instead for a tmux
session you can watch and type in; that one keeps permission prompts ON, so Claude will ask you to
trust the folder (press `y`) the first time. Sessions the Board starts (a card, the board chat,
**Run board**) are started by the board through the runner's API and get the same default: an agent-kind
session in the Local sandbox when your daemon has the sandbox image, on the host only when it
doesn't, or when you opt out (below).

## Before the Board can start sessions

**Run this card works out of the box on a single-user install.** A board starts sessions as the
person whose automation token it holds, and the board gets that token for you: a new board has
**Run this board's cards as me** checked when you create it, and a board without a token offers
**Run as me** in its settings panel (the **model** chip in the header → **Automation**) and next
to **Run this card**. One click mints a `run-sessions` agent token for you in core and saves it on
the board; nothing is copied or pasted, and the token never reaches your browser. It shows up in
core **Settings → Agent tokens** as "Board <name> automation", where you can revoke it like any
other. Pick the engine (Claude, Codex or Hermes) in the same panel. To use a token of a service
account instead, choose **Use a different token…** and paste one with preset `run-sessions`.

Every session that board starts then runs as you: on a cluster install on the engine credential
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

From the launch sheet both session kinds, Terminal and Agent, default to the sandbox, so the
agent sees `/workspace` (a clone of its own of the repo: never your checkout or its `.git`; its
commits land on `wip/<session-id>` in your repo) and your mounted logins and nothing else of the
machine. **This machine,
unsandboxed** is the third card in its Run column: it runs on the host as you with full filesystem
access (and, for an Agent session, no permission prompts), it is never a default, and Launch stays
disabled until you tick the acknowledgement.

Sessions the board starts through the runner's API get the same default: the sandbox when the daemon has its image, where the agent reaches the board and runner
over the `blerg-sandbox` network but has no git credentials (it can commit, not push). Opt out
onto the host with `runtime: daemon`; a board runs on a host only where its owner chose it in Where it runs. A v1
start never *asks* to bypass permission prompts, but an Agent session runs non-interactively and
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
`tmux ls -F '#{session_name}' 2>/dev/null | grep '^blerg-' | while read -r s; do tmux kill-session -t "=$s"; done`
and `docker rm -f $(docker ps -q -f name=blerg-sandbox-)`. The tmux line ends only the daemon's
own `blerg-<id>` sessions on this machine and leaves your other tmux sessions running (one of
yours whose name starts with `blerg-` would go too); a Local sandbox session's tmux lives in its
container and ends with the `docker rm`.

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
- **Upgrade:** `git pull && ./blerg-up.sh`; database migrations run automatically. On a large
  database the runner's first start after an upgrade can take a while: migrations 041, 042 and 049
  build indexes on its event table, and the daemon's events wait until they are done.
  `./daemon/install.sh install` rebuilds the daemon (if agent sessions have work running that the
  restart would cut off, it lists them and asks first; `--yes`, on it or on `blerg-up.sh`, skips
  the question). Do it on every upgrade: much of what a session shows comes from the daemon (the
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
`~/.claude/skills/managing-tickets` links the daemon made), `docker rmi blerg-runner-sandbox:latest`
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

## Workspace 0.5.0 upgrade status

Update core, runner and the desktop daemon together and retain database backups.
Core migrations 026/027 add desktop credentials and shell policy; runner migrations
076–083 add selection, ownership, runtime, settlement, consent and completion state.
The installer builds the contained Linux workspace worker (protocol 3). Sandbox
builds use the repository root as their Docker build context.

Workspace v2 and owner shells remain gated off. Existing single-repository sessions
retain scalar contracts. A discovery-capable peer without workspace v2 refuses
checked-out selections, including a single repository, rather than losing source
identity through a bare-name downgrade. Sandbox file viewing requires an in-container
reader and returns unavailable when that binding is absent. Do not enable a
capability from discovery availability or a successful image build. See
[workspace integration](../../docs/workspace-integration.md) for remaining gates,
including physical Mac and Docker engine restart acceptance. Reinstalling does
not authorize deleting retained workspace allocations.
