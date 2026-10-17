# MCP tool changes: review in place, trust policy, audit — design

**Status:** reviewed, 2026-10-13 (v3: security + feasibility reviews, then a confirmation pass; §13 records what
changed). Extends the MCP gateway design in [`ai-crons.md`](ai-crons.md) §5 (the gateway), §6.3 (tool
picker and pinning) and §9 (proposals), and the user docs [`mcp-connections.md`](../mcp-connections.md)
and [`proposals.md`](../proposals.md). "Crons §n" below refers to `ai-crons.md`.

## 1. The job

Today a session's MCP tools are **pinned at launch**: each selected tool is recorded with a hash of
its name, description and input schema (Crons §6.3). When the upstream server later changes a
definition, the gateway hides the tool from `tools/list` and refuses `tools/call` with "start a new
session and select it again". A resume copies the old grant verbatim. Nothing is shown to the person.

The pin is right: it is the defence against a server quietly changing what an approved tool does
(tool poisoning). The cost is wrong: one vendor release drops a tool from every running session of
every account at once, and the only remedy is a relaunch. Teams on third-party MCP servers meet this
in their first week.

This design keeps the pin and adds three things:

1. **Review in place.** A changed definition, or a tool the launch never saw, becomes a *tool
   change* the person reviews next to their proposals: before and after, accept or keep off.
   Accepting re-pins the session's grant; the agent is asked to re-list and gets the tool back at
   its next turn boundary. No relaunch.
2. **A per-connection trust policy.** `strict` (default) is the above. `auto_accept` re-pins changed
   tools and grants new ones by the session's recorded preset, so an organization's own servers
   never interrupt anyone. The connection's owner sets it; Blerg has no admin role.
3. **Audit.** Every pin event — accepted by a person, auto-accepted, declined, expired — is a row
   the person can read on the session and on the connection; the call log's "changed" refusals get
   their own outcome.

### Decisions already taken

- Auto-accept covers **new** tools as well as changed ones, bounded by the preset the grant recorded
  at launch: `allow_all` → new tools are allowed; `approval` → new tools are **propose** (the
  server's `readOnlyHint` is never trusted to widen that); `custom` → nothing is granted
  automatically, a change is recorded for the person.
- Only the **launching account** reviews a session's changes, as for proposals and resume.
- A tool with a pending change **stays off** until decided.
- The change carries the **exact definition that was hashed**; the person judges the real text,
  never the agent's description of it, and never a truncated copy.
- Out of scope: an organization-wide admin role; diffs of annotations (not hashed, not served to
  the agent); an account-level "keep this tool off on this connection" (noted for later).

### What the agent actually sees (verified)

The gateway advertises `listChanged: false` and has no push channel. Claude Code lists tools once,
when its process starts; with mid-turn steering on, that process is long-lived (it closes after ten
idle minutes and restarts with `--resume`). So a re-pin alone does **not** reach a running agent:
it would keep calling with the definition it cached, or never see a newly granted tool.

The daemon already restarts the Claude process at the next idle point when the model or effort
changes (`markStale` in the Claude Code driver). This design reuses that: after any re-pin or grant
the runner sends the session's daemon a `refresh_session_tools` message; the driver marks the
process stale, it closes at its next idle point (no turn running, nothing queued; now, if idle),
and the next message starts a `--resume` process that lists tools afresh. Until that happens the
gateway **refuses calls to a re-pinned tool** ("list tools again before calling it"), so the agent
can never call a tool whose new text it has not listed. Other engines, and an older desktop
daemon, ignore the message; their tools refresh on resume.

Because such an agent lists only at process start, **drift is usually first seen on a
`tools/call`**, not on a list. The gateway therefore records changes on both paths, and under
`auto_accept` a call that meets drift triggers the refresh so that the restarted process's first
`tools/list` performs the re-pin (§4.1).

### Constraints this design lives under

- The gateway re-reads the grant row on every request (`MCPGrantByTokenHash`), so an in-place
  change needs no cache invalidation. Its per-grant in-memory state caches only name→hash with a
  10-second TTL, is keyed by token hash (empty after every resume), and is dropped after an idle
  hour; it cannot be relied on to remember definitions.
- A grant is one row per `(session, connection)` with a `tools` JSON map `{name: {mode, hash}}`.
  The existing replace helper (`storeMCPGrants`) deletes and re-inserts with a **new token hash**;
  it is for launch and resume only, never for an in-place change.
- Hashes are canonical SHA-256 over name, description and input schema (`mcpgw/hash.go`); the
  picker, the launch check (`resolveGrantProof`, 409 on a stale hash) and the gateway all agree.
- Core holds connections and never calls the runner; the runner fetches a connection's credential
  from core before any upstream request and caches it for at most five minutes.
- Proposals (Crons §9) are the existing "an agent asked, a person decides" pattern: owner-only
  page, human-only routes proven by the login session, badge, 7-day expiry, caps per session and
  account. Tool changes reuse the shape and the maintenance ticker, not the table.
- A changed-tool refusal is **already logged** today with outcome `refused`; nothing is silent.

## 2. Architecture

```
upstream MCP server ──tools/list──▶ runner gateway (per request: grant row, upstream listing with definitions)
                                        │ pin matches ─▶ listed / forwarded (as today)
                                        │ pin differs ─▶ hidden / refused
                                        │     strict:      record mcp_tool_changes(kind=changed) once; definition stored by hash
                                        │     auto_accept: re-pin on tools/list (CAS) + pin event, serve the new text; calls refused until listed
                                        │ name not in grant.known_tools ─▶ hidden
                                        │     strict / custom preset: record mcp_tool_changes(kind=new)
                                        │     auto_accept + allow_all|approval: grant (CAS) + pin event
                                        │ name in known_tools but not granted ─▶ off, nothing recorded (the person left it off)
                                        ▼
runner UI: Proposals page gains "Tool changes" (shared badge)
   accept   ─▶ POST /api/tool-changes/{id}/accept  ─▶ CAS grant update + pin event + refresh_session_tools to the daemon
   keep off ─▶ POST /api/tool-changes/{id}/decline ─▶ pin event; later changes to that tool are recorded muted
core: mcp_connections.pin_policy (strict | auto_accept), set in Settings, returned with the credential to the gateway
```

| Component | Lives | Changes |
|---|---|---|
| Gateway | `runner/internal/mcpgw` | listing returns definitions + completeness; detect/record changes; `auto_accept` on list; "listed" gate on call; outcome `changed` |
| Grants | `runner/internal/db` (`session_mcp_grants`) | `preset`, `known_tools`; CAS single-tool updates; replace carries the new columns |
| Definitions, changes, events | `runner/internal/db` (new tables) | `mcp_tool_definitions`, `mcp_tool_changes`, `mcp_pin_events` |
| Review API + daemon refresh | `runner/internal/server`, `runner/internal/protocol`, `runner/internal/daemon` | owner-only routes; `refresh_session_tools` message → `markStale` |
| Runner UI | `runner/frontend` | Proposals page section; badge; capabilities marker; picker sends the preset; hidden-character rendering |
| Connection policy | `core/internal/mcpconn`, `core/internal/api`, `core/web` | `pin_policy` column, PATCH field, token response, Settings control, audit of flips |
| Launch, resume, crons | `runner/internal/server/{mcpstart,cronstart}.go` | launch records preset + known tools and stores definitions; resume copies; cron fire re-pins under `auto_accept` |
| Docs | `docs/mcp-connections.md`, `docs/proposals.md` | the behavior in the user's words |

## 3. Data model

Runner migration `039_mcp_tool_changes.sql`; core migration `019_mcp_pin_policy.sql` (idempotent,
`ADD COLUMN IF NOT EXISTS`, as the core convention).

### 3.1 Grants: preset and known tools

```sql
ALTER TABLE session_mcp_grants
  ADD COLUMN preset      text  NOT NULL DEFAULT 'custom' CHECK (preset IN ('allow_all','approval','custom')),
  ADD COLUMN known_tools jsonb;   -- ["name", …] every tool the launch listing contained; NULL = unknown (pre-migration grant)
```

- **`preset`** is what `auto_accept` uses for a tool the launch never saw. The picker sends the
  preset button last clicked per connection (cleared by any per-tool edit; never sent with
  `requireExplicit`, i.e. crons and saved defaults). The server **re-derives** the preset from the
  selection against the live listing *including the tools left off*, and stores `custom` when they
  disagree. An older client that sends nothing gets `custom`. (`presetOf` in `mcpSelection.ts`
  works backwards from the modes and reads "Require approval" as `allow_all` on an all-read-only
  server; it is not the source of truth.)
- **`known_tools`** is the full set of names the launch listing contained. The grant map keeps only
  `allow`/`propose` entries, so without it a tool the person deliberately left off is
  indistinguishable from a new one. `new` means "not in `known_tools`". A grant with `NULL` never
  records `new`: grants created before this migration, and grants whose launch listing was
  **incomplete** (the picker's `ListTools` stops at ten pages while the gateway lists up to a
  hundred; a tool past the launch window must not look new).
- A grant map entry becomes `{mode, hash, listed?}`. In Go, `MCPGrantTool` gains
  `Listed *bool \`json:"listed,omitempty"\``: absent means listed; `false` is set by a re-pin or an
  auto-grant and cleared by the first `tools/list` that serves that exact hash. The field is never
  set on a connection's `default_tools` (core decodes those with `DisallowUnknownFields`).
- `storeMCPGrants` (launch, resume) carries `tools`, `preset` and `known_tools` from the rows it
  deletes (`DELETE … RETURNING`) the same way it carries `calls_used` today, so a resume cannot
  overwrite an accept that landed between its read and its write. The carried `tools` travels as
  raw jsonb (`[]byte`), never through `map[string]MCPGrantTool`, so no field is lost.
- Three single-tool writes, all compare-and-set, all used only in place, all with the tool name
  passed as a bound `ARRAY[$tool]::text[]` path (names are upstream-controlled and may contain
  commas, quotes or braces):
  - `RepinGrantTool(session, connection, tool, oldHash, newHash, mode?)`:
    `UPDATE … SET tools = jsonb_set(tools, ARRAY[$tool], {mode, hash: new, listed: false})
    WHERE tools->$tool->>'hash' = oldHash`. `mode` may only tighten (`allow` → `propose`); absent
    keeps the pinned mode. Zero rows means someone else moved it; the caller gives up for this
    request.
  - `GrantTool(session, connection, tool, hash, mode)`: `… WHERE NOT tools ? $tool` — it can never
    turn an existing `propose` into `allow`.
  - `MarkListed(session, connection, {name: hash})` clears `listed` only
    `WHERE tools->$name->>'hash' = $hashServed`, so a list that served B can never mark C as
    listed when a re-pin to C landed in between.

### 3.2 Definitions by hash (new table)

```sql
CREATE TABLE mcp_tool_definitions (
  hash       text PRIMARY KEY,         -- the pin hash
  name       text NOT NULL,
  canonical  text NOT NULL,            -- the exact canonical JSON bytes the hash was computed over
  size       integer NOT NULL,
  unsafe     text,                     -- NULL, or why it cannot be reviewed: 'hidden_chars' | 'nul' | 'too_large'
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen  timestamptz NOT NULL DEFAULT now()
);
```

- `canonical` is the byte string `hash.go` already builds: a JSON object with sorted keys
  `description`, `inputSchema`, `name`, so the UI parses it back losslessly. An input schema that
  is not JSON is carried as a JSON string and shown as text. It is `text`, not `jsonb`: jsonb
  rewrites numbers and drops duplicate keys, so a stored jsonb copy need not be the text that was
  hashed. The accept handler checks `sha256(canonical) == new_hash` and refuses otherwise; that
  rule, not the live re-list, is what ties "what the person saw" to "what gets pinned".
- **Bounded writes.** Definitions are written (`INSERT … ON CONFLICT (hash) DO UPDATE SET
  last_seen = now()` at most once a day per hash) for (a) every tool a **launch or picker** listing
  shows, and (b) a **drifted hash only when a change row, re-pin or grant is recorded** for it, so
  the gateway's hot path writes nothing for an unchanged listing and a hostile server's writes are
  bounded by the §3.3 caps. Repeat hashes are skipped through the gateway's in-memory recorded set.
  The `old_definition` of a change is then a lookup on the pinned hash: exactly the text the person
  confirmed in the picker, available after restarts, resumes and the idle sweep. A grant pinned
  before migration 039 has no stored old definition; the card says so.
- **Never truncated.** A canonical form over **256 KiB** is stored as `unsafe='too_large'` with an
  empty `canonical`; a definition whose decoded strings contain a NUL is `unsafe='nul'` (the
  encoder escapes it as `\u0000`, so the check runs on decoded text); one containing Unicode tag
  characters (U+E0000–E007F) or bidi controls (U+202A–202E, U+2066–2069) is `unsafe='hidden_chars'`.
  An unsafe definition is **off** in the gateway and in the picker, is never re-pinned or granted by
  any policy, and a change that points at it has no Accept button ("this definition cannot be
  reviewed").
- Pruned by the maintenance ticker when `last_seen` is older than 180 days and no grant, pending
  change or pin event references the hash.

### 3.3 Tool changes (new table)

```sql
CREATE TABLE mcp_tool_changes (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id      text NOT NULL,
  session_id      uuid NOT NULL,
  connection_id   uuid NOT NULL,
  connection_name text NOT NULL,
  tool            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('changed','new','removed')),
  old_hash        text,                       -- NULL for new; definitions looked up in mcp_tool_definitions
  new_hash        text,                       -- NULL for removed
  mode            text NOT NULL DEFAULT 'propose' CHECK (mode IN ('allow','propose')),  -- what accept will set; changed keeps the pinned mode, new defaults to propose
  muted           boolean NOT NULL DEFAULT false,  -- recorded after a decline of this tool in this session: no badge
  state           text NOT NULL DEFAULT 'pending'
    CHECK (state IN ('pending','accepted','declined','superseded','expired')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,                -- set on every leaving of 'pending', supersede and expiry included
  decided_by      text                        -- account id, 'policy:auto_accept', 'system:superseded', 'system:expired'
);
CREATE UNIQUE INDEX mcp_tool_changes_pending
  ON mcp_tool_changes (session_id, connection_id, tool) WHERE state = 'pending';
CREATE INDEX mcp_tool_changes_account ON mcp_tool_changes (account_id, state, created_at DESC);
CREATE INDEX mcp_tool_changes_session ON mcp_tool_changes (session_id, created_at DESC);
```

Lifecycle rules (`RecordToolChange`, one transaction, idempotent):

- At most **one pending row per (session, connection, tool)**, enforced by the partial unique
  index. Recording runs as: supersede any pending row for the tool whose `(kind, new_hash)`
  differs (`decided_by='system:superseded'`, `decided_at=now()`), then
  `INSERT … ON CONFLICT DO NOTHING`. Recording the hash of an already pending row inserts nothing.
  A hash that was superseded earlier can be opened again (A→B→A ends with A pending and B
  superseded). Two concurrent recordings of different hashes (a grant allows four concurrent
  requests) leave one of them pending; the next request supersedes it. The in-memory recorded set
  is updated only when the row was inserted or was already pending with that hash.
- When the live hash **returns to the pinned one**, the pending row is superseded; the tool is
  simply on again.
- A hash that the person **declined** in this session is never opened again; any *other* new hash
  for that tool in that session is recorded with `muted=true` (listed, no badge).
- `removed` (a pinned tool the server no longer offers in a **complete** listing) is informational:
  one pending row, no buttons, superseded when the tool reappears.
- A pending row **expires after 7 days** (`state='expired'`, `decided_by='system:expired'`); the
  tool stays off, the refusal text reverts to today's "start a new session", and **that hash is not
  reopened** in this session (a later different hash opens a normal row). Decided rows are pruned
  30 days after `decided_at`.
- **Caps:** 50 pending per session, 200 per account (higher than proposals' 10 and 50, because one
  server release can add many tools). Past the cap nothing more is recorded and the page says so
  for that session. The gateway keeps a per-grant in-memory set of `(tool, hash)` it already
  recorded so repeat drift costs no write.

### 3.4 Pin events (new table)

```sql
CREATE TABLE mcp_pin_events (
  id              bigserial PRIMARY KEY,
  account_id      text NOT NULL,
  session_id      uuid NOT NULL,
  connection_id   uuid NOT NULL,
  connection_name text NOT NULL,
  tool            text NOT NULL,
  action          text NOT NULL CHECK (action IN ('repinned','granted','declined','expired','throttled')),
  old_hash        text,
  new_hash        text,
  mode            text,
  actor           text NOT NULL,              -- account id, 'policy:auto_accept', 'system'
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_pin_events_session    ON mcp_pin_events (session_id, created_at DESC);
CREATE INDEX mcp_pin_events_connection ON mcp_pin_events (account_id, connection_id, created_at DESC);
```

Kept 180 days. Never holds definitions or arguments. A pin event is written **in the same
transaction** as the grant write it records; a re-pin without its event cannot happen.

### 3.5 Connection policy (core)

```sql
ALTER TABLE mcp_connections ADD COLUMN IF NOT EXISTS pin_policy text NOT NULL DEFAULT 'strict'
  CHECK (pin_policy IN ('strict','auto_accept'));
```

`Connection` (the public view), `PatchInput` (with the "nothing to change" guard extended), the
internal **token response** (`/internal/mcp/connections/token`, through `SecretResult` in both the
static and OAuth branches of `FetchSecret`) and the internal **list response**
(`internalMCPConnection`, decoded into the runner's `MCPConnection.PinPolicy`, fail closed to
`strict`) gain `pin_policy`. Core has no general audit table and `mcp_secret_access_log` records
only credential hand-outs, so the same migration adds

```sql
CREATE TABLE IF NOT EXISTS mcp_connection_events (
  id            bigserial PRIMARY KEY,
  account_id    uuid NOT NULL,
  connection_id uuid NOT NULL,
  action        text NOT NULL,        -- 'pin_policy'
  from_value    text,
  to_value      text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
```

A flip writes one row inside `Patch`'s existing transaction; the rows are pruned with the access
log (180 days) and shown on the connection in Settings. The built-in `board` connection is not
pinned and has no policy.

### 3.6 How the gateway learns the policy

It rides the credential: `mcpgw.Credential` gains `PinPolicy`, filled from the token response and
cached with it (at most five minutes). **Fail closed:** core unreachable, field missing (older
core) or an unknown value all mean `strict`. The policy is only consulted when drift is detected, so
this costs nothing on the normal path, and a tightened policy bites within five minutes on every
replica, with no dependency on crons or the scheduler lock. There is no policy column on the grant.

## 4. Gateway behavior

`listUpstream` returns, per tool, the definition and its hash, plus whether the listing was
**complete** (today it knows but does not say); it writes `mcp_tool_definitions`. A name the upstream
lists more than once is **off** and is never recorded, re-pinned or granted (today it hashes to
`""`). All of the below runs after the existing pin comparison; nothing before it changes.

### 4.1 A pinned tool whose hash changed

Drift is detected on **both** paths: `tools/list` (the listing) and `tools/call` (`currentHash`,
which for a long-lived agent is where drift is first met). Both record; only the list re-pins.

- **strict** — `tools/list` hides it; `tools/call` refuses with the neutral text
  `tool <name> is unavailable in this session because its definition changed; continue without it
  and do not ask the person to enable it`
  and logs the call with outcome **`changed`** (today: `refused`). Either path records a `changed`
  row (once per hash, §3.3 rules) and stores the drifted definition (§3.2). After the row expires
  the text reverts to today's "start a new session and select it again".
- **auto_accept** — on `tools/list`: `RepinGrantTool` (CAS on the old hash) + pin event
  `repinned` (actor `policy:auto_accept`) in one transaction; the entry is served with the new text
  and `listed:false` is set, then cleared by `MarkListed` for the `{name: hash}` pairs this response
  contains. If the write fails or affects zero rows the tool is hidden for this request (fail
  closed). On `tools/call` with a changed hash the gateway never re-pins: it refuses with
  `tool <name>'s definition changed; list tools again before calling it`, outcome `changed`, and
  asks the runner to send `refresh_session_tools` (§5.3) so the restarted process's first
  `tools/list` performs the re-pin. The agent therefore always lists the new text before it can
  call it. The gateway reaches the hub through a `Refresher` hook the server installs at start-up
  (as it installs `SetProposalSink`); the refresh is rate-limited to one per grant per minute.
- **Throttle.** Under `auto_accept` a tool is re-pinned at most **3 times per hour** per grant. Past
  that it is treated as `strict` for the rest of the hour, a `changed` row is recorded and a pin
  event `throttled` written, so a server that flips a definition back and forth cannot bury the audit.

### 4.2 A tool not in `known_tools`

- Nothing when `known_tools` is `NULL` (pre-migration grant) or the name **is** in `known_tools`
  (the person left it off; "unknown tool" on call, as today).
- **strict**, any preset — hidden; a `new` row with `mode` = `allow` for `allow_all`, else
  `propose`. The card's mode picker lets the person change it.
- **auto_accept** + `allow_all` — `GrantTool(… mode allow)` + pin event `granted`, served in this
  `tools/list` with `listed` cleared. **auto_accept** + `approval` — the same with mode **propose**,
  whatever the server's `readOnlyHint` says (the hint is server-controlled and not hashed).
  **auto_accept** + `custom` — recorded as a `new` row like strict; a custom selection was
  deliberate and the policy does not widen it.
- Granting happens only on `tools/list`; a `tools/call` to an ungranted name stays "unknown tool".

### 4.3 A pinned tool the server no longer offers

Hidden (as today). Recorded as `removed` only when the listing was **complete**; superseded when
the tool reappears. It exists so the person can see why a tool vanished.

### 4.4 Calls to a tool with `listed:false`

Refused: `tool <name> was re-enabled with a new definition; list tools again before calling it`,
outcome `changed`. This applies to accepts (§5) as well as to `auto_accept`. The first `tools/list`
that serves the entry clears the flag.

### 4.5 Proposals after a re-pin

A propose-mode call creates a proposal frozen at the hash the gateway saw at call time; the
gateway uses the entry's hash **after** the re-pin for new proposals. A proposal frozen at an older
hash fails its approval re-check (409), as today: fail closed.

Recording failures (`mcp_tool_changes`, `mcp_tool_definitions`) are logged and never fail the agent's
request. Grant writes under `auto_accept` are the exception: if they fail the tool is hidden.

## 5. Review API (runner, human only) and the daemon refresh

Routes follow `routes_proposals.go`: a signed-in person, `account_id = caller` on every query,
uniform 404 for anything not theirs, never reachable with an agent token (they are added to the
authz and privacy-guard route tables).

| Route | Does |
|---|---|
| `GET /api/tool-changes?scope=open\|decided&session=<id>&before=&limit=` | the owner's changes, paged like proposals; `open` = pending, unmuted first |
| `GET /api/tool-changes/{id}` | one change with both definitions (looked up by hash) and their `unsafe` flags |
| `POST /api/tool-changes/{id}/accept` body `{mode: "allow"\|"propose"}` | see below |
| `POST /api/tool-changes/{id}/decline` | state → `declined`, pin event `declined`; the tool stays off |
| `GET /api/proposals/count` | gains `tool_changes_pending` (unmuted) next to `pending_count`; one poll for the badge |
| `GET /api/sessions/{id}/pin-events`, `GET /api/mcp/connections/{id}/pin-events` | the audit, newest first, 200 rows, owner-scoped |

### 5.1 Accept

1. Load the row (owner-scoped, `pending`); `mode` is required and must be `propose`, or `allow`
   only if the row's `mode` is `allow` (a person may tighten, never widen past what the row carried).
2. Load `mcp_tool_definitions[new_hash]`; refuse 409 if missing or `unsafe`; recompute the hash over
   `canonical` and refuse 409 unless it equals `new_hash`.
3. Re-check the connection as proposal approval does (`snapshotHolds`: it exists, same account,
   URL host matches the grant's snapshot, live `ListTools` shows the tool at `new_hash`; otherwise
   409 "the tool changed again; review the newer change").
4. In one transaction: `RepinGrantTool` (changed) or `GrantTool` (new) with `listed:false`; pin
   event `repinned`/`granted` (actor = account); row → `accepted`. Zero rows affected → 409 and the
   row is re-evaluated on the gateway's next listing.
5. Send `refresh_session_tools` to the session's daemon (§5.3); return 200. Idempotent: accepting an
   `accepted` row is a 200 no-op.

410 when the grant row is gone (the session reached a final end and `revokeSessionGrants` ran); the
row is closed `expired`. A disconnected cluster session still has its grant and can be accepted.

### 5.2 Decline

Closes the row; the same hash is never re-opened for this session; later changes to that tool in
this session are recorded `muted`. (An account-level "keep off on this connection" is a later
item.)

### 5.3 `refresh_session_tools` (server → daemon)

A new protocol message `{type:"refresh_session_tools", session_id}` sent through
`FindDaemonForSession` after an accept on the review routes (§5.1) and when a `tools/call` meets
drift under `auto_accept` (§4.1). A re-pin made on `tools/list` sends no refresh (the listing that
re-pinned already carries the new text to the process that listed); a cron fire re-pins before the
run's process exists, so it needs none either. The daemon's manager registers it beside
`set_session_model`; `AgentHost.RefreshTools(sessionID)` looks the session up and type-asserts an
optional `toolRefresher` (`refreshTools()`) interface on its driver (the pattern `sandboxedDriver` uses), so only the
Claude Code driver acts: the process closes at its next idle point (no turn running, nothing
queued; now, if idle) and the next message starts a `--resume` process that lists tools and reports
capabilities afresh. With steering off every turn is a new process anyway. Other drivers, and an
older daemon that does not know the message, ignore it. A native resume can retain the old
schema, so resuming is not a substitute for starting a new conversation when the adapter cannot
reload definitions. The gateway's `listed` gate (§4.4) and current grants still govern calls.

Each actual adapter reports `capabilities.tool_schema_refresh`: `next_turn` means an accepted
definition reloads when idle or before the next turn; `new_session` means the person must start a
separate session and select the current MCP connections and tools. Claude Code reports
`next_turn`; the native API loop, Codex and other CLI adapters currently report `new_session`.
Missing or malformed metadata means unknown, including older daemons. This is adapter metadata,
not a browser engine-name heuristic or a promise that a running turn changes its schema.

The tool-change list, detail and decision responses expose only that enum from the owned
session's latest capabilities report. Listing uses one bounded batch lookup with a partial
latest-capabilities index; lookup failure leaves the policy unknown. The UI distinguishes an
updated grant from a loaded native schema, keeps the current conversation available, and never
starts, ends or restarts a session automatically.

## 6. UI

### 6.1 Runner: Proposals page gains "Tool changes"

- The sidebar badge is `pending_count + tool_changes_pending` from the single count route. The
  badge store keeps the two numbers as separate fields with separate tickets, and `GET
  /api/proposals` also returns `tool_changes_pending`, so the page's own refresh cannot clobber the
  sum. The badge's accessible label becomes "N items awaiting your review".
  Proposals come first (an agent is waiting), then tool changes grouped **per (session,
  connection)** with one card per tool inside.
- A card shows: connection, tool **name** (never `annotations.title`), kind, the session (link),
  when; for `changed` a before/after of the description (word diff) and of the input schema
  (pretty JSON, line diff) from the two stored definitions; for `new` the definition and a mode
  picker defaulting to **propose**, with the existing `MCP_ALLOW_WARNING` when `allow` is chosen.
  Every non-printing, format, bidi, tag or private-use code point renders as a visible escape
  (`⟨U+E0041⟩`) with a "contains hidden characters" badge; a non-ASCII tool name is flagged. An
  `unsafe` definition has no Accept button and says why. Buttons: **Accept** / **Keep off**.
- `removed` cards have no buttons and read "the server no longer offers this tool". Muted rows
  appear under a "declined earlier" fold.
- The capabilities panel's MCP group marks a **server** with "N tool changes pending" (it lists
  servers with counts, not tools) and links to the page.
- The launch picker (`McpPicker`) renders definitions with the same hidden-character escaping and
  shows `unsafe` tools as off.

### 6.2 Core Settings: the policy

In **Settings → MCP connections**, each connection gets a **Definition changes** control:
*Ask me* (strict) / *Accept automatically* (auto_accept), with: "Accept automatically trusts this
server to change what an agent may do without asking you, and a changed description can steer the
agent's use of your other connections and the session's own tools. Use it only for servers your
organization runs. Takes effect in running sessions within five minutes." Changing it writes
through the existing PATCH and is recorded in the connection's access log. The row links to the
runner's pin-event history filtered by this connection (core never calls the runner).

### 6.3 Launch sheet

Unchanged for the person. The picker sends the preset button last clicked per connection (§3.1);
the server stores what it re-derives.

## 7. Launch, resume, crons

- **Launch** (`resolveGrantProof`): switches from `LiveToolHashes` to `ListTools` so it has the
  definitions; writes `mcp_tool_definitions`; stores `preset` (re-derived) and `known_tools` (every
  name listed). The 409 "tool changed … confirm it again" stays: the picker is right there.
- **Resume** (`reissueGrants`): copies the grant, carrying `tools`, `preset` and `known_tools`
  through `DELETE … RETURNING`. It does **no** reconciliation of its own: the gateway's first
  `tools/list` after the resume detects drift and applies §4, so cluster and desktop sessions behave
  the same and the resume never waits on upstream servers.
- **Crons**: a cron's selection is always explicit (`requireExplicit`), so its preset is `custom`
  and a cron **never gains tools automatically**. At fire time `resolveGrantProof` is called with a
  `repin` option that only the scheduler passes, carrying the connection's `pin_policy` from the
  `ListConnections` call the fire already makes (§3.5 adds it to the list response): under
  `auto_accept` a changed hash is accepted for this run's grant, never for an `unsafe` definition
  or a duplicated name; once `StartSession` has succeeded, the cron's stored selection is re-pinned
  through `UpdateCronTx` with a mutate that swaps a tool's hash only if the stored hash still
  equals the old one (a concurrent human save wins), in the **same transaction** as the `repinned`
  pin event carrying the run's session id. Under `strict` the fire fails as today, and the error
  text names the Settings control and the cron form. `checkMCP` (save-time validation) keeps the
  409 unconditionally.

## 8. Failure handling, as the person and the agent see it

| What goes wrong | What happens | Who sees what |
|---|---|---|
| Upstream changes a tool (strict) | hidden; one `changed` row; refusals logged `changed` | agent: neutral refusal; person: badge + card |
| Upstream changes a tool (auto_accept) | re-pinned on the next list; event; a call that meets the drift first asks the process to re-list; calls refused until listed | agent: "list tools again" once; person: the event |
| Upstream adds tools (strict) | `new` row per tool, mode propose (allow for allow_all) | person: cards |
| Upstream adds tools (auto_accept + allow_all/approval) | granted on the next list; events | person: events |
| Upstream adds tools (auto_accept + custom) | `new` rows | person: cards |
| The person left a tool off at launch | nothing | agent: "unknown tool" as today |
| Tool changes again while pending | older row superseded, new row opened; A→B→A reopens A | person: the card shows the newest |
| Live hash returns to the pin | pending row superseded | agent: tool on again |
| Accept after another change | 409, review the newer change | person: message on the card |
| Accept after the session ended | 410; row `expired` | person: message |
| Definition too large / hidden characters / NUL | stored `unsafe`; tool off; card without Accept | person: "cannot be reviewed" |
| Server flips a definition repeatedly (auto_accept) | 3 re-pins per hour, then strict + `throttled` event + card | person: event and card |
| Server lists a name twice | that name is off; nothing recorded | agent: unknown tool |
| Recording the change fails (DB) | the list/refusal proceeds; error logged | person: nothing until the next request records it |
| Grant write fails under auto_accept | tool hidden this request | agent: hidden; person: nothing yet |
| Policy changed in Settings | reaches every gateway within five minutes; flip audited | person: the Settings note and the log row |
| Decline | tool stays off; that hash never asked again; later hashes muted | agent: refusal continues |
| Pending row expires (7 days) | tool stays off; refusal text reverts to "start a new session" | person: the row under decided |
| Daemon away when refresh is sent | nothing now; the next resume re-lists | agent: "list tools again" on call until then |

Nothing here forwards a call the person did not approve, and nothing changes a grant's modes
except an accept (person, never widening past the row) or `auto_accept` (bounded by the preset).

## 9. Security

- **The pin is unchanged as a control.** A changed definition is never served under `strict` until
  a signed-in owner reads the exact hashed text and accepts it. The accept recomputes the hash over
  the text it showed, and the gateway serves only listings whose hash equals the pin, so a server
  cannot slip a third definition between the review and the write.
- **The agent cannot act on text it has not listed.** `listed:false` gates calls after every re-pin
  or grant; the daemon refresh makes the re-list prompt.
- **`auto_accept` is a deliberate, owner-set widening**, audited both as a policy flip and per event.
  The preset bounds what it can grant: `approval` → propose only, `custom` → nothing. The
  server-controlled `readOnlyHint` never widens a mode. A throttle stops a flapping server from
  hiding in its own noise. The warning names the blast radius (other connections, the session's
  own tools).
- **The reviewer sees the real definition.** Content-addressed, exact bytes, never truncated;
  invisible code points are made visible; unreviewable definitions cannot be accepted. The agent
  cannot author or edit a change row.
- **Scope.** Changes, definitions-by-hash lookups and events are owner-scoped like proposals;
  another account's connection is indistinguishable from none; agent tokens cannot reach the routes.
- **Hot path.** Recording is one idempotent insert per new drift, skipped by an in-memory set on
  repeats; caps mirror proposals; `auto_accept`'s re-pin is one CAS update plus one event row.
- **Fail closed everywhere the policy is involved:** unknown policy, core down, write failure,
  zero-row CAS, incomplete listing for `removed`, duplicate names.
- **The refusal text does not recruit the agent:** it tells the agent to continue without the tool
  and not to lobby the person.
- **Accepted risks:** five minutes for a policy change to bite; a resumed session's first list
  does the reconciliation (one request of latency); the agent learns from the refusal that a
  definition changed (harmless).

## 10. Testing

- **db:** CAS semantics of `RepinGrantTool`/`GrantTool`/`MarkListed`; replace carries `preset` and
  `known_tools`; partial unique index and supersede/reopen (A→B→A); declined-hash lookup and muting;
  caps; definitions upsert, `unsafe` marking and the hash-recompute check; prune rules.
- **mcpgw (DB-backed):** strict changed → hidden, refused with the neutral text, one row, outcome
  `changed`; ten requests → one row; second hash → superseded + new; return to pin → superseded;
  auto_accept changed → re-pinned on list, event, `listed:false`, call refused until the next list,
  then served; auto_accept on call → refused, no re-pin; throttle after three; new tool under each
  (policy × preset) cell including `known_tools` NULL and "left off"; duplicate name → off, nothing
  recorded; removed only from a complete listing; recording failure → request succeeds; grant
  write failure → hidden; proposals after re-pin carry the new hash.
- **server:** owner-only and agent-token-refused on every new route (authz + privacy-guard tables);
  accept → next `tools/list` serves the tool; accept with stale `new_hash`/unsafe/hash-mismatch →
  409; mode widening → 400; after session end → 410; decline → mute; count route; pin-event
  routes; `refresh_session_tools` is sent; launch stores preset (re-derived, mismatch → custom) and
  `known_tools` and writes definitions; cron fire with `repin` under auto_accept re-pins and updates
  the stored selection, under strict fails with the pointer text; `checkMCP` still 409.
- **daemon:** `refresh_session_tools` → `markStale` on the Claude Code driver, ignored elsewhere.
- **core:** migration idempotent; `pin_policy` in view/PATCH/token response; validation; flip
  audited; "nothing to change" guard.
- **frontend:** section rendering (changed with diff; new with mode picker defaulting to propose;
  removed; unsafe; hidden-character escapes; muted fold); badge sums from one route; Settings
  control writes the PATCH and shows the note; picker sends the preset and clears it on per-tool
  edits; `CronForm.sameSelection` unaffected.
- **Existing tests to update:** `gateway_test.go` and `gateway_f3_test.go` refusal-text and outcome
  assertions; `proposals_g3a_test.go` (exact count body); grant fixtures across mcpgw/db/server
  test files (new columns); `authz_routes_test`; core `mcpconn` service tests (PATCH, events).
- **Drift on call (mcpgw):** strict call → `changed` row recorded without any list; auto_accept call
  → refused, no re-pin, one refresh per grant per minute; `MarkListed` with a stale hash leaves
  `listed:false`; expired hash not reopened; incomplete launch listing → `known_tools` NULL.

## 11. Sequencing

| # | Item | Size |
|---|---|---|
| 1 | Runner migration 039 + db primitives: `preset`, `known_tools`, `Listed`, CAS writes, replace carry-over (raw jsonb), `mcp_tool_definitions`, `RecordToolChange` lifecycle, `mcp_pin_events`, prune | L |
| 2 | Core: migration 019 (`pin_policy`, `mcp_connection_events`), service/PATCH/token/list responses, flip audit; runner `MCPConnection.PinPolicy` + `Credential.PinPolicy` fail-closed | M |
| 3 | Protocol + daemon: `refresh_session_tools`, `AgentHost.RefreshTools`, Claude driver `markStale` | S |
| 4 | Gateway + launch together: `listUpstream` definitions/completeness, detection and recording on list and call, `auto_accept` on list, `listed` gate, throttle, `Refresher` hook, outcome `changed`; `resolveGrantProof` stores preset/known tools/definitions; picker sends the preset; `MCPSelection.Preset` | L |
| 5 | Review routes, counts, pin-event routes; cron fire `repin` + `UpdateCronTx` | M |
| 6 | Core Settings control + events + link; runner Proposals page section, badge store, capabilities marker, hidden-character rendering (page and picker) | M |
| 7 | Docs; deploy to the lab cluster; e2e with a connection the owner controls: change a description → card → accept → the agent lists and calls the tool at its next turn without a resume; auto_accept on the same connection incl. a propose-mode call → approval of the resulting proposal; policy flip audited | M |

Order: 1 → 2 → 3 → 4 → 5 → 6 → 7. Item 4 is one item because its gateway half must never ship
without its launch half (every custom grant would flood `new` cards). **Nothing deploys before
item 6**: strict changes must not accumulate with no page to review them on.

## 12. Open items for plan time

- Exact home of the per-grant "already recorded" set, the throttle counters and the refresh rate
  limit in `grantState` (all advisory; the DB rules are authoritative).
- Where the runner UI's "history filtered by connection" page lives (likely a query parameter on the
  Proposals page's decided scope).
- Whether to serve the agent's schema from the canonical form rather than the raw upstream bytes
  (today the description is cut to 2048 runes for the agent while the hash covers the full text).

## 13. Review record (2026-10-13)

Two adversarial reviews (security; feasibility against the code). Folded:

- New vs. left-off indistinguishable → `known_tools` at launch; NULL never records `new`.
- Replace helper rotates the token → in-place writes are standalone CAS updates; replace carries the
  new columns via `RETURNING` so resume cannot clobber an accept.
- No definition cache exists; the old text would be missing most of the time → content-addressed
  `mcp_tool_definitions` written at every listing; accept recomputes the hash over the stored text.
- Truncated/jsonb-normalized definitions let a person approve text they never saw → exact bytes as
  `text`, never truncated, `unsafe` marking (size, NUL, tag/bidi characters), no Accept for unsafe.
- Agent does not re-list; same-request re-pin would forward arguments written for the old
  definition → re-pin only on `tools/list`, `listed:false` call gate, `refresh_session_tools` →
  `markStale`.
- Hourly policy refresh via the cron sweep is slow and conditional → policy rides the credential
  fetch, five-minute bound, fail closed, no grant column.
- `readOnlyHint` is server-controlled → `approval` auto-grants propose only; cards default to
  propose; accept cannot widen.
- UNIQUE with NULLs and table-wide uniqueness break `removed` and A→B→A → partial unique index on
  pending rows with `coalesce`, reopen semantics, `decided_at` on every close.
- DoS and consent fatigue → caps as proposals, in-memory recorded set, grouped cards, re-pin
  throttle with `throttled` event.
- Duplicate names, incomplete listings → off / never recorded; `removed` only from a complete listing.
- Cron contradiction → crons never extend; `repin` option only the scheduler passes; stored
  selection updated; events after `attachGrant`.
- Core cannot show runner data → Settings links to the runner's history; flips audited in core.
- Refusal text recruits the agent; decline dodged by whitespace → neutral text; declined hash
  blocked, later hashes muted.
- Claims corrected: refusals are already logged (`refused` → `changed`); resume never calls
  `resolveGrantProof`; `presetOf` is derived from modes; migration numbers 039/019; the
  maintenance ticker is `maintainProposals`; the capabilities panel lists servers, not tools.

Amendments during the build (2026-10-13): a re-pin made on `tools/list` sends no refresh (the
listing carries the new text); definitions are stored only once a change row, re-pin or grant was
actually recorded, so the §3.3 caps bound them; a failed declined-hash lookup counts as blocked
(fail closed); `auto_accept` never re-pins a hash the person declined, or that expired, in that
session; a capped recording supersedes nothing; `removed` rows are listed but excluded from the
badge counts.

Confirmation pass on v2 → v3:

- A long-lived agent meets drift only on `tools/call` → both paths record; a call under
  `auto_accept` triggers the refresh (rate-limited) via a `Refresher` hook on the gateway.
- `MarkListed` could clear the flag for an unseen hash → takes `{name: hash}` and matches the hash.
- `Listed` would be dropped by the Go struct → `Listed *bool omitempty`; resume carries raw jsonb;
  bound `ARRAY[$tool]` paths; `RepinGrantTool` may tighten the mode.
- Index allowed two pending hashes → unique on `(session, connection, tool)` pending; one
  transaction; expired hashes not reopened.
- Definition writes on every listing were unbounded → launch/picker listings plus recorded drift
  only; `last_seen` daily.
- Internal list response lacked the policy; no core audit table exists → `internalMCPConnection`
  and `SecretResult` gain it; `mcp_connection_events` added.
- Cron re-pin could leave no event or clobber a human save → `UpdateCronTx` CAS after
  `StartSession`, same transaction as the event.
- Incomplete launch listing → `known_tools` NULL. Caps wording corrected. Badge store per-field;
  `proposals_g3a_test` listed. Sequencing: daemon message is its own item; nothing deploys before
  the UI.
