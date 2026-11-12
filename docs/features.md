# Blerg feature inventory

This is the product and operator feature inventory for Blerg. It is also the checklist for engine parity reviews and adding an engine. Features are grouped by behavior, not by every endpoint, configuration variable, button, or internal helper. A feature existing here does not promise that every engine or runtime supports it.

Initial inventory: source at `fc748ec`, reviewed 2026-10-16. Updated for the isolated 0.3.0 implementation; this does not imply deployment. See [the parity and abstraction review](design/engine-parity-review.md) for findings and coverage, and [Adding an engine](adding-an-engine.md) for implementation work. The adjacent Inkhold application is a consumer, not a Blerg component; its integration findings are recorded separately in the review.

Stable identifiers make omissions and changes reviewable. When adding, removing, or materially changing a feature, update its row, its implementation link, and the applicable parity tests. Do not renumber existing identifiers. New provider-native features should say which engines and runtimes actually implement them.

Session board tabs and the Board app share the column, card and facet-filter
presentation in `packages/board`. Columns are capped at 290 px, including on
phones; card titles use regular weight with inline references and metadata.
The tab's compact Filter menu and release select sit beside search and the
quiet Open in Board link. Facets narrow the loaded page set; a board larger
than the tab's 1,000-card bound asks the person to narrow search or release.
Clicking a tab card opens its live details beside the board; another click swaps
the card. Escape or the close icon dismisses it. On a phone-sized panel the
details occupy the full content area. The existing ticket details supply the
description, status actions and trail.
Both hosts offer Columns inside Filter. Any subset can be hidden, including
all columns; a persistent notice offers Show all columns. Browser-local choices
are keyed by person and board, and newly added columns start visible. Hidden
column counts remain visible and a successful card move names a hidden
destination. An unknown identity can customize its current view without saving.

The runner's left dock includes Channels, Files and Repositories tool
windows. Desktop tools stack or merge; phones switch tabs. Tabs switch tools
without a window collapse button. The folder toggle opens and closes the desktop
dock; keyboard shortcuts and the phone sheet’s Back remain available. Repository
state reads retry temporary reader contention; failed or incomplete reads show
**Couldn’t read state** and **Retry**. Repositories show the current authorized members in padded rows with
name, branch, and state in plain words. Indented branch lines use existing active
chat data without extra Git reads; hover, keyboard focus, or a phone long press
previews the title, engine, runtime and status. Clicking a branch opens its chat.
Long branch names truncate in the middle. There are no usage counts. Host and sandbox
workspaces add and remove repositories there; each change waits for a safe turn
boundary, shows as pending and states any blocker on its row (see
[Live repository membership](design/workspace-membership.md)).
Main’s repository requests also appear in chat, Needs you, Questions, and the workspace
attention count. All surfaces decide the same persisted proposal.
Main can supply a credential-free HTTPS or Git SSH URL to `blerg-runner repository add`.
Its host must already be connected in Settings. Approval names the host, owner and repository;
only then does Blerg clone over HTTPS into the repositories folder and resolve the
local addition. URL rewrites, redirects and transport fallback are disabled. Failed
clones leave no partial destination and show a plain, dismissible refusal in the dock.
Settled requests leave chat, Questions and Needs you; failures are also relayed to Main.
Clones run in the background with separate bounded capacity from discovery. Folder
workspaces receive new clones inside their selected folder; destinations include the
complete repository identity so repositories with the same name do not collide.
Public clones carry no token; private host clones retain the desktop's personal-token opt-in policy.
Cluster workspaces still refuse repository changes. The dock omits declined operations;
genuine refusals have a dismiss icon, remembered for that operation across reloads.
Question dialogs show Markdown at body size under a short title and muted
“Asked by” line, with a growing five-line composer and a labeled primary Answer
button. Needs you shows answerable questions in full before opening their reply dialog. Dialogs
scale with the viewport; long question text scrolls above the answer controls.
Bound approvals retain their action title, snapshot and existing answer controls.
Each repository also has a collapsible branch graph. The graph follows local
commit parents and labels branch tips, including synced wip branches. Open graph
tab focuses that repository at reading width; commit buttons open diffs in Blerg.
History starts at 50 commits per repository; More history increases only that
repository to at most 200. Collapsed graphs make no history reads. Temporary
read contention retries while retaining the last graph.
History reads as a compact list beside a gutter capped at four reused lane
columns. Tags label their commits; release details list every commit in a range
only when its boundaries are known in loaded history. Empty or unknown release
ranges have no row. Branch chips proven merged into the default branch are hidden
unless Show merged is selected; unknown ancestry stays visible. Their commits
remain in history. On phones, subjects occupy a second line below chips, SHA and time.
The reader favors recently updated branches, always retains main and the default
branch, and places the default branch in the first lane. Older or unreadable
branches are omitted with a notice. History traversal is bounded by the selected
depth and branch count, including without a Git commit graph.
The reader returns at most 64 local branch refs and never reads worker checkouts
or uncommitted worker state. Missing branches or objects remain unavailable until
their commits are delivered to the repository. No repository write or network
fetch is performed.
Git reads reuse the owner-authorized
workspace endpoint, run serially, and refresh after turns or returning to the app;
hidden tools do not poll. Missing or partial status never implies clean. Delivery warnings use the existing recorded per-member pending/error
results. This view does not add, remove, discover or adopt workspace members.

The sidebar tools share the workspace tab strip and split-group renderer (#372), including
insertion markers, edge and center drops, responsive stacking, resize handles and arrow-key
navigation. Dock tools and workspace panels have separate drag scopes. Selecting a tab keeps
its saved order; dragging changes the order. Older stacked dock layouts migrate automatically.
The phone dock stays a switch-only sheet and preserves the desktop splits.

The workspace panel remembers open and closed choices per account and workspace. A workspace
without a choice opens the panel automatically; after a close, a new question or tool change
reopens it once. Older browser-wide choices remain a migration fallback; new choices never
change another workspace. Saved layouts and Undo restore panel visibility and width.


On desktop, a single click or Enter on a dock chat previews it in the same
replaceable tab slot as files. Double-clicking the row or sending a message keeps
the chat open. Phone chat tabs stay permanent. Main is an ordinary closable tab;
the Channels sidebar reopens it and highlights only channels visible in panels,
with a stronger highlight for the focused panel. Saved layouts retain earlier
tabs. Main and channels use one shared
speech-bubble icon in dock rows and tabs; workers use the shared diamond.

Channel and worker chats open published files (including decks, images, PDFs,
Markdown, charts, diagrams and diffs), workspace files, review/mark-up, tickets,
commits (including Open original/Open changed file revisions) and popped-out
messages/visuals in the workspace's existing desktop tab
strip. File tabs retain the originating chat's file access, live updates and
review context, including after closing its chat tab. Identical workspace paths
in different chats keep separate tabs. Preview/keep, close/reopen and keyboard
navigation use the same controls as Main. New viewer requests on phones keep
the modal fallback.

Board card runs, discussions, reviews and standing agents use every repository targeted by the card. Multi-repository desktop starts require workspace discovery; cluster starts authorize and check credentials for every member before launching. A missing or ambiguous member refuses the whole start (#64).

People open **Workspaces** from the sidebar. **Main** is the orchestrator. The dock's
**Channels** tool puts Main first, followed by chats started by the person. **Workers**
are started by agents and remain workers when opened. Workers collapse to a count
and state glyphs; a blocked or failed worker is named on the collapsed row. Expanded
worker rows are smaller, dimmer and use a non-chat icon. Runtime glyphs have Host,
Sandbox or Cluster tooltips and accessible text; the collapsed group shows a runtime
only for host or mixed runtimes. Empty channel sections have no header. The right
panel's **Shared** tab holds published files and personal attachments; left-dock
**Files** browses repositories. Scheduled unattended work is a **run**. API, database,
CLI and agent-guide terminology remains session; stored layouts retain their keys.
Workspace cards count every nested channel and worker separately, even during search.
Channel unread is aggregated onto the workspace; delegated workers stay quiet, while
standalone agent workspaces and orphans retain their own unread and waiting signals.
Phone cards count failed and blocked chats. Worker attention includes person questions,
proposals, tool changes, reviews and answer pages, read from the existing live contract.
Links and selected side chats reveal their worker row again when the phone sheet reopens.
End and discard actions use the conversation's role. Idle glyphs use a fixed,
non-wrapping box in dock rows at every width.

## Accounts, identity, and platform discovery

These features are shared control-plane services, independent of the coding engine.

| ID | Feature | Implementation / contract |
| --- | --- | --- |
| CORE-01 | Local account login; first-run setup (the first admin chooses a username and password on `/setup`, with a single-use setup link or, on the desktop, from localhost); required password change after an operator-issued one-time password | [Authentication](../core/internal/api/auth_handlers.go), [setup](../core/internal/api/setup_handlers.go), [local provider](../core/internal/authprovider/local.go), [setup codes](../core/internal/authprovider/setup.go) |
| CORE-02 | External GitHub or OIDC login, callback/state handling, and account resolution | [Auth providers](../core/internal/authprovider/) |
| CORE-03 | Browser sessions, short-lived audience-specific access tokens, refresh, and single-device logout | [Human sessions](../core/internal/identity/human_session.go), [auth API](../core/internal/api/auth_handlers.go) |
| CORE-04 | Logout everywhere, account/token revocation, and lineage checks | [Identity](../core/internal/identity/), [shared verifier](../contracts/identity/) |
| CORE-05 | Change password and username, identity/profile display | [Auth API](../core/internal/api/auth_handlers.go), [identity API](../core/internal/api/me_handler.go), [web app](../core/web/src/App.tsx) |
| CORE-06 | Platform roles and capability enforcement; account reconciliation and disabled-account handling | [Roles](../core/internal/identity/roles.go), [reconciliation](../core/internal/identity/reconcile.go) |
| CORE-07 | Project records, membership roles, and project capability resolution | [Projects](../core/internal/projects/service.go), [router](../core/internal/api/router.go) |
| CORE-08 | Named agent tokens: preset/audience, bounded expiry, reveal once, list last use, revoke, and remint | [Agent tokens](../core/internal/identity/agent_tokens.go), [Settings](../core/web/src/Settings.tsx) |
| CORE-09 | Internal scoped session/cron tokens, renewal/revocation/status, and delegated board token exchange | [Internal token API](../core/internal/api/routes_internal_tokens.go), [exchange](../core/internal/identity/exchange_tokens.go) |
| CORE-10 | Signing keys, JWKS publication, audience and capability verification, and revocation distribution | [Signing](../core/internal/signing/), [shared identity](../contracts/identity/), [router](../core/internal/api/router.go) |
| CORE-11 | Component registration, heartbeat staleness, aggregate agent manifest, and Markdown agent guide | [Discovery](../core/internal/discovery/registry.go), [manifest contract](../contracts/agentsmanifest/) |
| CORE-12 | OpenAPI publication and self-describing component operations/authentication | [Core API](../core/internal/api/openapi.json), [agent handler](../core/internal/api/agents_handler.go) |
| CORE-13 | Signed-in component landing page, bounded health probes, site links/branding, and build identity | [Web app](../core/web/src/App.tsx), [shell](../core/web/src/Shell.tsx), [landing](../core/internal/landing/) |
| CORE-14 | Browser authentication refresh guard, origin/CSRF checks, login limiting, and safe redirect handling | [API auth](../core/internal/api/auth_handlers.go), [login limiter](../core/internal/api/login_limiter.go), [refresh guard](../core/web/src/refreshGuard.ts) |
| CORE-15 | Registered engine integration and plugin metadata for credential/settings UI | [Descriptors](../contracts/engines/), [Core endpoint](../core/internal/api/engine_descriptors.go), [Settings](../core/web/src/Settings.tsx) |
| CORE-16 | "Run as me" mint for boards: a run-sessions token for the person signed in to the board, proved by their live board access token, with its own key, per-account rate limit, `board <id>` source in Settings, and board-scoped revoke | [Board automation mint](../core/internal/api/internal_board_automation.go), [identity](../core/internal/identity/board_automation_tokens.go), migration `022` |
| CORE-17 | In-app What’s New after an upgrade, remembered per account on dismissal, with a quiet first sign-in, bundled release history, and keyboard dismissal; blocking questions and unattended workers take precedence | [Account API](../core/internal/api/whats_new.go), [release notes](../core/internal/releasenotes/), [dialog](../runner/frontend/src/components/WhatsNew.tsx); engine independent |
| CORE-18 | Administrator People: single-use member invitations, desktop setup, persistent account disable and audited desktop reassignment | [People and desktop setup](admin-people.md), [account API](../core/internal/api/people.go), [People UI](../core/web/src/PeopleSection.tsx) |

## Credentials, plugins, and external connections

| ID | Feature | Implementation / limits |
| --- | --- | --- |
| CRED-01 | Per-account opaque engine/git credential import, replacement, metadata listing, and deletion | [Vault service](../core/internal/credentials/service.go), [API](../core/internal/api/credential_handlers.go), [Settings](../core/web/src/Settings.tsx); accepted kinds are explicitly enumerated |
| CRED-02 | Encryption at rest with local AES-GCM or Vault Transit | [Key backends](../core/internal/keybackend/) |
| CRED-03 | Vault Kubernetes authentication, renewal/relogin, or operator-provided static token | [Vault backend](../core/internal/keybackend/vault.go) |
| CRED-04 | Private authorized credential delivery using live account-bound token/session proof | [Internal API](../core/internal/api/internal_handlers.go), [managed API](../core/internal/api/managed_credential_handlers.go) |
| CRED-05 | Managed device login, polling, cancellation, reconnect status, and discovered login capabilities | [Managed credentials](../core/internal/credentials/managed.go), [UI](../core/web/src/ManagedCredentials.tsx); Codex adapter currently registered |
| CRED-06 | Centralized refresh authority, encrypted refresh state, generation fencing, refresh claims, and lifecycle audit | [Managed service](../core/internal/credentials/managed.go), [adapter contract](../core/internal/credentials/adapter.go) |
| CRED-07 | Access-only session credential refresh without distributing managed refresh authority | [Runner credential broker](../runner/internal/server/enginecredentials.go), [daemon credential contract](../runner/internal/daemon/enginecredentials.go) |
| PLUG-01 | Ordered per-account always-on plugin settings, replace/remove, marketplace policy, and entry validation | [Core plugins](../core/internal/plugins/service.go), [shared grammar](../contracts/pluginspec/), [UI](../core/web/src/PluginsSection.tsx); Claude/Codex only |
| PLUG-02 | Native engine plugin preparation and per-session snapshots | [Daemon engines](../runner/internal/daemon/engines.go), [Codex preparation](../runner/internal/daemon/codexplugins.go), [plugin documentation](../install/desktop/DAEMON.md#always-on-plugins) |
| PLUG-03 | Owner-approved plugin requests, session-only or additive always-on selection, between-turn reload, and explicit new-session-only outcomes | [Runner workflow](../runner/internal/server/sessionplugins.go), [daemon reload](../runner/internal/daemon/sessionplugins.go), [protocol](../runner/internal/protocol/sessionplugins.go), [panel](../runner/frontend/src/components/SessionPlugins.tsx); live reload: unrestricted host Claude only |
| CONN-01 | Named account-owned HTTP MCP connections, metadata, rename/update/delete, and status/events | [Connection service](../core/internal/mcpconn/service.go), [UI](../core/web/src/McpConnectionsSection.tsx) |
| CONN-02 | Bearer/header-secret, OAuth, and built-in board connection authentication | [MCP connection service](../core/internal/mcpconn/), [connection guide](mcp-connections.md) |
| CONN-03 | OAuth discovery/registration, consent callback, reconnect, refresh, and credential isolation | [OAuth implementation](../core/internal/mcpconn/oauth.go), [token rotation](../core/internal/mcpconn/oauth_tokens.go) |
| CONN-04 | Default tool allowlists, tool definition pins, and change policy | [Connection service](../core/internal/mcpconn/service.go), [guide](mcp-connections.md#tools-are-off-by-default) |
| CONN-05 | Private connection-token/default retrieval with live proof and connection event audit | [Internal connection API](../core/internal/api/internal_mcp_defaults.go), [routes](../core/internal/api/routes_mcp.go) |
| CONN-06 | URL validation, DNS/address policy, redirect refusal, and outbound request limits | [Network guard](../contracts/netguard/), [connection policy](../core/internal/mcpconn/pin_policy_test.go) |

## Workspace launching and control plane

Workspaces on a desktop or on the cluster can start and drive their own workers with owner
approval, standing rules or workspace grants; a desktop parent's children run on that desktop or,
on an install with a cluster runtime and on request, as cluster pods; a cluster parent's as cluster
pods. Child controls, files, live status and separate usage totals share the
engine-independent runner services; nesting is limited to one generation. See
[starting workers](talking-to-an-agent.md#when-the-agent-starts-workers). Children remain in a stable worker list until explicitly discarded through the CLI,
panel or sidebar. Discard stops and archives the child, preserving searchable History,
Insights totals and direct URL access; archived workers cannot resume. A stopped child can be
archived by its human owner without a live parent. Unfinished workspace cleanup retains all
work and reports a retryable failure. Discard requests and receipt decoding wait at most 35
seconds in the browser; the confirmation can be dismissed even while pending. A daemon
restart that cannot recover a session replaces stale running state with a pause and its plain
reason. Stop on an unhosted session reports why cleanup cannot be verified instead of claiming
success; it does not delete retained work.

Implementation roots: [server](../runner/internal/server/), [DB](../runner/internal/db/), [MCP](../runner/internal/mcp/), [gateway](../runner/internal/mcpgw/), [protocol](../runner/internal/protocol/). Evidence paths below are relative to those roots unless a full path is shown. GAP references identify findings in the accompanying review.

| ID | Feature/surface | Claude | Codex | Evidence, constraints and tests |
| --- |---|---|---|---|
| RUN-01 | Public agent/component discovery, OpenAPI, version/package URLs | shared | shared | `agents.go`, `openapi.json`, `coreregister.go`, `packages.go`; authenticated dynamic launch discovery is separate from the public component manifest. |
| RUN-02 | Browser HTTP session launch | supported | supported | `api.go:618` spawn request engine/model/effort/runtime/kind; validation/dispatch later in file; `model_effort_test.go`, `spawn_validation_test.go`, `api_test.go`. |
| RUN-03 | Programmatic REST headless start | supported | supported | `runner.go`, `engine_targets.go`; engine/model/effort/interaction/scratch/auto-stop/callback/idempotency; engine-aware placement and explicit host pins. |
| RUN-04 | MCP headless start and launch discovery | supported | supported | `mcp/tools.go`, `mcp/server.go`; start/discovery delegate the same authorized services as REST. |
| RUN-05 | Launch idempotency and replay | shared | shared | `runner.go` request hashing/stable retries; `StartRequest` engine participates in body; `runner_test.go`, `model_effort_test.go:307`, MCP retry tests. |
| RUN-06a | Per-session verbosity: Narrated / Standard / Terse | shared | shared | Account default, launch choice, next-turn changes, Terse children; shared session guidance on all engines; `verbosity.go` and `VerbositySelect.tsx`. |
| RUN-06 | Interaction mode / watched vs unattended | shared | shared | `interaction.go:35-69`; browser defaults interactive, tools unattended, cron always unattended; preserved on resume; `interaction_test.go`. |
| RUN-07 | No-repo scratch sessions | supported | supported | `norepo_test.go`, `engine_placement_test.go`; scratch placement honors requested engine/runtime and explicit host pins. |
| RUN-08 | Host execution and local Docker runtime | supported server dispatch | supported server dispatch | runtime selection `runner.go`, daemon selectors `hub.go`; Docker/bare-host permissions distinct; daemon engine operation outside scope. |
| RUN-09 | Cluster execution / scheduling / capacity | supported | supported | `k8sjobs.go:965` launch/secret/spec flow; per-runtime config/capacity `clustersettings.go`; `k8sjobs_test.go`, `startstages_test.go`. |
| RUN-10 | Principal/target-scoped available engines | supported | supported | `launch_options.go`, `launch_readiness.go`, `engine_targets.go`; known/unknown/empty states, asynchronous readiness, personal-only option for crons. |
| RUN-11 | Model catalog and per-model efforts | supported public catalog | supported native account/CLI catalog | `models.go`, `clustermodelproviders.go`, `clustermodelcache.go`; account-bound headless callers and operator keys use separate authorized scopes. |
| RUN-12 | Backend cached catalogs / startup restore / bounded refresh | supported catalog source | supported account/engine/image cache | `clustermodelcache.go`, `db/clustermodelcatalog.go`, migrations047/048; asynchronous refresh/last good snapshot; `clustermodelcache_test.go`, `models_test.go`. |
| RUN-13 | Launch model/effort validation | supported | supported | `models.go:191-235`; engine rules plus known model efforts; per-daemon validation only where target known. Headless unknown-target only allowlist documented; `model_effort_test.go`. |
| RUN-14 | Live model/effort control | WS/REST/MCP | WS/REST/MCP | `runner_model.go`; one authorized setter, immutable native engine, next-turn dispatch acknowledgement and effective native metadata. |
| RUN-15 | Session listing/title/star/read/privacy metadata; renames by the owner (or an administrator), the parent (`session rename`) or the agent (`title`), each in History | shared | shared | `api.go`, `session_rename.go`, `db/queries.go`, browser WS mark/read/star, privacy guards; `api_test.go`, `session_rename_test.go`, `privacy_test.go`, `privacy_scope_test.go`, `privacy_guard_test.go`. |
| RUN-16 | Start progress and stage diagnostics | shared | shared | `startstages.go`, `startstages_test.go`; ordered persisted start_stage plans, native/pod reports. |
| RUN-17 | Native conversation recovery on Cluster | supported registered layout | supported registered layout | `nativehistory.go:47,122,218,287`, nativehistory registry; owner/engine/PVC UID/writer fence, opt-in storage/capacity/retention; `nativehistory_test.go`. Workspace is separate, not backed up by conversation PVC. The engine home (`~/.codex`, `~/.claude`) is an agent-owned emptyDir prepared by the init container, so logins never reach the PVC; `nativehistory_enginehome_test.go`. |
| RUN-18 | Daemon reconnect reconciliation and lost-session states | shared | shared | `daemon_conn.go:701-890`, `daemongrace.go`, `sessionpaused.go`, `jobreconcile.go`; actual native restart retention owned by daemon reviewer; [background-task lifetime reconciliation](design/background-task-reconciliation.md) closes abandoned work and repairs counts atomically. |
| RUN-19 | Pause / restart pause / explicit user continuation | shared | shared | `sessionpaused.go`, browser request handlers, `SessionPaused`; preserved asks; `sessionpaused_test.go`, `jobreconcile_test.go`; no assumption ended sessions can resume. |
| RUN-20 | Cluster owner-authorized resume and fresh credentials | supported | supported | `session_recovery.go`, `runner.go`, `k8sjobs.go`; fresh caller proof, retained-history checks, native vs legacy transcript reconstruction, visible launch failures. |
| RUN-21 | Resume with MCP connections | intentional authority limit | intentional authority limit | `grantResumeProblem`, `reissueGrants`; owner fresh browser proof required for these grants; ordinary cron restrictions retained; not arbitrary shared-key revival. |
| RUN-22 | Resume when provider limit resets | supported | supported | `limit_resume.go`, descriptor capability; provider-owned signals/reset times and one-shot durable dispatch. Additional engines default to unsupported. |
| RUN-23 | Provider usage limits in daemon panel | supported report transport | supported report transport | `usage_limits.go`, engine descriptors and protocol usage fields; sanitized subscription/spend percentages and unknown resets, daemon-only in-memory cache. Claude spend meters from 0.2.1 coexist with Codex windows. Cluster emits none by design at this layer. |
| RUN-24 | Durable message acceptance and receipt replay | browser/REST/MCP | browser/REST/MCP | `sessioninputs.go`, `runner_controls_test.go`; optional stable client IDs, duplicate-content checks, receipt-aware hosts, visible unsupported/permanent failure. |
| RUN-25 | Mid-turn input and message routing | supported transport | supported transport | shared `AgentUserMessage`, SendMessage/browser WS; native steering/queue semantics belong daemon adapter reviewer. |
| RUN-26 | Session questions/ask/update/note | supported native/CLI routing | supported special native routing | `messages.go:120`, `messageevents.go:97-160`; Codex ask marker/message transaction, source-aware chat answer forwarding; `messageevents_test.go`, `messages_test.go`. Native choices/child RPC support upstream scope; no typed choice contract here. |
| RUN-27 | Interrupt, stop, pause/delete and end attribution | shared | shared | `runner.go`, `sessionend.go`, `sessionpaused.go`, `sessions.go`, `db/session_end.go`; explicit user/system origin; final token/grant cleanup; `sessionend_test.go`, `autostop_test.go`. |
| RUN-28 | One-shot auto_stop | supported | supported | `autostop.go:52`, HandleAgentEvent turn_done persisted then stop; defers pending continuation; `autostop_test.go`, `limit_resume_test.go`. |
| RUN-29 | Structured results / final answer / failure | shared | shared | `result.go:169` excludes child assistant text and uses last turn; engine-neutral normalized events; `result_test.go`. |
| RUN-30 | Signed callbacks / retries / SSRF handling | shared | shared | `webhook.go`; HMAC/callback_secret, no redirects, bounded retries/address guard; `webhook_test.go`. Engine-independent terminal trigger. |
| RUN-31 | Native and terminal output + scrollback transport | shared | shared transport | `sessions.go:452`, browser subscribe/output/resize/scrollback; terminal capability/engine behavior delegated daemon. |
| RUN-32 | Durable agent events / transient stream / ack/replay | shared | shared | `agentevents.go:73`, `ingestorder.go`, `hubgap.go`, `events_live.go`, `events_stream.go`; per-event IDs/seq, missing replay/slow subscriber refill; `ingestorder_test.go`, `events_live_test.go`, `events_stream_test.go`. |
| RUN-33 | Tail/history paging and child context | shared | shared | `replaycontext.go:16`, `db/replaycontext.go`; includes subagent_update and bounded historical upper fence; `replaycontext_test.go`, `replaytail_test.go`. |
| RUN-34 | Live session aggregate / Running Now / status-aware subagents and running child sessions | shared | shared consumer | `livestate.go:60,142`, `db/livestate.go`, shared livestate reducer; human-only view intentional; `livestate_test.go`. Running Now counts and lists running started sessions with the subagents and background tasks (`SessionRail.tsx`). Correct native child emit coverage daemon scope. |
| RUN-35 | Loaded session capabilities | supported normalized payload | supported normalized payload | `agentevents.go:264`, `protocol/capabilities.go:13-182`; bounded six groups, unknown vs empty; sanitize/persist/replay tests in `capabilities_test.go`. |
| RUN-36 | Session Insights | shared with settled usage semantics | shared with settled usage semantics | `session_insights.go`, `db/insights.go`; child model/configured-effort updates, validated root/child token partitions, costs and counts; ambiguous attribution stays unknown. |
| RUN-37 | Install Insights / prices / Prometheus metrics | shared | shared | `insights.go`, `insights_calc.go`, `metrics.go`; owner-scoped dated failure, card-time, time-share and spending charts; previous-period comparisons; provider weekly pace and reset dates; deployment/release/milestone overlays. Model prices live under Spending. Settled usage only; missing prices, timing and board access remain explicit. |
| RUN-38 | Cron create/edit/run/pause/resume/renew/history | supported | supported | `routes_crons.go`, `crons.go:481,555`, `cronstart.go:130,532,604`; engine/model/effort retained, model clears on engine switch; migrations046; `codexgates_test.go`, `crons_test.go`, `cronstart_test.go`. |
| RUN-39 | Cron restricted authority / watchdog / grace / missed fires | supported | supported | `cronstart.go`, `db/crons.go`, cron scheduler; no bare host, one-shot, no operator fallback; `crons_f2_test.go`, `restricttools_test.go`. Scheduling algorithm package outside owned scope, integration traced. |
| RUN-40 | Cron built-in board grant and failure cards | shared | shared | `cronboard.go`, `cronfailurecard.go`; same scoped grant/tokens; `cronboard_test.go`, `cronfailurecard_test.go`. |
| RUN-41 | MCP connections/tools/defaults picker | supported | supported | `mcptools.go:35`, `mcpstart.go:591`; verified human controls grants; no arbitrary API `mcp` grant body accepted; `mcptools_test.go`, `mcpstart_sites_test.go`. |
| RUN-42 | MCP gateway grant attach/revoke and restricted tools | supported | supported explicit capability | `mcpstart.go:595-649`; Codex Docker requires codex_restricted; same Cluster spec/gateway; `codexgates_test.go`, `restricttools_test.go`. |
| RUN-43 | MCP tool definition pinning / changed schema review | shared | shared policy | `toolchanges.go`, DB definitions/pins/events, `RefreshSessionTools`; Codex native resume may retain advertised tool definitions (daemon/upstream intentional limit), removed grants remain server enforced. |
| RUN-44 | Proposed write approvals and outcome resolution | shared | shared | `proposals.go`, `routes_proposals.go`, `db/proposals.go`; owner approvals/snapshots/unknown resolution; proposal and G3 tests; actual gateway package separate but integration inspected. |
| RUN-45 | Always-on plugins | supported registered native installer | supported registered native installer | `plugins.go:124-132,195-216`, `codexcaps.go:18-47`; engine forwarded to Core, restricted sessions omit intentionally; `plugins_test.go`, `codexgates_test.go:48,66`. Some stale Claude-only comments remain. |
| RUN-46 | Agent config upload/download | shared bundle storage | shared bundle storage | `agentconfig.go:15,32,83`; daemon-only write, scoped unrestricted runtime read; actual file layout/engine application in daemon reviewer scope. One install-wide last-uploaded bundle, not account-specific; existing architectural policy, not a Codex regression. |
| RUN-47 | Personal credential first / operator fallback / foreign-provider filtering | supported legacy OAuth/API-key adaptation | supported opaque snapshot or managed access | `k8sjobs.go:680,1033,1112-1168,1284,1796`; fetch failures fail closed, verified absence may fallback, selected-engine-only keys; `k8sjobs_test.go`, `runtimeauth_test.go`. |
| RUN-48 | Managed refresh/access-only broker | not same managed mode needed here | supported | `enginecredentials.go:37,85,154`; immutable session/account/engine/live launch binding, only access delivery, no refresh credential to runtime; `enginecredentials_test.go`. Managed family/shape hardcoding ARCH-06 in the review. |
| RUN-49 | Messaging token healing on workstation | shared | shared | `sessiontokenheal.go:95`; current sessions/new token file capability, no resurrected MCP grant, no runner pod automatic heal; `sessiontokenheal_test.go`. |
| RUN-50 | Current-launch runtime websocket authority | shared | shared | `runtimeauth.go`, `daemon_conn.go`, `db/runtime_tokens.go`, `runtime_fence.go`; immutable repo/daemon UUID, replacement fence and separate message token; runtimeauth and adversarial/interleaving tests. |
| RUN-51 | Private session/data/token visibility | shared | shared | `privacy.go`, `boardauth.go`, `coreprincipal.go`, `startedby.go`; private owner-only; token totals owner/admin; explicit native-provider independent access rules; extensive guards. |
| RUN-52 | Repo lists/provider credential discovery, clone URLs/origins | shared | shared | `repos_handler.go`, `user_repos.go`, `repo_provider.go`, `github.go`; registered git provider API, account scoped caches, credentials separate from model provider; repo/provider tests. Account-bound discovery resolves the authorizing agent's owner and current proof. |
| RUN-53 | New repository creation / clone-from / workdir containment | shared | shared | `newrepo.go`, `api.go` launch fields; native git provider token/permissions bounded; newrepo/spawn validation/gitproviders tests. Actual engine git ability runtime-specific. |
| RUN-54 | Daemon repos-root admin / re-list | shared | shared | `reposroot.go`, `DaemonInfo`, correlated results; host-only execution targets; reposroot/hosttargets tests. |
| RUN-55 | Session artifacts/versioning, uploads, attachments, file visibility/deletion | shared | shared | `routes_artifacts.go`, `artifacts.go`, `uploads.go`, `files.go`, `runner_files.go`; own runtime/session authorization; artifact/upload/runner_files tests. |
| RUN-56 | Mockup/screenshot publication | shared | shared | `publish.go`; dedicated host separation/path validation; `publish_test.go`. Runtime `checkDaemonAuth`/scope policies, not engine branches. |
| RUN-57 | History topics/outcomes/commit links; the strokes feed (marks, asks, approvals, proposals, reviews) | shared | shared | `topics.go:34`, `marks.go`, `db/reel.go`, `topics/strokes.go`, topic/reel reducers; session messaging token and git-provider refs; topics/reel/strokes/marks tests. CLI access (`topic`, `mark`) supplied to both engines. |
| RUN-70 | Checklist work map: stable item IDs, multiple doing items, explicit session-scoped native/Blerg worker assignments and Plan navigation | shared | shared | [Checklist model](../runner/internal/checklist/), [session work state](../runner/internal/server/worklist.go), [agent guide](talking-to-an-agent.md#tickets-and-checklists); legacy replay, scoped assignment, concurrent mutation and UI identity tests. |
| RUN-58 | Project memories/knowledge search | shared | shared | `dataplane.go`, `dataplaneauth.go`; exact current repo, unrestricted runtime only; `dataplaneauth_test.go`, `dataplane_test.go`. Engine-specific tool exposure daemon scope. |
| RUN-59 | Runner-integrated boards/columns/tickets/dependencies/events | shared | shared | `boards.go`, `columns.go`, `tickets.go`, `boardauth.go`, DB board/ticket graph/rank/token files; same REST authorization, no engine checks. Assist uses an agent session and validates and forwards engine/model/effort. |
| RUN-60 | Push notices/unread/idle/waiting and background completion | shared | shared normalized events | `push.go`, `notices.go`, `sessions.go`, `livestate.go`; account/login-owned push, owned public/private sessions notify only their owner (ownerless public sessions target eligible accounts with a live subscription), live Core login required; unowned legacy rows deleted by migration 105 and silently re-registered; DELETE only caller-owned endpoints; Turn off waits for deletion, restores never prompt; suppress headless asks/notices, defer idle with running child work; `notices_test.go`, push/session tests. |
| RUN-61 | Health/config/version/registration/metrics auth | shared | shared | server main/config wiring and `coreregister.go`; version/publish admin paths no engine assumption; no release/restart behavior changed. |
| RUN-62 | MCP gateway protocol, per-session grants, upstream sessions/auth, call/result/concurrency budgets and audit | Shared | Shared | [Gateway implementation](../runner/internal/mcpgw/) |
| RUN-63 | Frozen write proposals, approve/decline/expiry, bounded queues and explicit unknown outcomes | Shared | Shared | [Gateway proposals](../runner/internal/mcpgw/proposals.go), [proposal guide](proposals.md) |
| RUN-64 | Canonical tool definitions, pins/drift policy, live revalidation and repin/change review | Shared | Shared enforcement; native advertised-tool refresh limit remains | [Tool drift](../runner/internal/mcpgw/drift.go), [live tools](../runner/internal/mcpgw/livetools.go) |
| RUN-65 | GitHub/GitLab remote parsing, repository list/create, clone/web URLs and token/redaction policy | Shared | Shared | [Git providers](../runner/internal/gitprovider/), independent of coding engine |
| RUN-66 | Cron timezone parsing, missed-fire/grace, persisted claims, watchdog and credential-failure maintenance | Supported | Supported | [Scheduler](../runner/internal/cron/), fixed restricted-engine policy remains |
| RUN-67 | Existing-session catalog discovery bound to immutable engine and actual host/runtime | supported | supported | `launch_options.go`, `runner_controls_test.go`; owner authorization, no fallback to another execution host. |
| RUN-68 | Live native MCP readiness and bounded first-input hold | Persistent Claude Code control protocol | Codex app-server thread status | [Shared tracker](../runner/internal/daemon/mcptracker.go), [live contract](../runner/README.md#mcp); owner-only release, durable snapshots, gateway scope, process restart tracking. Legacy engines without a live source remain explicitly unknown. |
| RUN-69 | Read a live session's workspace file | Shared host/container/pod primitive | Shared host/container/pod primitive | [Workspace read contract](../runner/README.md#opening-a-session-workspace-file); human owner/admin only, bounded reads, launch-folder identity, no symlink escape, protected names and hardlinks refused; Linux/macOS, other platforms fail closed. |
| RUN-71 | Workstation sessions reach the MCP gateway through the server's web address (sandbox and, for the board alone, This machine) | Shared | Shared | [Web-port mount](../runner/internal/server/mcpstart.go) (`RegisterMCPGatewayRoutes`: grants issued for it only, uniform 401, address throttle, seven-day idle limit, `BLERG_RUNNER_MCP_GW_PUBLIC`), [daemon](../runner/internal/daemon/mcpgateway.go) (`viaServerGateway`); `mcpgw_web_test.go`, `mcpgatewayvia_test.go`. |
| RUN-72 | Explicit review requests: a session asks its owner to approve one published file or diff at an exact version; owner-only decide, supersede on a newer version, decision delivered as a message or to a waiting CLI | shared | shared | [Review requests](../runner/internal/server/reviewrequests.go), [storage](../runner/internal/db/reviewrequests.go) (migration 069), [guide](artifacts.md#asking-for-an-approval); `reviewrequests_test.go`, `test_blerg_runner_review_request.py`. |
| RUN-73 | A parent is told when its child's turn ends (finished, asked, waiting on an approval, failed): one Blerg note per child turn at the parent's next step, never to an ended parent, reserved source `child_turn` | shared | shared | [Child-turn notes](../runner/internal/server/child_turn_notes.go), [guide](talking-to-an-agent.md#when-the-agent-starts-workers); `child_turn_notes_test.go`. |
| RUN-74 | The plan keeps itself honest: the session's own card moves pin (in progress) or unpin and tick the items naming `#N` or the bound board’s current full card key (done), a board-started session pins its card, a child naming either form is assigned to the item naming it, a Board card's pin is filled from the Board; holds deterministic drift facts at turn end and attaches one rechecked notice to the next input (your last turn ran tools), each fact once (reserved source `plan_drift`), off per session (Nudge when the plan drifts) | shared | shared | [plansync.go](../runner/internal/server/plansync.go), migration 075, mcpgw `BoardObserver`, chat 0.83.0, runner `PlanNudges`; [guide](talking-to-an-agent.md#what-keeps-the-plan-honest); `plansync_test.go`, `board_test.go`, `PlanNudges.test.tsx`, `SessionRail.browser.test.tsx`. |

## Native engines and execution runtimes

Implementation roots: [daemon](../runner/internal/daemon/), [native loop/tools](../runner/internal/agent/), [pod bootstrap](../runner/internal/runner/). Native provider semantics differ; supported does not imply identical native capabilities.

| ID | Feature | Claude implementation | Codex implementation | Assessment / important limits |
| --- |---|---|---|---|
| EXEC-01 | Native CLI headless agent sessions | `claudecode.go`, `claudestream.go` | `codexdriver.go`, `codexapp.go` | Both supported, engine-owned login; common sessionDriver. |
| EXEC-02 | In-process provider loop | `agentdriver.go`, agentsession native branch | No separate OpenAI in-process loop | Intentional architecture: Codex app-server supplies its native loop. Not a missing UI feature. |
| EXEC-03 | Agent versus terminal launch | manager/headless dispatch, claudeCommand | EngineSpec.NewAgentDriver and terminal builder | Both supported. |
| EXEC-04 | Host/local sandbox/Cluster | AgentHost + sandboxExec + runner pod | Same routing with Codex native driver | Supported; restriction/version limitations below. |
| EXEC-05 | Interactive/unattended behavior | interactionParagraph + ccSystemPromptFor | same developer instructions; restricted prompt replaces coding prompt | Both differentiated; server chooses mode. |
| EXEC-06 | Mid-turn messages | persistent Claude streaming stdin; optional kill-switch fallback | native turn/steer with rejection/race recovery | Both supported with detailed fixtures. |
| EXEC-07 | Message identities/ack/replay | shared wsEmitter, inputJournal | same | Shared durable receipts and client-id failure events. |
| EXEC-08 | Queue bounds / rejection | queueFullError and lifecycle handling | queue64, stopped/retryInput handling | Both bounded; dropped messages reported explicitly. |
| EXEC-09 | Interrupt | Claude control request/process fallback | turn interrupt + bounded write/process fallback | Both supported; generation/turn fences differ. |
| EXEC-10 | Model/effort at launch and change | modelEffortArgs, SetModel, native settings | same rules plus runtime model catalog/default reconciliation | Both supported. Codex live model+effort native reports tested. |
| EXEC-11 | Model discovery | built-in/shared Claude source outside daemon prober | EngineSpec.ListModels, periodic ModelProber, codex debug models | Both available, different sources. Probes bounded, cached off spawn path. |
| EXEC-12 | Account usage limits | Claude OAuth subscription/spend windows + rate-limit fallback | account/rateLimits/read through helper app-server | Both supported via separate sources in limits.go. |
| EXEC-13 | Provider-limit refusal / auto-resume signal | Claude text/parser and reset-zone validation | Codex refusal parser and expected-turn check | Both emit shared usage_limit contract; no arbitrary old-turn replay. |
| EXEC-14 | Root text and thinking | stream partials, optional native CLI flags | agentMessage deltas/final + reasoning item transitions | Both rendered into shared event contract. Codex reasoning summary is completed-item content. The working line shows the thinking phase's clock for both (neither reports reasoning tokens mid-block); Hermes shows the phase's native token count when it reports one (`reasoningprogress.go`, chat 0.32.0). |
| EXEC-15 | Root tool calls/results | Claude tool_use/tool_result | native command/file/MCP/dynamic/web/image/collab items | Both supported for mapped item variants; unknown Codex item types silently ignored. No exhaustive installed-protocol variant coverage was asserted. |
| EXEC-16 | Compaction events | CLI compaction parsing | contextCompaction item | Both signal compaction; native payload richness differs. |
| EXEC-17 | Subagent start/status/finish | native tool lifecycle + transcript metrics + SendMessage resume | verified ancestry + settings/token notifications + immutable run IDs | Shared normalized lifecycle; Codex type remains literal Codex rather than a richer native role taxonomy. |
| EXEC-18 | Child own model and reroutes | native/transcript reported model | thread/settings/model-rerouted notifications | Live and Insights consume updates; completed child usage partitions preserve observed known-model usage and leave ambiguous model-switch usage unknown, without double counting legacy totals. |
| EXEC-19 | Child configured effort | Unknown when native child events do not report it | Native child settings, updates and recovery snapshots | Shared child events, Live cards and Insights carry reported configured effort; unknown remains explicit and never inherits the parent setting. |
| EXEC-20 | Child metrics/tools/duration/errors | claudesubagents + clauderesume | codexchildren + appItem parent linkage | Both present; final events under child model. |
| EXEC-21 | Background shells | native task notifications | authoritative terminal inventory | Transient RPC/decode failures retain the last inventory and retry; native runtime shutdown finalizes known work. |
| EXEC-22 | Background/resumed children | native background notifications and knownAgent map | child run IDs, usage baselines and known child allowlist | Supported; distinct resume runs prevent reopened completed spinner. |
| EXEC-23 | Explicit slash skills | shared resolution / native CLI skill path | skills/list and native skill input | Both supported; Codex skill path inventory remains private. |
| EXEC-24 | User/project instructions | native CLAUDE.md and direct-loop assembly | native AGENTS.md/config | Native conventions differ; adapter-declared safe config roots include Codex/shared skills and respect custom homes. |
| EXEC-25 | User/managed plugins | workshop selected snapshot, --plugin-dir | private native plugin home, selected marketplace enablement | Both supported on compatible runtimes; selected cache snapshot protects source settings. |
| EXEC-26 | Capabilities | native init inventory | skills/plugins/MCP RPCs + observed tools | Effective model changes refresh the snapshot. Unreported native built-ins/roles remain explicitly unknown. |
| EXEC-27 | MCP explicit grants | private native MCP file and current grant | dynamic gateway bridge plus ambient-MCP disable | Both interactive and restricted modes; Codex verified CLI required. |
| EXEC-28 | Changed MCP tool definitions | toolRefresher marks Claude process stale | no toolRefresher; native resume keeps old schema | Actual-adapter refresh policy is advertised in capabilities and tool-change review. Start a fresh session with current connections when required; approval does not prove the native schema reloaded. Removed grants remain enforced. |
| EXEC-29 | Strict unattended tool surface | Claude allow/deny config; no ambient config | read-only native sandbox + dynamic rooted file/MCP tools + deny feature list | Both intended supported; Preparation requires separate confinement review. |
| EXEC-30 | Restricted native version validation | Claude pinned feature/security fixtures | exact codex-cli 0.162.1 runtime gate | Codex grants/restricted runtime reject unverified CLI, intentionally fail closed. |
| EXEC-31 | Restricted file operations | native Claude tools with deny policy | Go rooted read/write/edit/glob/grep | Codex symlink/control-file denial and platform-specific confinement; Linux native tests. Glob ** unsupported is explicit. |
| EXEC-32 | Person questions | messaging ask protocol, native-loop tool | requestUserInput bridge -> private ask/answer | Codex options textual; secret questions rejected; children relay via parent on pinned CLI. |
| EXEC-33 | Native command/permission elicitation | CLI policy runs without per-call approval prompts | approvalPolicy never, unsupported interactive RPCs rejected | Blerg MCP proposals are separate and supported. No general native approval UI parity promise. |
| EXEC-34 | Host daemon restart recovery | durable record + transcript + native identity | durable record + exact thread + transcript + known children | Both for eligible host sessions; no kickoff replay. |
| EXEC-35 | Recovery auto-continue safety | deny list, waiting-person checks, once-per-finished-turn | common adoption plan with Codex callbacks | Both; human input outranks synthetic continuation. |
| EXEC-36 | Local sandbox restart recovery | excluded by recordable | same exclusion | Shared product limit, not Codex-specific. Container state removed on restart. |
| EXEC-37 | Host MCP/restricted recovery | excluded by recordable | same exclusion | Shared deliberate credential/policy limit. Cluster has separate persistent native path. |
| EXEC-38 | Cluster native conversation history | nativeHistoryKeeper binds Claude driver | same keeper binds Codex + SQLite + child allowlist | Both owner/engine/claim UID/workdir bound; workspace reconstructed separately. Server storage fences audited elsewhere. |
| EXEC-39 | Missing native history fallback | Claude reconstruction/resume fallback unless strict native mode | bounded archived transcript context only when no native thread | Explicit notices; native retained history mode refuses mismatched identity. Cannot recreate lost native data. |
| EXEC-40 | Legacy terminal exact recovery | recorded Claude session ID | private Codex terminal home, bounded cli-source transcript identity | Both supported with real CLI fixtures. Obsolete comments still say Codex cannot resume; implementation does. |
| EXEC-41 | Session CLI publish/fetch/topic/review | buildSessionEnv + messaging CLI on PATH | same | Engine-neutral tool availability; no separate daemon artifact pipeline needed. `review reply` answers a round in one call (repeated triples or `--file`). |
| EXEC-42 | Session token refresh | shared sessiontoken file/sandbox copy | same | Master token excluded from child env; private launch auth fetched separately. |
| EXEC-43 | Managed refresh authority | Claude existing credential mode, provider-owned refresh outside this Codex broker | Core-owned managed source -> external native login/refresh callback | Codex supported for explicit Cluster enrollment. Imported credential snapshots remain separate. |
| EXEC-44 | Managed login in workstation sandbox | not this enrollment path | explicitly refused by prepareCodexRestrictedRuntime | Deliberate supported-shape boundary; host credentials use mounted native login. |
| EXEC-45 | Repo clone/new/no-repo/provision | shared workspace setup | same | No confirmed engine-specific omission in shared path. |
| EXEC-46 | Agent reconnect and state tracking | native engine state and persisted agent records | same | Legacy Terminal sessions retain read-only history; no runtime recovery. |
| EXEC-47 | Host install engine readiness | installer/hostcheck heuristics | same scripts with Codex check | Multi-engine supported; Codex custom-home readiness honors an absolute CODEX_HOME. |
| EXEC-48 | Status/open-work/restart cause | private records, same installer restart metadata | same | Both report unfinished work and planned restart notices; [shared command guidance](design/codex-guide-parity.md) reaches native, terminal and recovered Codex sessions with shell access. |
| EXEC-62 | Engine plan in the session panel | Root TaskCreate/TaskUpdate and TodoWrite folding | Root turn/plan/updated snapshots | Shared `/live` plan shape; [plan adapter](../runner/internal/daemon/codexplan.go), [fold](../runner/internal/livestate/plan.go). Child/stale/malformed updates are ignored; explicit empty snapshots clear. Restricted Codex keeps update_plan disabled. |
| EXEC-63 | Relayed-decision marker | a relay reaches the model in a `<system-reminder>` block headed with the session's private marker code, announced in the appended system prompt (a 0600 file, never argv); other text neutralized | same block in the turn input, code announced in developerInstructions | Native loop: same, announced in its system prompt. **Hermes and OpenClaw: no marker** (relays arrive as plain user text; [Hermes guide](hermes-gateway.md)). The daemon scrubs the code from every outbound event, and any 16-hex value in the relay header or announcement shapes (another session's code). Not a security boundary against an agent told to print a transcript or `marker.key`; the server-side review state is the authority. [inputtext](../runner/internal/agent/inputtext.go), [markercode](../runner/internal/agent/markercode.go), [key](../runner/internal/daemon/markernonce.go); `systemmessages_test.go`, `markerscrub_test.go`. |

### Direct API-loop features

Claude Code CLI and Codex are native drivers. The older direct Claude API loop is a third backend; its controls do not automatically govern either native CLI. These distinct capabilities must remain visible in parity claims. Evidence is rooted at [the native loop](../runner/internal/agent/) and [driver selection](../runner/internal/daemon/agentdriver.go).

| ID | Feature | Direct-loop implementation | Native CLI distinction |
| --- | --- | --- | --- |
| EXEC-49 | Provider transport and content | `agent/provider.go` Provider.Stream with text/thinking/tool/result blocks; `agent/anthropic/anthropic.go` Messages SSE, opaque thinking, cache breakpoints, adaptive effort, retryable error translation | Both CLIs own provider calls. No generic OpenAI direct Provider adapter currently exists; adding Codex does not require one. |
| EXEC-50 | Mutation protocol | `agent/checkin.go:42` rejects mutation before task_start and selected risky Bash commands before pre_risky; loop.go:301 completion reminder then check_in_missing | Neither native CLI exposes this Go enforcer. Their native policy/instructions differ. Check-in is protocol disclosure, not permission approval or full command safety analysis. |
| EXEC-51 | Tool execution scheduling | `agent/loop.go:404` bounded concurrent read/check-in pass, serial mutating/unknown calls, one blocking ask per assistant step | CLI engines own their scheduler. No claim the same concurrency/policy is enforced there. |
| EXEC-52 | Session budget | `agent/loop.go:77-98,339` price-table accumulation and provider-call gate; direct children fold spend into parent | `AgentDriverOpts` contains no BudgetUSD/Pricing. Neither Claude Code nor Codex receives the direct-loop hard budget; usage Insights is separate from a spend cap. Missing price entries cannot contribute known cost. |
| EXEC-53 | Compaction | `agent/compaction.go:25-64` ~80%-window trigger, oldest ~60% summary, turn-aligned cut, summary cost and compaction event | Native engines own compaction, whose events are normalized; threshold/summary settings here are direct-loop-only. |
| EXEC-54 | Chat/messaging | `agent/messaging.go` update/note, blocking ask, AskCloser, record answer as ask_answer, preserve teardown races | Native Codex question bridge and shared CLI offer equivalent user communication by different mechanisms. Claude CLI's own asking path is native/CLI driven. |
| EXEC-55 | Child workers | `agent/subagent.go:170-238` type definitions from .claude/agents, model precedence explicit>type>parent, optional readonly tools, one nesting level, no child ask, remaining budget | Native CLIs have richer own child lifecycles; direct child stream intentionally not forwarded into parent transcript. Don't promise direct-loop children have native live metrics. |
| EXEC-56 | Restore | `agent/restore.go` rebuild messages from persisted events, compaction base, missing tool results, discard cut-off turn, restore model | Native drivers prefer exact provider conversation; this is separate reconstruction semantics. |
| EXEC-57 | Provider retries | `agent/retry.go`: up to five stream-open attempts, configurable backoff base and RetryAfter; no mid-stream retry or per-attempt transcript notice; final failure becomes a turn error | Native Codex reports own willRetry, restart loop handles app-server lifecycle; Claude CLI reports its own retry progress. |
| EXEC-58 | Skills/instructions/hooks | `agent/skills/{skills,hooks}.go`, `agent/skilltool.go`, `daemon/agentprompt.go` | Direct-loop-only emulation of Claude conventions. Native engines should load their own config in their own runtime. Preparation must stay within the selected driver and runtime boundary. |
| EXEC-59 | Workspace tools | `agent/tools/readonly.go` path resolution/capped reads/glob/grep; `mutating.go` write/edit/Bash timeout/group kill | Direct tools are not the Codex restricted rooted tools nor general sandbox. Native engines have their own corresponding tools. |
| EXEC-60 | Blerg memory/knowledge | `agent/tools/dataplane.go` memory_write/delete, knowledge_search; `daemon/agentsession.go` `fetchDataPlaneContext` fetches the memory index into cfg.System | Explicit tool registration and automatic context are direct-loop-only. Both native CLI drivers discard this cfg. This is a real cross-backend integration distinction, not Codex-specific regression. API capability exists separately. |
| EXEC-61 | Legacy visual publishing tools | `agent/tools/publish.go` push_mockup and push_screenshot with caps, symlink skips | CLI agents use shared blerg-runner publish; formats/routes are not identical legacy tool names. |

## Chat, workspace panel, and embedded surfaces

Implementation roots: [runner frontend](../runner/frontend/src/), [shared chat](../packages/chat/src/), [Go proxy](../packages/chat/proxy/go/), [embedding contract](../packages/chat/README.md).

| ID | Feature | Claude / Codex assessment | Implementation / limits |
| --- | --- | --- | --- |
| CHAT-01 | Runner workspace launch | Principal/target-scoped available engines; sole-engine selection hidden | RunColumn, LaunchSheet, lib/launchOptions |
| CHAT-02 | Runner model/effort picker | Dynamic per engine/target; cached with bounded pending/error/retry states | lib/engineModels, lib/launchOptions, ModelPicker, ModelCatalogNotice |
| CHAT-03 | Runner cron scheduling/runs/MCP grants | Dynamic restricted-engine choices; Cluster catalogs use personal credentials only | CronForm, CronsPage, CronRuns, McpPicker |
| CHAT-04 | Workspace list/search/archive/places/favorites/history | Shared Workspace metadata | SessionList, ArchiveView, HistoryTab, lib/sessionRail |
| CHAT-05 | Workspace status/end/start progress/retry/recovery | Shared event contracts; requires backend support | SessionDetail, SessionPlaceholder, StartSteps; chat startStages/sessionEnd |
| CHAT-06 | Mid-turn messaging/queue/drafts/replay/pagination | Shared normalized chat paths | packages/chat hooks/useSession, useTranscriptStore and queue/replay/tail tests |
| CHAT-07 | Subagents/running panel/background tasks | Shared normalized child/task event display; late model updates supported in Live | lib/liveState.ts:44; chat model/subagents/liveState, hooks/useLiveState |
| CHAT-08 | Ask/reply/note/check-in | Shared ask routing; options text only | chat AskReply/model/asks; board proxy message source human |
| CHAT-09 | Workspace settings/limit reset resume | Shared runner settings; provider capability needed | UsageLimitResume, SessionRail, UsageMeters |
| CHAT-10 | Capabilities/skills/plugins/MCP/tools | Data-driven rendering; unknown groups explicitly marked | CapabilitiesPanel.tsx:3,140; lib/capabilities |
| CHAT-11 | Tool changes/proposals | Shared API contracts | ToolChanges, Proposals (history and audit) and lib/proposals/toolChanges; proposal cards in the chat (chat cards/ProposalCard, model/proposals) with Edit in ProposalEditSheet; every pending proposal in Needs you and the bell (ProposalAttention, ProposalPreview: tool and connection first, ProposalNotifications, useProposalInbox), live `proposals` count, deadline extensions (db/proposal_extensions.go) |
| CHAT-12 | Legacy Terminal history | Read-only recorded output | No input or resume | TerminalHistory tests |
| CHAT-13 | Usage meters/Insights/cost prices | Both named; estimates/completed work limitations | UsageMeters; Insights, SessionInsights; lib/insights |
| CHAT-14 | Files/attachments/previews/download/delete | Engine independent artifacts service | runner FilesTab/ArtifactViewer and chat transport/files/FilesPanel; attachment notes carry IDs (previews by ID; a pre-0.4.4 name-only note uses the newest same-name upload at or before the message, model/artifacts `attachmentAt`); `blerg-runner fetch` names same-name files by upload time and prints IDs (chat 0.32.0) |
| CHAT-15 | Artifact text/Markdown/PDF/image/HTML/CSV/review/markup; workspace file review | Shared publish/review contract, including [schema-validated answer pages](answer-back-pages.md) with owner-only Submit and unchanged HTML sandbox; published file tabs follow latest by default with remembered pinning and a live newer-version indicator; viewer actions share one plain-icon utility bar and comparison appears only for verified differences; workspace previews offer Watch file ([trust model](design/live-preview-threat-model.md), #263) | chat ArtifactViewer, components/review, components/markup, model/artifacts; review-state publishes fold into one line per round of answers (model/reviewRounds, ReviewRoundLine); the viewer scales with the screen (review and diff about 96vw × 92vh, plain up to 1400px, prose at a reading measure) and has a maximize toggle remembered per layout (chat 0.30.0; `ViewerSpace.browser.test.tsx`). WorkspaceFileViewer / review/WorkspaceReview collect one batch across tabs and split panes, with shared model/workspaceAnchor hashes and moved/outdated line ranges; existing review replies carry status. Unsent drafts are memory-only; nontext files get whole-file anchors. `WorkspaceReview.test.tsx`, `WorkspaceReview.browser.test.tsx`, `workspaceAnchor.test.ts` (chat 1.41.0). |

| CHAT-16 | Sound/notifications/theme/auth/version/update | Shared UI infrastructure | runner notify/sound/authClient/UpdateBanner; five [shared account themes](themes.md), including color-blind and high-contrast presets, across Core, Runner, Board and chat (#332). |
| CHAT-17 | Embedded Chat transport direct Blerg WS | Both normalized; host owns model picker | packages/chat transport/blerg, ChatView slots |
| CHAT-18 | Embedded proxy Chat | Normalized transcript/files/control; optional model/effort forwarding and durable IDs, visible unsupported errors | transport/proxy, proxy/go, board chatproxy |
| CHAT-19 | Inkhold embedded writer | Dynamic Blerg engine/model/effort discovery, interactive turns, explicit native engine handoff | Separate consumer branch and tests; direct Anthropic backend remains distinct |
| CHAT-20 | Published artifact links open the existing viewer | Exact trusted workspace URL plus known viewable artifact; modified/external links preserve navigation | chat model/artifactLinks and Markdown links; authenticated host URL mapping |
| CHAT-EXPORT | Print and download viewer content | Browser-only exports reuse the shared toolbar; primary actions precede Print and downloads, which move into More in narrow panes to keep the toolbar and live timestamp on one row. Phone Layouts opens from the Open menu to preserve title space; chat tables offer visibly labeled CSV (formula-safe) and MD downloads on hover or focus, code keeps its language extension, and diagrams/charts retain SVG/PNG exports. Print isolates content in a light document with repeating table headers and unbroken rows. Cached published files download without another server request. PDF uses native print; sandboxed HTML prints sanitized static content; MCP dashboard payload exports remain disabled. | chat model/contentExport, ContentActions, ViewerToolbar; runner ContentExport/ViewerToolbar/PopOutMessage browser tests (BLG-589). |
| CHAT-21 | Mermaid diagrams and inline-data Vega-Lite charts | Shared renderer, independent of engine and host | [Visual renderer](../packages/chat/src/components/VisualBlock.tsx); lazy strict Mermaid, inert SVG images, bounded charts with a deny-all loader and expression interpreter, compiled and laid out in a disposable worker with runtime row/scene/facet budgets and a five-second termination deadline (#334); live chat-token themes, responsive plots, native SVG hover and arrow-key focus emphasis, a reduced-motion-aware first-render entrance (BLG-483), source/copy/fullscreen zoom, Markdown previews/review and file viewer. Dashboard stat/gauge cards support [live authorized datasets](live-dashboard-datasets.md) for workers, weekly engine usage, and main merge buckets over sequenced socket deltas. |
| CHAT-22 | Anchored code review of published diffs | Shared existing review files, feedback and reply flow | [Diff selection](../packages/chat/src/model/diffSelection.ts), [review surface](../packages/chat/src/components/review/ReviewMode.tsx); direct gutter comments and inline threads, general feedback, Markdown formatting/preview, new-side suggestions, revision-aware resolved/rejected replies and reopening, individual thread collapse and a resolved-thread filter, unambiguous quote carryover with outdated fallback, scoped/mobile selection and one-message submission. |
| CHAT-23 | Workspace permissions (children, board access levels, MCP tools) | Shared server policy; engine independent | runner session_permissions.go, launch_permissions.go, db/proposal_grants.go; frontend SessionPermissions (panel summary + chips), PermissionsModal (search, Boards level selects, staged Save, Maximize), LaunchPermissions (Permissions… button), useChildApproval; created cards as History chips (topics/reel.go card items) |
| CHAT-25 | Board access levels per workspace (propose, read, file without asking, full), checked on every call | Claude and Codex: the `blerg_board` workspace tool (stdio `_blerg_session` server or Codex dynamic tool) from launch, so a level works mid-session; launch-board engine tools follow the level per call but stay listed until restart; other engines: the CLI | runner session_board_access.go (owner PUT, workspace GET/POST board-tools), mcpgw/boardlevel.go (gateway per-call level, CallBoard one-call exchange), migration 070; CLI `board access` and board commands without launch env; `session_board_access_test.go`, `boardlevel_test.go`, `board_levels_test.go`, `blerg_runner_board_test.py` |
| CHAT-30 | Children inherit board access: `session start --board <board>:<level>`, never above the parent; capped by the parent's level on every call; the person lowers a single child | All engines (the `blerg_board` tool for Claude and Codex, the CLI for every engine); desktop and cluster children, sandboxed or not | runner child_board_access.go (start resolve/re-check, seed rows `created_via='parent'`, live cap, cascade notes), migration 073; card "with Blerg · Full" (`@blerglab/chat` 0.35.0); child Permissions shows the cap; CLI `--board`; `child_board_access_test.go`, `child_board_levels_test.go`, `test_blerg_runner.py`, `blerg_runner_board_test.py` |
| CHAT-32 | The sidebar opens Workspaces; the dock puts Main first, person-started Channels next, and agent-started Workers in a group collapsed by default. The group summarizes count and state glyphs and names blocked/failed workers; expanded rows are smaller, dimmer and use a non-chat icon. Opening a worker preserves its identity. Runtime glyphs explain Host, Sandbox or Cluster in tooltips and accessible text; the collapsed group shows runtime only for host or mixed groups. Channels, repository Files and Repositories share workspace tab strips and split groups with a separate drag scope; saved layouts, stable tab order and keyboard navigation remain; empty Channels headings are hidden. The right panel’s Shared tab holds exchanged files. Finished retains History links. Sidebar state remains one glyph and cards use at most two lines. Phone workspace cards keep descendant counts and worker failure/person-question summaries without nested cards; child search matches remain explained. Delegated worker cards, tiles and tabs suppress unread dots; standalone roots and orphans retain signals, and channel unread is aggregated onto workspace cards | Shared; engine independent; no backend additions | runner SessionDockWindows, DockRepositories, ExplorerTreeRow, lib/sessionRole, SessionTabBar, SessionRail; WorkersDock browser checks at 390, 1024 and 3840 px with zero, three and forty workers; SessionDockWindows tests cover saved layouts and directly opened workers |
| CHAT-34 | A context meter on every Running now row (Main and workers) and in the Workspace section: share of the model's window in use at the latest turn's end, tooltip with tokens / window and the compaction size where known, amber from 85%, ⇊ after a compaction; hidden when the engine reports no size or window; owner or administrator only | Claude and Codex (size and window), the native loop (also its compaction size); Hermes: not reported (no meter, said in the token chip's tooltip) | runner agent ContextUsage on turn_done (daemon claudecode/claudestream/rootusage, codexapp, agent loop), db ReadContextTurn, server live_usage.go (`usage.context`); frontend lib/liveUsage contextMeter, ContextMeter.tsx; `rootusage_test.go`, `compaction_test.go`, `live_usage_test.go`, `liveUsage.test.ts`, `ContextMeter.browser.test.tsx` |
| CHAT-33 | A token chip on every Running now row (Main, subagents, workers): compact total, tooltip with this turn, total, input vs cached, subagents and cost; owner or administrator only | Shared; engine independent (recorded turn usage; Claude, Codex, Hermes) | runner server live_usage.go (`usage` on GET /live and on each child), db ReadTurnUsageEvents; frontend lib/liveUsage, SessionRail MainRow, ChildSessions; `live_usage_test.go`, `liveUsage.test.ts`, `RunningTokens.browser.test.tsx` |
| CHAT-31 | A workspace proposes a new board (`blerg-runner board create`); the owner approves it in Needs you, on Proposals or in the chat (lowering the requested access if they like); it is created with the owner's authority and only the proposing workspace gets access; Board's New board picks repositories from the connected desktops | Shared; engine independent | contracts boardfiling/newboard.go; core `POST /internal/tokens/exchange/board-new`; board `POST /api/boards/create-approved` (migration 028), `GET /api/repo-choices`, NewBoardRepos; runner board_create.go (`POST /api/sessions/{id}/board-create`, `GET /api/board-repo-choices`), db/board_create.go; frontend ProposalPreview, BoardCreateAccess; chat ProposalCard `board_create` (chat 0.74.0); `board_create_test.go`, `newboard_test.go`, `internal_board_new_test.go`, `test_blerg_runner_board_create.py`, `Proposals.test.tsx`, `ProposalAttention.browser.test.tsx`, `NewBoard.browser.test.tsx` |
| CHAT-24 | Review request card, viewer review bar (Approve / Request changes) and Needs you row | Shared; engine independent | chat ReviewRequestCard, ReviewRequestBar, model/reviewRequests (chat 0.29.0); runner SessionRail ReviewRequestRow; `ChatView.reviewRequests.test.tsx`, `ReviewRequests.browser.test.tsx` |
| CHAT-26 | A parent's messages and answers in its child are labeled as the parent ("↑ Parent · title · Answer", linked inside Blerg), never "You"; the child header reads "started by <agent> from <parent>" | Shared; source `session` and `answered_by` set by the runner from the parent's token | chat isFromParentSession, EventCards author link (chat 0.33.0); runner AgentChatView describeUserMessage, lib/startedBy; server SendMessage, messageevents.go answerAskFrom; `ChatView.notices.test.tsx`, `parent_message_source_test.go` |
| CHAT-27 | Persistent question threads: any number of person/Main replies, in-place revisions with history, and whose-turn labels in Needs you. Only explicit Answer, Approve/Decline or Dismiss resolves one; replies never commit bound decisions. Open as tab focuses one retained question tab, with read-only outcomes after resolution. `ask --resolve` withdraws an obsolete question | Shared; owner/session-scoped APIs, durable person input and reserved `question_thread` relay; native/CLI blocking waits hand off to discussion without an answer | runner question_threads.go, messages.go, migration 163; CLI `ask --reply/--revise/--no-wait/--resolve`; chat questionThreads, QuestionCard, AskReply (chat 1.59.41); runner QuestionTabs, QuestionRow; `question_threads_test.go`, `questionThreads.test.tsx`, `QuestionThreadsApp.browser.test.tsx`, `QuestionThreads.browser.test.tsx`, `test_blerg_runner_question_threads.py` |
| CHAT-28 | A pinned Blerg Board card is shown live in the panel's chip and ticket dialog (column, tags, origin, repos, trail; Move in place, Open in Board for Run/Discuss/Archive), with the person's own board permissions; other trackers' pins as pinned, one column with detail chips | Shared; engine independent (the agent only pins `board_id` + `#number`) | runner board_cards_live.go (`GET /api/board-cards/{board}/{number}`, `POST …/move`, `GET /api/sessions/{id}/board-card-url`), ticket.BoardCard, worklist.go sets the address; board MCP `blerg_card_get with_trail`; frontend lib/liveBoardCard, BoardCardView, SessionRail TicketRow/PlanSummary; chat TicketModal `body`/`actions`, TicketDetailChips (chat 0.34.0); CLI `ticket get/create/file` address lines; `board_cards_live_test.go`, `worklist_test.go`, `card_trail_test.go`, `SessionRail.boardcard.test.tsx`, `BoardCardView.browser.test.tsx`, `blerg_runner_board_test.py` |
| CHAT-29 | A worker at work has one card, in Running now (model, effort, elapsed, current tool, plan step, discard); workers has a "N running ↑" line and lists stopped children; a subagent without its own effort shows the workspace's | Shared; engine independent | runner lib/liveState `childIsWorking`, ChildSessions StartedSessions/ChildSessionRow, SessionRail; chat SubagentCard + ChatContext `sessionEffort` (chat 0.34.0); `SessionRail.test.tsx`, `ChildRunning.browser.test.tsx`, `ChatView.subagent.test.tsx` |

## Boards and agent workflows

Implementation roots: [board API](../board/internal/api/), [board DB](../board/internal/db/), [board MCP](../board/internal/mcp/), [board web](../board/web/src/). The board admission gate is direct inference, distinct from native coding sessions.

| ID | Feature | Claude / Codex assessment | Implementation / limits |
| --- | --- | --- | --- |
| BOARD-01 | Board CRUD, templates, icons, columns, ordering; sessions propose exact approved metadata, column changes, tag renaming/removal, and state settings | Shared, engine independent | api/api.go:93-103; board/web BoardView, BoardSettings |
| BOARD-02 | Card CRUD/schema/priority/claim/comments/archive | Shared, engine independent | api/api.go:105-117; db/cards, mcp/tools |
| BOARD-03 | Card dependencies/graph/search/global overview | Shared, engine independent | api/api.go:107,114,123-124; db/graph, search |
| BOARD-04 | Event history, card doc, diff/git panels/checks | Shared output contract | api/api.go:116-121; carddoc, diff, git; web/GitPanel, DiffPage |
| BOARD-05 | Admission gate/disputes/log | Claude/Hermes/local inference; Codex unavailable by design | gate/personal.go:48; direct-inference distinction in the parity review |
| BOARD-06 | Automation identity/encryption/revocation | Both via current stored owner proof; registered engine selection | api/automation, api/runnercontrol, db/automation_crypto; browser never receives the stored token |
| BOARD-07 | Worker/card discussion/reviewer/conflict worker | Both via shared startOnRunnerConfigured | api/runnerapi.go:249-284; automation_test Codex cases |
| BOARD-08 | Board bootstrap/header chat | Both; backend-qualified recovery with native/reconstruction reason | api/boardsession, api/runnerapi, web/BoardChat |
| BOARD-09 | Board run priority/concurrency/capacity/dependencies | Shared engine start path | api/boardrun; runnercapacity; boardrundeps tests |
| BOARD-10 | Standing agents/respawn/quiet workers | Shared identity start | api/standing.go:305; automation.go:18 |
| BOARD-11 | Review/accept/merge/conflict/deployment recording | Shared Git/runner contract | api/review.go; runnerapi.go:98-100; automerge tests |
| BOARD-12 | Per-role/card model and effort configuration | Shared inheritance and engine-bound future launches | Engine changes clear board/card overrides transactionally; existing sessions retain native identity; model-only changes clear stale effort |
| BOARD-13 | In-session model/effort switching and discovery | Shared REST/MCP/proxy controls with actual-session target catalog | api/sessionmodel, api/launchoptions, web/SessionModelControls; effective metadata is separate from dispatch acknowledgement |
| BOARD-14 | Board metrics/card tokens/model attribution | Shared normalized event usage; no effort/engine dimension | api/cardstats.go:109; boardmetrics.go:65 |
| BOARD-15 | Board tokens/membership/project scope/MCP | Shared security contracts | auth, corecred, mcp/scope; dynamic discovery checks card.write and current automation authority |
| BOARD-16 | Live board updates for every card change (UI, REST, MCP, automation) | Shared, engine independent | api/hub.go change pings on every card write; web boardSocket waits for a silently renewed token and only the newest reload lands; api/live_test.go, web api/BoardView tests |
| BOARD-17 | Run a card on a chosen target (cluster, or a connected daemon's sandbox), kept on respawn | Shared start path | [Run targets](../board/internal/api/runtarget.go) (owner of the automation token only; `GET /api/cards/{id}/run-targets`), card panel `Conversation.tsx`; the runner runs a pinned start in the folder that daemon has for the repository (`repoFolderOn`) |
| BOARD-17a | A board's own "Where it runs" (cluster, a desktop's sandbox, or its host with a recorded acknowledgement); desktop-only installs pick the desktop with the board's repo | Shared start path | [Board runtime](../board/internal/api/boardruntime.go) (`GET /api/boards/{id}/run-targets`, PATCH `run_runtime`/`run_daemon_id`/`run_host_acknowledged`), `BoardRunTarget.tsx`; Board migrations `025`, `026` |
| BOARD-17b | A bare folder-name board repo is resolved once to owner/name from the desktop the admin picks (only that desktop is asked; desktops have no owner, so it is admin + automation owner only), keeping its place; a folder without a remote stays desktop-only and the cluster is refused for it | Shared start path | [Bare repos](../board/internal/api/barerepo.go) (`GET /api/boards/{id}/repo-remote`, `POST /api/boards/{id}/repos/resolve`), `RepoResolve.tsx`; board migration `027` (repo order); runner launch options `targets[].repo_origin` for the named desktop only |
| BOARD-18 | Run as me: one-click automation identity (settings panel, create-board default `BLERG_BOARD_DEFAULT_RUN_AS_CREATOR`, card call to action); a minted token is revoked when replaced, removed or its board deleted; pasting stays available | Shared start path, engine independent | [Run as me](../board/internal/api/runasme.go), [core client](../board/internal/coremint/coremint.go), migration `024`; web BoardSettings, App create form, Conversation `RunAsMeCta` |

## Installation, operations, and distribution

| ID | Feature | Implementation / contract |
| --- | --- | --- |
| OPS-01 | Desktop Docker Compose stack with separate Core/board/runner databases and persistent volumes | [Desktop installer](../install/desktop/README.md), [Compose](../install/desktop/docker-compose.yml) |
| OPS-02 | Desktop prerequisites/engine-login checks, stack health checks, and uninstall instructions | [Host checks](../install/desktop/hostcheck.sh), [installer](../install/desktop/blerg-up.sh), [uninstaller](../install/desktop/uninstall.sh) |
| OPS-03 | Linux systemd-user and macOS launchd daemon installation/configuration | [Daemon installer](../install/desktop/daemon/install.sh), [daemon guide](../install/desktop/DAEMON.md) |
| OPS-04 | Kubernetes build/push/deploy, configurable namespaces/images, ingress/TLS, and upgrade/rollback checks | [Cluster installer](../install/k8s/README.md), [deploy script](../install/k8s/deploy.sh) |
| OPS-05 | Agent-readable installation runbook, checkpoints, secret handling, and handoff verification | [Kubernetes agent runbook](../install/k8s/AGENT-INSTALL.md) |
| OPS-06 | External secret references, Vault choices, cluster session RBAC, and example NetworkPolicy | [Cluster runtime guide](../install/k8s/CLUSTER-RUNTIME.md), [session resources](../install/k8s/cluster-runtime.yaml) |
| OPS-07 | Extend/replace agent images, pinned engine CLI dependencies, Go/Node build toolchains, every sandboxed session (launch sheet, board, cron, child; agent and terminal) in its own `git clone --shared` on its recorded work branch (returned by `blerg-runner session result <id>`) with the repository's objects read-only, the checkout and its `.git` never mounted, the branch fetched back by the host with hooks and configured commands off at each turn end and at stop, and leftover work committed in the container at stop ([sandboxclone.go](../runner/internal/daemon/sandboxclone.go); `sandboxclone_test.go` and `sandboxsessions_test.go` escape probes), and reproducible image builds | [Image guide](sandbox-image.md), [pinned engines](../runner/sandbox/engines/package.json), [Dockerfile](../runner/sandbox/Dockerfile) |
| OPS-08 | Data/database/key backup and restore, state inventory, and recovery drill | [Backup guide](backup-and-restore.md) |
| OPS-09 | Configurable session-resource limits, idle expiry, retained-history lifecycle, and operational status | [Cluster guide](../install/k8s/CLUSTER-RUNTIME.md), [Job manager](../runner/internal/server/k8sjobs.go) |
| OPS-10 | Health endpoints, component/build versions, operational logging, and Prometheus metrics | [Telemetry](telemetry.md), [build/version contract](../CONTRIBUTING.md#versions-and-releases) |
| OPS-11 | Development builds, Go/web tests, lint, browser tests, and disposable database checks | [Contributing](../CONTRIBUTING.md), [Makefile](../Makefile), [CI](../.github/workflows/ci.yml) |
| OPS-12 | Privacy scrub, documentation link checks, installer checks, and release guard scripts | [Scripts](../scripts/), [Contributing](../CONTRIBUTING.md#tests) |
| OPS-13 | Unified semantic version/changelog, public release assembly, tags, and release metadata | [Release guide](../CONTRIBUTING.md#versions-and-releases), [release script](../scripts/release-public.sh) |
| OPS-14 | Security reporting policy and documented trust/containment limits | [Security policy](../SECURITY.md), [MCP security properties](mcp-connections.md#security-properties-and-their-limits) |
| OPS-15 | Shared Hermes model providers for cluster sessions: install-time fragment, pod config merge, cluster model catalog, local-budget rule, opt-in egress to model servers | [Cluster runtime guide](../install/k8s/CLUSTER-RUNTIME.md#shared-hermes-providers), [template](../install/k8s/hermes-providers.example.yaml), [server](../runner/internal/server/hermesproviders.go), [pod merge](../runner/internal/runner/hermesconfig.go) |
| OPS-16 | Engine discovery after install: per-engine status with reason and exact fix from each desktop, every engine listed in the launch sheet with Check again / Re-detect PATH, Settings → Engines per desktop, installer login-shell PATH capture and `refresh-path` | [Adding an engine after install](../install/desktop/DAEMON.md#adding-an-engine-after-install), [daemon](../runner/internal/daemon/enginestatus.go), [server](../runner/internal/server/enginestatus.go), [launch options](../runner/internal/server/launch_options.go), [installer](../install/desktop/daemon/install.sh); `enginestatus_test.go`, `LaunchSheetEngines.browser.test.tsx`, `EngineStatusSection.test.tsx`, `daemon_install_test.sh` |

## Inventory maintenance and completeness

This inventory describes all identified feature families in the reviewed baseline. It is not a claim that every line, test, or provider behavior was executed. Keep source-backed uncertainty visible in the parity review. A broad audit needs both static tracing and runtime-specific fixtures; a passing shared test does not establish native-provider parity.

For each change, check the browser, REST/MCP, unattended and embedded entry points; principal and runtime scope; persistence/recovery; usage/capability reporting; installer delivery; and failure paths. New engines should consume shared features through declared capabilities rather than copying lists into each feature.

### Document and image review interactions

Markdown review supports rendered editing, tracked/clean views, explicit source fallback,
and anchored margin comments (Ctrl+Alt+M). PDF comments share the page-aware margin
workflow. Image markup supports individually selectable drawings with comments, style
editing, visibility controls and a flattened annotated export. See
[review interactions](talking-to-an-agent.md#reviewing-documents-and-images).

Attachment chips offer safe local raster-image thumbnails, first-page PDF thumbnails,
and bounded plain-text excerpts before send, with file-tile fallbacks. Document editing
uses icon formatting controls with tooltips, accessible labels and live active states.

Hermes agent-session protocol coverage and explicit runtime limits are documented in [Hermes gateway support](hermes-gateway.md). Its native tools, steering, questions, plans, children, background inventory and cumulative usage are translated into the shared runner event contracts; this does not enable restricted MCP/cron or managed-plugin gates.

## Workspace 0.5.0 integration status

| ID | Feature | Implementation / availability |
| --- | --- | --- |
| WORKSPACE-01 | Immutable, owner-bound selections, members and results; additive legacy compatibility | [Selection](workspace-selection.md), [contracts and runtime gates](workspace-integration.md). Supported on verified Linux/WSL hosts, local sandboxes and cluster pods; native macOS host stays unavailable (#181). |
| WORKSPACE-02 | Descriptor-based explorer, bounded filename search, member Git status and read-only repository inventory | [Explorer](workspace-explorer.md), [quick open](command-palette.md), [Repositories dock](../runner/frontend/src/components/DockRepositories.tsx). Runtime reader binding is required; legacy sandbox reads without it return unavailable. Native macOS host workspaces remain unavailable (#181); local sandboxes on macOS use their Linux runtime. |
| WORKSPACE-03 | Quota-backed local preparation, frozen copy, retained recovery and durable result completion | [Sandbox runtime](sandbox-image.md), [desktop transaction adapter](../runner/internal/daemon/workspacecontrol.go). Enabled with verified backend and containment; retained recovery and exact cleanup remain mandatory. |
| WORKSPACE-04 | Independent cluster member credentials, retained claims and generation-fenced settlement | [Cluster runtime](cluster-workspace.md). Enabled after the real-pod two-member happy-path smoke, including explorer, shell, sync and stop cleanup. Protected storage and containment are required; the full failure/measurement campaign is deferred (#180). |
| WORKSPACE-05 | Owner-only shell tabs and splits; supported personal desktops retain shells through sleep and reconnect with per-shell consent and offline withdrawal | [Owner shells](owner-shell.md), [supervisors](owner-shell-supervisors.md). Enabled on supported workspace runtimes; the palette/menu/shortcut appear only after an affirmative owner capability response. Native macOS host stays OFF (#181). |

Cluster smoke acceptance has limited scope: default hostpath/local-path storage does not enforce byte/inode quotas, and exhaustion tests were not run. AlwaysAllow clusters cannot enforce RBAC boundaries (#182); installation warns and a successful smoke reports `pass-rbac-not-enforced`. The full failure and measurement campaign remains deferred to #180 (0.5.1).

Workspace tabs also support [nested split groups](session-tabs.md#split-groups)
(#218): edge drag previews, resizable dividers, focused-group shortcuts and
per-session layouts. Phones retain the layout while displaying one group.
Implementation: [group tree](../runner/frontend/src/lib/sessionGroups.ts),
[group surface](../runner/frontend/src/components/SessionGroups.tsx), and
[browser coverage](../runner/frontend/src/components/SessionGroups.browser.test.tsx).

- Configurable account work-branch templates: [Work branches](work-branches.md).

### Workspace channel categories

The Channels dock stores categories on the server, shared by the workspace. Main stays first;
unfiled workers stay under Workers. Open a row's actions with right-click, Shift+F10, or a
touch long-press. The menu offers New category, filing, and category rename, delete, Move up,
and Move down. Desktop users can also drag channels or workers into a category, or drag a
category onto another to place it before that category. Categories summarize running, unread
channel output, failures, and person attention using the same summary as Workers. A selected
session or one needing attention opens its category and ancestor channels automatically. Deleting a category unfiles its sessions and never
deletes them. Archived sessions remain in Finished. History is unchanged.

Main can use `blerg-runner category list`, `create "Research"`, `rename <id> "Reading"`,
`move <id> --before <category-id>`, `delete <id>`, and
`file <session-id> --category <category-id> [--before <session-id>]`. Omit `--category` to
unfile a session; omit `--before` to move to the end. Only the workspace owner and Main's
own session credential can organize that workspace. Other open views refresh within ten
seconds or when focused. Migration 112 adds the workspace category state.

The API is `GET /api/sessions/{root-id}/categories` and action-based `POST` to that route.
POST accepts `action` (create, rename, delete, move, file), category `id`, `name`, `session`,
and optional `before`. Reads omit discarded, missing, and unrelated children without taking a
workspace row lock. Writes decode the request first, then serialize on the workspace with
`FOR NO KEY UPDATE`, allowing concurrent child foreign-key inserts. A stale action refreshes
the dock and asks the person to review the refreshed list before trying again.

Insights opens as one ordinary, closeable tab in the current workspace panel set,
sharing drag, split and focus behavior with graphs and boards. Its four views ask
whether agents are working well, what a card costs, where time goes and what is
being spent. Each opens with its question and an answer from recorded data, then
one chart. Card cost switches between active time by size, reviews by building
engine and rework by engine; card and conversation phase charts retain drill-down.
There is no board-phase timing. Startup, token tables, prices, lifetimes, outcomes
and review records live under one Details disclosure at the bottom.

The toolbar shares the Board's compact filter treatment. On narrow panels, view
and range selects keep it compact. Filters and range persist per signed-in person.
Visible reports refresh every 30 seconds without dimming, adding loading text or
replacing the current report. Hidden panels and browser tabs suspend reads. Bounded,
account-scoped in-memory caches redraw reports immediately on remount; identity
changes clear them. First loads use the Board's shared skeleton bars, including
reduced-motion support. Diagnostic tables stack based on panel width.

Legacy Insights bookmarks continue to resolve: Flow opens Card cost, Usage opens
Spending, and Overview, Reviews and Live open Agent health. The workspace query
keeps scope across views. Missing stalls, restarts and intervention telemetry stay
explicitly unavailable. Spending groups the largest workspaces' range-scoped token
usage by explicitly linked engine; unknown engines stay unknown, and the answer
states that full engine totals are unavailable. Existing account usage meters sit
beside this context and retain their own provider windows. APIs remain unchanged.

Insights engine observations require an explicit risk tag and disclose that
mission-critical work goes to one engine. They show classified active time with
sample counts, not a randomized performance ranking. Small groups show raw dots.

### Person-started channels

The dock's Channels tab offers **+ New channel**. Name the chat inline and choose
an engine available on the workspace's runtime. Repositories and permissions are
inherited by default; **Change repositories and permissions** selects a repository
subset or starts without inherited permissions. Each channel uses the worker
launch authorization and isolated workspace machinery, with its own branch.
Legacy host workspaces must be restarted on a Git repository before adding channels.
Channels stay interactive and survive Main stopping. Main can read public channels,
but only the person can control any channel. Channels cannot start workers yet
and do not inherit worker-start grants. They do not use Main’s worker or hourly
start limits; account limits still apply. Waiting channels ask the person and
contribute to the global waiting count. Opting out of inherited permissions never
removes the workspace’s tool restrictions. A private channel is readable
only by its person: Main can list it and file it into categories, but cannot read
its chat, events, results, or files. Channel questions and turn summaries are never
automatically relayed to Main.

The browser launches through `POST /api/sessions/{id}/channels`, which requires the
workspace owner's live human sign-in. The body accepts `title`, `engine`, optional
`repos` (workspace member IDs; omitted means all), `inherit_permissions` (default
true), and `private` (default false; a private workspace always remains private).
No migration is needed: the existing parent link and personal origin distinguish
channels from workers. Create accepts an optional `Idempotency-Key` header; retries
with the same request return the same channel, and changed requests return 409.
The browser keeps the key for retries of unchanged options.
Board release planning (#66): per-board releases, target dates, planned/in progress/released states, card assignments, release filters and grouping, column progress, open/blocked cards, and atomic cuts with rollover and searchable archived Done records. REST and MCP writes follow the person-behind rule. Draft release notes and epics are deferred.

The workspace Board button opens a new native tab, including another view of an
already open board. Each tab saves its own filters, visible columns, focused
column and selected card across reloads. New tabs seed the person's saved column
visibility. Board names include their focused column and a copy number when needed.
Legacy saved board tabs remain available. Boards share file tabs' split,
focus and close behavior; columns scroll inside their group. Text, tag and active release filters
use the existing board search through the runner proxy. Visible boards refresh every 15 seconds,
on returning to the page, and after a move. A card opens the shared live ticket dialog outside the pane. On phones, the live ticket header puts named icon actions on a separate row, keeps the key on one line with a full-key tooltip, and scrolls away with the details; status choices stay on one horizontally scrollable row with compact field pills. Its Open as tab action keeps the live card in a workspace tab; Escape closes the dialog without closing its board tab. Assigned workers appear as kind and state glyphs with hovercards and open their conversations; Full board
access permits dragging between columns. Read and File access keep moves disabled. Open in Board
provides the board's remaining actions. Command search and a card sheet can also open board tabs.

Double-click a column header or use its focus control for a responsive grid at
the normal card width. All columns or Escape returns; phones offer the same
control beside the column selector. Search, filtering and card details remain
available in focus. Initial runner loads use the shared board skeleton.

Tab context menus use the dock row menu for closing, closing other tabs or tabs
to the right, duplication and moving to a split panel. Keyboard Menu or Shift+F10
and touch long press open the same menu. Conversations omit duplication; board
duplicates copy their source view. Actions follow the existing tab control paths.

Board tab moves send the displayed card version and report a conflict when another writer
changes or archives it. Pinned-card Status still uses the person's board role; only native
tab reads and moves require the workspace grant. Boards without a readable grant open in
the Board app. Card dialogs offer Open board tab only with a matching workspace host and grant.
Tabs use canonical board IDs and current permission labels. Columns expand on wide screens;
on phones, tap a card, then Status to move it. Filters wait 300 ms; polling pauses in hidden
tabs, backs off to two minutes after failures and retains the last good data with a dismissible, timestamped notice. Overlapping
reads share one request; browser reads do not consume the agent call limit.
Columns and releases are cached in the tab for one minute. Each refresh reads at most ten
100-card pages, removes duplicates and sorts cards by rank; narrow filters when the limit is reached.

Board moves accept version zero for new cards. A conflict refreshes the card before the next
person action and never retries a move automatically. Failed polls keep open sheets unless
access was denied; unknown card columns trigger a metadata refresh. Worker and channel chat
sheets open board tabs with that chat's workspace grant; sheets already in a board tab hide
the redundant action. Pinned GET reads use the person's board role even at Propose access.

Questions use plain dock rows with an asker and answer ownership line, clamped hover
previews and a full question dialog capped at 960 px and 92% of the viewport, with 24 px padding.
Escalation prefixes become asker metadata for display; agent messages stay intact. The
dock's Questions tab lists attention across the workspace and can be moved like the other
tool tabs. Needs you also opens as a retained workspace tab from its panel heading: the panel
shows the newest three items, while the tab groups the full list by worker or channel, newest
first, with runtime glyphs, group counts, a worker filter, and the shared chat-style question
cards. Their one-line answer composers expand on focus; conversation links stay in the card header. Its count, the Questions badge, workspace card
count and Needs you panel count share one item collection,
including asks, file reviews, pending plan proposals, failed workers and paused workers.
Plan proposals open the existing read-only plan review; decisions still require Main's bound question. Person answers use the existing message reply route; questions routed to Main
remain read-only for the person until escalated.

Posted diffs offer Open changed and Open original on each file. These show the full
blob in Blerg's file viewer at the named commit (original uses its first parent),
including commits retained only on a work branch. Added and deleted files explain
which side is absent. Multi-repository workspaces offer a repository picker; reads
retain the existing owner scope, runtime generation fence and 2 MiB commit limit.

Question dialogs keep an unfinished answer per question while the page remains open, including
when dismissed with Escape or the backdrop. Commit-file previews disable relative Markdown
links and images and do not offer Save a copy to Files. Full-file actions require a commit
named by the patch file; a viewer navigation target never supplies a missing commit.

Inbox (BLG-516): recipient-private durable reading material, Main-only
posting and CLI, cross-device read state, icon explorer tabs, compact Inbox list,
one reused workspace reading tab and recipient-only arrival cards. Migration 153.
Inside a workspace, Inbox lists, item actions, pagination and unread badges are scoped to that workspace’s Main (BLG-622). The Inbox API accepts `workspace=<Main session ID>` on list, get, read and delete; foreign items return 404. Omitting it preserves existing account-scoped callers.
Inbox items can be deleted from the reader or list; a five-second toast offers Undo before the recipient-only DELETE request; reloading during that grace period cancels the pending deletion. Reader ticket references use the source session’s board and open live ticket hovercards and dialogs (BLG-523).
Collapsed-toggle counts, list-heading access, command palette, Ctrl/⌘+Alt+I and recipient-scoped push notifications complete chunk 2. Inbox pushes follow quiet hours (BLG-515).

Account Settings includes a time zone, initialized once from the browser and stored per account.
Agent inputs carry the person's current local time, zone abbreviation and IANA zone on each turn,
including startup/recovery; UI timestamps continue using the browser's zone.
Quiet hours are an optional daily window in that account zone (off by default). During the
window, ordinary push notifications are held durably and combined to the latest per session;
Needs you retains questions. The server drains held pushes after the window, rechecking
privacy and live browser login proofs, and summarizes activity when held questions have already been answered. Explicit
`blerg-runner ask --urgent` and destructive/command-approval asks still notify immediately.
The shared server helper is `inQuietHours(account persontime.Account, now time.Time)`;
its wall-clock interval includes the start and excludes the end, including DST transitions.

The zone picker searches the browser's IANA location list (plus UTC); abbreviations such as
EST are refused because they can lose DST rules. Account time PUT replaces the entire value:
omitting quiet_hours turns the window off. Daemon-made wake-ups and restart continuations
inherit the latest saved account clock. Quiet push leases last until the window ends; changed
settings release leases for rechecking. The drain suppresses pushes for an active person and
retains an ask link when later updates arrive, with an activity-count summary if it was answered.
A clock lookup failure logs its typed cause and lets notification delivery proceed.

Bound decision answers use the current browser login over HTTP, refreshing and retrying after an expired token. Their question dialogs offer Approve (`yes`), Decline (`no`), and a text reply for clarification. Ordinary questions keep their text reply. Question dialogs cap their width at 960 px and 92% of the viewport.

Decision dialogs hide redundant typed yes/no instructions while their decision buttons are present; the server ask text and chat preview retain them. Plan change lists use compact rows, showing eight changes before a disclosure reveals the rest.

Bound decision questions use server metadata to show Approve/Decline in Questions, Needs you and chat, even before a transcript is loaded. Plan summaries name boards and card keys, retain reasons, and keep proposal identifiers in the bound subject.

Question rows in Needs you and the Questions tab resolve ticket references against the
question's originating session board, including decision snapshots. Hover a ticket key
for its card and select it to open the ticket dialog. Explicit question withdrawals
appear as a quiet “Question withdrawn” notice; real closures retain an update without
exposing the question ID. Visible thinking and progress are requested in the agent's
own first-person voice; native reasoning content is passed through verbatim.

Question discussions persist separately from answers. People and Main may exchange
any number of replies; Main can revise an unbound question in place with history.
Needs you names whose turn it is. Explicit Answer, Approve/Decline or Dismiss
resolves a question; discussion, including “yes”, never commits a bound decision.
A dialog's Open as tab icon opens or focuses one `Question: <short title>` tab.
Tabs retain the same thread and a read-only outcome until closed; none opens
automatically. The implementation is `question_threads.go`, the CLI's
`ask --reply` / `ask --revise`, and the shared `questionThreads` UI.

Stopped desktop Local sandbox sessions offer **Release storage** beside the
session controls. The signed-in owner reviews ignored, untracked, and modified
files and potentially unsaved references, then explicitly confirms deletion.
Agents cannot confirm this action. The preview binds the current file bytes and
references; changes require a fresh preview. Release uses the private workspace
journal and verifies runtime absence. Acknowledgment prunes the per-session
workspace and allocation records; failed or uncertain operations retain their
journals for retry. History and already delivered commits remain available.

Automatic clean-copy settlement permits only regular ignored, untracked files
matching explicit cache layouts: direct `*.pyc`/`*.pyo` files in `__pycache__`;
`.pytest_cache` metadata (`.gitignore`, `CACHEDIR.TAG`, `README.md`) and
`v/cache/{nodeids,lastfailed,stepwise}`; `.mypy_cache` metadata and Python-version
module `*.data.json`/`*.meta.json` files or `@plugins_snapshot.json`;
`.ruff_cache` metadata and version-directory numeric cache files; and known
Babel-loader hash JSON or Webpack pack files under `node_modules/.cache`.
Unknown cache contents and nonregular files retain storage. Any nested `.git`
requires confirmation, including one inside an ignored cache directory. A kept
nested repository produces an informational notice rather than a sync error. Tracked
files still require exact saved-byte proof.

Large storage previews show bounded file/folder and reference lists with full
counts. Unsaved commits have reserved preview space and appear before files. Confirmation binds the complete inventory and bytes, including omitted
entries. The dialog explicitly warns that reflogs and commits reachable only
from them are lost. Oversized result frames log a safe cause. Recovery and
pruning reject journals whose filename does not match their embedded session.

Test Performance: an Insights breakout tab imports project gate history with the Go
importer, shows suite-time trends and retains bounded owner-scoped run details.
See [Test Performance](test-performance.md) for ingestion and retention.
