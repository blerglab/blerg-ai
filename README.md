# Blerg

Blerg runs coding-agent sessions (Claude Code, Codex and others) on your own machine or your own Kubernetes cluster, and gives them a shared kanban board to draft and pick up work. It exists so that agents can do real work under a login, a budget and a sandbox you control, instead of on someone else's platform.

**Try it in roughly ten minutes, plus the first sandbox image build** (Linux, macOS or Windows with WSL2; Docker and a logged-in Claude Code or Codex needed, see [Requirements](#requirements)):

```
git clone https://github.com/blerglab/blerg-ai.git blerg && cd blerg/install/desktop
./blerg-up.sh
```

The script builds everything, starts it and opens `http://localhost:8081`. Then follow the first-login and first-session notes under [On your desktop](#on-your-desktop-docker). On a cluster instead? Jump to [On Kubernetes](#on-kubernetes).

## How it fits together

```mermaid
flowchart LR
  you([You, in a browser])
  subgraph stack[The stack: Docker Compose on your machine, or a Kubernetes namespace]
    core[blerg-core<br/>login, accounts, agent tokens,<br/>credential vault]
    board[blerg-board<br/>kanban board agents write to]
    runner[blerg-runner server<br/>launches and watches sessions]
    db[(Postgres)]
  end
  daemon[Workstation daemon<br/>on your machine, as you]
  sandbox[Local sandbox<br/>hardened container]
  pod[Cluster runtime<br/>one throwaway pod per session]
  you --> core
  you --> board
  you --> runner
  board -->|starts sessions| runner
  core --- db
  board --- db
  runner --- db
  runner <-->|WebSocket| daemon
  daemon -->|Agent or Terminal session| sandbox
  daemon -->|unsandboxed, opt-in| host[Your machine]
  runner -->|Kubernetes Jobs| pod
```

- **`blerg-core`** is the control plane. It issues the login every other component accepts, holds each person's engine and git credentials encrypted, and mints agent tokens.
- **`blerg-board`** is a kanban board built for agents to write cards and humans to curate them; an admission step turns away duplicates and vague cards. It can start sessions from a card.
- **`blerg-runner`** launches and supervises agent sessions and streams their work back as structured events. Its web UI has the launch sheet.
- **The workstation daemon** runs on your machine, as you, and does the actual launching for the desktop install. It cannot be containerised because it drives your own `claude`/`codex` login and `tmux`.
- **Runtimes.** Every session runs in one place you choose on the launch sheet: the **Local sandbox** (a hardened container on your daemon's host, the desktop default), the **cluster runtime** (a throwaway pod per session, the Kubernetes default), or **This machine** (unsandboxed, as you, never a default).

Also on this page: [safety model](#safety-model), [documentation map](#documentation).

## Project status

Blerg is **pre-1.0**. Expect breaking changes between minor versions, and read [`CHANGELOG.md`](CHANGELOG.md) before you upgrade. The supported desktop shape is **one core per developer machine**; a core shared by several people belongs on the Kubernetes install. Only the current `main` receives security fixes ([`SECURITY.md`](SECURITY.md)).

## Requirements

**Desktop install**

- Linux, macOS, or Windows with WSL2. On WSL2, systemd must be enabled: put `[boot]` and `systemd=true` on two lines in `/etc/wsl.conf`, then run `wsl --shutdown` from Windows and reopen the distribution.
- Docker with Compose v2 (Docker Desktop, or the docker engine on Linux), plus `git`, `curl` and `openssl`. The daemon is built with your host Go 1.25 or newer if you have it, and inside a container if you don't. Unsandboxed terminal sessions use the host's `tmux`.
- **Claude Code or Codex installed and logged in before your first session.** The installer does not do this for you. Run `claude` (or `codex`) once and sign in, or run `claude setup-token` and export the result as `CLAUDE_CODE_OAUTH_TOKEN` before `./blerg-up.sh`. Sessions use that login and therefore your subscription or budget.
- On Linux your user must be in the `docker` group. **Membership of the `docker` group is equivalent to root on that machine**: a sandbox session can start any container with any mount. Add only accounts you would trust with root.
- Disk: the sandbox image is roughly 6-7 GB and takes several minutes to build.

**Kubernetes install:** see [On Kubernetes](#on-kubernetes) for the list.

## Install

Two ways to run the full stack: on your own machine with Docker, for one developer; or on a
Kubernetes cluster, shared by a team, with agent sessions running as pods.

### On your desktop (Docker)

**Prerequisites:** everything under [Requirements](#requirements) above, and a terminal (the installer asks where your repos live).

```
cd install/desktop
./blerg-up.sh
```

What that does, once: generates `install/desktop/.env` with random secrets, builds the three service images, builds the local-sandbox session image (`blerg-runner-sandbox:latest`, roughly 6–7 GB; this step takes several minutes, longer on a slow connection), installs the runner daemon as a background service (systemd on Linux, launchd on macOS), then prints the next steps and opens `http://localhost:8081`. It exits back to your shell when everything's up — that's success.

**First login.** `blerg-core` issues the login every component accepts (board and runner have no passwords of their own). The one-time admin credentials are in core's first-boot log:

```
docker compose logs blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD
```

That line shows both the username (`provider_subject=…`) and the password. It is printed exactly once, on the boot that creates the account — a restart or rebuild never prints it again. If the log has rotated away, mint a fresh one-time password instead: `./blerg-up.sh --reset-admin` (or `docker compose exec blerg-core /blerg-core users set-password --subject <username>`). Click **Sign in** on the landing page, log in, and you'll be sent to `/change-password` — pick a real one (12–72 characters), then sign in again with it.

Log in at exactly `http://localhost:8081` — the desktop stack turns the cookie `Secure` flag off for that origin so every browser (Safari included) works; `127.0.0.1` or a LAN IP will not.

**First session.** Open **Runner** (`http://localhost:8083`) → **+ New** → pick a repo under your repos root, or type any `owner/name` or paste a repository URL (a public one like `torvalds/linux`, or, in the Local sandbox, a private one your GitHub/GitLab token in Settings can reach; it is cloned into your repos root first — see [DAEMON.md](install/desktop/DAEMON.md#cloning-a-repository-you-name)) → Launch. The button reads **Launch Session** for a repo already in your repos root, **Clone & Launch →** when the repo has to be cloned first, and **Create & Launch →** when you are starting a new folder (or, with Cluster pod selected, a **New repository** the runner creates on GitHub or GitLab with your token — see [CLUSTER-RUNTIME.md](install/k8s/CLUSTER-RUNTIME.md#new-repository)). The Run column shows every choice at once; on a desktop install it defaults to an **Agent** session in the **Local sandbox** — a container on your daemon. Pick **Terminal** instead for a tmux session you can watch and type in; that one keeps permission prompts ON, so Claude will ask you to trust the folder (press `y`) the first time. Board session / card spawn / **Run board** on the Board go through the runner's v1 start contract instead, and get the same default: an agent-kind session in the Local sandbox when your daemon has the sandbox image, on the host only when it doesn't (or when you opt out — see the next paragraph).

**Before the Board can start sessions**, give each board an automation token — the Board starts nothing without one, and says so. In core **Settings → Agent tokens**, create a token with preset `run-sessions`; on the board, open the settings panel (the **model** chip in the header) → **Automation**, paste it, pick the engine (Claude, Codex or Hermes), and Save. Every session that board starts — **Run board** and everything it spawns, plus the card and board-chat buttons — then runs as you: on a cluster install on the engine credential you connected in Settings (for Hermes, your own endpoint), on the desktop on your daemon's engine login as below. It must be your own token; it is never shown again, and it expires (90 days by default — the panel says when). Anyone who can write cards on that board can spend your credential and could get an agent to reveal it, so only connect a token you're comfortable with every card-writer effectively having. Details: [`board/docs/CONFIG.md`](board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

**What runs as you.** Every session — sandboxed or not — uses the engine login on your machine (`claude`, `codex`) and therefore your subscription/budget; the sandbox mounts your `~/.claude` and `~/.codex` read-write so the engine can use them, so it does NOT protect those credentials or your host Claude settings. A repo you launch a session in is code the agent will run: don't point it at something you wouldn't run yourself. From the launch sheet both session kinds — Terminal and Agent — default to that sandbox, so the agent sees `/workspace` and your mounted logins and nothing else of the machine. **This machine, unsandboxed** is the third card in its Run column: it runs on the host as you with full filesystem access (and, for an Agent session, no permission prompts), it is never a default, and Launch stays disabled until you tick the acknowledgement. Sessions started through the v1 start contract — everything the Board launches — get the same default: the sandbox when the daemon has its image, where the agent reaches the board and runner over the `blerg-sandbox` network but has no git credentials (it can commit, not push); opt out onto the host with `runtime: daemon`, or `BLERG_BOARD_RUNNER_RUNTIME=daemon` on the board. A v1 start never *asks* to bypass permission prompts, but note that an Agent session runs non-interactively and its engine bypasses them regardless, on either runtime. Stop everything: `systemctl --user stop blerg-runner-daemon` (macOS: `launchctl bootout gui/$(id -u)/dev.blerg.runner-daemon`), then `tmux ls -F '#{session_name}' 2>/dev/null | grep '^blerg-' | while read -r s; do tmux kill-session -t "=$s"; done` and `docker rm -f $(docker ps -q -f name=blerg-sandbox-)`. The tmux line ends only the daemon's own `blerg-<id>` sessions on this machine and leaves your other tmux sessions running (one of yours whose name starts with `blerg-` would go too); a Local sandbox session's tmux lives in its container and ends with the `docker rm`.

**Day to day**

- Stop: `docker compose down` (from `install/desktop`). The daemon keeps running and reconnects on its own; `./daemon/install.sh uninstall` removes it.
- Start again: `./blerg-up.sh` (rebuilds by default so a `git pull` takes effect; `--no-build` to skip, `--no-open` to keep the browser closed).
- Upgrade: `git pull && ./blerg-up.sh` — database migrations run automatically; `./daemon/install.sh install` rebuilds the daemon; `docker build --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest ../../runner/sandbox` rebuilds the sandbox image (keep the `UID` argument, or files the sandbox writes into your repos may end up owned by the wrong user). The engines inside it are pinned, so a rebuild only changes them when the pins changed; [DAEMON.md](install/desktop/DAEMON.md#local-sandbox-runtime) says how to move to a newer engine version.
- Backup: `docker compose exec -T postgres pg_dump -U blerg -d blerg_core > core.sql` (and `blerg_board`, `blerg_runner`), plus a copy of `install/desktop/.env` — **losing `BLERG_CORE_LOCAL_KEY` loses every credential stored on the Settings page.** Full backup and restore procedure for both installs: [`docs/backup-and-restore.md`](docs/backup-and-restore.md).
- Uninstall, from `install/desktop`: first *Stop everything* (above), then `docker compose down -v --rmi local` (the containers, the database volume and the three `desktop-blerg-*` images), `./daemon/install.sh uninstall` (the service, and the `~/.local/bin/blerg-runner` and `~/.claude/skills/managing-tickets` links the daemon made), `docker rmi blerg-runner-sandbox:latest` and `rm -rf ../../bin`. These are left for you to remove by hand, on purpose:
  - `<repos root>/.blerg-runner/` and `~/.blerg-runner-daemon/`: session recovery records (they contain session tokens, which stop working once the database volume is gone) and the daemon's saved repos-folder setting. `install.sh uninstall` never deletes anything under your repos root, and it keeps the daemon's settings so that a reinstall comes back pointed at the same folder.
  - `install/desktop/.env`: it holds `BLERG_CORE_LOCAL_KEY`, the stack's secrets and, if you gave one, your Claude OAuth token. Delete it once you no longer need a backup; it is the only copy of that key.
  - The base images the builds pulled (`postgres:16`, `golang:1.25`, `golang:1.25-bookworm`, `node:22-alpine`, `debian:bookworm-slim`, `gcr.io/distroless/static:nonroot`) and Docker's build cache (`docker builder prune`). Other projects may use them.
  - `~/.claude`: if it did not exist before Blerg and you don't use Claude Code, it can go; otherwise leave it, because it holds your Claude login and settings. If you opted into `BLERG_RUNNER_PROVISION_CLAUDE_MD`, remove the block between the `blerg-runner-messaging` markers in `~/.claude/CLAUDE.md`.
  - Repositories cloned into your repos root: they are yours and stay.
- Ports taken? Edit `BLERG_PORT_*` in `.env` and re-run `./blerg-up.sh`.
- **Forgot the admin password?** `./blerg-up.sh --reset-admin` prints a new one-time password (every session is signed out).
- **Add a teammate:** `docker compose run --rm --no-deps -T --entrypoint /blerg-core -e "DATABASE_URL=postgres://blerg:blerg@postgres:5432/blerg_core?sslmode=disable" blerg-core users create --subject alice --role member` — hand them the printed one-time password; they'll be asked to change it. One core per developer machine is the supported desktop shape; a shared core belongs on the [Kubernetes install](install/k8s/README.md).

Managing the daemon, other platforms, engines and `--no-daemon`: [`install/desktop/DAEMON.md`](install/desktop/DAEMON.md).

### On Kubernetes

**What you need:** `kubectl` pointed at the cluster; `docker`, `envsubst` and `openssl` on the machine you deploy from; a default StorageClass (Postgres takes a 5Gi volume); an image registry the cluster's nodes can pull from; ingress-nginx; DNS for `<DOMAIN>`, `board.<DOMAIN>` and `runner.<DOMAIN>`; and TLS for those names — a cert-manager ClusterIssuer or a proxy in front — because login cookies are `Secure`.

```
cd install/k8s
cp -n .env.example .env
$EDITOR .env        # REGISTRY, TAG, DOMAIN, CLUSTER_ISSUER
./deploy.sh
kubectl -n blerg logs deploy/blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD
```

**What you get:** core at `https://<DOMAIN>/`, the board at `https://board.<DOMAIN>/` and the runner at `https://runner.<DOMAIN>/`, one Postgres, and agent sessions that run as throwaway Jobs in their own namespace. Log in with the username and one-time password from the last command, choose a real password, then each user adds their own engine and GitHub credentials in **Settings** (and can list plugins there, such as `superpowers`, to have them installed in every cluster session they start) — sessions run as the person who launched them, so no operator credential is needed.

**Where next:** [`install/k8s/README.md`](install/k8s/README.md) — every setting, verification, backup and uninstall, troubleshooting, and connecting a workstation daemon. Having an AI agent do the install? Give it [`install/k8s/AGENT-INSTALL.md`](install/k8s/AGENT-INSTALL.md). How cluster sessions work: [`install/k8s/CLUSTER-RUNTIME.md`](install/k8s/CLUSTER-RUNTIME.md).

## Safety model

In plain terms; the precise rules and how to report a problem are in [`SECURITY.md`](SECURITY.md).

- **Agents run code, as somebody.** An unsandboxed session runs on your machine as you, with your full filesystem access. A sandboxed session sees only the repository at `/workspace` and the engine logins mounted into it. A cluster session runs as a throwaway pod.
- **The Local sandbox is not a credential vault.** It drops capabilities, limits memory and processes and hides the rest of your machine, but it mounts your engine logins (`~/.claude`, `~/.codex`) so the engine can work. The code an agent runs there can read them. It holds no git credentials: an agent can commit, and you push.
- **Point sessions only at code you would run yourself.** A repository you launch a session in is code the agent will execute.
- **Credentials are per person.** Each account stores its own engine and git credentials in core's vault (encrypted with `BLERG_CORE_LOCAL_KEY` on the desktop), and a cluster session runs with the credentials of the person who launched it. There is no shared operator credential to leak.
- **Plugins and board automation tokens act with a person's credentials.** A plugin you list under Settings runs third-party code in every cluster session you start, with that session's access. A board's automation token lets that board start sessions as you, so anyone who can write cards on that board can spend your credential and could get an agent to reveal it. Connect only what you are comfortable sharing that way.
- **Unattended agents read untrusted text.** A cron, or any session with [MCP connections](docs/mcp-connections.md), runs with file tools only and just the tools you selected, in a sandbox or a pod, in a session only you can see. A hostile message can still steer it into misusing those tools, so allow the fewest and prefer read-only ones; see [`docs/crons.md`](docs/crons.md).
- **The stack listens on `127.0.0.1` by default.** Put authentication and TLS in front of it before exposing it.
- **Docker group membership is root-equivalent.** Add only accounts you would trust with root.

## For agents and tools

Blerg describes itself, so an automated tool never has to be told how it works. Read
`<core>/agents` first — `http://localhost:8081/agents` on the desktop stack. With
`Accept: text/markdown` it is a guide written for an LLM; as JSON it is the machine-readable
manifest of every component on the install, with its base URL, auth and operations (and
`<core>/openapi.json` alongside it). Both are public, so you can read them before you hold a
credential. Then mint one: **Settings → Agent tokens**, preset `run-sessions`, and send it as
`Authorization: Bearer <token>`. With that you can start and drive coding-agent sessions over the
runner's REST contract or its MCP server — full reference in
[`runner/README.md`](runner/README.md#api-contract-v1).

## Configuration

Every environment variable each service reads, with defaults (the runner's are in the table in
[`runner/README.md`](runner/README.md#configuration-env)):

- [`core/docs/CONFIG.md`](core/docs/CONFIG.md) — `blerg-core` (login/refresh origins, auth providers, key backends, frontend build-time vars)
- [`board/docs/CONFIG.md`](board/docs/CONFIG.md) — `blerg-board`

## Run blerg-core alone (Go, no Docker)

Needs a Postgres and two environment variables (`DATABASE_URL`, `BLERG_CORE_LOCAL_KEY`); the
exact recipe is in [Running from source](CONTRIBUTING.md#running-from-source).

## Run board/runner frontends in dev mode (hot reload)

```
cd board/web && npm ci && npm run dev
cd runner/frontend && npm ci && npm run dev
```

Requires `blerg-core` (and, for the runner, `blerg-board`) already running — see above.

## Tests

```
make test                                  # Go tests: contracts, core, runner and board (some need Postgres)
cd core/web && npm test                    # core frontend
cd board/web && npm test                   # board frontend
cd runner/frontend && npm test             # runner frontend
```

## Documentation

| | |
|---|---|
| [`install/desktop/DAEMON.md`](install/desktop/DAEMON.md) | The workstation daemon: service management, engines, the Local sandbox |
| [`install/k8s/README.md`](install/k8s/README.md) | Kubernetes install, settings, verification, uninstall, troubleshooting |
| [`install/k8s/CLUSTER-RUNTIME.md`](install/k8s/CLUSTER-RUNTIME.md) | How cluster sessions work |
| [`core/docs/CONFIG.md`](core/docs/CONFIG.md), [`board/docs/CONFIG.md`](board/docs/CONFIG.md) | Every environment variable |
| [`board/README.md`](board/README.md), [`runner/README.md`](runner/README.md) | The board and the runner, including the runner's API contract |
| [`docs/mcp-connections.md`](docs/mcp-connections.md) | MCP connections: giving a session tools from a remote MCP server, through the runner's gateway |
| [`docs/proposals.md`](docs/proposals.md) | Proposals: agent write calls queued for your approval, what you approve, limits |
| [`docs/crons.md`](docs/crons.md) | Crons: scheduled, unattended agent runs, what they can and cannot do |
| [`docs/telemetry.md`](docs/telemetry.md) | Insights page and Prometheus metrics: startup times, session durations, tokens, estimated cost |
| [`docs/talking-to-an-agent.md`](docs/talking-to-an-agent.md) | Messaging a running agent, interrupting, pausing a cluster session, long conversations |
| [`docs/design/`](docs/design/) | Design notes: [AI crons and MCP connections](docs/design/ai-crons.md), [mid-turn steering](docs/design/mid-turn-steering.md) |
| [`docs/artifacts.md`](docs/artifacts.md) | Files from a session: `blerg-runner publish`, what the app can show, limits, where they live |
| [`docs/backup-and-restore.md`](docs/backup-and-restore.md) | What state exists, how to back it up and restore it, desktop and Kubernetes |
| [`CHANGELOG.md`](CHANGELOG.md) | What changed in each release |

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for setup, tests, releases and what we look for in a change, and the [Code of Conduct](CODE_OF_CONDUCT.md). Report security problems privately, as described in [`SECURITY.md`](SECURITY.md).

## Trademarks and affiliation

Claude and Claude Code are trademarks of Anthropic; Codex and OpenAI are trademarks of OpenAI; Hermes, OpenClaw and every other product or company name mentioned here belong to their respective owners. Blerg is an independent project. It is not affiliated with, sponsored by or endorsed by any of them.

## License

Apache License 2.0, see [`LICENSE`](LICENSE).
