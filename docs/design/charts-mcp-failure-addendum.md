# MCP polling failure addendum (BLG-514)

This addendum applies the failure matrix in [Charts as first-class content](charts-first-class.md) to slices I–J. It specifies required behavior, not shipped enforcement. Polling stays disabled until implementation and review pass.

## Failure rule

Dispatch only a currently approved binding revision, with a fresh Core session-less capability, current server read-only metadata, effective live-data `allow`, the approved canonical tool hash, authorized visible demand, a durable lease, available quotas, and a committed, unused pre-dispatch audit attempt. The gateway atomically consumes that attempt before the provider call. Recheck durable authority and fencing before dispatch, storage, and delivery. A missing or uncertain prerequisite prevents dispatch. Never fall back to a session token or launch an agent.

Transient provider failures preserve only the last authorized good sample, its tick time, and its last successful fetch time. Show the safe cause and bounded retry time; never synthesize zero. Authority loss, expiry, deletion, or seven-day no-view auto-pause fences work and purges payload rows and derived content. Log every failure using typed, rate-limited causes; never log arguments, provider bodies, credentials, or mapped values. Audit metadata survives payload removal for 180 days.

## Subscriber read freshness

The requested read-load reduction permits one second of shared authority reuse per
binding, exact approved scope and sample sequence, for subscriber reads only.
Owner/workspace membership, known local revocation, local expiry and the Runner's
highest observed epoch remain uncached. A new sample requires a new authority
result; unchanged samples recheck Core every five seconds. Browser purge after a
remote revocation is bounded by that periodic check, at most one second of shared
read reuse and transport time. Known local purges fence queued delivery immediately.
Provider dispatch, secret release and post-call/storage checks always reauthorize
without this cache. Read-path audits record refusals and authority state changes;
unchanged success is not a new security event. Mandatory mint/renew/call audits remain.

## Approval and policy authority

**Core records the approval itself.** A Runner-held bound-ask receipt is not evidence Core can independently trust. Add a `live_binding` subject kind to BLG-496's person-required bound ask, binding the proposal ID, binding ID, revision, owner, and exact configuration digest. Use that ask's authenticated owner answer to enter Core's approval transaction. Core verifies the person's own forwarded `blerg-runner`-audience access token, following `internal_board_automation.go` (signature, audience, kind human, live `sid`, subject equal to owner, enabled account past password change), bound to the exact revision digest; forwarding a `sid`, a Runner receipt, or a service-key assertion alone cannot approve anything. Where that credential is unavailable, leave the revision pending through the existing Core sign-in flow; do not infer approval from chat. Agents, workers, and service identities cannot call the human approval operation successfully.

Core stores the immutable approved configuration, digest, approving login `sid`, approval time, owner security epoch, expiry, and revocation generation in its own durable grant. The digest is domain-separated SHA-256 over a versioned canonical representation of owner, installation/Runner, binding/revision, connection/generation, exact tool name/hash and read-only requirement, arguments, mapping and keys, interval, owner-only visibility, and absolute expiry. Reject duplicate/unknown configuration keys and ambiguous encodings before digesting; freeze the canonical bytes and verify their digest at every boundary. A digest cannot substitute for storing the selected configuration. Core must match the exact pending revision and digest before recording approval. Runner marks the ask committed only after Core's durable, idempotent confirmation of that same revision; lost responses are reconciled by the exact subject, never by an untargeted yes. A denied, canceled, stale, superseded, or expired ask cannot activate a revision.

**The session-less policy source is a Core-owned connection-level live-data policy**, separate from session tool grants and connection pin auto-accept settings. It has per-tool `allow`, `propose`, and `off`, defaults to `off`, and raising it to `allow` is accepted only in Settings with the owner's own verified `blerg-core` token, owner match, and digest binding; the internal policy endpoint refuses all raises; lowering remains lenient and can only reduce authority. These controls live in the existing settings panel. Only `allow` is eligible. Binding approval does not promote `propose` or `off`; both pause polling. Core and the gateway check current policy on every call. Lowering/removing an allow, revoking the connection, or changing its generation revokes affected grants and fences outstanding attempts. Raising policy later does not resurrect a revoked grant: a fresh binding revision needs approval.

Server-provided read-only annotations are untrusted provider claims, not proof that a tool is harmless. Require the claim to be present and true, explicit owner allow, and exact approved definition hash; check read-only metadata separately even if the existing canonical tool hash omits annotations. Initial approval shows the tool definition and this limitation. The binding path ignores `auto_accept` entirely: every definition hash change requires existing MCP pin re-approval **and** a new person-approved binding revision, even for an apparently harmless change. Restoring an old hash does not un-revoke a grant.

Arguments must be schema-validated and nonsecret before proposal and again before approval. Refuse credential-bearing fields, including nested authorization/header/token/password/API-key/cookie/private-key fields, schema-marked secret or password fields, credential-shaped bearer/private-key values, and URLs containing credentials or authentication query parameters. This conservative validation may reject legitimate inputs; it must never silently strip them. Detection is not a proof that arbitrary strings are nonsecret: unknown or ambiguous authentication inputs are ineligible, and authentication must stay in the Core-managed connection. Require a canary test that submits nested and disguised secret arguments and proves rejection before persistence/proposal, with the canary absent from responses, audit records, logs, and published source. Cover approved argument substitution at the gateway separately.

## Credential, service identity, and signing

Core issues a session-less capability through `POST /internal/live-data/credentials` only to a **dedicated live-data Runner service identity** authenticated with mutual TLS. Provision its client private key as a container/pod-only secret, readable only by that Runner service, never in `install/desktop/.env`, an agent mount/environment, or a published artifact. Core pins the service identity to one installation and Runner and restricts it to live-data mint/renew/current-authority and grant-scoped connection-resolution operations; the gateway uses the same protected channel for current grant checks and connection credential resolution. The existing shared `X-Internal-Key` is explicitly insufficient and is not accepted on these operations: desktop agents can read the host `.env` and reach Core on loopback. If the dedicated identity cannot be provisioned, report unavailable and keep polling disabled. Desktop host agents may be in the Docker group, which is root-equivalent and can reach container keys. Live MCP polling defaults OFF on any installation where any agent runtime has container-runtime or host-admin access. Only an explicit owner opt-in in the existing Settings panel can override that posture, with the plain text: “Agents on this machine can reach the polling credentials.” Absence or uncertainty about posture or opt-in refuses dispatch. Cluster installations without such agent access are unaffected; cluster agents with administrator access follow the same refusal rule. Test unsafe desktop refusal at both mint and dispatch, including after posture changes.

Identity is defense in depth, not dispatch authority. Even a minted capability cannot create attempts, approve revisions, replace arguments, or bypass the gateway's single-use committed-attempt check. The scheduler's private database role is an operational convention, not a security boundary against privileged host agents. Keep its credential out of host `.env` and agent access, but enforce the posture refusal/explicit owner opt-in and the verified Core approval regardless of database role. The gateway independently validates attempts, authority, arguments, demand, and quotas; a writable attempt table alone cannot mint authority. Host administrator or container-runtime administrator compromise is outside this isolation boundary; do not claim a private file defeats a hostile host administrator.

Sign using Ed25519 (`alg: EdDSA`) with a dedicated Core live-data signing key and protected header `typ: blerg-live-data+jwt`, plus `kid`. The verifier accepts exactly that algorithm/type and known keys, never token-selected algorithms, embedded keys, or remote key URLs. Store the private key in a Core-only secret mount, separate from ordinary human/agent signing keys and host `.env`; only public verification keys leave Core over the authenticated service channel. The audience identifies **both** the installation UUID and Runner UUID; another installation or Runner rejects it. Claims bind owner/security epoch, binding ID/revision/configuration digest, connection ID/generation, exact tool/hash, operation `poll`, grant/revocation epoch, issuer, `iat`, `nbf`, and `exp`. Lifetime is at most five minutes. No general tool, dataset, session, or connection scope is valid here.

Rotate by provisioning a new `kid`, distributing its public key to the bound Runner, then activating issuance. Ordinary retirement accepts the preceding key only until its last five-minute capability expires, with current authority still required. Unknown keys fail closed; refresh keys through the authenticated channel, never from token data. Compromise retires the affected key immediately with no overlap, increments the live-data authority epoch, fences attempts, purges affected payloads, and pauses grants for new owner approval under the replacement key. Key loading/rotation failure disables mint and dispatch. A token's key ID and authority epoch must be currently accepted, not merely cached as historically valid.

Use Core/database UTC time as the authority clock. Allow at most 30 seconds of future-`iat`/`nbf` skew, enforce `exp` without a post-expiry grace, and reject a lifetime over five minutes. Expired attempts also fail the database clock check. A detected service clock offset over 30 seconds pauses dispatch with `clock_skew`; never extend grants, expiry, or leases to compensate. Restart and restore do not reuse cached credentials.

## Gateway execution and replay boundary

The session-less verifier is **in-process only**, invoked by Runner's durable scheduler. It is not registered on `/mcp-gw/`, the sandbox listener, or any public/agent HTTP or MCP route. Those session routes reject the live-data token type; this verifier rejects human, agent, and session-grant tokens. No live-data capability, connection credential, or service private key enters an agent environment, browser, dashboard source, or socket event.

Each call carries an opaque attempt ID in addition to its capability. The gateway fetches a committed, dispatch-eligible, undispatched audit row for exactly the owner, binding, approved revision/digest, connection/generation, tool/hash, current lease fence, and current authority epoch. It rechecks authorized visible demand, due time, expiry, effective policy, read-only metadata, and quota reservations. It takes **all provider arguments from the durable approved revision**, verifies its digest against Core, and ignores any caller-supplied arguments. A changed request cannot change the approved invocation.

Immediately before provider dispatch, use one database transaction to recheck the shared quotas/semaphores and lease/demand fences and atomically mark that audit attempt consumed. Only one contender can transition `undispatched` to `dispatched`; there is no reset-to-unused operation. Missing, stale, mismatched, expired, already-used, or rolled-back attempts refuse dispatch, even with a valid capability. No transaction commit means no provider call. Reservation and consumption enforce the sliding owner/connection windows and global concurrency across replicas, rather than trusting a caller's claim to have checked quotas.

The gateway performs a fresh durable Core grant/policy/account/connection/security-epoch check on **every call**. It requires a fresh provider tool listing for each call, bypassing `HashCacheTTL` (normally 10 seconds), and rechecks the exact hash and explicit read-only claim. Runner durably remembers the highest Core authority epoch observed for its installation/Runner and rejects every lower epoch, including after restart. Estimate clock offset against Core/database UTC using request send/receive times: compare Core time with the midpoint, reject round trips over 30 seconds as uncertain, and pause when absolute midpoint offset plus half the round-trip exceeds 30 seconds. Bypass the five-minute connection-credential cache for this check; a cached capability or OAuth access token is never authority. Core returns credentials only after that fresh check. No cached positive authority during Core failure. Immediately before dispatch, revalidate the Runner fence; results are accepted only after another current authority/fence check at storage and delivery. Revocation arriving during a provider call cancels/ignores the result and purges derived payload. These are authorization checks at defined boundaries, not a claim that an already-sent remote request can be recalled.

If the process crashes after consuming an attempt, or the network fails with uncertain remote completion, keep it consumed and reconcile an interrupted outcome after lease expiry. Retry with a new audited attempt, new quota checks, and current authority; never replay that ID. This guarantees at most one gateway dispatch per attempt, not exactly-once provider execution across retries. A forged capability gains nothing, and a stolen valid capability has neither a reachable agent execution endpoint nor authority to create/reuse a committed attempt.

## Revocation, audit, and retention

Deletion of the binding, disabling/deleting its owner, owner security-epoch changes, revocation/deletion of its connection, loss of live-data `allow` or server read-only status, and hash mismatch invalidate the grant immediately. Core refuses mint/renew/current-authority checks and the gateway refuses already-issued capabilities. Five-minute expiry is not a revocation grace period. Ordinary login `sid` expiry after recorded approval does not end this durable session-less grant; **sign-out everywhere and password reset increment the owner's security epoch in `RevokeAccountEverywhereTx` (`human_session.go`), revoke live-data grants in that same transaction, and require fresh owner approval**. “Owner deleted” means disabled in the existing account lifecycle; do not invent a separate account-deletion path. Reconnecting OAuth likewise does not silently restore a revoked revision. Material edits require new approval; an ordinary pending edit may leave a still-authorized old revision running, but cannot extend it.

Runner writes a durable attempt before every scheduled call: attempt ID, opaque owner/binding IDs, revision/digest, canonical hash, due/start/end, lease fence, safe outcome, and row count. Audit failure prevents dispatch. A credential failure completes an attempt with a safe failed outcome and no dispatch. Core also writes a durable audit row for **every mint, renewal, and refusal**, recording opaque service/owner/grant IDs when known, binding revision, `kid`/epoch, operation, time, attempt correlation ID where supplied, and a typed outcome; unknown attacker identifiers are omitted or bounded. No audit availability means no successful mint/renewal. No arguments, provider results, secrets, capabilities, or login tokens are logged. Access and proxy logs must omit/redact `Authorization`, all credential-bearing headers, and request/response bodies on approval, mint, renewal, and connection-resolution paths. Prove this with a bearer canary through the actual access/proxy logging path, not just application logging.

Live binding calls use Runner's dedicated live-data attempt audit with 180-day metadata retention, **not** fake session IDs in `mcp_call_log`. The existing `mcp_call_log` remains the session-only gateway log with its existing 90-day retention and session insights semantics. Shared safe call instrumentation may be reused, but each live call is attributed to its binding/attempt in the dedicated audit; do not double-count it as a session call or weaken the 180-day requirement. Core's mint/renewal/refusal audit also retains safe metadata for 180 days. Payload purge never deletes either audit early.

Core stores approved immutable revisions and revocation authority; Runner stores due times, shared leases/fences, normalized payload, and attempts. Core backup restore is a fenced recovery operation: before exposing live-data endpoints, rotate the signing key, advance an authority epoch outside the restored snapshot, invalidate all restored live-data grants/attempt authority, and require fresh owner approval. If an independent epoch cannot be established, keep live-data disabled. A restored approved row cannot resurrect a deleted owner/connection/binding or bypass revocation tombstones. Runner restore must apply tombstones and purge invalid payload before service resumes. No migration below 152 is used for Runner changes.

Limits remain 20 enabled bindings, 60 calls/minute and two concurrent calls per owner; 30 calls/minute and one concurrent call per connection; four installation-wide workers and an 80-binding bounded queue shared across replicas; 30-second call timeout; 15-second interval floor and 60-second default, raised by installation/provider minima. Use fair owner rotation, separate bounded credential/audit work, and no catch-up burst. A capability does not bypass quota or visible-demand checks.

## Failure matrix

| Condition | Required behavior |
|---|---|
| Provider timeout, outage, or disconnect | Cancel at 30 seconds; retain authorized good rows and timestamps; bounded backoff; independent cards continue. |
| Provider 429 or pool/owner/connection quota | Honor provider minimum and retry guidance; reschedule without overlapping calls or queue growth; expose delayed state. |
| Missing/null/invalid/nonfinite value or schema drift | Reject the entire sample, including valid preceding rows; retain authorized good data with schema failure. Never coerce missing data to zero. |
| Oversized response, rows, or mapping | Bound input and evaluation; reject atomically above 10,000 rows or 5 MiB normalized data. No truncation. |
| Tool missing, non-read-only, `propose`, denied, or canonical hash changed | Pause and invalidate authority. Require MCP pin re-approval and a new binding revision; ignore `auto_accept`. Recheck untrusted read-only metadata independently. |
| New/edit proposal denied, canceled, stale, or expired | Never activate it. An ordinary pending edit may preserve the still-valid approved revision; unsafe old authority cannot continue. |
| Credential, grant verifier, audit, or durable database unavailable | No dispatch; explicit paused authority/audit cause. No session credential fallback or cached positive authority. |
| Revoked connection, ownership/permission loss, binding deletion, or 30-day approval expiry | Fence in-flight work; purge payload and derived caches immediately; reject export and queued delivery. Require new approval. |
| No authorized visible viewer | Heartbeat every 30 seconds; lease TTL at most 60 seconds; stop scheduling and cancel/ignore results after final lease. After seven days auto-pause and purge. |
| Restart or duplicate dispatch | Durable shared lease and generation fence; reconcile consumed/interrupted audit without reusing its ID; fresh capability; at most one due call rather than catch-up. |
| Owner isolation or shared dashboard | Only the binding owner receives rows, tooltips, thumbnails, or exports. Other viewers receive private-source state. Dashboard sharing grants no binding access. |
| Sequence gap, reconnect, or reordered delivery | Reauthorize, reconcile a full keyed snapshot through chunk 2's socket path, discard obsolete generations; no document republish. |
| Unsupported live renderer | Refuse setup before any binding/proposal/ask, with a reason naming native stat and gauge without sparklines. Legacy unsupported cards create no demand. Owner dropped authorized static snapshots on 2026-11-05. |
| Socket outage, sample TTL, export failure | Mark disconnected locally; retain only while local authority remains valid. Delete rows 24 hours after last success, swept at least hourly. Export failure remains local. |
| Market closed or halted plus provider failure | Preserve both facts independently. Unknown entitlement/session metadata remains unknown; a stub is labeled simulated. |
| Forged, replayed, or expired capability | Reject bad signature/type/audience/scope/time/epoch; consume no provider call. Replayed attempt cannot dispatch twice, even with an otherwise-valid token. Safe refusal audit; no token in logs. |
| Gateway down | No provider fallback. Keep committed-but-unused attempts bounded; on recovery recheck due/demand/authority/quota/fence and expire obsolete attempts. Uncertain consumed attempts stay consumed. Show delayed gateway-unavailable state with true last-success time. |
| Core down mid-renewal | Fail closed before dispatch, regardless of cached capability/connection token. Audit safe unavailable cause where storage is reachable; reconcile incomplete attempts after recovery. Retain rows only within current local authority/TTL; mark authority unavailable and require reauthorization before serving/exporting them. |
| Owner account deleted | Core revokes grants and denies current checks; Runner fences attempts, cancels/ignores in-flight results, and purges rows/derived content. Retain only safe audit/tombstone metadata under existing account-deletion policy. |
| OAuth needs-sign-in | Pause with connection-needs-sign-in cause; no agent or session fallback, no credentials in source. Fence/revoke authority and purge access to payload; owner reconnects in settings and approves a new binding revision. |
| Signing key rotated or compromised | Ordinary rotation has only the bounded overlap described above. Unknown/retired keys refuse. Compromise immediately removes key trust, advances epoch, fences attempts, purges affected payload, and requires new owner approval. |
| Core database restored from backup | Keep live-data unavailable until independent epoch advance, key rotation, restored-grant invalidation, and tombstone reconciliation complete. Require new approval; never resurrect authority from the snapshot. |
| Clock skew | Check Core/database UTC and bounded skew; refuse expired capabilities without grace and refuse expired attempts. Offset above 30 seconds pauses dispatch; report clock-skew cause without lengthening validity. |
| Sign-out everywhere or password reset | Advance owner security epoch and revoke live-data grants. Reject existing tokens on per-call checks; fence/purge and require owner login plus new approval. Ordinary single-session expiry does not implicitly revoke a durable grant. |

## Required proofs and delivery status

Tests must exercise every matrix row with fake clocks and durable lease/authority seams, then real-process Runner/Core/gateway calls against a visibly simulated market MCP. Remove each authorization guard in turn and prove its test fails for the intended denial. Cover wrong owner/service/installation/Runner/type/key, service-key-only approval forgery, substituted arguments, consumed-attempt races/replay, quota bypass, stale cache after revocation, policy downgrade, hash change under `auto_accept`, disabled/deleted owners, security epochs, restore epochs, OAuth failure, clock bounds, and interrupted audits. Probe public and sandbox listeners to prove the session-less verifier is unreachable there. Canary assertions cover secret arguments, bearer/service credentials, audits, proxy/access logs, responses, source, sockets, and exports. Mapping tests alone cannot establish these boundaries.

At the assigned base `38939439`, chunk 2 supplies `DatasetReference`, `DatasetDelta`, subscription heartbeats, and local dataset resolution. Its reference validator accepts only named Blerg datasets; it does not supply external MCP authority. Core has no live-data credential issuer/durable grants and the gateway has only session-grant verification. Main confirmed BLG-514 includes those missing A2 prerequisites and requires review of this addendum before credential code. A bounded declarative mapper may proceed independently. Remaining work includes Core human approval and dedicated service identity, issuer/key management, in-process gateway verifier, scheduler/audits/quotas/retention, owner-only socket delivery, CLI/MCP/settings, and real-process/real-app evidence. No owner-facing live MCP card is claimed by this documentation increment.


### Foundation implementation checkpoint

The first code increment adds the isolated Ed25519 token contract, Core private-key loading/rotation and issuer service, durable immutable grant/policy and credential-audit tables, account/connection/policy revocation fences, the account epoch update in `RevokeAccountEverywhereTx`, and Runner's in-process verifier and single-use attempt/epoch tables. Per-call checks bypass cached authority; tool drift invalidates the grant, and attempts enforce current demand, lease, interval, quotas, and epoch before dispatch. The bounded mapper is independent of dispatch. Tests use private PostgreSQL fixtures and a separate-process simulated market MCP.

These are service primitives, **not an enabled polling feature**. No live-data HTTP or MCP route, production verifier constructor, human approval/policy-raising endpoint, installation identity provisioning, host-posture detector/settings opt-in, credential-resolution/provider adapter, scheduler loop, retention worker, or card integration is wired by this increment. The trusted posture callback and Core authority/provider interfaces must be implemented from server-owned state and authenticated service transport, never request fields. The human approval endpoint and Settings controls must receive review before any production constructor enables polling. No UI changes or real-app screenshots are claimed here.

## Human approval checkpoint

Core now owns pending canonical revisions and their digests. The Runner's new
`live_binding` bound-ask subject carries that immutable selection; its HTTP and
browser-socket answer paths forward the person's actual access token only in
memory. The existing component gate admits the relay, but cannot approve a
binding or raise a policy by itself. Core independently verifies the Runner
audience, signature, human kind, owner, expiry, live login chain, enabled account,
and forced-password-change state. Approval and policy writes hold the same
account-session fence as logout and password reset. Core records the verified
login chain, digest and ask ID. Exact retries retrieve a receipt without extending
a grant; another ask ID or an outdated revision refuses.

Approval refusal audits require an authenticated caller: a verified component
gate or a fully verified person. Before authentication, refusals go only to the
bounded, rate-limited failure logger; hostile requests must not allocate durable
audit rows. Authenticated refusals retain their durable audit requirement.

The first argument validator accepts a closed, bounded schema subset: typed
objects with `additionalProperties: false`, arrays, primitive types, required
properties, enums, numeric bounds with exact decimal comparisons, and string/array lengths. Numeric
exponents are ineligible in this first subset. Unsupported schemas,
references, secret annotations and malformed constraints refuse eligibility.
The definition must hash to the exact MCP pin. Both proposal and approval repeat
schema and secret checks; the owner sees the definition, arguments, mapping,
interval, expiry and digest in the question. Runner obtains the definition from
its existing pin-definition store rather than trusting agent-supplied prose.
A newer pending revision leaves the current approved revision intact. Approval of
the replacement revokes its predecessor atomically.

Settings → Integrations contains the default-off machine consent and separate
per-tool policy. Raising a policy requires the owner's own verified credential;
internal reductions can use the component gate alone. Disabling machine consent
or lowering policy irreversibly revokes affected grants. Safe cluster posture
does not require machine consent. The runtime assessment and external authority
epoch are trusted process configuration, never request fields; an absent epoch
keeps proposal and approval admission unavailable.

This checkpoint does not enable polling. Production has no configured approval
epoch, issuer transport, scheduler or dispatch adapter. Review the human-approval
endpoint before wiring those dependencies or enabling any calls. The existing
in-process verifier remains unreachable from MCP and sandbox listeners.

## Step B review fixes

The Core grant and every authority snapshot carry the complete canonical approved
configuration. Core and Runner recompute its digest and compare scope, arguments
and expiry before execution. Runner passes the approved interval to atomic
attempt consumption and refuses any locally shortened interval; it applies only
the approved mapping to the provider result. Legacy incomplete grants are
revoked, never upgraded into new approval.

Core registers the Runner-generated ask ID at proposal creation and makes it
immutable. Approval requires that exact ask and a verified person token whose
`iat` is strictly after proposal creation. An older socket token reports “sign in
again.” Allow policies can only be raised through Settings; internal reductions
remain lenient. Core overwrites installation and Runner IDs from the verified
calling Runner. The default resolver requires a TLS-verified dedicated client
certificate with exactly one URI of the form
`spiffe://blerg/installation/<uuid>/runner/<uuid>`; body fields and proxy headers
are not identity. Process-injected resolvers are trusted configuration, not an
HTTP bypass. Provision its certificate outside host `.env`; no such production
transport is enabled by this checkpoint.

Proposal and approval each request a fresh, complete upstream tools listing via
the trusted configured Runner. Its normal gateway handshake and guarded provider
transport perform the listing, bypassing the hash cache. Core independently
compares the returned definition and explicit true read-only claim with the
frozen configuration. Runner also rechecks before forwarding either action.
Missing, changed, unsafe, incomplete or non-read-only listings refuse. The ask
shows the actual Runner, exact definition/hash and untrusted read-only claim;
Settings exposes the hash and definition too.

Core records approval provisionally. It is not dispatch authority until Runner's
local decision commits and its durable decision outbox confirms the exact
subject to Core. Lost replies reconcile through an exact-subject receipt lookup,
never another arbitrary ask. A later decline or cancel revokes the grant,
including an already approved revision. Runner lifecycle/expiry triggers enqueue
cancellations transactionally; reconciliation retries delivery after restart.
Cancellation dominates confirmation and cannot be reversed by replay. The
redundant post-Core login check is removed: Core's serialized person check and
per-call durable authority remain the boundaries.

Machine consent is bound to the independent authority epoch. Enabling production
polling must advance that epoch; existing machine consent then displays off and
requires the owner to acknowledge the plain warning again. Safe cluster posture
continues to need no machine opt-in. The maintenance loops purge audits and
attempts after 180 days, purge normalized payload after 24 hours, and fence/purge
bindings with no view for seven days. Cleanup does not dispatch provider calls.

Polling remains disabled: secure service transport/authority-epoch provisioning,
the scheduler and dataset/card integration still require their implementation and
review. Core migrations for this round are 155–158. Runner migration 155 adds the
decision outbox and retention state; Runner 154 stays reserved for BLG-515.

#### Review round 2 follow-through

The Runner metadata callback rejects every forwarding header and requires an exact
Core certificate URI from a TLS-verified client chain, plus the internal key.
An unset dedicated Core identity refuses callbacks even during the disabled rollout.
An agent live-binding ask short-circuits before tools/list while polling is disabled.
There is still no production activation path for polling.

Browser live-binding question answers use the authenticated HTTP reply path with the current
human token, rather than the socket's connection-time token. A fresh-login refusal
returns 401; the client renews and retries once without closing the question.
The client input ID survives this retry for transcript reconciliation.

Fresh metadata listing and Core decision I/O run before Runner ask/workspace locks;
Core listing runs before the account-session fence and proposal locks. Both sides
re-read the exact revision/digest/hash under lock before committing. Core refuses
preflight listings older than ten seconds. Core approval remains undispatchable
until the Runner commits its local decision and confirms through the durable outbox;
concurrent cancellation/expiry continues to enqueue revocation. No remote I/O runs
while the decision locks are held, including duplicate receipt reconciliation.

Round 3 preserves the existing socket path for all other ask answers, including
ordinary agent questions, bound cut/draft questions and closed-ask chat fallback.
Only the server's persisted live-binding ask marker selects HTTP refresh/retry;
daemon-origin ask events have that marker stripped before persistence/fan-out.
A typed `decision_revoked` refusal on confirm is terminal: Runner marks that outbox
operation handled and logs the cause. Generic conflicts and outages remain
retryable; the operation predicate prevents abandoning a concurrent cancellation.

### Host-posture implementation checkpoint

The trusted posture callback now uses the dedicated Runner certificate identity,
its authenticated runtime inventory and a fresh, complete Kubernetes agent-pod
listing. Host enrollment, desktop container access, privileged pods and uncertain
inventory never attest isolation. Approval performs this callback before taking
account/proposal locks and checks its five-second freshness inside the transaction.
See `docs/live-data-transport.md` for the exact isolation projection and conservative
handling of custom/older root controllers. Mint and dispatch reuse this same
server-owned callback, with no positive cache. Polling is still disabled.

### CA renewal isolation checkpoint (BLG-520)

Cluster service pods no longer mount the CA in any container. A dedicated daily
Job with its own ServiceAccount updates exactly the two existing leaf Secrets;
it has no issuer-secret or workload-control permission. Running services follow
only a valid same-identity, same-root leaf, without stale-key fallback or restarts.
The issuer epoch is unchanged by leaf renewal. Polling remains disabled.

### Activity and pending credential cleanup

Activating a newer approved revision resets its inactivity clock; an exact
receipt replay cannot extend it. The in-process verifier releases the exact
attempt's pending listing exchange on every exit, including cancellation or
revocation before tools/call. Cleanup does no network I/O and remains effective
with a canceled context. Other attempts and scopes cannot be discarded by it.

### Cross-process security proof checkpoint

`scripts/test-live-data-process.sh` builds a Core test fixture, then runs only the
`TestLiveDataCrossProcess` test families in Runner. Core, Runner/gateway and the simulated
market MCP are separate OS processes. Core and Runner use their real dedicated
TLS transport, durable authority, attempt/receipt tables and provider adapter.
Fixture controls use inherited private pipes, never production HTTP routes.
Approved revisions are seeded as operator fixture data; this dispatch proof does
not replace the earlier human-approval endpoint process tests.

The first checkpoint proves a real poll and mapped result, single-use credential
receipts, revocation during an upstream call, owner/other-account/administrator
socket and queued delivery, and restore refusal
until explicit fencing and a new approval. Restore uses a complete PostgreSQL
fixture-database clone while Core is stopped; the external authority state is
never restored. The four critical guards have independent removal proofs.
The enable-blocker follow-up supersedes ordinary restart preservation: every
Core startup now invalidates prior approvals, including an uncheckpointed
pre-revocation snapshot. Reapproval is required after restart.

Both database environment variables are required and must identify the same test
fixture. CI runs this command in the Runner job. The helper version participates
in the Runner test-cache inputs, so changing only Core cannot reuse a stale proof.
All children are joined, their owned fixture databases are dropped, and logs are
checked for the fixture credentials and signed tokens. Production flags remain
false. The remaining failure-matrix cases are still required before enablement.

A fresh complete listing that omits or duplicates the bound tool is authority
loss, not a transport outage. The process proof now checks those cases alongside
read-only loss and canonical hash drift, including with auto_accept configured.
Each revokes the grant; restoring the old listing cannot mint another capability
without a new binding approval. Incomplete or failed listings remain unavailable
without being mistaken for an authoritative empty listing.

Known tool loss also fences the exact approved scope durably in Runner before
requesting Core revocation. One local transaction disables that revision, purges
its sample, ends its lease and demand, and refuses its unused attempt receipts.
It ignores expired leases and caller cancellation; a failed Core revocation
cannot make that approval retryable. Replaying its activation receipt cannot
resume it. A newer owner-approved scope is unaffected and can recover; dispatched
attempts retain their audit state until their actual completion. Both stores'
failures have typed, rate-limited causes, and cancellation cannot downgrade a
terminal revocation to an ordinary retry.

### Provider failure process checkpoint

The process matrix checks outages, listing outages, 429 responses, upstream 401,
missing/null values, excess rows and excess wire bytes after a good sample.
Transient/validation failures keep the good sample's original fetch time; an
authentication refusal fences and purges the exact revision for reconnect and
new approval. Provider retry guidance survives the adapter and verifier and is
applied after jitter, so local backoff cannot shorten it. Retry-After accepts
seconds or HTTP dates, bounded by the maximum 30-day approval horizon. Without
provider guidance, backoff starts at the effective interval and caps at 15
minutes (or the larger effective interval). Live JSON/SSE streams are limited
before parsing; JSON input over 5 MiB is refused without reading an unbounded
body. No provider body or header text becomes a diagnostic.

### Authority and availability process checkpoint

Separate-process proofs now cover disabled owners, sign-out everywhere, connection
revocation, OAuth sign-in loss, policy off/propose, binding deletion and withdrawn
machine consent. They check both pre-dispatch refusal and the purge of an already
queued owner frame. Core outage refuses dispatch and subscriber reads despite an
existing capability; recovery reauthorizes an unused attempt. Gateway outage
cannot release a fallback credential, and restarting Runner preserves the attempt's
single-use state. Core and Runner audit-write failures prevent provider I/O and
recover through a fresh audited attempt.

The capability fixture includes a valid-token positive control and signs each
invalid variant with Core's test key using the exact canonical JWT encoding.
Wrong type/algorithm/key/audience/owner/installation/Runner and expiry are tested
against real transport and dispatch. Clock offset and compromise fencing also
have process proofs; compromise rotates external authority, rejects the old key
and grant, and requires a new approved scope. Independent removals of the type,
signature, clock-offset and mandatory-audit guards must fail behavioral assertions.

### Retention and browser deadline checkpoint

A seven-day no-view sweep now records an explicit local pause and sequence change.
A 24-hour sweep records payload expiry separately from authority loss. Both purge
payload and log bounded causes after commit. Queued delivery checks the exact
scope/workspace, current payload freshness and the deadline recorded when that
frame was collected; a newer sample cannot lend validity to an expired frame.

Subscriber authority decisions carry Core's approved expiry and interval. The
one-second read cache cannot extend that expiry. Each MCP delta carries
`valid_until`, bounded by Core approval and the sample's 24-hour TTL, plus the
approved cadence and next scheduled attempt. Runner-local expiry cannot extend
the browser deadline. Missing or expired deadlines cannot install MCP values.
Socket stores and offscreen cards have independent purge timers; an unavailable
empty response cannot restore an expired card from a previous React sample.

Payload expiry produces a clear “Data expired” state and unknown value, with no
meter value or retained marks. It leaves an otherwise-authorized subscription
alive for a fresh sample. Authority loss still terminates delivery. The real-app
harness checks expiry, recovery and revocation at 390 px, a 1024 px pane, and
3840 px, including the color-blind theme. Production polling remains disabled.

### Crash, quota, and expiry proof checkpoint

The process harness now exercises shared connection, owner, global and pending-attempt quotas, expired binding admission, a killed Runner after dispatch, the actual provider deadline, and persisted signing-key replacement across restarts. Interrupted calls remain consumed; recovery uses a new audited attempt without catch-up calls. Public and sandbox listeners reject live capabilities. Password reset and secret-access audit failure are also exercised through Core.

Expired approvals purge durable samples even while Core is unavailable. Maintenance disables expired bindings, and reconciliation retires unused expired attempts with a typed cause. Runner migration 162 adds idempotent indexes for dispatch reconciliation and quota queries. Eleven independent guard removals were caught by their intended assertions. These proofs do not enable polling or complete the remaining revision-stream and presentation matrix.

### Stream recovery and presentation continuation

Subscription recovery now replaces the opaque subscription identity and requests
a newly authorized full snapshot. Obsolete generations and reordered frames
cannot replace recovered values or restore a purged sample. The real Runner
socket/browser harness covers gaps and reconnects without document republishing.

Native MCP stat/gauge cards distinguish safe unavailable causes and show retry
time without replacing the true fetch time. Optional mapped market-status and
entitlement facts remain separate from provider health; unknown remains unknown.
Unsupported MCP card shapes create no subscription and explicitly say
`unsupported-live · Static snapshot unavailable`. No one-shot static read or
payload-bearing dashboard exporter is added.

[The rule-by-rule proof index](charts-mcp-proof-index.md) lists every failure
matrix row and the applicable tests, inherited evidence, and remaining limits.
Polling remains disabled; this index is not an enablement approval.

### Owner scope decision (2026-11-05)

The owner dropped owner-name private-source display and authorized static
snapshots. These are not enablement prerequisites. Shared viewers continue to
receive no payload. New MCP card setup must declare `card: {kind: "stat"}` or
`card: {kind: "gauge"}` in the immutable binding configuration; `sparkline: true`,
other kinds, or a chart `spec` are ineligible. Core and Runner refuse before
provider metadata I/O, proposal persistence, or ask creation. The fixed reason
names supported native scalar types. Existing approved grants without this setup
metadata retain their prior authority semantics; they cannot bypass the new
proposal gate. Polling remains disabled.
