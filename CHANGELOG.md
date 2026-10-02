# Changelog

All notable changes to Blerg are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/) with the pre-1.0 rule described in
[`CONTRIBUTING.md`](CONTRIBUTING.md#versions-and-releases): breaking changes may land in any
minor release and are called out here.

## [0.1.0] - Unreleased

First public release: everything below is what exists today. Blerg is pre-1.0, so expect
breaking changes in later minor versions.

### Core (`blerg-core`)

- Control plane and single sign-on: one login that the board and the runner both accept, with
  local accounts, a one-time bootstrap admin password (printed once in the first-boot log), forced
  password change, `users create` / `users set-password` commands and rate-limited sign-in.
- Identity revocation that takes effect immediately, including for a re-login.
- Per-person credential vault for engine and git credentials (Claude, Codex, Hermes, GitHub,
  GitLab), encrypted with a local key (`BLERG_CORE_LOCAL_KEY`) or with a Vault transit backend.
  The Settings page is where each person adds their own.
- Agent tokens: user-minted, preset-based (for example `run-sessions`), listed and revocable in
  Settings.
- Agent discovery: a public manifest at `GET /agents` (Markdown for an LLM, JSON for a tool) and
  `GET /openapi.json`, plus the component registry the home page reads.
- Always-on plugins: a per-account list that is installed into every new cluster Claude session,
  restricted to an operator-set marketplace allow-list.
- Web UI for sign-in, Settings and the home page; every environment variable is documented in
  [`core/docs/CONFIG.md`](core/docs/CONFIG.md).

- MCP connections: add a remote MCP server (a URL plus a static token, or none) under Settings, up
  to 20 per account. The token is stored encrypted, never shown again, and every read of it by the
  runner is audited. See [`docs/mcp-connections.md`](docs/mcp-connections.md).
- Connection URLs are checked at connect time: `https` only, and internal addresses (loopback,
  private, link-local, metadata, carrier-grade NAT) are refused unless an operator lists the host in
  `BLERG_CORE_MCP_ALLOW_HTTP_HOSTS` / `BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS`.
- OAuth connections: sign in to an MCP server that supports it from Settings (PKCE, dynamic client
  registration where offered), with Reconnect when the sign-in lapses. Tokens are stored encrypted
  and never leave core. Needs `BLERG_CORE_PUBLIC_URL`; the redirect URI is
  `<core public URL>/auth/mcp/callback`.
- Short-lived board tokens: core can mint a 10-minute token for one board, which the runner uses
  for a cron's built-in board connection and revokes when the session ends.
- Cron tokens: internal endpoints (`/internal/tokens/mint`, `revoke`, `status`) let the runner act
  for an account without a login. The token is never issued as a bearer credential, lasts at most
  365 days and is revoked by "log out everywhere".

### Board (`blerg-board`)

- A kanban board that agents write to and humans curate: per-board custom field schemas,
  idempotent writes (`dedup_key`, `external_id`) and optimistic concurrency (`If-Match`).
- Admission gate: an LLM curator on the agent write path that denies duplicates and sends vague
  cards back for revision, with an append-only audit trail. It can use any OpenAI-compatible
  endpoint, the Claude API, or one account's own connected credential.
- MCP server (`/mcp`), a small CLI (`blerg-board ls|get|new|move|search`) and a bundled
  `managing-cards` skill.
- Board automation identity: a board holds one person's agent token and starts every runner
  session as that person, on their engine credential. The token is set on the board (engine:
  Claude, Codex or Hermes), never shown again, and expires.
- Sessions can be started from a card, and **Run board** works a whole board; they default to the
  Local sandbox when the daemon has one.
- Deployment signals (`POST` per-environment state) and the `GET /api/version` endpoint.
  Configuration: [`board/docs/CONFIG.md`](board/docs/CONFIG.md).

- Artifacts on cards: a new `artifact` link kind for a file a session published, an atomic
  `add_links` append on `PATCH /api/cards/{id}` (also used by the `blerg_card_link` MCP tool), and
  `blerg-runner publish --card` to attach a file to the session's card. Artifact links must be the
  runner's viewer address exactly. See [`docs/artifacts.md`](docs/artifacts.md). Migration `019`.
- Card links are only made clickable for `http(s)` addresses, everywhere on the card, and `pr`, `url`,
  `session` and `artifact` links must be web addresses when written.
- Board scoping: a token minted for one board can no longer read or write another.
- Focus board template: one click creates a board with Inbox, Today, This week, Waiting on,
  Someday, Proposed and Done columns and a matching field schema, with a starter prompt for a cron.

### Runner (`blerg-runner`)

- Server, workstation daemon and per-session pod entrypoint. The server serves the API, the
  daemon WebSocket and the web UI; sessions stream back as structured events.
- Three runtimes, chosen explicitly on the launch sheet's Run column and never silently
  widened: **cluster pod**, **Local sandbox** (a hardened container on the daemon's host) and
  **This machine** (unsandboxed, needs an acknowledgement).
- Agent and Terminal session kinds; engines Claude Code, Codex, Hermes and OpenClaw (OpenClaw on
  the host only), with engine-agnostic model and effort pickers fed by live model lists.
- Clone a repository by `owner/name` or URL onto a daemon (GitHub and GitLab), change the repos
  folder live, and start a session with no repository.
- Agent contract v1: `GET /agents` discovery, idempotent session start, one-shot sessions
  (`auto_stop`), server-sent events, a signed completion webhook and an MCP server.
- Session recovery after a daemon or pod restart, start-progress reporting, the reason a session
  ended, a Skills and plugins panel and a compact tool-call view in the agent transcript.
- Reference: [`runner/README.md`](runner/README.md).

- MCP gateway and connections: pick a connection and its tools in the launch sheet (all off by
  default, each pinned to the tool's definition). The session reaches them through a gateway on its
  own listener (`BLERG_RUNNER_MCP_GW_ADDR`, `BLERG_RUNNER_MCP_GW_URL`) and never holds the
  credential. Cluster pods and the Docker sandbox only; not on the bare host.
- Crons: schedule an unattended agent run (a five-field expression and a time zone, at least 15
  minutes apart), with run history, run now, pause and renewal. A machine that was off runs an
  overdue cron once, marked late. See [`docs/crons.md`](docs/crons.md).
- Proposals: set any tool of a connection to "propose" and the agent's call is queued instead of
  run. Review the frozen arguments on the Proposals page, approve or reject; pending proposals
  expire after 7 days. See [`docs/proposals.md`](docs/proposals.md).
- Built-in `board` connection: a cron can read and write cards on one board through a fixed set of
  board tools, with no stored token. A run that cannot start posts a failure card to that board
  (`BLERG_RUNNER_BOARD_URL`, `BLERG_RUNNER_BOARD_MCP_URL`).
- Private sessions: a cron session, and any session with MCP connections, is visible only to the
  account that started it, on every surface, administrators included.
- Restricted unattended sessions: sessions with connections and every cron run get file tools only
  (no shell or web access) and only the MCP tools you selected. Cluster crons never fall back to
  the shared operator credential.
- Installers: the gateway is published only on the sandbox network (desktop) or through an internal
  Service (Kubernetes), never the ingress, and the session network-policy example now covers it.
- Files from a session: an agent runs `blerg-runner publish <file>` and you get a card in the chat
  and a Files panel to download, view (Markdown, text and code, JSON, CSV, images, PDF, audio and
  video, sandboxed HTML) or delete it; 25 MiB per file, 50 per session, stored on the runner data
  volume. The cluster agent image now installs the `blerg-runner` CLI (its own entry point is
  `blerg-runner-pod`) and cluster sessions get a per-session token like desktop ones. See
  [`docs/artifacts.md`](docs/artifacts.md).
- Files to a session: attach files to a message with the paperclip, drag and drop or paste; the
  agent fetches them with `blerg-runner fetch --all`. 25 MiB per file, 20 files and 200 MiB per
  session, listed under "Uploaded by you" in the Files panel. See the "Uploading files to a
  session" section of [`docs/artifacts.md`](docs/artifacts.md).
- File versions: publishing (or attaching) a file whose name already exists in the session makes
  the next version of it instead of an unrelated duplicate, so an agent revises a file by
  publishing it again under the same name. The Files panel shows one row per file with a `v3`
  badge and the earlier versions behind a disclosure, the viewer says "v2 of 3" with a link to the
  latest, and an older version downloads as `name-v2.ext`. Up to 20 versions per name. Migration
  `033` numbers existing duplicates by time. See the "Versions" section of
  [`docs/artifacts.md`](docs/artifacts.md).

- Cluster session pods now end after 24 hours without activity (a message sent or a turn finished)
  instead of a fixed 6-hour lifetime; the hard cap on any pod's life is 7 days. Both are settable by
  an administrator on the Cluster page (`PUT /api/cluster/settings`) and by
  `BLERG_RUNNER_POD_IDLE_TIMEOUT_SECONDS` / `BLERG_RUNNER_POD_TTL_SECONDS`.
- The cluster session cap (`BLERG_RUNNER_MAX_SESSIONS`, default 4) is now editable by an
  administrator on the Cluster page, next to the pod limits (`max_sessions` on
  `PUT /api/cluster/settings`, 1 to 64, `0` or `null` returns to the environment value). It applies
  to the next session start, including cron runs, without a restart, and the page shows how many
  pods are in use.
- Cluster session workspaces are cloned as blobless partial clones, which starts pods much faster on
  a slow link.
- Unsent chat messages are kept per session across navigation and reloads, and active cluster
  sessions appear in the sidebar.
- Mid-turn steering for Claude Code sessions: a message sent while the agent works is written to a
  long-lived `claude` process and delivered at the agent's next tool step instead of waiting for the
  whole turn; Esc interrupts the turn without restarting the agent, and a queued message then runs
  next. Set `BLERG_CLAUDE_STEERING=0` for the old one-process-per-message behaviour (an older Claude
  Code that lacks the streaming input mode falls back to it by itself). See
  [`docs/talking-to-an-agent.md`](docs/talking-to-an-agent.md).
- Pause: a live cluster session can be paused, which frees its pod and keeps the session; a message
  resumes it. A killed or ended session now says so in the chat and locks the message box.
- Long conversations open on their most recent messages at once; the earlier ones load in the
  background without moving the view.
- The sidebar's Cluster count follows sessions as they start and end, and a cap saved on the Cluster
  page shows at once. The Cluster page no longer goes blank on a cluster with no operator engines.
- Chat toolbar icons on narrow screens; a calmer "sending" bubble; a skeleton while history loads.

### Installers

- Desktop (`install/desktop`): `blerg-up.sh` generates secrets, builds and starts Postgres,
  core, board and runner with Docker Compose, builds the pinned Local sandbox image, and installs
  the workstation daemon as a background service (systemd user service on Linux, launchd on
  macOS). Flags: `--no-build`, `--no-daemon`, `--no-open`, `--regen-secrets`, `--reset-admin`.
  `daemon/install.sh` takes `install`, `uninstall`, `status` and `preflight`.
- Kubernetes (`install/k8s`): `deploy.sh` builds and pushes the images, generates the secrets and
  applies a kustomization with ingress and optional cert-manager TLS. The cluster runtime is on
  by default, with agent sessions as throwaway Jobs in their own namespace. A guide for having an
  AI agent do the install is in [`install/k8s/AGENT-INSTALL.md`](install/k8s/AGENT-INSTALL.md).
- Repository hygiene: Apache-2.0 licence, a scrub check that keeps private hostnames and
  addresses out of tracked files, and an offline documentation link check.
