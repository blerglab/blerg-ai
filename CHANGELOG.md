# Changelog

All notable changes to Blerg are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[semantic versioning](https://semver.org/) with the pre-1.0 rule described in
[`CONTRIBUTING.md`](CONTRIBUTING.md#versions-and-releases): breaking changes may land in any
minor release and are called out here.

## [Unreleased]

## [0.5.2] - 2026-10-29

### Fixed

- Host workspace sessions no longer get stuck at the end of a turn while they run Go builds or tests. To copy a turn's commits safely, Blerg briefly freezes every process in the session; a freeze that landed while a program was starting another one (every Go `os/exec`, between `vfork` and `exec`) could never complete, so the turn's commits weren't brought back and the session refused to start anything again ("writer admission closed", "Codex app-server could not start", Stop doing nothing) until the daemon restarted, and a restart then ended it. The freeze now accounts for a thread waiting in `vfork` (it can run nothing while its child is stopped), a freeze that still can't be proven skips that turn's copy but leaves the session usable, and the helper allows 30 s instead of 5 (BLG-339).
- "Always allow" for child sessions works again for sessions you launch yourself with a board or an MCP connection: such launches are private, and privacy wrongly dropped your own approval rules. Sessions started by agents, crons, board runs or with restricted tools still ask for every child; a start whose origin can't be recorded is now refused instead of being treated as yours (BLG-338).

### Changed

- The upgrade notice shows a new release within about 15 minutes instead of up to 6 hours: core checks GitHub about every 10 minutes (with a little jitter), sending the last answer's ETag so an unchanged release costs nothing against GitHub's rate limit; runner and board cache the notice for 5 minutes.
- Thinking cards have the same Copy message button as replies (they look the same, so they copy the same) (`@blerglab/chat` 1.39.7).

## [0.5.1] - 2026-10-29

### Fixed

- Parallel builds no longer kill host workspace sessions or shells. Process containment counted threads against a limit of 128 per session and 32 per shell, so a Go build or test run on a many-core machine (about one thread per core per program) ended the whole session, the agent included ("exit status 137"), and left it unable to start again until the daemon restarted. The limits are now 4096 per session and 1024 per shell. A runaway that passes them still stops, with "blerg: process limit reached: … retry with less parallelism" shown with the exit or in the terminal, and the session can start its next turn (BLG-325).
- Charts draw: the chart viewer refused most real Vega-Lite ("Chart resources and custom expressions are disabled"), including themed and layered charts. It now draws theming (`config`, literal values from an allowlist) and composition (`layer`, `concat`, `hconcat`, `vconcat`, `facet`, `repeat`, within limits of 64 panels in all, counting facet cells, 32 marks and the canvas size). Data transforms are not available yet (`transform` is refused), and so is anything that runs code or loads from outside (`calculate`, `expr`, `signal`, `params`, `impute`, `url`, `href`, image marks); dates must be checked date objects or numbers where Vega-Lite would turn them into expressions, and a string is refused wherever Vega-Lite's schema takes a number or a fixed word. Field names that reach a prototype (`__proto__`, `constructor`, `prototype`), stacked areas that would fill in more than 100,000 points, and number formats asking for widths or precisions above 100 are refused too. Every refusal names the key and where it is. The session guide tells agents what a chart may use (BLG-329; `@blerglab/chat` 1.39.6).
- The desktop installer no longer asks about the Windows sign-in task (WSL) when it is already registered.
- The desktop install README: `git pull && ./blerg-up.sh` is the whole upgrade (it reinstalls the daemon too).

### Changed

- The "New output" pill's tooltip says why the chat stopped following new output (a wheel or trackpad, a finger, a key, or a scroll nothing explains), so an unexpected stop can be reported with its cause (BLG-327; `@blerglab/chat` 1.39.0).

## [0.5.0] - 2026-10-28

### Security

- Desktop enrollment requires explicit confirmation of the displayed hostname, OS user, connection time and server-observed source address; a changed connection requires a fresh confirmation, with confirmation and outcome audit records.
- Desktop credentials are delivered through daemon enrollment or private local export files; browser credential download routes are removed. Runner records authorized enrollment refusals alongside issuance and completion audits.

### Added

- Cluster workspaces (file explorer, owner shell, several repositories, sync at turn end) work without a special storage class: by default they use **standard** storage, a retained claim per session in the class of Blerg's own `blerg-runner-data` claim (`BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS`, set by `deploy.sh`), with no physical size limit per workspace. `deploy.sh` prints the mode on every deploy; the trade-off and how to remove retained claims are in [install/k8s/README.md](install/k8s/README.md#workspace-storage). The agent image must match the server: an older one fails standard sessions closed. Runner migration 095.
- The file explorer compacts loaded single-child folders into one row with selectable path segments, single-click expansion, stable keyboard focus and explicit file reveal that preserves search; the per-account **Compact folders** setting restores plain rows without losing expansion (#295).

- Working top-level sessions receive authenticated engine usage-limit notices at 80%, 90% and 95%, with turn-start catch-up, reset times, other engines’ headroom and running child counts. Administrators can change the install-wide notice thresholds in runner Settings. Sidebar usage meters turn amber at 80%, red at 90%, and pulse slowly at 95%; reduced motion uses a solid bold warning (#277; `@blerglab/chat` 1.34.0).
- A line wrap toggle in the code viewer and the diff (#265): a toolbar button, Alt+Z (Option+Z on a Mac) and a **Toggle line wrap** command in the Ctrl/Command+K palette. It starts on for prose (Markdown, plain text, reStructuredText, README/CHANGELOG/LICENSE-like names) and off for code, and your choice is kept per account for each of the two. A wrapped line keeps one line number at the top of its row, with its continuation rows blank in the gutter; in a diff, unified or side by side, the +/− mark and the row's color cover every wrapped row, and nothing scrolls sideways. The same toggle covers published plain-text files (now numbered like code) and an expanded code block from the chat. A file over 5,000 lines does not wrap: its toggle is off and disabled, and says why. A line number never wraps. Without a known account (a Board chat before sign-in) the choice is not stored on the device; the Board's chat now names the signed-in account for what the chat keeps locally. A diff review always wraps, so its threads fit beside the code (`@blerglab/chat` 1.31.0).
- The file explorer refreshes itself, quietly and in place, when the agent's turn ends and when you come back to the tab or window. It never polls, waits while you type in its search or while an expand, Load more or Refresh is reading, and a hidden explorer catches up when it is shown (#270).
- Image tabs have − / + / Fit / 100% controls, pointer-centered Ctrl/Command+wheel and touch pinch zoom (10%–1600%), drag/wheel/arrow panning, double-click Fit/100%, and focused-viewer keyboard shortcuts. File/version changes reset to Fit; split groups, focus mode, and image annotations keep their own coordinates (`@blerglab/chat` 1.32.0, #283).
- Consistent initial skeletons for session panel tabs, the workspace explorer, Files, shells and board card sheets. Slow first reads show content-shaped placeholders after 150 ms; cached and fast reads stay immediate, with static placeholders when reduced motion is preferred. Missing Files sources fall back after three seconds. Silent connected shells stop loading, and connection notices remain visible (#282).

- Tab focus mode maximizes the active Chat, file, diff or shell tab while retaining hidden split groups and running shells. Double-click a kept tab (a preview double-click only keeps it open), use Ctrl/Command+Shift+Enter (including inside a terminal), or use Focus mode in the palette; the tab bar provides an exit control. Exiting restores split sizes and pane states/widths, keeping the last-used tab group selected. Navigation and reload return to the normal layout (#262).

- **Settings → Account → Your desktops** in core: each of your desktops with its state (enrolled, needs enrolling, offline) and last seen, with Rename, Remove (confirmed; revokes its credential) and Enroll for a connected desktop that needs it. Administrators also see and can remove everyone's desktops and assign unassigned ones. **Add a desktop** issues a one-time, 15-minute code bound to you and shows one command for the new machine, `install/desktop/daemon/install.sh install --enroll <code>`: a new remote desktop enrolls after the 0.5 upgrade without an internal key or a connected session. Codes are stored hashed, single use, revocable, rate-limited and audited; the installer never prints or stores the code. Core reaches the runner on the new `BLERG_CORE_RUNNER_URL` (set in the desktop compose file and the Kubernetes manifests). Core migration 030, runner migration 090 (#193).
- Full board access includes column management: a session at Full adds, renames, moves and removes a board's columns and sets their properties (`blerg-runner column add|rename|set|move|rm`, `rm --move-to <column>` for a column with cards, or the `blerg_column_*` board tools). Lower levels are refused like any other write; a cron or a session launched with an agent token never manages columns. Removing a column with `--move-to` is a card move for every card on it: it needs card write access, passes the board's admission gate, and runs the same move automation (standing agents, review flow). Every column change is recorded on the board, in the same transaction, with who made it and the runner session it came from. A person who raises a token-launched session's board to Full in Permissions enables column management there at the next call.
- A board button in the session's tab bar, between Shell and the panel toggle, shown only when the session has a board: its launch board or any board it has access to. One board opens in a new tab; several open a small menu, launch board first. It stays on archived sessions and on phones (#243).
- **Attachments wait for space instead of failing** when a session's attachments are full, and survive reloads and session switches (even mid-upload); a waiting file belongs to the tab it was chosen in, other tabs show it as waiting there, and the next tab takes it over when that tab closes. One note under the waiting chips says you can delete older attachments to make room, with **Free up space…** (oldest attachments preselected) and, for an administrator, **Increase storage** (500 MiB per click, up to an install maximum, audited); anyone else is told to ask an admin. Send stays available for a message while files wait. Each attachment can be deleted from the session panel's Files tab; the session owner or an administrator may delete any file and a person their own attachments, also from a board's chat, where the runner checks the person the board names with Core on every request (an active account that can see the session; administrator status from the account's real role). Every file deletion is recorded with who made it and how (browser, runner contract, or the board on the person's behalf). The per-session attachment total and the maximum are new Core administrator settings (**Settings → Session storage**, audited); `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB` is the initial value. Waiting files leave the device at sign-out, on discard, when the session ends, or after seven days (`@blerglab/chat` 1.24.0, #231).
- A context meter beside the token chip on every Running now row (Main and started sessions, Claude and Codex) and in the Session section: how full the session's context window was at the end of its latest turn, with tokens against the model's window and the compaction size where the engine reports one. It turns amber above 85% and marks a turn in which the engine compacted; an engine that does not report its context (Hermes) shows no meter. Owner or administrator only, as the token chip (#191).

- Admins can manage audited plugin marketplace policy in Settings across runner servers and desktop daemons without restarts. Official Anthropic and OpenAI Codex sources ship enabled; disallowed plugins remain listed and flagged.
- Engine discovery after install: the daemon reports every known engine's status with the facts behind it (not found on its PATH, with where a usual install folder has it and the PATH it searched; not logged in; not set up; too old). The launch sheet lists every engine, the unavailable ones disabled with the reason and the exact fix, plus **Check again** (re-detect now) and **Re-detect PATH** (the daemon reads your login shell's PATH and keeps it, no restart). **Settings → Engines** shows the same per desktop, and Cluster engines say when they aren't connected. The installer gives the service your login-shell PATH (systemd and launchd), and `daemon/install.sh refresh-path` refreshes it without a rebuild (#237).
- Boards have unique short keys such as BLG and AD. Card chips and CLI output use BLG-205 across boards; renamed keys remain aliases. Existing boards can confirm an editable key suggestion in Board settings without changing their card numbers.

- Session tab groups support nested, resizable splits. Drag a tab to an edge for a side-by-side or stacked preview, or drop it in the center to move it into a group. Ctrl/Command+K provides split right/down, move-to-next-group and focus-next-group commands; tab shortcuts follow the focused group. Chat keeps its composer, layouts persist per person/session, and phones show one group while retaining the desktop layout. Code, raw and diff viewers can scroll past EOF by at least a third of their height (six-line minimum); rendered Markdown gets bottom breathing room (#218; `@blerglab/chat` 1.20.0).
- Shared file and folder icons across the explorer, tabs, quick open, Files panel and chat, with language colors that adapt to the theme. Bundles a curated MIT-licensed Seti SVG subset without network requests (`@blerglab/chat` 1.11.0, #213).

- Session workspace tabs for files, Markdown, diffs and commits: a pinned Chat tab, replaceable italic previews, Keep open, closing and reordering, per-person/session reload persistence, unread agent output, and phone back navigation. Explorer and shell toggles live on the bar; the header More actions menu is replaced by visible controls and palette commands (#205).

- Enroll desktops from Settings → Account → Your desktops through the current authenticated daemon socket, with private atomic credential storage, retry rotation, generation checks and audit records. Every person sees their desktop state and enrollment guidance; the installer supports private file imports (#186).

- Prepare an isolated cluster smoke with public repositories, fixture-backed sync, shell/explorer checks and scoped teardown; defer failure and measurement cases to #180 for 0.5.1. Cluster workspaces and owner shells are enabled after the real-pod happy-path smoke.
- Workspace package integration, authenticated desktop generation/root/result control and durable completion receipts (`@blerglab/chat` 0.92.0). Quick open uses bounded workspace search; owner shell actions appear only with an affirmative capability response. Availability follows the [per-runtime enablement policy](docs/workspace-integration.md).
- Guarded local workspace runtime with shared clones, persistent quota allocations, contained sync and settlement, recoverable lifecycle intents, child preparation, and generation-bound runtime/supervisor adapters. Capabilities reflect the verified backend available for each runtime.
- Cluster workspace children accept repeatable member `--repo` selections or `--all-repos`, show the chosen members for approval, and use independent retained volumes and branches. Pod explorer and owner-shell adapters use the existing authenticated connection with bounded, generation-fenced requests; workspace agent startup requires a writer-containment adapter.
- Cluster workspace preparation and settlement use independent repository credentials, a public projects root, retained volume identities, and per-member push/recovery results.
- Read-only workspace explorer with lazy directory pages, bounded name search, repository status badges, and runtime file viewing fenced by launch generation and desktop ownership. Protected metadata stays private, and queued viewer replies retain priority during explorer activity.
- Owner-shell runtime supervisors, exact-launch host consent, and bounded shared transport scheduling. Capabilities follow each runtime's enablement and verified lifecycle.
- Owner-shell policy controls, a separate authenticated shell channel, bounded grants and audit records, and a responsive shell dock with up to four panes. Shells require the enabled runtime's verified supervisor.
- Shared workspace runtime root bindings and supervisor lifecycle contracts, with generation-fenced replacement and results.
- Workspace selection with a checked desktop folder tree, hosted multi-select, bounded discovery, immutable parent-member child selection and repository lists on approval cards (`@blerglab/chat` 0.84.0). Runtime consumers enforce the selected backend's capability.
- Workspace v2 shared manifest/member/result contracts, capability and shell policy schemas, and additive runner migration 076; existing single-repository sessions retain scalar compatibility. Runtime and UI support are negotiated per backend.
- Warn during Kubernetes installation when the runner can read secrets outside Blerg's namespaces, explain that RBAC boundaries are not enforced, and show how to enable RBAC on MicroK8s; inconclusive checks also warn without blocking deployment (#182).
- **Runner command palette and quick open.** Ctrl/Command+K searches registered session and navigation actions; Ctrl/Command+P inside a session searches workspace files through the bounded explorer contract. Recent matches come first, `>` switches files to commands, and the header search button works on phones. Keyboard navigation, focus restoration and Board shortcut ownership are preserved. Workspace file search becomes available with the workspace explorer; shell actions register only when the shell feature is present. See [the command palette guide](docs/command-palette.md).
- Added a Chromium Mermaid corpus covering every diagram type in the bundled 12.2.0 release, syntax features, labels, themes, size limits, and explicit upstream limitations, with structural SVG baselines in the normal frontend test command.
- Install-wide administrator control for public release checks and notices, with copyable desktop and Kubernetes upgrade commands linked beside versions in core, board, and runner. Unreachable release checks quietly clear notices.
- **The session's plan keeps itself honest.** A coordinating session's pins and checklist no longer depend on the agent remembering them. A Blerg Board card the session moves into an in-progress column (through `blerg-runner ticket move`, the `blerg_board` tool or its board connection) is pinned, filled from the Board; with five pinned, the oldest pin whose card is done makes room, and when none is done the card is not pinned and the agent is told. A card it moves into a done column is unpinned, and every open checklist item naming it as `#N` (a whole token, on the session's one board) is ticked, the chat row saying "(Blerg: #N done)". A board-started session pins its card, and a session the agent starts whose title or prompt names `#N` is assigned to the agent's open item naming `#N`. Pinning a Board card with only its key and title now fills its description, column, type, priority and tags from the Board (read with the session's board access; what the agent sent stays where the Board has nothing). At the end of each turn the runner checks the plan against what the turn did and, at most once per turn and each fact once, holds facts and attaches a marked Blerg note to the next input (your last turn ran tools), never starting a turn (source `plan_drift`) naming what drifted and the commands that fix it: every pinned ticket done while it still works, a checklist all ticked or with nothing doing while it ran tools, a child session or background subagent started with no item, a pinned card closed on the board, a child that looks finished (stopped or idle, with nothing since its result was read or with its `wip/<id>` branch merged into the parent's branch (its own wip branch when sandboxed): discard it). Every rule is a fixed check, no model judges. **Nudge when the plan drifts** in the session panel turns the notes off per session (on by default, owner only); a cron session is nudged only when it keeps pins or a checklist (`GET`/`PUT /api/sessions/{id}/plan-nudges`; runner migration 075; `@blerglab/chat` 0.83.0). Reinstall the desktop daemon for the session guide's new line, the note's system marker (older daemons receive no note) and the merged-branch check (`read_commit` with `merged_child`; an older daemon never reports a branch merged).

- **A token chip on every Running now row.** Main and every started session (Claude, Codex or another engine) now carry the compact token count their subagents already had, and every chip, the subagents' included, has the same breakdown tooltip (`1.4M`, `180k`): what that session has used so far, from the recorded turn usage Insights reads. Its tooltip breaks it down: the total, this turn (or the last finished one), input against cached, the subagents' share and the estimated cost. Only the session's owner and an administrator see it, as in Insights (`usage` on `GET /api/sessions/{id}/live` and on each child; #233).

### Changed

- Bounded workspace storage (each workspace held to 11.25 GiB and 700000 inodes by its own filesystem) is now opt-in with `WORKSPACE_STORAGE_CLASS`; an empty value no longer refuses cluster workspaces. A session keeps the mode it started with.
- Selected repositories now appear at the public workspace root by name on desktop, sandbox and cluster runtimes; desktop selections offer an account-remembered **Keep folder structure** option to preserve paths below their deepest shared folder (#294).

- Board proposal idempotency keys may now be as short as 1 character (previously 8); callers should use unique keys such as UUIDs.
- Runner retry keys now consistently accept 1–128 ASCII characters from `[A-Za-z0-9_.:-]` across REST and MCP (whitespace is not allowed). Empty, repeated, or malformed values return 400. Idempotency scopes now encode each part unambiguously. No database migration is needed; a retry across this upgrade may repeat an operation because ambiguous legacy claims are not reused. Cron recovery reads its unambiguous legacy scope for one release to adopt interrupted starts. Start claims expire after 24 hours; pruning is opportunistic, and mark claims can remain until that sweep. Finish pending retries before upgrading when duplication matters.
- **Coming back to a session is instant.** Its chat, Files list, open file tabs and shells are drawn in the first paint instead of reloading: a long conversation no longer shows placeholder bubbles for a frame, the Files list and the contents of open file, published-file and commit tabs come from memory (a workspace file is read again behind it and updated in place only if it changed), and shell terminals stay alive, detached, so a shell shows what it showed at once and reconnects behind it without replaying it. Scroll positions come back too: the chat returns to its end and keeps following if you were following it, or to the exact message you were reading (new messages that arrived meanwhile do not move it); open file, diff and commit tabs, and each session panel tab (Files, History, Plan, Capabilities, Live) return where you left them. The transcript resumes where the kept one is complete, so an event missed just before leaving is fetched on return instead of skipped. What is kept is the signed-in account's only and in memory only: the last 10 sessions' transcripts and Files lists, up to 32 MiB of file contents (a file over 8 MiB is not kept), and the terminals of the last 3 sessions; a sign-out, a sign-in change (another account or another login of the same account), a desktop change, or a session that ends, is archived or discarded forgets it, shell scrollback included; a kept terminal never sends input while hidden, and an evicted one closes its connection (`@blerglab/chat` 1.33.0, #281).
- Desktop shells (host and local sandbox) no longer close after 5 minutes with no browser attached or 30 minutes without typing: by default they last until the session ends. Cluster shells keep the 5- and 30-minute limits. An administrator can set either limit for desktop and cluster shells in **Settings → Shells**, to its default, "until the session ends" or 10 minutes to 87,600 hours; a change reaches running shells, and a shell whose limit gets shorter keeps at least 10 minutes (never more than it already had). A shell tab ended by one of these limits says which one. A desktop that sleeps or loses its network still ends its shells (#276).
-- The public repository no longer includes `video/`, the maintainers' demo and capture harness for marketing videos (it stays in the private repository; the release script now excludes whole folders).
 **Coming back to a session is instant.** Its chat, Files list, open file tabs and shells are drawn in the first paint instead of reloading: a long conversation no longer shows placeholder bubbles for a frame, the Files list and the contents of open file, published-file and commit tabs come from memory (a workspace file is read again behind it and updated in place only if it changed), and shell terminals stay alive, detached, so a shell shows what it showed at once and reconnects behind it without replaying it. Scroll positions come back too: the chat returns to its end and keeps following if you were following it, or to the exact message you were reading (new messages that arrived meanwhile do not move it); open file, diff and commit tabs, and each session panel tab (Files, History, Plan, Capabilities, Live) return where you left them. The transcript resumes where the kept one is complete, so an event missed just before leaving is fetched on return instead of skipped. What is kept is the signed-in account's only and in memory only: the last 10 sessions' transcripts and Files lists, up to 32 MiB of file contents (a file over 8 MiB is not kept), and the terminals of the last 3 sessions; a sign-out, a sign-in change (another account or another login of the same account), a desktop change, or a session that ends, is archived or discarded forgets it, shell scrollback included; a kept terminal never sends input while hidden, and an evicted one closes its connection (`@blerglab/chat` 1.33.0, #281).
- Switching back to a session draws its explorer and session panel at once from memory instead of reloading them: the explorer's listings, open folders, Git status, scroll position and search, and the panel's permissions, options, History, Capabilities and plugin choices. Each part is then read again in the background, only what is on screen (open folders one read at a time, the open panel tab), and updates in place without moving expansion, scroll or focus; a failed read keeps what is shown with the usual notice. The model menu fills at once from the launch sheet's per-account model lists. The last 10 sessions viewed are kept, for the signed-in account only, in memory only (never file contents); a reply that started before a sign-in or desktop change, or before a newer read, is never kept; a session that ends, is archived or discarded is dropped, and a runtime change discards the explorer's cached pages. What is kept for a return holds at most 200 explorer listing pages per session, the folders on screen first. On return the explorer re-reads the first page of each open folder, one at a time (later pages stay as they were until you load more); an open folder whose listing was not kept is read and stays open. No folder is closed, and browsing itself is not limited. Refresh still reloads everything (#250).
- Shells are regular session tabs: reorder, split, restore, and close them beside chat and files, on desktop and phone. Terminals follow Blerg's theme and code font and refit after layout changes. Light-theme ANSI foregrounds keep at least 4.5:1 contrast; white and bright white remain paper colors for explicit color pairs. Focused terminals keep their control keys; only Ctrl+K/P on Windows/Linux and Command+K/P on macOS open search. Failed refreshes preserve shell layouts, and queued close requests survive reloads. Shell launch errors stay beside their control, and disconnected tabs always close (`@blerglab/chat` 1.27.0 adds `placement="anchor"` to `ConfirmPopover`, #248).
- Split view keeps every group's tabs on the top row, at the same height as a single group: the separate toggle row is gone. The explorer toggle sits at the left end of the first group's tab strip, and the shell, board and panel toggles at the right end of the top-right group's strip. Dragging a tab shows one translucent highlight, as in VS Code: the half of the group a split would give it, or the whole group for a move, and a marker between tabs on a tab strip; the edge outlines and labels are gone. The panel toggle's work-inside count uses the same robot face as the subagent rows in Running now (#245).
- **Stop moved to the session header, far from Send.** Stop (■, "Stop the agent's current turn") now sits immediately left of End session in the session header, shown only while the agent works and disabled while its interrupt is on its way; on a phone it ends the header row. The message box is back to Attach and Send. This reverses #233's Stop beside Send, which was too easy to hit by accident. `ChatReport` gains `busy` and `interrupt` for a host's Stop; `Composer` no longer takes `busy` or `onInterrupt`, and the board's card chat no longer shows a Stop (`@blerglab/chat` 1.26.0).
- **Esc no longer interrupts a running turn.** Interrupting is rare and Stop has its own button in the header; Esc stays free for closing menus and dialogs (`@blerglab/chat` 1.26.0).
- Desktop management moved from Runner to core's Settings → Account → Your desktops. Runner's Settings → Desktop ownership section and the full-width enrollment banner on sessions are removed; Runner Settings links to the new page and an affected session shows a one-line notice with an **Enroll in Settings** link (#193).
- Redesigned Ctrl+K with grouped Sessions, Cards, Files and Commands, removable scope chips, a small recent-session start view, relevance-first ranking and safe defaults for destructive actions. Search accessible Board cards by number or text and open them in the app.

- **Session panel order and Stop beside Send.** The panel's Session section lists what does not change first (where it runs, the engine, when it started), then what can (model and effort), then the options and Pause. Stop moved from its own row above the chat into the message box beside Send, shown only while the agent works; Esc still interrupts (#233; `@blerglab/chat` 1.18.0). The session header's "⏱ working in background, N tasks" chip is gone (the panel's Running now shows that work), and the panel toggle's single number became two small counts: the work inside the session (`⧉N`, subagents and background tasks) and child sessions at work (`↳N`), each hidden at zero and counted by the same rule as Running now. What needs you and Main at work are no longer counts on the toggle.

- File, artifact, diff and commit tabs use one compact toolbar with copyable paths, labeled icon actions and Raw/Rendered controls. File tabs use the full chat height; returning to Chat restores the mounted composer’s draft and focus. Keep and close controls live on the tab bar, including the active file on phones. Global tab navigation and close shortcuts appear in the command palette, with Alt bindings on Windows/Linux and Control on macOS (#217; `@blerglab/chat` 1.9.0).

- Compact session chrome moves model, effort, Rules and execution details into the Session panel, with shared runtime icons, palette model/effort commands, a single tab-bar panel toggle, and an open-file count on phones. Collapsed session tiles share the expanded list’s place and search selection (#214; `@blerglab/chat` 1.8.0).
- **Upgrade note (#220):** marketplace policy `*` now permits Codex-native `codex:<marketplace>` identities as well as GitHub `owner/repo` sources. Operators who want a GitHub-only boundary must replace `*` with explicit approved GitHub sources in Core, runner server and daemon policy. The default allowlist is unchanged.

- Make the new-session sheet compact: repository search and create/empty choices first, removable selection chips, recent repositories, keyboard navigation, and concise runtime and tool explanations (#209).
- Permission chips use concise effects across engines, including “No prompts,” “Asks first,” and “Read only.” Tooltips and accessible descriptions include access scope and native approval/sandbox terms in capability cards, launch sheets, and session headers; risky modes remain amber (#211).
- Desktop ownership and enrollment controls use the shared settings styles, including confirmation actions.

- Complete the shared pane system for the session panel and shell dock, including matching edge resizing, reset, collapse rails and per-person sizes. Combine the collapsed session list and explorer controls in one rail with project-tinted session tiles, state glyphs, child counts, keyboard navigation and shared hovercards (`@blerglab/chat` 1.0.4; #199).

- Use shared pane controls for the explorer and session list: inner-edge resizing, double-click reset, matching collapse chevrons, per-person widths, and icon rails that reopen with one click. The session-list rail keeps New and Notifications; Ctrl/Command+B toggles the list. The palette toggles panes and Focus mode restores the previous layout. Phones open the explorer from More actions (#198, #199).

- Enable workspace v2 and owner shells on verified Linux/WSL hosts, local sandboxes and cluster pods independently. Cluster multi-repository sessions passed the real-pod happy-path smoke; macOS host support remains off (#181). Missing or cross-runtime capabilities fail closed (`@blerglab/chat` 0.94.0).
- Desktop discovery and launch metadata now follow the exact desktop owner across APIs, websocket delivery, Board proxies and browser caches. Automatic desktop configuration sharing is retired; operators can explicitly supply a shared archive with `BLERG_RUNNER_SHARED_AGENT_CONFIG_BUNDLE`. See [workspace selection](docs/workspace-selection.md) for upgrade and runtime integration notes.
- **Child session limits reset with a rolling window.** Removed the lifetime start cap. Parents default to three children at once and 120 starts per rolling hour. Administrators can change both install-wide in core Settings; saved values apply at the next start request. Environment variables seed the defaults. Refusals name the limit and explain when another start is possible. Account limits continue to apply.

### Removed

- **Agent "standing rules".** The Rules button and list in the Session panel, the `/api/agent/rules` routes, the built-in agent tool `rule_propose` and the daemon's "Project rules" prompt section are gone: only Blerg's own built-in agent tools could propose a rule and none were in use. Runner migration 089 drops the `agent_rules` table; any rows in it are deleted. The standing rules that approve child-session starts are a different feature and are unchanged (#233).

### Fixed
- Terminal (tmux) sessions are removed: none can be started (the server says "Terminal sessions were removed in 0.5.0: start an agent session"), and only an existing one named as such opens in the terminal layout. A session whose kind is missing opens as an agent session; a cluster agent session showed the Terminal/Messages layout on a phone.
- Switching back to a session no longer parses every visible message's Markdown again: a text renders the same every time, so its result is reused (bounded, in memory only, forgotten on a sign-in change or sign-out). Redrawing 300 typical messages takes about a third of the time (`@blerglab/chat` 1.38.0, #306).
- The launch sheet no longer remembers the last Board: a remembered board was pre-selected for unrelated sessions and gave them Full access to it. Every launch starts with no board; give a session board access from its Permissions when you want it (#302).

- Board card list and search share filters, including repeatable column names or IDs, return all filtered list matches unless explicitly limited, reject unknown or repeated single-valued query parameters, and document filtering and paging in MCP and onboarding (#300).
- In the board's chat, the start card's spinner spins (the chat now defines its own `blerg-spin` animation; a test keeps every animation the chat uses defined in its own CSS), and the "Session ready" card names the model the session's turns report, marked "engine default", when no model was chosen and the configured model is not shown to the board (`@blerglab/chat` 1.37.0).
- The "Starting session" card paints only each step's icon by state; finished and waiting rows were filled green or boxed because a row shared its icon's state class. A desktop session seen from the board names its start steps ("Installing plugins", "Starting the agent") instead of "Preparing session" for every step; their details stay hidden from the board (`@blerglab/chat` 1.36.0).
- The host shell consent dialog shows a focus ring on its focused choice (Cancel) when it opens after a mouse click; Enter still answers the focused one.

- Reopening a cached explorer folder refreshes its expanded subtree and stale compact-folder segments; single-child expansion stops at deleted Git entries, matching the displayed rows (#295).
- **The board's chat shows the session again.** Board session and card chats stayed empty ("No response from the session") although the session answered: the runner names a session by its own id while the board names it by the board's, and the chat dropped every event as another session's. The chat now labels what a session's stream sends with the session it asked for; a session's "Runs on" is left out when the runner withholds it from the board instead of showing "none" (`@blerglab/chat` 1.35.0).

- Local workspace turn-end sync freezes every engine thread individually, preventing containment timeouts with persistent multithreaded engines; containment errors retain the failed helper check in daemon logs.
- A shell whose authorization expired or that the server no longer has (for example after Blerg was updated) now says "This shell ended." in its tab, with why, and a New shell button that starts a fresh shell in the same tab position, instead of an empty terminal; a connection that drops unexpectedly reconnects by itself (each try with deadlines, giving up after about a minute as "Disconnected." with Reconnect) behind the kept picture, while a shell another tab took over says "Controlled in another tab." and waits for Reconnect or Take over.
- Coming back to a session with a shell no longer flashes a "Connecting…" bar above the terminal while it reconnects behind its kept picture; the bar appears only when a reconnect takes longer than a second; what you type meanwhile is held (up to 4 KB) and sent once the shell is back, never after you leave.

- Workspace search budget/deadline and shell server-credit tests use virtual time and synchronization instead of host scheduling windows, keeping their behavior assertions deterministic under the race detector (#287).

- The session panel drawer keeps Tab and Shift+Tab inside when its final control is a model or effort selector, including when optional settings are unavailable. Firefox browser tests run files serially to preserve native keyboard activation inside the test iframe (#280).

- Usage-limit notices require daemon support, summarize threshold jumps once, ignore refusal-only caps, and tolerate reset-time jitter. Low usage re-arms notices even when reset times are unknown; critical meters and capped chips pulse consistently with their displayed percentages (#277).
- Usage-limit continuation matches model-specific weekly windows, preserves valid reset text on uncapped meter readings, handles midnight re-refusals, and backs off unknown-reset polling before handing it back for manual continuation after seven days (#279).

- Resume when limit resets recognizes Claude's session, weekly, model and usage-credit refusals and Codex's local retry dates. Continuations wait for the reset with a grace period and a small random delay; unreadable reset times fall back to the owning daemon's usage meter and request refreshes until the window resets. The option also works when enabled after the refusal. Expired usage windows disappear from budget and agent-facing responses while waiting for a fresh report. CLI budget resets include each window's date and timezone so a weekly reset days away does not look like tonight (#279; desktop daemon upgrade required).
- `blerg-runner ticket list` and `ticket search` follow all search pages by default; `--limit` caps total output with a clear notice. Lists append `[tags: a, b]` when tags exist, and combine tag and column filters across pages. Ticket lookup accepts UUIDs, `#N`, `N`, board keys and aliases, and titles with leading markdown or whitespace; search also recognizes keys. Board REST/MCP search offers opt-in cursor pages while retaining legacy array responses (#288).
- Shells survive a browser refresh with their screen: the runner server keeps each shell's last 256 KiB of output in memory, including output printed while no browser is attached, and replays it when a page reattaches, followed by live output with nothing missing or repeated. Terminal queries in the replayed output are not answered again. The scrollback is never stored, only the shell's own sign-in can read it, and it is freed when the shell ends (#268).
- The owner shell no longer closes itself on a burst of output (a long `ls` of missing paths, `cat` of a large file, `seq`, a noisy build). Output now slows down instead: every stage between the terminal and the browser waits when it is full or over its per-second budget, so the program blocks on its write as it would on a real terminal, and the output arrives complete and in order. Small writes are combined into larger frames, the desktop paces output to the server's budget, the shell helper only sends output the daemon has room for, and the desktop only sends output the server has room for, so a slow but working browser slows the program down rather than overflowing a queue. A browser that takes nothing for five seconds is disconnected (Reconnect resumes the still-running shell) rather than ending the shell. Takes effect after the desktop daemon is reinstalled and the runner server is updated; sandbox shells also need a rebuilt sandbox image (#249).
- Every tab scrolls inside its split group (#259). A diff, commit or other viewer that jumped to a place in its content (a commit chip opening a published diff at that commit) slid the whole tab up inside its group: the toolbar went out of view, the file list and hunks stopped short of the group's edge and the rest could not be reached. Groups and their tab panels now clip instead of hiding overflow, so only a viewer's own content scrolls. Docked HTML and PDF frames take the height they are given, so their document scrolls inside the frame rather than the viewer around it. A diff review in a short group keeps the patch in view: its bars stay on one row each and the selection help yields, where they had squeezed the patch to nothing; in the smallest group the viewer body scrolls the bars away rather than clipping the patch. The full-screen sizes of the diff and review viewers now apply only to the modal, so a docked viewer never takes the window's height whatever order the stylesheets load in (`@blerglab/chat` 1.31.0).
- The host-shell consent dialog works from the keyboard: Left and Right move between Cancel and Allow, Enter answers, and focus taken by something outside (such as a terminal tab) returns to the dialog. Cancel stays the default.
- Blerg's own notice rows in a session chat (a child's turn ended, review outcomes, plan drift) now render their text as inline Markdown with card and commit references, as chat text does: backticks and asterisks no longer show raw, and hashes and card numbers are clickable. The text can quote a child's output, so only the inline subset renders (code, bold, italic, links, references); HTML, images, headings, lists and code blocks do not (`@blerglab/chat` 1.30.0, #275).
- The session list's search box has the same visible space above it (from the header's separator line) and below it (to the first session card).
- Tab focus leaves dialog and review comment shortcuts with their owners, announces pane focus outside sessions, and avoids duplicate Chat entries in the phone kept-files menu (#262).
- Tab strips preserve manual scrolling when badges or labels change width or a drag is canceled, use the dominant wheel axis, and mark the current overflow-menu tab for screen readers (#266).

- Session tab strips scroll with wheel, trackpad, Shift+wheel and phone swipes, keep the active tab visible, and auto-scroll at drag edges. Pinned tools stay outside the strip; an All tabs menu lists each overflowing group, including previews. Phone tabs use a full-width row below the tools (#266).

- Leaving a session and coming back no longer ends its shell. Checking that a running shell is still allowed no longer re-attaches an older session's workspace on the desktop every second; that step is busy-limited across all sessions on a desktop, so opening another session could make the check time out and end the shell as "revoked". The shell is now checked against its recorded launch and the desktop's own description, as for newer sessions, and it still ends when the desktop, project folder, consent, runtime or sign-in really changes. Returning to the session reattaches the shell and shows the output it displayed before you left; that scrollback stays in the page's memory only, and is dropped when the shell ends, when you sign out or sign in again (as anyone, including a new sign-in of the same account), or when the desktop's access changes. The desktop rechecks the project folder link an older session was started through on every check, so pointing that link elsewhere ends a running shell. Terminal queries in the replayed output are not answered a second time, and leaving and returning quickly, a shell capability refresh while it reconnects, or leaving while a new shell is still opening no longer disconnects the shell you came back to. When you sign out while returning output is still being drawn, none of it reappears on the ended shell. Needs the desktop daemon update (#257).
- A busy desktop no longer makes shells, the explorer and file reads wait out a 2-second timeout. When its few workspace-attach or file-read slots are taken, the desktop daemon now answers "busy" at once (and logs refusals at most every 10 seconds) instead of dropping the request silently. Runner retries a busy attach with a short backoff; a desktop that stays busy is reported as retryable (the explorer and file viewer retry it, the shell button waits and asks again) and never ends or fences a running shell, which still ends if its authorization expires. Needs the desktop daemon update for the immediate answer (#267).
- Workspace file search and Ctrl/Command+P honor Git ignore rules, preserve tracked files and nested member roots below ignored folders, and skip common dependency and cache folders when Git is unavailable. Large repositories retain ignore filtering under load; deadlines preserve partial matches. Ignored entries remain browsable on host, sandbox, and cluster workspaces (#258).

- Card and commit references outside chat text (a diff's commit header, the review's commit list) render as reference links like in chat, not as bare browser buttons (`@blerglab/chat` 1.28.0).
- Reinstalling the desktop daemon now refreshes the local-sandbox session image when it was built from another commit (it is labeled `org.blerg.build-commit`), so sandbox shells and the sandbox file explorer get the trusted helpers of the installed build. Before, the image was built only when missing and kept its first helpers forever. Docker's layer cache keeps the update quick; a failed update keeps the existing image and says so. Sandboxes already running keep their image until the session's sandbox is recreated.
- The file explorer no longer gets stuck on "The workspace explorer is busy." The server runs one explorer read per person at a time; the explorer now waits and retries a refused read for up to about 8 seconds (as the file viewer already did) before showing the refusal.
- Selecting text in a shell reaches the end of the line: the app-wide letter-spacing (and kerning) no longer reach the terminal, which had squeezed glyphs off xterm's cell grid so selection and copy dropped the last character of a line ("/…/core" copied as "/…/cor").
- The empty Ctrl/Command+K palette no longer leads with ended, stopped and cron sessions. Inside a session it opens on a **This session** group (quick open a file, toggle the shell, switch model, set effort, open board, split right, insights; the first safe one highlighted), then at most three **Recent sessions**: live sessions you opened in this browser, most recent first, never crons, ended, stopped or archived ones or the session in view (before you have opened any, your own live sessions, latest first), then Commands. Outside a session the first group is left out. Search still finds every session (`s literary`). **Switch model…** and **Set effort…** fill the search with the choices (#246).
- Split view marks the focused group by the accent under its active tab, not a line across the top of its tab bar that the active tab interrupted; the other group's active tab keeps its background without the accent.
- Resize edges (session list, explorer, session panel, split view) behave the same: a resize cursor on hover and no highlight; the highlight shows only when an edge has keyboard focus.
- Shell typing no longer lags: input goes out at once while it fits a sliding one-second budget (12 KiB, 64 frames, under the server's 16 KiB and 128), and only bursts past it queue, coalesced per shell. Before, every keystroke waited for its own 260 ms slot (#247).
- Host and sandbox shells open on a desktop whose clock is a second or two behind the server's (common under WSL). A shell grant may be issued up to 5 seconds ahead of the desktop's clock; expiry stays strict, so a grant never lasts more than 35 seconds of real time. A desktop more than 5 seconds behind fails with its own message, "This desktop's clock is off by more than a few seconds. Sync it (on WSL: sudo hwclock -s), then try again.", instead of "Shell authorization changed."; an expired grant still says the authorization expired (#242).
- One failed shell start no longer locks the session's shell until the runner restarts. A start the desktop refused and then proved cleaned up waits 3 seconds before the next try; an uncertain failure blocks that connection until the desktop reconnects, and a reconnect clears it, also for sessions started before workspaces. A blocked open says so ("paused after a failed start") instead of "unavailable" (#242).
- The launch sheet can select a host folder that contains no Git repository: it becomes an empty-folder workspace with a short note, instead of failing with "The selection could not be checked." Sandbox launches explain that they need a repository (#242).
- Board sessions and card runs start on a 0.5 desktop again: the runner resolves the repository a board (or any API caller) names into a fresh workspace selection on the chosen desktop and runtime, as the launch sheet does, instead of refusing with "Select an existing repository using fresh source references". The repository is matched from fresh discovery only, pinned desktop or not (a name, or an exact origin for owner/name); several matches, a folder whose origin is another repository, or a listing too long to be complete are refused rather than guessed. A repository the desktop does not have is refused with what to do, and every runner refusal reaches the board as a plain message with Open board settings and Try again, never the raw HTTP exchange.
- A restart row that left a session paused now reads **Paused after a restart** with a **Resume** button that continues the session (it sends what the person's next message would, as the removed Resume all did), instead of the agent-facing "Host restarted · paused — check the interrupted command". Its details say a command was cut off and may not have finished, and keep the cut-off list; once resumed (or after any later message) the row reads **Resumed**. Continued rows are unchanged (`@blerglab/chat` 1.26.0).
- On a phone, a card's session page runs its chat edge to edge, so the message box keeps its width now that Stop sits beside Send (#233).

- A chat file refusal for a desktop that is not yours now points to Settings → Account → Your desktops instead of the removed Runner Desktop ownership section (`@blerglab/chat` 1.25.0, #193).

- Board sessions start again on installs with a cluster when the board's repo is a bare folder name (such as `blerg`): with no "Where it runs" choice, the board runs it in the sandbox of the one connected desktop that has that folder instead of sending it to the cluster, which can't clone a folder name. A board start nobody placed never lands on a desktop's host: a desktop without a sandbox is refused with what to do, and `BLERG_BOARD_RUNNER_RUNTIME=daemon` no longer puts unplaced board starts on the host (they run in the sandbox); the host is used only where the board's owner chose and acknowledged it in Where it runs. A runner whose discovery lists no targets gets no unplaced desktop start without a sandbox: the start is refused until a target is chosen (or goes to the sandbox, or the cluster, per the install's default). No desktop, several desktops or a host-only desktop is refused before anything starts, with what to do (open Blerg on that desktop, choose Where it runs, or resolve the repo under Repos), and the board session drawer offers Open board settings and Try again. A runner refusal reaches the person in the runner's own words, never as a raw HTTP 422.
- Board keys start with a letter. Cached key discovery keeps unknown acronym references from reading cards; board chat resolves individual card numbers, including archived cards. Key controls respect board admin scope, and server suggestions avoid reserved aliases (#206).

- A running session no longer re-renders the whole chat view and the composer every second: the elapsed-time clock ticks only in the working line that shows it (`@blerglab/chat` 1.23.0, #238).
- Prepare Cluster Codex snapshots before pod delivery, redact credentials contained in snapshot diagnostics, and explain how to migrate existing snapshot sessions to a new managed session. Keep immediate model-catalog refreshes current.
- Cluster Codex explains when an imported login needs a dedicated managed connection and links directly to reconnect. Connection failures retain redacted engine details in the transcript and log; cluster workspace refusals no longer request desktop ownership.

- Split views keep workspace controls at the outer edges, suspend hidden transcripts, and clear views when switching terminal sessions. Groups have distinct accessible names and focus announcements; tab moves retain scroll offsets and insert at the drop position. Plain HTTP splits work without secure-context UUIDs, and divider persistence is throttled (#218).
- Orphaned sessions can ask for input when their parent is archived, discarded or missing. Expanded children waiting on a live parent use a muted parent-wait glyph; nested cards keep repositories that differ from their parent. Bound child-event batch time separately from keystrokes (#234).
- Keep typing responsive while child sessions stream: sidebar cards and collapsed tiles subscribe to their own content and descendant glyphs, without repainting sibling sessions, the transcript, viewers or composer. Keep the Cluster count live when a child stops, and run strict typing performance tests after other browser workers exit. Keep host/type icons first on sidebar card detail rows, with optional task counts last, including on phones (#234).
- Reset marketplace policy revision tracking when Core policy identity changes, keep revoked plugins removable, and log rejected policies and failed policy writes. Installer guidance now reflects saved Settings authority and pod bootstrap (#223).

- Bootstrap current plugin marketplace policy on every cluster pod start before allowing plugin installs (#223).

- Retain the latest marketplace policy across outages and restarts, reject stale or replayed revisions, and apply live updates to cluster startup and session plugin installs (#223).
- Buttons no longer appear with the browser's default look: "Retry models" in the session panel, Retry, Save and Cancel on board cards and ticket drafts, cluster settings, crons, the notifications list, Needs you, Insights and the workspace tree, History's card chips, the board name in a card sheet and Answer in a chat question (`@blerglab/chat` 1.16.0). A static guard in every web app's tests now fails on a button that is not provably styled (selectors must match it at rest, inline values must be known, unresolved spreads fail closed), naming the file, the line and the approved classes; reviewed exceptions are a reasoned comment on the line above; see [`CONTRIBUTING.md`](CONTRIBUTING.md#what-we-look-for-in-a-change) (#229).
- Keep idle sessions quiet after planned restarts, let paused sessions expand their interrupted-work list, preserve person-authored recovery text, and continue updates monitored by read-only installer wait loops (#235).

- Continue the sole session that started a successful daemon update using same-user daemon observations, including background installers and make invocations; show one compact restart row and notify parents when children pause (#235, #232).
- Keep shell consent and cluster Pause out of default palette selection and recency ranking. Require explicit safety classifications for registered actions and protect the real End, Discard, Pause and shell commands with regressions (#226).
- **Long-running sessions keep accepting files.** The lifetime caps of 100 attachments and 50 published files per session are gone; the size totals (500 MB of attachments, 1 GiB published, 100 MB per file) still bound storage. A file that cannot be attached now says why next to its name (`@blerglab/chat` 1.10.0).
- Preserve terminal shell failure diagnostics when buffered output encounters invalidated consent, while continuing to reject stale terminal bytes (#224).

- Owner shells preserve the exact bootstrap transport stage and report failures after startup in the daemon log and shell popover, including authorization expiry and rejected control requests (#224).

- Owner shell startup reports the failed launch stage with redacted diagnostics and specific recovery guidance; disposable systemd user-service coverage exercises host consent, PTY input, resize and verified cleanup.

- Preserve named-folder icon colors in the workspace picker in light and dark themes, and show shared file icons in the active phone file tab (#213).
- Runner Board palette commands open a separate tab while keeping the conversation open. The Session panel groups engine controls, runtime, timing and options compactly; Explorer labels branches and detached commits. Sidebar cards and tiles share descendant work aggregation; only a root session’s own question or pause asks the person, and fully idle trees keep Zz (#225, #234). Collapsing a parent hides all children, including working and waiting children, with the same saved choice in tile previews.
- Session attachments and review state now use a private per-session folder outside repositories, with absolute fetch paths and local Git excludes for explicit repository destinations. Legacy review state remains readable. Ending or discarding a session clears its private downloads; a bounded startup sweep checks old folders against server state before cleanup and continues through retained folders over time. Cluster pods provide separate writable ephemeral attachment storage outside the workspace.
- Continue the requesting session after a verified protected daemon update when its only interrupted call is the exact recorded update invocation; preserve the pause for other destructive work (#221).

- Allow one CSS pixel of browser rounding in session action popover viewport checks while retaining all edge and interaction assertions (#214).
- Keep long Rules actions visible in the Session panel, restore the selected shell by identity, and return keyboard focus after closing the panel. Use shared confirmations and retryable errors for board/column deletion and replies, and describe shell startup failures without misidentifying the failed executable (#214).

- Restore each session’s last composer, file, shell or explorer focus without raising the phone keyboard; contain History and long session rows within their panes. Use one anchored confirmation/error surface and one composer-safe toast style. Preserve person-owned decisions in chat and History, and retain redacted shell-start diagnostics with safe UI causes (#214).
- Restore workspace explorer status for repositories with linked worktrees while retaining bounded metadata checks and checkout isolation (#219).
- Install Codex marketplace plugins from the plugin browser for the current session or always on for the account, through Codex's native installer and the existing approval and marketplace policy. Accept native sources in the by-name and sandbox fallback forms, and show disabled install reasons below their buttons, including Codex account restrictions (#220).
- Claim palette and session quick-open shortcuts before browser defaults, including repeated keydowns and while a modal blocks opening. Preserve review comment link shortcuts (`@blerglab/chat` 1.7.0), recognize dialogs that restore inherited visibility, and ignore retained dialogs hidden by ancestor styles, React Activity or accessibility masks. Install and cache Chromium and Firefox for runner browser tests.
- Stabilize runner regression coverage for sandbox token recovery with separate temporary roots, MCP event pagination during cluster startup, and explorer preferences when browser storage is disabled.

- Preserve at least 320px for Chat beside the docked explorer and session panel on desktop, with horizontal scrolling confined to the session columns at narrow widths (#215).

- Restore the transcript’s reading position and bottom-following state when returning from a commit viewer, including messages received while Chat is hidden. Keep keyboard focus on History pagination controls at the first and last windows (`@blerglab/chat` 1.5.0, #215).

- Keep long-session typing responsive after visiting commit viewers, keep the composer below independently scrolling content, contain wide code and tables, page History topics and strokes, and open History cards in the in-app ticket modal. Large commits start collapsed with lazily rendered, capped file bodies (`@blerglab/chat` 1.5.0, #215).

- **A long-running session keeps its checklist.** The checklist no longer stops for good after 500 changes. It is limited to 60 changes a minute instead, which still stops an agent stuck in a loop.
- Bound Raw diff highlighting to the file viewer's character and line limits, keeping larger diffs complete as plain text with an explanatory note. Correct the documented Insights action destinations (#212).
- Link every card in slash-separated references such as `#210/#211`, while leaving paths and URL fragments unlinked.
- Keep the desktop Insights action in the header and remove its duplicate from the session panel.
- Close file, diff, commit and preview tabs with middle-click, preserving Chat, focus and phone history.
- Show safe, specific repository status refusals in the explorer and preserve them through desktop status replies.
- Highlight Go manifests, Dockerfile and Makefile name variants, and provide per-tab Raw/Rendered source views using Code colors (`@blerglab/chat` 1.4.0).
- Reserve composer palette shortcuts before local editing bindings consume them.
- Keep repository branches collapsible during launch-sheet search, restore their prior expansion when search clears, and expose checked tree items with coordinated keyboard focus. Launch controls use shared settings styles (#209).
- Recover host Codex sessions with their previous plugins if a replacement engine cannot load a plugin change, stop native helpers during planned restarts, and refresh the open plugin browser when changes finish (#210).
- Host Codex sessions apply approved plugin additions and removals between turns by restarting the engine and resuming the same conversation. Always-on installation remains available without live loading, with clear next-session wording and a launch action (#210).
- Keep host-shell consent interactive and in control of focus and Escape when resizing between desktop docks and phone sheets; canceling grants no shell access (#205).
- Session tabs refresh Files after saving workspace copies, preserve keyboard focus during navigation and closing, skip closed views in phone history, and label terminal tabpanels (#205).
- Refresh chat and checklist references when the signed-in account or reference reader changes (#184).
- Preserve specific cleanup and identity refusal reasons when opening persisted owner shells, and make overflowing host-consent details keyboard-scrollable on short screens (#203).
- Host shells can reopen with fresh consent after a helper exits in a resumed session, once containment cleanup is proved. Daemon logs and shell errors explain runtime refusals, and host consent uses Blerg's modal styling and keyboard controls.


- Keep session tile previews open while keyboard focus moves through their controls; Escape closes the preview and returns focus to its tile (#199).
- Preserve saved shell preferences through Focus mode, retain session-state glyphs beside unread tile dots, and show the full shared session card in tile hovercards (#199).
- Keep shell errors and app notifications in a dismissible, scrollable stack above the composer and focused inputs, bounded by the phone keyboard viewport. Keep shell connection notices above their terminal input (#204).
- Keep the open command palette stable during background session refreshes, retaining provider results, the highlighted command and scroll position (#202).
- Open the command palette and quick open with Ctrl/Command+K and Ctrl/Command+P from the chat composer and ordinary text fields, preventing browser shortcuts while preserving terminal, code-editor and IME input (#196).
- Scope Focus mode snapshots to the current person, session and pane instance; layout restoration only reveals existing shells. Ctrl/Command+B works from ordinary text fields while respecting terminal, editor and modal ownership. Both session-list disclosure controls expose their expanded state and controlled pane (#198, #199).
- Restore the workspace explorer and owner shell when older desktop sessions reconnect or resume. Explorer Refresh retries registration and reports the specific problem, progress, and result. Attachment checks the requesting owner first, preserves existing Git and file readers, and accepts unchanged project directory aliases (#197).
- Explain workspace-file ownership refusals with sign-in, enrollment or session-owner guidance, including access changes during reads. Cover compatibility and enrolled desktops, benign dot folders and ignored files (`@blerglab/chat` 0.99.0; #187).
- Keep open chats mounted while their desktop enrolls or refreshes inventory; refresh source access without replaying the transcript, and keep cached source and replay state bound to the viewer's desktop access across reconnects (#195).
- Failed personal desktop connections now record a disconnected status with reconnect guidance after binding, including welcome delivery and recovery-policy failures (#185).
- Desktop refusal status now preserves enrollment guidance through reconnect recovery (#185).
- Installer preflight accepts pending personal enrollment credentials after a lost acknowledgment.
- Size agent-pod Go, Vitest, Node, Playwright and Make budgets from downward-API CPU and memory limits, with a resource-capped suite smoke and session guidance (#192).
- Explain terminated agent pods, including OOM memory limits and recovery guidance, keep disconnected sessions resumable, and label failed pods for monitoring exclusions without deleting retained work. Keep diagnostics in owner views, show other viewers a generic stopped notice, and clear death notices in connected browsers when resume begins (#190).
- Preserve optional runner ConfigMap settings across Kubernetes redeploys, support `.env` overrides for every optional runner setting, and default fresh Kubernetes installs to 8Gi of memory. Existing installs without an explicit memory limit keep 4Gi until the operator opts in to a higher limit (#189).
- Preserve upgrade-era shared desktops automatically through 0.5, including launching, children and file references, with per-person scope and fresh shared desktops refused. Compatibility is recorded and warned about until enrollment or 0.6. Ownership file refusals explain enrollment (`@blerglab/chat` 0.96.0; #186).
- Record authenticated desktop refusals and socket closures immediately, with a safe reason, independently of session recovery grace (#185).
- Card references use `board:<board-id>#<number>` for chips, hover previews, and in-session opening across boards; bare numbers follow the session's created or pinned board. The CLI and agent guide use the same form. Updated `@blerglab/chat` to 1.2.0.
- Shared settings controls keep buttons, fields, and accessible toggles styled across core, runner, and board, with regression guards and phone/4K browser coverage.
- Improve recovery of interrupted cluster launches and keep launch and owner-shell diagnostics private.
- Wait for engine readiness before opening the cluster smoke owner shell, and report safe shell refusal codes in HTTP responses, audits and smoke stage receipts.
- Retry optimistic quota conflicts when creating cluster launch PVCs, Secrets and Jobs, with bounded backoff and verified recovery after lost responses. Preserve foreign-object refusals and report exhausted conflicts at their launch stage.
- Preserve safe Kubernetes reason classes in cluster Job submission failures and smoke receipts, and distinguish local scheduling refusals from API conflicts.
- Admit prepared cluster workspaces through the pod's exact reader authority before agent startup. Record launch stages and protected bootstrap failures, and collect sanitized stage diagnostics on failed cluster smoke runs without waiting for Job completion.
- Continue the isolated cluster acceptance smoke when impersonated authorization probes identify AlwaysAllow, reporting limited success instead of plain acceptance. Keep RBAC exec grants fatal and record safe exception diagnostics for setup, smoke and collection failures.
- Bound deep macOS explorer listings within the existing deadline by retaining and revalidating directory ancestry across entries. Refuse unsupported macOS host workspace sessions and owner shells explicitly before allocation or journaling; advertise no macOS host workspace or host-shell capability. Linux hosts and sandbox sessions on a Mac retain their existing behavior. See [platform support](docs/workspace-integration.md#macos-host-support-in-050).
- Compute and negotiate workspace and shell capabilities from installed runtime adapters and verified helpers; runtime support is negotiated independently.
- Flush workspace explorer and session result responses under bounded desktop authority, and refresh the terminal privacy audit registry.
- Repair workspace VM launches and host lifecycle recovery, keep stop cleanup authorized after read access ends, and reconcile completion receipts durably.
- Preserve workspace member provenance in commit reads and session outcomes, and bound desktop inventory delivery.
- Move keyboard focus into expanded nested explorer folders with Right Arrow.
- Announce explorer file statuses, retain readable names in deeply nested trees, and associate the search combobox with keyboard-selectable results.
- Keep workspace files visible above the phone explorer, preserve chat width and refresh focus, and remove the conflicting explorer shortcut. Refine the dock with shared file/folder icons, Git status letters and folder rollups, one search box, and edge/drag collapse (`@blerglab/chat` 0.93.0).
- Move the workspace explorer into a resizable left dock with per-person layout preferences, a header toggle and palette command, keyboard tree navigation, and a full-screen phone sheet. The Files panel keeps published artifacts.
- Coordinate local workspace synchronization, owner-shell cleanup and reader lifetimes; preserve runtime authority through synchronization and cleanup.
- Workspace explorer and file viewing now use registered desktop runtime readers,
  including in-container reads for existing single-repository sandbox sessions.
- Wire authorized cluster workspace selections and children to retained pod launches, with protected controller storage, contained engine and shell processes, and in-pod explorer services.
- Discard stale desktop source caches when ownership changes, keep historical session sources private after reassignment, and bound discovery response writes.
- Apply desktop ownership consistently to workspace source readers and browser state, and preserve validated repository selections through launch and child handoff.
- Store frontend test caches in an ignored checkout-local cache directory instead of a retired path, keeping the documentation link check green (#179).
- Keep the session header title readable on phones after adding search, match the search icon to the other controls, and verify the complete control set at phone and 4K widths (#178).
- Preserve Stop and its kill fallback before a written Claude turn starts, even without an acknowledgment or at full request capacity, retire rejected interrupt requests, and recover abandoned request capacity without disabling Stop (#177).
- Keep outstanding steering interrupts distinct across turn results, join sandbox watchers in tests, and require three daemon race-detector passes in CI and release verification (#177).
- Authorize History marks before reading their bodies, clean CLI text before truncating it, compare index columns when rebuilding, refresh closed questions, and retry keyed live marks without recording duplicates (#121).
- Announce unfiltered board counts only when clearing a filter, and make the phone column panel focusable (#126).
- Expand ascending ticket ranges of up to ten cards into individual chat chips after every card is confirmed; unresolved or larger ranges stay text (#160, `@blerglab/chat` 0.93.0).
- Make hovercard list tests wait for input delivery and rendering before advancing their manual clock and asserting row content (#161).
- Command palette shortcuts preserve terminal and editable input and leave existing modals in control of focus and Esc. Command rows show only real shortcuts and omit switching to the current session.
- Fixed Mermaid git graph XML spacing, journey and event modeling labels, quadrant radius styling, kanban assignment metadata, inline style statements, multiline shape metadata, and flowchart class assignments with Unicode, multiple classes and punctuation, while every following style is still validated. Mermaid error SVGs now show a reason instead of appearing as successful diagrams (`@blerglab/chat` 0.91.0).
- Open project README and other authorized local file references directly from chat, preserving absolute paths until the session daemon resolves them against its launch working directory. Symlinked launch folders accept both recorded and canonical absolute paths, and extensionless `/workspace/` inline references remain clickable. Sandbox references use the session clone, and document links and images use the returned relative path (`@blerglab/chat` 0.90.0; update the runner server and desktop daemon together).
- WSL2 daemon recovery after Windows sign-in: setup offers an opt-in Windows logon keep-alive task, checks user lingering for distro boot, and keeps retrying transient service startup failures. Startup/task diagnostics, idempotent removal, and supported configurations are documented. Profile/log paths preserve PowerShell wildcard characters, checkout paths preserve Unicode quotation marks, and Windows task lookup or removal failures keep the daemon service installed with repair guidance.
- Tighten release notice authorization, release integrity, cache freshness, and upgrade source selection; keep deployment details and custom build labels private.
- Local sandbox workspaces preserve live sessions across reconnects, keep interrupted finalizations pending until durable acknowledgement, and recover stopped quota storage without discarding saved files.
- Local sandbox workspaces preserve saved file bytes and retry interrupted runtime and result delivery transactions. Desktop installation includes the contained Linux workspace worker for Docker Desktop, and checkout entry quotas cover file aliases.
- Cluster workspace stop and discard clean up terminal and failed launches without changing their recorded outcomes; incomplete cleanup remains retryable.
- Cluster workspace cleanup retries retain acknowledged outcomes, copy scheduling rotates across members, and refused trees cannot hide session budget exhaustion.
- Cluster workspace replacement and stop preserve final member outcomes. Pod validation precedes credential allocation; unchanged pushes retry without freezing, copy cancellation still thaws writers, and one repository’s traversal refusal no longer blocks unrelated pushes.

### Upgrade notes

- **Confirm existing board keys after upgrading.** A board admin confirms each existing board's suggested key (for example `BLG` for a board named Blerg) in Board settings → Board key. See the [one-time confirmation steps](board/README.md#one-time-upgrade-confirmation).
- **Enroll desktop daemons for a person.** Desktop credentials now come from core and bind one person, desktop ID and ownership revision. Re-run the desktop installer after updating core and runner; an existing desktop keeps its ID and is assigned to the installing account (`BLERG_RUNNER_INSTALLING_ACCOUNT_ID`), or the first enabled admin. On a fresh install, create your account first and rerun `daemon/install.sh install`. Upgrade-era shared desktops retain automatic compatibility through 0.5; enroll them through the session banner or ownership Settings before 0.6. Fresh desktops require personal credentials. Unrelated unknown desktops remain administrator assignment inventory. Runner migration 077 and core migration 026 add the ownership and credential records.
- **Known limitations:** the full cluster failure and measurement campaign is deferred to 0.5.1 (#180); native macOS host workspaces and owner shells are unavailable (#181). Kubernetes installations with AlwaysAllow authorization do not enforce the runner RBAC boundary (#182): the installer warns, and a successful isolated smoke reports `pass-rbac-not-enforced` rather than full acceptance. Enable RBAC separately; the warning does not change cluster workspace enablement.

## [0.4.5] - 2026-10-22

### Upgrade notes
- **Reinstall the desktop daemon** (`install/desktop/daemon/install.sh install`) for the sandbox homes below and for `blerg-runner` under Claude Code's own sandbox. A sandboxed session can no longer renew its engine's sign-in: keep Claude Code or Codex signed in on the machine (running it there refreshes the sign-in the sessions get). Sandboxed sessions started by an older daemon keep their old mounts until they end; stop them before you reinstall. If you ran Local sandbox sessions before this release, check `~/.claude/settings.json`, `~/.claude/settings.local.json`, `~/.claude/hooks`, `~/.claude/plugins`, `~/.claude/skills`, `~/.claude/agents`, `~/.claude/commands`, `~/.claude.json` (its `mcpServers`), `~/.codex/config.toml` and `~/.hermes/config.yaml` for anything you did not put there.
- The same daemon reinstall gives `blerg-runner` the dismissed exit code (4) for `ask`, `review wait` and `review status`; an older CLI reads a dismissed question as an empty answer and a dismissed review as a failure (exit 1).
- The same daemon reinstall gives `blerg-runner` in your sessions the new review-thread rules (your own resolve or reject stands unless the agent reopens the thread, your replies show in `blerg-runner review list`, and `review reply <id> reopen`). Until then an older CLI still lets an agent's answer at the same revision change a thread you settled, in the agent's own view; the browser keeps your status either way.

### Added
- **Dismiss in Needs you and the bell.** A review request or a question you answered some other way (in the chat, say) no longer waits forever: **Dismiss** closes it at once, with an **Undo** toast for 5 seconds before anything is sent. A review request closes as `dismissed` (neither approved nor changes requested) and a question as dismissed (not answered); the agent is told in a Blerg system message ("The person dismissed the review request for <file>; they may have answered in the chat instead."), may ask again, and `blerg-runner ask`, `review wait` and `review status` exit **4** for a dismissal. Only the person can dismiss (the session's owner for a review request; whoever may answer for a question); the agent still closes its own questions with `ask --resolve`. History shows a dismissed item as "dismissed by you", and the bell now lists your pending review requests too (and drops one you decide or dismiss at once). A dismissal reaches the agent the way an answer does: queued while its desktop is away and sent again after a server restart. A question someone else dismissed says "Dismissed by another person". Dismiss is never offered on an engine's command approval (allow or deny those), and a dismissal still in its Undo time is sent when you close the tab or switch away. An engine that waited on the question (Codex, Hermes, native sessions) reads that it was dismissed, not a bare close. Runner migration `074` adds the `dismissed` state (`@blerglab/chat` 0.82.0).
- **Card numbers in the checklist link to their cards and show when the plan is stale.** A checklist item that names a card ("#155 Dismiss in Needs you"), on Plan and on Live's checklist line, gets the chat's treatment: once the card is confirmed on the session's one board, "#155" is an inline link with the card's hovercard, and a click opens the ticket dialog. Beside the item, the card's state glyph (✓ ◐ ○) shows when it differs from the item's own, for example an unticked item whose card is done. Until confirmed the text is drawn as written; a number with no such card stays text; the lookups are batched and kept, and the card states follow the live reads the pins use. Live's checklist line is no longer one button: a click anywhere still opens Plan, and its head is the keyboard's control. Every Plan item has a small chevron beside its title (aria-expanded) that shows the whole item from the keyboard; a click on the words still does.
- **Ask a session to make a board.** `blerg-runner board create "<name>" [--repo R ...] [--template T | --columns A,B,C] [--description D] [--access read|file|full]` proposes a new Board; nothing is created until you approve it. The proposal waits in Needs you, on Proposals and as a card in the chat, showing the name, description, repositories (the first is primary), columns or template, and the access the session asks for on the new board (Full by default). When you approve you can lower that access or give none, never raise it. Approving creates the board with your own authority (only an administrator can, signed in; Core issues a 30-second credential for that one approved board, and Board uses it once), then gives the proposing session, and no other, access to the board at the approved level, capped for a started session by its parent. Rejection or expiry creates nothing, approving twice creates one board, and "Allow for this session" never applies. Each proposal carries an idempotency key (`--idempotency-key` repeats one whose reply was lost). Sessions still never hold board administration; cron, restricted and board-started sessions cannot propose a board. Names, columns and descriptions with bidi or invisible format characters are refused. The session guide tells agents to use it when asked to "make a board for X" (`@blerglab/chat` 0.77.0: board proposals in the chat card; board migration 028).
- **New board picks its repositories.** The Board app's New board form lists the repositories your connected desktops have checked out (`owner/name` when the desktops agree on the folder's remote, else the folder name), several allowed, with a filter and a field to type one no desktop has; the first chosen is the board's primary repository. They are set as the board is created, so there is no trip to the board's settings afterwards. Templates work as before.
- **"+ Add" in the Capabilities tab opens a plugin browser, and MCP servers that need a sign-in get a button for it.** The browser lists every plugin of the marketplaces the session's engine knows (Claude Code and Codex), read by the session's daemon from the engine's own files: name, description, version, author, marketplace, and whether it is installed and enabled. It has a search, a marketplace filter, and an Install per plugin, which goes through the same owner-approved path as before (this session only, or always on). Adding a marketplace (a GitHub `owner/repo` or `https://github.com/owner/repo` URL the install allows) and entering a plugin by name stay as secondary actions; an engine with no catalog, or a sandboxed session, gets the add-by-name form. In the MCP servers list, a claude.ai connector that needs a sign-in has **Connect**, which opens claude.ai's connector settings in a new tab (restart the session after connecting); a server from your own settings (Claude Code's user scope, or Codex's global `config.toml`), in a session on your machine, has **Sign in**: Blerg asks the engine to start its own OAuth sign-in, shows the address the link goes to (in plain ASCII, with a warning when it uses international characters), and asks you to confirm the server, where it was defined and the address before opening it. "Your own settings" means your engine configuration on that machine, which an agent running there could also edit: only continue for a server you added. The sign-in returns to the engine on the machine that runs the session, the token stays where the engine keeps it, and the row turns connected when it is done. A server the project, a plugin or the session defined gets no link: sign in to it from a terminal. Sandboxed sessions say sign-in there is coming. Marketplaces added here are shared by everyone using that machine's engine. Only the session's owner can browse, add or sign in. A session's custom variables can no longer set `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` or `XDG_CONFIG_HOME`. Needs a daemon reinstall.
- **Card numbers and commit hashes in chat messages are chips once confirmed.** In a message, the agent's or a person's, `#147` links to the card when the session's board is known (its one board) and the card exists there, and a 7 to 40 digit hash links when the session's repository confirms it is a commit (a read only the session's owner may make; others see plain text). A confirmed reference has the same hovercard as elsewhere, and a click opens the ticket dialog or the commit viewer. Until confirmed the text is drawn exactly as written, so nothing moves; "#1 priority" with no card #1, a hex word that is no commit, anything in a fenced code block and anything inside a link stay text, and an inline code span links only when it holds nothing but a hash. Lookups are batched (one pass every 60 ms, each reference asked once per session and kept) (`@blerglab/chat` 0.75.0).
- **A README's HTML renders as on GitHub.** Raw HTML in a markdown file (a centered banner, badges, a table of videos, a screenshot grid) used to show as literal tags; in the file viewer, the folder-file viewer and review mode it now renders through GitHub's allowlist of elements and attributes, sanitized by DOMPurify and rebuilt by React, with anything else (scripts, event handlers, styles, frames, forms, `javascript:` and `data:` links) dropped. In the viewer, a markdown file from the session's folder shows its relative images, read from the folder beside the file with the session's own access; a published file shows their alt text. Remote images (badges) are never fetched by themselves: each shows its alt text and **Load image**. Folder images load two at a time and retry while the server is busy, up to 50 per file before **Load images**. Relative links in a folder file open that file inside Blerg (in a published file they stay text), `#` links scroll to the heading, and external links open in a new tab. Chat messages, approvals, tickets, board cards and threads keep HTML as literal text, so nothing in them can be hidden from you; in review mode a document whose page does not show every word (an HTML comment, a closed section) says so, with **Show source**. Workspace-file reads are limited per person (two at a time, four for the server), so one person's README cannot keep someone else's viewer waiting (`@blerglab/chat` 0.70.0).
- **A commit chip with no published diff is no longer a dead end.** Hovering it reads the commit from the session's own workspace, through the session's daemon: its message, author, date and files changed with +/− counts. Clicking it opens the commit's diff, generated on demand, in Blerg's diff viewer (a diff over 2 MB says it is too large); on the Plan tab a chip naming one commit does the same, and a History chip whose commit a published diff holds opens that diff. When the commit cannot be read (the session ended, its daemon is offline, the sha is unknown there, or you cannot read the session), the hovercard shows the short sha and the repository's name and nothing else, and a click says the commit can't be read right now. Only the session's owner or an administrator can read its commits, as with the session's workspace files (a commit by sha reaches the repository's whole history); anyone else sees the short sha (`GET /api/sessions/{id}/commits/{sha}` and `/diff`; see the runner README). Git reads with hardened settings (no command, network, submodule recursion or file named by the repository's configuration) in the workspace the session is bound to, refusing a `.git` that points elsewhere; a sandboxed session's commits are read from the repository its clone came from, where each turn's end brings them back. Reinstall the desktop daemon for it; an older daemon degrades to the short sha (`@blerglab/chat` 0.69.0: `TransportFiles.commit`/`commitDiff`, `CommitViewer`, `CommitViewerHost`, `useCommitOpener`, `HovercardSources.repo`).
- **The agent can reopen a thread you resolved or rejected, to ask you something.** `blerg-runner review reply <id> reopen "<why>"` opens a resolved or rejected thread again, one revision up, with the agent's reason shown as its own ("Reopened by the agent") in the thread, and in the agent's next review message. The thread is open again in the counts and `review list`, an approval's relayed comments list it with the reason, and the agent's question shows under Needs you as one plain question for the threads it reopened at once ("The agent reopened a review thread…"), answered in the threads or in the question (the agent closes it once you answer in the threads); it is not a file review request, so nothing you press there reads as approving the file. A reopened thread stays open until you act on it: until you reply, resolve or reject it again, the agent's own `done` or `declined` is kept in the thread's history, and a Submit about other threads does not count as acting on it. One reopen per thread at a time. A reason must say something, in at most 2,000 characters (invisible and unassigned characters do not count); one malformed entry in the agent's review copy is left out (the review says so) instead of hiding the whole copy, and a reopen whose reason cannot be shown still keeps the thread open for you. The agent still cannot resolve or reject over your status, add requests, or write anything as yours (`@blerglab/chat` 0.56.0).
- **Live lists every pinned ticket, and Running now says which card each worker is on.** A coordinating session that pinned several cards (`blerg-runner pin` keeps up to 5) now sees all of them at the top of the side panel's Live tab, one compact line each, newest first: the key, the title (cut), the status as the board has it now and ● when it was read live from the board, ↳ when a worker is on it. Three show, **+N more** the rest in place; a line opens the ticket. Live's plan line is the list alone (the ticket chip it carried is one of these lines). A Blerg Board card that moves to a terminal column (Done) is shown done (✓, muted; Live and Plan read the pins again every minute and when you come back to the tab) and is dropped from Live at the next pin, once the agent pins or updates any ticket after the card moved to Done (a card whose move is not in its history stays); the Plan tab keeps listing every pin, done ones marked. It is not unpinned on the server: the runner reads a card only with the viewing person's own board permissions, so nothing may change the agent's pins when a card moves. A Running now row (a subagent or a started session) gets one more line when its worker is on a card, `↳ Build History fold · #122`: from its checklist assignment when the item names a pinned ticket, or from the started session's own pin, which the parent shows on the child's row in Running now and in Started sessions (its key opens that session) (the live state's `children[].ticket`; a child's pin refreshes its parent's panel). The item opens it on Plan, the key opens the ticket.
- **Hovercards: a preview of every ticket, file and commit chip on hover**, like GitHub's. Rest the pointer on a chip for 400 ms (or reach it with the keyboard) and a small card opens above the page, never clipped by the panel and kept on screen: a ticket shows its number, title, column, priority, type, tags, the assignee or the worker on it and the start of its description (a Blerg Board card as the board has it, read with your permissions); a file its thumbnail (an image), first ten lines (markdown, text, code), a small render (Mermaid and Vega-Lite, with the viewer's own sandbox) or its files with +/− counts (a diff), then its name, version, size, who published it and when, and whether a review waits on it; a commit its message, author and files changed, read from the published diff that holds it. It stays open while the pointer moves into it and closes on leave or Esc. It is on the chips of Live, Plan, the chat, History, Running now and Files, only on a device with a mouse or trackpad: on a touch screen a tap opens the thing as before. Nothing is read for a chip you never hover, and what is read is kept. Turn them off under **Settings → Chat → Hover previews**; the delay and the fields each kind shows are set in one place (`@blerglab/chat` 0.54.0: `Hover`, `HovercardProvider`, `HOVERCARD`, `setHovercardsEnabled`, `VisualThumb`).
- **Board on a phone: one column at a time, and search and filters in the toolbar.** On a phone (up to 640 px) the Board shows one column under sticky tabs with counts ("Inbox 37 · Ready 0 · In progress 3 · Done 64"); a tab or a sideways swipe moves between columns (vertical scrolling is untouched), the tab strip scrolls sideways when the columns do not fit, and the last column you picked is remembered per board on that device. The toolbar's 🔍 (or `/`, or Ctrl/⌘-K, which do nothing while a card, a dialog or a popover is open) opens into a search field the height of the toolbar's buttons (on a phone, a full-width field under the column tabs) that narrows the board's cards in place as you type: every word must match the title or a tag (accents ignored), `#123` matches that card number and `#ops` a tag starting with "ops". A list under the field still offers what the 🔍 search did: this board's matching cards at once, more from the server (body text, archived cards), every board's cards after Backspace twice on an empty field (or the scope chip; searching every board leaves this board unfiltered), and the boards by name; ↑/↓ choose and Enter opens the first card (a board only when you chose it). Opening a hit clears the search text, so the board is not left cut down to it. **Filter** beside it opens Type, Priority and Tag choices (a popover; a bottom sheet on a phone), each with how many cards it would leave, tags sorted by count with their own search; choices in one group are alternatives, groups narrow each other. Only what is on shows, as small removable chips beside it ("search: auth ×", "type: bug ×", "0.4.5 ×") with **Clear**, so with nothing on the toolbar is a single row. Screen readers hear how many cards the filter left; the column tabs follow the tabs pattern (arrow keys, Home, End). While a filter is on, counts read shown/total ("Inbox 4/37") and a column the filter empties says "No match among N". The filter is kept per board for the browser tab's session. Dragging a card in a filtered column drops it where it appears among the shown cards. Desktop columns still cannot be collapsed. Other pages keep the 🔍 search dialog.
- **Reply to, resolve and reject review threads yourself.** In a diff review (Files and the Conversation), every thread now has **Reply**, and an open one **Resolve** and **Reject**, like a reviewer on GitHub; a settled thread keeps **Reopen**. They go with your next **Submit**, and the agent's review message says what you did ("the person replied", "Resolved by the person"); `blerg-runner review list` shows your newest reply, and an approval's relayed comments carry it. Replying to a thread the agent resolved or rejected opens it again, as on GitHub, so the agent sees it in `review list`, the counts and an approval's comments. Each thread has a clock (its revision) that you raise, and that the agent raises only to reopen a settled thread: an agent answer from an older revision cannot change the thread's status, and your own resolve or reject stands until you change it or the agent reopens the thread with a question. An agent answer sent after your resolve is kept and shown in the thread without changing its status. The agent's copy of a review is trusted only for the agent's own answers and reopens: it can never claim that you resolved something or add words or a request in your name. Your words reach the agent quoted (`> `), without invisible characters. A pending Reply, Resolve, Reject or Reopen can be undone before Submit. The thread shows who did what and when, a settled bar says who settled it, and the Conversation's timeline lists your replies, resolves and rejects per review. Review files gain `settledBy` and a per-comment `action`; older files read as before (`@blerglab/chat` 0.49.0).
- **A Conversation view for diff reviews, like a pull request's page.** A diff review now has two tabs, **Conversation** and **Files** (the diff as before), and opens on the Conversation as a pull request does (on Files when opened at a commit). The Conversation starts with the body, the agent's ask from its review request (a quiet placeholder when there is none), then a timeline: each version of the diff ("v2 · 3 files · +12 −4", with commit chips that open the diff at that commit inside Blerg), each review request and its decision (approved, changes requested), each review you submitted with the threads it started, and each thread with the few lines of code it is attached to, its file and line, its replies and its resolved or rejected state, marked **Outdated** when a later version changed that code (**Not in vN** when its file is not in the newest version). Clicking a thread's code or its `file:line` jumps to it in Files. While the Conversation is open the review-request bar keeps its decision buttons but not the ask, which is the body. The versions and reviews are read once per review; a read that fails is retried, one that never finishes times out, and the Conversation then says so with a Retry. Files names a thread whose file left the newest diff as the Conversation does ("not in v2"). It is built from what is already stored (the diff's versions, your submitted review files, the review requests): no migration. Markdown and image reviews do not have it yet. `ReviewRequestContext` is now exported for hosts that show the viewer outside the chat (`@blerglab/chat` 0.41.0).
- A video pipeline for short feature videos (`video/`, see `video/README.md`): a seeded demo install with its own ports, databases and daemon, and a scripted agent so captures need no model calls (`video/demo/demo.sh up|reset|down`); Playwright capture scripts for desktop and phone with honest speed-up markers; and a spec-driven assembly into 16:9 and 9:16 videos with burned-in captions, optional voice-over and music. The first short covers in-session diffs and code review.
- Board settings resolve a board repo given as a desktop folder name (for example `app`) to the `owner/name` the cluster clones, once, with a person present: under **Where it runs → Repos for the cluster**, pick the desktop this folder lives on, see `app → owner/app` from that desktop's git remote, and save it. The board and its cards then name `owner/app` (in the same place: a board's first repo stays its sessions' working directory, and a card's primary stays primary), and cluster card runs clone it; desktop runs still find the folder. Only the picked desktop is asked. A folder with no remote stays a desktop-only repo ("this folder has no remote, so the cluster can't clone it; run it on a desktop"), and the board does not offer the cluster for it: saving the cluster as the board's target, or running such a card there, is refused with the reason. Existing boards with folder-name repos show the same prompt. Resolving is for a signed-in board admin who owns the board's automation token. The runner's launch options report a bare folder's remote for the one desktop the request names (`targets[].repo_origin`) and whether the cluster resolves bare names itself (`resolves_bare_repos`).
- **Renaming sessions from the agent.** A parent renames its own children with `blerg-runner session rename <child> "<title>"`, so a reused child no longer keeps a stale title, and an agent renames its own session with `blerg-runner title "<title>"`. A parent can rename its own children only. Each rename, the ones you make in the app included, is a row in the session's History ("Renamed by the parent: Worker → Fix the login flow"). The runner builds the index History reads them with in the background after it starts (`CREATE INDEX CONCURRENTLY`, no migration), so event writes are never held while it builds. Titles are stored as one line without control, bidi or zero-width characters, and the CLI quotes them.
- **History is a compact feed of the session's major strokes.** One line each, newest first, under the topics as section headers: ◆ your decisions and ◇ every decision the agent wrote (its own, which you can overrule, and its account of yours), ? what was asked of you and how it was resolved (questions, approvals of child sessions, proposals, review requests and rounds; a run of Hermes command approvals is one "N command approvals" line), ✓ milestones (merged, deployed, released, a topic done with commits) and ⚠ findings that changed the plan; filter chips with counts; the newest 100 lines first, with **Show earlier** (the server sends the newest 300); the time jumps to the moment in the conversation; card and commit chips stay in Blerg. The open topic shows the checklist's progress ("7 of 17") instead of a duration, and a topic's counts are small glyphs. Questions, approvals, proposals, reviews and topics come from what Blerg already records (`GET /api/sessions/{id}/topics` now has `strokes`; the runner builds the index it reads them with in the background, no migration). Decisions, milestones and findings are marked by the agent with the new `blerg-runner mark decision|milestone|finding "<text>" [--by person|agent] [--commit <sha>] [--card <n>] [--at <time>]` (`POST /api/sessions/{id}/marks`), or imported with `blerg-runner mark --import <file.jsonl>` (every line is checked before anything is sent; a refused chunk names the lines recorded before it; importing again records nothing new, and duplicates never count toward a session's 500 marks); the session guide says when to mark. A mark is plain text, cleaned like a title and capped at 240 characters; only the session's own agent, the daemon or the session's owner can mark it, and a child cannot mark its parent. A live mark is a thin row in the chat (`@blerglab/chat` 0.48.0).

### Changed
- **A pinned ticket is one quiet row in the chat.** "📌 Pinned #157 ◐ <title> · <time>" at bubble width replaces the full-width card and its Open ticket button: the number is the card chip with its hovercard, the glyph says the card's state (✓ ◐ ○, as in the panel), another tracker's ticket shows its key, title and source. Consecutive pins fold into one row ("📌 Pinned #157, #158"), unpins read "📌 Unpinned #127 (done)", and a refresh says what changed. Opening a ticket from the chat now shows the same live dialog as the panel: the Board card as the board has it, description included, in the two-column layout, instead of the pinned copy (`@blerglab/chat` 0.81.0).
- **A pinned Board card opens laid out like the Board's own card sheet.** The session's ticket dialog for a live Blerg Board card now has two columns: the description across the whole main column (no narrow text beside empty space), then the trail and its comments; beside it the card's fields, each a quiet label over its value: Status, Type, Priority, Tags, Repos (the first marked primary), the Worker on it (↳, a subagent or started session) and where it was filed from. Status is the board's columns as pills, the card's own in the selected blue: one click on another moves the card (through the session's board access, as before); the unstyled column select and its Move button are gone. "Open in Board" is in the header only (the inline link is gone). In a narrow dialog (a phone, or any under 640 px) the fields fold under the title, one line each, above the description.
- **Agents publish mockups instead of offering a browser.** The session guide for every engine now says: for a mockup, prototype, interactive demo or rendered page, write one self-contained HTML file and publish it, and the person opens it in Blerg. It also says a file in the session's folder can be opened without publishing by naming its path.
- **End session asks in a small confirmation instead of arming the ✕.** Pressing the session header's ✕ (or a starting session's **Stop session**) no longer turns it red for a second press: it opens "End this session?" anchored to the button, saying what is kept where the session runs: "The agent stops. The conversation and files are kept." on this machine; on a local sandbox, that its container is removed and the conversation, files and commits are kept; on a cluster pod, that the pod is removed and work in it that was not pushed is lost; where it runs is not known, "The agent stops. Work not yet saved may be lost; the conversation is kept." A terminal session is worded as a terminal ("The terminal session stops…"). **End session** is its only red control; the focus starts on **Cancel**, the rest of the page is inert while it asks, and Esc or a press outside cancels and returns the focus to the ✕ (as does a failed End). An End on its way cannot be sent twice. On a phone (640 px and less) it is a small bottom sheet, **End session** above **Cancel**. A failed start's **Delete session** is now **End session**: it was always the same stop, and the history stays. One shared component (`@blerglab/chat` 0.71.0: `ConfirmPopover`, `endSessionText`).
- **A new session is named after its repository.** The new-session sheet no longer suggests a made-up name (the old word list is gone): picking a repository fills the name with the repository's name ("blerg"), switching repositories updates it until you type your own (then yours sticks), and without a repository it is "Session". When a running session already has that name, the new one gets " 2", " 3" and so on. A scratch folder for a session without a repository is now `.scratch-session-<random>`. Agents can still rename sessions; board-started and child sessions keep the names they get today.
- **A subagent's card always says which model it runs on.** Subagents can run on other models than Main: a subagent with a model of its own shows it ("Model: Sonnet 5.5"); one on the default model shows the model it inherits, the session's own ("Model: Opus 5.5 (same as Main)"); and when even the session's model is not known it says "Model: same as Main". Never blank; the model's full id is in its tooltip.
- **Tickets on Live and Plan no longer repeat each other.** Live's Tickets list only the cards being worked on now: a card leaves Live as soon as it is done, and a card not started yet is not there. Plan's Tickets list every pinned card as the same compact line Live uses, each with one state glyph of one weight: ✓ done (green), ◐ in progress (blue) and ○ not started (muted). The header says "Tickets · 4 of 5 done", with the checklist right under it, no longer pushed down by big ticket cards. No status chips are left in either tab, and the green "read live" orb is gone: the words are in each line's tooltip, its accessible name and its hovercard, which also says "Live from the board". The state comes from the card's board column: a terminal column, an archived card, or a done or closed name (Done, Closed, Merged, Won't do) is done; a column name that means work (In progress, In review, Review, Code review, In dev, Dev, Implementing, WIP, Doing, Testing, QA, Verify, Staging, Blocked; numbering, emoji and counts around the name don't matter) is in progress; anything else (Inbox, Backlog, Ready, To do, a name not known) is not started. A card a worker is on right now (a running subagent or started session assigned to it in the checklist, or a started session that pinned it) is in progress whatever its column says. A ticket from another tracker gets its state the same way, from the status the agent pinned it with. Live waits up to 1.5 seconds for a board card's read before showing it, so a card already done never flashes there; Plan shows the pinned state at once, muted until the read lands (`@blerglab/chat` 0.67.0: `CardPreview.live`).
- **Hovercards no longer get in the way of the list below.** A chip's card now opens beside its list, never over the rows that follow: to the left of the side panel for every chip in it (Tickets, Running now, Plan, History, Files), and beside the row in the chat on whichever side has room; below or above only when neither side has room. While a card is open, and for 300 ms after it closed, moving onto the next chip shows its card at once, so walking down a list previews each item in turn. The card stays open only while the pointer heads for it (the safe triangle, judged over a few moves so a slow or stair-stepping hand keeps it); moving down the list or anywhere else switches to the next chip or closes it at once. A quick sweep across a list doesn't open every card it crosses. Every Running now row has a card too (Main, a subagent, a background command or a child session): what kind it is, its engine and where it runs, the model in full ("Claude Code · Opus 5.5", its id in the tooltip; no more guessing what "O" means), its state and whole current step, elapsed time and tokens, the card it is on and who started it; it opens beside the panel and switches as you move down the rows, and a click on a child session's row still opens that session. The small glyphs keep their tooltips. The timings live in `HOVERCARD` with the rest, and **Settings → Chat → Hover previews** turns these off too (`@blerglab/chat` 0.64.0: `warmMs`, `graceMs`, `gap`, `placeCard`, `data-hc-boundary`, `useHovercard`, the `run` card).
- **Markdown reads like GitHub.** Rendered Markdown (a published file in the viewer and its inline preview, the review page, a ticket's description, and chat messages at a compact scale) now follows GitHub's README styling, adapted from github-markdown-css (MIT, credited in `THIRD_PARTY_NOTICES.md`): GitHub's type scale and rhythm (24 px above a heading, 16 px below and between blocks, .25em between list items), ruled h1 and h2, inline code at 85% in a 6 px chip, code blocks at 85% with a 16 px pad, collapsed tables with zebra rows, a quote with a bar in the muted color, in the theme's colors for dark and light. Headings no longer take double spacing (their margins were measured in their own larger size), and the app's display face and code boxes no longer leak into documents. A document (a markdown file, the review page, a ticket's description) uses GitHub's font and 16 px; a chat message stays compact at the chat's size: the same rhythm at three-quarter gaps, with tamer headings (no rules). A document in a viewer reads in GitHub's box: a centered, bordered column with 1012 px of text, however large or maximized the viewer (in a narrow column, a phone or review's page beside its source, the full width without a border). The runner and Board bundles keep license comments, so the credit ships with them (`@blerglab/chat` 0.63.0).
- **The session panel's Capabilities tab is organized like Live and Plan.** It opens on a compact card: the engine with its version and the model's name ("Claude Code 2.1.305 · Opus 5.5", the model's id in the tooltip), then the workspace (cut in the middle, whole in the tooltip), the permission mode as a chip in words ("Bypass permissions" in amber for modes that act without asking, "Default", "Accept edits", "Plan" and the rest plain; an unknown mode shows as reported) and "As of 12:05 AM" (with the date when it is not today). The two explanations (what the engine loaded when its latest turn started, when changes reach it, that an active turn keeps its tools and that Blerg's grants apply to every call) moved behind an ⓘ beside it, which the keyboard reaches, a screen reader reads out when it opens, and Esc closes first, before the panel. The filter and a **+ Add** button share one row; **+ Add** opens the same add-a-plugin form as before. Skills, Plugins, MCP servers, Slash commands, Tools and Subagents are sections with the panel's heading style and counts; each row is its status as one glyph (● on, ○ off, ◌ connecting, ! needs sign-in, × failed, the word in the tooltip and for screen readers), the name, its detail and a source chip, and one line of description; a description that is cut is a button that shows it whole, with the whole text in its tooltip. The terminal session's Skills & plugins dialog shows the same.
- **Child-session permissions use ⑂, not ↳.** In the session panel ↳ means "runs inside Main"; the permission to start child sessions is now a fork, ⑂ ("can start sessions"), on the Live tab's permission chips, in the Permissions dialog (launch and running) and on a child-session approval in Needs you. Boards keep ▦ and MCP tools ⚙; the words stay in tooltips and screen-reader labels.
- **History folds runs of repeated strokes and opens a topic onto summary lines.** Three or more consecutive strokes of one kind in a topic (proposals, command approvals, child-session starts, review requests, review rounds) are one line, for example "Filed 26 cards → approved, 1 rejected", with the cards as wrapping chips and the span; a tap opens one line per stroke (the newest 50, and every waiting one, with "and N earlier"). Any other stroke or a topic's edge ends a run. Questions, decisions, milestones and findings are never folded. An opened topic is one line per kind (your messages, subagents with their time on the clock, files, commits, filed cards, renames) instead of the replayed rows, each line's time jumping into the conversation. The waiting items themselves are pinned above the feed under **Waiting on you**, with a "↑ pinned" marker in their place, and the server always sends them. A question the agent did not wait on reads "unanswered" and is not pinned, as do a question or a child-session start still pending when the session ended (nobody can answer or allow them any more); a proposal or a review request can still be decided after the end and stays waiting (`strokes[].count`, `items`, `items_truncated`, `first_at`, `cards`, `cards_truncated`, `strokes_truncated`; `topics[].messages`, `messages_truncated`).
- **On, open and selected controls are blue, not orange.** The session panel toggle when the panel is open, every pressed icon button, tabs, segmented controls, filter chips (the Board's filter chips and phone column tabs included; an active priority chip keeps its priority color as a small dot), the selected session in the sidebar, picked options in the launch sheet, checked boxes and the diff and review viewers' pressed buttons now use one blue "selected" color instead of the orange accent, which beside the red of **End session** read as a warning. Orange stays for the brand, primary actions and things that need you; red stays for danger. New tokens `--selected-border`, `--selected-bg` and `--selected-text` in `brand/tokens.css` (and its copies), tuned for WCAG AA in light and dark; the chat theme contract gains `--chat-selected-border`, `--chat-selected-bg` and `--chat-selected-text` (`@blerglab/chat` 0.53.0).
- **Resolved review threads stay on their lines, as on GitHub.** In a diff review, a resolved or rejected thread is no longer moved to the bottom of the panel: it stays on its line, collapsed to one bar ("✓ Resolved · conversation by the agent · Show"), and opens in place. In a later version of the diff, a thread whose code is unchanged stays on that code. A thread whose code changed is **Outdated** and, as on GitHub, leaves that version's Files for the new Conversation view, with its original code; Files shows "N outdated conversations · view". An open Outdated thread still counts as open, is sent with every review and can be answered and reopened from the Conversation, and opening the version it was written on shows it inline on its line. The "Earlier revision comments" section is gone (`@blerglab/chat` 0.41.0).
- The diff viewer on a phone (under 700 px) gives the diff most of the screen: the file list becomes a picker in the toolbar, the header and the review bar are one line each, and the selection help text is hidden. Review threads and comments work as before; desktop and 4K layouts are unchanged.
- The session header is one row of icon buttons in one style (outlined, same size, the word in the tooltip): star, Insights, the side panel, **End session** (a red ✕; was Kill; the only red control; the chat's Stop still interrupts the current turn) and **⋯**, which holds Discard ("end the session and archive it; history and URL kept", confirmed, with a warning while the session runs), Pause for a cluster session, and a terminal session's Skills & plugins. The header has no bell any more.
- The app has one Notifications bell, beside **+ New** in the sessions header (on a phone, in a session's header, with Insights moved into ⋯); the separate push toggle is now a setting inside it.
- **The Plan tab's checklist is one line per item.** Each item is its box (a spinner while doing), the title cut with an ellipsis (a click or the keyboard focus shows it whole), who it is assigned to as a small glyph (🤖 a subagent, ↳ a started session) whose tooltip and accessible name give its name, never its id, and one commit chip on the right that opens the diff in Blerg (its shas, line counts and how many items it covers in the tooltip). Open and doing items come first; the done ones fold into one "✓ N done · show" row, which a jump to a done item opens. Items that share a commit show its chip once, on the first of them. The live state names the checklist's assignees (`checklist.worker_titles`: a subagent by its description, a started session by its title, only for a started session the viewer may see: a private one is named to its owner only, an archived one to no one).
- **Running now cards are two lines each.** A subagent, background task or started session at work is its state glyph (the spinner while it runs), its title and its clock, then its current step in mono, cut with an ellipsis, and small glyphs: the model (or engine) and tokens, a ☰ for the plan item it is assigned to, words in their tooltips. "In the background", the subagent's type and its effort, a task's kind and start time, and a started session's status, effort, tool calls, cost and endpoint are in the title's tooltip. A click anywhere on the card (or Enter on its title) brings it into view in the conversation (a started session opens); the separate "Show in conversation" link is gone. The step line is cut to one line; a tap (or Enter) on it shows it whole in place, a long command with its description and result in a box that scrolls, and a second tap folds it. A started session's card keeps the clock of its current activity.
- Stopping a session that the server refuses now toasts "Ending the session failed" (was "Kill failed").
- The bell's Push notifications setting can turn push off as well as on (the server forgets that browser's subscription: new `DELETE /api/push/subscribe`), and says when the browser cannot receive push at all.
- A board's repos keep the order they were given (board migration `027`, which keeps the existing alphabetical order for current boards): the first is the working directory of board sessions. Until now they were read alphabetically.
- **Board call limits are higher and an administrator can tune them.** A session's board calls now default to 600 requests a minute and 8 at once (were 240 and 4). An administrator can change these, the 20 unrevoked credentials an account may have while revokes fail, and core's cap of 100 live exchange tokens per account, in Blerg's main Settings under **Board call limits**. Core's cap applies at the next exchange; the runner reads the rest from core and uses a change within a minute. Core migration 023 adds the table; with no row the defaults apply.
- **Only a session's owner, or an administrator, can rename it in the app.** Someone a session is shared with can still read it, but renaming it now answers 403.
- **Started sessions file proposals with their parent's sign-in.** A child (desktop or cluster) can now file (`blerg-runner ticket file`, `blerg_ticket_file`): it lists boards, searches and prepares the card with the sign-in of the session that started it, as its other board calls do. At Propose, the default for a board the parent did not pass down, the filing waits for you in Needs you; at File without asking or Full it is filed without asking, capped by the parent's level at that moment and checked again right before it runs. The filing is refused when the parent's sign-in has ended or the parent was stopped for good. The existing limits still apply.

### Deprecated
- New tmux-backed Terminal sessions are hidden from the launch sheet and refused when a person or an agent starts one (launch sheet, REST, the browser socket; MCP and child starts only ever start agent sessions). A start that names no `kind` is answered `kind "agent" is required`, and the API description no longer offers `tmux`. Existing Terminal sessions keep working. A board's **Assist** still starts a Terminal session until it moves to an agent session in 0.5.0, and an Assist start that fails now says why beside the button (it used to show "Starting…" and then nothing). Terminal support will be removed in 0.5.0.

### Fixed

- Workspace navigation cancellation preserves the shared daemon connection, and cached Find results refresh expired repository badges. Native macOS workspace checks now run with one command and retain commit-specific evidence.
- Owner-shell policy authorization, takeover cleanup, bounded control handling, terminal focus, local screen retention, and pop-out shortcuts. Runtime shell capability remains disabled pending containment and consent verification.
- **An answer you give inline no longer shows up as your own message in the chat.** Answering a question from its Needs you card (or its card in the chat) drew Blerg's relay to the agent as a "You" bubble with the question's id and text repeated ("Answer to your question 7b5dcd88-… (\"…\"): orange · Queued 3 of 3"). Now the question's card updates in place to "Answered: orange · by you · <time>" (or "Dismissed by you"), and the chat keeps one short row, "You answered: orange". Relayed decisions (review and proposal outcomes) are one short Blerg row without request ids. The agent still receives the full marked Blerg message, unchanged. The runner now records whose message each queued input is, so the chat can tell A message sent under an id Blerg keeps for its own relays (`question-…`, `review-…`, `proposal-…`, `child-turn-…`, `board-access-…`) is refused, so it can never stand in for your answer (`@blerglab/chat` 0.82.0).
- **Changing where a session runs no longer resets its model.** In the new-session sheet, switching between the local sandbox, the cluster and this machine (or another daemon) used to throw away the model and effort you had picked, so the place's default (Sonnet) quietly won: the sheet cleared every pick whenever the place changed. Your model and effort now stay: each place's list is matched by the model's id, then by its name (the same model under another id), then by its family. When a place doesn't offer it, the sheet says so beside the highlighted model field, e.g. "Opus 5.5 isn't available in the cluster: switched to Sonnet 5.5", and going back to a place that offers it brings your choice back. A list that is still loading never counts as a switch. An engine a place doesn't offer is switched the same way, with a notice.
- **A README's images all load.** An image in a markdown file from the session's folder could stay a placeholder while its neighbor loaded: the session's daemon dropped the reply when its connection was busy sending another one, and the page then gave up. The daemon now waits briefly for its connection (and no longer sends a reply the server has stopped waiting for), the server says why it refused a read in a machine-readable `code` beside its sentence, and the page retries by that code: while the server is only busy, for as long as the file is open (paused while the tab is hidden, one backoff for the whole page); a reply that did not arrive, a few times; an image whose read failed offers **Retry**. The folder viewer keeps showing the server's reason when it gives up. Reinstall the desktop daemon for the daemon part (`@blerglab/chat` 0.73.0).
- **A History card chip that names no board now links and previews.** An imported mark or a `mark --card N` line names a card number only; when the session has access to exactly one board, the chip links to that board's card and shows its hovercard, as chips that carry their board do (a pinned ticket of the board without a board of its own reads its card the same way). With several boards, or none, the chip stays the number and its hovercard says "board unknown".
- **Older history loads ahead of you, and only when you scroll for it.** The next earlier page is asked for when you are about one and a half screens from the top, so it usually lands before you get there. When a page lands and moves the top out of reach, the next waits for you to scroll up again (sitting still no longer loads page after page); when it adds little or nothing above you, the next comes by itself, so you are never stuck at the top. Requests are at least 400 ms apart. "Loading earlier messages…" is now a small pill over the top of the chat that takes no room and appears only when a page takes longer than 300 ms, so the messages never move for it; a screen reader hears it, and "Loaded earlier messages" when a page arrives (`@blerglab/chat` 0.68.0).
- **The diff review's [+] for a new comment sits where GitHub puts it.** In unified view each line has exactly one [+], always in the same spot just right of both line-number columns, at the start of the code and level with the first line of a wrapped line (it was centered on the row, so on a long line it sat below its number). It comments on the new line for an added or unchanged line and on the old line for a removed one. In side-by-side view each side has its own [+], shown only for the side under the pointer, commenting on that side's line; an unchanged line no longer shows two at once. Keyboard focus still shows it. On a touch screen it is always shown with a hit area of at least 24×24 px (`@blerglab/chat` 0.62.0).
- **Running now shows the agent you're talking to.** The side panel's Running now said "Nothing is running" (0) while the session's own agent was busy, for example minutes into a Bash command. It now starts with a **Main** card while the agent works: a spinner, "Main" and the engine's mark, the turn's clock, and its current step from the same live data as the working line under the chat ("Bash: Fast-forward main… 4:09", or "Thinking…", or "Working…"), on the chat's clock, so the two agree; Main counts in the Running now badge and in the header's panel badge (its label and screen-reader announcement too), but a turn starting never opens the panel by itself. Subagents and background commands run inside Main and are indented under it behind a ↳ (a screen reader hears "Runs inside Main"); started sessions run on their own and stay at Main's level, each with its own engine mark. While a live session is idle Main shows Zz (? while it waits on you) and does not count; with nothing running the section says "Idle." for an idle session ("Waiting on you." while the agent waits on an answer) and "Nothing is running." for one that is starting, stopped, ended, failed or disconnected, which have no Main card. The session's own progress bar is on Main's step line, and gives way so the step and its clock stay visible. Main's tooltip: "The agent you're talking to. Indented rows run inside it; child sessions run on their own." A click on Main scrolls the chat to its end (`@blerglab/chat` 0.52.0: `useWorkingLineStore`, `useMainStep`, `mainStep`).
- **A sandboxed session whose container stops now ends with the real reason.** The daemon looks at a sandboxed session's container every few seconds and whenever its engine fails; when Docker says it has stopped, the session ends as failed with "The sandbox container stopped (exit code N, OOMKilled: yes/no, at <time>)" (or "is gone" when it was removed), and that error event records Docker's exit details. This holds for every engine that runs in a sandbox: Claude and Hermes no longer go on retrying in a dead container, and Codex no longer retries there and then advises updating Codex. A paused container (the turn-end sync) is not taken for a dead one, every look goes to the Docker context the container was started on (switching your docker CLI's context does not end sessions), and a container counts as removed only when Docker says so twice a few seconds apart. Reinstall the daemon.
- **The daemon only removes its own sandbox containers.** Every sandbox container now carries an owner label for the daemon that started it; the restart sweep and reattach pass touch only containers with that label, or, for containers from before it, sessions the daemon has a record of. A container that merely has a `blerg-sandbox-` name (another daemon's, a test's) is left alone. The daemon's tests label every container they start and remove only by that label.
- A Hermes session stopped on purpose (by the person or a parent) whose gateway shutdown cannot be confirmed is no longer reported as a runtime failure, so `blerg-runner session watch` reports that child as ended (exit 0), not failed (exit 4).
- **Scrolling up into older history no longer jumps.** While an earlier page loads, the top of the chat says "Loading earlier messages…" with a spinner; when it lands, the message you were looking at stays exactly where it was on screen, also as images, diagrams and code blocks above it size late (the chat keeps your place itself after every change, on every browser, instead of relying on the browser's scroll anchoring). A flick or trackpad glide still going when a page lands stops there, so you scroll again from where you are rather than being carried through the new messages. At the very start the chat says "Beginning of the session". While you follow the end, late-sizing content no longer leaves the view short of the newest message (`@blerglab/chat` 0.46.0).
- Local sandboxes use Docker's init process to reap orphaned tool and test processes, preventing exited children from accumulating as zombies against the 512-task limit. Existing containers keep their launch settings.
- The Codex usage meter no longer goes missing for a work account: a usage-based plan's spend limit shows as "spend" (share and reset only), windows Codex reports only per limit are read, and an account with no window says so ("—") instead of showing nothing. A provider a connected machine runs that has not reported yet has a placeholder row whose tooltip says what to expect (and that an older daemon never fills it); a provider the daemon does not read (Claude's reader off, a sign-in it cannot read) says so ("⊘"). Hovering a meter gives the share used and left and the reset time of each window. Reinstall the desktop daemon (or redeploy) for the Codex changes.
- Settings no longer shows browser-default buttons and dropdowns: every button and select (Sessions starting sessions included) uses the app's styled button and select, with focus and disabled states, and the dialog widens on large screens.
- **Real Mermaid diagrams draw, and one that does not says why.** The diagram check refused a label that contained a word such as "image", "style" or "click" (`vm-image-controller`), and any color styling. It now matches the actual image, HTML, link and URL syntax, and allows `classDef`, `class`, `style` and `linkStyle` with fill, stroke, color, stroke-width, stroke-dasharray, font-weight, font-style and opacity (plain values, every declaration on the line checked; `url()`, functions, escapes and `!important` are still refused), `A@{ … }` shape blocks with plain keys (shape, label, w, h, pos, constraint; never img or icon), class annotations (`<<interface>>`), `<br/>` line breaks and a front matter title. The check reads the diagram as Mermaid does (every line ending, including a lone `\r`, and every Unicode space normalized), and Mermaid gets exactly the string that was checked. Mermaid now draws in a sandboxed frame with no network access (its own Content-Security-Policy, no same-origin access), so a diagram the check missed still cannot make the browser load anything. A diagram that is refused or does not parse shows its source with the reason, in the chat and in the viewer ("line 12: url() not allowed", or Mermaid's parse error), instead of only "Preview unavailable". A published `.mmd` draws as the diagram on its card in the chat, with View. In the viewer a tall diagram is scaled to fit instead of being cut off at the bottom (`@blerglab/chat` 0.36.0).
- **Image mark-up lands where you drew it.** When the image was narrower than the mark-up area (a wide or maximized viewer, a height-capped landscape image, any portrait image), the marks in the annotated PNG sent to the agent, and the positions of its comments, were shifted left and squashed sideways. The drawing layer now covers the image's own pixels and maps the pointer, the on-screen marks and the flattened copy through the image at one scale (`@blerglab/chat` 0.36.0).
- Unpinning Blerg Board cards matches legacy board-name keys as well as card numbers, with or without a recorded board ID, while retaining ambiguity checks and the original event identity. A card pinned with no recorded board ID matches a named board only by the board name stored with it; one with no board recorded at all is unpinned by its bare `#number`, and naming a board for it is refused as ambiguous.
- Core, board, and runner wait through database failures at boot with exponential backoff, and log why each attempt failed (the error class, such as `SQLSTATE 28P01`, and its message, with passwords and connection strings removed); after five failures in a row, `/readyz` names the kind of failure (for example `database: SQLSTATE 28P01 (authentication failed)` or `database: connection refused`), never the user, host, database or message, since it is reachable through the ingress. Liveness stays healthy while readiness and API traffic return 503 until migrations and initialization complete.
- Kubernetes deploys keep the serving core, board, and runner replica until database-backed readiness confirms required migrations. Runner surge pods share the data volume's node. On SIGTERM, core and the board keep serving for 5 seconds with `/readyz` at 503 while the pod leaves the Service, then shut down gracefully, so a deploy no longer cuts their in-flight requests; the runner still exits at once, by design (it keeps its connections in memory). The session CLI retries a 502, 503 or 504 reply for at most 30 seconds in all (time spent waiting on attempts included), and a refused or reset connection for at most 10, and only for reads and other idempotent requests or a start that sends an Idempotency-Key; other writes (`publish`, `update`, `ask` and the like) are not retried, and a successful reply is never retried, whatever its body (an attached HTML file downloads at once).
- With Claude Code's own sandbox on (`sandbox.enabled` in your user, project or managed settings), `blerg-runner` works inside it: a Claude session the daemon starts gets the runner's (and board's) host and port on `sandbox.network.allowedDomains` in that session's own settings, never in your settings files and never as a bare host; `blerg-runner` dials directly first and then goes through the sandbox's proxy, only for the session's own runner and board. When managed settings allow only managed domains, Blerg does not override them: the session says which address the administrator has to allow. Codex's own sandbox is not changed; `install/desktop/DAEMON.md` has the manual settings for Claude Code and Codex.
- **A session whose engine has stopped for good no longer shows as running, and `session watch` no longer waits for it forever.** When Codex's app-server failed for good (for example because a sandboxed child's container was gone), the daemon kept listing the session, so the runner revived it to "running" at every heartbeat and a parent's `blerg-runner session watch` waited for a turn that never came. The daemon now ends such a session with the engine's reason: it shows as failed with that reason on its card and page, and its parent gets a note (`Child "<title>" failed: "<reason>" and stopped`). A Hermes session whose gateway cannot run again (an unsupported gateway, a changed provider binding, a shutdown it could not verify) now ends the same way instead of staying listed with nothing to run it. Errors the engine recovers from (a restart that works, a failed turn) do not end a session. `session watch` and `session wait` return as soon as the child has ended in error or its runtime reports it has stopped, print `the session failed: <reason>` and exit with code 4 (documented in `session --help`); an error the child can recover from does not end the watch. Reinstall the desktop daemon (or redeploy) for the daemon part.

### Security

- Harden workspace shell launch revocation, consent invalidation, and fair transport scheduling; runtime capability remains disabled pending security gates.
- **SVG images open in isolation.** The file viewer, previews and hovercards now draw SVG images so that an image opened on its own has no access to Blerg.
- **Every git command the daemon and a session pod run themselves goes through one hardened runner** (the server runs none). The session origin read, the commit lookup, the sandbox clone's setup, turn-end fetch and release, the clone of a named repository and a cluster pod's own clone and wip push now share the same protections: their own environment (no inherited `GIT_*` variable or secret, no system or global configuration, no replace objects, no grafts file outside the repository), every configured command, hook, submodule recursion and the diff order, mailmap and attributes files a configuration names switched off, only the transport the call needs allowed (`GIT_ALLOW_PROTOCOL`), the whole process group killed on timeout (the pod's steps now have timeouts too), and a memory limit on the reads. A sandboxed clone's `upload-pack` now carries those overrides itself and parses no commit-graph, multi-pack index or bitmap; the turn-end fetch is refused, with what to remove, when the clone has `.git/info/attributes`, a `.gitattributes` anywhere under `.git`, grafts, a `config.worktree` or an include in its config, or more than a capped amount of data under `.git`. The host still parses the clone's config, refs, packed refs, shallow file, pack indexes and objects (received objects are checked). Where a call legitimately differs (a clone from a remote keeps your own git configuration, credential helper, git-lfs and ssh keys; the pod's blobless clone may fetch missing objects over its clone URL's transport, whose URL still carries the pod's token), the code says so.
- Desktops have no owner in the runner, so the board cannot tell whose desktop is whose: the repo lookup answers for whichever connected desktop the admin picks, and can show the `owner/name` behind a folder of that name on another person's desktop. It is limited to the board admin who owns the automation token, asks one picked desktop at a time, reveals only the GitHub `owner/name` of a folder whose name is a repo of the board, and saves nothing until that admin confirms.
- A Local sandbox session no longer gets your `~/.claude`, `~/.claude.json`, `~/.codex` or `~/.hermes` files. Each sandboxed session gets a home of its own with only the engine it runs: its current sign-in with the refresh token replaced by a placeholder (so the session can neither renew nor change your sign-in; the daemon re-copies your current one every 15 seconds and never reads anything back), its settings as copies without their secrets (no `env` block, credential helpers, hooks or status line in Claude's settings; no MCP servers, plugins, `notify`, shell environment or provider headers and tokens in Codex's `config.toml`, also for a Codex session with MCP connections; `~/.claude.json` reduced to the account state), and your instructions, skills, agents, commands, hooks and plugins read-only. A session whose sign-in has expired on your machine says so. The home is removed when the session ends, and a crash's leftover homes when the daemon next starts. See `docs/sandbox-image.md`.

## [0.4.4] - 2026-10-21

### Upgrade notes
- **Reinstall the desktop daemon before starting Local sandbox sessions** (`install/desktop/daemon/install.sh install`). The runner refuses every sandboxed start (launch sheet, board, cron, child) on a daemon that does not report `sandbox_clones`, with "update the daemon before starting Local sandbox sessions"; host sessions are unaffected. Stop running sandboxed sessions before you reinstall: the restart ends sandboxed Agent sessions as before, and a sandboxed Terminal session an older daemon started with your folder mounted is stopped when the new daemon reattaches (or, if its container is gone, not recovered), with a note in the session. A sandboxed session's work now lands on `wip/<session-id>` in your repository, not in your working tree: check out or merge that branch to use it. If you ran Local sandbox sessions before this release, check the `.git/hooks` and `.git/config` of those repositories for anything you did not put there.
- **Existing "Always allow … on the cluster" rules now also cover your unsandboxed desktop sessions' cluster children** (until now they could start pods only for cluster sessions). Review them in Settings → Sessions starting sessions. Sandboxed desktop sessions still ask for each cluster child unless the runner sets `BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN=any` (new; default `once`; see `install/k8s/CLUSTER-RUNTIME.md`).
- Desktop sessions can start cluster children (no migration). Reinstall the desktop daemon for the updated session CLI and guide (`--runtime cluster` from a desktop session, `budget --runtime cluster`). Desktop sessions launched before the upgrade have no recorded sign-in, so their first cluster child always asks on the card.
- Runner migration `071` records proposal deadline extensions (it applies on start; nothing to do).
- Runner migration `072` adds questions that do not wait (`messages.no_wait`; it applies on start). Reinstall the desktop daemon (or redeploy) for `blerg-runner ask --no-wait` / `--resolve`, the session guide line that tells interactive sessions to use it, and the marked delivery of a question's answer: a daemon older than this release passes the answer to the agent as plain text, which another person or a parent session could imitate. Questions are notified to the session's owner only (bell, toast) and send no phone push, because push subscriptions belong to no account.
- Runner migration `073` lets a board level a parent passed down to a child be stored without a sign-in of its own (`created_via = 'parent'`; it applies on start). Reinstall the desktop daemon (or redeploy) for `blerg-runner session start --board` and the cap shown by `board access`.
- Runner migration `070` gives every session board permission a level; existing filing permissions become **File without asking**. Reinstall the desktop daemon (or redeploy) for the updated session CLI (`board access`, board commands without a launch board) and the `blerg_board` session tool for Codex.
- Reinstall the desktop daemon (`install/desktop/daemon/install.sh install`), or redeploy, for the marked relayed decisions in Claude, Codex and native sessions, the owner-only Hermes approvals and their failure messages, and the batch review-reply wording in the session prompts and the managed `~/.claude/CLAUDE.md` block. The daemon keeps a new private key, `marker.key`, in its state directory (`~/.blerg-runner-daemon`); a cluster pod with retained conversation history keeps it on that history's volume, so a replacement pod derives the same codes. Do not delete it: Claude Code records a conversation's system prompt, the code's announcement included, and a resumed conversation keeps that record until it is compacted, so after a new key every relay to a resumed Claude conversation carries a code the model was told is not Blerg's, and the agent treats real decisions as forged (it can still confirm them with `blerg-runner review status` or `ticket status`). A daemon that cannot keep the key says so in its log and uses an in-memory one, with the same effect after its next restart. Sessions started before this version keep the system prompt they were started with: their relays carry the marker, but the model is told about it only in new sessions or after a compaction.

#### Rolling back to 0.4.3
0.4.3 reads every live board permission as **File without asking**, so a board a person set to **Read** (or lowered to **Propose**) under 0.4.4 would file without asking again. Before starting a 0.4.3 runner on a database that 0.4.4 migrated, run this once against the runner database (on a database 0.4.4 never migrated it fails harmlessly: there is no `level` column):

```sql
UPDATE session_proposal_grants
   SET revoked_at = now(), revoked_reason = 'downgrade'
 WHERE kind = 'board_file' AND level IN ('propose', 'read') AND revoked_at IS NULL;
```

Board permissions at **File without asking** and **Full** keep filing without asking under 0.4.3, which is what they allow. A launch board that was lowered goes back to what its launch gave it (0.4.3 has no levels).

#### Rolling back to a build before children inherit board access
This applies to a rollback to 0.4.3 and to any earlier 0.4.4 build (one without runner migration `073`). Such a build does not cap a child by its parent: it reads a child's board level as the child's own, and acts with the sign-in stored on it. Before starting it, end every board level a parent passed down and every level set on a started session, including children whose parent row was deleted (the delete clears `parent_session_id`, so a child is also known by how it was started):

```sql
UPDATE session_proposal_grants g
   SET revoked_at = now(), revoked_reason = 'downgrade'
  FROM sessions s
 WHERE s.id = g.session_id
   AND g.kind = 'board_file' AND g.revoked_at IS NULL
   AND (g.created_via = 'parent' OR s.parent_session_id IS NOT NULL
        OR COALESCE(s.delegation_via, '') <> '' OR s.started_by_kind = 'session');
```

Run it together with the block above when going back to 0.4.3. Started sessions then have no board access until you give them levels again.

### Added
- **Children inherit board access.** A session that starts another one passes board access down with the start: `blerg-runner session start --board <name|id>:<level>` (read, file or full; repeatable), never above its own level for that board. The approval card shows it ("with Blerg · Full"); a rule or grant can start such a child too, and the start is checked against the parent's levels again when it runs. No credential is minted: the child gets a level of its own and its board calls go through the runner with its own session token, acting with the parent's sign-in, on the desktop and the cluster, sandboxed or not. Every call is capped by the parent's level at that moment, so lowering or revoking the parent lowers every child at its next call, and a child never exceeds its parent even if the parent is raised later. The child's Permissions dialog shows each level with the cap; you can lower a single child, and raising one above its parent is refused. The child gets the same "Board access changed" note, the parent's changes included. Its access ends when the parent is stopped for good, a kept-running child included. Child-start rules and session grants you gave before this release also cover starts that pass boards (the child never gets more than the parent). A desktop child cannot file proposals yet (it has no recorded board sign-in), so **File without asking** gives it only Read in practice. Each child has its own board limits (4 calls at once, 240 requests a minute, 20 filings an hour without asking), bounded by 3 running children per parent and 6 per account (`@blerglab/chat` 0.35.0).
- **A pinned board card shows the live card.** A Blerg Board card the agent pins (`blerg-runner pin` with `"source": "blerg-board"`, its `board_id` and `"key": "#<number>"`) is shown in the session panel's chip and ticket dialog as the board holds it now, read with your own board permissions through the runner (`GET /api/board-cards/{board}/{number}`): its column, type, priority and tags, where it was filed from, its repos and its trail, with a live mark, and a note when the agent pinned it in another state. **Move** works in place (as your role on the board allows); Run, Discuss and Archive open the card in the Board. When the board cannot be read, the dialog shows the agent's copy and says so. The runner sets the card's address itself (the Board's page, never a link the agent wrote), and `blerg-runner ticket get`, `ticket create` and `ticket file` print the card's address and a ready `pin` line. `blerg_card_get` takes `with_trail` (column name, columns and the latest 30 trail events in one call). Pins from other trackers keep their dialog, now one column with the details as chips instead of a Details box. The ticket dialog takes `min(1400px, 94vw)` like the file viewer, keeps its text at a reading width, and has **Maximize** (⤢ / Ctrl+Alt+F), remembered like the viewer's (`@blerglab/chat` 0.34.0).
- **Questions that do not block the agent.** `blerg-runner ask --no-wait "<question>"` puts a decision that belongs to you in the session's Needs you and the bell ("Question · not blocking"), and the agent keeps working. Answer it there or on its card in the chat: the answer is recorded like any answer and reaches the agent at its next step as a message naming the question (`Answer to your question <id> ("…"): …`). A message you type in the chat is never taken as its answer; if you answer that way, the agent closes it with `blerg-runner ask --resolve <id>`. Only the session's owner answers one, a daemon restart leaves it open, and a session started with the runner key cannot post one. An answer the agent can never be sent (for example, too long to send with its question) is tried three times; then the session says so, and you send it as a message. A delay (a busy session, a full queue, a database error) is retried until it clears; if it lasts ten minutes, the session says once that the answer is still waiting. The source `question_answer` is refused from every caller (422) like Blerg's other relayed notes (`@blerglab/chat` 0.34.0). The session guide and the messaging skill now tell agents to put such decisions in as a question, not only as prose.
- **Board access levels** in a running session's Permissions dialog. The **Boards** section replaces Board filing: each of your boards has a level, and each level includes the ones below it: **Propose** (every board: search and propose cards that ask you), **Read** (list, get and search every card), **File without asking** (the former filing permission), **Full** (create, update, move, link and comment). A board chosen at launch shows as Full and can be lowered or revoked; chips read "Product · Full". The level is checked on every board command and call, so a change holds at the agent's next one with no restart: through the board chosen at launch (its engine's board tools and CLI), through the `blerg-runner board`/`ticket`/`tag` commands of a session without one (they go through the runner with the session's own token; `blerg-runner board access` lists the levels), and through the new `blerg_board` tool every Claude and Codex session has. The agent never holds a board credential: the runner exchanges one for each call (read-only for reads) and revokes it after. Owner-only and logged; restricted, cron and board-started sessions keep exactly what their launch gave them. The agent is told of each change by a note from Blerg, and checks its levels before saying it lacks access. No caller can write in Blerg's voice: the runner API and the MCP `send_message` tool refuse the sources Blerg writes itself (`proposal`, `review`, `system`, `ask_answer`, `note_reply`, `limit_resume`) with 422, and relayed notes quote board names and card titles on one line, at most 80 characters. One session runs at most 4 board calls at once and 240 board requests a minute. A signed-out board's level is renewed from its row (**Renew**); a sign-in limited to one board changes only that board. The launch sheet's **Permissions…** sets levels too. The Permissions dialog grows with a large screen and has **Maximize**.
- **Desktop sessions start cluster children.** On an install with a cluster runtime, a session on a desktop can start a child that runs as a cluster pod (`blerg-runner session start --runtime cluster`; `budget --runtime cluster` and `models --runtime cluster` show what it can run). The same approval card ("on the cluster"), limits, child routes, Live, discard and end cascade as any child. Cluster approvals are a third set beside the desktop's sandboxed and unsandboxed ones: the Permissions dialog shows a **Cluster** row for a desktop session (each row says "on this desktop" or "on the cluster"), the launch sheet offers it, and Settings can add a cluster rule. A **sandboxed** desktop session asks for each cluster child (Allow once or Deny; its Cluster row is shown off with why), because the pod gets your credentials; `BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN=any` lets it use session permissions and rules too. Allowing one cluster child on a card makes your current sign-in the one that session's later automatic cluster starts use. The child gets your engine credentials in its pod only, against the browser sign-in you launched the parent from (now recorded for desktop launches too) or your decision on its card; with no live sign-in a rule or grant asks you instead. Each cluster child keeps the sign-in it started with and is resumed with it. Its repository must be given as org/name unless the cluster has a default org. A desktop-only install answers "this install has no cluster".

### Changed
- **Hiding a session's started sessions no longer makes them look gone.** In the sidebar, a small chevron on the parent card's second line, under its status glyph, shows or hides the sessions it started; the indent line drops straight down from it, and it never opens the card. The "↳ N" chip on the top line is information only, and while they are hidden it shows what is behind them (? when one needs you, ● when one runs). A started session that runs or needs you is never hidden.
- **A running child shows once in the session panel.** A started session at work (starting, running, or waiting on an answer) has one card, in Running now, with everything: engine, model, effort, elapsed time, its current tool, the plan step it was assigned and Discard. Started sessions shows a single "1 running ↑" line that brings it into view, and lists the children that stopped (idle, done, failed) to reuse or discard. A subagent that reports no effort of its own shows the session's ("effort: high (session's)") instead of "unknown", in the panel and on its card in the chat (`@blerglab/chat` 0.34.0).
- The file viewer uses the screen (`@blerglab/chat` 0.30.0). A review or a diff takes about 96% × 92% of the window with no pixel cap, so a 4K display gets a 4K review; the review's source takes the left half with its own horizontal scroll, and the page (at a reading measure) and its 300px comment margin sit side by side, centered together in the rest. A plain file opens up to 1400px wide with prose at a reading measure, and images and PDFs fill it. A maximize button (⤢ / ⤡, or Ctrl+Alt+F) in the viewer's header takes it edge to edge and is remembered separately for files, reviews, diffs and image mark-up. The ticket modal widens to 1200px and the Permissions dialog to 900px. A phone keeps its full-screen sheet.
- **`blerg-runner fetch` takes attachment IDs, not names** (for agents and scripts). `fetch <name>` now fails with the matching IDs as a hint ("did you mean <id>?"), and `fetch --all` downloads every attachment, same-name ones included, rather than only the latest of each name. Same-name files are named by upload time (the oldest keeps `name.ext`, later ones are `name-1.ext`, `name-2.ext`), every `Saved` line names the attachment ID, a skipped one reads `Already have <id> at <path>`, and `fetch --list` shows where each ID was saved. Reinstall the desktop daemon (or redeploy) for the new CLI.
- While the model thinks, the working line's clock shows how long this thinking phase has run, then goes back to the turn's time when the phase ends (`@blerglab/chat` 0.32.0).

### Fixed
- A per-session request that hits a database error answers `503` with `Retry-After` instead of `404 session not found`, so a client retries instead of treating the session as gone.
- On an install without personal credentials for cluster pods, a cluster child (or session) with no recorded sign-in resumes when it is sent a message, instead of failing with "session could not resume; retry shortly". A live sign-in is required only where the pod fetches your personal credentials with it.
- **A parent's messages to its child no longer read as yours.** In a child session, the parent agent's answer to the child's question and its plain `blerg-runner session send` messages are labeled as the parent ("↑ Parent · <title> · Answer"), with a link to the parent session inside Blerg; your own answers still read "You". The runner sets this from the parent's own session token: the `session` source is refused (422) from every other caller, and an answer the parent gives is recorded with `answered_by: "session"` (`@blerglab/chat` 0.33.0). A child's header now reads "started by <agent> from <parent>" (it said "started by from …").
- **A parent is told when its child's turn ends.** When a child finishes its turn, asks a question, waits on an approval only the person can give, or fails, the parent gets a one-line note from Blerg at its next step (`Child "<title>" finished its turn: "<first line>". blerg-runner session result <id>`), so it no longer needs to keep `session watch` running. Once per child turn, never to a parent that has ended; the source `child_turn` is refused from every caller (422) like Blerg's other relayed notes, and the chat draws it as a Blerg note (`@blerglab/chat` 0.33.0). Reinstall the desktop daemon for the updated session guide.
- **A sandboxed child no longer works in its parent's checkout.** It works in a clone of its own on the branch `wip/<child-id>`, like every sandboxed session now (see Security), and its commits are fetched into the repository at the end of each of its turns, before the parent is told the turn ended. `session result` names that branch. Reinstall the desktop daemon: the runner refuses sandboxed sessions on an older one.
- Relayed review and proposal decisions reach Claude, Codex and native sessions as Blerg system notices: a `<system-reminder>` block whose header carries the session's private marker code, which only the daemon and the model know (the system prompt announces it). Marker-like text in any other message, and inside a relay's own content (an artifact name, a reviewer's note, an MCP server's error), is neutralized, so it can neither pass as Blerg nor close the block; the code is matched in any case, split by spaces, line breaks or invisible characters (marks, variation selectors and blank lookalikes included), or in fullwidth, and any session's code is removed where it appears in the marker's own header or announcement. The code never leaves the daemon: it is not on any command line (Claude reads its appended system prompt from a private file), and every event the daemon sends has it replaced, so a tool result that lists processes or reads a transcript cannot show it. The native loop also marks a decision that arrives mid-turn or while an ask waits. Hermes sessions get no marker (`docs/hermes-gateway.md`).
- Hermes approvals are owner-only (`@blerglab/chat` 0.31.0): the chat shows the reply box only to the session's signed-in owner, says nothing about who can answer until the session's live state has loaded, and on the board points to the session in Runner. A runner-key session's approval is refused with that reason; an approval that could not be delivered for another reason (a timeout, a server error) says so instead of blaming the runner key.
- The next cluster pod uses a personal Hermes credential's current metadata instead of a cached answer, and the launch sheet shows a credential saved in core the next time it opens (its answer is at most five seconds old, not a minute).
- An open board recovers its live updates after a failed token renewal or reload, retrying with a growing delay (3 s up to a minute) and not at all once the board is gone or access was lost.
- Review rounds show no count until the review loads, the session prompts and the managed `~/.claude/CLAUDE.md` block describe answering a whole round in one `review reply` call, and `review status` no longer repeats its warning.
- A message's attachment previews stay on the files it was sent with (`@blerglab/chat` 0.32.0): the note names each file by ID, review and image mark-up messages included, so a later upload with the same name no longer changes an earlier message's picture. A message from before this release (a name-only note) shows the newest upload of that name made at or before it, never a later one. A pasted screenshot gets a unique `screenshot-…` name; a copied file keeps its own. A repeated `fetch --all` skips files it already has.
- A thinking model shows progress on the working line and on Running now: "Thinking… 1:23" for the phase, or for Hermes, when it reports one, the phase's token count ("Thinking… 1,234 tokens", counted again from zero at each phase). Claude and Codex show the clock: neither reports reasoning tokens while the model is still thinking. Counts follow the token rule (the owner and administrators); others see the clock. A daemon restart no longer leaves a stale thinking clock on the session.
- **Every pending proposal shows in its session's Needs you and in the notification bell.** Needs you used to list only plugin requests, so a session's file_ticket proposal appeared only as a chat card (when its event was loaded) and on the Proposals page, where it could expire unseen. Each card now leads with the tool it runs and its connection, from the server, apart from any title the agent wrote (labeled as the agent's); then a readable preview (a ticket shows its title, type, board, repositories and rendered body), with the stored JSON under **Show exact request**. The session's live state counts pending proposals in `needs_you` (not one past its deadline, which shows as expired; the session page's and its panel's Needs you counts, the Proposals page's Pending count, the bell and the navigation badge leave it out too, by the server's clock). In its last 24 hours a proposal warns once, in Needs you and as an in-app notification, and **Extend 7 days** moves its deadline, up to 30 days after the agent proposed it (the warning is in-app only, never a push notification); extended board filings and plugin requests can then be approved and edited past the original seven days. The Proposals page opens on history: who decided ("you"), the session permission that approved it automatically, and every extension. Runner migration `071`.
- Board views, card sheets and session chats fit a phone: full-screen sheets, touch-sized controls and no sideways scrolling; on a large monitor the board session page grows with the screen.
- Ordinary board CLI use fits the session's request budget (240 a minute, was 60), and a refusal says how many seconds to wait (`Retry-After`). While revoking board credentials fails, an account stops starting board calls once 20 unrevoked ones are live, so a revoke outage cannot use up core's token budget.

### Security
- **A Local sandbox session no longer mounts your checkout, `.git` included.** Until now an ordinary sandboxed session (a Terminal or Agent session from the launch sheet, a board's card run, a cron) got the whole folder mounted writable at `/workspace`, so the agent in the container could plant `.git/hooks` or configuration (`core.fsmonitor`, `core.sshCommand`) that ran on the host as you the next time Git ran there, and could move your checked-out branch. Now every sandboxed session works in a clone of its own (`git clone --shared` under `~/.blerg-runner-daemon/sandbox-clones/<session-id>`, on the branch `wip/<session-id>`): only the clone is mounted, your working tree and `.git` are not, and your repository's objects are mounted read-only for the clone to borrow. Its commits land on `wip/<session-id>` in your repository at the end of each turn and when it stops, through a host fetch of that one branch with the clone's hooks and configured commands off, the container paused, and the clone's `.git` checked for links and size first; work it left uncommitted is committed for it inside the container at the stop. Nothing is merged or pushed. A new empty folder or a scratch folder is made an empty repository first (never inside another repository's working tree); a folder with content that is not a repository, or whose clone cannot be made, is refused, and nothing but the session's own recorded clone is ever mounted. With `wip/<session-id>` checked out, the commits go to `wip/<session-id>.incoming` instead. The 0.4.2 linked-worktree mount is gone (a linked worktree is cloned too). The daemon never runs host Git with the clone's configuration (the commit a filing names is read as plain files). `session result` names `wip/<session-id>` for every sandboxed session in a repository. Details: [`docs/sandbox-image.md`](docs/sandbox-image.md#what-a-sandbox-works-in-and-where-its-commits-land).
- Update the core web build dependency to address editor-launch vulnerabilities.
- A message sent from the browser is always the person's chat: the runner sets its source itself, so a signed-in account that can see a session can no longer send one as a relayed review or proposal decision, a system note or an ask answer over the WebSocket.
- Enforce filing deduplication and permission approval limits atomically; briefly cache permission-panel sign-in checks.
- Make Run as me mint limits atomic and bounded; serialize board deletion with token replacement. `BLERG_CORE_BOARD_MINT_DISABLED=true` on core switches new Run as me tokens off (existing ones can still be revoked), and the board says so.
- A session's **Read** and **Propose** board access levels no longer let a filing go through without asking; only **File without asking** and **Full** do.
- Session refresh hardening.
- Keep recently validated gateway grants usable under lookup pressure.
- The MCP gateway's public route answers a retryable 503 when its own database fails, instead of refusing a valid token and counting a failed attempt against it.
- Bound the sign-in limiter's memory when a caller sprays long usernames: a long name is tracked by its hash, still one bucket per name.

## [0.4.3] - 2026-10-20

### Upgrade notes
- Desktop: reinstall the daemon (`install/desktop/daemon/install.sh install`) for the new CLI commands (`models`, `review request`, `ticket status`) and the child-session guide; rerun `./blerg-up.sh` so it adds `BLERG_CORE_BOARD_MINT_KEY`.
- Kubernetes: rerun `./deploy.sh`; it adds the mint key, and optionally stores `HERMES_PROVIDERS_FILE` as the shared Hermes provider config. Runner migrations 064–069 and the new core and board migrations apply on start.
- **Run as me** needs one new secret, `BLERG_CORE_BOARD_MINT_KEY`, on core and the board. `./blerg-up.sh` adds it to an existing `.env` and `install/k8s/deploy.sh` to an existing `blerg-secrets`; with neither re-run, boards keep working and take a pasted token as before. Core migration `022`, board migration `024`.
- An existing install keeps its accounts, and setup mode does not open on it, because an account exists. That includes an install whose generated admin never signed in: its one-time password is still the one printed next to `BLERG_BOOTSTRAP_ADMIN_PASSWORD` in that first boot's log, if you still have the log. Otherwise reset it: on the desktop `./blerg-up.sh --reset-admin`; on a cluster `kubectl -n blerg exec deploy/blerg-core -- /blerg-core users set-password --subject <username>` (look up the username with the `psql` query in `install/k8s/README.md`, "First login").

### Added
- **Run as me**: a board's sessions run as you in one click. Board settings → Automation has a **Run as me** button that has core mint a `run-sessions` token for you and saves it on the board, with no copy and paste; the create-board form has **Run this board's cards as me** checked (`BLERG_BOARD_DEFAULT_RUN_AS_CREATOR`, default true; a multi-tenant operator may set false); and a board with no token offers it beside **Run this card**, after which the run-target chooser appears for you. Run this card now works out of the box on a single-user install. The token is listed in core **Settings → Agent tokens** as "Board <name> automation" (minted by board <id>) and can be revoked there; replacing or removing it, or deleting the board, revokes it. Pasting a token stays available (**Use a different token…**). The board never sees your core cookie, and for this it uses a dedicated key rather than core's internal key: it presents (`BLERG_CORE_BOARD_MINT_KEY`) that opens only this mint and its revoke, and core requires your own live browser session, refusing agent, session and daemon tokens, another account's, and more than 10 attempts an hour.
- Hermes agent sessions discover named model providers, preserve explicit provider selection through switches and native recovery, and keep local-budget claims conservative across configured targets.
- Live → Session lets owners enable or revoke child-session approval across currently available engines for the parent session, with separate sandbox and unsandboxed permissions and explicit standing-rule disclosure.
- Checklists show current work and explicit native-subagent or child-session assignments, with stable item references that survive rename and reorder.
- Hermes agent sessions use the native streaming gateway for tools, steering, questions, plans, children, background process inventory, model capabilities and token accounting, with exact native host-conversation recovery and explicit unsupported-runtime limits.
- Document local vLLM setup for Hermes, local-budget checks, sandbox reachability and child-session permissions.
- A session running on the cluster can start child sessions that run as cluster pods (`blerg-runner session start --runtime cluster`, the default there), with the same approval card ("on the cluster", chat 0.27.1), rules, session grants, budget, child routes and discard cascade as desktop children. Cluster rules and grants are separate from desktop ones; a desktop session still cannot start a cluster child.
- Hermes sessions on the cluster can use the operator's own model servers: set `HERMES_PROVIDERS_FILE` in `install/k8s/.env` to a Hermes provider fragment, and every Hermes pod merges it into its configuration, the cluster launch sheet lists those servers' models with the same IDs as on a desktop, and Hermes counts as local hardware when every endpoint is private and keyless. The example session NetworkPolicy gains an opt-in rule for reaching those servers.
- Proposals show in the session's chat as a card (chat 0.28.0): Approve, Edit (the draft editor in a sheet), Reject, or Allow for this session, which also approves the session's later proposals of exactly that scope (one board, or one connection's one tool). A created card follows as a compact ticket with Open, and becomes a chip in the History tab. The Proposals page stays the inbox, and a decision in either place is the same decision.
- One **Session permissions** block in Live replaces the separate child-approval checkboxes and the child-permissions row: toggles for starting children, filing cards on a board and running an MCP write tool without asking, and every permission in force with a styled Revoke. The launch sheet's **Permissions** gives the same ones before the session starts. Session permissions are owner-only, end with the session (and with a parent's discard), are refused for cron, restricted, board and agent-token sessions, and every automatic approval goes through the person's own approval path and records the permission that allowed it. A permission approves at most 20 proposals an hour (`BLERG_RUNNER_GRANT_HOURLY_CAP`), an MCP-tool permission stops when the tool's definition changes, and the same draft filed again within 15 minutes returns the earlier filing instead of a second card. Needs the updated session CLI for `ticket file` to report a card filed at once.
- Session permissions move into a **Permissions** dialog with a search: child sessions, board filing (a checkable list of your boards with a selected count and **Select all shown**), MCP write tools grouped by connection, and everything active with how and when it was given. Changes apply on **Save** with a one-line summary ("Grant 2, revoke 1"); the dialog is keyboard-first and a full-screen sheet on a phone. Live → **Permissions** is now a compact summary: the active count, **Manage**, and a chip with × per permission. The launch sheet's **Permissions…** opens the same dialog and counts what it will give. The proposal card's link now names **Permissions** (chat 0.29.1).

- A session can ask its owner, explicitly, to approve a file or diff it published: `blerg-runner review request <name> --ask "..." [--wait]`, or `publish`/`diff ... --request-review "..."`. The request is pinned to that version (a newer version supersedes it) and shows as "Review: <file>" under Live → Needs you and a card in the chat; the viewer gets a bar with Approve and Request changes (chat 0.29.0). Only the session's owner, signed in, decides; the decision, with the comments made on that version, reaches the session as a message or its waiting command. Runner migration `069`.

### Fixed
- Resolve desktop repository names consistently for cluster runs and children; precheck git read access before creating a pod. A configured default organization decides a bare name; otherwise only the desktop the start names (the launch sheet's machine, or a desktop parent's own) may resolve it, never another person's machine. The precheck sends a git credential only over https.
- Honor per-board execution targets across chat and runs; on installs with desktops and no cluster, use the only desktop or the one with the board's repository. Choosing a desktop's host for a board needs an explicit acknowledgement, recorded with who and when (Board migration `026`).
- Resolve board links from site metadata, read by the runner server-side (`GET /api/site-board`) since core sends no CORS headers; page large permission lists and show partial save results with retry.
- Confirm model effort changes in the session CLI; cover engine-home refusal and keep approval tests on the active hook.
- Decisions Blerg relays to an agent (a review request's outcome, a proposal's outcome) show in the chat as a **Blerg** note, not as the person's message (`@blerglab/chat` 0.29.2).
- A session's first prompt is recorded as written by whoever started the session: a board's card run or board session as the **Board**, a child's task as its parent session, a cron's prompt as the **Cron**. The chat draws it under that author, never as **You**, such a message never answers the agent's open question, and Insights no longer counts it (or a relayed decision) among the person's messages; a person's own launch is unchanged (`@blerglab/chat` 0.29.3). Sessions started before the upgrade keep what was recorded. No daemon update is needed: daemons since 0.3.0 honor the spawn frame's `initial_prompt_source`, and an older one records the prompt as the person's, as before.
- Codex sessions in a cluster pod with native conversation history get their login again, and Claude Code sessions there can write their settings: the history mounts under `~/.codex` and `~/.claude` made the container runtime create those directories as root, so Codex could not write `auth.json` ("permission denied") and its app-server kept failing. The pod now prepares each engine home as an agent-owned, pod-ephemeral directory; the login and config stay off the history volume.
- A child session's model is resolved against its engine's catalog where it will run: a routing id, a display name or a unique part of an id is accepted and recorded as the routing id, and an unknown or ambiguous name is refused with the available models (`blerg-runner models` lists them), instead of a child that fails on a made-up model route.
- A session running in a cluster pod can start child sessions and read `blerg-runner budget` again: the server recognized a cluster parent only while it sat on the placeholder cluster daemon, which its own pod replaces as soon as it connects, so every start was refused with "children run on the parent's desktop daemon". Cluster rules, grants and requests are keyed on the cluster, so they survive a resume into a new pod.
- An agent whose ticket filing or MCP call waited for approval now hears the decision: when the person approves or rejects it (or a session permission approves it, or the approval ends failed or unknown), the session gets a message such as "Proposal <id> approved: Launch Night #6 'Live check' created". `blerg-runner ticket status <id>` confirms it, and `ticket file` says the outcome will arrive as a message. Rejecting through the API takes an optional note, which the agent is told. Needs the updated session CLI for `ticket status`.
- Answering a review no longer posts a JSON file card per reply: the session's `.review.json` versions stay out of the chat (still in Files), and each round of answers is one line, such as "11 requests answered", that opens the review (chat 0.27.0). `blerg-runner review reply` takes several answers in one call, or `--file`, and publishes once.
- Live → Running now counts and lists started sessions that are still running, in the same compact row as Started sessions; a row opens the child.
- "Run this card" can start the session in one of your connected desktops' sandboxes or on the cluster, instead of only where the board is configured to run; the choice defaults to the board's setting, is offered to the owner of the board's automation token, is kept when the card's worker is respawned, and the repository is resolved to that desktop's checkout. Other automated board runs are unchanged.
- Board access from a session on a workstation daemon works: the daemon dials the MCP gateway through the server's web address it already uses, instead of the gateway's in-cluster address, and a session on This machine can take a board (the board alone, never other connections). The runner serves the gateway for them on its web port under `/mcp-gw/`, only for grants issued for that route, with uniform refusals, address throttling and a seven-day idle limit; `BLERG_RUNNER_MCP_GW_PUBLIC=off` turns that route off. Needs the updated daemon.
- Discarding a parent session stops and archives all its children, including Keep running workers, while retaining conversations and history; incomplete stop delivery stays retryable without allowing recovery.
- Child-session permissions collapse into one sidebar row, separate from started children, with clear scope and quiet revoke controls.
- Model pickers and session details use concise Nemotron family names, retain full routing IDs in tooltips, and distinguish otherwise ambiguous choices.
- Thinking messages render Markdown code, lists and emphasis using the same safe renderer as answers.
- Pending child-session approvals can be reviewed and decided from Live even when their conversation card is outside loaded history.
- Started sessions show live tool activity, reported usage and explicit pending usage across engines; Hermes forwards native usage snapshots without adding billable turns.
- Resumed Hermes workers retain bounded display identities that remain assignable to checklist items.
- Long error messages wrap inside the transcript instead of creating a horizontal scrollbar when older history loads.
- An open board shows cards created, changed, moved or archived by scripts, agents and other tabs within a second, after a plain reload too; before, it only showed them on the next reload. Running a card whose session then fails to start updates other open boards as well.
- Hermes agent sessions run without command-approval prompts, like Claude and Codex, including in the Docker sandbox; any approval Hermes still raises is asked in the chat, answerable only by the session's signed-in owner with exactly one offered choice, instead of being withdrawn and stopping the agent.
- Checklist assignments stay beneath full-width item titles, with compact metadata and faint item dividers.
- Checklist navigation uses two soft row pulses without a selection outline or a separate spinner highlight.
- Background-task waiting banners stay compact, with full commands available in keyboard-accessible, scrollable details.
- The diff sidebar highlights the file currently visible while scrolling or selecting files.
- Diff review keeps the file list and code independently scrollable on desktop and phone screens.
- Recorded tool failures use neutral styling with accurate success/failure totals and full details; red remains reserved for explicit run errors and attention states.
- Collapsed tool groups show success and failure totals without repeating failed output; expand a call for its full details.

### Changed
- Test readiness waits tolerate loaded CI runners while preserving timeout and cancellation behavior; websocket tests wait for registration and peer closure explicitly.
- First start asks for your username and password instead of logging a generated admin password. With no account yet, core is in setup mode: the first person creates the admin on a "Create your account" page (`/setup`) and is signed in, with no forced password change. On the desktop, `./blerg-up.sh` opens that page on localhost, with no code needed; until setup is done, any user, process or container on that machine could claim the admin, so finish it right after installing (`BLERG_SETUP_ALLOW_LOCAL=false` in `.env` requires a setup link there too). On a cluster, the page needs a single-use setup link (`/setup#code=…`, valid 24 hours, a credential until used) that `blerg-core setup-link` prints, and that `deploy.sh` prints at the end of a fresh install when run on a terminal. Core never logs the link. Setup closes for good once an account exists.

### Security
- Security hardening for session refresh.

## [0.4.2] - 2026-10-19

### Upgrade notes
- Desktop: reinstall the daemon and rebuild the sandbox image (`docs/sandbox-image.md`) so sandboxed
  child sessions can commit in Git worktrees and have Go; existing containers keep their old image.

### Fixed
- A child session drawn under its parent in the sidebar no longer repeats "from {parent}".
- A sandboxed child session reports its runtime as `docker` (it said `daemon`).
- Child-session rows show the token unit once and one clearly labeled revoke control per engine, runtime and desktop.
- Allowing child starts for a session covers every repository, including no repository, on the approved engine, runtime and desktop; duplicate permissions are consolidated without losing their audit records.

### Added
- Explicit child-session discard stops and archives workers from the CLI, panel and sidebar while preserving history, Insights totals and direct URL access. Children stay in a stable list until discarded.
- Sandboxed coding children can commit in linked Git worktrees and run Go builds/tests with the default image; shared Git metadata is validated and mounted without adding host Git push credentials. The default sandbox now includes Go 1.26.9 alongside Node.js 22.

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

[Unreleased]: https://github.com/blerglab/blerg-ai/compare/v0.4.4...HEAD
[0.4.5]: https://github.com/blerglab/blerg-ai/compare/v0.4.4...v0.4.5
[0.4.4]: https://github.com/blerglab/blerg-ai/compare/v0.4.3...v0.4.4
[0.4.3]: https://github.com/blerglab/blerg-ai/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/blerglab/blerg-ai/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/blerglab/blerg-ai/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/blerglab/blerg-ai/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/blerglab/blerg-ai/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/blerglab/blerg-ai/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/blerglab/blerg-ai/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/blerglab/blerg-ai/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/blerglab/blerg-ai/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/blerglab/blerg-ai/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/blerglab/blerg-ai/releases/tag/v0.1.0
