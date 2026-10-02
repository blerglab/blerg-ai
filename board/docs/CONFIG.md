# Configuration reference

Every environment variable `blerg-board-server` and the `blerg-board` CLI read, with
defaults and what happens when a var is left unset. Nothing here is
required beyond `DATABASE_URL` — the server starts in a minimal, ungated,
runner-less mode and you opt into the rest.

## Server core

| Var | Default | Notes |
|---|---|---|
| `DATABASE_URL` | — (required) | Postgres connection string. Server exits at startup if unset. |
| `BLERG_BOARD_ADDR` | `:8080` | Listen address. |
| `BLERG_BOARD_WEB_DIR` | `web/dist` | Static directory the SPA is served from. |
| `BLERG_BOARD_SERVICE_KEY` | unset | Bearer key for the service principal (full capabilities, `gate.bypass`). Unset means no service principal exists. |
| `BLERG_BOARD_SECRET_KEY` | unset | AES-256 key (base64 of 32 random bytes) that encrypts each board's automation token at rest. Required before any automation token can be saved, and to boot once one exists. See the security notes below. |
| `BLERG_BOARD_ALLOWED_ORIGINS` | unset | Comma-separated list of `scheme://host` origins allowed to open `GET /ws` cross-origin, e.g. `https://board.example.com,https://ops.example.com`. A request with no `Origin` header (non-browser clients) and a same scheme+host request are always allowed regardless of this var; it only extends the allowlist for genuinely cross-origin browser pages. Same semantics as blerg-runner's `BLERG_RUNNER_ALLOWED_ORIGINS`. |
| `BLERG_CORE_PUBLIC_URL` | unset | core's browser-facing origin; when set, `/healthz` allows that origin (the landing page's status tiles) cross-origin. |
| `BLERG_BOARD_PROMPTS_DIR` | unset | Directory of `<role>.md` files overriding the embedded session-brief templates (`prompts/*.md` — worker, reviewer, specreview, discuss, board, bootstrap). A same-named file there wins over the compiled-in default; unmatched roles keep their default. Overrides are parsed and dry-run rendered at startup — a broken template fails boot. An override replaces the whole file, so a custom `board.md` that omits `{{if .Board.Snapshot}}{{.Board.Snapshot}}{{end}}` drops the spawn-time board-state snapshot (columns, settings, one-line card index) and its board chats go back to re-reading the API on every question. |

## Local inference

One OpenAI-compatible endpoint, read once at startup and shared by every
feature that offloads to it (`internal/localinfer`). The admission gate is
the first consumer, not the owner — anything else that wants the local box
reads the same vars rather than inventing its own.

| Var | Default | Notes |
|---|---|---|
| `BLERG_BOARD_INFER_URL` | unset | **Canonical.** Base URL of a server speaking `/v1/chat/completions` and `/v1/embeddings`, e.g. `http://localhost:8000` — no `/v1` suffix. Unset means no local inference: callers that need it disable themselves, and the `openai` gate backend is not wired. |
| `BLERG_BOARD_INFER_MODEL` | `default` | **Canonical.** Chat model name. `default` suits single-model servers that ignore the field. |
| `BLERG_BOARD_INFER_EMBED_MODEL` | unset | Embedding model name. Deliberately separate: the chat model is never used for embeddings, so an embedding call with this unset fails with a "not configured" error instead of sending a meaningless request. |
| `BLERG_BOARD_INFER_TIMEOUT` | `30s` | Per-call bound (Go duration). Applied on top of whatever deadline the caller sets, so no call can hang forever. A malformed value is ignored in favour of the default rather than failing boot. |

### Legacy names

`BLERG_BOARD_INFER_URL` / `BLERG_BOARD_INFER_MODEL` are canonical, but an
earlier name set is still read as a fallback alias and is not going away —
a deployment that sets it keeps working untouched. Precedence, first
non-empty wins:

| Canonical | then |
|---|---|
| `BLERG_BOARD_INFER_URL` | `GATE_OPENAI_URL` |
| `BLERG_BOARD_INFER_MODEL` | `GATE_OPENAI_MODEL` |

There is no legacy alias for the embedding model or the timeout — those
vars are new.

## Admission gate

The gate runs in one of two modes, chosen at boot and never mixed.

### Account mode — the gate on one person's own credential

For an install where credentials are self-service (each person connects
their own in blerg-core **Settings**), the gate can run, for every board on
the instance, on ONE account's own connected Claude or Hermes credential. The
operator never handles that credential: board fetches it from core's vault.

| Var | Default | Notes |
|---|---|---|
| `BLERG_BOARD_GATE_ACCOUNT_TOKEN` | unset | A blerg-core agent token the account's owner minted for themselves (**Settings → Agent tokens**). Any preset works — board only needs to prove whose it is and that it is live — so use the least-privileged one, `platform`. Setting it switches the gate to account mode. |
| `BLERG_BOARD_GATE_ENGINE` | — (required with the token) | `claude` or `hermes`. `codex` is refused (see below). Set without the token, boot is refused. |
| `BLERG_BOARD_CORE_INTERNAL_KEY` | unset | Same value as core's `BLERG_CORE_INTERNAL_KEY` (the key the runner holds as `BLERG_RUNNER_CORE_INTERNAL_KEY`). Required in account mode; not read otherwise. |
| `BLERG_CORE_URL` | unset | blerg-core base URL (board verifies core-issued tokens against it). Required in account mode. |
| `BLERG_CORE_REGISTER_KEY` | unset | Core's registration key. With `BLERG_CORE_URL` set, the board registers itself (and its agent manifest) in core's component directory and keeps that registration alive. Unset: the board still verifies core's tokens but does not register. |
| `BLERG_BOARD_SELF_URL` | `BLERG_BOARD_PUBLIC_URL`, else `http://blerg-board:8080` | The address the board registers with core: where core and the other components reach it. Only read when `BLERG_CORE_REGISTER_KEY` is set. |
| `BLERG_BOARD_GATE_MODEL` | see notes | Model the gate asks for. Claude: defaults to `BLERG_BOARD_CLAUDE_MODEL`, then `claude-opus-5`. Hermes: defaults to the first model the endpoint lists at `/v1/models`. |

What the account needs connected in Settings:

- **Claude** — an Anthropic **API key** (`sk-ant-api…`). The gate calls the
  Messages API directly; a subscription token from `claude setup-token`
  (`sk-ant-oat…`) is for Claude Code itself and is refused with that reason.
- **Hermes** — the `~/.hermes/.env` contents, which must name the
  OpenAI-compatible endpoint Hermes talks to as `OPENAI_BASE_URL` (e.g.
  `http://my-gpu-box:8000/v1`), plus `OPENAI_API_KEY` if that endpoint wants
  one. The gate calls *that* endpoint — the person's own hardware — not the
  operator's `BLERG_BOARD_INFER_URL`. The board server must be able to reach
  it.

How the credential is held: the token is re-verified locally (no network) on
every gate call, so revoking it in Settings stops the gate using the
credential within one revocation poll (≤60s). The credential itself is
fetched on first use, kept in memory for an hour, fetched again after that
or immediately after the provider answers 401/403, and a failed fetch is not
retried for 30s. Nothing polls in the background. It is never logged, and
gate errors carry status codes only, never a provider response body. Every
fetch is an audited credential access on core's side.

**No fallback once opted in.** In account mode `ANTHROPIC_API_KEY` and the
local-inference endpoint are not used by the gate at all. If the credential
can't be fetched (core down, token revoked, nothing connected) the gate is
*unavailable* and each gated board's `gate_on_unavailable` policy applies —
it never quietly judges on the operator's credential instead. A
half-configured account mode (no engine, no core URL, no internal key)
refuses to boot.

**Why not Codex.** A Codex credential is the codex CLI's login
(`~/.codex/auth.json`). Signed in with ChatGPT — the usual case — it holds
OAuth tokens for the CLI's own backend, not an API key for a completions
endpoint; calling that backend directly would mean an undocumented API, and
refreshing the token would rotate it out from under the copy in the person's
vault. The only supported way to spend it is the `codex` CLI itself — a
`codex exec` process per gate call, i.e. seconds of latency on a synchronous
write path. Codex *is* available for the sessions a board starts (see
*Board automation identity* below).

**Security notes — trust decisions you make by turning account mode on.**

- **The internal key is gated by core, but it is still a sensitive key.**
  Core answers a credential fetch only when the request names exactly one
  live proof that belongs to the requested account: an agent token, or a
  browser session. An account uuid alone gets nothing, even for someone who
  is logged in. Every fetch board makes carries the configured token's id
  (it refuses to send one without), so core itself confines the answer to
  that token's live owner, and a revoked or expired token gets the same
  `404` as a missing credential. What the key still allows: anyone holding
  it *and* a live token id (or session id) of an account can fetch that
  account's credentials, so the key does not by itself expose other people,
  but the automation token you configure is the one thing it can spend.
  Configure the key only if you use account mode, and protect it like the
  runner's.
- **The account owns the endpoint.** In Hermes account mode the endpoint the
  gate calls is whatever `OPENAI_BASE_URL` the configured account has
  saved. They can repoint it at any time; the gate picks the change up
  within the hour (the credential cache), and board then makes requests from
  its own network position to wherever it points. What that endpoint
  answers — including its HTTP status, which appears in the board-visible
  "curator unavailable (HTTP …)" reason — is theirs to control. Choose an
  account you trust with that.
- **What board viewers see.** A gated board's "curator unavailable" reason is
  generic plus the provider's HTTP status at most; the underlying error
  (transport detail, provider response, why a credential was refused) goes
  to the server log only.
- **Automation tokens are encrypted at rest; the gate token is not.** Each
  board's automation token is stored AES-256-GCM-encrypted (`v1:` +
  base64(nonce‖ciphertext), the board id bound in as additional data) under
  `BLERG_BOARD_SECRET_KEY`: base64 of exactly 32 random bytes
  (`openssl rand -base64 32`), distinct from every other key. Board refuses
  to start if the key is malformed, or if it is unset while any board has a
  stored token; with the key unset and no tokens it starts, but saving a
  token is refused (422) until the key is set. Tokens saved before
  encryption existed are encrypted in place at the first boot with the key.
  **Losing or changing the key makes the stored tokens unreadable** — the
  affected boards refuse to start sessions with "automation token cannot be
  decrypted; re-save it", and each token must be re-entered in the board's
  settings. Back the key up with the rest of the deployment secrets. The
  gate token in board's environment (`BLERG_BOARD_GATE_ACCOUNT_TOKEN`) is
  still plaintext, like the other secrets in this codebase.

### Legacy mode — operator credentials (the default)

With `BLERG_BOARD_GATE_ACCOUNT_TOKEN` unset, gate backends are read in the
order named by `BLERG_BOARD_GATE_BACKENDS`; a backend is only added if its
required var is set. With no backends configured, gated boards fall back to
each board's `gate_on_unavailable` policy instead of failing writes.

| Var | Default | Notes |
|---|---|---|
| `BLERG_BOARD_GATE_BACKENDS` | `openai,claude` | Comma-separated backend order. |
| `ANTHROPIC_API_KEY` | unset | Claude API key. Enables the `claude` backend and the dispute tiebreak adjudicator. |
| `BLERG_BOARD_CLAUDE_MODEL` | `claude-opus-5` | Model used for both the `claude` gate backend and the tiebreak. |

The `openai` backend has no vars of its own: it uses the local-inference
endpoint above and is skipped when no URL is configured. The ordering is the
point — the local box answers first at ~0 marginal cost per write, and the
Claude API is held back as the adjudicator a disputed verdict escalates to.

## Runner (agent session spawning)

Off entirely unless both `BLERG_RUNNER_URL` and `BLERG_RUNNER_KEY` are
set — blerg-board ships one driver (blerg-runner-compatible HTTP); a self-hosted
deployment without a compatible runner should leave these unset and use
blerg-board purely as a board.

| Var | Default | Notes |
|---|---|---|
| `BLERG_RUNNER_URL` | unset | Runner service base URL. Required to enable the runner. |
| `BLERG_RUNNER_KEY` | unset | Runner service bearer key. Required to enable the runner. Used to follow, message and stop sessions — never to start one (see *Board automation identity*). |
| `BLERG_BOARD_RUNNER_RUNTIME` | unset | Runtime the board asks for on every session start, sent as `runtime` on `POST /api/runner/start`: `daemon` (the connected daemon's bare host), `docker` (that daemon's local sandbox) or `cluster`. Unset sends no `runtime`, so the runner decides: cluster where one is configured, else the sandbox when the daemon has its image, else the host. Set `daemon` to pin board sessions to the host — e.g. for agents that must `git push`, since the sandbox has no git credentials. Any other value refuses to boot. |
| `RUNNER_GIT_BASE` | unset | Git host base (e.g. `https://github.com/your-org`) sessions clone `<repo>.git` from. |
| `BLERG_BOARD_PUBLIC_URL` | `https://blerg-board.example.com` | Human-facing base URL used in card links. Always set it. |
| `BLERG_BOARD_AGENT_URL` | `http://blerg-board.blerg-board.svc` | blerg-board's own URL as reachable *from spawned sessions* (in-cluster address). **In-cluster default — override it — the installers set it to the public URL.** |
| `RUNNER_UI_BASE` | unset | Base URL of the runner's session UI (e.g. `https://runner.example.com/sessions`). Omits the session-view link when unset. It is also what an `artifact` card link (a file a session published) is checked against and resolved with: with it unset, artifact links are refused. |
| `RUNNER_BOOTSTRAP_REPO` | `blerg-board` | Scratch repo used for bootstrap sessions on boards with no repo wired yet. |
| `RUNNER_GIT_CREDENTIAL_ENV` | unset | Name of an env var (already present in spawned sessions) holding a git/GitHub credential, used in the bootstrap prompt. Unset has the session ask the human for one instead. |
| `INFRA_DOCS_URL` | unset | Paired with `INFRA_DOCS_NOTE` to append an infra-reference paragraph to session prompts. Both must be set for it to appear — either empty keeps prompts environment-agnostic. |
| `INFRA_DOCS_NOTE` | unset | One-line description of the deployment environment, shown alongside `INFRA_DOCS_URL`. |

### Board automation identity (who a board's sessions run as)

`BLERG_RUNNER_KEY` has no account behind it, so it never starts a session:
a session it started could only run on an operator-provisioned engine
credential, and credentials here are self-service. Instead every board
carries its own **automation token** and **automation engine**, set in the
board's settings panel (the model chip in the board header → *Automation*):

1. In blerg-core **Settings → Agent tokens**, create a token with preset
   `run-sessions` (it is yours: `on_behalf_of` is your account).
2. Make sure the engine you want (Claude, Codex or Hermes) is connected on
   the same Settings page.
3. On the board, paste the token, pick the engine, Save.

Every session the board starts — **Run board**, the quiet-worker respawn,
reviewer and conflict-worker spawns, standing agents, and the card
**Run**/**Discuss** and board-chat buttons — goes to the runner under that
token, so it runs as that person on their own engine credential (for Hermes,
their own endpoint). The runner does the credential fetch; board never sees
the credential.

- Only a signed-in person can set the token, and only their own — board
  checks it is a live `run-sessions` token whose owner is the person saving
  it. Any board admin can remove it. It is write-only: no endpoint (REST or
  MCP) ever returns it; reads show `automation_token_set` and
  `automation_token_expires_at`.
- `automation_engine` (`claude` default, `codex`, `hermes`) is also settable
  over `PATCH /api/boards/{id}` and the MCP `blerg_board_update` tool. A
  board's Claude model settings are not sent to a Codex/Hermes session.
- **No token, no sessions.** With none set (or an expired/revoked one) the
  board starts nothing and says why: the card/board-chat buttons and **Run
  board** answer 422 with the fix; a run already going stops and comments
  on the card it was about to start; a quiet worker that can't be replaced
  is flagged stuck with the reason; a standing agent records it on its queue
  item. It never falls back to `BLERG_RUNNER_KEY`.

Tokens expire (90 days by default); the panel shows when, and whose token
it is (the account it acts for, marked "you" for its owner).

**Anyone who can write cards on this board can spend this credential and
could get an agent to reveal it.** Every card-writer — people with
`card.write`, and the board-scoped tokens running sessions themselves carry —
can start work that runs on the token owner's engine credential, and that
session's environment necessarily holds the credential itself, so a prompt
can get an agent to print it. Only connect a token you're comfortable with
every card-writer effectively having.

### What the runner has to implement

The driver talks to these routes, bearer-authenticated with
`BLERG_RUNNER_KEY` — except `start`, which is made with the board's
automation token (above):

| Route | Used for |
|---|---|
| `POST /api/runner/start` | spawn a session (as the automation token's owner, with `engine`) |
| `GET /api/runner/sessions/{id}` | lifecycle/runtime status (404 ⇒ session gone) |
| `POST /api/runner/sessions/{id}/message` | deliver a message to the session |
| `POST /api/runner/sessions/{id}/interrupt` | cancel the in-flight turn |
| `POST /api/runner/sessions/{id}/stop` | end the session |
| `GET /api/runner/sessions/{id}/events` | settled event stream (`after_seq`, `limit`) |
| `POST /api/runner/sessions/{id}/model` | **optional** — re-model a live session |

`.../model` takes `{"model": "<model id>"}` and must answer JSON (or 204 with
no body — anything else is read as "route not implemented"). It changes
the model the session's NEXT turn runs on: same session, same context, same
event stream, nothing respawned (the in-flight turn finishes on the old
model). It is what lets a session escalate — or de-escalate — itself
mid-task, from `POST /api/runner-sessions/{id}/model` on blerg-board's own API.

A runner that does not implement it needs to do nothing: an HTML/SPA
fallback, 405 or 501 is read as "unsupported" and blerg-board answers its own
callers with 501 rather than pretending the change happened. A 404 on that
route is read as "no such session", so don't 404 a route you haven't built.
Runners that report their own model changes (a human re-modelling a session
in the runner's UI) should emit a `model_changed` event carrying `model` —
blerg-board follows it so the model it reports stays the model that is running.

## Optional integrations

| Var | Default | Notes |
|---|---|---|
| `GITHUB_TOKEN` | unset | Used to fetch PR diffs and create branches/PRs for in-app review. Needed for private repos; public repos work without it (lower rate limits apply). |

## CLI (`blerg-board`)

| Var | Default | Notes |
|---|---|---|
| `BLERG_BOARD_URL` | `http://localhost:8080` | Server base URL. |
| `BLERG_BOARD_TOKEN` | unset | Bearer token for API calls. |
| `BLERG_BOARD_BOARD` | unset | Board UUID most subcommands operate on. |
