# Durable engine credential lifecycle

Status: shipped.

## Ownership and setup

Settings → Managed connections offers a dedicated Codex device login for Cluster
sessions. The person opens the provider's sign-in page and enters the displayed
code. This creates a separate login from the workstation CLI. Blerg never enrolls,
synchronizes or overwrites the workstation's login automatically.

Core's adapter registry owns engine-specific device authorization and token
refresh, including the consumer access mode returned on both cached and refreshed
credentials. Each adapter declares that mode; the broker does not infer it from an
engine name. The initial adapter is `codex/codex_oauth`. Opaque credential imports and
static keys remain supported, explicitly separate modes. Importing `auth.json`
continues to store a snapshot: refreshing that snapshot in a pod does not update
Core or coordinate with a host CLI.

See the [adapter contract](../../core/internal/credentials/adapter.go),
[Codex adapter](../../core/internal/credentials/codex_adapter.go) and
[managed service](../../core/internal/credentials/managed.go).

## Durable refresh and authorization

Core encrypts managed access and refresh material in its credential vault. Every
replacement has a random generation. A durable, bounded refresh claim serializes
provider rotation across replicas, with provider I/O outside database transactions.
A late result cannot overwrite a manual import, deletion, canceled login or newer
generation. An expired or ambiguous rotation claim requires reconnecting; Core
never blindly retries a refresh token that the provider may already have consumed.

Login attempts belong to one account and its initiating browser session chain.
Polling, cancellation and completion require that authority. Before committing a
successful login, Core locks and verifies the live browser chain and enabled
account again. Crashed startup leases and dead-browser attempts can be replaced
without exposing their device code to a different browser.

Every access delivery requires component authentication and exactly one named
live browser session or agent token belonging to the account. Authorization is
checked before and after provider I/O; the final audit insertion also fences the
credential generation and live principal. Audit failure prevents disclosure.
Lifecycle events contain outcomes and identifiers, never credential material or
provider response bodies.

## Runtime delivery

Cluster launches receive separate per-session runtime and messaging credentials.
Neither is the operator's daemon master credential. Their current launch binding
is stored server-side; replacing a pod revokes its predecessor's pair and invalidates
already-open connections. WebSocket identity and allowed session come from that
binding, independently of the pod's hello fields. Live heartbeats extend only the
current, unrevoked pair.

For a managed engine, the private launch binding records account, engine, principal
proof and current runtime token identifier. The pod cannot choose another
account or pass a browser identity to the broker. Stopped sessions and superseded
pods cannot obtain access material. Provider errors, unavailable Core and revoked
principals fail closed; they do not select the operator's credentials instead.

Who can read the access token: only the pod's daemon, with its runtime token. The
messaging token that the agent's own environment carries (so `blerg-runner` works)
is refused. The agent never receives the refresh token. Because an unrestricted
session's agent runs as the pod's user, a determined agent could still recover the
runtime token from the daemon's process environment and obtain the short-lived access
token, but never the refresh token. Treat a pod's access token as readable by its
own session.

The Codex app-server uses `chatgptAuthTokens` external authentication. Blerg supplies
access tokens over its private native protocol and answers
`account/chatgptAuthTokens/refresh` with a bounded, account-checked refresh. Concurrent
requests share a refresh and generation fence. Core spends at most one forced refresh
a minute per account and engine; a forced request inside that window gets the cached
access. A failed refresh refuses only its caller: the account's other sessions keep
the still-valid access until it expires, and the possibly consumed refresh token is
never presented again, so the person reconnects once that access expires. Refresh
tokens stay in Core; managed launches do not receive a copied `auth.json`. Restricted
and granted sessions use fresh private configuration homes and the same access-only
authentication.

Transient catalog probes also use access only. Cached models describe choices;
they do not prove usable authentication. Existing runtimes launched with an opaque
snapshot keep that mode. Connecting managed authentication prepares new or
replacement pods; it does not silently rewrite an old process's environment.

## HTTP contracts

Browser endpoints require a live human session and return no access or refresh token:

| Endpoint | Result |
| --- | --- |
| `GET /api/credentials/managed/capabilities` | Registered engine/kind and trusted verification origin |
| `POST /api/credentials/{engine}/managed/login` | Account-bound attempt, verification URL/code, expiry and poll interval |
| `GET /api/credentials/{engine}/managed/login/{attempt}` | Throttled pending, connected or reconnect-required state |
| `DELETE /api/credentials/{engine}/managed/login/{attempt}` | Cancel that browser's attempt; already completed credentials remain connected |
| `GET /api/credentials` | Safe kind, state, generation and expiry metadata |

`POST /internal/credentials/managed/access` accepts the component key, account,
engine and exactly one live principal proof. It returns only short-lived access,
provider account, generation and expiry. Opaque imports return `409 not_managed`;
contention returns `202 refresh_pending`; unusable rotation requires reconnecting.
The legacy raw fetch route refuses managed credentials.

`POST /api/session/credentials/access` accepts only the current pod's runtime
credential; the session's messaging token gets `403`.
It derives all Core authorization fields from the private launch binding and
rechecks runtime authority after Core responds. Sensitive responses use
`Cache-Control: no-store`; diagnostics are fixed and provider redirects are refused.

## Evidence and limits

Database race tests cover account/browser separation, durable claims, stale
results, import/delete/cancel, revocation during provider I/O, disabled accounts,
crash recovery and audited disclosure. Runtime tests cover stopped and replaced
pods, foreign sessions, malformed responses, redirects, deadlines and operator
fallback refusal.

An installed CLI fixture uses synthetic JWTs and loopback servers to verify native
external login, a 401-triggered refresh, retry with the new access token and absence
of persisted `auth.json` token state, for unrestricted and restricted sessions.
Separate native-history fixtures verify retained conversation identity, fresh
grants, revoked-tool refusal and credential-marker scans for Codex and Claude.

A person must complete the dedicated provider login. Blerg cannot perform that
consent step for them. Provider outages and revoked logins can still require
reconnecting; a cached model catalog never overrides those failures.

## Upgrading

After an upgrade, each person completes Settings → Managed connections → Codex once.
That consent step creates the dedicated Cluster login; the upgrade does not convert
or renew an existing snapshot import automatically.

## Cluster Codex snapshot migration

The runner server prepares imported `auth.json` files as access-only snapshots
before creating any pod-bound Secret or environment reference. This includes
personal imports, caller environment overrides, and the operator Secret. Pods
reference a per-launch snapshot, never the original operator bundle. Model probes
use the same snapshot preparation. Refresh-token fields are omitted and local
refresh time is set to now; pods never rotate a desktop token family or write credentials back to a
workstation. When materializing the private native `auth.json` file, the consumer
adds an empty refresh-token schema slot required by Codex. That empty slot carries
no refresh authority and is absent from delivered Secrets and environment values.
For an existing snapshot session, use this migration:

1. Preserve any work using the session's normal repository sync/recovery controls.
2. For a person-owned launch, complete Settings → Managed connections → Codex.
3. Start a **new person-owned cluster session**, with the required repositories and
   work selected. The new launch enrolls its runtime credential with the managed
   connection. Stop the old snapshot session after preserving its work.

Retrying a turn, refreshing the model catalog, or restarting Codex inside the old
pod cannot enroll it with the new connection. Settings does not modify an existing
pod's credential source or runtime authorization. A connected catalog therefore
is not proof that the old snapshot session has migrated.

For an operator/runner-key launch, a person's Settings login does not apply: that
principal has no spawning account. Configure an API-key `auth.json` in the
operator credential Secret and launch a replacement session, or have a person
connect Codex and start a new person-owned session. Updating the operator Secret
only affects future launches; it cannot renew an existing pod's snapshot.
Reimporting a desktop login is not a renewal strategy. API-key imports remain
supported.

Pods launched before this server-side delivery change must also be stopped and
replaced after preserving work, even if their access has not yet expired. An
upgrade cannot erase credentials from a running process's initial environment.
No running pod is automatically rebound or restarted by this upgrade.

Core renews a managed connection before issuing access when it is near expiry,
serializes rotation across replicas, and persists each rotated refresh token in
the encrypted vault. Pods use the native `chatgptAuthTokens` mode and request new
access from Core after an authorization error. Model probes obtain fresh access
from the same broker before starting; they receive no refresh authority.

Catalog responses include `reconnect: true` only when a login or snapshot
migration is needed. Network, provider and malformed broker responses remain
catalog errors with Retry. The launch sheet and Session settings link directly
to the managed connection controls.

Codex structured errors retain `additionalDetails` and recognized connection
classes/HTTP statuses, with credential values and URL authority/query data
redacted before the transcript or log sees them. If an unexpected interruption
or engine exit follows retries, the last connection cause becomes the final
error. A requested interruption remains an interruption.

A cluster session without a workspace manifest cannot gain one through the
legacy desktop attachment protocol. Its explorer explains that a new cluster
session with a repository selection is needed. Existing cluster workspace
manifests keep their runtime-token and generation checks.
