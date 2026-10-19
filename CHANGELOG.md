# Changelog

All notable changes to Blerg are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/) with the pre-1.0 rule described in
[`CONTRIBUTING.md`](CONTRIBUTING.md#versions-and-releases): breaking changes may land in any
minor release and are called out here.

## [Unreleased]

## [0.4.1] - 2026-10-19

### Added

- Attachments show local image, first-page PDF and text previews before sending; document formatting uses accessible icons with active states (chat 0.26.0).

- Document reviews support rendered Markdown editing, tracked/clean views, and passage-linked margin comments with Ctrl+Alt+M. Image markup supports selectable annotations with comments, style editing and visibility controls (chat 0.25.0).

- Resolved and rejected code review threads can be collapsed individually or hidden together.

- Code review opens directly beside patch lines, with inline threads, general comments, Markdown previews, code suggestions and resolved/rejected replies carried across revisions (chat 0.24.0).
- Mermaid diagrams and inline-data Vega-Lite charts render in chat, Markdown and the file viewer, with native chat colors that follow theme changes, source, copy and zoom controls (chat 0.23.1).
- Request code changes on selected diff lines in a session, with desktop and phone selection and the existing review reply flow (chat 0.23.0).

### Fixed
- Reopening a document or code review thread requires a new comment, included with the original request and prior answers when sent to the agent (chat 0.26.2).
- Markdown and PDF reviews support collapse, Hide resolved and Reopen, preserving reply history and original passage anchors (chat 0.26.1).
- A browser could be signed out after waking with several Blerg tabs open.

## [0.4.0] - 2026-10-17

### Upgrade notes
- Reinstall the desktop daemon for child-session recovery and local-engine reports; start a new
  session to receive the updated CLI guide. Chat is 0.22.0.

### Added
- Add permitted plugins and bundled skills from a session’s Capabilities panel, with human-approved agent requests and between-turn loading for supported host Claude sessions.
- A session can start and drive other sessions on its desktop with your approval
  (`blerg-runner session …`), with children in Live, the sidebar and Insights (chat 0.22.0).
- `blerg-runner budget` reports the desktop's remaining capacity separately by provider.

- Propose Board cards from Claude or Codex sessions, with editable approval drafts, verified source metadata, and optional administrator-only card details.

- Choose a Board when launching a session, with verified access, shared MCP/CLI tools, and matching cron board discovery.
- Session and subagent progress bars show native plans or explicit `blerg-runner progress` counts, with each caller kept separate.
- A "vX.Y.Z available" link beside the version in the runner sidebar, core footer and board top bar when a
  newer public release exists, linking to its GitHub release page. Core checks api.github.com every 6 hours
  (the check shows the server's IP to GitHub); `BLERG_UPDATE_CHECK=0` turns it off, `BLERG_UPDATE_REPO`
  changes the repository.

### Fixed
- Authenticated messaging CLI requests refuse redirects, preserving session and board credentials.

- Keep long shell commands and results compact in the session panel, with scrollable full text and visible status and actions.
- Codex subagent typing stays separate from the main reply, and a completed subagent message no longer makes the main agent's unfinished text disappear.
- Ctrl+A (Cmd+A) with a file, code or ticket open over the page selects what is in it, ready to copy,
  instead of the whole page behind it; a text field keeps its own select-all (chat 0.21.1).
- A bare file name the agent mentions (`plan.md`, no folder) that is a file published in the session
  opens that published file, instead of looking for it at the top of the session's folder and saying
  "Not found" (chat 0.21.2).

## [0.3.1] - 2026-10-17

### Upgrade notes
- Desktop: reinstall the daemon (`./daemon/install.sh install`) so a background shell command shows its
  command line in Running now; until then it shows its description, as before.
- Chat (`@blerglab/chat`) is 0.21.0, with a new theme token `--chat-font-display` (section headings;
  a theme that does not set it gets a condensed system face); `commitUrl` is accepted but no longer used (commit chips stay in
  Blerg: they open the item's diff or copy the sha).

### Added

- On a touch screen, holding a message, code block, ticket card or checklist row opens a sheet with
  Copy message, Select text and Cancel; the always-visible Copy buttons are gone there (desktop keeps
  the hover button). Nothing is selected by the hold itself, and Select text opens the text full
  screen, all of it selected with the phone's own handles, with Copy (the part still selected) and
  Done.
- `blerg-runner checklist remove <n|--text …>` drops one checklist item; the other items keep their
  ticks, commits and diffs (before, removing one meant setting the whole list again).
- A published diff of one file with 20 or fewer changed lines shows its hunks in its card, colored
  and numbered like the diff viewer, with View kept.

### Changed
- The session panel's Live tab no longer has an MCP servers section; the Capabilities tab's MCP servers
  group shows each server's status as it is now (connecting, connected, failed, needs sign-in, disabled).
- Live's one-line summary is two controls: the ticket chip opens the ticket over the page, the checklist
  part goes to the Plan tab.
- The checklist's progress bar is a short rounded bar beside the count (green when done), not a thin
  line across the panel.
- A background shell command in the panel's Running now reads as a command: a `>_` glyph, the
  command in monospace (the full text on hover), what it is for, and shell, elapsed and status
  chips, with no model or effort. Subagents get an agent glyph. The daemon now sends the command
  with the task's start (reinstall the daemon; older starts show their description).
- The Files list is compact: one ~28 px row per file with a glyph for its kind, the name, and a muted
  size, version, time and "you" (for files you attached); Download and Delete are small icons that
  show on hover or focus (always on touch), and clicking the row opens the file. Past six files a
  filter box, a sort (newest or name) and an origin choice (all, from the agent, from you) appear,
  and are remembered per browser.
- The file viewer colors code and JSON: a `.go`, `.ts`, `.py` and so on opens as numbered, colored
  lines, and JSON is pretty-printed and colored (the start of a very large file; the rest plain).
- A CSV or TSV is one table style in the viewer and in the inline preview (borders, header row, zebra
  rows); the preview is the first rows and columns of the same table.
- Markdown files (inline preview, viewer, the review page) read like a README: system UI font, ruled
  headings, code chips, padded code blocks, bordered zebra tables. Chat messages keep their compact
  scale, with the code chip, table, and quote refined to match (chat 0.18.0).
- A pinned ticket reads like an issue in a tracker. Its status chip is tinted by meaning (gray to do,
  blue in progress, amber blocked, green done, faded and struck through when canceled) in the chat,
  the modal and the session panel. The chat's card is a summary: key, status, title, one line of type,
  priority and assignee, the start of the description and **Open ticket**. The modal has the
  description, the acceptance criteria, the comments with avatars and a Details column (chat 0.19.0).
  It is laid out like the Board's card sheet: uppercase section headings split by hairlines, Details
  as a panel of label/value rows, acceptance criteria as a list of boxes, comments as boxed entries
  on a trail, and **Open in …** as a button beside Close; on a phone the title takes the full width.
  A P-scale priority (P0 to P4, also "P1 - Critical") gets the same ↑ → ↓ glyph as High, Medium and
  Low (chat 0.21.0).
- A checklist item shows its commits' short shas directly (three, then "+N more") instead of an
  "N commits" toggle. With the item's diff, a sha opens it in Blerg's diff viewer at that commit;
  without one it copies the sha. **± View diff** is a real button with the diff's +/− counts. Commit
  chips, the History tab's too, no longer link to GitHub or another forge; `commitUrl` is accepted
  and unused (chat 0.19.0).

### Fixed
- Pasting code copied from a web page into the message box, inside a code fence you typed, no longer
  fences it a second time: inside an open fence the text goes in as copied. A code block copied from
  GitHub keeps its language on the fence.
- The card of a published diff shows its summary again ("6 files · +58 −11" and the file names); in the
  app it was squashed to a thin line because the image-preview style won over it (chat 0.17.2).

## [0.3.0] - 2026-10-17

### Upgrade notes
- Desktop: reinstall the daemon (`./daemon/install.sh install`). 0.3.0 needs it for opening session
  files, MCP connection tracking and the first-message wait, Codex plan updates, background-task
  reconciliation, child effort reporting, the shared command guidance for Codex sessions, and the
  session guide lines that ask agents to pin their ticket, keep a checklist and publish diffs. The
  `blerg-runner` CLI itself updates with the checkout and works under an older guide; until the
  reinstall, the panel simply has less to show.
- Runner: migrations 053 to 058 (cron engine, latest capabilities, checklist/ticket/plan and MCP-status
  indexes on `agent_events`, `account_preferences`) run when the server starts. No manual step.
- Chat package: `@blerglab/chat` is now 0.17.1. An app that embeds it takes the new version from the
  runner's manifest and installs it ([Install](packages/chat/README.md#install)); ChatView's
  `onViewCommitDiff` (0.15.0) is removed, and a side panel that drew checklist "View diff" passes
  `onViewDiff(artifactId)` instead. A proxy that serves the board's chat may add the optional
  `GET {prefix}/preferences` route for code colors.
- Contributors: Go database tests need a Postgres user with CREATEDB
  ([CONTRIBUTING.md](CONTRIBUTING.md)).
- New optional setting: `BLERG_RUNNER_MCP_PROMPT_WAIT_S` (default 30; 0 turns the first-message wait off).

### Added
- Chat (`@blerglab/chat` 0.12.0 to 0.17.1, now 0.17.1):
  - Code blocks are syntax-colored (a lazily loaded highlighter; diff lines colored) and, over 25
    lines, show 15 with Show all and an Open viewer with line numbers; a pasted message over 25 lines
    compacts the same way. Markdown, text, code, JSON, CSV and PDF files get small inline previews
    (size-capped, fetched when scrolled into view). (0.12.0)
  - `checklist` rows and `ticket` cards in the transcript; the checklist, the engine's plan and the
    pinned tickets in `LiveState`. (0.13.0)
  - Code color themes: `codeThemes`, `applyCodeColours` and friends, and six new code tokens
    (variable, built-in, operator, text, background, line numbers). (0.14.0)
  - MCP and checklist commits: `slots.aboveComposer`, `commitUrl`, `McpHoldLine`, `CommitChips`,
    `mcp_status` helpers. (0.15.0)
  - Diffs and file links: `DiffView`, the `diff` view, `parsePatch` and friends; `canOpenWorkspaceFiles`,
    `files.workspaceFile`, `workspaceFromCode`/`workspaceFromHref`. (0.16.0)
  - Rich copy and paste: every message has a Copy button that copies its Markdown source and its
    rendered HTML; selecting text in the chat and copying keeps headings, lists, links, tables and code
    as Markdown plus sanitized HTML (card chrome left out); and pasting rich HTML into the message box
    inserts it as Markdown (code language kept, images and scripts dropped), with Ctrl/Cmd+Shift+V
    still pasting plain text, as does a very large or deeply nested paste. See [Copying and pasting](docs/talking-to-an-agent.md#copying-and-pasting).
    (0.17.0)
- Diffs. `blerg-runner diff [<range> | <commit>... | --staged | --worktree] [--title T] [-- <paths>]`
  runs git in the session's folder (hooks, filters, external diff tools and text conversions of the
  repository never run) and publishes the patch as `<title>.diff`; `.diff` and `.patch` files get the
  view `diff`, a card that says "N files · +a −d" with the first names, and a diff viewer (file list,
  unified or side by side, line numbers, folded context, the code colors, Copy patch). The session
  guide names the command. See [Diffs](docs/artifacts.md#diffs).
- Clickable file links in the chat. A relative link or a path in inline code in the agent's text
  (`README.md`, `src/a.go:120`, `docs/a.md#L12`) opens that file from the session's folder in the
  viewer, scrolled to and marking the line when one was named, with Copy path, Download and
  **Save a copy to Files**. Only human session owners (and administrators, for sessions that are not
  private) can open these files, through the daemon, with traversal, symlink, file-identity and
  secret-file protections. See [Files the agent mentions](docs/talking-to-an-agent.md#files-the-agent-mentions).
- Settings. A **Settings** entry under Insights in the left bar (a tab on a phone) opens a dialog over
  the current page; it holds Show thinking and Compact tool calls, which left the session panel (same
  per-browser keys, so earlier choices carry over), the code colors below, and a link to Blerg's main
  Settings. The session panel's Settings section is gone; **Resume when limit resets**, which belongs to
  one session, sits in its Session section.
- Code colors. Settings has a **Code colors** section with ten themes
  (Blerg, GitHub Dark and Light, One Dark, Dracula, Monokai, Solarized Dark and Light, Nord, Gruvbox
  Dark), one for dark mode and one for light, per-color changes under Customize, and a live preview of
  real code blocks (ts, go, python, json, diff). The choice is kept per account on the runner
  (`GET`/`PUT /api/me/preferences`, migration 057 `account_preferences`) and cached in the browser. The
  chat's code blocks, viewer and file previews wear it, in the runner and in the board (which reads it
  through its chat proxy, `GET /api/chat/preferences`, and the runner's `GET /api/runner/preferences`;
  the board has no editor).
- The session checklist and pinned tickets. `blerg-runner checklist set|add|tick|untick|clear` keeps
  a checklist of the session's work; `blerg-runner pin <file.json>` pins a ticket from any tracker
  (JIRA, Bugzilla, GitHub, GitLab, Linear, a Board card, …) in one source-neutral shape, and
  `blerg-runner unpin` takes it off. The chat draws a compact checklist row and a ticket card (a re-pin
  says what changed); the session panel shows them above the engine's own plan. `checklist tick|add
  --commit <sha>` names the commits that finished an item (a commit chip per item, linked when the
  repository is known) and also publishes the diff of those commits; the chip gets **View diff**. New
  routes `POST /api/sessions/{id}/checklist` and `/tickets` (the session's own token); `GET /live` gains
  `checklist`, `plan`, `tickets` and `ticket`. Migration 055 adds three partial indexes on
  `agent_events`. Pinning copies the ticket's text into the session.
- Engine plans in the session panel. Claude Code's own task list and Codex's native plan updates both
  appear as **Plan** when the agent set no checklist.
- MCP servers in the session panel and **Send now**. Claude and Codex MCP connection state is tracked
  natively, with durable live snapshots. The Live tab gains an **MCP servers** line ("Connecting 2 of
  3…", then "2 connected · 1 failed · 1 needs sign-in") that opens to each server's state, source and
  tools; the first message is held for up to 30 seconds while the servers connect, the chat shows
  "Waiting for MCP servers before sending your first message · 0:12 / 0:30 · Send now" above the
  message box, and the session's owner can send it at once (a `release_held_prompt` message; `GET
  /live` gains `can_release_prompt`).
- Reported child configured effort in chat, Live and Insights, with explicit unknown values.
- Adapter-specific tool-definition refresh guidance in capabilities and tool-change review.
- Target-scoped engine/model/effort discovery for browser, REST and MCP callers, including
  existing-session catalogs and dynamic launch, cron and Board engine choices.
- Board role/card effort settings, effective native session metadata and backend-qualified
  recovery; engine changes clear future-launch overrides while preserving existing sessions.
- Published artifact links can open the chat viewer directly through a trusted session mapping.

### Changed
- The session panel has a **Plan** tab (Live, Plan, History, Files, Capabilities): pinned tickets,
  the checklist or the engine's plan, and the session's published diffs move there, and Live keeps one
  summary line ("Checklist · 18 of 23 · Next: ...") that opens it, so a long checklist no longer pushes
  Needs you and Running now off screen. The tab shows only when there is something for it.
- Go test suites run packages in parallel with isolated template-backed PostgreSQL fixtures and
  shorter injected test waits (the runner suite takes about 3 minutes, down from about 10); database
  test users need CREATEDB. Fixtures safely clean abandoned helper databases; OAuth, Codex timeout and
  webhook tests use controlled clocks and wait for asynchronous work before teardown. Stale Claude
  idle timers no longer close a newly rearmed idle window.
- Agent-facing launch guidance uses target-scoped engine/model discovery instead of unscoped catalogs.
- Engine integration metadata, customization roots, managed credential access modes and
  restriction/limit capabilities use shared registries with explicit adapter boundaries.
- REST/MCP and embedded proxies share model controls and optional durable message IDs.

### Fixed
- The "Couldn't sign you in · Retry" banner on the board and runner heals itself. After a power cut
  or an outage it used to stay until the person pressed Retry; it now tries the quiet hidden-frame
  renewal when the network comes back, when the tab is shown, and on a 5 s, 15 s, 30 s, 60 s, then
  two-minute backoff (paused while the tab is hidden, one attempt at a time), and a token takes it
  down with no page reload (the runner reconnects, the board reloads its data). When core answers
  that the sign-in has ended, the banner says "Your sign-in has ended · Sign in" and stops trying;
  for that core now sends a renewal frame whose sign-in is gone back to the page's own
  `/silent-refresh.html` with `#signin_error=<reason>` instead of to its login page. Automatic
  full-page redirects to `/auth/refresh` stay bounded exactly as before.
- A long checklist item wraps beside its box instead of dropping the whole text onto the next line.
- The session panel's "Resume when limit resets" checkbox keeps its status line under it in muted
  text.
- Codex terminal and recovered sessions receive the shared Blerg command guidance without overwriting
  user instructions.
- Orphaned background tasks close after daemon restarts, with atomic running counts and compatible
  live-task inventory; reconciliation does not block unrelated daemons.
- Usage caps without a reported reset use neutral wording instead of inferring a monetary spend limit.
- Root model usage attribution uses native usage identities/reports and validated partitions; ambiguous
  model-switch usage remains unknown, and partitions preserve a single logical turn count.
- Codex custom-home detection, transient background inventory recovery, capability model
  refresh and root/subagent model-reroute reporting. Newer manual settings retain priority.
- Insights attributes child usage to observed effective models without double counting.
- Scoped launch/recovery validation and Board session recovery/control handling.

## [0.2.1] - 2026-10-16

### Upgrade notes
- **Reinstall the desktop daemon** to get the connection check: `git pull`, then
  `./install/desktop/blerg-up.sh` (or `./install/desktop/daemon/install.sh install`). Until then a
  0.2.0 daemon keeps working against a 0.2.1 server exactly as before (it is sent nothing new).
- Server and daemons can be upgraded in either order. A 0.2.1 daemon against a 0.2.0 server logs
  `daemon: link to … open for 5s but not confirmed by the server` and `install.sh status` shows
  `unconfirmed`; that is expected and fine. Session pods pick the change up with the new runner
  image; nothing to do.
- `install.sh install` (and `blerg-up.sh`) now **exits 6** when the server refuses the daemon
  token. Before anything is replaced the running daemon is left alone; after the restart the new
  daemon is installed but refused. Fix `BLERG_RUNNER_DAEMON_TOKEN` in `install/desktop/.env` to
  match the server's and run it again. A daemon with a wrong token now retries once a minute (it
  used to retry every 2 s) and logs one `daemon: REFUSED:` line.
- Files are larger now: 100 MiB per file (was 25), 500 MiB of attachments per session (was 200),
  and a new 1 GiB total for the files one session's agent publishes (there was none). Check your
  runner data volume size (Kubernetes `blerg-runner-data`, `RUNNER_DATA_SIZE`, default `10Gi`;
  desktop: the `blerg-desktop-runner-data` Docker volume, on the disk Docker uses) against the
  sessions you keep, or lower the limits with `BLERG_RUNNER_MAX_FILE_MB`,
  `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB` and `BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB`.
- Kubernetes: re-apply the manifests (`deploy.sh`). The ingress body limit goes from `100m` to
  `128m` so a 100 MiB file fits; a controller other than ingress-nginx needs the same change by
  hand. The three limit keys are wired, optional, in `runner-deployment.yaml`.

### Security
- Go 1.26.9 everywhere the build picks a Go: every `go.mod` and `go.work`, the Dockerfiles (pinned
  by digest), the CI jobs (which now read the version from `go.mod`), the desktop installer's host
  and container builds, and the session images, whose Go toolchain is copied from the devcontainer
  build and so moves to 1.26.9 too. This picks up the standard-library fixes released since
  1.25.14; `govulncheck` reports nothing reachable in any module. `golang.org/x/crypto` moves to
  v0.56.0 in core and runner.
- `core/web` test tooling moves to vitest 4, which clears the `npm audit` findings in its
  development dependencies (they never shipped in the built app).

### Added
- The daemon keeps the state of its connection (`connecting`, `connected`, `unconfirmed`,
  `refused`) in `~/.blerg-runner-daemon/connection.json` (mode 0600, no token), and
  `install/desktop/daemon/install.sh status` prints it.
- `GET /api/daemon/whoami` on the runner: checks a daemon token without connecting a daemon
  (`200 {"ok":true,"mode":"daemon"|"runtime"}`, otherwise `401`).
- Desktop: `install.sh install` (and `blerg-up.sh`) asks the server whether it accepts the daemon
  token from `install/desktop/.env` before it replaces the running daemon. A refused token stops it
  with exit code 6 and leaves the running daemon as it was; an older server (no such check) or a
  server it cannot reach only gets a note. After the restart it waits up to 30 seconds for the new
  daemon to report its connection and stops with exit code 6 when the server refused it; then
  `blerg-up.sh` exits 6 as well. Troubleshooting: `install/desktop/DAEMON.md`, "Is the daemon
  connected?".

### Changed
- The session panel no longer repeats what is a click away: its Session section drops the Insights,
  Files and Capabilities links (the header's Insights button and the panel's own tabs open them),
  and on a wide screen the panel drops its own close button (the header's panel toggle opens and
  closes it). The drawer on a narrow screen keeps its ✕.
- The desktop installer uses a host Go only if it is 1.26.9 or newer; otherwise it builds the
  daemon in a container, as before.
- `make lint` refuses a golangci-lint other than the version CI pins (v2.13.1), and
  `scripts/check_lint_pin.sh` (run by CI) keeps the Makefile and the workflow on the same version.
- Removed `runner/Dockerfile.daemon` and the `make docker-build-daemon` target: the image could not
  build (it needs the repository root as context) and nothing used it.
- Runner: the file limits are configurable (`BLERG_RUNNER_MAX_FILE_MB`,
  `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB`, `BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB`, whole MiB; an
  invalid value keeps the default and logs one line; a per-file limit above a session total is
  lowered to it, also with one line) and passed through by the desktop compose
  file, the Kubernetes ConfigMap and the `.env` examples. The runner reports the limits in force as
  `limits` (`max_file_bytes`, `max_session_upload_bytes`, `max_session_artifact_bytes`) in
  `GET /api/sessions/{id}/files`, `GET /api/runner/sessions/{id}` and the browser's
  `initial_state`, so clients can refuse a file that is too big before sending it. A declared size
  over a limit is refused before the body is read; the 413 names the file's size and the limit.

### Fixed
- The Claude usage meter works on work and enterprise accounts. Their usage answer has no
  five-hour or weekly window, only a monthly spend limit, so the Claude row used to vanish. The row
  now shows "month" (Monthly spend limit) with the share used, "<1%" while under one percent, and
  "capped" once the limit is used up. A subscription with extra usage switched on shows it beside
  5h and week. Only the percentage leaves the machine, never an amount, limit or currency. Update the
  runner server (it carries the web app) and reinstall the desktop daemon: an older server ignores
  the new window.
- Lint passes again under Go 1.26: two `//nolint:gosec` comments in the runner's artifact code
  that the linter now reports as unused are gone.
- `TestCookieSecureFlag` models Safari's cookie rule itself. Go 1.26's cookie jar sends a Secure
  cookie to loopback addresses over http, as Chrome and Firefox do, so the jar no longer stood in
  for Safari. The login code was right and is unchanged.
- Desktop: a fresh install can store files. The runner image runs as an unprivileged user
  but shipped no `/data`, so the data volume was created owned by root and every publish and
  upload failed with "storage error". The image now ships `/data` owned by that user, and a
  one-shot service in the desktop compose file (`blerg-runner-data-init`, reusing the `postgres:16`
  image) repairs a volume an earlier install left behind, keeping its files. The runner also logs
  one `STORAGE NOT WRITABLE` line at startup, naming the directory, the user id, the error and the
  fix, when it cannot write to its data directory (it still starts).
- Desktop: container sessions and the stack work behind a corporate proxy. Docker copies the
  proxy settings of `~/.docker/config.json` into every container, which sent calls to
  `blerg-runner` and `blerg-board` to the proxy. Sandboxed sessions now get `NO_PROXY` and
  `no_proxy` as the union of the daemon's, your Docker client config's and Blerg's own service and
  loopback names (the proxy settings themselves are left alone), and the core, board and runner
  services of the desktop compose file list the service names as exceptions, keeping your own
  `NO_PROXY` and the `noProxy` of your Docker client config (`blerg-up.sh` passes it on).
  Reinstall the daemon and start new sessions for this to apply; see
  `install/desktop/DAEMON.md` ("Troubleshooting the stack").
- The daemon no longer says it is connected when the server refused its token. It logged
  `daemon: connected to …` as soon as it had sent its hello, and the server answered a wrong token
  only by closing the connection (4401), so a reinstall that kept a stale token looked fine. The
  server now confirms an accepted daemon with a `daemon_welcome` message (sent only to a daemon
  whose hello asks for it, so a 0.2.0 daemon sees nothing new; a session pod's carries its
  session), and the daemon logs `connected` only on it. Against a 0.2.0 server, which sends none,
  a connection that stays open for 5 seconds is taken as accepted and logged as not confirmed. A
  refused token is one loud `daemon: REFUSED:` line that says where the token comes from (for a
  session pod: its runtime token), and it no longer resets the reconnect backoff: a refused daemon
  slows to one try a minute instead of retrying every 2 seconds.
- Desktop: `blerg-up.sh --no-daemon` no longer prints the daemon token in its "do this next" box;
  the command it shows reads the token from `.env`.
- Runner: a failed upload, publish, fetch or topic says what happened. Failures of the runner's own
  disk or database on these routes answer with one sentence (which step failed; permission
  denied, disk full, read-only or missing; what to do) and an `error_id` that is also in the
  runner's log line, instead of "storage error" or "query failed". A full disk during an upload is
  reported as one, not as "upload interrupted". An attachment whose bytes are gone answers `410`
  to the agent, as it already did to a person. The remaining fragment replies on these routes
  ("session has ended", "file no longer available", "no database", "invalid JSON body") are
  sentences now.
- Messaging CLI (`blerg-runner`): requests to a local server or board (`localhost`, a loopback
  address, a one-word name such as `blerg-runner`) no longer go through a proxy from the
  environment, so a corporate proxy that Docker injects into a session container cannot swallow
  them; other addresses still honour the proxy. Every request goes through one helper, and a
  failure of `publish`, `fetch`, `topic` and the rest says the HTTP status, the server's sentence
  and error id (or an older server's raw reply, trimmed), the address called (without any
  user:password), and for a connection error which proxy variables are set (by name only) and
  whether NO_PROXY covers the host. A malformed proxy variable is reported by name, never quoted:
  a request that gets no reply is described in fixed words, never in the exception's own text. Every
  failure line is redacted before it is printed or cut short: user:password in an address,
  Authorization and Proxy-Authorization values, Bearer and Basic credentials, and the session's and
  daemon's tokens.
  `publish` checks a file against the runner's advertised limit (25 MiB from an older runner)
  instead of its own fixed 25 MiB, and against the session's published total when the runner
  reports it, streams it with its size declared,
  and zips a directory against the same limit. Success output is unchanged.
- Chat (`@blerglab/chat` 0.11.0), runner and board web apps: the attachment limit is a `ChatView`
  prop, `maxFileBytes`; the runner's app passes the limit its server advertises and the board's
  chat takes it from the session status its proxy passes through (`MAX_ATTACHMENT_BYTES`, 25 MiB,
  stays exported as the fallback for an older runner). A refused upload or download shows the
  server's sentence, and its error id in small text after it, instead of a generic "upload
  failed"; a proxy's plain-text refusal is shown as it is, and an upload cut off before any answer
  says so. New exports: `FileRequestError`, `fileErrorText`, `formatLimit`, `FileLimits`.
- Board, chat proxy: an upload through the board's chat (and the reference proxy in
  `@blerglab/chat`) carries the browser's `Content-Length` to the runner instead of going chunked,
  so a file over the runner's limit is refused before it is read. An app with its own
  `RunnerClient` sets the request's `ContentLength` from that header (`chatproxy.ContentLength`).

## [0.2.0] - 2026-10-16

### Upgrade notes
- Databases: runner migrations 039–052 and core migrations 019 and 020 run on startup. Runner
  migrations 041, 042 and 049 build indexes on `agent_events` (`agent_events_reel_idx`,
  `agent_events_live_idx`, `agent_events_child_lifecycle_idx`) inside their transaction, once, at
  the first start of the new server; each blocks transcript writes for the duration of one scan of
  that table (seconds to tens of seconds on a few million rows). Daemons buffer and re-send, so
  nothing is lost.
- Desktop: reinstall the daemon (`install.sh install`, or `blerg-up.sh`), first or together with
  the server. Much of this release lives in the daemon: background tasks and subagent rows,
  thinking and retry states, usage meters, recovery after a restart and its notices, message
  receipts, the Codex app-server driver, Codex crons and limit resume. A session left with a
  revoked messaging token heals only once both are updated.
- Desktop: the new Claude usage meter is on by default; to turn it off, add
  `BLERG_RUNNER_CLAUDE_USAGE=0` to `install/desktop/.env` and run `install.sh install`.
- Cluster: deploy the new agent image together with the runner. Pods now authenticate with a
  per-launch runtime token instead of the operator daemon key.
- Cluster: re-apply `install/k8s/cluster-runtime.yaml`. The runner's role gains
  `persistentvolumeclaims` (get, list, create, delete) for retained native conversations and
  `pods/log` (get) for Cluster model probes.
- Cluster: new `blerg-config` keys `BLERG_RUNNER_NATIVE_HISTORY`, `…_STORAGE_CLASS`, `…_SIZE`,
  `…_MAX_CLAIMS` and `…_RETENTION_HOURS`, wired in `install/k8s/runner-deployment.yaml`. The
  shipped ConfigMap turns native history on, which creates a 1 Gi PVC per account-owned Claude or
  Codex Cluster session from the default StorageClass; set `BLERG_RUNNER_NATIVE_HISTORY` to
  `"false"` to keep it off.
- Kubernetes: `deploy.sh` creates the runner's data claim `blerg-runner-data` (`RUNNER_DATA_SIZE`,
  default `10Gi`; optional `RUNNER_DATA_STORAGE_CLASS`; empty, nothing to migrate) and the runner
  Deployment now uses the `Recreate` strategy, so a deploy stops it for a few seconds (see Fixed).
- Each person who wants a managed Codex connection completes Settings → Managed connections →
  Codex once; an existing imported snapshot is not converted.
- Breaking: a script that parsed `provider_subject=` from core's first-boot line or from
  `blerg-core users` needs updating, and a host of `@blerglab/chat` that used `BackgroundChip` or
  `backgroundTasks: 'strip'` must drop them (see Changed).

### Security
- Only a Cluster pod's daemon can obtain the managed ChatGPT access token: the access route takes
  the pod's runtime token, which no agent process inherits, and refuses the session's messaging
  token (in the agent's environment) with 403. Core spends at most one forced refresh a minute per
  account and engine (a forced call inside that window gets the cached access), and a failed refresh
  refuses only its caller: the account's other sessions keep the still-valid access until it expires.
- Session start ran enabled Claude plugins' SessionStart hooks for every engine and outside a
  session's confinement; they now run only for host native-loop sessions.

### Added
- An agent session's page has a side panel: a column beside the chat on a wide screen, a drawer over
  it below 1024 px, opened from a toggle at the right of the session header whose badge counts what
  needs you and what runs. It has four tabs. **Live**: **Needs you** lists the agent's open
  questions, each with a box that answers it, and the MCP tool changes waiting on the session
  (linking to the Proposals page, which now shows that session's proposals only, with a way back to
  all of them); **Running now** lists the subagents at work (task, type, model, tokens, what each is
  doing, how long it has run) and the background tasks (start time, a ticking clock, a finished one
  for ten seconds, a failure until you dismiss it), each with "Show in conversation"; **Session**
  gives where and how it runs, when it started and how long it has lived, with links to Insights and
  the Files and Capabilities tabs; **Settings** holds "Show thinking" and "Compact tool calls", moved
  from the toolbar and kept under the same keys, and **Resume when limit resets** (below), so the
  toolbar keeps model, effort, Rules and Stop. **History** (below). **Files** is what the Files dialog
  over the chat showed (every file with its versions, Download, View, Delete, Attach files), read
  again each time the tab comes on screen; **Capabilities** is the Skills & plugins dialog's content,
  asked for only while on screen. The Files and Capabilities tabs carry a count badge like Live's,
  hidden at zero: the number of files (names, not versions) and the number enabled (skills, enabled
  plugins, connected MCP servers; a disabled skill does not count). A tab choice saved under the
  tab's earlier name, Skills, still opens Capabilities. The two dialogs and their toolbar buttons are
  gone (a terminal session keeps its Skills & plugins button). On a wide screen the panel is open by
  default; closing it is remembered per browser, and a new question reopens it once. The drawer has
  the same tabs and opens only when you ask. `@blerglab/chat` 0.10.10: `ChatView` takes `compact`
  (the host holds the choice; no checkbox) and `filesButton={false}` (no Files button or dialog, and
  no `openFiles`), `useLiveState` reports `files` (the list, its operations, the message box's file
  picker), and `FilesList` is the Files panel's list without its dialog; `loadCompact`, `saveCompact`
  and `COMPACT_KEY` are exported.
- The panel's Live tab, its header badge and its opening by itself read what is live from the runner
  server, `GET /api/sessions/{id}/live`, which goes through the session's whole event log (open
  asks, running subagents and background tasks, the viewer's pending tool changes) instead of the
  part of the transcript the page has loaded; the page folds the events that come after the reply in
  until its next read. So refreshing a long session lists everything on the first paint, including a
  subagent or task that started before the loaded part of the conversation, and Running now agrees
  with the sidebar's ⏱ count. A subagent or background task with no recorded end whose newest event
  is more than 12 hours old (a daemon restart cut it off) is left out, and its subagent row reads
  stopped, "no end recorded". Migration 042 adds the partial index it reads
  (`agent_events_live_idx`). An older runner server keeps the old reading. `@blerglab/chat` 0.10.2:
  `useLiveState` is documented as the fallback for a host whose server has no such route.
- The panel's **History** tab lists the pieces of work (topics) the agent marked with the new
  `blerg-runner topic "<title>"` / `blerg-runner topic --done "<outcome>"` (the session guide, the
  managed CLAUDE.md block, `--help` and AGENTS.md tell it when; the CLAUDE.md line adds "Without
  markers the History tab shows only a summary."), newest first: each opens onto your request (or
  the marker), every file it published with all its versions (a chip opens the file), its subagents
  with status, model, tokens and time, and its outcome and commits, with "Show in conversation" for
  what the chat has loaded. Everything outside a topic is one collapsed "Unmarked work · 42 agents ·
  8 files" line after the cards, opening onto those files and one line per subagent; with no topics
  the tab says how the agent marks one. Nothing is guessed from your messages. New routes `POST` and
  `GET /api/sessions/{id}/topics`; migration 041 adds a partial index on `agent_events`
  (`agent_events_reel_idx`), built once at the first start of the new server (it holds writes to
  that table while it builds).
- A topic can name the commits its work landed: `blerg-runner topic --done "<outcome>" --commit <sha>`
  (repeat it, or comma-separate the shas; at most 50). History's Done row shows each as a short
  chip that links to the commit when the runner knows the session's repository, and copies the sha
  otherwise; the card's counts add "N commits". `POST /api/sessions/{id}/topics` takes `commits` on a
  done, and the reel's done row carries `commits: [{sha, url}]`. The session guide asks for them.
- After a daemon restart an agent session picks up what the restart cut off by itself, once,
  interactive sessions included. The desktop daemon's recovery record lists the background
  commands, Monitors and subagents a session has running, the agent's own open tool calls
  (`open_calls`) and whether the session was waiting; when the restart kills them they are marked
  stopped in the chat ("the daemon restarted"; the sidebar's ⏱ count drops to 0), and the agent
  gets one system message listing each background task and subagent by kind, description and id and
  each open call by what it ran, telling it to check their worktrees, outputs and state first,
  resume subagents by message and re-run unfinished commands, never anything destructive, then
  "Continue the task you were working on.". A Claude Code session is continued whether the restart
  caught it mid-generation, in one of its own tool calls, or waiting on background tasks or
  subagents (also one whose turn had ended with only subagents running, and from a recovery record
  an older daemon wrote). The one exception is an interactive session that was waiting for the
  person (status `waiting`, an open `blerg-runner ask` or `AskUserQuestion`, or an ask the server
  reports open for it: after each hello the server tells the daemon which of its sessions have one,
  `open_asks`). A message from you that reaches the session first (one sent while the daemon was
  down, too) goes first, replacing the continue and carrying the list. Any session, unattended too,
  cut off in a command on a fixed deny-list (force push, `rm -rf`, migrations, deploys,
  `install.sh` and service restarts, `DROP`/`TRUNCATE`; listed in `install/desktop/DAEMON.md`) is
  paused instead, and a session cut off before it had a conversation is neither continued nor
  paused: the person is asked to send its task again. Subagents still running when their Claude
  Code process ends are marked stopped too, and the daemon sends a heartbeat as soon as it
  connects. Host Codex agent sessions now persist their thread ID and survive daemon restarts
  with the same guarded continuation; a missing transcript is reported explicitly, and Codex
  sessions started before this release, which have no record, cannot be recovered automatically.
  `install.sh install` asks the new build (`blerg-runner-daemon status --open-work`) what the
  restart would cut off and asks "Reinstall anyway? [y/N]" when there is any (`--yes`, also on
  `blerg-up.sh`, skips it; with no terminal it stops). Reinstall the daemon to get it; the first
  restart past an older daemon cannot list its background work.
- A session the restart left waiting (it was waiting for the person, it had already been
  continued once, or its conversation was lost) is **paused by a restart**: the daemon reports it
  (`session_paused`), the server keeps `sessions.paused_reason` (migration 043) until it next runs,
  ends or is revived, closes its open asks (their `blerg-runner ask` died with the restart, so an
  answer must reach the agent as a message) and broadcasts `session_paused_changed`; the sessions
  list carries `paused_reason`. A paused card shows the waiting "?" with the tooltip "paused by a
  restart"; you resume it by sending it a message. A message accepted for a paused session (from any tab) ends the pause. A message sent
  to a session while its desktop daemon is away is held for the daemon instead of being taken as
  the answer to the session's open `blerg-runner ask`, which died with the restart (browser and
  API).
- The agent of a session a restart cut off (Claude Code or Codex) is told why the daemon
  restarted, so it does not take a command it ran for the cause. `install.sh install` (and so
  `blerg-up.sh`) writes a `restart-planned` marker (`update` or `reinstall`) into the daemon's
  state directory right before it restarts a running daemon, and the daemon notes
  `stopped-cleanly` there when it is told to stop; the next daemon reads and removes both. With a
  marker the notice says "updated and restarted" (or "reinstalled" when the build is unchanged,
  "restarted" after a plain stop) and "Nothing you ran caused it."; without one: "The daemon
  stopped unexpectedly. A test run, a build or ordinary commands cannot cause that; …"
  (`install/desktop/DAEMON.md`).
- The working line says what the agent is on when no tool call is in flight: "Thinking…" while the
  model thinks, and "Retrying the model (attempt 2)…" while the engine retries a failed API call,
  until the model answers (the muted retry line stays). A tool call it names now says what it does:
  "· waiting on Bash · Run the unit tests 4:12" (a Bash call's description, else the start of its
  command; an Agent's or a Monitor's description), and the session panel's Running now line for a
  subagent's Bash call gives its description too. The daemon starts Claude Code with
  `--include-partial-messages` when the CLI lists it and sends a transient `thinking_started` /
  `thinking_done` (with `parent_call_id` for a subagent's) as a thinking block opens and closes, the
  native engine likewise; the `api_retry` error notice carries `attempt`, `max_retries` and
  `retry_in_ms`. Needs a daemon reinstall. `@blerglab/chat` 0.10.8.
- The transcript keeps what the model thought: a Claude Code thinking block is recorded (event kind
  `assistant_thinking`) and shown as plain assistant text, captioned "thinking" where the time sits,
  with no special styling or fold; "Show thinking" in the panel's Settings hides it.
  `@blerglab/chat` 0.9.4 takes a `thinking` prop on `ChatView` (`'shown'` by default, or `'hidden'`;
  the old `'open'` and `'collapsed'` still work for one version).
- A Claude Code subagent's work is gathered under it in the conversation, as one row like a row of
  tool calls: its task and one glyph for its state (a spinner, a muted spinner and ⏱ in the
  background, ✓, ✗ or …). Its type, model and tokens are in the row's tooltip and accessible name,
  since the session panel shows them in full. Clicking the row opens what it is doing or how it
  went, the prompt it was given, its tool calls and messages, and the report it returned; a row you
  open stays open while you scroll (`@blerglab/chat` 0.10.11). A completed subagent keeps its
  completed status when late reports arrive, and a page of older history carries the last
  lifecycle events of the subagents it refers to (migration 049 indexes them), so its rows agree
  with Running now without skipping older messages. The runner records
  `subagent_started` / `subagent_update` / `subagent_done` for Claude Code's `Agent` calls and a
  `parent_call_id` on every subagent event, and passes `--forward-subagent-text` when the CLI's
  `--help` lists it, so the subagent's own text and thinking reach its row.
- A subagent the agent resumes after it finished (Claude Code's `SendMessage`) is a new run you can
  follow: a row of its own where it was resumed, titled as the first with a small "resumed", its
  tool calls and messages inside it, running until it reports back; the session panel's Running now
  marks it "resumed", History lists it as a row of its own, "(resumed)", and while the agent waits
  on it the working line says "· waiting on <its title>". A resumed background subagent counts as a
  running background task, as its launch did (kind "agent", under its SendMessage call's id). After
  a daemon restart the subagents it cut off can be resumed the same way; one the daemon no longer
  knows is titled "resumed agent". The daemon reports it as a new `subagent_started` with
  `resumed: true` and `resumed_from` (`@blerglab/chat` 0.10.5; a daemon reinstall brings it). A run
  Claude Code starts on its own, with no message from the agent, is not followed yet.
- Background tasks are visible. A Claude Code agent's background commands, Monitors and background
  subagents are recorded as `background_task_started` / `background_task_done` events and listed in
  the session panel's Running now (each task's kind, description, start time and a ticking clock,
  jumping to the call that started it, and ending also when the task ends while the session is
  idle, which the CLI reports only as a system `task_notification` line); the working line says
  "Waiting on N background tasks" when that is all the agent is doing, and the sidebar shows "⏱ N"
  on the session's card. Such a session does not read as idle: its card shows "⏱ N" in place of
  "Zz" and never counts as waiting for input, and the session header, the panel's Session facts (a
  Status row) and Insights say "working in background". The session list carries
  `background_tasks` and browsers get `session_background_changed`. Migration: runner 040.
- Codex agent sessions use the persistent app-server interface: mid-turn steering,
  acknowledged model/effort changes, native skill commands, runtime capabilities,
  structured tools, thinking/compaction, background work and nested subagent activity. A model
  change reconciles reasoning effort against the native model catalog; a completed subagent
  followed up again gets a new tracked run; MCP startup and login notifications refresh the
  capabilities; cached input tokens do not inflate total usage. A message rejected because a turn
  just finished stays queued and starts after its completion notification, keeping its order and
  delivery receipt, and child ownership, question completion and native RPC deadlines stay fenced
  across recovery, so a foreign child's events or a late question cannot change the active session.
- Codex native questions are enabled in ordinary unrestricted turns. Answers use the existing
  private ask flow; interruption and native completion close stale questions. Native subagent
  questions are relayed through the parent on the pinned CLI; choices use a text reply.
- Codex supports managed native plugin snapshots, sandboxed file/MCP sessions and scheduled
  crons. Engine-specific daemon capabilities keep unsupported older adapters from accepting them;
  plugin snapshot reads stay inside their scoped root, and sandbox guidance names the engine's
  credentials rather than assuming Claude configuration.
- New Codex terminal sessions retain private native history and resume their exact conversation
  after tmux loss, including sandbox replacement; missing history never replays the kickoff task,
  and history reads stay inside their scoped root.
- Managed Codex device login in Settings creates a dedicated Cluster connection. Core owns
  encrypted refresh tokens and coordinates rotation; pods and model probes receive access only.
- New account-owned Claude and Codex Cluster sessions retain their native conversation across
  pod replacement, with fresh authorization and grants, immutable claim identity and bounded
  cleanup, in which one claim that cannot be reclaimed does not block the others. A replacement pod
  whose previous native conversation storage is unavailable retains bounded transcript context.
- Accepted chat messages are durably queued before forwarding. Their pending bubbles
  survive session changes and reloads; updated daemons retain correlated receipts so
  reconnect redelivery does not execute known-consumed messages again.
- Owned Claude and Codex agent sessions can opt into **Resume when limit resets**, in the session
  panel's Settings section. The runner persists a one-shot continuation after an actual provider
  refusal, supports desktop and cluster sessions, cancels on manual intervention, and bounds
  retries. Unknown reset times require manual continuation. Updated desktop daemons are required;
  replacement pods validate retained owner authorization. Its locks share a bounded PostgreSQL
  pool, event ingestion waits briefly for a held lock instead of stalling the transcript until the
  daemon's resend, and sessions that never opted in take no lock on each message.
- A version line on every web app: the runner's sidebar foot, core's page footer and the board's
  top bar show the release version and commit, "v0.2.0 · 510d0c0" (the `VERSION` file and the git
  commit the image was built at). The runner's "Blerg was updated" banner now also notices a new
  commit under an unchanged version. Core's `GET /api/site`, the board's `GET /api/version` and the
  runner's `initial_state` carry new `release` and `commit` fields; the installers pass the commit
  to the image builds.
- Usage-limit meters in the runner's sidebar, under **Where sessions run**: one compact row per
  provider with a thin bar per window ("5h", "week"), the share used and, on hover, when it resets;
  amber from 70 %, red from 90 %. Only percentages and reset times leave the machine. Codex's limits
  are read by the desktop daemon from `codex app-server` at start, every 10 minutes and after each
  Codex turn. Claude's come from the account usage endpoint the CLI's own usage view reads, with
  the CLI's sign-in from `~/.claude/.credentials.json`, at start, every 10 minutes and about 5
  seconds after each Claude Code turn, and Claude also shows "capped · resets 14:00" while a call
  refused for its limit holds. The token is read from the CLI's file and never stored or sent
  anywhere but Anthropic; the refresh token is never read and the file never written. An expired or
  refused sign-in waits for the file to change; a 429 or 5xx backs off up to an hour. The endpoint
  and the file are the CLI's internals, not a documented API: if they change, the Claude meter goes
  empty. No Claude meter on macOS (the login is in the Keychain) or without the file; the daemon
  logs that once. On by default; `BLERG_RUNNER_CLAUDE_USAGE=0` turns it off. New daemon message
  `usage_limits`, `initial_state` `daemons[].usage_limits` and the browser event
  `daemon_limits_changed`; the desktop daemon needs a reinstall to report them.
- MCP tool changes are reviewed in place. When a server changes a tool a session pinned, or offers a
  new one, the tool is off in that session and the change is listed on the runner's Proposals page
  under *Tool changes*, with the old and new definition side by side; Accept re-pins the session
  without a restart, Keep off leaves it off and blocks that definition. Each MCP connection gains a
  **Definition changes** setting in core Settings (Ask me, the default, or Accept automatically for
  servers you run), with an audit of every flip; every re-pin, grant, decline and expiry is a pin
  event kept 180 days, listed per session and per connection. The agent is told a changed tool is
  unavailable and asked not to lobby for it; a definition that cannot be reviewed (hidden characters,
  over 256 KiB) is never re-pinned. Migrations: core 019, runner 039.
- Choose your own username. A local account picks it on the page that asks for a new password at
  first sign-in (prefilled with the random one, which you can keep), and can change it later under
  Settings → Account with the current password. Lowercase letters, digits, dots, dashes and
  underscores, 3–64 characters.
- Insights for one session: an **Insights** button in the session header opens that session's time
  (working, waiting, idle), work (turns, tool calls by tool, your messages, files, MCP calls,
  proposals), subagents, and tokens and estimated cost by model, main agent and subagents apart.
  It refreshes while the session is live. Tokens and cost are shown to the session's owner and to
  admins only, as on the Insights page. Route: `GET /api/sessions/{id}/insights`.
- Insights counts subagent usage: a Claude Code subagent's tokens are added under its own model
  (they are not counted as turns).

### Changed
- The runner's sidebar lists sessions as one list, no longer under repository headings, and a card
  no longer moves when its session changes state: the starred sessions first, then every other by
  start time, newest first, and nothing else. The status glyph is how the state shows; each card
  says its `org/repo` in small muted text before its chips (none for a session with no
  repository), truncated so a card is two lines at most. The "N sessions waiting for input" banner
  stays, a newly started session appears
  at the top, and starred, search and the phone's list work as before. The top of the sidebar is
  one row of compact pills: All, the cluster (with its running count) and each machine (with its
  status dot) both show where sessions run and filter the list (the separate focus row is gone);
  the usage meters sit beneath it. "Enable notifications" is a bell beside "+ New".
- The "Session idle" toast and push now mean the work is done. A session whose turn ends while its
  background subagents or background tasks still run says nothing until the last of them ends,
  then tells you once, "<session> is waiting for you." (it used to say "finished its turn" at the
  turn's end). The server looks at a waiting notice again every minute, so work that never reports
  its end or goes stale does not hold it, and after 30 minutes with only background tasks left (a
  dev server, a Monitor) it goes out anyway: "<session> is waiting for you (a background task is
  still running)." A new turn, a question or the session's end cancels the wait. Sessions nobody
  watches call no one: an unattended session, a cron's, one started with the operator key, and one
  a tool started with an agent token without asking for a person in the chat send no idle or
  waiting toast or push, and their questions are listed on the Messages page without a toast or
  push. A board's Discuss, board and Assist sessions have a person in them and keep their notices.
  The runner server now words the toast and sends it as a `session_notice` browser message; the
  browser no longer composes one from `session_state_changed`, and `message_created` carries
  `quiet` for a question not to toast. A page still on the old bundle keeps its own turn-end toasts
  until it reloads; a new page against an older runner server shows no idle or waiting toast (the
  server sends no `session_notice`).
- The New Session sheet's "I understand this runs unsandboxed on this machine…" checkbox now sits in
  the sheet's footer directly above Launch, so it is in view whenever it applies instead of at the
  foot of the Run column. On a wide screen the footer content sits under the Run column at that
  column's width, not across the whole sheet.
- The chat stops following new output as soon as you scroll up (with the wheel, a finger, the
  keyboard or the scrollbar), even within the newest card, and starts again only at the very end or
  when you send; a "↓ New output" pill at the bottom says more has arrived and takes you there.
  Before, a reader of a card taller than a phone screen counted as at the bottom and was pulled to
  the end by every event. Opening or switching chat sessions scrolls to the actual bottom,
  including timeline padding, and stays there as the panel finishes resizing.
- Cluster model caches are isolated by account, engine and agent image. Chat, launch and cron
  pickers share runtime targeting and bounded discovery with retry; Cluster requests cannot use
  a workstation's catalog. Older catalog rows remain compatible during rolling upgrades.
- The words "provider_subject" no longer reach a person: core's first-boot line reads
  `BLERG_BOOTSTRAP_ADMIN_PASSWORD=<password> (username: <username>; ...)` and `blerg-core users`
  prints `username=<username> password=<one-time>`. A script that parsed the old
  `provider_subject=` text needs updating; the API and form field names are unchanged.
- The start steps on the runner's Insights page are a box-and-whisker per step on one shared time
  axis: fastest to slowest, the middle half, the median and the 90th percentile, with each step's
  number of starts, its slowest and how many starts were slow (more than twice the median). A step
  with under five starts shows dots instead of a box. Each `startup.steps` row of
  `GET /api/insights` gains `min`, `p25`, `p75`, `max` and `slow`.
- In the working line, the "waiting on <tool>" clock stands apart from the turn's.
- `subagent_done` carries `is_error` when the subagent failed (a daemon reinstall brings it).
  `@blerglab/chat` 0.10.0: a `topic` event draws as a thin divider (`topicDividerText`, type
  `TopicPayload`); person bubbles, briefs, artifact cards and topic dividers carry `data-event-id`;
  `ChatReport` and `LiveState` gain `filesKey`.
- `@blerglab/chat` 0.9.0 (breaking for a host that used 0.8.1's `BackgroundChip` or
  `backgroundTasks: 'strip'`): `BackgroundChip` and the strip are removed and `ChatView`'s
  `backgroundTasks` takes `'none'` only; the package exports
  `useLiveState` (running subagents, open asks, the running task count, and the chat's `answer` and
  `openFiles`), `useChatReportStore`, `liveSubagents`, `openAsks`, `AskReply`, `scrollToEvent`, the
  subagent wording (`subagentTitle`, `subagentModel`, `subagentTokens`, `doingLine`,
  `subagentStatus`, `tokenLabel`) and the task pieces (`KindIcon`, `KIND_ICON`, `KIND_NAME`,
  `StatusGlyph`, `stateClass`, `elapsedOf`, `titleOf`, `clockLabel`, `truncateLabel`, `taskOk`,
  `taskFailed`), beside `useBackgroundTasks`, `useBackgroundLiveStore` and `scrollToCall`. A
  dismissed task now goes at once.
- `@blerglab/chat` 0.8.0: `Transport.send` takes optional `{clientId, answerTo}` and a transport
  may set `answersAsks`; `SubscribeHandlers` gains an optional `onAskClosed`; `DescribeUserMessage`
  gets a third argument, `{first}`; `UserMessagePayload`, `ErrorPayload`, `CompactionPayload` and
  `MessagingPayload` gain optional fields (`client_id`, `message_id`, `code`, `trigger`,
  `pre_tokens`); the package exports `askStates`, `userMessageShape`, `isRetryNotice`,
  `isTurnError`, `GAP_WAIT_MS` and the `AgentEventsGap` type. Existing transports and hosts need no
  change.
- `@blerglab/chat` 0.7.0: the event types gain `background_task_started` / `background_task_done`
  (`BackgroundTaskStartedPayload`, `BackgroundTaskDonePayload`), both hidden kinds, and the package
  exports `backgroundTasks`, `runningTasks` and `shownTasks`. `ChatView` users need no change.
- `@blerglab/chat` 0.6.0: `TimelineItem` gains a `subagent` variant (a `SubagentEntry` holding the
  subagent's own rows), `buildTimeline` and `timelineBuilder` take an optional `{groupTools}`, and
  the event types gain `AssistantThinkingPayload` and an optional `parent_call_id`. Code that
  switches over `TimelineItem` needs the new case; `ChatView` users need no change.
- `@blerglab/chat` 0.5.0: `useSession`'s `loadOlder()` fetches one older page rather than every
  page to the start, and the handle gains `loadingOlder`. `TranscriptProps` drops `now` and takes
  `since: number | null` (the working line keeps its own clock), and gains `sessionId`,
  `hasOlder` and `onLoadOlder`. `ChatView` users need no change.

### Fixed
- Kubernetes: the runner's files (published and attached files, agent-config bundles, mockups)
  now live on a persistent claim `blerg-runner-data` (`RUNNER_DATA_SIZE`, default `10Gi`;
  optional `RUNNER_DATA_STORAGE_CLASS`) instead of an `emptyDir` that every restart or deploy
  emptied while the Files tab kept listing the files. The runner Deployment now uses the
  `Recreate` strategy, so a deploy stops it for a few seconds. **Upgrading:** `deploy.sh` creates
  the claim empty — there is nothing to migrate; files published before the upgrade are already
  gone and stay listed as "no longer available".
- A file whose row survived but whose bytes did not answers `410 Gone`
  (`{"error":"file no longer available"}`) on view and download instead of a not-found, and the
  Files list and History's version chips mark it missing: drawn disabled with "no longer
  available", no View or Download, still deletable; the viewer says so instead of a load error
  (`@blerglab/chat` 0.10.12).
- Cluster pods receive separate current-launch runtime and messaging credentials instead of the
  operator daemon key. Session, repository and engine authority cannot be widened by pod metadata;
  replaced launches lose access, and runtime pods cannot be selected as workstation daemons. The
  pods' frame fence pool is sized from the session cap (two per session plus two, 6 to 32), and a
  frame whose fence times out is retried once before the pod is disconnected.
- Credential lookup failures and revoked launch authority refuse startup rather than selecting
  operator credentials. Configuration exports reject linked files and enforce read limits.
- Cluster model catalogs are cached in the database and restored at startup. The New session
  sheet remains usable while catalogs refresh in the background, including on the first load.
- Cluster Codex launches now load models and per-model effort levels from the cluster agent
  image with the launching account's credential. Catalog failures are visible in the New session
  sheet, and temporary probes cannot rotate the account's refresh token.
- A daemon whose Codex CLI is installed but reports no model list (logged out, a proxy-only host,
  a failing `codex debug models`) now says why in its log, once, with the tail of the CLI's stderr;
  before, that case was silent and indistinguishable from Codex not being installed.
- A session started while its daemon was busy, reconnecting or just reinstalled no longer runs with
  a revoked messaging token, every `blerg-runner` command refused. A spawn waits in the daemon's
  queue (clone, plugins) before the daemon lists it, and a heartbeat or reconnect in that window read
  the new session as lost: stopped, its messaging token revoked and its MCP gateway grants deleted,
  a completion webhook fired, then revived by its own start with the dead token still in its
  environment. The daemon now reports its queued spawns (`queued_sessions` in its hello and
  heartbeat) and the server leaves them alone however long they wait; an older daemon gets a
  two-minute grace instead. A running or starting session the daemon lists without a valid token is
  sent a new one (`session_token`, answered by `session_token_result`; a token the daemon could not
  store is revoked and tried again), which the daemon writes to
  `<state dir>/session-tokens/<session id>` and `blerg-runner` reads first. Only the messaging token
  comes back: MCP grants an earlier stop deleted stay deleted (restart such a session). Sessions
  already in this state heal at the next heartbeat once both the server and the daemon are updated —
  reinstall the daemon first or with the server; an older daemon is never sent a token. A dead
  token's 401 now says "token revoked or expired".
- On a phone, the first flick up in a busy session no longer snaps back to the bottom. The chat
  told your scroll from its own by the scroll position alone, and it scrolls to the end on almost
  every event, so your first upward scroll often read as its own and the next event pulled you down.
  It now reads you going up from the input itself (a wheel turned up, a finger moving down the
  screen, ArrowUp, PageUp, Home or Shift+Space) and does not move the view while your finger is on
  it or for 300 ms after (`@blerglab/chat` 0.10.1).
- The runner could show no sessions and no daemons, with no error, in a browser tab whose one
  sign-in bounce had failed (during a runner restart, say), and stayed that way through reloads and
  a logout and login: the "already tried" flag outlived the page load. It now lasts one page load,
  and a tab that can't get a token says "Couldn't sign you in · Retry" at the top; the board had the
  same flaw and has the same fix.
- Opening a session with a long history no longer makes the line above the composer flash while
  the history loads. The statuses the replay passes through are held, and the last one is applied
  when the replay is done; the working and waiting lines stay off until then, and a status that
  arrives afterwards shows as before (`@blerglab/chat` 0.10.3). In a session that is running, the
  line's "· waiting on <tool>" and "Waiting on N background tasks" no longer name calls and tasks
  from hours ago, nor change with each page: the suffix names only a call of the turn now running,
  and none while the start of that turn is not loaded; both keep what they last said of a complete
  transcript while a page of history is still coming in, the suffix until a new turn starts and the
  background words until the session is no longer live. The subagent cards, the tool rows' clocks
  and the session panel count calls in flight as before (`@blerglab/chat` 0.10.4).
- A foreground subagent's one-line report no longer ends in the CLI's agentId note.
- Opening a long, running session no longer makes "· waiting on Subagent · started earlier" come
  and go at the foot of the chat as each page of older history arrives. The page at the top edge of
  what is loaded often begins in the middle of a subagent's work from hours ago, without its start
  or its launch into the background; such a subagent reads as running, and it was given the time of
  its first loaded step as its start, so the working line named it, and dropped it again when the
  next page brought its launch. A subagent's start time is now only one that is loaded (its
  `subagent_started` or its call), and the working line names no subagent without one; the session
  panel shows such a subagent with no clock (`@blerglab/chat` 0.10.6).
- Opening a long session no longer plays its history back through the working line ("Waiting on
  2 background tasks", "Waiting on a background task", and back, several times). The chat asks for
  the first older page while it handles the newest page's end-of-replay message, and the runner's
  socket hands that same message to the handler the request has just added, which took it as its
  answer: the request ended at once with nothing, so nothing showed as loading; the real page then
  came in as live events one at a time, with the working line read from each half-loaded page; and
  its end asked for the next page, on through the whole history. An older page's request now
  takes only an answer whose events are older than it asked for (`@blerglab/chat` 0.10.7).
- The New session sheet opens at once from what it last knew and refreshes underneath, instead of
  saying "Loading..." until every list (above all the repository list, which could wait on GitHub)
  had answered. Hovering **+ New** warms it; `GET /api/repos` serves an expired list while it
  refreshes in the background and answers an unchanged one with `304`; `/api/me/credentials` and
  `/api/cluster/status` reuse short caches.
- An agent event whose text held a NUL character (a `cat` of a binary file, for example) could never be
  stored: the database refused it and the daemon re-sent it every 30 seconds for good. NULs are now
  replaced by U+FFFD on both sides.
- A Claude Code subagent's messages and tool calls were shown, and counted, as the main agent's,
  and a subagent's report could become a session's `last_assistant_message`. They are now
  attributed to the subagent, and the result reports only the main agent's text.
- Switching to a session puts the cursor in the message box, so writing is one click, not two (chat 0.5.1). The box never takes the cursor from another field you are typing in, and on a touch screen it stays unfocused so the keyboard does not pop up.
- Insights on a phone: the Insights page's startup and tokens-by-model tables become one stacked entry
  per row and the price editor scrolls sideways inside its section, so the page is never wider than
  the screen; a start step's tooltip opens on a tap (Esc, a second tap or a tap elsewhere closes it)
  and stays inside the screen; the session Insights dialog wraps the Tokens subtitle and long model
  names instead of cutting them short; and the session header's Insights button is an icon.
- `blerg-runner ask`, `update` and `note` were invisible in an agent session's chat. They now show:
  an ask as a card with its own reply box, an update and a note as system cards, and a card shows when
  the question was answered or closed. While an ask is open the message box says that what you send
  answers it, and a chat message sent then does answer it instead of queueing behind an agent that is
  blocked waiting (`@blerglab/chat` 0.8.0). The native agent's ask works the same way. The board's
  session chat has no reply box, but a message sent from it while the agent waits on an ask answers
  the ask, as does one sent through the runner API (`POST /api/runner/sessions/{id}/message`, MCP
  `send_message`) by a person or an API caller; a board's own automation never answers one.
- Text nobody typed is no longer drawn as yours: the "Session resumed after a restart" note is a
  system note, and a slash command's output (`/cost`, `/context`) is a system card. A slash skill
  you typed is recorded as what you typed, not as the skill's instructions. These need the desktop
  daemon reinstalled.
- A model API failure is an "API error" row with its reason rather than the model speaking, and can
  no longer become a session's result. An API retry is a muted line that leaves the working
  indicator running, a failed turn gives the engine's reason, and a Claude Code compaction reads
  "Conversation compacted (manual, 151k tokens before)" rather than "0 char summary". These need the
  desktop daemon reinstalled.
- A session's `last_assistant_message` is the last turn's answer only. It used to fall back to an
  earlier turn's text when the last turn ended in an error; it is now empty, with the failure in
  `error_reason`.
- A message you sent no longer shows twice, or holds up the ones after it, after a slash skill or
  a message the session dropped: each message carries an id the daemon echoes, and its bubble clears
  by that id. A message dropped because the session's queue was full says it was not delivered. A
  bubble that nothing ever records (the send went unanswered, or the session sat idle for 15 seconds
  after a later turn ended) says it may not have been delivered, then goes.
- A live chat no longer loses events when the browser falls behind. The server tells the browser
  what it had to drop and the chat fetches it again; it also fills any hole in the live transcript
  that does not close by itself within a second (`@blerglab/chat` 0.8.0).
- Agent events keep the time the daemon gave them (within ten minutes of the server's clock), not the
  moment they were stored, so a late event reads at the right time and live and reloaded views agree.
- Events stay in order. When the database refused one, later events of that session used to be
  stored first and the refused one landed after them, so a message could be drawn after its reply;
  the session's later events now wait, for at most two minutes, until the earlier one is stored.
- A re-sent event the server already had no longer repeats its side effects: it no longer moves the
  session's status or model again, sends another notification or stops the session again.
- The board folds only a session's first message (the brief it was started with) into the
  "session brief" line. A later message typed in the runner's app, an answer to the agent's
  question and a reply to its note are shown as the person's.
- Switching back in the runner's web app to a session visited before, once it had gathered a lot
  of history, was far slower than reloading the page: everything since the last visit was
  replayed one event at a time, each re-sorting and redrawing the whole transcript.
  `@blerglab/chat` 0.4.0 applies a replayed page in one update and appends live events without a
  re-sort; a session more than five full pages (about 1000 events) behind is loaded from its end,
  the way a reload does, rather than replayed forward.
- Opening a session with thousands of events in the runner's web app left the clicked row
  looking dead for seconds: the whole transcript was drawn at once, and all of its history was
  fetched in the background, redrawing it page by page. `@blerglab/chat` 0.5.0 draws the newest
  300 rows and reveals or fetches earlier ones as you scroll up (one older page is fetched up
  front, the rest on demand), paints the switch before drawing a long held transcript, and no
  longer redraws the transcript on each keystroke or each second of a running turn; the sidebar
  highlights the session you click at once.

## [0.1.2] - 2026-10-09

### Security

- `@blerglab/chat` renders PDFs with `pdfjs-dist` 6.4 (was 5.7): PDF.js below 6.2.108 could run
  JavaScript from a malicious PDF ([GHSA advisory](https://github.com/advisories?query=pdfjs-dist)),
  and the viewer shows files an agent published.

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

[Unreleased]: https://github.com/blerglab/blerg-ai/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/blerglab/blerg-ai/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/blerglab/blerg-ai/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/blerglab/blerg-ai/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/blerglab/blerg-ai/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/blerglab/blerg-ai/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/blerglab/blerg-ai/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/blerglab/blerg-ai/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/blerglab/blerg-ai/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/blerglab/blerg-ai/releases/tag/v0.1.0
