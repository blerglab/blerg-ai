# AI crons and MCP connections: design

Status: implemented. Applies to `contracts`, `core`,
`board`, `runner` and the runner and core frontends.

## 1. Purpose

Let a person schedule an AI agent to run unattended, give it access to their own
services through MCP servers, and have it keep a board up to date. The motivating case
is a personal "focus" board: a few times a day an agent reads email and a calendar,
creates and updates cards, and proposes actions (a reply, a calendar entry) that the
person approves. The person can edit the board and ask for new tracking at any time.

Two features make this possible and are useful on their own:

1. **MCP connections.** A user connects remote MCP servers once, in settings. A session
   or a cron then enables the connections it needs, explicitly, per session.
2. **Crons.** A scheduled, owner-scoped definition that starts an ordinary one-shot
   agent session on a schedule.

### Goals

- Works on a pure desktop install (no cluster) and on a cluster, with one code path.
- A cron survives the machine being off: a missed slot runs once when the stack comes
  back, not once per missed slot.
- Connections are per user. One account's cron can never use another account's
  connection.
- Everything defaults to off: no connection is available to a session unless selected,
  and no tool is available unless allowed.
- Long-lived secrets never enter a session pod or sandbox container.
- An agent that reads untrusted content (email) cannot take an outward action without
  a human approving it, unless a person explicitly allowed that tool for that cron.

### Non-goals (this version)

- A visual workflow builder, a marketplace, event triggers (webhooks, new mail).
- Agents creating or editing crons or connections. Both are human-only.
- Approving a proposal from inside the board UI (a card links to the runner).
- Local (stdio) MCP servers. Only remote HTTP servers.
- Engines other than Claude. Codex, Hermes and OpenClaw have no MCP injection path here;
  a session or cron with an MCP grant must use `engine = claude`, and the native
  (non-Claude-Code) agent loop is not supported.
- Provider-specific OAuth clients. The exchange follows the MCP authorization
  specification.

## 2. Concepts

| Term | Meaning |
|---|---|
| **Connection** | A user's saved link to one remote MCP server plus its credential. Stored in core. |
| **Grant** | The connections, each with per-tool modes, one session may use. Stored in the runner, per session. |
| **Tool mode** | Per tool: `allow` (the agent calls it), `propose` (the call is captured for approval) or `off`. Unlisted tools are `off`. |
| **Gateway** | A component of the runner server between a session and the upstream MCP servers. It enforces grants and holds credentials. |
| **Proposal** | A captured tool call, frozen with its arguments, awaiting a human decision. |
| **Cron** | A schedule plus prompt, target and grant that starts a session. Owned by one account. |
| **Run** | One firing of a cron and the session it started. |

## 3. Architecture

```
 browser ──► core (settings: connections, OAuth)      runner UI (launch sheet, crons, proposals)
              │  vault: secrets, OAuth tokens                 │
              │  internal API (key + liveness proof)          ▼
              └───────────────────────────────────►  runner server
                                                     ┌────────────────────────────┐
                                                     │ scheduler ──► StartSession │
                                                     │ MCP gateway ◄── session    │
                                                     └───────┬────────────────────┘
                                                             │ upstream MCP (HTTPS)
                                                             ▼
                                                     calendar, mail, board, ...
```

- **Core** owns connections and credentials and performs the OAuth exchange.
- **Runner server** owns crons, the scheduler, grants, the gateway and proposals.
- **Sessions** (cluster pods, desktop sandbox containers) see only the gateway. Each
  enabled connection appears as one MCP server whose URL is on the gateway, with a
  token that works for that session and connection only.

### Why a gateway

Writing credentials and MCP servers into the session's Claude configuration and
limiting tools with client flags is ruled out:

1. The session would hold every enabled credential, and an agent reading hostile
   content can be steered into leaking what it holds.
2. The daemon launches Claude with `--dangerously-skip-permissions` on every turn, so an
   allow-list is not something to rely on as a security boundary.
3. Turning a write call into a queued proposal cannot be done from outside the process
   that makes the call.

One mechanism solves all three and behaves the same on pods and desktop.

### The gateway does not close every exfiltration path

A gateway restricts MCP. A session that also has a shell or web access can send data it
has read anywhere it can reach. So cron sessions, and any session with a grant, also
run under the controls in section 8.

## 4. MCP connections (core)

### 4.1 Data model

Core migration `015_mcp_connections`:

```sql
CREATE TABLE mcp_connections (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
  url text NOT NULL,
  auth_kind text NOT NULL CHECK (auth_kind IN ('none','static','oauth')),
  header_name text,                      -- static only, validated (4.2)
  secret_ciphertext bytea, key_id text,  -- static secret, or the oauth token bundle
  oauth_meta jsonb,                      -- issuer, resource, client_id, endpoints (no secrets)
  default_tools jsonb NOT NULL DEFAULT '{}',  -- {"tool": {"mode": "allow"|"propose", "hash": "..."}}
  status text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','needs_auth','error')),
  last_verified_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (account_id, name)
);
CREATE TABLE mcp_secret_access_log (
  id bigserial PRIMARY KEY, account_id uuid NOT NULL, connection_id uuid,
  fetched_by_session_id uuid, fetched_by_token_id uuid, fetched_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE mcp_oauth_state (   -- single-use, server-side (4.3)
  state_hash bytea PRIMARY KEY, account_id uuid NOT NULL, sid text NOT NULL,
  code_verifier text NOT NULL, draft jsonb NOT NULL, issuer text NOT NULL,
  resource text NOT NULL, redirect_uri text NOT NULL, expires_at timestamptz NOT NULL
);
```

Secrets are encrypted with the existing key backend, as engine credentials are. Limit:
20 connections per account. Names `board`, `blerg` and `gateway` are reserved, and the
name is also rejected if it collides with another after Claude Code's tool-prefix
normalisation. Deleting an account cascades. Retention: `mcp_secret_access_log` rows
older than 180 days are pruned by a daily job.

### 4.2 Static connections

The user gives a URL, a header name (default `Authorization`) and a value. Validation:

- `header_name` must be an RFC 7230 token and must not be a hop-by-hop or framing header
  (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, `TE`,
  `Trailer`, `Mcp-Session-Id`, `Content-Type`, `Accept`, `Cookie`).
- The value must not contain CR, LF or NUL and is capped at 4 KiB.

### 4.3 OAuth connections (the exchange)

For servers implementing the MCP authorization specification (protected-resource
metadata discovery, authorization-server metadata, dynamic client registration where
offered, PKCE):

1. The user starts a connect flow. Core discovers the metadata, registers a client if
   the server supports it (otherwise the user supplies a client id), and stores a row
   in `mcp_oauth_state` keyed by the hash of a random `state`, holding the account, the
   login session id, the PKCE verifier, the issuer, the RFC 8707 `resource`, the exact
   redirect URI and a 10 minute expiry. Core also sets a `SameSite=Lax` cookie holding
   the hash of `state`, as the login flow does (the refresh cookie is `SameSite=Strict`
   and is not sent on the top-level redirect back).
2. The callback `GET /auth/mcp/callback` requires a matching, unexpired, unused state
   row (deleted on use), the Lax cookie, a live login session (`HumanSessionLive`), an
   exact redirect-URI match and, when the server returns it, an RFC 9207 `iss` equal to
   the recorded issuer. PKCE is S256 only. The redirect URI is
   `<BLERG_CORE_PUBLIC_URL>/auth/mcp/callback`; on a desktop install that is core's
   published local address.
3. Core stores the refresh token and current access token encrypted. Consumers never
   receive the refresh token. They call the token exchange (4.5) and receive an access
   token that is never cached past its expiry.
4. Refresh takes a row lock on the connection (`SELECT ... FOR UPDATE`) so two
   concurrent callers cannot both spend a rotating refresh token. A refresh failure sets
   `needs_auth`.
5. Deleting the connection revokes the grant upstream where the server supports it.

All fetches core makes (discovery, registration, token, revocation) use the network
policy in 4.6.

### 4.4 Public API (human sessions only)

Every route requires a human principal (`requireHumanPrincipal`) with the `card.read`
gate like tokens and plugins, and is covered by the CSRF and authorization matrices.

| Route | Purpose |
|---|---|
| `GET /api/mcp/connections` | List the caller's connections. Never returns secrets. |
| `POST /api/mcp/connections` | Create a `none` or `static` connection. |
| `POST /api/mcp/connections/oauth/start` | Begin an OAuth connect; returns the authorization URL. |
| `GET /auth/mcp/callback` | OAuth redirect target (unauthenticated by design, protected by the state row). |
| `PATCH /api/mcp/connections/{id}` | Rename, change `default_tools`, replace a static secret. |
| `DELETE /api/mcp/connections/{id}` | Remove it. |

### 4.5 Internal API (runner to core)

Guarded like `/internal/credentials/*`: the internal key plus exactly one of `token_id`
or `session_id` as a liveness proof. Any failure, including another account's
connection, returns the uniform `404`.

| Route | Purpose |
|---|---|
| `POST /internal/mcp/connections/list` | The account's connections: id, name, URL, status, `default_tools`. No secrets. |
| `POST /internal/mcp/connections/token` | For one connection: `{url, header_name, value, expires_at}`. OAuth: a fresh access token. Static: the stored secret. Writes an audit row first and fails if it cannot. |
| `POST /internal/tokens/exchange` | Mint a short-lived agent token for a built-in target (section 10.2). TTL 10 minutes. The audience and caps are fixed by the target and intersected with the account's role caps. The token's `sub` is recorded so the runner can revoke it at session end. |
| `POST /internal/tokens/mint` | Create a cron token (7.4). Proof must be a `session_id` (a live human login). Inserts an `agent_tokens` row of kind `cron` (not listed as a copyable token in `GET /api/tokens`) with a random, never-issued hash, and returns only `{id, expires_at}`. No signed token is produced, so nothing valid can be presented on the wire. Counts toward the 50 live tokens per account. |
| `POST /internal/tokens/revoke` | Revoke a token id. The request names `account_id` and core checks it owns the token (proof: the human `session_id`, or, when the scheduler pauses a cron on the account's behalf, the internal key together with that `account_id` and the token id). The internal key alone never revokes another account's token. |
| `POST /internal/tokens/status` | Whether a token id is live and which account owns it. |

### 4.6 Network policy (SSRF)

The URL is user supplied and is fetched by core (discovery, OAuth) and by the runner
(gateway, tool listing). One shared package in `contracts` implements the dialer:

- `https` only. `http` only for hosts an operator lists
  (`BLERG_CORE_MCP_ALLOW_HTTP_HOSTS`, `BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS`).
- The check runs on the resolved IP at connect time (`net.Dialer.Control`), on every
  connection, including IPv4-mapped IPv6, `0.0.0.0`, loopback, link-local, cloud
  metadata, private and carrier-grade NAT ranges. Private hosts are allowed only when
  listed (`BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS`, `BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS`).
- No redirects are followed. No proxy environment variables are honoured.
- Response size and time limits on every call.

The runner and core are separate processes with separate allow-lists. New variables are
added to the compose file, the Kubernetes configmap comments and `.env.example`.

### 4.7 Settings UI (core web)

An "MCP connections" section beside "Plugins": list with status, Add (static or OAuth),
Reconnect, Remove. The tool picker (6.3) lives only in the runner UI, because only the
runner can list a server's tools; core settings links to it. `default_tools` is set from
the runner UI through a runner route that calls core's `PATCH` on the user's behalf.

## 5. The gateway (runner server)

### 5.1 What the agent sees

For each enabled connection the runner adds one MCP server named after the connection:

```json
{"mcpServers": {"calendar": {"type": "http",
   "url": "<gateway base>/mcp-gw/calendar",
   "headers": {"Authorization": "Bearer <gateway-token>"}}}}
```

The config is written to a `0600` file in a private temporary directory outside the
session's working directory and passed as `--mcp-config <path> --strict-mcp-config`, so
no ambient MCP configuration applies and the token is not in `ps` or `docker inspect`
output. `ccTurnArgs` (currently a pure function of text, model, effort and resume id)
gains parameters for the config path and for disallowed tools (section 8).

There is **one token per (session, connection)**. It is random, stored only as a hash,
compared in constant time, and the request path carries only the connection name; the
gateway derives the grant from the token and rejects a path that does not match it.
Failed attempts are rate limited.

### 5.2 Delivery to each runtime

- **The base URL.** The gateway has its **own listener** (5.5), so sessions use a
  gateway base URL, configured as `BLERG_RUNNER_MCP_GW_URL` (the address sessions dial:
  an in-cluster Service for pods; for desktop sandbox containers the runner's address on
  the sandbox network). It is sent to a session as part of the config, not derived from
  the daemon's WebSocket address. A spawn with a grant is refused with a specific error
  if the gateway is not configured.
- **Cluster pods.** The config (with tokens) is placed in the per-session Kubernetes
  Secret, never in the Job spec, and written to a temp file by the pod entrypoint before
  the agent starts.
- **Daemon sessions.** `SpawnSession` gains a `MCPGateway` field (path prefix, per
  connection name and token). It is not sent through `ExtraEnv`, which drops
  `BLERG_RUNNER_*` keys. The daemon's hello message gains an `mcp_gateway` capability.
  The server **refuses** to start a session with a grant on a daemon that does not
  report it, because an older daemon would ignore the unknown field and run with no MCP
  at all.
- **Persistence and resume.** The daemon's recovery record stores the field so a
  restarted daemon can relaunch the session. A resumed session goes through the start
  path again and receives new tokens; the old ones are revoked with the session.
- **Token cleanup.** Gateway tokens are removed when the session ends, reusing the
  existing revoke-on-end path for session tokens.

### 5.3 Grants

Runner migration `020_mcp_grants.sql`:

```sql
CREATE TABLE session_mcp_grants (
  session_id uuid NOT NULL, connection_id uuid NOT NULL, name text NOT NULL,
  account_id text NOT NULL,
  tools jsonb NOT NULL,                -- {"tool": {"mode": "allow"|"propose", "hash": "..."}}
  url_snapshot text NOT NULL,          -- connection URL at grant time
  proof_kind text NOT NULL CHECK (proof_kind IN ('token_id','session_id')),
  proof_value text NOT NULL,
  token_hash bytea NOT NULL UNIQUE,
  call_budget int NOT NULL, calls_used int NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, connection_id)
);
```

The proof is what the gateway presents to core. A cron session uses the cron token's
id. A human-started session uses the starter's login session id, which is valid for the
login's lifetime (up to 30 days), not the 10 minute access token. If the login ends
first, credential fetches fail with a clear "sign in again" error.

### 5.4 Behaviour

The gateway implements the MCP Streamable HTTP transport as a proxy and is a strict
**allow-list of methods**:

- Accepted: `initialize`, `notifications/initialized`, `notifications/cancelled`,
  `ping`, `tools/list`, `tools/call`. Everything else (resources, prompts, completion,
  logging, tasks, roots, sampling, elicitation) is refused. JSON-RPC batch arrays are
  rejected.
- `initialize` is answered by the gateway. The gateway keeps its own upstream MCP
  session per (grant, connection), initialises it on first use and re-initialises it if
  the upstream drops it.
- Upstream replies as either JSON or an event stream are read until the response to the
  request arrives and returned to the session as one JSON reply. Server-initiated
  requests are not supported.
- `tools/list`: all pages are fetched (following `nextCursor`, at most 500 tools), then
  filtered by exact tool name to those whose mode is `allow` or `propose` **and** whose
  pinned hash still matches (6.3). Tool descriptions are passed through with a length
  cap.
- `tools/call`, mode `allow`: forwarded with a fresh credential. Mode `propose`: **not
  forwarded**; the arguments are frozen into a proposal (section 9) and the agent gets a
  normal result saying the action was queued, with the proposal id and its runner URL.
  Any other tool is refused.
- Results are text-only: image, audio, embedded-resource and resource-link blocks are
  replaced with a short placeholder, and each result is capped (256 KiB). Upstream
  output is an injection channel and is treated as untrusted content everywhere.
- Limits: per-call timeout, per-grant concurrency (4), a per-session **call budget**
  (default 200, counting `tools/list` and `propose` calls too, so a looping agent cannot
  poll without bound, incremented atomically in the database:
  `UPDATE ... SET calls_used = calls_used + 1 WHERE calls_used < call_budget`), and a
  cap on pending proposals per account (50).
- The credential is fetched from core on first use and cached in memory for at most
  **5 minutes** or the token's expiry, whichever is shorter, then re-fetched. Deleting
  or rotating a connection therefore takes effect within minutes, and each fetch checks
  the connection still exists. It is never written to disk or logged.
- Every call is recorded in `mcp_call_log` (session, connection, tool, mode, outcome,
  duration; not arguments or results). Rows older than 90 days are pruned.

### 5.5 Placement and exposure

The gateway is served on a **separate listener and `http.Server`** in the runner
process (`BLERG_RUNNER_MCP_GW_ADDR`, default a distinct port), with its own read, write
and idle timeouts, so a hostile upstream or a looping agent cannot starve the control
plane and the gateway is never reachable through the public Ingress, which routes only
the main port. On Kubernetes it is exposed only through an internal Service, the
session network-policy example allows pod-to-gateway traffic on that port, and a
manifest test asserts the Ingress does not route it. On desktop it is published only on
the sandbox network. Every request is authenticated by the token before any body is
read, has body-size and stream caps, and the per-grant limits above.

## 6. Selecting connections

### 6.1 Session launch

A single function, used by both `POST /api/sessions` and the in-process `StartSession`,
takes an optional grant. The request field is `mcp`: `[{"connection": "<id>",
"tools": {"tool": "allow"|"propose"}}]`, absent or empty meaning none, added with
`omitempty` so the idempotency request hash of existing retries does not change. If
`tools` is omitted for an entry, the connection's `default_tools` apply.

Each `tools` entry names a tool and the mode, and carries the **hash the client saw**
(6.3). The runner validates before starting that every connection belongs to the caller
(the core list call), the proof is live, `engine` is claude and `kind` is `agent`, and
that every selected tool's hash equals the hash in a live upstream `tools/list` at that
moment, so a tool that changed after the person confirmed it is refused. A tool omitted
from `tools` takes its mode and hash from the connection's `default_tools`; a named tool
with no hash is refused.

**Only a human principal may attach a grant through a public route.** The grant is a
non-JSON field (`json:"-"`) on the internal start request, so it can never arrive in a
request body; only the public human route (`Kind = human` with a live `Sid`) resolves an
`mcp` field into one. The v1 `/api/runner/start`, the runner's own MCP `start_session`
tool and any agent-kind principal cannot attach a grant, and a body containing `mcp` on
those routes is refused. The scheduler sets the grant in process from the stored cron.
The five spawn paths (`POST /api/sessions` cluster repo, cluster no-repo and daemon, and
`StartSession` cluster and daemon) each either attach the grant or refuse; a test
enumerates them.

### 6.2 Launch sheet

An "MCP servers" section, all unchecked by default. Checking a connection shows its
tools and modes (defaulted from `default_tools`) so they can be narrowed for this
session. The capabilities panel already shows connected MCP servers; it also shows each
tool's mode, read from the grant.

### 6.3 Tool picker and pinning

`GET /api/mcp/connections/{id}/tools` on the runner (human only) calls upstream
`tools/list` with the user's credential and returns each tool's name, description,
schema, annotations and a **hash of description plus input schema**.

- Every tool defaults to `off`. A server's `readOnlyHint` is only shown as a badge and
  enables a one-click "select all read-only tools" action. It is never pre-selected,
  because the server controls the hint.
- `propose` is offered for any tool. `allow` on a tool not marked read-only carries an
  explicit warning ("this can change or send things without asking you").
- The selected mode is stored with the tool's hash. If the upstream later changes a
  tool's description or schema, the gateway treats it as `off` until the person opens
  the picker and re-confirms it. New upstream tools stay `off`.

## 7. Crons (runner)

### 7.1 Data model

Runner migration `021_crons.sql`:

```sql
CREATE TABLE crons (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_account_id text NOT NULL, name text NOT NULL, enabled boolean NOT NULL DEFAULT true,
  schedule text NOT NULL, timezone text NOT NULL,
  prompt text NOT NULL CHECK (length(prompt) <= 16384),
  engine text NOT NULL DEFAULT 'claude' CHECK (engine = 'claude'),
  model text, effort text,
  runtime text NOT NULL DEFAULT 'auto' CHECK (runtime IN ('auto','cluster','docker')),
  daemon_id uuid,                      -- optional pin for the docker runtime (honoured in process only)
  board_id text,                       -- target board (section 10)
  mcp jsonb NOT NULL DEFAULT '[]',
  token_id text NOT NULL, token_expires_at timestamptz NOT NULL,
  grace_seconds int NOT NULL DEFAULT 3600, max_runtime_seconds int NOT NULL DEFAULT 1800,
  next_run_at timestamptz NOT NULL, last_run_at timestamptz,
  consecutive_failures int NOT NULL DEFAULT 0, paused_reason text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE cron_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  cron_id uuid NOT NULL REFERENCES crons(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL, claimed_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz, session_id uuid,
  status text NOT NULL CHECK (status IN ('claimed','started','held','skipped','failed')),
  reason text, late boolean NOT NULL DEFAULT false, manual boolean NOT NULL DEFAULT false
);
CREATE INDEX cron_runs_open ON cron_runs (status) WHERE status IN ('claimed','held');
```

`runtime` has no `daemon` value: bare-host sessions are not allowed for crons
(section 8). The scheduler resolves `auto` itself (cluster when a cluster runtime is
configured, otherwise `docker`) and always sends an explicit `cluster` or `docker` to the
start path, never an empty value, because an empty runtime falls back to the bare host
when no sandbox image exists. The start request gains an in-process-only daemon pin.
Limits: 20 crons per account, minimum interval 15 minutes, prompt 16 KiB, 2 concurrent
cron sessions per account. Retention: `cron_runs` older than 90 days are pruned.

### 7.2 Schedule

Standard five-field expressions plus an IANA time zone, with a picker for common shapes.
A well-known cron library is added to the runner module and its behaviour is pinned by
tests: a wall-clock slot that does not exist on a spring-forward day runs once, at the
first valid time after it; on a fall-back day a slot fires once, at its first occurrence.
Only plain five-field expressions are accepted: `@every`, other `@` descriptors and
`CRON_TZ=`/`TZ=` prefixes are rejected, as is any expression with no next occurrence
(for example 30 February). The 15 minute minimum is checked over a full year of
occurrences (so a constraint like `*/5 * 1 * *` cannot slip through). If the chosen
library does not give the DST behaviour above, the slot iterator is hand-written and
tested.

### 7.3 The scheduler

One loop in the runner server (started in `main.go`), every 30 seconds, holding a
Postgres advisory lock (`pg_try_advisory_lock`) so only one replica schedules; the
runner also runs as a single replica today, and the daemon connection hub is per
replica, so single-scheduler is the supported shape.

Each tick:

1. **Claim.** In one transaction, select due crons (`enabled`, not paused, `next_run_at
   <= now()`) `FOR UPDATE SKIP LOCKED`, set `next_run_at` to the next slot strictly after
   now (collapsing missed slots), and insert a `cron_runs` row with status `claimed`,
   marked `late` when the claim is more than a minute after the slot.
2. **Start** each claimed run outside the transaction (7.5): `started` on success.
3. **Reap.** Any `claimed` row older than a minute (and younger than 23 hours) is
   re-driven with the same idempotency key, which is `cron:<cron id>:<run id>` so a token
   renewal cannot change its scope. If the key exists but no session row does (a crash
   between the two), the key is released and the start retried once. This makes a crash
   between claim and start neither lose nor duplicate a run. Older claims become
   `failed`.
4. **Retry held.** A `held` row is retried each tick. Its grace window is measured from
   the **later of the slot and runner start-up time**, so a machine that was off longer
   than the grace window still gets its one catch-up run when the daemon reconnects. When
   the window ends the row becomes `skipped` with the reason. A slot that arrives while
   an earlier run of the same cron is still held is skipped (`previous run still
   active`).

Rules at start:

- **Overlap:** a slot is skipped if the cron's previous run's session is still active.
- **Capacity is checked before anything is written.** A pre-flight capacity check (cluster
  cap, connected daemon) runs before any session row or idempotency key exists and
  returns a typed error, so a held run retried every tick creates no session rows,
  webhooks or errored sessions. A retry that does start uses a fresh session id.
- **Typed failures:** the start path returns typed errors, so the scheduler can tell
  `capacity` (cluster cap, no daemon connected: `held`) from `credential` (personal
  credential fetch not successful, connection missing or needing sign-in: `failed`)
  from other failures (`failed`).
- **Repeated failure:** three failures in a row set `paused_reason`.
- **Edits:** changing the schedule, timezone or resuming a paused cron recomputes
  `next_run_at` from now, so it does not fire immediately.
- **Run now:** a manual run uses the key `cron:<id>:manual:<uuid>` and `manual = true`.

### 7.4 Identity: the cron token

An unattended run has no logged-in human. When a cron is created, the runner calls core's
`/internal/tokens/mint` with the creating human's login session id as proof; core
creates a `run-sessions` agent token row for that account and returns only its id and
expiry. **No signed token is ever produced or stored.** The scheduler acts as the
in-process runner principal `{Kind: agent, Sub: token_id, OnBehalfOf: owner}` after
confirming liveness with `/internal/tokens/status`, and that id is the proof it gives
core for every later credential, connection and exchange call.

- Deleting a cron revokes its token through `/internal/tokens/revoke`. So does pausing
  it. Revoking it in Settings, or "log out everywhere" (which revokes all of an
  account's agent tokens), makes the next status check fail and the cron pauses with the
  reason `access revoked`.
- Tokens expire after at most 365 days. The UI shows the expiry and offers renew, which
  mints a replacement. An expired cron pauses.
- The token carries only `session.start`. It counts toward the 50 live agent tokens per
  account, which the 20-cron limit leaves room for.
- **Cluster runs never fall back to the shared operator credential.** The existing
  fallback (a personal credential fetch that returns nothing, for any reason, quietly
  uses the shared Secret) is disabled for cron sessions by an explicit job-spec flag: a
  start that does not receive the personal credential fails with a typed credential
  error and creates no Job, so a revoked token can never run on the operator's account.
- **On the Docker sandbox path the daemon does not call core.** The scheduler's status
  check and the gateway's own core calls are the liveness gates. That session also uses
  the **host developer's own Claude login** (the sandbox mounts it), not the cron owner's
  credential; the cron form states this.

### 7.5 Starting the session

The scheduler calls the shared start function with `RunnerStartRequest` fields:
`no_repo`, `auto_stop`, `engine = claude`, model and effort from the cron, a title of
the cron name and slot time, the resolved runtime, the grant from the cron's `mcp`
(plus the built-in board connection, 10.2), and the disallowed-tool list from
section 8. `auto_stop` ends the session after its first finished turn, so a run is one
agent turn. A **watchdog** (new work) stops a run that exceeds `max_runtime_seconds`.

### 7.6 Lifecycle

- Deleting, pausing or revoking a cron stops any running session it started and deletes
  its grants and gateway tokens.
- Deleting a connection takes effect in the runner through two mechanisms, because core
  never calls the runner: the gateway re-fetches credentials at least every 5 minutes
  and each fetch checks the connection exists, and an hourly runner sweeper lists
  connections from core and removes orphaned grants and marks matching pending proposals
  `failed`. The runner also checks each cron's connection ids against core at fire time;
  a missing one fails that run with a visible reason.
- Cron sessions are ordinary sessions for streaming and transcripts, and carry a "cron"
  badge.

### 7.7 API and UI

Routes require a human principal **with a live login session** (`Kind = human` and a
non-empty `Sid`), because the runner's existing browser gate accepts any token with
`session.start`, including agent tokens: `GET/POST /api/crons`,
`GET/PATCH/DELETE /api/crons/{id}`, `POST /api/crons/{id}/run`,
`POST /api/crons/{id}/renew`, `GET /api/crons/{id}/runs`. Another account's cron answers
`404`. The runner's own tool-facing MCP surface has no cron tools.

The runner frontend gets a `/crons` page: list (name, schedule in words, next and last
run, status, enabled toggle, paused reason), a create and edit form (name, schedule
picker, timezone, prompt, model, runtime, target board and an MCP section with the tool
picker where every mode is chosen explicitly), "Run now", and run history with a "ran
late" marker.

## 8. Containing an unattended agent

Cron sessions, and any session with a grant, are more dangerous than an interactive
one: they read untrusted text and nobody is watching. These controls are part of the
feature, not optional hardening.

- **No bare-host runs.** A cron never runs on a bare-host daemon session, which is a
  full shell as the developer with their keys and credentials. It runs as a cluster pod
  or in the Docker sandbox. A request that would otherwise put a grant on a bare-host
  session is refused.
- **Tools.** Sessions with a grant are launched with a built-in tool **allow-list**
  (`--tools`, naming only safe file tools) plus their MCP tools, rather than a deny-list
  that would miss agent-spawning, monitoring and future built-ins. The list is a
  documented constant chosen from what the pinned Claude Code version reports (spike,
  section 14), and its exact flag form (`--tools` or a deny-list) is whatever the spike
  proves is enforced under `--dangerously-skip-permissions`. The runner server sets the
  list in the spawn message and the per-session Secret; the daemon does not accept an
  override from environment variables. Variadic flags are passed in `--flag=value` form
  so they cannot swallow the prompt. Per-tool selection for anything more is a
  follow-up.
- **File tools are path-scoped.** The file tools run as the same user as the runner, so an
  unrestricted `Read` could open the gateway config (bearer tokens), `/proc/<pid>/environ`
  (the pod's own environment, including the runner's credentials) or the engine login, and
  copy them into a card through an allowed board tool; an unrestricted `Write` could
  overwrite the host login a Docker sandbox mounts. A restricted turn therefore also carries
  `--disallowedTools=Read(...),Edit(...)` path rules: `/proc`, `/etc`, mounted service-account
  secrets, the runner's own `blerg-mcp-*` temporary directories (and the exact directory of
  this session's config), `~/.claude`, `~/.claude.json`, `~/.config/claude`, and the usual
  credential dotfiles (`~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.netrc`, `~/.git-credentials`). Proven
  on the pinned Claude Code 2.1.284 under `--dangerously-skip-permissions`, `--tools` and an empty
  `--setting-sources`: the deny holds for Read, Grep and Glob (a recursive search skips the denied
  tree), for Write and Edit (both fall under an Edit rule), through a symlink, through `..`, with
  `~`, and with a wildcarded directory name, while the session working directory stays usable. An
  absolute path in a rule needs the `//` prefix. Residual risk: enforcement is Claude Code's
  permission engine (the version is pinned and the behaviour must be re-proved on an upgrade, see
  the unit test of the rule list and `ccPathDenyRules`); the agent's own uid can still read these
  files, so a bug in that engine would expose them. Neither the mount of the claude login into a
  Docker sandbox (it must stay writable, the engine keeps its transcripts there) nor the uid can be
  changed cheaply; the pod's daemon token is also still delivered as an environment variable (the
  server accepts no per-session credential on the daemon channel yet, so it is the master
  daemon token), which the `/proc` rule keeps out of reach of the file tools. A restricted
  pod also receives no codex or hermes secrets, and a restricted sandbox mounts only the claude
  login.
- **Egress.** The Kubernetes install documents and ships a default-deny egress policy
  for session pods that allows only DNS, the runner and core/board in-cluster, and it is
  recommended whenever crons or grants are used. The desktop sandbox network already
  limits reach.
- **Sessions are private.** Any session with a grant, and every cron-started session, has
  a `private` flag and is visible only to the account that started it. A single central
  predicate decides visibility with no human bypass for private rows, and is applied to
  every surface that names a session or emits its data: the session list, the browser
  WebSocket broadcast (status, titles, agent events, terminal output, push
  notifications), per-session read, patch and delete routes, agent events, capabilities,
  messages, screenshots and previews, board events and completion webhooks. A test
  enumerates every registered route that takes a session id. This lands before any grant
  or cron session can exist. The runner's current behaviour, where every human on an
  install can read every session, is unchanged for other sessions but is not acceptable
  for mail content.
- **Daemons are not per-account.** The runner cannot bind a daemon to an account (the
  daemon token is shared). For Docker-sandbox crons on a desktop the trust boundary is
  the existing documented one: every account on an install can use every connected
  daemon, and the supported desktop shape is one person per install. The cron form
  says so, and lets the owner pin a daemon.
- **Residual risk.** A hostile message can still make the agent create misleading cards
  or proposals, or read other data reachable through an allowed MCP tool and place it in
  a card or proposal. The board gate, the frozen arguments shown at approval and the
  narrow tool set are the controls.

## 9. Proposals

```sql
CREATE TABLE mcp_proposals (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id text NOT NULL, session_id uuid, cron_id uuid,
  connection_id uuid NOT NULL, connection_name text NOT NULL,
  url_snapshot text NOT NULL, tool text NOT NULL, tool_hash text NOT NULL,
  arguments jsonb NOT NULL CHECK (pg_column_size(arguments) <= 65536),
  agent_summary text,                          -- the agent's own words; shown as such
  state text NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','executing','done','rejected','expired','failed','unknown')),
  result jsonb, created_at timestamptz NOT NULL DEFAULT now(),
  decided_at timestamptz, decided_by text
);
```

The **frozen arguments are the authoritative record** and are what the UI displays; the
agent's summary is shown separately and labelled as the agent's description.

- The agent's tool result contains the proposal id and its runner URL, generated by the
  gateway. The agent puts that link in a card. The runner never renders an
  agent-supplied URL.
- Approve and reject are human-only (`Kind = human`, live `Sid`), owner-only, and
  same-origin protected. Approve compare-and-sets `pending → executing` atomically so a
  double click or a retry cannot run it twice.
- Execution snapshot check: the connection must still exist with the same URL as
  `url_snapshot`, and the tool's current hash must equal `tool_hash`; otherwise the
  approval is refused with an explanation.
- The gateway then fetches a fresh credential with the approver's login as proof and
  calls the upstream tool with exactly the stored arguments. No agent is involved. On
  success `done`; on a definite failure `failed`; on an ambiguous outcome (timeout after
  the request may have been delivered) `unknown`, which needs a human decision and is
  never retried automatically.
- Pending proposals expire after 7 days (a daily job). Retention: decided proposals are
  pruned after 30 days.
- UI: a "Proposals" list in the runner frontend and a count badge in the sidebar.

## 10. The board

### 10.1 What the agent does

The agent uses the board's MCP server. Its cards go through the board's admission gate
(agent tokens are not exempt). The starter prompt tells it to set a stable `external_id`
per source item (for example a mail thread id) so a rerun updates a card and does not
duplicate it.

### 10.2 Access without a stored board token

The board is offered as a built-in connection named `board`, through the same gateway.
The gateway calls `/internal/tokens/exchange` for a short-lived (10 minute) agent token
with the board audience and the `card.read`, `card.write`, `column.write` caps
(intersected with the account's role), re-exchanging as it nears expiry, and revokes it
(a `sub` revocation) when the session ends. For crons the built-in connection exposes
only these board tools, all in mode `allow` because board writes are already gated by
the admission gate: `blerg_board_get`, `blerg_board_list`, `blerg_board_schema`,
`blerg_column_list`, `blerg_card_search`, `blerg_card_get`, `blerg_card_create`,
`blerg_card_update`, `blerg_card_move`, `blerg_card_comment`, `blerg_card_link`. Archive,
column create and move, and board create and update are excluded. This is the one place
a connection is enabled without the person ticking each tool, and the cron form shows
the list.

**Scoping to one board needs a board change.** Today a core-issued token in the board
carries no board scope and reaches every board. The exchange token therefore carries
the target board id in its `Project` claim, and the board maps `Project` to a board
scope for core-issued agent tokens, so a token minted for one board cannot read or write
another. The board's sensitive-capability staleness rule applies to it like any other
core token. The cron's only board-related stored value is the board id.

### 10.3 Focus board template

A one-click template creates columns Inbox, Today, This week, Waiting on, Someday,
Proposed and Done, and a field schema with `source` (enum: email, calendar, manual),
`due` (timestamp) and `tracking` (text). A starter cron prompt files items into those
columns, keeps `external_id` per source item, leaves cards a person has moved, and uses
the propose path for any outward action. The person asks for new tracking by editing the
prompt, or by chatting with an interactive session that has the board connection.

### 10.4 Visible failure

A run that cannot start (connection needs sign-in, token expired, board unreachable)
records a failed run, and when a target board exists the runner posts one card
("Cron `<name>` could not run: `<reason>`") with a fixed `external_id`, updated in place
on repeats.

## 11. Operations and conventions

- **Desktop.** The scheduler runs in the runner container; state is in the runner
  database, which persists. When the stack is stopped nothing runs. When it starts,
  overdue crons fire once, flagged late, once a daemon is connected (7.3).
- **Cost limits.** One turn per run, `max_runtime_seconds`, the call budget, and a
  per-account limit of 2 concurrent cron sessions.
- **Audit.** `mcp_call_log`, the core secret access log and `cron_runs`.
- **Configuration** (all documented in the README env table, `.env.example` and the
  configmap comments): the four allow-list variables (4.6), the gateway limits
  (concurrency, call budget, result cap), the cron limits, and the retention periods.
- **Documentation.** The new routes are human-only, so they are **not** added to either
  `openapi.json` (the runner's is checked against the agent-contract mux and documents
  what agents may call); they are documented in the README. Each route group gets its own
  table test that registers the real routes and asserts no token gives 401, an agent
  token 403, another account's object 404, and, for state-changing core routes, the
  same-origin check. A `CHANGELOG.md` entry; docs stay generic and do not name a
  particular third-party service or a personal setup.
- **Code standards.** All outbound HTTP (gateway, OAuth, tool listing) is context bound
  and passes `noctx`, `bodyclose` and `gosec`.
- **Scope.** The runner's stated aim is local agent management. This adds a gateway, an
  OAuth client, a scheduler, proposals and a board template, and is therefore delivered
  in the phases below, each usable alone.

## 12. Testing

- Unit: cron expression next-time across DST and the minimum-interval check; scheduler
  claim, catch-up collapse, reaper and held retry; gateway method allow-list, batch
  rejection, tool filtering, pin mismatch, mode enforcement, result stripping and size
  caps; proposal freeze, state machine and snapshot checks; URL policy (private ranges,
  rebind, IPv4-mapped IPv6, redirects); header and value validation; token exchange
  scoping; grant validation; OAuth state, PKCE, `iss` and refresh locking.
- Integration (Postgres-backed harness): two schedulers, one firing; a crash between
  claim and start neither loses nor duplicates a run; a revoked cron token stops a run
  and credential access and never falls back to the operator credential; another
  account's connection is refused; `propose` never reaches a fake upstream and `allow`
  does; an agent token cannot create crons, approve proposals or attach a grant; other
  accounts cannot read a cron session; deleting a connection invalidates its grants.
- A fake MCP server (Streamable HTTP, JSON and event-stream replies, an OAuth variant).
- Frontend: launch-sheet MCP section, picker defaults and pinning UI, crons form and
  list, proposals list.
- Manual: a real Claude session with a granted fake server on the cluster and on the
  desktop sandbox.

## 13. Delivery phases

1. **Connections (static) and gateway.** Core connections API and settings UI, shared
   network policy package, runner gateway, grants, config injection, daemon capability
   and protocol field, launch-sheet section, tool picker and pinning, tool restrictions
   and private sessions (section 8).
2. **Crons.** Tables, scheduler, cron token endpoints, start-path typed errors, API and
   UI, run history, watchdog, catch-up.
3. **Proposals.**
4. **Board integration and template.** Exchange tokens, the board's board-scoping,
   built-in `board` connection, focus-board template, failure card.
5. **OAuth exchange.** Discovery, registration, PKCE, refresh, `needs_auth`.

Phase 5 is last because it is the most self-contained, but the mail and calendar case
depends on it (or on an MCP server that accepts a static token), so it should not slip
far behind phase 4.

## 14. Items to verify first (a blocking spike before phase 1 builds on them)

1. Claude Code at the version the pod image and sandbox pin accepts `--mcp-config <file>`
   with `type: "http"` and headers; `--strict-mcp-config` removes ambient servers,
   including those in the `~/.claude.json` the sandbox mounts; and either `--tools` (an
   allow-list) or `--disallowedTools` really prevents the tool under
   `--dangerously-skip-permissions`. If neither holds, cron sessions run with a
   permissions mode that enforces it. The spike also records the exact built-in tool
   names the init event reports.
2. Streamable HTTP details the gateway must proxy: session ids and event-stream replies.
3. Whether the intended upstream servers accept a bearer header and speak Streamable
   HTTP.
4. That the board can scope a core agent token by `Project` (10.2).
5. Whether all disallowed built-in tool names are stable across Claude Code versions.

## 15. Decisions taken on the requester's behalf

- A gateway enforces tool modes, rather than client-side flags, keeping the same
  user-visible behaviour (section 3).
- Approval executes the frozen call; no agent runs (section 9).
- Crons and connections are human-only, and crons never run on a bare-host daemon.
- Cron sessions are private to their owner and run without shell or web tools.
- Limits: 20 crons per account, 15 minute minimum interval, 30 minute run time.
