# Blerg feature inventory

This is the product and operator feature inventory for Blerg. It is also the checklist for engine parity reviews and adding an engine. Features are grouped by behavior, not by every endpoint, configuration variable, button, or internal helper. A feature existing here does not promise that every engine or runtime supports it.

Initial inventory: source at `fc748ec`, reviewed 2026-10-16. Updated for the isolated 0.3.0 implementation; this does not imply deployment. See [the parity and abstraction review](design/engine-parity-review.md) for findings and coverage, and [Adding an engine](adding-an-engine.md) for implementation work. The adjacent Inkhold application is a consumer, not a Blerg component; its integration findings are recorded separately in the review.

Stable identifiers make omissions and changes reviewable. When adding, removing, or materially changing a feature, update its row, its implementation link, and the applicable parity tests. Do not renumber existing identifiers. New provider-native features should say which engines and runtimes actually implement them.

## Accounts, identity, and platform discovery

These features are shared control-plane services, independent of the coding engine.

| ID | Feature | Implementation / contract |
| --- | --- | --- |
| CORE-01 | Local account login, bootstrap administrator, and required initial password change | [Authentication](../core/internal/api/auth_handlers.go), [local provider](../core/internal/authprovider/local.go) |
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

## Session launching and control plane

Sessions on a desktop can start and drive their own child sessions with owner approval, standing
rules or session grants. Child controls, files, live status and separate usage totals share the
engine-independent runner services; nesting is limited to one generation. See
[sessions starting sessions](talking-to-an-agent.md#when-the-agent-starts-other-sessions).

Implementation roots: [server](../runner/internal/server/), [DB](../runner/internal/db/), [MCP](../runner/internal/mcp/), [gateway](../runner/internal/mcpgw/), [protocol](../runner/internal/protocol/). Evidence paths below are relative to those roots unless a full path is shown. GAP references identify findings in the accompanying review.

| ID | Feature/surface | Claude | Codex | Evidence, constraints and tests |
| --- |---|---|---|---|
| RUN-01 | Public agent/component discovery, OpenAPI, version/package URLs | shared | shared | `agents.go`, `openapi.json`, `coreregister.go`, `packages.go`; authenticated dynamic launch discovery is separate from the public component manifest. |
| RUN-02 | Browser HTTP session launch | supported | supported | `api.go:618` spawn request engine/model/effort/runtime/kind; validation/dispatch later in file; `model_effort_test.go`, `spawn_validation_test.go`, `api_test.go`. |
| RUN-03 | Programmatic REST headless start | supported | supported | `runner.go`, `engine_targets.go`; engine/model/effort/interaction/scratch/auto-stop/callback/idempotency; engine-aware placement and explicit host pins. |
| RUN-04 | MCP headless start and launch discovery | supported | supported | `mcp/tools.go`, `mcp/server.go`; start/discovery delegate the same authorized services as REST. |
| RUN-05 | Launch idempotency and replay | shared | shared | `runner.go` request hashing/stable retries; `StartRequest` engine participates in body; `runner_test.go`, `model_effort_test.go:307`, MCP retry tests. |
| RUN-06 | Interaction mode / watched vs unattended | shared | shared | `interaction.go:35-69`; browser defaults interactive, tools unattended, cron always unattended; preserved on resume; `interaction_test.go`. |
| RUN-07 | No-repo scratch sessions | supported | supported | `norepo_test.go`, `engine_placement_test.go`; scratch placement honors requested engine/runtime and explicit host pins. |
| RUN-08 | Host execution and local Docker runtime | supported server dispatch | supported server dispatch | runtime selection `runner.go`, daemon selectors `hub.go`; Docker/bare-host permissions distinct; daemon engine operation outside scope. |
| RUN-09 | Cluster execution / scheduling / capacity | supported | supported | `k8sjobs.go:965` launch/secret/spec flow; per-runtime config/capacity `clustersettings.go`; `k8sjobs_test.go`, `startstages_test.go`. |
| RUN-10 | Principal/target-scoped available engines | supported | supported | `launch_options.go`, `launch_readiness.go`, `engine_targets.go`; known/unknown/empty states, asynchronous readiness, personal-only option for crons. |
| RUN-11 | Model catalog and per-model efforts | supported public catalog | supported native account/CLI catalog | `models.go`, `clustermodelproviders.go`, `clustermodelcache.go`; account-bound headless callers and operator keys use separate authorized scopes. |
| RUN-12 | Backend cached catalogs / startup restore / bounded refresh | supported catalog source | supported account/engine/image cache | `clustermodelcache.go`, `db/clustermodelcatalog.go`, migrations047/048; asynchronous refresh/last good snapshot; `clustermodelcache_test.go`, `models_test.go`. |
| RUN-13 | Launch model/effort validation | supported | supported | `models.go:191-235`; engine rules plus known model efforts; per-daemon validation only where target known. Headless unknown-target only allowlist documented; `model_effort_test.go`. |
| RUN-14 | Live model/effort control | WS/REST/MCP | WS/REST/MCP | `runner_model.go`; one authorized setter, immutable native engine, next-turn dispatch acknowledgement and effective native metadata. |
| RUN-15 | Session listing/title/star/read/privacy metadata | shared | shared | `api.go`, `db/queries.go`, browser WS mark/read/star, privacy guards; `api_test.go`, `privacy_test.go`, `privacy_scope_test.go`, `privacy_guard_test.go`. |
| RUN-16 | Start progress and stage diagnostics | shared | shared | `startstages.go`, `startstages_test.go`; ordered persisted start_stage plans, native/pod reports. |
| RUN-17 | Native conversation recovery on Cluster | supported registered layout | supported registered layout | `nativehistory.go:47,122,218,287`, nativehistory registry; owner/engine/PVC UID/writer fence, opt-in storage/capacity/retention; `nativehistory_test.go`. Workspace is separate, not backed up by conversation PVC. |
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
| RUN-34 | Live session aggregate / Running Now / status-aware subagents | shared | shared consumer | `livestate.go:60,142`, `db/livestate.go`, shared livestate reducer; human-only view intentional; `livestate_test.go`. Correct native child emit coverage daemon scope. |
| RUN-35 | Loaded session capabilities | supported normalized payload | supported normalized payload | `agentevents.go:264`, `protocol/capabilities.go:13-182`; bounded six groups, unknown vs empty; sanitize/persist/replay tests in `capabilities_test.go`. |
| RUN-36 | Session Insights | shared with settled usage semantics | shared with settled usage semantics | `session_insights.go`, `db/insights.go`; child model/configured-effort updates, validated root/child token partitions, costs and counts; ambiguous attribution stays unknown. |
| RUN-37 | Install Insights / prices / Prometheus metrics | shared | shared | `insights.go`, `insights_calc.go`, `metrics.go`; configured per-model prices, owner/admin token visibility; `insights_api_test.go`, `insights_calc_test.go`, `metrics_test.go`. Active usage shared settled-usage limitation. |
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
| RUN-57 | History topics/outcomes/commit links | shared | shared | `topics.go:34`, `db/reel.go`, topic/reel reducers; session messaging token and git-provider refs; topics/reel tests. CLI access supplied to both engines. |
| RUN-58 | Project memories/knowledge search/rules | shared | shared | `dataplane.go`, `dataplaneauth.go`; exact current repo, unrestricted runtime only; approved rule reads vs admin mutation; `dataplaneauth_test.go`, `dataplane_test.go`. Engine-specific tool exposure daemon scope. |
| RUN-59 | Runner-integrated boards/columns/tickets/dependencies/events | shared | shared | `boards.go`, `columns.go`, `tickets.go`, `boardauth.go`, DB board/ticket graph/rank/token files; same REST authorization, no engine checks. Legacy Assist also validates and forwards engine/model/effort. |
| RUN-60 | Push notices/unread/idle/waiting and background completion | shared | shared normalized events | `push.go`, `notices.go`, `sessions.go`, `livestate.go`; suppress headless asks/notices, defer idle with running child work; `notices_test.go`, push/session tests. |
| RUN-61 | Health/config/version/registration/metrics auth | shared | shared | server main/config wiring and `coreregister.go`; version/publish admin paths no engine assumption; no release/restart behavior changed. |
| RUN-62 | MCP gateway protocol, per-session grants, upstream sessions/auth, call/result/concurrency budgets and audit | Shared | Shared | [Gateway implementation](../runner/internal/mcpgw/) |
| RUN-63 | Frozen write proposals, approve/decline/expiry, bounded queues and explicit unknown outcomes | Shared | Shared | [Gateway proposals](../runner/internal/mcpgw/proposals.go), [proposal guide](proposals.md) |
| RUN-64 | Canonical tool definitions, pins/drift policy, live revalidation and repin/change review | Shared | Shared enforcement; native advertised-tool refresh limit remains | [Tool drift](../runner/internal/mcpgw/drift.go), [live tools](../runner/internal/mcpgw/livetools.go) |
| RUN-65 | GitHub/GitLab remote parsing, repository list/create, clone/web URLs and token/redaction policy | Shared | Shared | [Git providers](../runner/internal/gitprovider/), independent of coding engine |
| RUN-66 | Cron timezone parsing, missed-fire/grace, persisted claims, watchdog and credential-failure maintenance | Supported | Supported | [Scheduler](../runner/internal/cron/), fixed restricted-engine policy remains |
| RUN-67 | Existing-session catalog discovery bound to immutable engine and actual host/runtime | supported | supported | `launch_options.go`, `runner_controls_test.go`; owner authorization, no fallback to another execution host. |
| RUN-68 | Live native MCP readiness and bounded first-input hold | Persistent Claude Code control protocol | Codex app-server thread status | [Shared tracker](../runner/internal/daemon/mcptracker.go), [live contract](../runner/README.md#mcp); owner-only release, durable snapshots, gateway scope, process restart tracking. Legacy engines without a live source remain explicitly unknown. |
| RUN-69 | Read a live session's workspace file | Shared host/container/pod primitive | Shared host/container/pod primitive | [Workspace read contract](../runner/README.md#opening-a-session-workspace-file); human owner/admin only, bounded reads, launch-folder identity, no symlink escape, protected names and hardlinks refused; Linux/macOS, other platforms fail closed. |

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
| EXEC-14 | Root text and thinking | stream partials, optional native CLI flags | agentMessage deltas/final + reasoning item transitions | Both rendered into shared event contract. Codex reasoning summary is completed-item content. |
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
| EXEC-40 | Terminal exact recovery | recorded Claude session ID | private Codex terminal home, bounded cli-source transcript identity | Both supported with real CLI fixtures. Obsolete comments still say Codex cannot resume; implementation does. |
| EXEC-41 | Session CLI publish/fetch/topic/review | buildSessionEnv + messaging CLI on PATH | same | Engine-neutral tool availability; no separate daemon artifact pipeline needed. |
| EXEC-42 | Session token refresh | shared sessiontoken file/sandbox copy | same | Master token excluded from child env; private launch auth fetched separately. |
| EXEC-43 | Managed refresh authority | Claude existing credential mode, provider-owned refresh outside this Codex broker | Core-owned managed source -> external native login/refresh callback | Codex supported for explicit Cluster enrollment. Imported credential snapshots remain separate. |
| EXEC-44 | Managed login in workstation sandbox | not this enrollment path | explicitly refused by prepareCodexRestrictedRuntime | Deliberate supported-shape boundary; host credentials use mounted native login. |
| EXEC-45 | Repo clone/new/no-repo/provision | shared workspace setup | same | No confirmed engine-specific omission in shared path. |
| EXEC-46 | PTY resize/reconnect/state tracking | shared tmux/pty, rendered terminal heuristic | same | Headless state native-driven; terminal heuristic includes Codex fixtures. |
| EXEC-47 | Host install engine readiness | installer/hostcheck heuristics | same scripts with Codex check | Multi-engine supported; Codex custom-home readiness honors an absolute CODEX_HOME. |
| EXEC-48 | Status/open-work/restart cause | private records, same installer restart metadata | same | Both report unfinished work and planned restart notices; [shared command guidance](design/codex-guide-parity.md) reaches native, terminal and recovered Codex sessions with shell access. |
| EXEC-62 | Engine plan in the session panel | Root TaskCreate/TaskUpdate and TodoWrite folding | Root turn/plan/updated snapshots | Shared `/live` plan shape; [plan adapter](../runner/internal/daemon/codexplan.go), [fold](../runner/internal/livestate/plan.go). Child/stale/malformed updates are ignored; explicit empty snapshots clear. Restricted Codex keeps update_plan disabled. |

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
| EXEC-60 | Blerg memory/rules/knowledge | `agent/tools/dataplane.go` memory_write/delete, rule_propose, knowledge_search; `daemon/agentsession.go:1054-1069` fetch approved rules + memory index into cfg.System | Explicit tool registration and automatic context are direct-loop-only. Both native CLI drivers discard this cfg. This is a real cross-backend integration distinction, not Codex-specific regression. API capability exists separately. |
| EXEC-61 | Legacy visual publishing tools | `agent/tools/publish.go` push_mockup and push_screenshot with caps, symlink skips | CLI agents use shared blerg-runner publish; formats/routes are not identical legacy tool names. |

## Chat, session panel, and embedded surfaces

Implementation roots: [runner frontend](../runner/frontend/src/), [shared chat](../packages/chat/src/), [Go proxy](../packages/chat/proxy/go/), [embedding contract](../packages/chat/README.md).

| ID | Feature | Claude / Codex assessment | Implementation / limits |
| --- | --- | --- | --- |
| CHAT-01 | Runner session launch | Principal/target-scoped available engines; sole-engine selection hidden | RunColumn, LaunchSheet, lib/launchOptions |
| CHAT-02 | Runner model/effort picker | Dynamic per engine/target; cached with bounded pending/error/retry states | lib/engineModels, lib/launchOptions, ModelPicker, ModelCatalogNotice |
| CHAT-03 | Runner cron scheduling/runs/MCP grants | Dynamic restricted-engine choices; Cluster catalogs use personal credentials only | CronForm, CronsPage, CronRuns, McpPicker |
| CHAT-04 | Session list/search/archive/places/favorites/history | Shared Session metadata | SessionList, ArchiveView, HistoryTab, lib/sessionRail |
| CHAT-05 | Session status/end/start progress/retry/recovery | Shared event contracts; requires backend support | SessionDetail, SessionPlaceholder, StartSteps; chat startStages/sessionEnd |
| CHAT-06 | Mid-turn messaging/queue/drafts/replay/pagination | Shared normalized chat paths | packages/chat hooks/useSession, useTranscriptStore and queue/replay/tail tests |
| CHAT-07 | Subagents/running panel/background tasks | Shared normalized child/task event display; late model updates supported in Live | lib/liveState.ts:44; chat model/subagents/liveState, hooks/useLiveState |
| CHAT-08 | Ask/reply/note/check-in | Shared ask routing; options text only | chat AskReply/model/asks; board proxy message source human |
| CHAT-09 | Session settings/limit reset resume | Shared runner settings; provider capability needed | UsageLimitResume, SessionRail, UsageMeters |
| CHAT-10 | Capabilities/skills/plugins/MCP/tools | Data-driven rendering; unknown groups explicitly marked | CapabilitiesPanel.tsx:3,140; lib/capabilities |
| CHAT-11 | Tool changes/proposals | Shared API contracts | ToolChanges, Proposals and lib/proposals/toolChanges |
| CHAT-12 | Terminal sessions/touch/selection/screen | Shared terminal renderer; CLI ability distinct from agent engine | TerminalDOMView, terminalRenderer/terminalKeys tests |
| CHAT-13 | Usage meters/Insights/cost prices | Both named; estimates/completed work limitations | UsageMeters; Insights, SessionInsights; lib/insights |
| CHAT-14 | Files/attachments/previews/download/delete | Engine independent artifacts service | runner FilesTab/ArtifactViewer and chat transport/files/FilesPanel |
| CHAT-15 | Artifact text/Markdown/PDF/image/HTML/CSV/review/markup | Shared publish/review contract | chat ArtifactViewer, components/review, components/markup, model/artifacts |
| CHAT-16 | Sound/notifications/theme/auth/version/update | Shared UI infrastructure | runner notify/sound/authClient/UpdateBanner; board corresponding modules |
| CHAT-17 | Embedded Chat transport direct Blerg WS | Both normalized; host owns model picker | packages/chat transport/blerg, ChatView slots |
| CHAT-18 | Embedded proxy Chat | Normalized transcript/files/control; optional model/effort forwarding and durable IDs, visible unsupported errors | transport/proxy, proxy/go, board chatproxy |
| CHAT-19 | Inkhold embedded writer | Dynamic Blerg engine/model/effort discovery, interactive turns, explicit native engine handoff | Separate consumer branch and tests; direct Anthropic backend remains distinct |
| CHAT-20 | Published artifact links open the existing viewer | Exact trusted session URL plus known viewable artifact; modified/external links preserve navigation | chat model/artifactLinks and Markdown links; authenticated host URL mapping |

## Boards and agent workflows

Implementation roots: [board API](../board/internal/api/), [board DB](../board/internal/db/), [board MCP](../board/internal/mcp/), [board web](../board/web/src/). The board admission gate is direct inference, distinct from native coding sessions.

| ID | Feature | Claude / Codex assessment | Implementation / limits |
| --- | --- | --- | --- |
| BOARD-01 | Board CRUD, templates, icons, columns, ordering | Shared, engine independent | api/api.go:93-103; board/web BoardView, BoardSettings |
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

## Installation, operations, and distribution

| ID | Feature | Implementation / contract |
| --- | --- | --- |
| OPS-01 | Desktop Docker Compose stack with separate Core/board/runner databases and persistent volumes | [Desktop installer](../install/desktop/README.md), [Compose](../install/desktop/docker-compose.yml) |
| OPS-02 | Desktop prerequisites/engine-login checks, stack health checks, and uninstall instructions | [Host checks](../install/desktop/hostcheck.sh), [installer](../install/desktop/blerg-up.sh), [uninstaller](../install/desktop/uninstall.sh) |
| OPS-03 | Linux systemd-user and macOS launchd daemon installation/configuration | [Daemon installer](../install/desktop/daemon/install.sh), [daemon guide](../install/desktop/DAEMON.md) |
| OPS-04 | Kubernetes build/push/deploy, configurable namespaces/images, ingress/TLS, and upgrade/rollback checks | [Cluster installer](../install/k8s/README.md), [deploy script](../install/k8s/deploy.sh) |
| OPS-05 | Agent-readable installation runbook, checkpoints, secret handling, and handoff verification | [Kubernetes agent runbook](../install/k8s/AGENT-INSTALL.md) |
| OPS-06 | External secret references, Vault choices, cluster session RBAC, and example NetworkPolicy | [Cluster runtime guide](../install/k8s/CLUSTER-RUNTIME.md), [session resources](../install/k8s/cluster-runtime.yaml) |
| OPS-07 | Extend/replace agent images, pinned engine CLI dependencies, and reproducible image builds | [Image guide](sandbox-image.md), [pinned engines](../runner/sandbox/engines/package.json), [Dockerfile](../runner/sandbox/Dockerfile) |
| OPS-08 | Data/database/key backup and restore, state inventory, and recovery drill | [Backup guide](backup-and-restore.md) |
| OPS-09 | Configurable session-resource limits, idle expiry, retained-history lifecycle, and operational status | [Cluster guide](../install/k8s/CLUSTER-RUNTIME.md), [Job manager](../runner/internal/server/k8sjobs.go) |
| OPS-10 | Health endpoints, component/build versions, operational logging, and Prometheus metrics | [Telemetry](telemetry.md), [build/version contract](../CONTRIBUTING.md#versions-and-releases) |
| OPS-11 | Development builds, Go/web tests, lint, browser tests, and disposable database checks | [Contributing](../CONTRIBUTING.md), [Makefile](../Makefile), [CI](../.github/workflows/ci.yml) |
| OPS-12 | Privacy scrub, documentation link checks, installer checks, and release guard scripts | [Scripts](../scripts/), [Contributing](../CONTRIBUTING.md#tests) |
| OPS-13 | Unified semantic version/changelog, public release assembly, tags, and release metadata | [Release guide](../CONTRIBUTING.md#versions-and-releases), [release script](../scripts/release-public.sh) |
| OPS-14 | Security reporting policy and documented trust/containment limits | [Security policy](../SECURITY.md), [MCP security properties](mcp-connections.md#security-properties-and-their-limits) |

## Inventory maintenance and completeness

This inventory describes all identified feature families in the reviewed baseline. It is not a claim that every line, test, or provider behavior was executed. Keep source-backed uncertainty visible in the parity review. A broad audit needs both static tracing and runtime-specific fixtures; a passing shared test does not establish native-provider parity.

For each change, check the browser, REST/MCP, unattended and embedded entry points; principal and runtime scope; persistence/recovery; usage/capability reporting; installer delivery; and failure paths. New engines should consume shared features through declared capabilities rather than copying lists into each feature.
