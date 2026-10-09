# Blerg on your desktop

One developer, one machine, Docker. The stack (core, board, runner, Postgres) runs in Docker
Compose; a small daemon runs on your machine, as you, and launches sessions in a sandbox
container. A team sharing one Blerg belongs on the [Kubernetes install](../k8s/README.md).

## Requirements

- Linux, macOS, or Windows with WSL2. On WSL2, systemd must be enabled: put `[boot]` and
  `systemd=true` on two lines in `/etc/wsl.conf`, then run `wsl --shutdown` from Windows and
  reopen the distribution.
- Docker with Compose v2 (Docker Desktop, or the docker engine on Linux), plus `git`, `curl` and
  `openssl`. The daemon is built with your host Go 1.25 or newer if you have it, and inside a
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
`http://localhost:8081`. It exits back to your shell when everything's up; that is success.

## First login

`blerg-core` issues the login every component accepts (board and runner have no passwords of
their own). The one-time admin credentials are in core's first-boot log:

```
docker compose logs blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD
```

That line shows both the username (`provider_subject=…`) and the password. It is printed exactly
once, on the boot that creates the account; a restart or rebuild never prints it again. If the
log has rotated away, mint a fresh one-time password instead: `./blerg-up.sh --reset-admin` (or
`docker compose exec blerg-core /blerg-core users set-password --subject <username>`). Click
**Sign in** on the landing page, log in, and you'll be sent to `/change-password`: pick a real one
(12–72 characters), then sign in again with it.

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

Give each board an automation token; the Board starts nothing without one, and says so. In core
**Settings → Agent tokens**, create a token with preset `run-sessions`; on the board, open the
settings panel (the **model** chip in the header) → **Automation**, paste it, pick the engine
(Claude, Codex or Hermes), and Save. Every session that board starts then runs as you: on a
cluster install on the engine credential you connected in Settings (for Hermes, your own
endpoint), on the desktop on your daemon's engine login. It must be your own token; it is never
shown again, and it expires (90 days by default; the panel says when). Anyone who can write cards
on that board can spend your credential and could get an agent to reveal it, so only connect a
token you're comfortable with every card-writer effectively having. Details:
[`board/docs/CONFIG.md`](../../board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

## What runs as you

Every session, sandboxed or not, uses the engine login on your machine (`claude`, `codex`) and
therefore your subscription or budget. The sandbox mounts your `~/.claude` and `~/.codex`
read-write so the engine can use them, so it does NOT protect those credentials or your host
Claude settings. A repo you launch a session in is code the agent will run: don't point it at
something you wouldn't run yourself.

From the launch sheet both session kinds, Terminal and Agent, default to the sandbox, so the
agent sees `/workspace` and your mounted logins and nothing else of the machine. **This machine,
unsandboxed** is the third card in its Run column: it runs on the host as you with full filesystem
access (and, for an Agent session, no permission prompts), it is never a default, and Launch stays
disabled until you tick the acknowledgement.

Sessions the board starts through the runner's API get the same default: the sandbox when the daemon has its image, where the agent reaches the board and runner
over the `blerg-sandbox` network but has no git credentials (it can commit, not push). Opt out
onto the host with `runtime: daemon`, or `BLERG_BOARD_RUNNER_RUNTIME=daemon` on the board. A v1
start never *asks* to bypass permission prompts, but an Agent session runs non-interactively and
its engine bypasses them regardless, on either runtime.

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
- **Start again:** `./blerg-up.sh` (rebuilds by default so a `git pull` takes effect; `--no-build`
  to skip, `--no-open` to keep the browser closed).
- **Upgrade:** `git pull && ./blerg-up.sh`; database migrations run automatically.
  `./daemon/install.sh install` rebuilds the daemon;
  `docker build --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest ../../runner/sandbox`
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

From `install/desktop`: first *Stop everything* (above), then `docker compose down -v --rmi local`
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
- The base images the builds pulled (`postgres:16`, `golang:1.25`, `golang:1.25-bookworm`,
  `node:22-alpine`, `debian:bookworm-slim`, `gcr.io/distroless/static:nonroot`) and Docker's build
  cache (`docker builder prune`). Other projects may use them.
- `~/.claude`: if it did not exist before Blerg and you don't use Claude Code, it can go;
  otherwise leave it, because it holds your Claude login and settings. If you opted into
  `BLERG_RUNNER_PROVISION_CLAUDE_MD`, remove the block between the `blerg-runner-messaging`
  markers in `~/.claude/CLAUDE.md`.
- Repositories cloned into your repos root: they are yours and stay.
