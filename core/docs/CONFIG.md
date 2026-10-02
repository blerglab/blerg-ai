# Configuration reference

Every environment variable `blerg-core` reads, with defaults and what happens
when a var is left unset. Only `DATABASE_URL` is strictly required; everything
else has a default that boots, and you opt into the rest.

Two of these are load-bearing for human login and are easy to miss:
`BLERG_CORE_ALLOWED_RETURN_ORIGINS` and `BLERG_CORE_ORIGIN_AUDIENCES`. With
them unset, `GET /auth/refresh` 400s on every request and nobody can sign in to
board or runner at all — see [Human login](#human-login) below.

## Server core

| Var | Default | Notes |
|---|---|---|
| `DATABASE_URL` | — (required) | Postgres connection string. The process exits at startup if unset. Migrations run automatically on boot. |
| `BLERG_CORE_LISTEN` | `:8080` | Listen address. |
| `BLERG_CORE_WEB_DIST` | `web/dist` | Directory `core/web`'s built SPA (`/login`, `/app`, `/settings`) is served from, relative to the process's working directory when not absolute. The Docker image sets this to `/app/web/dist`. If the directory doesn't exist, every SPA route 404s — which looks exactly like "login is broken". |
| `BLERG_VERSION` | `dev` | Build version shown on the landing page. |
| `BLERG_CORE_REGISTER_KEY` | unset | Shared bootstrap key components (board, runner) present to `POST /components` to register themselves. Unset disables component self-registration. |
| `BLERG_CORE_INTERNAL_KEY` | unset | Shared secret guarding `POST /internal/credentials/fetch`, the endpoint runner calls to fetch a spawning account's personal engine credential. Deliberately a DIFFERENT secret from `BLERG_CORE_REGISTER_KEY`: registration is a low-trust bootstrap, credential fetch hands back plaintext. Unset disables the endpoint entirely (runner then always falls back to the shared operator Secret). Runner reads the same value as `BLERG_RUNNER_CORE_INTERNAL_KEY`. |
| `BLERG_CORE_PLUGIN_MARKETPLACES` | `anthropics/claude-plugins-official` | Comma-separated GitHub `owner/repo` marketplaces an account may register **always-on plugins** from; `*` allows any *valid* `owner/repo` (never a URL or path). Enforced when a list is saved (`422 marketplace not allowed by this install`); the runner re-checks it at install time with its own `BLERG_RUNNER_PLUGIN_MARKETPLACES` — set both to the same value. Malformed entries are logged and dropped, never widened. |
| `BLERG_CORE_MCP_ALLOW_HTTP_HOSTS` | unset | Comma-separated hostnames for which an MCP connection's URL may use plain `http://` (otherwise `https` only). Core fetches a connection's URL itself, so this is an operator decision: list only hosts you run and trust. Empty by default. See [`docs/mcp-connections.md`](../../docs/mcp-connections.md). |
| `BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS` | unset | Comma-separated hostnames an MCP connection may point at even though they resolve to a loopback, link-local, private (RFC 1918 / ULA) or carrier-grade NAT address. Every other host is checked at connect time, on the address actually dialled, and is refused if it is one of those. Empty by default. The runner has its own list, `BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS`; keep them consistent. |
| `BLERG_CORE_COOKIE_SECURE` | `true` | Secure attribute on every cookie core sets. The desktop compose sets false because Safari does not treat http://localhost as a secure context for cookies. Anything reachable beyond localhost must keep true (and TLS). |

**core must be served over TLS, or from `localhost` with `BLERG_CORE_COOKIE_SECURE=false`.**
The human-session refresh cookie (`blerg_core_refresh`, set by `POST /auth/login`/`GET
/auth/callback`) and every other cookie core sets carry the `Secure` attribute whenever
`BLERG_CORE_COOKIE_SECURE` is `true` (the default). A browser only ever sends a `Secure`
cookie back over HTTPS — WebKit (Safari) does NOT extend an exception to plain
`http://localhost`, unlike Chromium/Firefox, so with the default `true` a plain-HTTP
`localhost` install still silently drops the cookie in Safari and login loops forever at "Sign
in required". `BLERG_CORE_COOKIE_SECURE=false` turns the attribute off; it exists **only** for
that one plain-`http://localhost` desktop case (the desktop compose sets it) — never set it
false for anything reachable beyond localhost, since that would send the session cookie over
plain HTTP to whoever's listening.

### Request hardening

Every JSON/form body core decodes is capped with `http.MaxBytesReader` **before** decoding
starts (not after) — `POST /api/credentials` at 64 KiB, `POST /internal/credentials/fetch` and
`POST /internal/credentials/list` at
16 KiB, `POST /auth/password` at 16 KiB, and the local provider's `POST /auth/login` form body
at 16 KiB. Exceeding the cap is `413 Request Entity Too Large`; there is nothing to configure
here, the caps are fixed constants sized generously above any real payload the corresponding
endpoint ever needs.

A bearer-token verification failure (expired, malformed, wrong audience, revoked, bad
signature, ...) always answers the caller with a bare `401 unauthorized` — never the underlying
`cid.Verify` error text. The real error is logged server-side (`verifyBearer: ...`) for
operators; a caller (or anyone probing tokens) gets no signal about *why* a token failed, only
that it did.

`POST /internal/credentials/fetch` also never distinguishes "the session/token you named is not
live, or not this account's" from "this account has no stored credential for this engine": both
are the same `404 not found`, same body. A caller holding a valid `BLERG_CORE_INTERNAL_KEY`
cannot use response status alone to enumerate which accounts are logged in or which session ids
exist. Malformed `account_id`/`session_id`/`token_id` values (not UUID-shaped) and a missing or
doubled liveness proof are rejected with `400` before any query runs.
`POST /internal/credentials/list` and `POST /internal/plugins/list` apply the identical gates
and the identical `404`. See [Internal endpoints: which live thing authorises the call](#internal-endpoints-which-live-thing-authorises-the-call).

**Revocation cache.** `Deps.RevCache` memoizes the revocation snapshot for **2 seconds**
(`revCacheTTL` in `internal/api/router.go`) so a burst of authenticated requests doesn't re-read
the full revocation set from Postgres on every single one. A revocation (password change,
logout-all, account disable, `Revoke`/`RevokeAccountEverywhere`) therefore takes up to 2 seconds
to bite on core's OWN endpoints (`/agents`, `/api/*`, ...) — it does **not** affect how fast
board/runner learn about a revocation, since they read `GET /revocations` on their own polling
interval, uncached, same as before. `main.go`'s `buildDeps` always wires a `RevCache`; a `Deps`
built without one (e.g. most existing unit tests, or a hand-built `Deps{}` literal) falls back
to querying on every call, unchanged from pre-hardening behavior — this is a memoization only,
never a source of "stale but wrong" truth: a query failure is still treated as fatal (`503`),
never as "nothing is revoked".

**Revocations are timestamped.** Every `revocations` row carries `revoked_at` (migration
011; existing rows backfill to the migration time), and `GET /revocations` emits it as
`revoked_at` (RFC3339) next to `kind`/`value`. Consumers — board and runner's `coreauth`
clients and core's own snapshot checker, all via `contracts/identity.RevocationSet` — apply a
`sub` or `lineage` entry only to tokens whose `iat` is **at or before** `revoked_at`
(equality and a missing `iat` fail closed); `kid` entries are unconditional. So after a
password change, `logout-all`, replay detection or a reconcile disable, every token issued
up to that moment is dead everywhere, while the token a subsequent successful login mints
verifies on board/runner immediately — even though their cached list may still carry the
`sub` entry for up to one 60 s poll. Re-revoking an existing entry moves `revoked_at`
forward. A successful login still deletes the `sub` row (below); the timestamp is what
covers the window between that deletion and the consumers' next poll. An entry without a
timestamp (older core, or `null`) is treated as revoking every token.

## Human login

`GET /auth/refresh` is the only way a frontend obtains an access token: the
browser is sent to core's own origin (so the `SameSite=Strict` refresh cookie
is actually sent), core mints a short-lived access token, and redirects back to
`return_to` with the token in the URL fragment.

Accounts can be reversibly deactivated (`accounts.disabled_at`) — set/cleared
by the github-provider reconcile loop as org membership changes, or by an
admin directly. A disabled account gets `403 Forbidden` at `/auth/login` even
with the right password, and its refresh fails with `ErrAccountDisabled`
(existing session rows are not enough — minting a fresh access token is
refused too). A successful login clears any stale "sub" revocation left over
from a prior disable or a detected refresh-token replay, so re-enabling an
account (or a user simply logging back in once reconcile sees them back in
the org) is not a permanent lockout.

Both vars below have NO safe default, so both ship unset and must be configured
per install. `install/k8s/core-deployment.yaml` and
`install/desktop/docker-compose.yml` both set them for the topology they
describe.

| Var | Default | Notes |
|---|---|---|
| `BLERG_CORE_ALLOWED_RETURN_ORIGINS` | unset (empty allowlist) | Comma-separated `scheme://host` origins `/auth/refresh` may redirect back to. An origin not on the list is a 400 — before the cookie is even read, so this endpoint can never be used as an open redirect. **An empty allowlist rejects everything except core's own origin** (see below) — a user still can't reach board/runner, but core's own login isn't hostage to this var. Example: `http://blerg.local,http://board.blerg.local,http://runner.blerg.local`. |
| `BLERG_CORE_ORIGIN_AUDIENCES` | unset | Comma-separated `origin=audience` pairs deciding which audience the access token is minted for, per `return_to` origin. Unmapped origins fall back to `blerg-core`, which board and runner both reject — the symptom is an endless redirect loop between the component and `/auth/refresh`. Example: `http://board.blerg.local=blerg-board,http://runner.blerg.local=blerg-runner`. Malformed entries are logged and skipped rather than failing boot. |
| `BLERG_CORE_SESSION_TTL` | `720h` (30 days) | `time.ParseDuration` string sizing a session's **absolute** lifetime, set once at login (`human_sessions.expires_at`) and never extended by rotation — a session created at login always dies at `login time + BLERG_CORE_SESSION_TTL`, no matter how many times it's refreshed in between. Each rotated refresh token carries the same `expires_at` forward, and the refresh cookie's `Max-Age` is recomputed on every refresh to track the session's REMAINING lifetime (`expires_at - now`), so it shrinks as the session ages rather than resetting to a fresh full TTL. A malformed value is logged and falls back to the default rather than failing boot. |
| `BLERG_CORE_BEHIND_PROXY` | `false` | Set to `true` when core sits behind a reverse proxy/ingress that sets `X-Forwarded-For`. Controls only session attribution (`human_sessions.ip`/`user_agent`, captured once at login and carried forward unchanged on every rotation — they are never touched again after `IssueRefreshToken`) — **never** enable this unless the proxy is trusted to overwrite that header itself; otherwise any caller can forge their own logged IP. |
| `BLERG_CORE_LOGIN_RATE_LIMIT` | `10/5m` | `<max failures>/<window>` before `POST /auth/login` answers `429` (+`Retry-After`) for that client IP or that subject. In-memory, per process. `0/0` disables. The same limiter instance also guards `POST /auth/password` (change-password), keyed on the caller's own account ID rather than a typed login subject, so the two endpoints' per-subject buckets for the same account are independent — but they share the per-IP bucket — see the accepted trade-off below. |
| `BLERG_CORE_PUBLIC_URL` | unset | Core's own browser-facing origin, e.g. `https://core.blerg.local` (scheme + host, no trailing slash needed — one is trimmed if present). Used as the source of truth for `publicOrigin`, which backs the same-origin/login-CSRF check on every cookie-bearing `/auth/*` POST (`POST /auth/login`, `POST /auth/logout`, `POST /auth/logout-all`) — see [Login CSRF protection](#login-csrf-protection) below. **Optional** while `BLERG_CORE_AUTH_PROVIDER=local` (the default): the CSRF check falls back to deriving the origin from each request's own `Host`/`X-Forwarded-Proto`/TLS state, which is safe for that check specifically (a wrong guess only makes the check MORE strict, never less). **Required** (boot fails fast otherwise) for any non-local provider (`github`, `oidc`), which needs a stable, correctly-known redirect URL rather than one derived per-request. |

**Accepted trade-off: `BLERG_CORE_LOGIN_RATE_LIMIT` has a per-subject bucket, not just a
per-IP one.** That is deliberate — it stops one attacker from cycling through many client IPs to
brute-force a single known account — but it cuts both ways: anyone who can reach `POST
/auth/login` at all can also *lock a known username out* for the configured window, simply by
submitting that many wrong passwords for it themselves (an availability cost, not a
confidentiality/integrity one — no credential is exposed and no session is created). The same
limiter instance now also guards `POST /auth/password` (the change-password endpoint): a
hijacked or otherwise stolen access token used to be able to try the real password against
`old_password` with no limit at all, and a successful guess there is a full account takeover (it
revokes every other session). `POST /auth/password` keys its per-subject bucket on the
authenticated caller's account ID rather than the typed login subject, so — for the same account
— the two endpoints' per-subject buckets don't collide with each other; they DO share the
per-IP bucket, so enough wrong guesses at either endpoint from one IP can still 429 the other one
for the window. Accepted for the same reason the login-side trade-off is: the cost is always a
temporary, self-clearing lockout, never a credential leak.

**Core's own origin is always an accepted `return_to`, regardless of
`BLERG_CORE_ALLOWED_RETURN_ORIGINS`.** Both the local-login page and the OAuth
callback (`handleCallback`) redirect through `/auth/refresh?return_to=<publicOrigin>/app` —
core sending a browser back to itself. `publicOrigin` is derived per-request when
`BLERG_CORE_PUBLIC_URL` is unset, from `Host`/`X-Forwarded-Proto` — headers a caller genuinely
does control, same as any other request header. What makes accepting it here safe isn't that
`Host` is somehow trustworthy; it's *who* would need to be fooled for it to matter. An open
redirect is only a problem if it can send some OTHER party (a victim) somewhere an attacker
chose — but a victim's own browser sets `Host` from the URL it is actually connecting to, so a
malicious page cannot make a victim's browser send core a forged `Host`; it can only ever cause
the victim's browser to send core's real one. A caller forging its OWN request's `Host` (e.g. a
script bypassing a browser) only gets `return_to` accepted for *that same request* — there is no
victim to redirect anywhere else. This reasoning must NOT be reused anywhere the caller making
the request and the party who'd be redirected could be the same actor with something to gain
from steering it — which is exactly why the non-local providers (`github`, `oidc`) require
`BLERG_CORE_PUBLIC_URL` rather than trusting this same request-derived value for their OAuth
`redirect_uri`: that URL is registered once with the IdP and reused for every login, so letting
it track whichever request happens to trigger it would let any requester steer where
authorization codes get delivered. This means an install can leave
`BLERG_CORE_ALLOWED_RETURN_ORIGINS` completely unset and core's own `/login` → `/app` flow still
works; the allowlist only needs entries for OTHER origins (board, runner) that also redirect
through this endpoint.

Every successful `GET /auth/refresh` **rotates** the refresh token: the presented cookie's
session is revoked (with `replaced_by` pointing at the new row) and a new cookie value is set
in the same response, inheriting the original session's `user_agent`/`ip` and its absolute
`expires_at`/`chain_id` lineage `BLERG_CORE_SESSION_TTL` controls (see above — rotation narrows
the cookie's `Max-Age`, it never resets it). Presenting an already-rotated-out refresh token
again (e.g. a copied/stolen cookie replayed after the legitimate client already rotated past it)
is usually theft and revokes the entire rotation chain — every session descended from the same
original login is killed and every device tied to it is forced to sign in again — UNLESS it
happens within a 60-second grace window of the rotation AND the successor session is still live,
in which case it's treated as a benign double-refresh race (a lost redirect, two tabs refreshing
close together): the caller gets a fresh access token against the existing successor session, no
new row is created, and the cookie is left untouched. `POST /auth/logout-all` (gated behind a
human bearer token, not the refresh cookie) does the same thing on demand: it revokes every live
session for the caller's account, plus the account's `sub` in the shared revocations table (so
already-minted access tokens on other devices, not just future refresh attempts, stop verifying
immediately), and clears the calling browser's own refresh cookie.

### Login CSRF protection

`POST /auth/login`, `POST /auth/logout`, and `POST /auth/logout-all` are all cookie-bearing
state changes, and are all guarded by a same-origin check before doing anything else — this
closes the login-CSRF hole where a hostile page could silently POST a victim's browser to
`/auth/login` with the ATTACKER's own credentials, logging the victim into an account the
attacker controls. The check (`sameOriginRequest` in `core/internal/api/auth_handlers.go`):

1. If the request carries `Sec-Fetch-Site` (sent by every modern browser, on every request), it
   must be `same-origin` or `none`. Anything else (`cross-site`, `same-site`) is rejected.
2. Otherwise, if the request carries an `Origin` header (sent by every browser on every POST),
   it must match `publicOrigin(r)` — see `BLERG_CORE_PUBLIC_URL` above.
3. A request with neither header carries no browser-CSRF signal at all — it's a non-browser
   client (a script, `curl`, another service), and CSRF is a browser-only attack (it relies on
   the *victim's own browser* automatically attaching credentials an attacker page can't read),
   so it's allowed.

A rejected request gets `403 cross-site request rejected`. For `POST /auth/logout-all`, which is
also gated behind a valid human bearer token (`requireHumanPrincipal`), the bearer-token check
runs first (in `router.go`'s route registration) — so a request with an invalid/missing token
gets the usual `401` regardless of its CSRF headers, and the CSRF check's own `403` is only
observable once a request already carries a genuinely valid token.

### `GET /api/me`

Returns the caller's own identity — gated behind a valid bearer token (`requirePrincipal`; the
handler itself decides which kinds it will answer for). This is how `core/web`'s `/app` page
shows who is signed in: it calls this endpoint rather than decoding the access token
client-side, since the token's own claims (`contracts/identity.Claims`) carry only `sub`, not
anything human-readable. It is also how an external tool confirms an agent token works — see
[Agent tokens](#agent-tokens) below.

```json
{
  "account_id": "…",
  "provider": "local",
  "provider_subject": "alice",
  "email": "alice@example.com",
  "role": "member",
  "must_change_password": false
}
```

`password_hash` (and anything else on the `accounts` row not listed above) is never included.
Without a token: `401`.

A **`platform` agent token** (`kind: "agent"`, `aud: blerg-core`) gets a different shape — the
token's own introspection, so a tool can confirm its credential works and learn what it may do
before trying anything:

```json
{
  "kind": "agent",
  "token_id": "…",
  "account_id": "…",
  "provider_subject": "alice",
  "role": "member",
  "caps": ["card.read"]
}
```

`token_id` is the token's `sub`; `account_id` is its `on_behalf_of` (the owning account);
`caps` is exactly what core enforces. Any other token — a service token, or an agent token with
no `agent_tokens` row behind it (one minted internally rather than by a user), or one that has
been revoked or expired out of that table — gets `403`.

### Frontend build-time vars

These are read by Vite at **build** time (`core/web`, `board/web`,
`runner/frontend` all share the same `authClient.ts`), not by the server.

| Var | Default | Notes |
|---|---|---|
| `VITE_CORE_URL` | unset | Absolute origin blerg-core is served from, e.g. `https://core.example.com`. When unset, `coreOrigin()` derives it: strip a leading `board.`/`runner.` label (the k8s ingress shape, where core is the apex domain), or, for a bare hostname or raw IP, keep the host and use core's port (the desktop-compose shape). Set this for any topology that matches neither. Available as a Docker build arg on all three images. |
| `VITE_CORE_PORT` | `8081` | Port used by the bare-hostname branch above. Change it when `BLERG_PORT_CORE` is changed in `install/desktop/.env`. |

## Discovery (public)

`GET /agents` and `GET /openapi.json` are **unauthenticated, on core and on every component**, by
design: an agent has to be able to read how to obtain a credential before it holds one, so gating
discovery behind a credential would make the contract unbootstrappable. Neither document carries a
secret — public base URLs, version strings, capability names, preset names, and the documentation
of endpoints that each enforce their own auth.

Core's `/agents` is the **aggregate** manifest: core's own entry first, then every registered
component, each with an absolute `token_endpoint` core fills in (a component never states core's
public origin itself), plus a quickstart written for an LLM to follow. With
`Accept: text/markdown` (or `?format=md`) the same document comes back as Markdown; `Accept:
application/json` (the default) gives the machine-readable form. The runner's own manifest and
session contract are documented in
[`runner/README.md`](../../runner/README.md#api-contract-v1).

## Agent tokens

Named, scoped, revocable credentials a user mints in the browser (spec §2), so an external
automated tool needs neither database access nor a shared static key. A token is a core-signed
JWT with `kind: "agent"`, `sub` = the token's own id, `on_behalf_of` = `lineage` = the owning
account id, and the preset's audience and capabilities.

Only a **SHA-256 hash** (hex) of the token is stored, in `agent_tokens.token_hash`. The value
itself is returned exactly once, by `POST /api/tokens`, and is unrecoverable afterwards — it is
never logged, never listed, and never re-displayed.

### Presets

A closed list: audience and capabilities are never free-form, and the caps are additionally
**intersected with the owner's platform role**, so nobody can mint a token that does more than
they can.

| Preset | `aud` | Caps | Grants |
|---|---|---|---|
| `run-sessions` | `blerg-runner` | `session.start` | The runner session contract and the runner MCP server. |
| `board` | `blerg-board` | `card.read`, `card.write`, `column.write` | Board REST and the board MCP server. |
| `platform` | `blerg-core` | `card.read` | Core's `GET /api/me` and the `GET /api/credentials` list. |

Capabilities are **frozen at mint**: they are baked into the signed token, so demoting an
account (or narrowing a role) does not shrink the tokens it already holds. To take capabilities
away from an existing token, revoke it — individually, or account-wide with "log out
everywhere".

A `run-sessions` token is additionally scoped to the sessions it started: it can drive its
owner's sessions and nothing else, while an admin (`board.admin`) sees them all.

### Endpoints

All three require a **human session** (`requireHumanPrincipal` with `card.read` — the same gate
`/api/credentials` uses, so a must-change-password bootstrap token cannot mint anything). An
agent token can never mint another agent token: revoking the parent would not reach the child.

| Route | Notes |
|---|---|
| `POST /api/tokens` | `{name, preset, expires_in_days?}` → `201 {id, token, name, aud, caps, expires_at, created_at}`. Same-origin (CSRF) checked and rate-limited; see below. |
| `GET /api/tokens` | The caller's own tokens, newest first: `{id, name, aud, caps, created_at, expires_at, last_used_at, revoked_at}`. Never the token or its hash. |
| `DELETE /api/tokens/{id}` | `200`, idempotent. Another account's id — or an unknown or malformed one — is `404`, never a `403` that would confirm the id exists. |

Validation on `POST`: `name` is trimmed and must be 1–64 characters; `preset` must be one of the
three above; `expires_in_days` defaults to `90` and must be between `1` and `365`. Each is a
`400`. A disabled account gets `403`. The body is capped at 16 KiB before decoding (`413`
beyond that).

An account may hold at most **50 live** (unrevoked, unexpired) tokens
(`maxLiveAgentTokens`). At the cap, `POST` answers
`409 {"error":"too many active agent tokens (limit 50); revoke one first"}` — the one rejection
the Settings UI renders as a specific, actionable message. Revoking a token or letting one
expire frees a slot; the cap bounds what an account *holds*, never how many it has ever
created.

`POST` and `DELETE` carry the same same-origin guard as `POST /auth/login` (see [Login CSRF
protection](#login-csrf-protection)) — minting or destroying a long-lived credential off a
browser session is exactly what a CSRF request would want. `GET` is read-only and has none.

`POST` is guarded by the same `LoginLimiter` instance as `/auth/login`
(`BLERG_CORE_LOGIN_RATE_LIMIT`), keyed on the authenticated account id and the caller's IP, so a
stolen access token does not buy an unbounded run at minting credentials; over the limit is
`429` with `Retry-After`. Only **rejected** attempts count against it — counting successes would
drain the per-IP bucket `/auth/login` shares and lock the account out of signing in.

### Revocation

`DELETE /api/tokens/{id}` stamps `revoked_at` **and** writes a `revocations` row of kind `sub`
with the token's id; every consumer already polls `GET /revocations`, and core checks it on
every request, so the token stops verifying everywhere.

Account-wide kill switches — "log out everywhere", a password change, an admin disable, a
reconcile removal — go through `RevokeAccountEverywhere`, which revokes the account id under
**both** `sub` (which matches human access tokens) and `lineage` (which matches agent tokens,
whose own `sub` is the token id). Both entries are timestamp-scoped, so anything minted
afterwards verifies normally. The `lineage` entry is deliberately never cleared again: removing
it on the next sign-in would resurrect every agent token the revocation killed, and an agent
token can live a year.

`RevokeAccountEverywhere` also stamps `revoked_at` on every one of the account's live
`agent_tokens` rows, so `GET /api/tokens` shows them as **Revoked** instead of listing dead
credentials as active.

That timestamp comparison is in whole seconds and equality fails closed, so a token minted in
the *same second* as an account-wide revocation would be revoked for its entire life while
listing as live. `CreateAgentToken` verifies every freshly signed token against the current
revocation snapshot and, if it comes back revoked, waits out the second and mints once more
with a fresh `iat` — it never persists a token that was dead on arrival.

### Always-on plugins

Each account has an ordered list of plugins per engine (`user_plugins`, migration 014) that the
runner installs into every new cluster session started as that account. Only `claude` is
registered today (`core/internal/plugins`); an entry is `{marketplace, plugin}` — a GitHub
`owner/repo` and a plugin name (`^[a-z0-9][a-z0-9._-]{0,63}$`). Max 20 per account, no duplicates,
marketplace must be allowed by `BLERG_CORE_PLUGIN_MARKETPLACES`. The list is not secret.

- `GET /api/plugins/{engine}` and `PUT /api/plugins/{engine}` (body `{"plugins":[…]}`, replaces the
  whole ordered list atomically; a rejected list changes nothing): a signed-in **human** session
  only, for the caller's own account. An agent token gets `403` on both — plugins run code in every
  future session, so an agent must never be able to add one. Validation failures are `422` with a
  short plain-text reason.
- `POST /internal/plugins/list` (`X-Internal-Key`, same as the credential endpoints): the runner
  reads the list for a session it is starting. The body must carry `account_id`, `engine` and
  **exactly one** liveness proof, `token_id` or `session_id`, verified as described under
  [Internal endpoints](#internal-endpoints-which-live-thing-authorises-the-call); neither, or
  both, is `400`. (The old bare `human_session: true` claim is gone; it is now "neither".) A dead
  or foreign principal is the same anti-enumeration `404`. Returns `{"plugins":[…]}` only.

Known limits: a human session that is logged out or revoked makes the runner's read `404`, so a
resumed session then starts without plugins (the start panel says so); with `BLERG_CORE_PLUGIN_MARKETPLACES=*`
two allowed marketplaces may share a marketplace *name*, and then `plugin@name` is ambiguous in
the CLI; core and the runner read separate allow-list variables, so keep them equal or the
runner's stricter list silently wins at install time. Two saves racing on the same list: the loser
gets `409`. A plugin, once installed, acts with the session's full access — the allow-list is the
control.

### MCP connections and cron tokens

A person's remote MCP servers (`mcp_connections`, migration 015) and the tokens that let a scheduled
run act for them. Concepts and the security properties are in
[`docs/mcp-connections.md`](../../docs/mcp-connections.md) and [`docs/crons.md`](../../docs/crons.md).

- `GET/POST /api/mcp/connections`, `PATCH/DELETE /api/mcp/connections/{id}`: a signed-in **human**
  only, for the caller's own account (an agent token gets `403`; another account's id is `404`).
  Writes are same-origin checked. A response never contains the stored secret. At most 20
  connections per account; a name is a short slug and may not be `board`, `blerg` or `gateway`.
- `POST /internal/mcp/connections/list`, `.../token` and `.../defaults` (`X-Internal-Key`, the
  runner only): `list` returns the account's connections without secrets, `token` returns exactly
  one connection's credential after writing an audit row (`mcp_secret_access_log`; a failed audit
  write aborts the call), `defaults` saves a connection's default tool selection and accepts a
  login-session proof only. All three take `account_id` plus a liveness proof as described under
  [Internal endpoints](#internal-endpoints-which-live-thing-authorises-the-call), and every failure
  past the shape checks is the same `404`.
- `POST /internal/tokens/mint`, `.../revoke`, `.../status` (`X-Internal-Key`, the runner only): the
  cron token. `mint` needs a live login session as proof and returns only an id and an expiry; no
  signed token is ever produced, so the token cannot be presented anywhere. It counts toward the
  50 live agent tokens per account, expires after at most 365 days and is hidden from the token
  list you can copy from. `revoke` needs the owning `account_id`; "log out everywhere" and
  revoking the token in Settings make `status` answer not live.

### Internal endpoints: which live thing authorises the call

`POST /internal/credentials/fetch`, `POST /internal/credentials/list` and
`POST /internal/plugins/list` all take `account_id` plus **exactly one** of two proofs naming the
live thing that stands behind the call. Core verifies that *specific* thing belongs to
`account_id` and is live; a valid internal key plus an account uuid is never enough, even when
that account is logged in. The limit on what a key holder can read is enforced here, not left to
the callers (the runner, and board in gate account mode).

- `token_id`: an agent token (a session started with an agent token has no browser behind it).
  It must be one of that account's tokens, unrevoked and unexpired; the check stamps the token's
  `last_used_at`.
- `session_id`: the human browser session that started the launch, i.e. the `sid` claim of the
  human access token the launching request carried. It is the session's rotation-chain id
  (`human_sessions.chain_id`), stable across refresh rotations, so a token stays valid for the
  whole session even after the refresh cookie rotates. It must name a chain of that account with
  an unrevoked, unexpired session row, on an account that is not disabled.

Neither proof, both, or a malformed id is `400` (a structural mistake). A proof that is unknown,
revoked, expired, or another account's is the one uniform `404 not found`, the same status and
body as "no credential stored for this engine". Each proof is the whole gate: a live session never
rescues a dead token and vice versa (naming both is `400`).

**Tokens minted before `sid` existed.** Human access tokens carry `sid` from the release that
introduced this rule. A token minted before it (10-minute TTL) has none, so callers cannot name a
session: the runner **fails closed** for those (a session start answers `401` with "Reload the
page (or sign in again) to get a fresh session", credential listing answers "unavailable"), and
there is no "any live session of the account" fallback. A page reload mints a fresh token.

Successful fetches are audited in `credential_access_log` with the principal that authorised
them: `fetched_by_session_id` (the live session row) or `fetched_by_token_id`, the other left
NULL, so an auditor can always tell which kind of principal performed the fetch. A refused
request writes no row, and no row ever holds a credential value.

## Auth providers

Exactly one provider is active. `local` is the default and the only one that
needs no external service.

| Var | Default | Notes |
|---|---|---|
| `BLERG_CORE_AUTH_PROVIDER` | `local` | One of `local`, `github`, `oidc`. An unknown value fails boot rather than silently falling back. |

### `local`

Username + password against core's own `accounts` table. No configuration.

On the very first boot against an empty `accounts` table, core seeds exactly
one admin account with a random password and logs it once, with a greppable
prefix:

```
BLERG_BOOTSTRAP_ADMIN_PASSWORD=<password> (login as provider_subject=<subject>; ...)
```

It is logged, never written to disk — the container filesystem is ephemeral, so
capture it from the logs on first boot (`docker compose logs blerg-core | grep
BLERG_BOOTSTRAP_ADMIN_PASSWORD`, or `kubectl logs`). Seeding is idempotent: a
non-empty `accounts` table is a no-op, and it only runs under the `local`
provider — so a restart, rebuild, or redeploy never prints the line again and
never changes the password. If the log that had it is gone, mint a new
one-time password with `blerg-core users set-password --subject <subject>`
(see "Operator CLI" below).

The bootstrap account is also seeded with `must_change_password = true`, and this
is enforced server-side, not just suggested in the UI (R7/I-7): every access token
`MintHumanAccessToken` mints for such an account carries **only** the
`password.change` capability — this replaces the account's normal role caps
entirely, so an admin whose password is still pending rotation can do nothing
useful but change it, no matter what the client sends (`GET /api/me` and
`POST /auth/logout-all` stay reachable too — neither is capability-gated — but
neither leaks or changes anything the flag needs to withhold). The endpoint that
capability actually unlocks is `POST /auth/password`
(`{"old_password","new_password"}`); the SPA notices the same thing client-side
(`core/web/src/claims.ts`'s `mustChangePassword`, decoding the token's `caps`
claim without verifying it — UI routing only, not a security boundary) and
routes straight to `/change-password` instead of a page full of silent 403s.
`new_password` must be 12–72 **bytes** (bcrypt's own input limit is 72 bytes;
past that `bcrypt.GenerateFromPassword` errors, so this is enforced up front
rather than surfacing as a 500) — both bounds reject with 400. A successful
change stores the new bcrypt hash and clears the flag in one statement, then
revokes the account **everywhere**: every live session AND the account's `sub`
entry in the shared revocations table, so an access token already minted on some
OTHER device before the change — the scenario a password rotation exists to
defend against — stops verifying immediately, not just on its next refresh
(mirroring `POST /auth/logout-all`'s `RevokeAccountEverywhere`). The caller's own
refresh cookie is cleared too, and a subsequent successful login clears the `sub`
revocation the same way it always does — this is not a permanent lockout.
Anyone with `kubectl logs`/`docker compose logs` access on core sees the initial
printed password, so rotate it immediately after first boot.

### `github`

GitHub org membership as the source of truth for accounts. All three vars are
required when this provider is selected; boot fails if any is missing.

| Var | Default | Notes |
|---|---|---|
| `BLERG_CORE_GITHUB_CLIENT_ID` | — (required) | OAuth app client id. |
| `BLERG_CORE_GITHUB_CLIENT_SECRET` | — (required) | OAuth app client secret. |
| `BLERG_CORE_GITHUB_ORG` | — (required) | Org whose membership is mirrored into `accounts`. |
| `BLERG_CORE_GITHUB_RECONCILE_INTERVAL` | `5m` | Go duration between org-membership reconcile passes (accounts for people who left the org are deactivated). A malformed value logs and falls back to the default rather than failing boot. The loop only runs under the `github` provider. |
| `BLERG_CORE_GITHUB_TOKEN` | — (required for reconcile) | A GitHub token with `read:org` scope, used to authenticate the org-membership listing the reconcile loop makes. Without it, GitHub would answer an unauthenticated members listing with 200 and only the org's PUBLIC members — every account with a private membership would look gone and get disabled. So the loop does not start without it: with this unset, boot logs a loud `WARNING` and people who leave the org simply keep their access until it's set. |

### `oidc`

Generic OpenID Connect against any spec-compliant IdP (Google Workspace, Okta,
Keycloak, …). Authentication only — no group/role claim mapping: every account
this provider creates gets `role='member'`, and an admin promotes people
afterwards.

All three vars are required when this provider is selected. The issuer's
discovery document is fetched at boot, so an unreachable or invalid issuer
fails startup rather than the first login.

| Var | Default | Notes |
|---|---|---|
| `BLERG_CORE_OIDC_ISSUER_URL` | — (required) | Issuer URL, e.g. `https://accounts.google.com`. No trailing `/.well-known/...` — discovery appends that. |
| `BLERG_CORE_OIDC_CLIENT_ID` | — (required) | OIDC client id. Also checked as the ID token's `aud`. |
| `BLERG_CORE_OIDC_CLIENT_SECRET` | — (required) | OIDC client secret. |

### OAuth login flow (`github`/`oidc`)

Under a non-local provider, the frontend never posts credentials directly — it
sends the browser through core:

1. `GET /auth/provider` returns `{"id": "local"|"github"|"oidc"}`, so the login
   page knows whether to render the password form or a single "Sign in with
   …" link.
2. That link points at `GET /auth/start`, which 404s under `local`. It mints a
   random CSRF `state`, sets it (as `SHA-256(state)`, never the raw value) in
   an `HttpOnly`, `Secure`, `SameSite=Lax` cookie named `blerg_oauth_state`
   (`Path=/auth`, 10-minute `Max-Age`), and redirects to the IdP's authorize
   URL via `Provider.LoginURL(state)`.
3. The IdP redirects back to `GET /auth/callback?code=...&state=...`. The
   `state` query param must hash to the `blerg_oauth_state` cookie's value —
   a mismatch or missing cookie is `400` and no session is created. The
   cookie is cleared either way (on success or failure), so it can never be
   replayed against a second callback. On success the handler sets the
   session (`blerg_core_refresh`) cookie and redirects into
   `GET /auth/refresh`, same as a local-provider login.

`redirect_uri` (the URL the IdP sends the browser back to, and — for
`github` — the value GitHub also requires on the token-exchange request) is
always `<BLERG_CORE_PUBLIC_URL>/auth/callback`, **never** derived from the
incoming request. This is why `BLERG_CORE_PUBLIC_URL` is required (boot
fails fast otherwise) for `github`/`oidc` — see the entry in [Login CSRF
protection](#login-csrf-protection) above. Register that exact URL
(`<public>/auth/callback`) as the callback URL with the IdP (the GitHub
OAuth app's "Authorization callback URL", or the OIDC client's registered
redirect URI).

## Key backend

Encrypts the per-account credential vault (`core/internal/credentials`).

| Var | Default | Notes |
|---|---|---|
| `BLERG_CORE_KEYBACKEND` | `local` | `local` or `vault-transit`. An unknown value fails boot. |
| `BLERG_CORE_LOCAL_KEY` | — (required for `local`) | Base64 encoding of 32 random bytes, used directly as the AES-256-GCM key for the `local` backend. Generate one with `openssl rand -base64 32`. **Required** when `BLERG_CORE_KEYBACKEND=local` (the default) — boot fails fast with an actionable message if it's unset or not valid base64/32 bytes. |
| `BLERG_CORE_VAULT_ADDR` | — (required for `vault-transit`) | Vault address, e.g. `https://vault.example.com:8200`. |
| `BLERG_CORE_VAULT_K8S_ROLE` | — (required for `vault-transit`) | Vault Kubernetes-auth role core logs in as, using its mounted service-account token. |
| `BLERG_CORE_VAULT_TRANSIT_KEY` | — (required for `vault-transit`) | Name of the Transit key used for encrypt/decrypt. |

`local`'s AES-256 key comes **only** from `BLERG_CORE_LOCAL_KEY` — it is never
generated or stored in the database (a key sitting next to the ciphertext it
protects is obfuscation, not encryption). Keep it in the k8s `blerg-secrets`
Secret / desktop `.env` file (both installers generate it). **Losing it loses
every stored credential** — there is no recovery, since the key exists only
in the environment. **Changing it** (rotation) is equally destructive in v1:
`Decrypt` only ever accepts the one key_id a running instance was built with,
so every credential encrypted under the old key becomes unreadable and users
must re-enter them on the Settings page.

### Credential kinds

The vault stores at most one credential per account per *kind*, under a closed
allowlist (`validEngines` in `core/internal/api/credential_handlers.go`) — an
unknown kind is rejected with `400 unknown engine` by both
`POST /api/credentials` and `DELETE /api/credentials/{engine}`, so no free-text
name can ever be encrypted and stored:

| Kind | What it is |
|---|---|
| `claude`, `codex`, `hermes`, `openclaw` | The agent engine's own personal API credential, injected into the session the runner starts for that user. |
| `github`, `gitlab` | The user's personal token for that git provider (`gitCredentialKinds`). Not an engine — it is what lets a session pod clone and push as the launching human rather than as a shared service identity, and what the runner lists that user's own repositories with (GitHub: a fine-grained token with Contents read/write and Metadata read; GitLab: a personal access token with `read_api`, `read_repository`, `write_repository`). Each is the id of a git provider the runner registers (`runner/internal/gitprovider`); a new provider adds its id here. |

### `POST /internal/credentials/list`

Component-to-component only (same `/internal/...` prefix, no CORS, never
browser-facing), and gated exactly like `/internal/credentials/fetch`: the
`BLERG_CORE_INTERNAL_KEY` shared secret via `Authorization: Bearer <key>` or
`X-Internal-Key`, **plus** the independent requirement of exactly one named live
proof (`session_id` or `token_id`) belonging to `account_id` — see [Internal
endpoints](#internal-endpoints-which-live-thing-authorises-the-call). Unconfigured
key → `503`; wrong or missing key → `401`; non-UUID `account_id`, or no/both
proofs → `400`; a proof that is not live or not this account's → `404 not found`,
the same anti-enumeration shape as fetch's answer. That `404` means **"the
session/token you named is not live for this account"** — never "this account has
no credentials"; an account with several stored credentials 404s here the moment
that session logs out. A caller must treat it as "couldn't tell", not as an empty
list: an empty list is a `200` with `{"engines":[]}`.

Request `{"account_id":"<uuid>","session_id":"<uuid>"}` (or `"token_id":"<uuid>"` instead); success
`200 {"engines":["claude","github"]}`
— the sorted **names** of the kinds that account has stored, `[]` when none. It
returns no credential values and no timestamps, and therefore writes **no**
`credential_access_log` row: nothing decrypts here, so there is no credential
access to log. The runner uses it to shape a cluster session pod for the
launching user (which kinds to fetch) without probing fetch once per kind.

## Operator CLI

`blerg-core` doubles as a CLI for operator tasks that need direct database
access. Both subcommands below (and `mint`) dispatch **before** the server's
own `validateSecrets()` check — they only need `DATABASE_URL` to open the
store, not the full set of server-boot secrets (`BLERG_CORE_REGISTER_KEY` /
`BLERG_CORE_INTERNAL_KEY`), so a stack whose `.env` still has a placeholder
register/internal key doesn't block admin recovery.

| Subcommand | Purpose |
|---|---|
| `blerg-core mint --aud <audience> [--sub ...] [--project ...] [--caps ...] [--on-behalf-of ...] [--lineage ...]` | Mints a core-signed agent token for testing, printed to stdout. |
| `blerg-core users create --subject S --role member\|admin` | Creates a new `local`-provider account with a fresh one-time password and `must_change_password=true`. Fails with `ErrSubjectExists` (exit 1) if `provider_subject` is already taken, or exit 2 for an invalid `--role`. |
| `blerg-core users set-password --subject S` | Resets an existing `local` account's password to a fresh one-time value, sets `must_change_password=true`, and invalidates every existing session/token for that account (`identity.Service.RevokeAccountEverywhere`). Exit 1 if `S` has no local account. |

Both `users` subcommands print **exactly one line** to stdout on success:

```
provider_subject=<S> password=<one-time>
```

That line is the only place the new password is ever available in
plaintext — it is never logged, stored, or printed anywhere else. Exit
codes: `0` success, `2` usage/argument error (missing `--subject`, bad
`--role`), `1` any other failure (unreachable database, unknown subject on
`set-password`, duplicate subject on `create`).

Typical desktop usage (from `install/desktop`, with the stack already up):

```
docker compose run --rm --no-deps -T --entrypoint /blerg-core \
  -e "DATABASE_URL=postgres://blerg:blerg@postgres:5432/blerg_core?sslmode=disable" \
  blerg-core users create --subject alice --role member
```

`install/desktop/blerg-up.sh --reset-admin` wraps `users set-password` for
the common "I lost the bootstrap admin password" case: it looks up the
**oldest** local admin account (by `created_at` — deterministic even if a
second admin was created later) and resets its password.

## Related vars read by other components

Not read by `blerg-core` itself, but they must agree with the values above.

| Var | Component | Notes |
|---|---|---|
| `BLERG_CORE_URL` | board, runner | Base URL of core. Enables core as the identity/discovery authority for bearer tokens. Unset leaves that path inert. |
| `BLERG_CORE_REGISTER_KEY` | board, runner | Must equal core's own value. |
| `BLERG_RUNNER_CORE_INTERNAL_KEY` | runner | Must equal core's `BLERG_CORE_INTERNAL_KEY`. Together with `BLERG_CORE_URL`, enables the personal-credential-first session spawn path; either unset means runner always uses the shared operator Secret. |
| `BLERG_RUNNER_ALLOWED_ORIGINS` | runner | Comma-separated `scheme://host` origins allowed to open the `/ws/browser` WebSocket cross-origin. Same-origin requests and requests with no `Origin` header are always allowed, so this is only needed when the frontend is served from a different origin than runner's API. |
| `BLERG_BIND_ADDR` | `install/desktop/docker-compose.yml` | Not read by any Go process — it's a `docker compose` interpolation variable that prefixes all three published ports (core/board/runner): `${BLERG_BIND_ADDR:-127.0.0.1}:${BLERG_PORT_CORE:-8081}:8080`, etc. (R2/C1: without a host address, Docker publishes on `0.0.0.0` and bypasses `ufw`/`firewalld` — every LAN/VPN peer can reach core, board and runner). Defaults to `127.0.0.1` (loopback only). Set to `0.0.0.0` only if other machines must reach the stack — and then also point `BLERG_CORE_ALLOWED_RETURN_ORIGINS`/`BLERG_CORE_ORIGIN_AUDIENCES`/`BLERG_CORE_PUBLIC_URL` at that host, or login will still only work from `localhost`. |
