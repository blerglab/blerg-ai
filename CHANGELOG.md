# Changelog

All notable changes to Blerg are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/) with the pre-1.0 rule described in
[`CONTRIBUTING.md`](CONTRIBUTING.md#versions-and-releases): breaking changes may land in any
minor release and are called out here.

## [Unreleased]

## [0.1.1] - 2026-10-09

### Fixed

- The first public CI run was red on five jobs, none of them a product bug: a test built its
  database URL with `&` on a URL that had no query string; `install/desktop/.env.example` listed
  `BLERG_RUNNER_METRICS_TOKEN` as a generated key (it is optional, and now commented out like the
  others); the runner's browser tests could abort when Vite re-optimised `pdfjs-dist` mid-run
  (it is pre-bundled now); and lint findings in code written while CI was unavailable are
  addressed, each `nolint` with its reason. CI can also be started by hand (`workflow_dispatch`).

## [0.1.0] - 2026-10-09

First public release: everything below is what exists today. Blerg is pre-1.0, so expect
breaking changes in later minor versions.

### Chat and sessions

- **An HTML file shows as a live preview in the chat.** A published (or attached) HTML page up to
  2 MB is drawn small in its card, laid out at desktop width, and clicking it opens the viewer
  where the page can be used. The preview runs in the viewer's sandbox (its own origin, no
  network, no popups, no navigation) and cannot be clicked, scrolled or focused from the chat.
  This replaces the Preview tab. `@blerglab/chat` 0.3.3.
- **A desktop agent session survives a daemon restart.** Updating, crashing or restarting the
  desktop daemon used to end every agent session it hosted. A Claude Code agent session is now
  hosted again by the restarted daemon, from a record in its state directory, and continues the
  same Claude conversation. The step that was running is lost; the agent and the chat are told,
  an interactive session waits for the next message and an unattended one is asked to continue
  once. Sandboxed, MCP-connected and cron sessions, and other engines, are not brought back.
  The server now waits 20 seconds before marking a desktop daemon's sessions lost, so a restart
  is not seen as an ending by the board or by a broker, and a message typed in the browser
  meanwhile is delivered when the daemon returns.
  The first update to a daemon with this still ends the sessions running then. See
  [`install/desktop/DAEMON.md`](install/desktop/DAEMON.md#what-a-daemon-restart-does-to-running-sessions)
  and [`docs/design/agent-session-recovery.md`](docs/design/agent-session-recovery.md).
- **Interactive and unattended sessions.** Every agent session is started as `interactive` (a
  person is reading the chat: the agent discusses before acting, says what it is doing, asks and
  waits) or `unattended` (nobody is watching: it decides, notes its assumptions, finishes and
  summarises), and is told which. The launch sheet has an **Unattended** box (off by default);
  `POST /api/runner/start` takes `"interaction"` and defaults to `unattended`; a cron is always
  unattended. The board says which for each session it starts: a card's Run, reviews and standing
  agents are unattended, Discuss and the board session are interactive. The mode is fixed for the
  session's life and an unattended session is labelled on its page. A desktop daemon must be
  updated for desktop sessions to get it. See
  [`docs/talking-to-an-agent.md`](docs/talking-to-an-agent.md#interactive-and-unattended).
- **The board's sessions use the shared chat.** A card's session and the board session are
  shown by `@blerglab/chat`, the chat the runner's own app uses: live text as the agent writes, tool groups, files and attachments, start
  progress, review and mark-up. The board still brokers: the chat talks to new `/api/chat/…`
  routes on the board, which pass each request to the runner for that one session after the
  board's own permission check (`card.read` to look, `card.write` to change, and only a signed-in
  person may message or attach). The kickoff prompt shows as a collapsed "session brief" and the
  board's own relays are labelled as the board's, as before. `board/web` joins the npm workspace
  at the repository root.
- `@blerglab/chat` 0.3.0: `createProxyTransport` takes `headers` and `onUnauthorized` for an app
  that signs in with a bearer token; `ChatView` takes `describeUserMessage`, `readOnly` and
  `placeholder`; the reference proxy gains `MountWith` (a session-id `Resolve` and a
  `MessageSource`).
- **Review and mark-up** in the chat's file viewer. Review a published markdown or PDF file:
  select a passage and request a change, edit the markdown source directly, submit it all as one
  message (requests with ids, your edits as a diff). Mark up an image with pen, arrow, box and
  numbered pins. The session answers each request with `blerg-runner review reply`, and the
  answers show beside the requests. PDF pages are drawn by pdf.js, loaded only when a PDF review
  opens. A person may now attach 100 files per session (was 20; the 200 MiB total is unchanged).
  See [`docs/artifacts.md`](docs/artifacts.md#review-and-mark-up) and the
  [design note](docs/design/feedback-tools.md).
- **`@blerglab/chat`** (`packages/chat`): the chat surface — transcript, streaming, tool groups,
  files and viewer, attachments, start progress, composer — as one React package the runner's
  web app is now built from and an outside app can install. An app talks to a session through a
  `Transport` (`createBlergTransport` for the runner's own web app, `createProxyTransport` for an
  app that proxies the runner's v1 contract behind its own sign-in; a reference proxy in Go ships
  with the package), themes it through `--chat-*` CSS tokens, and can render its own cards from
  ```` ```card:<kind> ```` fences in the agent's Markdown. The runner serves the packed tarball at
  `GET /packages/` and advertises it in `/agents` as `ui.chat_package`. See
  [`packages/chat/README.md`](packages/chat/README.md) and the
  [design note](docs/design/chat-package.md). `packages/chat` and `runner/frontend` are one npm
  workspace rooted at the repository: `npm ci` at the root installs both.
- **An agent session's page is one view.** The agent's asks, updates and notes are in the
  conversation and are answered from its composer. A terminal session has a second tab, Messages.
- **A tab left open across an update says so.** The runner app shows "Blerg was updated" with a
  Reload button when the server it reconnects to is not the version the page was loaded from.
- **Resuming a cluster session shows its progress where you are.** Sending a message to a session
  whose pod has gone starts a new pod, and the start progress (queued, scheduling, image, clone,
  ready) appears after the conversation, worded as a resume, in place of the "No pod is running"
  banner until the new pod is ready.
- **A message to a session with no host is refused.** `POST /api/runner/sessions/{id}/message`
  and the MCP `send_message` tool answer 503 with `Retry-After` while the session waits for its
  host, and 409 once it has ended. A disconnected cluster session is resumed instead.
- **A stop made while the daemon was away holds.** A daemon that reconnects still hosting a
  session its person stopped (or that auto-stopped) is told to end it.
- A session you launch from the launch sheet with MCP connections is not restricted: it keeps the
  shell, web, your settings and always-on plugins, and only the connection's tools are bounded by
  what you allowed or set to propose. Crons and board-started sessions with connections are
  restricted. The launch sheet says a session with connections is private to you, and the start
  panel says when plugins were skipped because a session is restricted
  ([design note](docs/design/interactive-mcp-sessions.md)).

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
- Refresh-token reuse does not sign the account out everywhere. A rotated-out refresh token that
  comes back is first checked for the benign case (its successor was never presented, so the
  browser never received the rotation: a refresh frame torn down on a timeout, a dropped
  connection, two tabs racing) and that rotation is redone, at any age. Only when the successor
  has been used, so two parties held the same token, is it reuse: then that one browser session is
  revoked (its rows, and its access tokens through a `sid` revocation kind every component
  honours) and nothing else; other devices stay signed in and agent tokens keep working. The web
  apps refresh one tab at a time (a Web Lock) and never tear down a refresh in flight.
  `human_sessions.revoke_reason` records why a row ended. Design:
  [docs/design/session-reuse.md](docs/design/session-reuse.md).
- Agent tokens can be **re-minted** from Settings (`POST /api/tokens/{id}/remint`): the old token
  is revoked and a new one with the same name, preset and lifetime is shown once.

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
- Board scoping: a token minted for one board cannot read or write another.
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
- A session's own token (what `blerg-runner publish`, `ask`, `files` and the rest use) lives as
  long as the session: each use with less than half a day left pushes its expiry out another day,
  so a long session can still publish and message its person. Session end revokes it.
- Agent contract: an agent token can work with a session's files (`GET …/artifacts`, `raw`,
  `download`, `DELETE`, `POST …/uploads` under `/api/runner/sessions/{id}`) and follow a session
  live from the hub (`GET …/events/live`, SSE: `agent_event` frames with typing deltas,
  `replay_done`, `status`, `end`, resumable with `after_seq`, `tail` for the newest page first),
  for a session its credential started — anything else answers 404. `GET …/events` takes
  `before_seq` for older pages. The runner serves the chat package at `GET /packages/` and
  `/agents` lists it under `ui.chat_package`. The chat's Stop button uses the contract's
  `interrupt`.
- A session can list and remove the files it published: `blerg-runner files` and
  `blerg-runner unpublish <name|id>` (`GET`/`DELETE /api/sessions/{id}/files…` with the session
  token). A file a person attached stays theirs to delete. `publish` at the 50-file limit says so.
- Sessions started by a tool with an agent token, by a cron or by the operator key are listed in
  their own collapsible **Automated** section of the sidebar — grouped by the token's name, with a
  header that counts how many are running — instead of among the sessions you launched; ended ones
  stay there rather than in History. The session page says who started such a session. Sessions
  carry `started_by` (`kind`, and the token's `name`); migration 037.

- Always-on plugins on desktop sessions: the Settings plugin list reaches Claude agent sessions
  on a workstation daemon (Local sandbox and This machine), not only cluster pods. The daemon
  installs into its own plugin workshop under `~/.blerg-runner-daemon` — never the person's
  `~/.claude` — and starts Claude with `--plugin-dir` per plugin, mounting the cache read-only into
  a sandbox. Restricted sessions (grants, crons) and terminals get none. The start panel shows the
  install as a `plugins` stage. See `install/desktop/DAEMON.md`.
- New repository on the cluster: the launch sheet's **New repository** creates `owner/name` on
  GitHub or GitLab with the person's own token (private by default) and starts the session in it.
  When it cannot be created the session starts in an empty, initialised repository pointed at
  where it would be, with a warning in the start panel and a note to the agent. **Behaviour
  change:** `POST /api/sessions` with `new_repo: true` on the cluster used to be ignored (an
  existing repository was cloned); it now creates, or answers 409 for an existing repository. New
  request field `visibility`; new column `sessions.new_repo` (migration 035).
- Insights page and Prometheus metrics: pod startup time (total and per step), session durations and
  busy time, tokens by model and day, and an estimated dollar cost from admin-entered prices. Members
  see their own sessions, admins can see everyone's. `GET /metrics` (off until
  `BLERG_RUNNER_METRICS_TOKEN` is set) exposes aggregates only. See
  [`docs/telemetry.md`](docs/telemetry.md).
- MCP gateway and connections: pick a connection and its tools in the launch sheet (all off by
  default, each pinned to the tool's definition). The session reaches them through a gateway on its
  own listener (`BLERG_RUNNER_MCP_GW_ADDR`, `BLERG_RUNNER_MCP_GW_URL`) and never holds the
  credential. Cluster pods and the Docker sandbox only; not on the bare host.
- Crons: schedule an unattended agent run (a five-field expression and a time zone, at least 15
  minutes apart), with run history, run now, pause and renewal. A machine that was off runs an
  overdue cron once, marked late. See [`docs/crons.md`](docs/crons.md).
- Proposals: set a connection's write tool to "propose" and the agent's call is queued instead of
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

- Cluster session pods end after 24 hours without activity (a message sent or a turn finished)
  instead of a fixed 6-hour lifetime; the hard cap on any pod's life is 7 days. Both are settable by
  an administrator on the Cluster page (`PUT /api/cluster/settings`) and by
  `BLERG_RUNNER_POD_IDLE_TIMEOUT_SECONDS` / `BLERG_RUNNER_POD_TTL_SECONDS`.
- The cluster session cap (`BLERG_RUNNER_MAX_SESSIONS`, default 4) is editable by an
  administrator on the Cluster page, next to the pod limits (`max_sessions` on
  `PUT /api/cluster/settings`, 1 to 64, `0` or `null` returns to the environment value). It applies
  to the next session start, including cron runs, without a restart, and the page shows how many
  pods are in use.
- Cluster session workspaces are cloned as blobless partial clones, which starts pods much faster on
  a slow link.
- Unsent chat messages are kept per session across navigation and reloads, and active cluster
  sessions appear in the sidebar.

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

[Unreleased]: https://github.com/blerglab/blerg-ai/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/blerglab/blerg-ai/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/blerglab/blerg-ai/releases/tag/v0.1.0
