# MCP sign-in for sandbox and cluster sessions

Status: revised design for BLG-145, replacing the rejected design at `317738e2`.
Design only; no feature is enabled by this document. Cross-model security review
and the implementation gates below are required before shipping.

## Boundary and review disposition

The person signs in through Core; the agent uses the Runner MCP gateway. Core is
the OAuth client and the only refresh-token holder. Neither native engine OAuth
nor a callback relay into a pod is part of this design. Main confirmed these
choices, session-scoped encrypted storage, retention across resume, detachment on
completion, and no worker inheritance in the design discussion.

| Review item | Revised requirement |
| --- | --- |
| P1: sandbox can read refresh token | Core vault only; gateway receives access tokens in memory; runtime receives only a scoped gateway bearer. |
| P1: callback not browser-bound | Core SameSite=Lax state-hash cookie plus the initiating live human login, checked before exchange. |
| P1: agent supplies URL and connected status | Core constructs and validates authorization requests; issuer and endpoints pinned; UI displays provider host; Core and gateway establish status. |
| P2: RFC 9207 iss | Exact issuer checks, including advertised support and missing, duplicate or unexpected values; see callback contract. |
| P2: scopes | Explicit reviewed scope set, bounded parsing, no discovery-driven silent expansion; scopes and tool grants are independent. |
| P2: sign-out after completion | Terminal completion disables gateway access and deletes session credential; provider revocation is attempted separately. |
| P2: workers | No inherited sign-in; worker sees no such server; future explicit delegation requires its own bounded grant. |
| P2: resume | Encrypted Core credential survives suspension; fresh gateway bearer and live human proof required on resume. |
| P2: engine URL logs | Engines never receive provider URLs, codes or OAuth commands; secret-canary log tests cover the entire path. |

## Existing system and required extensions

Read alongside [architecture](../architecture.md) and
[MCP connections](../mcp-connections.md). This proposal reuses these concrete paths:

- `core/internal/mcpconn/oauth.go`: `StartOAuth` discovers and registers a client,
  generates 256-bit state and S256 PKCE, stores state with login sid, issuer,
  resource and redirect URI, and builds the authorization URL. `CompleteOAuth`
  consumes state with DELETE RETURNING and checks expiry, issuer, redirect URI and
  live login before exchanging and sealing tokens.
- `core/internal/api/mcp_oauth_handlers.go`: a per-attempt state-hash cookie binds
  the callback to its initiating browser. The callback is already
  `/auth/mcp/callback` on Core, not Runner. Core's Strict login refresh cookie is
  deliberately unnecessary on the provider's cross-site top-level GET.
- `core/internal/mcpconn/service.go`: encrypted `mcp_connections`, account ownership,
  and audit-before-secret-fetch. The caller must validate liveness before
  `FetchSecret`; this method alone does not establish authority.
- `core/internal/mcpconn/oauth_tokens.go`: process-local refresh singleflight,
  encrypted bundle replacement by ciphertext compare-and-set, needs_auth on grant
  rejection, and best-effort upstream revocation. This is not a distributed
  refresh lock; concurrent replicas need the additional fencing described below.
- `core/internal/mcpconn/oauth_discovery.go`: protected-resource and issuer
  discovery, client registration, S256 validation and guarded network requests.
- `runner/internal/mcpgw/core.go`, `grants.go`, `gateway.go`, `upstream.go`:
  internal authenticated Core fetch, hashed per-session/per-connection bearers,
  tool/hash pins and allow/propose modes, token reissue on resume, bounded calls,
  guarded upstream HTTP, and access-token memory cache capped at five minutes or
  token expiry. Current credential requests carry a human sid or agent token id;
  they do not carry the Runner session binding proposed here.

New work must add a distinct session-scoped connection lifecycle, not silently
turn an account-wide connection into a session credential. Proposed Core fields
are `runner_installation_id`, `runner_session_id`, `credential_generation`,
`lifecycle` (active, suspended, closing, deleted), and a monotonically increasing
refresh fence. Account connections keep their existing behavior. Session-scoped
connections are excluded from account connection pickers, cron attachment and
other sessions' token fetches. They count against bounded account quotas.

Runner needs durable attempt-to-session binding, owner-only status and cancellation,
and a reconciliation job for interrupted cross-service changes. Core needs
internal session-bound start/status/disable operations and a token-fetch extension
that validates the trusted Runner request, account, session, connection,
generation and live human proof. Agent-token proof is refused for this new flow.
An internal key authenticates Runner, not arbitrary session-supplied assertions:
Runner derives these fields from its database and current owner authorization.
`validInternalKey` currently validates one static `BLERG_CORE_INTERNAL_KEY`.
That shared key is the trust boundary: `runner_installation_id` is bookkeeping,
not authenticated installation isolation. A holder can impersonate another Runner
installation; this design trusts every holder as control-plane infrastructure.
No per-installation cryptographic identity is claimed. Test
`TestScopedConnectionSharedKeyBoundary` refuses missing/wrong keys and verifies
that changing the installation label alone cannot confer session authority through
a runtime-facing route. Compromise of the shared key defeats this boundary.
Old callers missing any binding cannot fetch a session-scoped connection.

All account-scoped routes must exclude scoped rows from listings or uniformly
refuse them by ID: PATCH, DELETE, `/reconnect`, `/events`, and
`/internal/mcp/connections/list`, as well as ordinary account detail/token routes.
Apply this in Core service queries, not only the UI. In particular an account
reconnect cannot replace a session's bundle under an unrelated OAuth flow.
Dedicated session lifecycle routes perform scoped mutation and cleanup.
`TestAccountRoutesRefuseSessionScopedConnection` exercises every route with the
owning account and a foreign account, verifies uniform refusal, no events leaked,
and byte-for-byte unchanged encrypted bundle and generation after PATCH/reconnect.
Account list tests verify scoped rows never appear.

## Human start and provider consent

Remote sign-in is offered only for a supported gateway runtime in Docker sandbox
or cluster. Host native sign-in remains a separate feature. Unknown engines and
unsupported versions show “Remote MCP sign-in is not supported for this engine.”
Do not lift the existing native sign-in host guard to implement this flow.

The existing settings panel manages owner-approved remote HTTP server definitions;
Capabilities can link to the corresponding settings flow. Workspace files,
plugins, engine inventory, prompts, and agent-provided URLs are untrusted suggestions
and cannot establish a server identity. A human selects a secret-free definition,
whose exact resource URL, definition version and chosen issuer are frozen for the
attempt. A definition edit invalidates pending attempts; reconnect cannot silently
change the issuer, resource or scopes. The approval preview labels provenance as
“From settings” or “Suggested by this session”; neither label implies provider
trust. Never prefill a resource URL, issuer, scopes or other OAuth fields from agent
text. A session suggestion may show an inert notification; the person must enter
and save the definition in settings before approving it. Preserve the suggestion
provenance in the preview even after that explicit save.
`TestSuggestionPreviewRequiresHumanEntry` supplies agent text containing a URL and
scopes, verifies empty editable fields and no discovery request, then verifies the
saved definition's preview shows its session-suggestion origin.

1. An owner-only same-origin POST checks live human login, current ownership,
   supported engine/runtime, session lifecycle and launch generation. Agent tokens,
   on-behalf-of credentials and workers cannot initiate, inspect or cancel it.
2. Runner requests a Core draft bound to that session and login sid over the
   authenticated internal channel. Core discovers using its existing network
   policy. Discovery is not consent: if multiple issuers are offered, the person
   selects one before authorization. Core pins its exact issuer and endpoints.
3. Core returns a secret-free preview: resource host, issuer, actual authorization
   endpoint host, requested scope list, and session identity. Display the provider
   host as plain text, including its ASCII form, before the person continues.
   Different issuer and authorization endpoint hosts are displayed separately.
   No agent-supplied display name is treated as evidence of trust.
4. On Core's origin, a same-origin authenticated confirmation POST validates the
   frozen draft and initiator sid. Core alone calls `StartOAuth`. The browser may
   first navigate from Runner using an opaque, expiring draft reference; this
   reference cannot initiate OAuth, supply redirect parameters, or confer login.
   It is bound to the same account and initiating sid. No cross-domain cookie
   sharing or credential-bearing Runner relay is needed.
5. Core generates state and verifier, sets the cookie, and navigates the browser
   to the server-built URL. Only this browser receives the authorization URL.
   No engine command, daemon event, transcript or runtime RPC contains it.

The authorization endpoint must be a validated absolute HTTPS URL, without userinfo
or fragment. Reject preexisting reserved OAuth query parameters and duplicate
parameters instead of merging an attacker-controlled state or redirect value.
Core constructs exactly one each of `response_type=code`, pinned `client_id`,
exact registered `redirect_uri`, fresh state, S256 challenge/method, resource and
reviewed scopes. Validate the finished URL against the frozen record before issuing
it. No engine-authentication URL is accepted even if it looks structurally valid.
Discovery, registration, token and revocation endpoints use netguard at dial time:
no redirects, environment proxies, private/metadata addresses or unbounded bodies.
Existing operator network exceptions remain explicit operator authority; this
remote browser flow requires public HTTPS and never infers exceptions from input.

Scopes are a validated, deduplicated set with the existing 1024-byte bound and
OAuth scope-token grammar. Discovery hints are suggestions shown for review,
never an automatic request for all advertised scopes. Empty means explicitly no
scope parameter; it must not invoke the current discovery fallback implicitly.
The UI explains provider defaults may still apply when omitted. Freeze requested
scopes and store effective returned scopes; omission in a token response means
requested scopes. Reject a returned superset, malformed scope or refresh expansion,
disable the connection and revoke issued tokens best effort. A reduced set may be
stored and shown, with unavailable tools reported normally. Tool permissions remain
off until the person selects and pins them; OAuth scopes never grant tools.

## Callback contract and authoritative completion

Use only the configured HTTPS Core public origin plus `/auth/mcp/callback`, exactly
as registered and sent in both authorization and token exchange. Never construct
it from Host, forwarded headers, a session URL or a provider parameter. A deployment
without the configured reachable origin and query-redacted ingress does not
advertise remote sign-in. No localhost callback or pod listener is required.

Use the existing per-attempt cookie naming scheme: value is SHA-256(state),
HttpOnly, Secure, SameSite=Lax, host-only (no Domain), restricted to Core's existing
auth path, and expires with the ten-minute Core state TTL. Do not relax the Strict
login cookie. Enforce same-origin mutation checks on draft confirmation to prevent
an attacker from planting their own browser binding. Cap outstanding drafts and
states per owner and globally, retaining existing rate limits. Preview replacement is limited to the same initiating login and exact session/definition; old callbacks lose the final stage guard. Use a per-owner lock. The global count is an approximate pressure guard across concurrent owners; the per-owner cap is exact. Start restarts the ten-minute attempt lifetime, capped by the frozen human deadline.

The callback accepts only a bounded top-level GET with query response mode. Reject
malformed encoding, repeated state/code/error/iss, empty required values, code plus
error, and unsupported response modes. Hash the supplied state and constant-time
compare against the matching cookie before consuming the state. A missing or wrong
cookie produces a generic refusal without consuming another browser's attempt.
Clear the matching cookie after it is validated. Do not echo state or provider text.

Core atomically consumes the unexpired state once. Match exact redirect URI and
pinned issuer; if `iss` is present it must exactly equal the pinned issuer. If
metadata advertised RFC 9207 support, missing `iss` is a refusal. If not advertised,
absence is permitted only with the one pinned issuer, token endpoint, resource,
client, redirect and PKCE binding; a present wrong or empty `iss` still fails.
No rediscovery on callback or fallback to a different issuer is permitted.

Check the initiating human sid is still live and still owns the Runner session,
and its lifecycle and attempt launch generation still match. Recheck after the
network exchange before committing or attaching tokens; cancellation, logout,
completion or replacement during exchange wins. Core encrypts verifier storage
for new session attempts (the existing state table currently stores it as text),
client secrets and token bundles with the existing key backend. Codes exist only
in bounded request memory and token exchange, never in an outbox or event row.

Attempt states are pending → exchanging → completed/failed, or pending →
canceled/expired. Consumption is irreversible across replicas. A timeout or crash
after consumption requires a fresh sign-in, never code redelivery. An engine or
runtime replacement cancels pending attempts. Core restart may preserve pending
rows only while all recorded bindings still validate. A completed exchange with
failed attachment is not “connected”: leave it unusable, reconcile idempotently
against the same generation or delete and revoke it. Never broaden tools to recover.

The browser gets a fixed same-origin result page, then an owner-authorized session
link reconstructed from stored IDs, never a callback return URL. All callback and
authorization responses are no-store, no-referrer, with restrictive CSP and no
external assets. Core status means “Signed in”; “Connected” requires a successful
Runner gateway initialize and tools/list with this credential generation. That
probe must not call a mutating tool. Engine inventory and callback receipt alone
cannot establish success. Tool selection and actual use remain separate steps.

## Gateway and engine configuration

Core returns only the access token to the trusted Runner gateway using the internal
credential-fetch contract. Never return a refresh token, client secret, verifier
or provider authorization URL to Runner runtime APIs or the browser. The access
token is held only in gateway memory, for at most the lesser of five minutes and
provider expiry. This cache limit does not shorten the provider token's actual
lifetime; upstream token theft remains a residual risk for a compromised gateway.

Each runtime receives a random gateway bearer specific to its session, connection
and launch generation, with only its selected tools, hash pins, proposal policy,
budget and existing network route. It receives neither provider access nor refresh
tokens. Treat this bearer as readable by a sandbox shell: security comes from its
bounded gateway authority, not file modes. Keep normal config isolation, private
files outside the repository, and no bearer in argv, environment listings or Job
specs. Do not enable ambient project/plugin MCP definitions.

| Engine | Configuration contract |
| --- | --- |
| Claude | Use the existing generated gateway MCP entry: supported HTTP transport, gateway URL and bearer header. Preserve current interactive/restricted launch rules and tool refresh path. No `mcp_authenticate`. |
| Codex | Unsupported until a versioned adapter proves isolated generated MCP config containing only gateway URL and bearer header, no native OAuth, and live tool refresh/restart behavior. No `mcpServer/oauth/login`. |
| Hermes | Unsupported until its own adapter passes the same config isolation, transport, bearer redaction and tool lifecycle tests. Do not infer support from Claude. |
| Unknown engine/version | Refuse sign-in and launch attachment with the explicit unsupported message. |

The exact config syntax is the responsibility of each existing engine adapter;
capability advertisement follows tests against the pinned binary, not a shared
assumption about engine support. Any restart needed to load config preserves grants
and invalidates old launch bearers. It never copies a native credential directory.

## Persistence, refresh, sign-out and workers

Core keeps the encrypted session credential through suspension, server restart and
runtime replacement. Runtime volumes, engine homes, backups of workspace output,
artifacts and exported transcripts contain no provider tokens. Only Core's existing
encrypted vault backup policy applies; key loss fails closed and requires sign-in.
Pending OAuth attempts do not survive a changed launch generation.

Pause/stop-for-resume suspends the connection and revokes runtime gateway grants;
it is distinct from terminal completion/discard. An automatic recovery after a
daemon restart or runtime restart has no live human action: recover the session
with this server detached and “Needs sign-in.” Do not mint gateway grants, renew
an active binding, or silently reuse even a still-live stored human sid.
`TestAutomaticRecoveryLeavesScopedServerDetached` restarts the daemon with a
persisted credential and live sid, then proves the server is absent from engine
config and gateway access fails until explicit human action.

Reattachment requires an owner-authenticated same-origin POST, including CSRF/origin
checks, for the same session, supported runtime, current definition and retained
tool pins. Use a fresh live human proof; if necessary first sign in to Blerg again.
Rebind the Core session record and use `ReissueGrants` with fresh tokens; never
restore a bearer from retained config or rebind unattended to an agent token.
A retained valid provider credential avoids repeating provider consent; “Needs
sign-in” describes the detached state, not a requirement to discard that credential.
`TestHumanReattachRequiresSameOriginAction` rejects GET, cross-origin POST, agent
proof and stale sid, then allows an owner POST and rejects the previous bearer.

Explicit Blerg logout suspends bindings attached to that login sid and advances
`credential_generation`; it does not erase their bundles. Extend the identity
`RevokeHumanSession` path in `core/internal/identity/human_session.go`, reached by
`handleLogout` in `core/internal/api/auth_handlers.go`, with this update in the
same revocation transaction. `handleLogoutAll` calls `RevokeAccountEverywhere` /
`RevokeAccountEverywhereTx`: extend that shared identity hook to suspend and advance
all account bindings, including account disable through `core/internal/identity/people.go`.
Re-enabling an account never reattaches anything. Other explicit identity revocation
paths use the same scope-appropriate suspension hook. Binding-suspend failure must
never block logout or identity revocation: update in the revocation transaction
when possible, isolating the optional update with a savepoint so its SQL failure
does not poison the transaction. Otherwise log a classified cause, commit identity
revocation regardless, and let the sweeper suspend bindings. Admission still checks
identity liveness, not only cached binding state.
`TestLogoutCommitsWhenBindingSuspensionFails` injects hook failure and verifies
revocation commits, warm-cache admission fails, and the sweeper later advances the
binding generation and suspends it.

Passive login expiry denies admission and moves the binding to suspended, retaining
the encrypted credential. It cannot renew itself through Runner recovery. Suspension
also advances generation when first observed, fencing in-flight work. It does not
invoke terminal erasure. `TestLogoutDisableAndExpirySuspendScopedCredentials`
covers logout, logout-all, account disable and expiry separately: appropriate
bindings suspend, generations advance, warm-cache calls fail, ciphertext remains,
and reenabling or background refresh cannot restore authority. Lease renewal must
check current login liveness using `HumanSessionDeadline`, not only Runner health,
and refuse renewal once that proof is dead. Persist the original login's absolute
deadline on the binding. A sweeper checks active bindings past that deadline and
current login liveness, so an idle session making no calls still suspends and
starts its seven-day clock. Lookup failures deny renewal and log a classified cause;
they never extend the deadline. `TestIdleExpiredLoginStartsSuspensionClock` keeps
Runner healthy with no MCP calls, advances past `HumanSessionDeadline`, and verifies
renewal fails, the sweeper suspends and advances generation, and retention cleanup
runs seven days after first suspension without any client request.

Suspension has a seven-day retention cutoff measured from first suspension in that
suspension episode. Record `suspended_at` and `cleanup_after` durably using Core's
clock; repeated failures, expired leases and background recovery never extend it.
Only an explicit successful human reattachment before cutoff can end that episode.
At cutoff atomically fence reattachment, enter closing, attempt bounded provider
revocation before erasing the bundle, and record the outcome. A failed revocation
still erases after its deadline. Explicit terminal completion, discard, session
sign-out or account deletion erases promptly through closing cleanup; account
deletion, unlike disable, has no seven-day retention. Disable admission immediately,
attempt bounded provider revocation and erase without waiting for the cutoff.
`TestSuspendedCredentialRetentionCutoff` uses a fake clock to verify retention just
before seven days, revocation-before-erasure at cutoff, no deadline extension on
repeated suspension, failure/timeout cleanup, and reattachment racing the cutoff.
Account deletion does not exist today; only disable exists. The deletion behavior
above is a future requirement, not a current route. When deletion is added,
`TestAccountDeletionErasesWithoutRetention` must distinguish disable from deletion
and verify immediate closing cleanup, provider revocation and refusal of late
refresh results; a database cascade alone is insufficient.

Core owns refresh. Extend existing process-local singleflight with a database lease
and monotonically increasing fence per connection across Core replicas. Acquire the
lease in a short transaction, release database resources during HTTP, and commit
only if the lease fence, credential generation and active lifecycle still match.
Use a bounded provider deadline shorter than the lease, no takeover of an uncertain
refresh: a crashed/expired lease or ambiguous rotation marks needs_auth until a new
human sign-in. Compare-and-set alone cannot prevent two replicas spending a rotating
token. Preserve encrypted rotation persistence after caller cancellation. Permanent
grant rejection disables use; transient definite failures may use an unexpired
access token. Never replay an ambiguous refresh or keep retrying a spent token.
Rotation persistence failure fences further refresh and requires reconnect; log the
cause without the provider response. Refresh must revalidate granted scopes.

Session sign-out, terminal completion, discard and account deletion first disable
new gateway dispatch, cancel attempts, and advance the Core generation so late
exchanges/refreshes cannot resurrect authority, then enter closing for erasure.
Blerg logout, account disable, passive login expiry and binding lease expiry instead
suspend as defined above; they do not erase. Unlike ordinary account connection
cache behavior, this flow requires a live binding/generation check before every
gateway upstream dispatch, including approved proposals, even with cached
credentials. Failure to check Core denies dispatch; cached credentials cannot bypass
logout. Dispatch admission is the revocation boundary: already admitted calls may
finish and cannot be rolled back. Deleting grants also blocks old bearers.

Make cleanup durable: Runner records explicit terminal/closing intent before sending
Core disable, and reconciles on restart. Core checks live human proof on every
admission and expires unreconciled active Runner bindings using a short
server-renewed lease (at most one minute). **Lease expiry moves a binding to
suspended, never closing or deleted, and never erases its bundle.** An outage longer
than one minute therefore detaches access without wiping sign-ins. Renewal after
expiry cannot reactivate it without the same-origin human reattachment action.
A partition refuses calls requiring Core checks. Only an explicit terminal/closing
record (including retention-cutoff cleanup) authorizes erasure; inference from a
missing heartbeat does not. `TestBindingLeaseExpirySuspendsWithoutErasure` holds
Runner offline beyond a minute, verifies retained ciphertext and detached server,
then proves automatic recovery cannot restore access while a human can reattach.

For closing records Core attempts bounded provider revocation, erases the encrypted
bundle and marks deleted; a crashed closing record resumes cleanup before any new
issuance. Failed or unsupported provider revocation still erases local material
and logs its cause. Say “Signed out of this session. Provider access may remain;
revoke it with the provider.” Do not claim remote revocation or rollback. Normal
terminal completion detaches and closes automatically, even if the final turn fails.

Workers get no entry for the parent's session-scoped connection and no copied
gateway config, bearer, connection ID fetch authority or native credential. Their
Capabilities panel says “No MCP sign-in shared with this worker.” Guessing a parent
connection ID or supplying the parent's human sid must not work through a child's
session authority. A future explicit human delegation could select a worker and a
subset of parent tool/hash/mode grants, create its own gateway token and Core binding,
and show its separate lifetime. It must never enlarge allow/propose modes, bypass
proposals, copy refresh tokens, or outlive parent revocation; that delegation is
outside this first implementation. A malicious parent that can deliberately send
its bearer or proxy tool output to a child cannot be contained by config isolation;
this is a residual limit of agent-readable bearer authority, not inherited access.

## Failures and observability

Every refusal, cancellation, expiry and cleanup failure records a safe typed cause
at the service that detects it, plus opaque attempt/session/connection IDs, stage,
HTTP status when safe, and retryability. This includes random generation, draft
validation, cookie/issuer/redirect/login checks, database and encryption errors,
discovery, exchange, attachment, gateway config/transport, refresh, revocation,
lease reconciliation and audit-write failures. A sweeper records expiry and recovery
causes. Rate-limited failures retain counters and cause summaries. Failure to write
the credential audit denies secret fetch; do not silently swallow logging failures.
Operational logging failure has a health signal and sanitized fallback sink.

Use fixed public American English text such as “Sign-in expired. Try again.”,
“Sign-in was canceled.”, “The provider could not be reached. Try again.”, and
“Your session needs sign-in again.” Keep the private cause distinct from the generic
cross-account refusal. Never log raw error strings that can contain URLs or provider
bodies: use classified causes, not `%v` on HTTP errors. Existing partial callback
logging and raw network error logging must be hardened in touched paths.
Classifying `tokenRequest` failures on authorization-code exchange and internal
credential/refresh paths is an explicit shipping gate, not deferred hardening.
In particular the existing callback exchange branch must never log raw provider
error bodies via a wrapped error. `TestCallbackExchangeErrorBodyCanary` makes the
synthetic token endpoint return a recognizable secret in each non-2xx/malformed
response and verifies callback logs contain only stage/status/classified cause.
`TestInternalTokenErrorBodyCanary` proves the same for internal fetch/refresh,
including transient and invalid_grant responses. These tests must cover current
account OAuth paths too, since the scoped flow reuses them.

Suppress query strings and request/response bodies at ingress, Core, Runner, tracing
and error collection, including before handler parsing. No authorization URLs,
codes, states, cookies, verifiers, provider tokens or gateway bearers in History,
engine logs, notifications, artifacts or telemetry. Engines only see the gateway
URL; redact bearer headers in adapter diagnostics and native config dumps. If a
pinned engine logs its bearer and cannot suppress it, it is unsupported. Callback
pages remove sensitive query material by redirect to a fixed clean result path;
no third-party content receives a referrer. Browser history and the provider still
see the authorization navigation; it cannot be promised absent from those systems.

## Threat table and implementation acceptance tests

These are required future tests, not results of this documentation change. Use only
a synthetic provider, synthetic engine homes and recognizable secret canaries.

| Actor | Asset | Attack | Mitigation | Test that proves it |
| --- | --- | --- | --- | --- |
| Sandboxed agent | Refresh token and client secret | Read engine home, retained volume, env or config | Core encrypted vault; runtime gets gateway bearer only | Inspect files, pod spec, process args/env and exports after login, rotation and resume; no provider secret canary appears. |
| Attacker in another browser | Victim account binding | Send a stolen state callback or login CSRF | SameSite hash cookie, same-origin start, initiating live sid | Start in browser A; callback in B fails without consuming A's state; A succeeds once; forged start POST fails. |
| Revoked human login | Signed-in session authority | Complete callback after logout or continue via cache | Live sid and lifecycle checks before commit and every dispatch | Logout before exchange, during exchange, and with warm gateway cache; no new attachment or dispatch succeeds. |
| Compromised agent | Consent and connection status | Supply malicious auth URL or report connected | Core-only URL construction; frozen preview; gateway probe | Inject engine URLs/status events and modified definitions; no navigation or connected state; provider host remains server-derived. |
| Malicious provider metadata | Client and code | Replace issuer, endpoint, redirect, state or S256 | Frozen issuer/client/resource/endpoints; exact URI; reserved parameter rejection | Alter each field and inject duplicate reserved queries; start fails before redirect; exchange always uses original URI and verifier. |
| Mix-up attacker | Authorization code | Wrong, missing or duplicate iss | RFC 9207 exact check and pinned single issuer | Wrong/empty/duplicate iss fail; advertised-but-missing fails; nonadvertised absence succeeds only for original binding. |
| Callback replayer | Single-use code | Concurrent callbacks across replicas, replay after crash | Atomic state consumption and irreversible exchange state | Race two callback handlers; provider sees one exchange; replay after restart, expiry or cancellation fails. |
| Untrusted discovery host | Internal network and tokens | SSRF, DNS rebinding, redirect to metadata | Dial-time netguard, bounded HTTPS requests, no redirects/proxies | Fake DNS changes public to private; all discovery/token/revoke redirects and blocked IP forms fail with logged safe causes. |
| Provider or injected scope hint | Consent scope | Silent broad scopes or refresh expansion | Reviewed set; no implicit fallback; validate returned scopes | Empty selection sends no scope; malformed and supersets disable/revoke; reduced response shown; refresh expansion fails. |
| Another account/session/worker | Parent grant | Guess ID, reuse sid, inherit config | Core session binding plus no default inheritance | Child and foreign account fetch parent connection via their own tokens; uniform refusal; child inventory lacks entry; no parent config is mounted. |
| Stale runtime | Resumed authority | Replay saved gateway bearer or late OAuth result | ReissueGrants, generations and disabled old grants | Resume on replacement runtime; old token and late completion fail; new token retains only prior authorized pins. |
| Concurrent Core replicas | Rotating refresh token | Spend same token twice or resurrect deleted bundle | Distributed lease/fence, uncertain outcome requires reconnect | Race two replicas; exactly one refresh; kill holder after send; no retry of old refresh; late response cannot commit after sign-out. |
| Crashing service | Session token lifetime | Lose attachment/completion message | Unusable draft, durable closing, bounded binding lease and reconciliation | Crash between each Core/Runner transition; no orphan becomes callable; lease expiry suspends without erasure; only explicit closing cleanup erases. |
| Cached gateway or queued proposal | Revoked access | Call after disconnect/completion using cache | Admission check each dispatch, grant deletion, generation invalidation | Warm cache and queue proposal, then disconnect/complete; ordinary and approved calls refuse; already admitted call may finish. |
| Failing provider revocation | Accurate sign-out state | Leave provider access active while UI claims revoked | Local disable/delete mandatory; separate best-effort revocation result | Timeout and unsupported revocation erase local credential, block use, log cause and show qualified sign-out wording. |
| Changed MCP tool definition | Tool authority | Swap schema or autoattach new tools after login | Existing hash pins, off by default, proposal enforcement | Reconnect/refresh with changed tools cannot call without applicable pin policy; no selection grants zero tools. |
| Log or artifact reader | OAuth and gateway secrets | Capture URLs, errors, native debug dumps | Classified errors, layer-wide redaction, no native OAuth | Canary code/state/verifier/refresh/access/bearer in success and every failure branch; assert absent from logs, events, traces, config dumps and exports; separately test ingress. |
| Compromised gateway/operator | Access token and allowed data | Read gateway memory or proxy allowed tool results | Short memory cache, scope/tool restrictions, trusted infrastructure boundary | Verify cache expiry and refusal on stale proof; document that privileged memory theft and deliberate output forwarding remain outside sandbox isolation. |

## Increment 3 dispatch boundary

Scoped gateway grants and proposals freeze installation/session IDs, original human
login, launch generation, credential generation and the exact grant token hash.
The Runner adds a universal `sessions.mcp_launch_generation`; zero refuses scoped
access. Increment 4 must maintain it at every lifecycle boundary. The installation
ID remains bookkeeping under the shared internal key, not a separate credential.

Core exposes separate internal scoped token and admission routes. The gateway
never falls back to account token fetching for a scoped grant. Every upstream
HTTP request, including initialize, initialized notification, paging, session
recovery and approved calls, first checks Core admission and then reads the exact
current Runner grant and live runtime immediately before sending. Core checks
login, binding lease and credential generation in an uncached indexed lookup on
every admission. Only a positive Runner authority answer may be cached for one
second, keyed by the complete frozen scope; a cold authority RPC is followed by
another Core lookup. The Runner's final local lookup prevents a runtime change
during that RPC from admitting stale dispatch. An already admitted request may
finish, as described above.

Refresh uses a durable lease and fence plus a conditional ciphertext replacement,
with no database lock held during provider HTTP. A pending refresh blocks dispatch,
even with a warm access token. Gateway admission waits at most two seconds before
failing transiently; it never retries an upstream tool action. An abandoned lease
suspends rather than spending the old refresh token again. Uncertain rotation,
expanded scopes, or a lost fence revoke newly issued tokens when available and
suspend the original generation; closing or newer rows cannot be resurrected.
Successful rotation persists on a bounded detached context if the caller leaves.
Approved calls retain the original binding and independently require the current
approver's live same-account login. The HTTP approval route chooses this branch
only from its frozen stored scope; request input cannot override it. Invalid scope
never falls back to account preflight or token fetching. Account-connection removal
cleanup excludes scoped proposals; their cleanup belongs to the session lifecycle.

This increment does not wire the production Runner authority, activate generation
zero, attach engine configs, or implement lifecycle sweepers. Those remain gates
for increment 4. Its focused proofs include
`TestSessionGatewayPublicFlowChecksInitializePagingAndWarmCalls`,
`TestSessionApprovedProposalKeepsOriginalScopeAndAddsApprover`,
`TestSessionDispatchTwoCoreInstancesSpendRefreshOnce`, and
`TestSessionDispatchRefreshCannotExpandScopesOrResurrectLostFence`.

## Delivery gates and review handoff

Required extensions are mapped below to existing code owners and proving tests.
New migrations belong to the corresponding service; named tests describe required
coverage, not tests that already exist.

| Required extension | Code to change | Proving test |
| --- | --- | --- |
| Reattachment generation fencing (required in increment 4b) | Every successful human reattachment advances Core credential generation before installing a new grant; a lost acknowledgment of an earlier suspension never authorizes closing that newer active generation | `TestReattachAdvancesGenerationAndRefusesOldFenceCloseAfterLostSuspendAck`. |
| Cleanup backlog capacity (required in increment 4b) | The two-row cleanup worker keeps suspension independent but may lag a large revocation backlog; add oldest-due age/backlog visibility and a bounded concurrency/throughput policy before enabling production sign-in, without dropping revocation or bypassing generation fences | `TestCleanupBacklogMakesBoundedProgressWithoutDelayingSuspension`, including slow providers. |
| Atomic retained pins and spent budget (required in increment 4b) | Explicit human reattachment must atomically revalidate and restore the ledger’s trusted pins and carry forward spent call budget; detaching and reattaching must never reset it | `TestHumanReattachRestoresPinsAndSpentBudgetAtomically`, including concurrent consumption and detach/rebind retries. |
| Human reattachment and retention cutoff (required in increment 4b) | Core same-origin human reattachment must atomically require a live owner login, current Runner launch, retained `status=ok` credential and `cleanup_after` still in the future before activating a new generation; `needs_auth` requires a new provider sign-in | `TestHumanReattachRequiresSameOriginAction`, `TestHumanReattachRacingRetentionCutoff`, `TestReattachRefusesUncertainRefresh`. |
| Pending-draft lifecycle fence (required in increment 4b; production authority remains disabled) | Runner draft/lifecycle ledger and Core callback commit guard must cover attempts before a connection/grant exists, cancel or fence terminal scopes, and prevent a callback racing terminal completion from storing or attaching authority | `TestPendingDraftTerminalFenceRejectsLateCallback` must race completion against final callback commit, revoke newly exchanged tokens, and prove no credential or attachment survives. |
| Future account deletion (not implemented today; disable is existing behavior) | Migration `015_mcp_connections.sql` currently gives scoped rows the account foreign key’s `ON DELETE CASCADE`; no deletion route exists. Future identity/account deletion route must invoke durable closing cleanup and bounded provider revocation before erasure; never use a database cascade as credential cleanup | `TestAccountDeletionErasesWithoutRetention` is required when deletion is added; disable still retains suspended bundles. |
| Logout availability and idle-login expiry | `core/internal/identity/human_session.go`, Core binding renewal and sweeper | `TestLogoutCommitsWhenBindingSuspensionFails`, `TestIdleExpiredLoginStartsSuspensionClock`. |
| Account-route exclusion for scoped rows | `core/internal/api/mcp_handlers.go`, `mcp_oauth_handlers.go`, and `core/internal/mcpconn/service.go` queries | `TestAccountRoutesRefuseSessionScopedConnection`: PATCH, DELETE, reconnect, events and internal list cannot expose or mutate scoped rows. |
| Identity-triggered suspension and generation advancement | `core/internal/identity/human_session.go` revoke transaction hooks and `people.go` disable path; Core binding lifecycle storage | `TestLogoutDisableAndExpirySuspendScopedCredentials`, `TestAccountDeletionErasesWithoutRetention`. |
| Automatic recovery without human action | Runner daemon/session recovery and resume paths; gateway config generation | `TestAutomaticRecoveryLeavesScopedServerDetached`, `TestHumanReattachRequiresSameOriginAction`. |
| Closing retry and bounded tombstones | `core/internal/mcpconn/session.go` and Core sweeper: closing rows always have an immediately due `cleanup_after`; retry failed erasure regardless of login state, and retain at most 20 newest deleted tombstones per exact session | `TestSessionClosingFailureHasRetryDeadline`, `TestSessionDeletedTombstonesBounded`; increment 4 must prove sweeper retry after database failure. |
| Lease suspension and bounded suspended retention | Core binding reconciler, migrations and `core/internal/mcpconn/oauth_tokens.go` revocation helper | `TestBindingLeaseExpirySuspendsWithoutErasure`, `TestSuspendedCredentialRetentionCutoff`. |
| Suggested-definition provenance without agent-prefilled fields | Core MCP settings preview, Runner Capabilities suggestion display and server draft validation | `TestSuggestionPreviewRequiresHumanEntry`. |
| Classified exchange and internal token errors | `core/internal/mcpconn/oauth.go`, `oauth_tokens.go`, and `core/internal/api/mcp_oauth_handlers.go`, `mcp_handlers.go` | `TestCallbackExchangeErrorBodyCanary`, `TestInternalTokenErrorBodyCanary` cover existing callback exchange and internal paths. |
| Session-scoped vault records, lifecycle, quota and picker exclusion | Core migrations, `core/internal/mcpconn/service.go`, `oauth.go` | Foreign session/account fetch and cron picker tests; no account connection behavior changes. |
| Session/generation-bound internal APIs under the shared internal key and live dispatch admission | `core/internal/api/mcp_oauth_handlers.go`, `mcp_handlers.go`; `runner/internal/mcpgw/core.go`, `upstream.go`, `proposals.go` | Warm-cache logout and stale-generation tests; old API request cannot fetch scoped secret; approved proposal also refused. |
| Authority wiring and gateway admission race | Future Runner authority must resolve URL identifiers only after checking exact human ownership and human-saved provenance; Core authority HTTP is limited to loopback and Kubernetes Service hosts, otherwise HTTPS. Core/Runner gateway admission must check current launch and credential generations after the callback’s final external authority check | `TestSessionAuthorityRejectsForeignSessionAndAgentOnlyDefinition`, `TestSessionGatewayRejectsChangedGenerationAfterCallback`, plus transport configuration tests. These remain required before production wiring. |
| Owner-authorized draft, immutable preview, binding and reconciliation | `runner/internal/server/pluginbrowse.go` and server MCP routes, Runner migrations; Core OAuth handlers and `oauth.go` | Cross-origin browser start, agent-token refusal, definition edit, lost attachment and late completion tests. |
| Exact authorization URL, callback parser and configured HTTPS origin | `core/internal/mcpconn/oauth.go`, `oauth_discovery.go`, `core/internal/api/mcp_oauth_handlers.go` | Reserved query injection, duplicate callback parameters, wrong redirect, issuer and Host spoofing tests. |
| Browser cookie plus live initiating login on completion | `core/internal/api/mcp_oauth_handlers.go`, `core/internal/mcpconn/oauth.go` | Two independent browser jars; missing cookie does not consume valid state; logout during exchange blocks commit. |
| Explicit requested/effective scopes and no silent expansion | `core/internal/mcpconn/oauth.go`, `oauth_discovery.go`, `oauth_tokens.go` | Empty request has no fallback; scope superset at exchange or refresh disables and attempts revocation. |
| Encrypted attempt verifier and single-use recovery | Core state migration and `core/internal/mcpconn/oauth.go` | Database plaintext canary scan; replay and crash tests across two Core instances. |
| Distributed refresh lease/fence and uncertain rotation handling | Core migrations and `core/internal/mcpconn/oauth_tokens.go` | Two-instance rotating-token race and holder-crash tests; no second spend or late resurrection. |
| Durable closing, binding lease, terminal cleanup and resume | Runner session lifecycle paths and migrations; `runner/internal/mcpgw/grants.go`; Core `service.go`, `oauth_tokens.go` and session binding reconciler | Crash at each boundary; stopped grants fail; lost notice suspends without erasure; automatic recovery stays detached; human reattachment rotates tokens. |
| Engine config isolation and capability registration | `runner/internal/daemon/mcpgateway.go`, engine adapters and cluster config generation | Pinned-engine transport/config tests, debug-log canaries, stale config after resume, unsupported engine refusal. |
| No worker inheritance | Runner child launch/config paths and Core scoped token authorization | Worker inventory lacks parent server; its session identity cannot fetch parent connection; no copied bearer. |
| Settings/Capabilities consent and authoritative status | `core/web` MCP settings, `runner/frontend` Capabilities and server status routes | Provider host/scope preview, unsupported message, failed probe never connected, scoped tools remain off until selected. |
| Every failure cause recorded without secret data | Core OAuth/discovery/token handlers, Runner gateway/adapter diagnostics and deployment ingress configuration | All failure branches produce classified cause; canary scan over logs/events/traces/exports; separate ingress query-redaction test. |

Implementation should be split into Core session binding and OAuth hardening;
Runner lifecycle/admission and gateway attachment; then supported engine adapters
and settings/Capabilities UI. Each stage needs failing-first focused tests for
its rows above. Database-backed concurrency tests must use two Core instances,
not only goroutines inside one service. Browser tests need independent cookie jars,
cross-origin navigation and Core/Runner on separate origins. Exercise suspension,
terminal failure, native process exit, account disablement and delayed callbacks.

Keep existing account-connection behavior and native host sign-in covered by
regression tests. Run touched Go package tests and race tests, affected frontend
lint/type checks/build and focused browser checks, plus scrub and links. The
coordinator runs the integrated gate; no broad suite is implied by a design commit.

Main approved the architectural defaults and round 1 lifecycle/retention decisions
above. No unanswered product choice is required to read this proposal. Exact migration/API schemas and pinned adapter
support are implementation work gated by security review; absence of evidence
means unsupported. This design authorizes neither implementation nor deployment.

## Increment 3 review hardening

Both scoped internal clients clone their standard HTTP transport once, clear its
proxy function for HTTP and HTTPS, and retain direct connection pooling. Neither
`HTTP_PROXY` nor `NO_PROXY` grants a path for the internal key or access bearer.
An explicitly injected nonstandard transport is trusted dependency injection;
production clients use the proxy-free standard transport.

Kubernetes Service suffixes permit plaintext only with the operator deployment
flag `BLERG_MCP_CLUSTER_DEPLOYMENT=true`, default false. The Kubernetes ConfigMap
and both Core and Runner deployments supply it. Loopback and the exact approved
Docker aliases remain separate exceptions. Core authority wiring must pass this
flag when enabled in increment 4b; it remains unwired now.

Confirmed pre-send token failures (request construction, dial/DNS, certificate verification, or network-policy
refusal) release only their own unexpired lease under scope, generation, fence and
ciphertext comparison and return unavailable. A read error containing a nested DNS
cause is not pre-send proof. Explicit `invalid_grant`, `invalid_client` or
`unauthorized_client` rejection suspends as a known terminal rejection. Ambiguous
post-send failure still suspends without resending a rotating grant. Losing or
failing the release comparison never clears a newer claim. Admission refusal with
`ErrConnectionGone` also purges the gateway's cached access credential; transient
admission failure retains the cache but cannot dispatch.

Core checks grant-hash syntax and includes the complete hash/scope in its authority
cache key; it has no local Runner grant table and cannot independently establish a
grant issuance. The Runner authority is responsible for the exact grant-hash lookup,
and the gateway repeats a fresh local grant/runtime check after Core admission.
Possession of the shared internal key remains the agreed privileged trust boundary;
a Runner installation ID is bookkeeping, not another authentication factor.
Increment 4b must prove `TestRunnerAuthorityRejectsForeignGrantHash` before wiring.


## Increment 4a lifecycle delivery

Identity chain revocation, logout-all and account disable suspend active scoped
bindings through a bounded, savepoint-isolated update in the identity transaction.
The hook cannot poison logout: its failure logs a classified cause, identity
revocation still commits, admission immediately denies the dead login, and Core's
sweeper reconciles the missed suspension. Only bindings of the revoked login are
changed for single-device logout. Other live browser logins retain their bindings.

Core renews using one database `clock_timestamp()` value, matching admission
without trusting the application clock. It renews only an already active, unexpired lease with the exact scope,
credential generation and live human chain, under the original absolute login
deadline. Renewal cannot reattach or extend a login. A 15-second Core loop suspends
active rows with a dead login, expired absolute deadline or expired binding lease;
it never erases them for Runner absence. Idle abandoned refresh leases also
suspend as `needs_auth`, requiring a new provider sign-in. It closes due suspended rows after 168
hours and retries due closing records. A separate cleanup worker closes at most
two credentials per pass, so provider latency cannot delay the suspension worker.
Cleanup invokes bounded provider revocation before
erasure even when revocation fails. Once closing commits, final erasure uses a
bounded persistence context independent of caller cancellation. Repeated
suspension does not reset retention.

Runner migration `173_mcp_session_lifecycle.sql` records each scoped grant's exact
immutable proof atomically and retains it independently of grant deletion or
session deletion. The ledger snapshots only trusted name/URL, pins, preset, known
tools, call budget and calls used, including the final committed row on removal;
it never stores a provider credential or raw gateway token. Runtime loss/restart/pause removes scoped gateway grants and
records suspension; a replacement runtime advances the launch fence. The trigger retains terminal facts for all retained conversation bindings, including
older launches, even if recovery
clears the live session row before the poll. Go reconciliation calls the existing
`db.TerminallyEnded` itself to classify those facts; `ended_at` alone never implies
erasure. Recoverable daemon-loss/error and unreported-runtime facts suspend.
Explicit discard/delegation-end/session-deletion records persist closing intent.
An outage or a missing row without a closing record cannot imply erasure.

Runner's bounded reconciliation loop delivers renew/suspend/close over the same
proxy-free internal transport and shared-key boundary. It rechecks current local
grant/runtime liveness before renewal. A disabled gateway does not renew. Work
acks compare both intent revision and acknowledged control generation. A successful
suspend response carries Core's resulting generation; Runner records that exact
fence separately from the original grant proof and uses it for subsequent close.
A lost response or generation conflict logs superseded and never guesses a newer
fence. Superseded records retain terminal facts and can later promote to closing.
After a refused terminal close, Runner may obtain an idempotent suspension
acknowledgment for the exact supplied generation or its immediately following
suspended generation, then close only that acknowledged fence. A newer active
attachment refuses both operations. This recovers lost acknowledgments and
Core-first suspension promptly; Core's retention sweeper remains the fallback.
Unchanged `ended_at` values never rewrite terminal facts or clear delivery.
Changed ends retain a bounded array: newest fact per canonical input class
(known end reason or unknown bucket, and stopped versus other status), at most
28 current classes with a hard 32-fact constraint. The exhaustive Go-vocabulary
contract test proves every class has uniform `db.TerminallyEnded` output. Thus
flapping cannot displace an earlier terminal event; Go alone decides whether
any retained fact closes the binding. A future predicate/input change must update
that contract before changing the retention classes. Non-secret trusted pins and spent counters live as long as the retained
conversation, independently of Core's seven-day secret deadline. Completed and
superseded delivery records are pruned after 168 hours only once that Runner
session is deleted. Suspended records of a living session are never pruned.
Reattachment must never reset budget, including when a new
provider connection ID is issued. Work is bounded to 100 oldest
records per pass; if Core or delivery is unavailable, leases fail closed.

The generic resume fix (`428e8cf8b`) excludes scoped connections from account
reissue and emits no gateway configuration for a scoped-only resume. Existing
account connection behavior is retained. Engine attachment, native configuration
rewriting, saved-definition provenance, explicit same-origin human reattachment,
and the pending-draft terminal fence remain increment 4b delivery gates.
Production Runner OAuth authority remains unwired; 4a does not enable sign-in.

| Implemented boundary | Code | Proving tests |
| --- | --- | --- |
| Identity suspension without blocking revocation | `core/internal/identity/human_session.go`, `mcp_bindings.go` | `TestLogoutCommitsWhenBindingSuspensionFails`, `TestLogoutDisableAndExpirySuspendScopedCredentials`, `TestLogoutSuspendsOnlyInitiatingLoginBindings` |
| Human deadline, lease renewal and retention | `core/internal/mcpconn/session_lifecycle.go` | `TestSessionBindingRenewalNeverResurrects`, `TestSessionRenewalChecksLiveHumanAndKeepsAbsoluteDeadline`, `TestIdleExpiredLoginStartsSuspensionClock`, `TestBindingLeaseExpirySuspendsNeverErases`, `TestSuspendedCredentialRetentionCutoff` |
| Revocation before erasure and closing retry | Core lifecycle sweeper and existing closing primitive | `TestRetentionRevokesBeforeEraseEvenWhenProviderFails`, `TestSessionSweepRetriesClosingAfterEraseFailure`, `TestClosingErasesAfterCallerCancellation` |
| Internal control without attach authority (`applied` acknowledges control, never liveness) | `core/internal/api/mcp_session_dispatch.go`, `runner/internal/mcpgw/session_core.go` | `TestSessionLifecycleInternalTransport`, `TestSessionLifecycleUsesInternalControl`, `TestLifecycleRejectsUnacknowledgedGeneration` |
| Durable runtime detachment and fenced delivery | Runner migration 173, `internal/db/mcpsessionlifecycle.go`, `internal/mcpgw/session_binding_lifecycle.go` | `TestRuntimeLifecycleDetachesBeforeResumeAndClosesOnlyTerminal`, `TestScopedBindingLedgerSurvivesGrantRemoval`, `TestBindingLifecycleDeliveryUsesExactRevision`, `TestSessionBindingDeliveryRenewsSuspendsAndCloses`, `TestSessionBindingDeliveryRetriesOutageAndNeverGuessesGeneration`, `TestDisabledGatewayNeverRenewsBindings`, `TestRuntimeDetachmentRetainsTrustedPinsAndSpentBudget` |

Increment 4a review regression gates: `TestLostSuspendAcknowledgmentCanClosePromptly`,
`TestIdempotentSuspendRefusesNewerAttachment`,
`TestTerminalCloseRecoversMissedSuspensionFence`,
`TestEndedSessionUpdateDoesNotRewriteTerminalFact`,
`TestRenewalUsesDatabaseClock`, `TestSessionCleanupBatchIsBounded`, and
`TestSessionSuspensionDoesNotWaitForProviderCleanup`.

Bounded fact retention is proven by
`TestMCPFactRetentionClassesMatchCanonicalTerminality` and
`TestTerminalFactSurvivesFlappingBeforeReconciliation`.

The 4b callback handshake may temporarily retain an encrypted **suspended**, unusable
bundle between Core storage and Runner activation. A durable reservation and final
Runner lifecycle transaction fence activation; refusal or abandonment schedules
provider-revoking compensation. The settings panel shows “Finishing sign-in…”
until activation is acknowledged. It never presents that transient state as connected.
Core owns versioned human-saved definitions in the existing MCP settings panel;
suggestions open blank human-entry fields. Capabilities must plainly say that
Codex and Hermes sessions do not support this yet. Opaque short-lived settings
draft references replace durable session/definition IDs in navigation.
