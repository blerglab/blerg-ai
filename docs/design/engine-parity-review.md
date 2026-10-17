# Engine parity and extensibility review

Initial audit: 2026-10-16 against `fc748ec`. The implementation status below records the subsequent isolated 0.3.0 work; it is not a deployment claim. This review accompanies the [feature inventory](../features.md) and [agent implementation guide](../adding-an-engine.md). It supersedes a narrower application audit: earlier completion claims applied to a shipped implementation scope, not to every Blerg feature.

## Scope and evidence

Three reviewers divided runner control plane; daemon/native engines/runtimes; and board/frontends/shared chat. The parent reviewed Core, contracts, installer/distribution paths and consolidated the results. The neighboring Inkhold consumer was inspected because its embedded chat was explicitly requested. Review used directory/file inventories, routes/tools, contracts, implementation tracing and existing test coverage. It did not execute all product workflows or inspect every line with equal depth.

“Shared” means a common implementation path exists, not that live provider parity was proved. An omission affecting both engines must be classified as a shared defect, enhancement or intentional exclusion; a provider-native restriction is not automatically an application defect. Findings are separated accordingly. Security-sensitive reproduction material belongs in private reporting under [SECURITY.md](../../SECURITY.md), not this document.

## Confirmed application gaps

The table records baseline findings, not a list of defects still open on the 0.3.0 branch. See implementation status below.

P1 denotes a significant workflow unavailable or failing despite intended support. P2 denotes a narrower integration, selection or reporting defect. These are product priorities, not vulnerability ratings.

| ID | Priority / kind | Finding and concrete impact | Evidence |
| --- | --- | --- | --- |
| GAP-01 | P1, discovery | Headless callers can start Cluster Codex but cannot obtain its principal/image-scoped catalog. Ordinary catalog lookup can instead describe unrelated hosts. | [Cluster model handler](../../runner/internal/server/models.go), explicit human-kind check in `handleClusterModels` |
| GAP-02 | P1, consumer | Inkhold's Blerg writer validates a fixed pair of Claude models, serves static choices, and omits engine/effort from launch. Codex-only installs cannot use it. | Adjacent consumer: `inkhold/internal/config/config.go`, `internal/api/writer.go`, `internal/runner/runner.go`, `web/src/writer/Composer.tsx` |
| GAP-03 | P1, placement | Ordinary headless host placement does not filter requested engine. An unsuitable lower-ID host can win over a Codex-capable host. | [Start placement](../../runner/internal/server/runner.go), [DaemonForRepo/DaemonForScratch](../../runner/internal/server/hub.go) |
| GAP-04 | P2, discovery/UI | Launch, cron, and board engine inventories are fixed. Unavailable choices remain offered; the selector remains for a single-engine installation. | [RunColumn](../../runner/frontend/src/components/RunColumn.tsx), [CronForm](../../runner/frontend/src/components/CronForm.tsx), [BoardSettings](../../board/web/src/BoardSettings.tsx) |
| GAP-05 | P2, availability | Codex availability checks only the default home while probes/execution honor `CODEX_HOME`, producing false unavailable state for custom homes. Installer preflight has the same default-home assumption. | [Daemon detection](../../runner/internal/daemon/engines.go), [probes](../../runner/internal/daemon/modelprobe_engines.go), [hostcheck](../../install/desktop/hostcheck.sh) |
| GAP-06 | P2, shared integration | Board/Inkhold clients call an unimplemented runner REST model route. Browser WS supports model changes; shared Go chat proxy returns 501, and its TS client silently accepts that status. | [Runner routes](../../runner/internal/server/agents.go), [browser WS](../../runner/internal/server/browser_conn.go), [board client](../../board/internal/runner/runner.go), [chat proxy](../../packages/chat/proxy/go/chatproxy.go), [proxy transport](../../packages/chat/src/transport/proxy.ts) |
| GAP-07 | P1/P2, shared recovery | Board chat treats disconnected sessions as permanently unusable, disables input and reports `resumable:false`, despite retained native history support. Backend resume authorization must also be verified; a UI-only change is insufficient. | [BoardChat](../../board/web/src/BoardChat.tsx), [board-session API](../../board/internal/api/boardsession.go), [runner driver lifecycle](../../board/internal/runner/runner.go) |
| GAP-08 | P2, shared settings | Board role/card/session paths omit reasoning effort, so it cannot be selected or forwarded for either engine. | [Board start request](../../board/internal/runner/runner.go), [model API](../../board/internal/api/sessionmodel.go), [MCP schemas](../../board/internal/mcp/tools.go) |
| GAP-09 | P2, metadata | Board drops Claude-prefixed models for non-Claude launches while initially recording the original model. Reverse engine changes are not symmetrically handled. | [modelForEngine](../../board/internal/api/automation.go), [board session record](../../board/internal/api/boardsession.go), [worker launch](../../board/internal/api/runnerapi.go) |
| GAP-10 | P2, validation | Board's handwritten model grammar/length is narrower than runner's, rejecting valid provider IDs or bracket aliases. Currently masked by missing REST switching. | [Board validation](../../board/internal/api/sessionmodel.go), [engine rules](../../runner/internal/models/validate.go) |
| GAP-11 | P2, legacy flow | Runner's legacy board Assist request/spawn has no engine/model/effort and defaults to Claude. Newer board automation does forward engine. | [Assist](../../runner/internal/server/assist.go) |
| GAP-12 | P2, tool discovery | MCP-only callers have no engine/model discovery tool; start instructions assume independent HTTP access. | [MCP tools](../../runner/internal/mcp/tools.go) |
| GAP-13 | P2, shared accounting | Insights excludes `subagent_update` both in SQL and reducer. Late-reported running child models remain unknown until completion. | [Timeline query](../../runner/internal/db/insights.go), [reducer](../../runner/internal/server/session_insights.go) |
| GAP-14 | P2, native reporting | Codex child `model/rerouted` is ignored; without a later settings report, final usage retains the prior model. | [Native notification handling](../../runner/internal/daemon/codexapp.go), [child accounting](../../runner/internal/daemon/codexchildren.go) |
| GAP-15 | P2, consumer lifecycle | Inkhold lacks stored engine/effort and an engine-transition contract; its model-only switch cannot change native engines. It also launches embedded human chat as unattended by omitting interaction. | Adjacent consumer: `inkhold/internal/store/conversations.go`, `internal/runner/runner.go`; Blerg [interaction defaults](../../runner/internal/server/interaction.go) |
| GAP-16 | P2, customization | Daemon-to-Cluster config sync includes Claude/Hermes roots but omits Codex/shared user skill and instruction roots. Managed plugins are a different path. | [Config bundle roots](../../runner/internal/daemon/configbundle.go), [pod download](../../runner/internal/runner/runner.go); isolated archive fixture reproduced omission |
| GAP-17 | P2, background work | One Codex terminal-inventory RPC/decode failure permanently stops the poller and marks known processes stopped/disconnected, even while the runtime remains alive. | [Background poller](../../runner/internal/daemon/codexbackground.go); isolated synthetic RPC fixture reproduced transient-failure termination |
| GAP-18 | P3, capabilities | Successful Codex model switching does not update the stored capability snapshot, so the Capabilities tab can retain the previous model until inventory refresh. | [Inventory snapshot](../../runner/internal/daemon/codexcaps.go), [model setter](../../runner/internal/daemon/codexdriver.go); isolated fixture reproduced stale model |
| GAP-19 | P2, shared reliability | REST/MCP messages lack browser durable client-ID acceptance and receipt replay. Interrupted delivery is ambiguous and retries can duplicate a turn. | [REST send](../../runner/internal/server/runner.go), [browser durable input](../../runner/internal/server/sessioninputs.go) |
| GAP-20 | P2, shared lifecycle | Headless resume reports success after a wrapper discards replacement-launch failure; its prompt does not use the durable browser input path. | [Headless message/resume](../../runner/internal/server/runner.go), [Cluster resume](../../runner/internal/server/k8sjobs.go) |
| GAP-21 | P2, discovery identity | Some discovery routes accept account-bound agent tokens, but credential listing uses their token ID (`Sub`) as the account instead of resolving the owner (`OnBehalfOf`). Shared operator keys have different discovery authorization. | [Credential discovery](../../runner/internal/server/api.go), [principal/account resolution](../../runner/internal/server/coreproof.go), [contract principal](../../runner/internal/server/runner.go) |

The security policy has also been reconciled with current-launch runtime/messaging token separation and engine-specific confinement. Security-sensitive findings and reproduction evidence remain private.

## Shared limits and intentional exclusions

- Headless REST and MCP start already accept engine/model/effort, unattended interaction and auto-stop. Empty engine remains a compatibility default to Claude.
- At the baseline, shared child events did not carry effort. The 0.3 follow-up adds reported configured effort; native omissions remain unknown.
- Insights completion-based token totals omit active work; model prices are administrator-configured estimates. Board aggregate metrics also need separate review for sessions that change models: latest-model labeling can misattribute earlier usage.
- Native questions use text replies; Codex native child questions relay through the parent under the pinned runtime policy.
- Codex child type currently says Codex rather than a richer role. Native role metadata needs validation before promising a mapped taxonomy.
- Shared Chat is a session renderer/transport, with host slots for model choice. It does not own launch authority or offer a reusable discovery selector. That is a valid component boundary, but embedders need an explicit integration contract.
- Board admission gate is a direct-inference service. It supports selected API backends and explicitly rejects Codex CLI login credentials. Supporting a new coding engine does not make its subscription credential usable against a general model API.
- Native resume restores conversation identity, not a workspace backup. Tool-definition changes can require a new native session; removed grants must still take effect immediately.
- Availability probes are advisory: installed/configured, authenticated, model-catalog-known, and launchable are different states. A static engine descriptor must not claim all of them.

## Existing abstractions worth preserving

| Boundary | Existing seam | What it already handles |
| --- | --- | --- |
| Execution | [EngineSpec](../../runner/internal/daemon/engines.go) | Non-Claude CLI invocation, model/effort flags, driver factory, availability, probes, plugins and static capability enumeration |
| Model validation | [EngineRules](../../runner/internal/models/validate.go) | Shared daemon/server rules, safe IDs, engine effort vocabulary |
| Model catalogs | [Sources](../../runner/internal/models/source.go), [Cluster providers](../../runner/internal/server/clustermodelproviders.go) | Engine-owned acquisition with shared normalization, cache and persistence |
| Event/UI contract | [Agent events](../../runner/internal/agent/events.go), [protocol](../../runner/internal/protocol/), [chat](../../packages/chat/src/) | Shared text/tools/usage/children/tasks, replay, Live state, files and review surfaces |
| Native persistence | [Layout registry](../../runner/internal/nativehistory/layout.go) | Engine-owned retained paths separate from authorization/configuration |
| Managed authentication | [Core Adapter](../../core/internal/credentials/adapter.go) | Provider-owned device/refresh protocol with central encrypted authority and declared access mode |
| Plugins | [Core registry](../../core/internal/plugins/service.go), [shared grammar](../../contracts/pluginspec/) | Validation and allowlist enforcement shared across storage and installation |
| Git | [Provider registry](../../runner/internal/gitprovider/) | Provider-specific repository/auth behavior independent of coding engine |

## Abstraction gaps and proposed direction

These recommendations were recorded at the baseline. The implementation status below distinguishes delivered abstractions from engine-specific work that remains necessary. Extensibility requires several deliberate adapter boundaries, not one global registry importing all service implementations.

| ID | Baseline coupling | Proposed boundary |
| --- | --- | --- |
| ARCH-01 | Claude is a special fallback outside the CLI engine registry; native-driver type checks remain in orchestration. | Register every engine under a common descriptor, with optional execution/recovery/question/tool capabilities. Preserve distinct CLI vs direct-inference drivers. |
| ARCH-02 | Separate fixed inventories in runner UI, cron, board DB/UI, Core import/UI/plugin settings, Cluster bootstrap and installers. | Shared serializable engine descriptors; component-owned adapter registration; consistency tests. Generate or discover presentation metadata rather than hand-copy lists. |
| ARCH-03 | Catalogs exist per engine, but launchability is reconstructed by each consumer from different auth/host/secret data. | Authorized launch-context resolver returns effective runtime/target, available engines, restriction support, model/effort metadata and freshness/error state. REST, MCP, browser and embedders call it. |
| ARCH-04 | Shared policy gates spell Claude/Codex; restricted and recovery paths depend on concrete native types. | Capability-based policy with default-deny behavior, backed by native confinement tests. A new engine doesn't inherit restricted support automatically. |
| ARCH-05 | Core accepted kinds, Settings labels/help, Cluster secret mapping and pod bootstrap are separate closed lists. | Credential-kind descriptors and engine-owned delivery adapters; keep git kinds separate from coding-engine availability. |
| ARCH-06 | Core managed adapter is device-flow-shaped; the access response retains `chatgpt_account_id`; runner fetch validation hardcodes `chatgptAuthTokens` before mode binding, so another mode-map entry alone cannot work. | Versioned credential access modes and provider payload adapters; optional login/refresh protocols for non-device engines. Preserve Codex compatibility without treating its wire fields as universal. |
| ARCH-07 | Plugin support is repeated in Core, contracts, UI and daemon installation, with a Claude marketplace default. | Per-engine plugin capability/grammar/default metadata. Do not assume every engine has marketplaces or shares their semantics. |
| ARCH-08 | Config/customization roots and instruction conventions are not a complete per-engine contract. | Adapter-owned declarative roots, precedence, safe snapshot policy and observed inventory. Discovery should not execute hooks. |
| ARCH-09 | Board role settings are model strings with prefix-based coercion; effort and native engine identity are incomplete. | Structured engine/model/effort configuration, explicit inheritance, and effective launch metadata distinct from requested values. |
| ARCH-10 | Browser WS, REST clients and chat proxies expose inconsistent model/recovery operations. | One authorized control service with capability/result semantics and thin transport adapters; unsupported is visible rather than silent success. |
| ARCH-11 | UI/framework logic embeds native assumptions such as disconnected meaning lost history. | Shared lifecycle/resumability reason contract; require fresh launch authority and native-state checks rather than dead-pod heuristics. |
| ARCH-12 | Coding engines and direct inference providers can be conflated by “support every engine everywhere.” | Separate coding execution, direct inference, and optional image/other providers. Advertise capabilities, not brand-name equivalence. |

## 0.3.0 implementation status

These changes live on an isolated implementation branch. Inkhold is a separately committed consumer branch; neither statement implies that a release has been deployed.

| Baseline findings | Implementation |
| --- | --- |
| GAP-01, GAP-03, GAP-12, GAP-21 | Shared authorized REST/MCP launch discovery, agent-owner resolution, exact proof scope, per-engine placement and explicit host pins. Existing-session discovery uses its immutable native engine and actual target. |
| GAP-04 | Runner launch, cron and Board settings consume scoped available engines. A sole choice hides the selector; unknown, unavailable, empty and pending states remain distinct. Cluster crons exclude operator credentials. |
| GAP-02, GAP-15 | Inkhold discovers Blerg engines/models/efforts, persists native identity and selection, launches interactively, and creates a new native session with explicit bounded context when changing engines. Direct Anthropic remains a separate backend. |
| GAP-05, GAP-16 | Codex custom homes honored in detection/preflight; safe customization roots declared by adapters and included in Cluster bundles without copying credentials or native history. |
| GAP-06, GAP-10 | Shared authorized WS/REST/MCP model service and proxy forwarding, actual-session catalog selection, opaque model grammar alignment, visible unsupported failures. |
| GAP-07, GAP-20 | Backend-qualified recovery modes/reasons, fresh automation/owner proof, retained native history checks, explicit resume failures and durable input on public transports. |
| GAP-08, GAP-09 | Board/card/role effort persistence, current effective engine/model/effort metadata, transactional clearing of future-launch overrides when changing engine, stale effort clearing when changing models. Existing native sessions keep their identity. |
| GAP-11 | Legacy Assist accepts, validates, persists and forwards engine/model/effort; selection honors compatible hosts and explicit pins. |
| GAP-13, GAP-14 | Insights consumes child updates; Codex reroutes update child model and usage partitions. SQL uses partitions instead of adding them to legacy aggregate totals. |
| GAP-17, GAP-18 | Background inventory retries transient failures; effective model changes refresh capabilities. |
| GAP-19 | Optional REST/MCP stable client IDs use durable acceptance, content-conflict checks and receipt replay. Hosts without receipts reject the guarantee explicitly. |
| Additional shared feature | Trusted published-artifact Markdown links open the existing viewer on an ordinary click; modified and external navigation retains browser behavior. |

The shared [engine descriptors](../../contracts/engines/) now own implemented runtimes, terminal/restricted/plugin/import capabilities, steering, continuation/window metadata and private credential-key ownership. Core settings consume integration metadata; launch consumers consume authorized availability. Config roots and managed access codecs are adapter-owned. Native driver, model source, credential bootstrap, plugin grammar and retained-history layouts remain explicit implementations: registering a name does not create those capabilities.

The common contracts and transport/persistence changes reduce the coupling identified in ARCH-01 through ARCH-11. Explicit native execution and provider adapters remain necessary; native CLI execution was not merged into the direct provider loop. Non-device login protocols still need an explicit optional interface when an engine requires one; no dummy device flow was introduced. Codex's existing account-field wire name stays confined to its codec. Legacy Board model-prefix cleanup remains a compatibility shim for previously stored mismatches; new engine changes invalidate future-launch overrides symmetrically. ARCH-12 remains an intentional architectural distinction.

Independent reviews covered principal/cache partitioning, placement, delegated controls, recovery authority, native reporting/config export, artifact navigation and consumer session handoff. Review findings received focused regressions and fixes. This is evidence about the reviewed code and fixtures, not a guarantee that no vulnerabilities remain.

## Coverage and maintenance

The following paragraph records verification of the initial documentation-only audit; implementation verification is separate and must not be confused with it.

Verification performed for this audit: ten focused no-database server tests passed under a sanitized environment. The selected protocol/MCP packages compiled but had no tests matching that filter. Four additional isolated daemon probes (five cases) reproduced identified behavior using fake native executables, temporary homes and synthetic RPCs; they are positive bug reproductions, not tests asserting that the implementation is safe or fixed. No live provider, real plugin, database, Docker launch or deployment was exercised. Browser/full/race suites were not rerun for this documentation-only change.

| Area | Initial audit coverage | Initial audit verification limit |
| --- | --- | --- |
| Core/contracts | API routes, auth providers/identity, credentials/managed adapters, plugins, MCP connections/OAuth, projects/discovery, settings, shared identity/network/manifest contracts | Source and existing tests inspected; no live credential/auth flows or full DB suites rerun |
| Runner control plane | Route and MCP inventories, launch/lifecycle/messages, authorization, scheduling, gateway/grants/proposals, repository/config/artifact/history/usage/metrics paths, DB/protocol contracts | Source and selected existing no-DB tests; no live launch/deployment or full DB suite |
| Daemon/native runtime | Driver/engine registry, native events and direct loop, host/sandbox/pod bootstrap, customization, questions/children/tasks, capabilities/models/usage, persistence/adoption | Source plus isolated synthetic tests where stated; no real credentials, provider calls, daemon restart or Docker launch |
| Runner UI/shared Chat | Component/library inventory, launch/catalog/cron, session controls/Live/History/Insights, terminal, files/review/markup, transports/proxies | Source and test inventory; browser suite not rerun |
| Board | API/MCP/schema inventory, automation/identity, CRUD/graph/search/docs, run/standing/review/merge, chat, metrics/gate, settings and shared proxy | Source and existing tests inspected; no DB/merge/deploy workflows executed |
| Installation/distribution | Desktop/Kubernetes entry points, images/engine pins, preflight/services, release/backup/telemetry/security docs | Source/config inventory; no install, rollout, registry push or release command |
| Inkhold | Writer config/API/launch/persistence/model selection and embedding | Source only; separate consumer changes required |

The inventory covers identified feature families throughout the repository; it does not assert every branch was tested or that no other bugs exist. Do not reuse a previous release's green suite as evidence for a newly found path. Keep precise reproductions, baseline commits and test limitations with each implementation change.

Implementation checks include isolated-database Go race suites, focused native fake-CLI regressions, frontend unit/browser tests and production builds, independent adversarial source review, dependency audit, lint, scrub and documentation links. Exact final results and commit IDs accompany the branch handoff. No production credentials, live provider session, installation, rollout or release command was needed for these checks. Future engine changes must repeat confinement and native-resume verification against their pinned runtime. Integration timing remains separate from implementation.


## Initial implementation verification

The final verification incorporates upstream `ae287fe` through isolated-branch merge `1085acd`, followed by the reviewed Vault-test synchronization (`3c0b575`), disconnect-grace/provider validation (`1201486`), Board effort-display (`31326dc`) and unused Board launch-wrapper (`b557466`) fixes. Tests used Go 1.26.9, dedicated test databases, synthetic native executables and sanitized environments. No production credential or live provider was used.

| Area | Result |
| --- | --- |
| Runner Go | All 24 non-server packages passed with the race detector. Two disjoint server shards covered all 901 enumerated tests, with no missing or skipped tests. The sole failure exposed a grace-period test override racing with a finishing WebSocket; the atomic fix then passed the affected reconnect/loss tests five times under the race detector. Provider-payload regression cases passed separately. |
| Core, contracts and Board Go | All packages passed race testing across the initial runs and affected-package reruns. The initial full Core run exposed a test-only Vault token assertion race; its independently reviewed fix passed 20 focused repetitions and the full affected package. |
| Browser/shared Chat | Core: 177 tests; Board: 119; shared Chat: 839; runner unit: 1,251 passed and five existing skips; runner browser: 57 passed. Type checking, lint and production builds passed. Builds retain existing bundle-size warnings. |
| Inkhold consumer | Integration commit `5597e0c` has 206 frontend tests, lint and build coverage. Security follow-up `ff83d0d` pins the module/build to Go 1.26.9 and updates compatible networking/text dependencies. Its full isolated-database race suite passed all 11 test-bearing packages; five packages have no tests. Source and both production-profile server/CLI binary vulnerability scans reported no findings. Optional individual-test skip counts were not captured by the plain-output run. |
| Static checks | CI-pinned golangci-lint 2.13.1, gofmt, frontend lint, secret scrub and documentation-link checks passed. |
| Installation/CLI fixtures | Desktop Compose, Kubernetes fixtures, hostcheck, 125 CLI tests and 30 Board CLI tests passed. Kubernetes live dry-run was deliberately excluded. No installer, rollout, daemon restart or release command was run. |
| Dependency checks | Frontend npm audits reported no vulnerabilities. Blerg Go scans found no reachable vulnerable symbols or imported affected packages. `golang.org/x/crypto` retains the required-module-only [GO-2026-5932 advisory](https://pkg.go.dev/vuln/GO-2026-5932) for its unused, unmaintained `openpgp` package; these applications do not import that package, and no fixed package version exists. |

Independent adversarial reviews checked authorization and cache partitions, credential delivery, runtime confinement, recovery fencing, model publication ordering, usage accounting, Board authority and consumer session handoff. Public documentation records outcomes rather than private reproduction material. This is a reviewed implementation with fixture and database coverage, not proof of live provider behavior or an absence of future defects.

Remaining limits: native engines still require explicit execution, credential and history adapters; non-device login needs its own protocol implementation. Exact model attribution remains unknown where native usage counters overlap an unsampled model boundary. Child configured effort stays unknown when the engine does not report it. Tool-definition changes can require a fresh native session; the actual adapter advertises that policy. Coding CLI execution and direct inference retain separate capability contracts. These limits do not justify advertising unsupported capabilities.


## Reporting and capacity follow-up for 0.3

The follow-up addresses application improvements previously grouped too broadly under native-protocol limits:

- Shared child events, persisted recovery metadata, Live, chat cards and Insights carry the child's reported configured effort. Omitted partial metadata preserves an earlier observation; an explicit reset clears it. A model boundary without a new effort report becomes unknown. Background launch acknowledgments cannot override a later live model/effort pair. Claude's currently inspected child events do not supply confirmed effort, so the UI does not copy it from the parent or claim per-turn compute telemetry.
- Root usage supports complete per-model partitions. Claude's root message identities distinguish reported models and deduplicate snapshots, excluding child messages. Codex's model-free root and child counters preserve a sampled known-model prefix; ambiguous usage after a switch stays unknown, and an initial unknown prefix is not retroactively renamed. Explicit stale turn identities are ignored, including after a child resumes. Partitions replace aggregate attribution, and one mixed-model native turn remains one logical turn. Stored maps are bounded and validated; malformed or inconsistent maps fall back to aggregate usage under an unknown model.
- Capabilities report the actual adapter's tool-schema refresh policy. Tool-change approval updates authorization without asserting that a native engine has loaded the new definition. The UI explains idle/next-turn refresh, fresh-session requirements and unreported behavior, while retaining the existing conversation and permission checks.
- Claude's descriptor permits the spend-window IDs reported by the separate 0.2.1 budget implementation. Regression coverage preserves fractional percentages and unknown resets, leaves Codex's declared subscription windows unchanged, and proves that another adapter can opt into spend windows. No monetary parser or meter UI is duplicated here. Account capacity remains separate from estimated session cost and local execution budgets; no monetary balance is inferred from token counts. The owner subsequently confirmed 0.2.1 had landed; merge `0923df5` integrates that work into the isolated 0.3 branch. See the integration verification record below.


Follow-up commits: `8e6d5f1` (spend-window compatibility), `c6818d0` (tool-schema guidance), `39f784c` (shared usage arithmetic), `225780c` (configured child effort), `a509c0a` (resumed wire fixture) and `f9ea988` (root/child attribution and validation).

| Follow-up validation | Result |
| --- | --- |
| Native Go | Fresh full daemon race suite passed in 75.157s. Stronger test-only first-known-model coverage passed separately. Full agent, Live-state and protocol race tests passed. |
| Persistence and server | Full DB race suite passed in 129.681s. After preserving omitted-zero bucket compatibility, the affected Insights DB suite passed in 12.084s and 22 selected server Insights, tool-change, child-effort and capacity tests passed under race detection in 16.804s, with no skips. |
| Frontends | Shared Chat: 843 tests; runner unit: 1,258 passed and five existing skips; browser: 57; Board: 119. Targeted chronological/partial/reset regressions passed. Shared Chat, runner and Board type checks, lint and production builds passed; existing bundle warnings remain. |
| Review and security | Independent source and adversarial reviews covered metadata chronology, native ownership/turn fences, recovery, usage bounds and SQL fallback, authorization before capability lookup, and mobile-visible settings. Findings received regressions and fixes. CI-pinned Go lint passed for both touched modules; the Go vulnerability scan again reported no affected code or imported packages, with the previously documented unused-module advisory unchanged. Secret scrub and documentation links passed. |

No live provider, daemon restart, deployment, release, main-branch mutation or monetary-budget implementation was needed for this follow-up. The follow-up was verified before the 0.2.1 integration recorded below.

The [0.3 integration checklist](engine-0.3-integration.md) records conflict-resolution boundaries and acceptance scenarios for the confirmed 0.2.1 integration.


## Confirmed 0.2.1 integration verification

The owner confirmed 0.2.1 had landed. Merge `0923df5` brings upstream `eb72ba2` (release
`818e7c1` plus the subsequent PDF observer test fix) into the isolated 0.3 branch. The released
0.2.1 history, version file and shared Chat package version are retained. Main,
its checkout/index and the running services were not modified.

Conflict resolutions preserve the combined launch/control contracts, owner/runtime authority,
durable inputs, scoped catalogs, artifact navigation, current file limits and capacity reports.
Claude's monthly/daily/weekly spend windows now pass the engine-descriptor sanitizer and render
beside Codex's independently reported subscription windows. Capacity reports still contain only
utilization and known reset times, with no financial amounts or invented billing resets.

A display regression found during integration was fixed in `bed0690`: a cap without a reset
is labeled as a usage limit with an unknown reset, rather than automatically called a monetary
spend limit. Explicit monthly/daily/weekly spend-window labels remain unchanged. Two additional
cross-provider cases reproduced the issue before the fix and passed afterward.

| Merged-source check | Result |
| --- | --- |
| Runner server | All 923 enumerated top-level tests/examples/fuzz seeds passed under race detection in two disjoint isolated-database shards; no missing, unexpected or skipped tests. |
| Runner non-server | All 24 packages matched the package manifest under race detection: 20 passed and four had no tests. Daemon passed in 81.936s, DB in 138.912s, gateway in 120.991s and MCP in 14.625s. |
| Board and contracts | Board vet and full race suite: 596 tests across 15 test packages, no skips. Contracts vet and full race suite: 86 tests across eight packages, no skips. Board runner/proxy and shared Go proxy race tests also passed. |
| Frontends | Shared Chat: 850 tests; Board: 121; runner full unit suite: 1,266 passed and five existing skips; browser: 57. The subsequent neutral-cap patch passed all 16 focused meter tests and frontend lint/type/build checks. Shared Chat, Board and runner production builds passed; existing bundle warnings remain. |
| Installation and CLI fixtures | Messaging CLI: 141 tests; Board CLI: 30. Compose configuration, Kubernetes render/secret fixtures, hostcheck, daemon-install stubs, lint-pin consistency, scrub and link regression fixtures passed. Live Kubernetes dry-run was deliberately excluded. No actual install or restart was performed. |
| Static/dependency checks | CI-pinned Go lint passed for all four modules; runner, Board and contracts vet passed. Workspace and Core frontend npm audits reported zero vulnerabilities. Runner Go scanning reported no affected code or imported packages, retaining only the previously documented unused-module advisory. |
| Independent review | Conflict resolutions and automatic merges reviewed for native confinement, current-launch credential binding, scoped authority, transport metadata, capacity privacy, truthful capabilities and accounting. Board metadata regression verifies explicit false steering, empty effort and incoming file limits survive composition. |

The [integration checklist](engine-0.3-integration.md) now records the confirmed source merge.
Automated fixtures verify these boundaries; representative live provider workflows and deployment
remain separate acceptance steps for an integrated build.
