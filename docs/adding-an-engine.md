# Adding an engine: guide for agents

Use this guide when adding a coding engine to Blerg or expanding an existing engine to another runtime. Read [AGENTS.md](../AGENTS.md), [Contributing](../CONTRIBUTING.md), [architecture](architecture.md), and [runner rules](../runner/CLAUDE.md) first. Work in an isolated branch/worktree. Installation, authentication, and runtime changes must not restart or replace somebody else's active work without task authorization.

The [feature inventory](features.md) is the acceptance checklist. The [engine review](design/engine-parity-review.md) records current abstraction gaps. The shared descriptor and adapter registries have distinct jobs: registering an `EngineSpec` alone does **not** make an engine available throughout Blerg. Advertise only runtime features backed by an implemented adapter.

## 1. Define support before writing an adapter

Choose a stable engine ID and display name. An engine is the native execution/integration protocol, not a model provider or model ID. One engine may support many providers; model names must not select an engine implicitly. Keep the existing empty-engine-to-Claude compatibility normalization at the API boundary.

Write a support table for the candidate engine:

| Dimension | Questions to answer with evidence |
| --- | --- |
| Invocation | Agent/headless? Terminal/TUI? Native structured protocol, streamed JSON, or plain text? |
| Runtime | Host, Docker sandbox, Kubernetes? Is the executable actually installed in each image? |
| Identity and auth | Host login, API key, imported credential, managed access token? What owns refresh authority? |
| Discovery | Available under which principal and target? Native model catalog? Per-model efforts/defaults? |
| Conversation | Native ID, resume/adopt semantics, required files/SQLite, compaction, interruption? |
| Communication | Mid-turn steering, queued input, blocking questions, notes, attachments? |
| Work visibility | Child agents, roles/models/usage, background tasks, reasoning, tools, errors and retry state? |
| Confinement | Can native shell/web/plugins/hooks/subagents be disabled while retaining granted file/MCP tools? |
| Integration | MCP connections/proposals, board, cron, headless API/MCP, embedded chat? |
| Local customization | Instruction roots, skills, plugins, settings/config bundle, loaded capability enumeration? |
| Accounting | Token/cache semantics, per-model changes/reroutes, pricing, usage limits/reset times? |

The [Hermes gateway adapter](hermes-gateway.md) is an example of negotiating native protocol versions and keeping host recovery identity separate from the Blerg session ID.

Mark unsupported, unknown, and intentionally excluded capabilities explicitly. Do not advertise a capability because the CLI accepts a similarly named flag. Inspect the pinned executable/protocol and official provider documentation; do not infer security or recovery semantics from another engine. Never use real terminal captures or credentials as fixtures.

## 2. Register integration metadata, model rules and catalog sources

Start with a [shared engine descriptor](../contracts/engines/engines.go). Declare its stable ID/name, implemented `Runtimes`, terminal support, restricted-tool support, plugin support and credential-import support. `LimitResume` and `UsageWindows` declare continuation and accepted limit-window semantics. Declare spend windows only when the adapter has an authoritative account source; account capacity is separate from estimated session cost or a local execution budget. Preserve an unknown billing reset rather than inventing a date. These flags describe implemented integrations; they do not prove installation, login, catalog freshness, grant authority or native confinement.

Declare `CredentialEnvKeys` for Cluster delivery and foreign-engine credential filtering. They are private adapter wiring, omitted from descriptor JSON. Register only keys owned by the engine; shared key aliases or multiple credential forms need an explicit adapter policy. The existing Claude adapter deliberately chooses between its OAuth/API-key forms. A future engine with several forms must implement its own choice rather than rely on the single-key helper.

Current model extension points:

- [Model rules](../runner/internal/models/validate.go): engine ID, model grammar, effort allowlist, and normalization. Model IDs and effort values must remain safe argv/config data.
- [Model source registry](../runner/internal/models/source.go), [server source wiring](../runner/internal/server/models.go): model lists with source/freshness, sections, default effort, and per-model efforts.
- Daemon `EngineSpec.ListModels` and [model prober](../runner/internal/daemon/modelprobe.go): bounded, asynchronous host probes. Inspect [existing probe examples](../runner/internal/daemon/modelprobe_engines.go).
- [Cluster catalog provider registry](../runner/internal/server/clustermodelproviders.go) and [cache](../runner/internal/server/clustermodelcache.go): principal/engine/image-scoped native acquisition. Persist safe catalog metadata, never authentication material.

Discovery must use the actual target: a host catalog is not a Cluster catalog, and one account's catalog is not another's. Return snapshots promptly while refresh runs asynchronously. Bound requests, subprocess output, pagination and stale-response handling. Distinguish a missing engine, pending probe, known empty catalog, stale snapshot, and error. A probe failure must not borrow another provider credential or silently choose a different payer.

Use [launch-options discovery](../runner/internal/server/launch_options.go) through `GET /api/runner/launch-options` or MCP `discover_launch_options`. It resolves the verified principal and selected runtime/host, with exact proof for personal credentials and separate operator scope. Reuse each returned `daemon_id` and runtime on launch; an explicitly pinned unavailable host must fail rather than move execution. `restricted=1` filters execution capability. `personal_only=1` separately excludes operator credentials for Cluster callers such as crons that forbid operator fallback. Restricted execution alone does not imply personal-only credentials.

The [readiness cache](../runner/internal/server/launch_readiness.go) keeps transport/RBAC failures unknown, bounds remote work and separates personal-only requests in its proof-scoped key. Cluster model catalogs partition account/engine/image; operator catalogs never borrow a person's entry. Availability is advisory: launch revalidates authority. New probes must preserve these boundaries rather than publishing private catalogs or adding static names to an embedded app.

## 3. Implement execution through the engine boundary

Read [EngineSpec and AgentDriverOpts](../runner/internal/daemon/engines.go), [driver selection](../runner/internal/daemon/agentdriver.go), and the daemon's driver interface. Implement typed invocation, argument rendering, availability checks, driver construction, optional model probes, plugin preparation, capability reporting and declared credential paths.

- Use `EngineSpec.TerminalModelProblem` when an interactive CLI cannot safely implement a model selection supported by its agent adapter. The shared manager checks it before terminal launch and recovery; return an actionable refusal instead of dropping the selection or falling back.

Translate the native protocol into [shared agent events](../runner/internal/agent/events.go) and [protocol messages](../runner/internal/protocol/). Shared chat, persistence, Insights and APIs should consume those events without engine-name switches. Required semantic coverage includes:

1. Start/ready/failure and bounded process shutdown.
2. User input, delivery receipt, mid-turn behavior, cancellation, and turn completion.
3. Text/reasoning/tool calls/results, safe errors/retries, and compaction where supported.
4. Actual model/effort changes, normalized token usage, usage limits, and reset times. For roots and children that change models, emit complete `usage_by_model` partitions that sum to aggregate `usage`; consumers expand the partitions instead of counting them again. Keep ambiguous usage under the empty (unknown) model key rather than guessing the final model. Partitions must not multiply logical turn counts. For live progress, use the shared [`usage_updated` replacement snapshot](telemetry.md#live-child-telemetry), clear it on a new or resumed turn, and keep it out of the completed-turn billing ledger. Preserve unknown usage and test a completion arriving between the snapshot and summary read.
5. Child ownership, start/update/end/resume identity, tool parenting and usage. Carry the child's own configured effort when reported, clear stale settings on authoritative resets/model boundaries, and leave it unknown otherwise. Do not copy the parent's effort.
6. Background process lifecycle and reconnection without false completion.
7. Questions/answers/cancellation and stale/duplicate request handling.
8. Capabilities that reflect what actually loaded, with honest unavailable/unreported state. The actual execution adapter reports `tool_schema_refresh`: `next_turn` only when it implements refresh at idle/next turn, `new_session` when a fresh session is required, or absent when unknown. Approval of a changed tool grants authority; it does not prove that the engine loaded its new definition.

Maintain native truth: a child launch acknowledgment is not its completion; a transient polling error is not proof a process stopped; cumulative counters must not be counted repeatedly. Use separate immutable identities for displayed runs when a native child thread is reused. Ignore foreign child events and verify ancestry before adding ownership. Never replay a user's shell command merely to reconstruct native identity.

Current debt: Claude remains outside the non-Claude `EngineSpec` registry, and some shared paths use concrete native-driver checks. Prefer optional capability interfaces and adapter methods. Preserve the distinction between Claude's direct API loop and CLI driver when unifying registration; don't force every engine into the direct API shape.

## 4. Implement credential delivery and refresh

For host execution, honor the engine's actual home-directory override and login policy in both availability detection and execution. For sandbox/Cluster, explicitly declare which credential paths/env variables are needed and isolate them from other engines.

The shared descriptor drives [Core import validation](../core/internal/api/credential_handlers.go) and [authenticated Core metadata](../core/internal/api/engine_descriptors.go). `GET /api/engines` is human-only integration metadata for Settings; it is not an available-engine endpoint. Credential controls discover registered importers dynamically. Owners can delete legacy entries even after import support disappears; permitting deletion must never widen import authority.

[Cluster credential mapping](../runner/internal/server/k8sjobs.go), [pod bootstrap](../runner/internal/runner/runner.go), and [daemon credential access](../runner/internal/daemon/enginecredentials.go) still need a real runtime consumer. Descriptor keys cover mapping/filtering; they do not install a binary or implement an authentication protocol. Audit these adapters rather than treating an accepted Core kind as complete support.

Managed credentials use the [Core adapter contract](../core/internal/credentials/adapter.go), [managed broker](../core/internal/credentials/managed.go), and [runner broker](../runner/internal/server/enginecredentials.go). Keep refresh authority encrypted in Core; session consumers receive access-only material under the immutable current launch. Preserve generation fencing, live principal checks, replacement/revocation checks, and fail-closed audit writes. Opaque imports remain snapshots unless explicitly migrated with the person's consent.

Register managed access protocols with [engineaccess codecs](../contracts/engineaccess/access.go): bind each mode to its engine, supply encode/decode functions and provider-specific validation. Core, runner broker and daemon share an access-only type. Both consumers decode against the immutable selected engine; a registered foreign mode must still fail. Unknown, expired, oversized or malformed envelopes fail closed; only Core's explicit `not_managed` response permits opaque fallback. A synthetic second codec proves that another protocol can use a neutral `account_id` wire field. The Codex codec intentionally preserves its exact existing `chatgptAuthTokens`/`chatgpt_account_id` wire format.

Current limit: the managed provider interface is still device-flow-shaped. Non-device authentication needs an explicit protocol extension or optional interfaces; do not invent dummy device operations or assume all engines accept Codex's mode. Registering a codec implements the exchange format, not a provider login flow or native consumer.

## 5. Prove runtime and confinement support

Add the binary to the [pinned engine dependencies](../runner/sandbox/engines/package.json) and both applicable image paths: [Docker sandbox](../runner/sandbox/Dockerfile) and [Cluster devcontainer](../runner/Dockerfile.devcontainer). For non-npm engines, use a separately reproducible installation. Check pod bootstrap, credential mounts, availability detection, installer preflight and native version compatibility. A host binary existing does not prove it exists in the image.

Restricted cron/MCP execution is a separate capability from unrestricted headless execution. Reuse the shared gateway and grant policy; implement an engine-owned restricted bridge that exposes only the allowed tools. Prove native shell/web/delegation/plugins/hooks/ambient MCP are unavailable, file access is confined, current grants are enforced, provider tokens cannot be obtained through tools, and cancellation/cleanup is bounded. If this cannot be proven for the pinned engine, reject restricted launches explicitly.

Keep hooks, prompts and customization within the selected engine/runtime boundary. Preparing unused drivers or generic metadata must not execute another engine's code. Read-only catalog/capability discovery must remain read-only. Preserve separate session messaging/runtime authority; never substitute the operator daemon key or revive a stopped launch from stale frames.

[Cron](../runner/internal/server/cronstart.go) and [MCP target checks](../runner/internal/server/mcpstart.go) consume declared restricted support, then validate actual runtime/daemon confinement capability. Do not bypass the second check by setting a descriptor flag. [Runtime bootstrap](../runner/internal/runner/runner.go), pinned images, installers and native authentication still need concrete adapters; provider-specific secret construction and protocol calls remain adapter-owned. Prove these paths separately for each new runtime.

## 6. Add native recovery deliberately

For host sessions, persist native conversation IDs and enough private session metadata to adopt after daemon/process restart. Confirm the same thread resumes; merely restoring chat events is insufficient. Separate agent and terminal recovery, and native resume from any limited transcript replay fallback.

- Version private identities with `EngineSpec.NativeSessionVersion`; set `NativeSessionMinVersion` only for explicitly compatible older records (zero accepts only the current version). Write the new version with the first identity when recovery enforcement changes, so older adapters refuse it rather than ignoring new binding metadata. Persist known intended bindings early, then independently verify the native runtime before admitting input.

For Cluster, register an engine-specific [native history layout](../runner/internal/nativehistory/layout.go), then integrate the engine-owned snapshot/restore identity with [daemon history](../runner/internal/daemon/nativehistory.go) and [server lifecycle](../runner/internal/server/nativehistory.go). Retain conversation files only: no login, refresh token, config bundle, plugin cache or old grants. Preserve owner/engine/claim-UID binding, distributed writer fencing, old-pod termination, fresh authorization and retention/deletion tombstones. Missing expected history must not silently become a new conversation.

Expose actual recovery eligibility and mode through [session recovery](../runner/internal/server/session_recovery.go). A disconnected workstation session is not a resumable Cluster session; absent/reclaimed retained history must not be advertised as native recovery. Public resume calls carry the owner's fresh exact proof instead of reusing a revoked launch token. Durable inputs retain their client ID and source across resume/replay.

Test retained identity across restart, pause/resume, token renewal, limited availability and child recovery. Workspace contents have a separate lifecycle. If native tool definitions remain fixed on resume, enforce removed grants immediately and disclose whether changed/new definitions require a new session.

Delegated child sessions carry `SpawnSession.RequireRecoveryReady` into their private recovery
record. Preserve it through every record rewrite, adoption and runtime wrapper. The engine's actual
turn admission must wait for the current connection's negotiated recovery-ready barrier, including
queued/manual input and question answers; waiting only before process startup is insufficient.
Keep the socket reader free to process the server's kills, open-ask snapshot and readiness frame.
Use the [shared recovery gate](../runner/internal/daemon/recoveryready.go) and its tests rather than
a delay. Reconnect invalidates readiness; a legacy or failed handshake cannot authorize marked
work. The server's durable parent-end boundary and epoch guard remain authoritative even after
the parent resumes. Keep-running children and pauses have different lifecycle rules.

Preserve `parent_call_id` on subagent text throughout streaming and durable events. A subagent's
text, completion or error must not append to or clear the main reply's streaming buffer. Test both
live delivery and replay batches when adding another engine's streaming adapter.

## 7. Wire customization and observed capabilities

Declare `EngineSpec.ConfigBundle` roots, canonical home directory and optional absolute home override; review [config bundles](../runner/internal/daemon/configbundle.go) and [capabilities](../runner/internal/daemon/capabilities.go) for current assumptions. The exporter copies only declared roots and shared `.agents/skills`, with existing archive size/path/link protections. Codex declares instruction files and skills; it deliberately excludes raw `auth.json`, `config.toml`, history and plugin state. Do not expand a bundle to an entire home directory to make one setting work.

Native plugins need [Core plugin registration](../core/internal/plugins/service.go), [shared validation](../contracts/pluginspec/), UI metadata, and an adapter-owned installer/snapshot. Core's descriptor endpoint advertises plugin engines only when a real Core plugin validator is also registered. Settings uses that metadata; frontend suggestions are presentation hints, not a support registry. A newly registered plugin engine gets generic UI without copying a Claude/Codex list. Register only real native support. A marketplace string valid for one engine need not be meaningful for another.

Codex-owned marketplaces use explicit `codex:<marketplace>` identities.
The official `codex:openai-curated` and `codex:openai-curated-remote` forms ship
enabled alongside Anthropic's official source. Admins manage the audited policy
in Core Settings; runner servers distribute it to daemons, which enforce it
locally. Environment lists initialize/fallback rather than intersecting saved
policy. See [marketplace policy precedence](../core/docs/CONFIG.md#plugin-marketplace-policy).
A GitHub source does not implicitly allow a corresponding native identity; the
wildcard permits both valid source kinds. Claude's GitHub grammar is unchanged.
People can save the native identity under Codex in Settings or use the session's
by-name form when catalog browsing is unavailable. Deploy Core, runner/frontend
and daemon together to make native identities available throughout the approval
and startup paths.

The daemon uses `codex plugin add <plugin>@<marketplace> --json` for native
marketplaces. Codex 0.162.1 also exposes app-server `plugin/install` with
`pluginName` and `remoteMarketplaceName`, but that RPC returns authentication
metadata rather than the installed path the snapshot verifier needs. The CLI
uses the same native installer and returns `installedPath`. It runs with no TTY,
closed stdin, capped output, command and overall timeouts, and an isolated
`CODEX_HOME`. Only a private copy of the session's native login is supplied;
installer settings and credential refreshes cannot write through to the login
home. The temporary home is removed after snapshotting or failure. GitHub-only
installs retain their credential-free workshop. Remote-only catalog entries come
from the session's app-server `plugin/list`; arbitrary local marketplace paths
are never converted to native identities. Codex account restrictions remain
visible in the browser and are enforced again by Codex at install time.

Host and local sandbox agent sessions load the verified startup snapshot. Live
application remains host-only. Cluster Codex plugin installation is unsupported
and reports a startup warning; saving an always-on setting does not imply that
an unsupported runtime loaded it. Agent tools still cannot call `plugin/install`.

Live plugin reload is a separate capability from installation at launch. Advertise the engine in
`DaemonHello.session_plugin_engines` only after implementing the owner-approved
[`apply_session_plugins` / `session_plugins_result` protocol](../runner/internal/protocol/sessionplugins.go).
The request carries approved additions and a durable request ID. Recheck name/source grammar and
the daemon's marketplace policy before installing; cache/recover terminal results so reconnect
replay cannot duplicate application. Serialize concurrent additions, install into a fresh private
snapshot, retain the old selection on failure, and persist the selection before reporting applied.
Bound reply writes so a stalled server cannot stop the session loop.

Reload only between turns when foreground and owned background work are idle; fence process
startup as well as normal turns. Resume the same native conversation, preserve its current MCP
grants and runtime restrictions, and refresh capabilities when the new process reports them.
Use [Claude's reload adapter](../runner/internal/daemon/sessionplugins.go) and its tests as the
current implementation, not as evidence that another engine can resume safely. Unsupported
engine/runtime combinations must leave `session_apply` false and clearly report that always-on
settings affect new sessions. Never silently install into a restricted session or claim a running
session loaded a plugin merely because its account setting was saved. Agent requests remain
human approvals for every engine; account-level additions use Core's narrow live-human-proof
endpoint rather than an agent's credentials.

Codex changes currently apply to new sessions in Blerg. Its app-server exposes experimental
plugin-management RPCs, but a reconciliation result is not a runtime-readiness acknowledgment.
Blerg's selected plugin homes are immutable; verified live replacement needs a new snapshot,
a quiescent app-server restart, and recovery of the same thread and its child ownership.
Do not interpret the current limitation as proof that Codex cannot reload plugins. Check the
[app-server protocol](https://developers.openai.com/codex/app-server) and native fixtures before
advertising live support.

Capabilities must distinguish native support, configured support, runtime restrictions, and observed loaded tools. Refresh them when effective settings change, or label the snapshot honestly. Never fabricate tools just to fill the session panel.

If the native engine reports its plan, emit the engine-neutral `plan_updated` snapshot using
the [shared plan contract](../runner/internal/protocol/plan.go). Bind notifications to the
current root conversation, turn and native process; child plans cannot replace the root plan.
Use an explicit empty list to clear, and discard unavailable or malformed snapshots. Bound
native decoding before allocations and preserve restrictions on planning tools. The existing
`/live` plan renderer then works without an engine-specific panel implementation.

For live MCP readiness, implement the optional daemon `mcpStatusSource` and `SetMCPInputGate`
interfaces. Read the running native process without a model prompt; distinguish unavailable
inventory from an authoritative empty list, and expose a cheap process-generation getter.
Bound native status RPCs independently of the session's process lifetime. The shared
[tracker](../runner/internal/daemon/mcptracker.go) owns polling, snapshots and the first-input
deadline; adapters gate delivery before removing an input from their FIFO and preserve it
across cancellation. Restricted sessions expose only their gateway inventory. Follow the
[live MCP contract](../runner/README.md#mcp), including metadata sanitization and honest
unsupported behavior when an engine has no native status source.

## 8. Make every feature consume discovery

Walk [the full feature inventory](features.md), including:

- Runner launch sheet, in-session model/effort switching, capabilities, Live/History and Insights.
- Headless REST and MCP start, discovery, messages, result/callback, stop/resume and idempotency.
- Cron configuration and engine-aware host placement.
- Board automation/roles/Assist, effective launch metadata and effort propagation.
- Shared chat transports/proxies and embedded apps, including their persisted selection and interaction mode.
- Core credential/plugin settings, installers, session images, manifests/OpenAPI and operator docs.

Use one launch-context discovery contract. With one available engine, show its models without an engine selector. With multiple engines, show the engine selector and that engine's own models/efforts. With none, present a configuration/actionable state. During failure or pending discovery, retain an explicit state and retry; don't silently switch engines. Clear invalid model/effort choices when changing engines and guard against late responses from a prior target.

The shared [model control](../runner/internal/server/runner_model.go) serves REST, MCP and browser dispatch; the session row supplies its engine, and success acknowledges next-turn dispatch rather than native confirmation. Embedded proxies must authorize the session before resolving its ID and forward only model/effort, never an engine override. Durable message client IDs must survive proxying and retries without duplicate acceptance or cancellation of later continuation work.

An existing native session cannot change execution engine through a model-setting command. Define engine switching as a new native session, with explicit continuity and ownership behavior. Report the effective model rather than a requested model that was dropped for compatibility.

## 9. Verify with a capability matrix

Write failing synthetic tests before fixing parity defects. Use fake native executables/protocol streams for deterministic semantics, and isolated real pinned-engine fixtures where provider behavior or containment matters. Test every supported runtime/principal combination, including unavailable/one/multiple-engine installations, heterogeneous hosts, custom engine homes, stale discovery, revoked accounts, missing native state, and failure/cancellation races.

Add registry consistency tests: every execution engine has model rules, advertised capabilities have an implementation, restricted engines have confinement proof, retained-history engines have a layout/identity adapter, and supported runtime/image entries agree. A synthetic second engine is useful for proving shared paths contain no accidental Claude/Codex coupling.

Run checks appropriate to the changes from [Contributing](../CONTRIBUTING.md#tests). Never point tests at a live database or use live credentials. Go tests for runner use `GOWORK=off`; database checks need a dedicated disposable database. Engine upgrades must repeat containment and native-resume tests. Run scrub and documentation link checks before completion.

## 10. Handoff criteria

The change is complete when the advertised feature matrix is true, unsupported states are explicit, native/runtime security claims are tested, feature inventory and operator/agent docs agree, and independent review covers both happy paths and failure/authorization boundaries. Record test limitations honestly. Commit on the isolated branch and hand off integration timing to the release owner when another session is cutting a release.

### Session command guidance

Shell-capable sessions should receive the shared Blerg command guide, including
work topics, ticket pins and checklists. Reuse the daemon's shared guide bundle;
add new command guidance once so native prompts and managed terminal instructions
stay aligned. A shellless restricted runtime must not advertise unavailable shell
commands. Preserve the person's existing engine instructions and developer
configuration. Managed terminal instructions belong in the session's private
engine home, with atomic writes that cannot follow a link back to the user profile.
Include recovery and sandbox launches in guide coverage tests.

Host Claude and Codex sessions support owner-approved live plugin changes. Codex stops its
app-server only between turns, after owned background work settles, then starts it with a
fresh plugin home and resumes the same native thread. The old process's native helpers are
terminated with it. The selection is reported as applied only after initialization and resume
succeed; a failed replacement restores the previous plugins and resumes the conversation,
with a plain failure message for the owner. Additions and session-only removals use the same
approval, marketplace allowlist, durable request and replay checks. The open plugin browser
refreshes when a request finishes, including changes deferred until a turn ends. Sandboxed
and older runtimes keep always-on installation for new sessions.
