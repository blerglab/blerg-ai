# blerg-runner

The session runner for Blerg — it spawns and supervises coding-agent sessions and streams their
work back as structured JSON events. Part of the Blerg monorepo; the control plane is `blerg-core`
and the board is `blerg-board`.

## Components

- **server** (`cmd/server`) — the hub. Serves the runner API, the daemon WebSocket, and the web UI.
  Registers with `blerg-core` and validates `blerg-core`-issued tokens. The web UI is a sidebar of
  sessions (one status glyph per card, in an order that does not move when a state changes, with
  the usage meters at its top and the version and commit at its foot) and, per session, the chat
  with a side panel (Live, History, Files, Capabilities): see
  [`docs/talking-to-an-agent.md`](../docs/talking-to-an-agent.md#the-session-panel).
- **daemon** (`cmd/daemon`) — runs on a workstation, dials the server, and spawns/supervises the
  actual agent sessions using the host's engine CLIs (`claude`, `codex`, …). This is where sessions
  really run, so it needs the operator's engine login (for Claude a subscription via
  `claude setup-token` / `CLAUDE_CODE_OAUTH_TOKEN`, or an already-logged-in `claude`; for Codex a
  logged-in `codex`). It keeps a recovery record per agent session, so a restart or update of the
  daemon continues a Claude Code or Codex agent session on the host rather than ending it.
- **runner** (`cmd/runner`) — PID 1 of a per-session pod in the k8s runtime.

To run the whole stack on a cluster, use [`install/k8s`](../install/k8s/README.md).

## Runtimes

Every session runs in exactly one of three places, and the runner UI's launch sheet asks which,
out loud, every time: the **Run** column shows all three as radio cards with nothing collapsed.
There is no implicit fallback onto the least sandboxed one — running unsandboxed is always a
choice someone made and acknowledged.

The wire spelling is the `runtime` field on `POST /api/sessions`: `cluster` | `docker` | `daemon`,
for both session kinds. Anything else is `422 invalid runtime` (an unrecognized value is refused
rather than defaulted, because defaulting would land the session on the *least* sandboxed
runtime). An empty value means `daemon`, for callers older than the Run column. The value is
recorded on the session row and returned, with `kind`, by `GET /api/sessions` — that is what the
`Cluster` / `Sandbox` / `Host` and `Agent` / `Terminal` chips on each session card read.

| | **Cluster pod** — `cluster` | **Local sandbox** — `docker` | **This machine** — `daemon` |
|---|---|---|---|
| What it is | a throwaway Job pod in the cluster's session namespace | a hardened container on the connected daemon's host | the daemon host itself, as the user the daemon runs as |
| Agent session | the pod runs the engine | the daemon runs the engine **inside** the container (`docker exec -i -w /workspace`) | the daemon runs the engine on the host, as you, with no permission prompts |
| Terminal session | refused — `422 terminal sessions run on a daemon` | tmux inside the same container | tmux on the host |
| Needs | a configured cluster runtime (`BLERG_RUNNER_AGENT_IMAGE`; see below) | a connected daemon reporting `sandbox_available` — i.e. the sandbox image is built | a connected daemon |
| Credentials | a per-session Secret holding the launching account's own engine + git credentials, falling back to the operator Secret | the host's `~/.claude`, `~/.claude.json`, `~/.codex` and `~/.hermes/{config.yaml,.env}`, bind-mounted read-write; for Claude with no such login, `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` is passed into the container | your own logins on that machine, directly |
| Refuses | terminal sessions | OpenClaw (`OpenClaw runs only on the host`); Claude with no credential the container can see | — |
| "Bypass permission prompts" toggle | not offered | terminal sessions only (`dangerously_skip_permissions` is docker-only) | not offered |
| Acknowledgement | none | none | required |

A sandboxed **agent-kind** session gets exactly the same container the terminal sandbox gets —
`--cap-drop ALL`, `--security-opt no-new-privileges`, `--pids-limit 512`, `--memory 4g`, the repo
at `/workspace`, the credential mounts above, and a `BLERG_RUNNER_*`-only environment — and the
engine process runs inside it rather than on the host. The daemon master token never enters it.
Preflight for such a spawn checks the image and looks for the engine binary *inside the image*,
never on the host PATH. The engine versions in the image are pinned (`sandbox/engines/package.json`,
`sandbox/install-hermes.sh`). Details, and the per-engine exceptions, are in
[`install/desktop/DAEMON.md`](../install/desktop/DAEMON.md#local-sandbox-runtime).

**Launch-sheet defaults.** Where it runs = **Cluster pod** when the install has a cluster runtime
configured (even if a daemon is connected), else **Local sandbox** when a connected daemon has the
sandbox image, else **This machine** — which then cannot be launched until the acknowledgement is
ticked. Session type = **Agent**. Engine, model and effort are whichever were used last (Claude
first time; the model then defaults to Sonnet at its default effort). The Model picker lists what
Claude Code's own `/model` offers, fetched live — see [Model lists](#model-lists). Unsandboxed is never a default, and a runtime or type the user picked is never overwritten
by a later data refresh. Choosing OpenClaw forces **This machine**; choosing **Terminal** disables
the cluster card, and choosing **Cluster pod** disables Terminal.

The agent-facing contract, [`POST /api/runner/start`](#starting-a-session), takes the same three
values and, with `runtime` omitted, the same defaults: cluster where one is configured, else the
**Local sandbox** when the connected daemon chosen for the repo reports `sandbox_available`, else
**This machine** — the daemon host, unsandboxed. So everything the Board launches on a desktop
with the sandbox image built runs in the sandbox. An explicit value is honored verbatim:
`runtime: "daemon"` is the opt-out onto the host (the board sends it for every start when
`BLERG_BOARD_RUNNER_RUNTIME=daemon`), and `runtime: "docker"` against a daemon without the image is
refused by the daemon rather than demoted onto the host.

A board-started session in the sandbox has its `BLERG_BOARD_*` variables, the `blerg-runner`
messaging CLI (mounted read-only by the daemon), and a route to the board and the runner over the
`blerg-sandbox` Docker network (loopback board and runner URLs are rewritten to the services'
names on it). Its limits:

- **no git credentials** — `~/.ssh` and `~/.gitconfig` are not mounted, so the agent can commit
  into `/workspace` but cannot push. Pin such work to the host with `runtime: "daemon"`;
- **of the stack, the board and runner only** — the `blerg-sandbox` network reaches those two
  APIs (both require a token) and other sandbox containers, not Postgres or core. Where that
  network does not exist the container runs on Docker's default bridge and cannot reach the board
  at all ([`DAEMON.md`](../install/desktop/DAEMON.md#local-sandbox-runtime)).

What the contract has no field for is the acknowledgement or the "bypass permission prompts"
toggle: a v1 start never *asks* for one. Note separately that an agent-kind session runs
non-interactively and its engine bypasses the prompts regardless, on either runtime — so a start
that lands on **This machine** (no sandbox image, or `runtime: "daemon"`) runs unprompted on the
host as you.

## API contract v1

The agent-facing half of blerg-runner: start a coding-agent session, drive it, follow it, collect
its result — over REST or MCP, with a credential a user mints in a browser. Everything below is
also machine-readable from the runner itself, so a tool never has to read this file.

### Discovery

| Request | Answers |
|---|---|
| `GET <runner>/agents` | this component's manifest: `base_url`, `version`, `contract_version` (`v1`), capabilities, `auth` (audience, presets, what it accepts), and every operation with method, path, capability and whether it is idempotent |
| `GET <runner>/agents` with `Accept: text/markdown` (or `?format=md`) | the same manifest rendered as Markdown, for an LLM to read directly |
| `GET <runner>/openapi.json` | an OpenAPI 3.1 description of the session contract — schemas for the start body, status, events, result, and every error response |
| `GET <core>/agents` | the whole install's aggregate manifest: core's own entry first, then every registered component (the runner among them), plus a seven-step quickstart. Start here if you don't know the runner's URL yet |

All four are **unauthenticated** — an agent must be able to read how to get a credential before it
has one. They contain no secrets: public URLs, version strings, capability names, and the
documentation of endpoints that each enforce their own auth.

### Auth

Every route below takes `Authorization: Bearer <token>`, and accepts either:

- **an agent token** minted by blerg-core from the `run-sessions` preset — `aud: "blerg-runner"`,
  capability `session.start`. Mint it in core's **Settings → Agent tokens**, or `POST /api/tokens`
  with a human session. The audience alone is not enough: a token for this component that does not
  carry `session.start` is rejected like any other bad credential.
- **the operator's static runner key** (`BLERG_RUNNER_KEY`) — a component credential with no owning
  account. When it is unset, the entire contract answers `404 runner endpoints not configured`,
  including `POST /mcp`.

`GET /api/runner/me` reports what the credential you just used actually is — `kind`, `sub`,
`on_behalf_of`, `aud`, `caps`, `expires_at` — so an expired token, a wrong audience and a missing
capability are distinguishable without guessing from a 401. The static key reports only
`{"kind":"runner_key"}`. The credential itself is never echoed back.

### Routes

Paths are relative to the runner's `base_url`.

| Method | Path | Purpose | Notes |
|---|---|---|---|
| `GET` | `/agents` | manifest | public; `Accept: text/markdown` for the Markdown rendering |
| `GET` | `/openapi.json` | OpenAPI 3.1 document | public |
| `GET` | `/api/runner/me` | credential introspection | `404` when the contract is not configured |
| `GET` | `/api/runner/launch-options` | available engines, models and efforts for this principal and target | cached snapshots; `runtime`, `daemon_id`, `repo`, `kind`, `restricted`, `personal_only` and `refresh` filters |
| `GET` | `/api/daemon/whoami` | would `/ws/daemon` accept this **daemon** token? | takes the daemon token, not an agent credential; see [Checking a daemon token](#checking-a-daemon-token) |
| `GET` | `/api/models/{engine}` | the models (and each one's effort levels) a session on that engine can use | see [Model lists](#model-lists); also readable with a browser session |
| `POST` | `/api/runner/start` | start a session | `202 {session_id}`; honors `Idempotency-Key` |
| `GET` | `/api/runner/sessions/{id}` | status | `{lifecycle, runtime, resumable, auto_stop, interaction, error_reason, end_reason, ended_by, limits}`; `limits` is `{max_file_bytes, max_session_upload_bytes, max_session_artifact_bytes}`, what the session's files may weigh (absent before 0.2.1) |
| `POST` | `/api/runner/sessions/{id}/message` | send a turn | `{text, source?, client_id?}` → `202`; resumes a disconnected cluster session |
| `POST` | `/api/runner/sessions/{id}/model` | select next-turn model/effort | `{model?, effort?}` → `202`; existing session engine remains authoritative |
| `POST` | `/api/runner/sessions/{id}/interrupt` | cancel the in-flight turn | `202`; `409` when there is no live runtime |
| `POST` | `/api/runner/sessions/{id}/stop` | end the session | `200 {"status":"stopped"}`; idempotent. Finished work **must** call this or cluster session slots leak |
| `GET` | `/api/runner/sessions/{id}/events` | transcript window | `?after_seq=&limit=` → `{events, has_more}`; `?before_seq=&limit=` pages backwards → adds `has_older, first_seq, server_time` |
| `GET` | `/api/runner/sessions/{id}/events/stream` | the same events as SSE | resumable with `Last-Event-ID` |
| `GET` | `/api/runner/sessions/{id}/events/live` | the live transcript as SSE: replay, events, typing deltas, status | see [Following a session](#following-a-session) |
| `GET` | `/api/runner/sessions/{id}/result` | structured outcome | one object instead of a replayed transcript |
| `GET` | `/api/runner/sessions/{id}/artifacts` | the session's files | `{artifacts: [...]}`, newest first; see [Session files](#session-files) |
| `GET` | `/api/runner/sessions/{id}/artifacts/{aid}/raw` | one file's bytes for a viewer | server-chosen type, `nosniff`, `CSP: sandbox` |
| `GET` | `/api/runner/sessions/{id}/artifacts/{aid}/download` | one file's bytes as an attachment | `Content-Disposition` with its name |
| `DELETE` | `/api/runner/sessions/{id}/artifacts/{aid}` | delete one file | `204`; any origin |
| `POST` | `/api/runner/sessions/{id}/uploads` | attach a person's file | raw body + `X-Artifact-Name` → `201`; the app vouches for the person |
| `GET` | `/packages/` | the UI packages this runner serves | public; see [UI package](#ui-package) |
| `GET` | `/packages/{file}` | a package tarball | public, immutable |
| `POST` | `/mcp` | MCP server (JSON-RPC 2.0) | the REST session and discovery operations as tools |

Every `/api/runner/sessions/{id}` route answers `404 session not found` for an id that does not
exist — and for one the calling credential may not see (see
[What "runs as you" means](#what-runs-as-you-means)).

### Starting a session

`POST /api/runner/start`, JSON body:

| Field | Required | Meaning |
|---|---|---|
| `repo` | yes, unless `no_repo` | a folder name or `org/name` — at most one slash, no `..`, no leading dot. On a daemon runtime it must exist under the daemon's repos root |
| `no_repo` | no | `true` = a session tied to no repository; `repo`, `git_url` and `provider` must then be absent. A blank `repo` is never read as this — without the flag it is still `422 repo is required`. See [No repository](#no-repository) |
| `prompt` | no | the session's first turn |
| `title` | no | a human-readable label |
| `model` | no | engine-specific model name; the install's default when empty. An `id` from [`GET /api/models/{engine}`](#model-lists); for Claude also a Claude Code alias (`sonnet`, `sonnet[1m]`). Claude: lowercase letters, digits, `.`, `-`, `[`, `]`, ≤ 64 chars; other engines: letters, digits and `. _ : / @ - [ ]`, no leading `-`, ≤ 128 chars |
| `effort` | no | reasoning effort; the model's own default when empty. Must be in the engine's allowlist — Claude: `low`, `medium`, `high`, `xhigh`, `max`; Codex: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra`; Hermes: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `ultra` (use the chosen model's `efforts` from `GET /api/models/{engine}`). An engine with no effort levels (today OpenClaw) refuses any value |
| `engine` | no | coding engine; `claude` when empty |
| `runtime` | no | `cluster`, `docker` (the connected daemon's local sandbox) or `daemon` (that daemon's bare host). Empty means cluster where one is configured, else `docker` when the chosen daemon has the sandbox image, else the daemon host. The sandbox has no git credentials (commit, not push); `daemon` opts out — see [Runtimes](#runtimes) |
| `git_url` | no | clone URL override for a repo outside the install's git base (cluster runtime only — a daemon works from its own checkout) |
| `env` | no | extra environment for the session, `{string: string}` |
| `callback_url` | no | where to POST the result when the session ends |
| `callback_secret` | no | HMAC key signing that callback. Stored to sign with; never returned by any endpoint, never logged |
| `auto_stop` | no | one-shot: end the session as soon as its first turn is done. `false` by default |
| `interaction` | no | `interactive` or `unattended`: whether a person is reading the session's chat as it works. `unattended` by default on this contract. See [Interactive and unattended](#interactive-and-unattended) |

Validation, and the status each failure answers with:

- `422` — missing `repo` (without `no_repo`); `no_repo` together with `repo`, `git_url` or
  `provider`; a repo name that fails the shape rule above; a `model` that fails the
  engine's model-name rule, or an `effort` outside the engine's allowlist (or, for a model the engine's list knows, outside that model's own `efforts`); an `env` key beginning
  `BLERG_RUNNER_` or equal to `ANTHROPIC_API_KEY` (reserved); a `runtime` that is none of
  `cluster`, `docker`, `daemon`; `runtime: cluster` on an install with no cluster configured; a
  `callback_url` that is not an absolute URL, or is not `https` (plain `http` is accepted only for
  `localhost`, `127.0.0.1` and `::1`, and only on an install that has set
  [`BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE=true`](#server-cmdserver) — the desktop compose stack does,
  a cluster install does not, so there a loopback callback is refused here rather than accepted and
  then silently undeliverable).
- `400` — malformed JSON, a malformed `Idempotency-Key`, or an `interaction` that is neither
  `interactive` nor `unattended`.
- `401` — missing, expired, revoked or under-capable credential. `404` — the contract is not
  configured, or (on the session routes) no such session.
- `503` — nothing can take the session: no connected daemon has the repo, the cluster refused the
  Job, a daemon's send buffer is full, or an idempotency record raced. All of these are retryable.
- `500` — the session could not be recorded; it was not started.

**`Idempotency-Key`.** Send one whenever a retry is possible. The key is 1–128 characters, scoped
to the calling credential (two callers using `retry-1` neither collide nor see each other's
sessions) and retained for 24 h. Same key, same body → the original `202` is replayed with
`Idempotent-Replayed: true` and no second session. Same key, different body → `409 idempotency key
reused with a different request`. A header that is present but empty, sent twice, or over 128
characters is a `400` rather than a silently dropped guarantee. A start that fails releases its
key, so a retry is a fresh start rather than a replay of a session that never existed.

### Discovering launch choices and controlling a conversation

Use `GET /api/runner/launch-options` (MCP `discover_launch_options`) before offering a
launch picker. It returns registered engines that are available for the caller's
credentials and selected execution target, with each engine's model catalog. Pass
`runtime`, an optional host `daemon_id`, and `repo` when choosing a repository. The
response's per-engine `runtime` and `daemon_id` identify its resolved target; pass
both on start to preserve that placement. An explicit host pin requires `daemon`
or `docker` and never falls back to another host. Without a pin, auto placement
may resolve different engines to different hosts and runtimes.

Render an engine selector only when several engines are available. A sole engine
is selected automatically; an empty list offers no invented default. `state:
unknown` distinguishes incomplete credential/host discovery from known absence.
`refreshing` means acquisition is running in the background; poll with a bounded
UI deadline and offer Retry. `refresh=1` requests one background refresh, rather
than making the HTTP request wait for a CLI or pod. Native model catalogs retain
the last successful snapshot on refresh failure. Availability is advisory:
launch and resume always enforce current authorization again.

Use `restricted=1` for confined file/MCP execution and `personal_only=1` when
operator credentials are forbidden, as for Cluster crons. Operator runner keys
see operator catalogs only; account-bound agent tokens resolve their owner and
carry their own current token proof. A descriptor describes an implemented
integration, not a claim that every target has that engine installed or logged in.

For retry-safe chat delivery, send a stable `client_id` with `text` to the message
endpoint (MCP `send_message`). Repeating identical content with that ID is accepted
once; changed content returns `409`. Receipt-capable hosts retain accepted input
until native consumption, including reconnect replay. Legacy hosts refuse IDs
rather than promising durability they cannot provide. A successful response means
accepted for delivery; native events report consumption and effective settings.

`POST /api/runner/sessions/{id}/model` (MCP `set_session_model`) accepts a model,
effort, or both for the existing agent session's next turn. It checks the stored
engine and its model rules. It does not switch a native conversation between
engines. An embedder that changes engines must explicitly launch a new native
session and explain how prior context is carried forward. Model changes are
acknowledged as dispatch; native `model_changed` events report effective values.
Disconnected Cluster recovery reports `resumable`, `resume_mode`, and
`resume_reason`; eligibility still requires successful current proof, placement,
and retained-history checks when the next message arrives.

### One-shot sessions

A session started with just a `prompt` answers it and then waits for the next message. That is what
you want for a conversation and not what you want for a tool call: the lifecycle stays `running`,
`result.terminal` stays false and the completion webhook never fires until somebody calls stop.

Start with `auto_stop: true` to say "this is one task". The runner stops the session itself as soon
as the first turn finishes — whichever turn that is: the prompt's, or, for a session started
without a `prompt`, the one the first message kicks off, and a turn that ends in a provider error
counts too. That stop is the same thing `POST …/stop` does: the pod or
daemon session is killed, the lifecycle becomes `ended`, the session's tokens are revoked and the
completion webhook is delivered. So a fire-and-forget caller can simply wait for its callback (or
poll until `result.terminal` is true) and read the answer out of `last_assistant_message`; there is
nothing left to stop and no slot left held. The flag is reported back by both `GET …/sessions/{id}`
and `GET …/sessions/{id}/result` as `auto_stop`.

A later turn on an auto-stopped session — a re-delivered event, a session that somehow produces
another — changes nothing: the stop happens exactly once, and a session that was already stopped,
ended or failed is left alone. Leave the flag off (the default) whenever you intend to send the
session more messages.

### Interactive and unattended

An agent session runs its engine headless, and an engine in that mode assumes nobody is watching:
it treats a remark as a work order and says nothing between tool calls. Whether somebody is
watching is a fact about the session, so it is set when the session starts, as `interaction`, and
the engine is told in its system prompt:

- `interactive`: a person is reading the chat as the session works, and answers there. The agent is
  told that everything it writes appears in their chat as it is written, to answer a remark or a
  question and talk it through before changing anything, to ask and end its turn when the decision
  is theirs, and to say what it is about to do and what it found as it goes.
- `unattended`: nobody is reading. The agent is told not to stop and wait for an answer in the
  chat, to make the reasonable decision and write down the assumption it made, and to end with a
  summary that stands on its own. `blerg-runner ask` still reaches a person for a decision only
  they can make.

`POST /api/runner/start` and the `start_session` MCP tool default to `unattended`, which is what a
tool's job or a board card run is; pass `interaction: "interactive"` when a person will follow the
session as a conversation. A session started in the app (the launch sheet, `POST /api/sessions`)
defaults to `interactive` and accepts the same field. A session a cron starts is always
`unattended`, whatever was asked. Any other value is a `400`.

The mode is fixed at start, kept across a cluster resume, and reported back as `interaction` by
`GET …/sessions/{id}` and in the session objects of `GET /api/sessions`. A restricted session (a
cron's, or one the board starts with MCP connections) gets no appended system prompt at all, as
before. A daemon older than the field ignores it and behaves as it always did.

### Lifecycle

`lifecycle` is one of five values, and it is a projection — the internal session state machine is
finer-grained and is never handed out raw:

| Value | Meaning |
|---|---|
| `starting` | accepted, not yet running |
| `running` | working (internally: running, waiting on input, or idle) |
| `disconnected` | its host is gone but the work can be picked up again |
| `ended` | finished or stopped |
| `error` | failed; `error_reason` says why |

`ended` and `error` are terminal — that is exactly what `result.terminal` reports, what closes the
SSE stream, and what fires the completion webhook. `runtime` is a separate axis (`none`, `docker`, `daemon`,
`cluster`, `job_finished`): where the session is hosted, not what state it is in.
Connected desktop sessions report their stored runtime: `docker` for the local sandbox and
`daemon` for the bare host. `resumable` is
true only for a `disconnected` session on an install with a cluster runtime.

A session whose cluster Job has finished while the session never got past `starting` reports
`error`, not a `starting` that never converges. A status value this build does not recognize is
reported as `running`: an unknown value is not evidence the work is over.

A desktop daemon reports, in its hello and every heartbeat, the sessions it runs
(`active_sessions`) and the spawns it has received but not started yet (`queued_sessions`: waiting
in its queue, cloning, installing plugins). The server stops a session its connected daemon reports
in neither list — its messaging token revoked and its MCP gateway grants deleted — so a queued spawn
must be listed: before `queued_sessions` (an older daemon) a slow start was read as lost, and it then
came back, through its own `session_started`, without its credentials. Such a daemon still gets two
minutes from the session's creation before the server reads "not listed" as "lost". A live session
left without a valid messaging token is sent a new one (`session_token`, to a daemon whose hello says
`session_token_file`; it answers `session_token_result`, and a token it did not store is revoked and
tried again at the next heartbeat). Only the messaging token is re-issued: deleted MCP grants are not.

### Why a session ended

A terminal session also says **why** it ended (`end_reason`) and, when an actor ended it, **what
kind of actor** (`ended_by: {kind}` — `human`, `agent`, `service` or `runner_key`; `null` when
nobody did). Both are in the status, the result, the SSE `end` event and the webhook; the session
page in the browser shows them as one line ("Ended by you", "Stopped automatically when its task
finished", "The process exited on its own", …).

| `end_reason` | Meaning | `ended_by` |
|---|---|---|
| `stopped_by_user` | a person stopped it (the browser's Kill/Delete, or a human token's `stop`) | `human` |
| `stopped_by_agent` | an automated caller stopped it (`stop`, MCP `stop_session`) | `agent` / `service` / `runner_key` |
| `auto_stopped` | the one-shot `auto_stop` ended it after its first turn | — |
| `process_exited` / `process_failed` | the process exited on its own, cleanly / with a non-zero code | — |
| `start_failed` | it never got going; `error_reason` has the detail | — |
| `job_finished` / `job_failed` / `job_disappeared` | the reconciler found its cluster Job exited, failed or gone | — |
| `not_resumed` | a disconnected cluster session nobody resumed within a day | — |
| `daemon_lost` / `daemon_unreported` | its daemon never came back / came back without it | — |
| `parent_ended` | it was a child session and its parent ended for good | — |

The vocabulary is fixed (`internal/db/session_end.go`); nothing a caller sends ends up in it. The
**first** reason recorded wins: a stop is recorded when it is requested, so the process exit it
causes is not mistaken for one nobody asked for, and a later stop or cleanup of an already-ended
row does not re-attribute it. A requested stop whose session is still reported alive more than a
minute later (the kill was lost) is dropped, so a later, unrelated ending is not blamed on it; a
last status the session reports while the kill is landing does not drop it. A session that comes
back (its daemon reports it alive again, a cluster session resumes) forgets its reason. `interrupt` cancels a turn and never ends a session,
so it records nothing. `end_reason` is `""` for live sessions and for sessions that ended before
the runner recorded reasons — treat an unknown value as "ended"; the set may grow.

The runner stores the core account id the actor is or acts for, but never sends it anywhere. The
browser API (`GET /api/sessions`, the browser socket) compares it with the viewer's own account and
sends only the answer, `ended_by.self: true`, so the page can say "you"; every other viewer sees
just the kind. The runner has no names, and the contract endpoints carry the kind alone.

### Start progress

What a session does between "accepted" and "running" is reported as transcript events of kind
`start_stage`, so they persist, replay (a browser reloaded mid-start shows the same progress) and
appear in `…/events` and the SSE stream like any other event. Payload:
`{plan?: bool, runtime?: string, stages: [{id, label, state, detail?, hint?}]}`, where `state` is
`pending` → `active` → `done`, or `failed` (with `detail` saying why and `hint` the next step). An
event with `plan: true` opens a start attempt and lists every stage in order; later events update
stages by `id`; every stage before the furthest active/done one counts as done. A resume opens a
new attempt. Stages are data — a viewer renders whatever list it is given.

| Runtime | Stages | Reported by |
|---|---|---|
| cluster | `queued` → `schedule` (Scheduling pod) → `image` (Pulling image) → `connect` → `clone` → [`plugins`] → `engine` → `ready` | server: queued, and schedule/image from the Job's pod status (polled every 2 s: Unschedulable with its cause, ErrImagePull/ImagePullBackOff, InvalidImageName, CreateContainerConfigError, exit, OOMKilled, eviction); pod: connect, clone (failure classified: repo not found / auth failed / host unreachable), plugins (only when the account has always-on plugins: `Installing plugins`, then `2 of 2 installed` / `1 of 2 installed — frontend-design failed`; a failing plugin never fails the start), engine, missing engine credential |
| daemon / docker | `queued` → `daemon` → [`plugins`] → [`sandbox`] → `engine` → `ready` | server: queued, daemon, sandbox, engine; daemon: plugins (only when the account has always-on plugins and the daemon's hello said `plugins`: `Installing plugins`, then `2 of 2 installed` or a warning naming what failed; a failing plugin never fails the start) |

`ready` is recorded when the session first reaches `running`/`idle`/`waiting`; a failure while
starting marks the stage it was on failed with the session's `error_reason`. Only fixed text and
k8s reason tokens reach a stage — never a raw k8s message or git output (they can carry node
addresses or a token-bearing clone URL). Reading pod status needs `get`/`list` on `pods` in the
sessions namespace (`install/k8s/cluster-runtime.yaml` grants it); without it starts work the same,
but `schedule`/`image` just sit "active" (no Unschedulable/ImagePullBackOff reason) until the pod's
own reports arrive. A session with nothing queued goes `starting` → `idle` as
soon as its engine is up — no "waiting for you" notification for that (see
[Notifications](#notifications)).

### Notifications

The runner tells a person when a session needs them, as a toast in every open runner page and, when
nobody has used one for three minutes, an OS push (never for a private session). The server decides
when and words it (`internal/server/notices.go`) and sends browsers a `session_notice`
(`{session_id, status, title, body, url}`), which they show as given:

- **waiting** (a turn paused for input): at once, "<session> is waiting for your input."
- **idle** (a turn finished): only once nothing the session started still runs — no subagent and
  no background task by the live state's rules (`GET /api/sessions/{id}/live`'s Running count). If
  some still run, the notice waits and goes out when the last of them ends while the session is
  still idle, once: "<session> is waiting for you." The server also looks at a held notice every
  minute, with the live state's stale rule, so a run that never reports its end or goes silent for
  12 hours does not hold it; and a hold 30 minutes old whose only running work is background tasks
  (no subagent) goes out anyway: "<session> is waiting for you (a background task is still
  running)." A new turn, a question or the session's end, the reconcilers' included, drops the held
  notice. (It is held in the server's memory: a server restart in between loses it.)
- **a question** (`blerg-runner ask`): its own toast and push, as before.

A session nobody watches sends none of these, and its questions are listed without a toast
(`message_created` with `quiet: true`) or push: an unattended session (interaction
`unattended`, or an older row a tool or cron started), a cron's (`cron_id`), one started with the
operator key (`started_by_kind` `runner_key`), and one an agent token started (`agent`) unless it
was started `interactive`. So a board's Discuss and board sessions (agent-token starts the board
makes interactive) and its Assist sessions, which a person drives, keep their notices.

### Capabilities (skills & plugins)

What an agent session has loaded — skills, plugins, MCP servers, slash commands, tools and
subagents — is reported as a transcript event of kind `capabilities`. It persists and replays like
any other event, appears in `…/events`, the SSE stream and MCP `get_events`, and is what the
session panel's **Capabilities** tab shows (a terminal session's Skills & plugins dialog says it has
none). Only the latest one describes the session. Payload:
`{engine, engine_name?, model?, version?, cwd?, permission_mode?, note?, groups: [{id, label,
note?, total?, items: [{name, description?, detail?, status?, source?}]}]}`, with group `id` one of
`skills`, `plugins`, `mcp`, `commands`, `tools`, `agents` (in that order). A group the engine does
not report is absent; a reported group with nothing in it has `items: []`. Browsers get the latest
report with `GET /api/sessions/{id}/capabilities` (`{capabilities, seq, ts}`, or
`{"capabilities": null}`).

| Engine | Source | When |
|---|---|---|
| Claude Code (host, sandbox, cluster pod) | its own stream-json `system/init` line | every turn re-inits; emitted only when the report changed (deduped by hash) |
| native loop | the skills, subagent types and tools it discovered | once at start |
| Codex | skills under `$CODEX_HOME/skills` (`.system` = built in) | once at start |
| Hermes | skills under `~/.hermes/skills` (not for a sandboxed session: the sandbox does not mount it) | once at start |
| OpenClaw, terminal (tmux) sessions | nothing — the panel says so; the terminal is never scraped | — |

Claude Code's init lists skill names only; a name matching a `SKILL.md` the daemon found for the
session (project, user or plugin) gets that skill's description and scope. MCP servers are reported
as name, status and a tool count derived from the tool list — their configuration (command, args,
env, headers, URLs) is never read. The working directory is reduced to `~/…` or its last two
components, and plugin paths only ever yield a version-shaped last component. Every payload is
checked on the daemon and again on the server (`protocol.SanitizeCapabilities`): unknown groups
dropped, at most 500 items per group (`total` says how many there were), names capped at 200
characters and descriptions at 1024, control and bidi/format characters removed, URL credentials
masked. Directory scans read only regular `SKILL.md` files, only their first 64 KiB, do not follow
symlinked directories, and stop at 3 levels, 400 directories or 300 skills.

**Adding an engine.** A driver that knows what it loaded emits the event itself (see
`claudeCodeDriver`: build a `protocol.CapabilitiesPayload`, emit it through a `capEmitter` so
unchanged reports are dropped). An engine that cannot report sets `EngineSpec.Capabilities`, a
function of the session's work dir, home dir and whether it is sandboxed, returning groups; the
host calls it once at start. It must be cheap, read-only, bounded and credential-free, and never
run a command. Leave it nil when there is no such source — the panel then says the engine does not
report capabilities.

### Following a session

**Poll.** `GET …/events?after_seq=<highest seq you have>&limit=<1-400, default 200>` returns
`{events: [{seq, ts, kind, payload}], has_more}`. `seq` is monotonic per session and is the cursor.

**SSE.** `GET …/events/stream` serves the same events as `text/event-stream`, framed as
`id: <seq>`, `event: <kind>`, `data: <payload JSON>`. Reconnect with `Last-Event-ID` (a browser
`EventSource` sends it for you) or `?after_seq=`; the header wins. A `: keepalive` comment goes out
after 15 s of silence. When the session becomes terminal the stream sends a final
`event: end` whose `data` is the full result body, then closes. A `404` for an unknown session is
an ordinary error response, never an empty stream.

**Thinking and subagents.** A Claude Code session records each thinking block as an event of kind
`assistant_thinking`, payload `{text}`; it is never the session's answer. While a block is open,
the live stream carries a transient `thinking_started` and then `thinking_done` (with
`parent_call_id` for a subagent's), which the daemon gets by starting Claude Code with
`--include-partial-messages` when the CLI lists it. When the agent starts a
subagent (its `Agent` tool), the runner records the ordinary `tool_call` and, after it,
`subagent_started` `{child_id, agent_type, model, summary}` whose `child_id` is that call's
`call_id`; the call's `tool_result` comes with `subagent_done` (same `child_id`, `summary` the start
of the report or `error: …`, plus `model`, `usage`, `tool_uses` and `duration_ms`). While it runs,
`subagent_update` carries its `model`, running `usage` and `tool_uses`, at most every 5 s. A
background subagent's launch is a `subagent_done` with `background: true`; its real end is a later
`subagent_done` for the same `child_id`, read from the CLI's completion notice. A finished subagent
the agent resumes (Claude Code's `SendMessage`) is a new run: a `subagent_started` with
`resumed: true`, `child_id` the `SendMessage` call's id and `resumed_from` the first run's
`child_id`, ended by a `subagent_done` of its own. Every `assistant_text`, `assistant_thinking`, `tool_call` and
`tool_result` the subagent produced carries `parent_call_id`, the `child_id` of its subagent; the
main agent's own events carry none. A subagent's own text and thinking are streamed only when the
CLI lists `--forward-subagent-text` in its `--help` (asked once per daemon on the host, once per
session in a sandbox); an older CLI still gets the subagent's tool calls and results.

`subagent_done` also carries `is_error: true` when the subagent failed (its Agent call returned an
error, or its background notice's status was `failed`; `killed` and `stopped` mean a person stopped
it); an older daemon leaves it out and says `error: …` in `summary`.

Codex gives every child run its own transcript identity, including follow-ups and runs
rediscovered after a restart. Recovery keeps a bounded allowlist of owned native child IDs,
short display metadata and usage baselines; saved metadata never implies that a child is
still running. When native notifications identify an unknown child, the daemon verifies its
ancestry with native thread metadata before accepting its activity or questions. Same-account
siblings and forks are not authorized merely because they share a session-tree ID.

Codex native questions use the session's private ask/answer route. The pinned CLI (0.162.1)
permits `request_user_input` only in the root thread, so a child sends its question to the
parent; the parent asks, identifies the child and forwards the answer. Unrestricted sessions
enable the pinned experimental `default_mode_request_user_input` feature so this tool is
available in Default mode. This selects a native tool; it does not grant additional provider
or filesystem permissions. Restricted sessions keep that feature disabled. The RPC bridge
also accepts child questions from runtimes that expose them, but only for a verified owned
child's current turn. Child completion closes that child's questions, and interruption closes
all outstanding native questions. Secret questions must use credential setup instead.

**Topics.** `blerg-runner topic` posts `POST /api/sessions/{id}/topics` (`{"action":"start","title"}`
or `{"action":"done","outcome","commits"?}`, the session's own token), recorded as an event of kind
`topic`, payload `{id, title, action, outcome?, commits?}`. `commits` (`topic --done --commit <sha>`)
is `[{"sha"}]`, on a done only: each sha 7 to 40 hex characters, lower-cased, deduplicated in order,
at most 50 (anything else is a `400`). `GET /api/sessions/{id}/topics` (a signed-in person who may
see the session; `ETag`/`If-None-Match`) returns the session's topics as the History tab shows them;
a done row's `commits` are `[{"sha", "url"}]`, `url` the commit's page when the session's repository
is known (its clone URL, else its daemon's origin for the folder) and `null` otherwise.

**Checklist.** `blerg-runner checklist` posts `POST /api/sessions/{id}/checklist` (the session's own
token, as topics; at most 64 KiB): `{"op":"set","items":["…"]}`, `{"op":"add","item":"…"}`,
`{"op":"tick"|"untick","index":N}` (1-based) or `{"op":"tick"|"untick","text":"…"}` (exact,
case-sensitive; of several matches the first not yet in that state), `{"op":"remove","index":N}` or
`{"op":"remove","text":"…"}` (drops that item, the first of several matches; the other items keep their
ticks, commits and diffs; the event's `changed` is the item removed), `{"op":"clear"}`. At most 50
items of 200 characters, cleaned of control characters. The read of the current list and the append
run in one transaction under a per-session advisory lock. Recorded as an event of kind `checklist`
holding the full list after the change: `{items:[{text, done}], op, changed?:{index, text, done}}`;
the reply is that payload (`201`). A refusal is a `400` with a sentence, an ended session a `409`,
the 501st checklist event of a session a `429`.

`GET /api/sessions/{id}/checklist/workers` uses that same own-session credential and active-session
restriction, returns `{workers:[{kind,id,title,status}],truncated}`, and never reads another session's
native workers or non-owned children. At most 100 are returned, current work first; titles are plain
text bounded to 200 characters. `blerg-runner checklist workers` discovers these recorded display IDs
before explicit assignment. A native tool's control/thread ID can differ from Blerg's recorded ID.

The additive work-map contract gives every item an `id`; `in_progress` marks current work, and
`assignees:[{kind:"subagent"|"child_session",id}]` links explicitly recorded workers. `doing` clears
done and marks current work (multiple items allowed), `pending` clears both flags, and `tick` clears
current work. Select with `item_id` instead of `index` or `text` for stable references. `rename` takes
`item`, `move` takes 1-based `position`; both retain identity. `set` creates new IDs; removal retires
one without rewriting history. Legacy IDs derive from the original event ID and position, hashing
long/malformed event IDs whole. New IDs are `item-` plus 32 random lowercase hex characters.

`assign`/`unassign` require `assignee`; `doing` optionally accepts it. Native IDs must have appeared
in this session's subagent events; an unambiguous call-ID alias resolves to its child ID. A Blerg
child must be a direct agent child with the same non-null owning account. Assigning a worker to a
new item transfers it atomically; completion preserves its recorded connection. Current live state
resolves later canonical native IDs without rewriting history; ambiguous or unknown native references
are omitted from live state and the next mutation instead of guessed. At most 32 workers
per item and 128 per list; worker IDs are bounded to 256 UTF-8 bytes without controls; the full saved
payload is at most 128 KiB. `changed` includes the item's ID/state/assignments and, on transfer,
`from_item_id`. Existing session-token auth and event limits are unchanged; none of these fields
creates authority over the assigned worker. This session-internal contract is described by
`x-session-work-checklist` and the checklist schemas in `openapi.json`, outside the public route set.

**Pinned tickets.** `blerg-runner pin` / `unpin` post `POST /api/sessions/{id}/tickets` (the session's
own token; at most 256 KiB): `{"action":"pin","ticket":{…}}` or `{"action":"unpin","source"?,"key"?}`
(neither: the current pin; a key alone when one pin has it). The ticket is `{source, key, title, url?,
status?, type?, priority?, assignee?, reporter?, labels?, description?, fields?:[{name, value}],
comments?:[{author?, at?, body}], acceptance?}`: source, key and title are required; every string is
cut (key 64, title 300, status/type/priority 64, people 120, labels 20 × 64, description 20 000,
fields 30 × (80, 500), comments the last 5 × (120, 40, 2 000), acceptance 30 × 500) and cleaned of
control characters; `url` must be an http(s) address; unknown fields are dropped; a value of the
wrong type is a `400` naming it (`null` reads as absent). A pin is identified by `(source, key)`; at
most 5 at once (`409`), at most 100 ticket events per session (`429`; an unpin is always allowed).
Recorded as an event of kind `ticket`: `{ticket, action: "pin"|"update"|"unpin", previous?:{status,
title}}` (an update is a re-pin of a ticket already pinned; an unpin's ticket holds only source, key,
title, status and url). The reply is that payload with `pinned`, the summaries after it, current
first. Neither route is in the `/api/runner` contract.

**Diffs.** `blerg-runner diff` publishes a patch of the session's folder as a `.diff` artifact (view
`diff`); `checklist tick|add --commit` publishes the diff of the item's commits and keeps its artifact
id on the item (`diff`). See [Diffs](../docs/artifacts.md#diffs).

**Code colors.** `GET`/`PUT /api/me/preferences` keep the person's chat code color choice
(migration 057); an app's chat proxy reads it with `GET /api/runner/preferences?account=<id>`.

**Live state.** `GET /api/sessions/{id}/live` (the same people as `/topics`; `ETag`/`If-None-Match`,
`Cache-Control: private, no-store`) answers what the session panel's Live tab shows, read from the
session's whole event log: `{session_id, through_seq, generated_at, tokens_hidden, asks:[{event_id,
message_id, question, asked_at, seq}], subagents:[{key, title, type, model, tokens, doing, started_at,
active_at, background, seq}], tasks:[{task_id, call_id, kind, description, started_at, seq}],
tool_changes, counts:{needs_you, running}}`. An ask is open until it is answered, its message is
closed or the session ends (one the native loop asked also closes with its turn); a subagent runs
until its final `subagent_done` (a foreground one only while its turn runs and no message but the system's has come since it started, as in the chat); a task until its
`background_task_done`; anything with no sign of life for 12 hours is left out. Tokens follow the
Insights rule, and `tool_changes` is the caller's own pending tool changes on the session. The page
folds the events after `through_seq` into the reply until its next read. Migration 042 adds the
partial index the read uses (`agent_events_live_idx`). The reply also carries `checklist` (the newest
list, `{items, event_id, updated_at}`, `null` when there is none or it was cleared), `plan` (the
engine's own plan, `{items:[{text, status: pending|in_progress|completed, active?}], updated_at}` or
`null`: Claude Code's top-level `TaskCreate`/`TaskUpdate` calls folded, a task's id read from its
result, or the latest `TodoWrite` list, or Codex's latest native plan update; a subagent's calls are left out), `tickets` (the pins'
summaries `{source, key, title, status, url, event_id, seq, updated_at}`, current first) and `ticket`
(the current pin in full, with `event_id` and `updated_at`, or `null`); migration 055 indexes the three
reads.

The `children` list additionally carries recorded `tool_calls`, bounded `activity`,
`activity_since`, `last_activity_at`, `usage_status` (`pending|reported|hidden`),
reported `tokens` and optional price estimates. A `local` flag describes the
owning daemon's endpoint classification; it does not invent a zero dollar cost.
Current-turn `usage_updated` snapshots are nonbilling display telemetry and are
replaced at completion. See [Live child telemetry](../docs/telemetry.md#live-child-telemetry).

**Background tasks.** When a Claude Code agent leaves something running (a `Bash` call with
`run_in_background`, a `Monitor`, a background subagent), the runner records
`background_task_started` `{task_id, call_id, kind, description, started_at}` once the call's result
names the task (`kind` is `bash`, `monitor` or `agent`; `call_id` is the call that started it), and
`background_task_done` `{task_id, call_id, kind, status, summary}` when the CLI reports it ended
(`status` as the CLI says, e.g. `completed`, `failed`, `killed`, `expired` for a monitor, or `stopped`
when the engine process ended under it; `summary` at most 120 bytes). A resumed run of a background
subagent is a task as its launch was: kind `agent`, `task_id` and `call_id` its `SendMessage` call's id.

**Messages from the agent.** In an agent session, `blerg-runner ask`, `update` and `note` are
recorded as transcript events of kind `ask`, `update` and `note`, payload `{body, message_id}`
(`message_id` is the id in the messages API; it can be missing on older events and on a native
agent's updates and notes). An ask is answered with `POST /api/messages/{message_id}/reply`
`{"answer": "…"}`, or from the runner's browser socket by an `agent_user_message` carrying
`answer_message_id`; and a plain chat message (source `chat` or none) sent over that socket while
one of those asks is open answers the newest open one instead of reaching the engine, which is
blocked inside the ask. The answer is recorded as `user_message` with `source: "ask_answer"` and the
ask's `message_id`, and the websocket broadcasts `message_answered` (`{message: {id, status,
answer}}`, with an empty answer for an ask that expired). Nothing is recorded when an ask expires,
so a client closes a card that carries a `message_id` on `message_answered`, on `initial_state`
(which lists the open asks) or when the session ends — not when the turn ends or when the asking
command returns, since the ask can outlive both. `POST …/message` (REST and the MCP `send_message`)
with source `human`, `chat` or `runner` answers the newest open ask too (the response says
`answered_ask: true`); a message from a board (`blerg-board`) never does. An update or note is
reached the same way, and a reply to a note arrives as `user_message` with `source: "note_reply"`. An
engine's command approval is an owner-only ask (posted with `"owner_only": true`): only the session's
owner, signed in as themselves, answers it, by any of those routes. The reply endpoint answers 403 to
the daemon token, board tokens, agent tokens and other people, and their chat messages are not taken
as its answer; the board's chat relays with the board's automation token, so an approval is answered
in Runner. The daemon may still close one it stopped waiting on with an empty answer (a deny).

**Errors, retries and system text.** `user_message` carries `source`, an optional `client_id` (the id
the sender gave the message, echoed so a client can match its queued bubble; a message dropped
because the queue was full is reported as an `error` carrying that `client_id`) and, for an
answer, `message_id`. A text the daemon wrote and a person did not type has `source: "system"`: the
restart note ("[system] Session resumed after a restart…") and the output of a slash command such as
`/cost` (the body of `<local-command-stdout>`, tags removed, possibly several lines). A slash skill
is recorded with the text the person typed. A failed model API call is an `error` event with
`code: "api_error"` and `retryable: true`, never an `assistant_text`; a turn that ends in failure
ends with `turn_done` whose `stop_reason` is `"error"`, after an `error` event with the reason (the
engine's error text, or its first listed error). A retry in progress is an `error` with
`code: "api_retry"` and `retryable: true`, with `attempt`, `max_retries` and `retry_in_ms` when the
engine says them: it is a notice, the turn continues, and it ends nothing.
A Claude Code compaction is a `compaction` event with an empty `summary` and `trigger` (`manual` or
`auto`) and `pre_tokens`. Fields are additive: an older daemon omits them, and records an API
failure as the agent's text.

**Times, order and gaps.** `ts` is the daemon's time for the event, kept within ten minutes of the
runner's clock. `seq` follows the order events are stored, which follows emission order (a failed
store holds the session's later events back while the daemon keeps re-sending the refused event,
and lets go two minutes after its last attempt; a payload the database rejects outright is never
held for). `…/events/stream` and a
re-poll from `after_seq` are always complete. On the live stream and the browser socket, a
subscriber that falls behind can have live events dropped: the websocket then sends
`{"type": "agent_events_gap", "session_id": "…", "from_seq": N}` (the lowest dropped seq) and the
client asks again with `subscribe_agent_events` and `after_seq: N-1`, deduplicating on
`client_event_id`; `…/events/live` refills the gap itself, inline and in seq order, with no new frame.
Live order is not strictly seq order, so a client that sees a hole in the seqs should wait a moment
(the chat package waits one second) before fetching it.

**Live stream.** `GET …/events/live` is what the runner's own browser gets over its websocket,
for the credential that started the session: it is fed by the hub, not by polling the table, so
it carries the typing deltas and the status changes the polling stream cannot. It is what a chat
UI (`@blerglab/chat`, below) wants; a broker that only needs the durable record should keep
using `…/events/stream`. Frames are `event: <type>` + `data: <json>`, with `id: <seq>` on
persisted events only:

| Frame | Data | When |
|---|---|---|
| `agent_event` | the event as the browser receives it: `{type, session_id, client_event_id, seq, ts, kind, payload}`; a typing delta, or a thinking block's start or end (`thinking_started` / `thinking_done`, no text), has `transient: true` and no `seq` | the opening page, then live |
| `replay_done` | `{session_id, last_seq, has_more, server_time}`; with `tail=1` also `older: true, has_older, first_seq` | once, after the opening page |
| `status` | `{session_id, status}` — `starting`, `running`, `waiting`, `idle`, `disconnected`, `stopped`, `ended` or `error` — plus `message`, `end_reason`, `ended_by` when known | once at open, then on every change |
| `end` | the result body | when the session is over; the stream then closes |

`?after_seq=N` replays forward from a cursor; `?tail=1&limit=200` opens from the end of the
transcript with the newest page, and the older part follows from
`GET …/events?before_seq=<first_seq>&limit=` (oldest first, `has_older` until the start). An
event may arrive both in the replay and live; deduplicate on `client_event_id`, as the browser
does. Opening replay pages also include up to three older lifecycle events for each referenced
subagent, so a completion outside the displayed window still determines its card’s status.
Use the replay marker’s `first_seq` and `last_seq` for pagination, not the minimum seq of these
sparse context rows. Older context keeps its original seq in JSON and omits the SSE `id:` line
so it cannot rewind the reconnect cursor. A `: keepalive` comment goes out after 15 s of silence. A `404` for a session the
credential may not see is an ordinary error response, never an empty stream.

**Webhook.** A session started with a `callback_url` gets one `POST` of the same result body when
it reaches a terminal lifecycle — including a session that died before it began, and a session
ended by `stop`.

- Headers: `Content-Type: application/json`, `X-Blerg-Event: session.completed`,
  `X-Blerg-Session: <session id>`, `X-Blerg-Delivery: <delivery id>`, and — only when you supplied
  a `callback_secret` — `X-Blerg-Signature: sha256=<hex HMAC-SHA256 of the exact body>`. The header
  is absent rather than unsigned when there is no secret: a receiver that checks for its presence
  is never handed something that merely looks verified.
- Retries: the initial attempt, then up to three more at **5 s, 30 s, 120 s**. Each attempt has a
  10 s timeout. A `2xx` ends delivery; so does a `4xx` (the receiver's verdict, which retrying
  cannot change) and a refused redirect. Network errors and `5xx` are retried. Every attempt is
  appended to the session transcript as a `webhook` event carrying `{delivery_id, attempt, status,
  ok, reason}` — never the URL, the secret or the body.
- Redirects are never followed: a `3xx` would ask the runner to send a signed body somewhere the
  caller never named.
- **SSRF rule.** The address actually dialled is checked on every attempt (no connection reuse, and
  the checked IP is dialled rather than the name, so DNS rebinding gains nothing). Loopback,
  private, link-local, unspecified, CGNAT `100.64/10`, `0.0.0.0/8`, `198.18/15` and `240/4` are
  refused, as are IPv4-mapped-IPv6 and NAT64 spellings of them; a hostname with mixed records is
  dialled on a public address or not at all. Set
  [`BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE=true`](#server-cmdserver) when your receivers legitimately
  live beside the runner — that switch is also what permits a plain `http://localhost` callback at
  all: without it such a URL is rejected at start with a `422`.
- **At most once, not exactly once.** Delivery is claimed in the database before the first byte
  goes out, so several paths marking one session terminal produce one delivery — but a runner that
  crashes mid-delivery never re-sends. Treat the webhook as the fast path and `GET …/result` as the
  source of truth.

### Result

`GET …/result` (and the SSE `end` event, and the webhook body, and the MCP `get_result` tool) all
return the same object, with every field always present:

```json
{
  "session_id": "…", "lifecycle": "ended", "terminal": true, "runtime": "cluster",
  "repo": "example-repo", "git_url": "https://example.com/org/example-repo.git",
  "branch": "wip/<session_id>", "last_assistant_message": "…", "error_reason": "",
  "end_reason": "stopped_by_agent", "ended_by": {"kind": "agent"}, "auto_stop": false,
  "started_at": "2026-09-25T12:00:00Z", "ended_at": "2026-09-25T12:31:00Z", "artifacts": []
}
```

`branch` is the convention the session runtime names its work after, derived from the session id.
`last_assistant_message` is the main agent's last text in the session's **last turn** only
(streaming deltas are not persisted, and a subagent's text is not the session's), found within the
last 400 events; while a turn runs, that is the turn in progress. A turn that ended in failure
(`stop_reason: "error"`) has no answer, so the field is empty, and the reason for the failure is in
`error_reason` (when the session has no error of its own, the newest error of that turn that is not a
retry notice). An earlier turn's answer is never reported in its place, and a model API failure's
text is never taken for an answer. `artifacts` is reserved and always empty in v1.
There is deliberately no `callback_secret` field.

### Session files

The files of a session — what its agent published with `blerg-runner publish`, and what a person
attached from the chat — are reachable with the same credential that drives it, under the same
rule as `message`: an agent token reaches the sessions its owner started, a private session only
its owner, and anything else is the uniform `404`. The handlers are the browser routes' own, so
the bytes, the server-classified content type, the hardening headers (`nosniff`,
`Content-Security-Policy: sandbox`, `no-store`) and the caps are identical.

| Request | Answers |
|---|---|
| `GET …/artifacts` | `{artifacts: [{id, name, size, content_type, view, origin, version, latest_version, created_at}]}`, newest first. `origin` is `agent` or `user`; a file published again under the same name is the next `version` of it |
| `GET …/artifacts/{aid}/raw` | the bytes for an in-app viewer: images and PDFs with their type, everything else as `application/octet-stream` |
| `GET …/artifacts/{aid}/download` | the bytes as an attachment, named after the file (an older version as `<stem>-v<N><ext>`) |
| `DELETE …/artifacts/{aid}` | `204`. Whoever may see the session may delete its files, whichever origin they came from |
| `POST …/uploads` | the body is the file, `X-Artifact-Name` (percent-encoded) or `?name=` its name; stored with `origin: user` and attributed to the account the credential acts for. `201` with the stored file (`previous` is the version it follows). 100 MiB per file, 100 attachments and 500 MiB per session, 20 versions per name (the sizes are configurable, see below, and the status above reports them); `409` on an ended session or a reached cap, `413` for a file over the limit (the message names both sizes; send `Content-Length` and it is refused before the body is read) |

An `artifact` event in the transcript announces each file the agent publishes, with the same
fields as a list entry minus `latest_version` and `created_at`.

### Sessions starting sessions (session-token routes)

These private routes accept the parent's own session token (or its bound runtime token), not an
agent token or a daemon master token. They are separate from the public agent contract and MCP
operations. The parent must have a human owner and run on its desktop daemon; child sessions cannot
delegate again. For child operations, another parent's child, the parent itself, and unrelated
sessions all return the same `404`.

| Route | Body and response |
|---|---|
| `GET /api/sessions/{id}/budget` | Provider capacity from the parent's desktop; unknown data remains unknown. |
| `POST /api/sessions/{id}/children` | `Idempotency-Key` required. Allowlisted body: `engine`, `model`, `effort`, `repo` or `no_repo`, `runtime` (`docker` or `daemon`), `title`, `prompt`, `keep_running`. Returns `201` with a child, or `202` with an approval request. Unknown fields return `422`; limit failures return `429` and `Retry-After`. |
| `GET /api/delegation-requests/{id}` | Request status; optional `wait` up to 25 seconds. Only its parent's token or its human owner can read it. |
| `POST /api/delegation-requests/{id}/expire` | Parent cancels its pending request; an already allowed request returns its outcome. |
| `POST /api/delegation-requests/{id}/decide` | Human with a live browser session only: deny, allow once, allow for the session, or create a standing rule, from the stored request. |
| `GET /api/delegation-rules` | Human owner's standing rules. |
| `POST /api/delegation-rules` | Human owner creates a rule for an engine, desktop and runtime across repositories, optionally restricted to a local endpoint. |
| `DELETE /api/delegation-rules/{id}` | Owner removes a rule; existing children continue. |
| `GET /api/sessions/{id}/child-approval` | Human owner with a live browser login only. Current-parent session permissions, eligible engines per runtime, standing desktop rules, effective authority and an opaque revision. |
| `PUT /api/sessions/{id}/child-approval` | Same owner login only: `{runtime:"docker"|"daemon",enabled:boolean,revision}` atomically sets or revokes exact current-daemon session grants. A stale revision returns `409`; reload before explicitly trying again. |
| `POST /api/delegation-grants/{id}/revoke` | Owner revokes a session grant; existing children continue. |
| `GET /api/sessions/{id}/children` | `{children:[...]}` with status, lifecycle, model, effort, runtime, asking, delegation and keep-running fields. |
| `GET /api/sessions/{id}/children/{child}` | Session status plus authoritative `open_asks:[{message_id,body}]`. |
| `POST /api/sessions/{id}/children/{child}/message` | `{text,client_id?}`; `202`, including `answered_ask` when it answers an open question. |
| `POST /api/sessions/{id}/children/{child}/model` | `{model?,effort?}`; `202`. |
| `POST /api/sessions/{id}/children/{child}/interrupt` | Interrupts the current turn; `202`. |
| `POST /api/sessions/{id}/children/{child}/stop` | Stops the child; `200 {status:"stopped"}`. |
| `GET /api/sessions/{id}/children/{child}/result` | The runner's normal result object. |
| `GET /api/sessions/{id}/children/{child}/events` | `after_seq`, or `before_seq` (zero means tail), `limit`, optional `wait` up to 25 seconds; `{events,has_more}`. |
| `POST /api/sessions/{id}/children/{child}/uploads` | Raw bytes and artifact-name header, with normal upload caps; `201`. Stored as an owner upload and announced as `session_upload`, preserving the parent's attribution without ending the child's working state. |
| `GET /api/sessions/{id}/children/{child}/artifacts` | `{artifacts:[...]}` using ordinary artifact visibility checks. |
| `GET /api/sessions/{id}/children/{child}/artifacts/{aid}/download` | File bytes with the normal artifact hardening headers. |

The proactive approval controls are for the person's browser, never a session, agent, delegated,
or daemon credential. The GET and PUT response is `{revision,daemon_id,allowed_engines,runtimes}`.
Each runtime reports `enabled`, `partial`, `allowed_engines`, `granted_engines`,
`standing_rule_engines`, `effective_allowed_engines`, `can_enable`, and a string `reason` (empty
when enabling is available). Arrays are never null. An offline or ineligible parent can still
revoke existing scopes; it cannot enable new ones. A direct personal parent must be unrestricted,
active, and on a connected desktop that advertises recovery-ready and available engines.

Enabling applies only to the engines currently registered and available for that runtime on the
parent's current desktop. It does not cover later installations or change another runtime. Host
execution needs a separate explicit change and is unavailable to a sandboxed parent. The opaque
revision binds the parent lifetime, desktop, eligibility, current session grants and applicable
standing-rule state. After an engine upgrade, concurrent edit, revocation or lifetime change,
reload and make a new explicit choice; do not automatically retry a stale write. Repeating a set
with the current revision is idempotent. Ending the parent invalidates its grants, including when
the same session ID is later resumed. Recovery within the same lifetime retains valid grants.

Disabling revokes session grants for this runtime and desktop, including an earlier **Allow for
this session**, while preserving its decision audit. It leaves existing children running and
never removes a standing desktop rule. `standing_rule_engines` and `effective_allowed_engines`
make that distinction explicit. Changed scopes are recorded atomically as a
`delegation_permissions_changed` audit event with runtime, desktop and opaque grant
IDs (also present in child delegation metadata); the owner account remains in the private grant rows; proactive grants have no fabricated approval-request ID. No production permission is enabled
by an upgrade or by reading this endpoint.

| CLI | Purpose |
|---|---|
| `blerg-runner budget [--json]` | Read provider capacity before selecting an engine. |
| `blerg-runner session start ...` | Start a child or wait for its approval. |
| `blerg-runner session wait <request>` | Resume waiting on a pending request. |
| `blerg-runner session list` / `watch <child>` | List children or follow completed messages, tools and open questions. |
| `blerg-runner session discard <child>` | Stop and archive a worker; retain its history, Insights totals and direct URL. |
| `blerg-runner session send <child> <text>` | Send a message; agents must leave destructive approval questions to the person. |
| `blerg-runner session upload <child> <file>` / `files <child>` / `download <child> <artifact>` | Exchange files through the artifact service. |
| `blerg-runner session model <child> ...` / `interrupt <child>` / `stop <child>` / `result <child>` | Change settings, interrupt, stop or read results. |

Waiting commands default to 100 seconds and exit with code 3 when still waiting, printing a command
to continue. The request remains pending until answered, canceled, or its ten-minute server expiry.
Use a shell timeout greater than `--timeout` when raising it (maximum 540 seconds). A failed CLI
wait never means that approval was denied. File downloads refuse existing destinations and symlinks;
authenticated calls refuse redirects rather than forward credentials.
Reusing an identical pending request can return `200` with that same request. Expiring an already
allowed request returns `409` with its existing outcome rather than canceling the child.

Limits are three running children and twenty starts per parent, six running children and sixty
starts per account per hour. A real parent end stops non-kept children and invalidates old grants,
including across daemon restart; a pause only expires the requests pending at that pause. New child
starts require the daemon's recovery-ready handshake. Host sessions share their home and can read
other host sessions' token files; use a sandbox for isolation. The injected CLI guide is delivered
when a session starts, so reinstall the daemon and start a new session after upgrading.

### UI package

The chat surface the runner's own web app is built on is a package, `@blerglab/chat`, that an
app built on this contract can install from the runner itself and wire to its own backend. A
build that packed it (the frontend build writes `frontend/dist/packages/manifest.json`, the
tarball and the README beside it) serves:

| Request | Answers |
|---|---|
| `GET /packages/` | `{packages: [{name, version, file, sha512, url}]}` |
| `GET /packages/<file>` | the tarball, `application/gzip`, `Cache-Control: public, max-age=31536000, immutable`. `npm i <url>` works from anywhere that reaches the runner; `sha512` is the integrity string for a lockfile |
| `GET /packages/@blerglab/chat/README.md` | the package's README, `text/markdown` |

and `GET /agents` advertises it as `ui.chat_package: {name, version, url, sha512, docs_url}`
(the Markdown rendering gets a "UI" section), so a session reading the manifest learns what to
install, how to pin it and where to read. All of it is public, like `/agents`; only the names
the manifest lists are ever served. A build without the package serves nothing, carries no `ui`
field, and answers `404` on `/packages/`.

### MCP

Native Claude Code and Codex sessions report `mcp_status` agent events without sending a model
prompt. `GET /api/sessions/{id}/live` includes the latest `mcp` snapshot: `servers` (name,
status, source and tool count only), `settled`, `since` and `engine`. Status is `pending`,
`connected`, `failed`, `needs-auth`, `disabled` or `unknown`. Server endpoints and credentials
are excluded. Restricted sessions report their own gateway inventory.

The daemon polls native status at most once every two seconds, stops after all servers settle
or 120 seconds, and starts tracking again after a native process replacement. At the deadline,
`settled: true` means tracking has finished; unresolved servers keep their reported status.
Claude Code uses its persistent stream-json `mcp_status` control request; Codex uses
`mcpServerStatus/list` for the current thread. Older or legacy engines without that control
channel do not claim live readiness. No prompt is sent to discover tools.

A new session's first native input waits for known pending servers for at most 30 seconds.
Initial inventory discovery is bounded to six seconds if no servers are known, within that
same 30-second cap (or a smaller configured wait). Later inputs
and restored conversations are not held. A held input remains in the driver's FIFO; native
process cancellation retains it for retry. While held, `mcp.hold` and the live state's
`mcp_hold` both contain `since` and `until` timestamps. Clients should expire a cached hold
at `until`. Release clears both fields and reports `released: settled|timeout|person` in
the status snapshot. Only the owner may send
`{"type":"release_held_prompt","session_id":"…"}` on the browser socket. Repeated release
is harmless and never starts an engine. Old daemons omit these fields; old servers accept
the additive event without interpreting it.

`POST <runner>/mcp` speaks JSON-RPC 2.0 over plain HTTP with the same bearer credential as the REST
routes (`initialize` at protocol revision `2025-06-18`, `ping`, `tools/list`, `tools/call`;
notifications get a bare `202`; anything else is `-32601`). `GET /mcp` is a `405`. Each tool is a
thin wrapper over the same function the REST handler calls — not an HTTP round-trip — so validation,
idempotency and error text cannot drift between the two surfaces. Every failure — a contract
rejection, a malformed argument object, a tool that does not exist — comes back as `isError: true`
with one shape: `{"error": …, "status": …}`, the status being what the REST path would have
answered. The one message that is deliberately not shared is a *type* error in the arguments:
REST decodes a request body and MCP decodes a tool-call argument object, so the wording differs.

| Tool | What it does |
|---|---|
| `start_session` | start a session on a repo and return its `session_id` (`repo`, `prompt`, `title`, `model`, `effort`, `engine`, `runtime`, `idempotency_key`, `callback_url`, `callback_secret`, `git_url`, `env`) |
| `get_session` | where a session is: lifecycle, runtime, resumable, error reason, why it ended |
| `send_message` | send a conversational turn (`session_id`, `text`, `source`); resumes a disconnected cluster session |
| `interrupt_session` | cancel the turn in flight, leaving the session alive |
| `stop_session` | end a session for good and free its slot |
| `get_events` | a window of the transcript (`after_seq`, `limit`) |
| `get_result` | the structured outcome in one object |

`start_session`'s schema marks `prompt` required although the REST contract requires only `repo`;
an MCP client that validates locally will refuse a promptless start that REST would accept. Its
`idempotency_key` may be omitted, but not sent empty: an empty string is a `400 idempotency key
must be 1-128 characters`, the same rejection an empty `Idempotency-Key` header gets, rather than
a silently dropped retry guarantee.

### MCP connections and crons (human-only routes)

These routes are for a signed-in person's browser session only: they need a human token **with a
login session** (an agent token, even one holding `session.start`, gets `403`, and no token `401`),
and another account's connection or cron is always `404`. They are deliberately **not** part of the
agent contract, so they are in neither `openapi.json` nor the MCP server. Concepts:
[`docs/mcp-connections.md`](../docs/mcp-connections.md) and [`docs/crons.md`](../docs/crons.md).

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/mcp/connections` | the caller's connections (from core, no secrets) with each one's default tools |
| `GET` | `/api/mcp/connections/{id}/tools` | the connection's live tool list: name, description, input schema, annotations and the hash a launch will pin |
| `PATCH` | `/api/mcp/connections/{id}/defaults` | save the default tool selection, `{"default_tools": {tool: {"mode": "allow", "hash": ...}}}`; `{}` clears it |
| `GET` `POST` | `/api/crons` | list and create crons (creating mints the cron token) |
| `GET` `PATCH` `DELETE` | `/api/crons/{id}` | read, edit and delete (delete revokes the token and stops a running session) |
| `POST` | `/api/crons/{id}/run` · `/pause` · `/resume` · `/renew` | run now, pause, resume and renew the token |
| `GET` | `/api/crons/{id}/runs` | run history |

A session with MCP connections is started from the launch sheet with an `mcp` field on
`POST /api/sessions`, which only a signed-in person may send; `POST /api/runner/start` and the
runner's own MCP `start_session` tool refuse it.

### Model lists

`GET /api/models/{engine}` answers `{models:[{id, name, description, section, efforts[],
default_effort?, effort_kind?}], source, fetched_at, daemon_id?, engine_efforts}` — one engine-neutral shape for
every engine. `section` is `main` (the headline choices; listed first) or `overflow` (older and
alternate versions). `efforts` is the subset of the engine's effort allowlist the model accepts,
lowest first, and is empty for a model that takes no effort (Claude's Haiku). `default_effort` is
absent when the engine does not name one (Hermes): sending no `effort` then leaves it to the
engine's own configuration. An engine with no list answers `200` with `models: []` and
`source: "none"`: there is nothing to pick, and a session on it runs on that engine's own
configured default. Readable with any credential that can start a session, and with a signed-in
browser session (the launch sheet uses it).

Where each engine's list comes from:

| Engine | List | Source | Effort flag |
|---|---|---|---|
| `claude` | the public Claude Code model catalog (what Claude Code's own `/model` offers), fetched by the **server** | `live` / `builtin` | `--effort <level>` |
| `codex` | `codex debug models` — the account- and CLI-aware catalog — run by each **daemon**; only models Codex itself lists (`visibility: "list"`), in its `priority` order, with each model's own levels and default | `daemon` | `-c model_reasoning_effort=<level>` |
| `hermes` | `GET <base_url>/models` of the OpenAI-compatible endpoint in the daemon host's `~/.hermes/config.yaml`, fetched by each **daemon** — only when that config says `provider: custom`; the configured `default` model first. Every model is offered every Hermes level with no default (Hermes does not say which a model supports), so the sheet shows **Auto**, which sends nothing | `daemon` | `--reasoning <level>` |
| `openclaw` | none | `none` | — |

For `claude` the runner server fetches the catalog (see `BLERG_RUNNER_MODEL_CATALOG_URL` below;
https only, and redirects only to the same host) at startup and every 6 h, keeps only models
offered on the first-party API, validates every id and effort level, and keeps the last good copy;
until a fetch succeeds — or for good when fetching is disabled — it serves the list compiled into
the build (`source: "builtin"`).

For `codex` and `hermes` the list is per workstation — it depends on that machine's Codex login and
Hermes config — so each daemon probes it (at start, on every reconnect, then every ~15 min with
jitter; off the heartbeat and spawn paths) and reports it in its `daemon_hello` and, only when a
list changed, a `daemon_heartbeat` (`engine_models`). Pass `?daemon_id=` for that daemon's own list
(`source: "daemon"`, with `daemon_id` and `fetched_at`); a daemon that reported nothing answers
`source: "none"`, never another daemon's list. Without `daemon_id`: the one daemon that reported a
list, or — with several — their union, taken in daemon-id order with the lowest id winning a model
both list (no `daemon_id`, the oldest `fetched_at`). A probe that fails transiently (our own
timeout or output cap, a probe that ignores its deadline — abandoned, and not restarted until it
returns — an unparseable answer or an unreadable Hermes config) keeps the last good list; a
definite "nothing" (the CLI is not installed or exits non-zero on its own, e.g. logged out; Hermes
is not on a `custom` provider or has no config; the endpoint answers 401/403) reports an empty one.

The New session sheet uses the target-scoped launch-options contract above. The compatibility
`GET /api/models/codex?runtime=cluster` route also accepts a verified account-bound human or
agent principal. This probes the
configured agent image using that account's credential (or the operator credential when no
personal one exists), in a bounded temporary Job, rather than using a workstation's catalog.
A native app-server `model/list` request must create a fresh account catalog; bundled fallback
models are rejected.
The picker preserves the CLI's model order, supported efforts and default for each model.
Catalog probes receive access tokens only, so reading a model list cannot rotate the durable
login's refresh token. Expired credentials and failed probes show an error in the sheet.
A Codex login pasted into Settings is a snapshot, not a synchronized host login: replace it
there after the source login is rotated or revoked. Cluster sessions now show an actionable
expired-login error while remaining available to retry after repair.
The endpoint returns an account/image-scoped cached snapshot immediately. Successful catalogs
are persisted in the runner database and reloaded at server startup. Catalogs older than 15 minutes
refresh in the background with a live browser proof; neither credential lookup nor pod startup
blocks the response. `refreshing: true` tells the modal to poll while keeping launch controls
usable. With no previous catalog, the engine's default and an optional model name remain
available. A failed refresh preserves the previous list and exposes a safe `error`; Retry requests
`refresh=1`. The modal stops polling when closed, preserves typed model choices and bounds any
stalled HTTP request at ten seconds. The server needs `get` access to `pods/log` in the session
namespace (included in `install/k8s/cluster-runtime.yaml`).

Workstation lists are not scoped per user: any credential that can start a session reads every connected
daemon's lists (model names only). That matches the runner's single-tenant model, in which every
signed-in user can already see and launch on every daemon.

Everything a probe reads is untrusted and is checked on the daemon *and* again on the server
(`models.SanitizeReported`): ids must pass the engine's model rule (so nothing flag- or
shell-shaped survives), efforts are cut to the engine's allowlist, counts and lengths are capped,
and control and bidi/format characters are stripped from names and descriptions; the server also
replaces a missing, pre-2020 or future `fetched_at` with the time it received the report, and caps
every daemon frame (4 MiB for the hello, which is read before the token is checked — a measured
worst-case legitimate hello is ~0.75 MB — and 64 MiB after, the size of a full 50,000-line
scrollback capture). The Codex probe runs the CLI with no stdin, in its own process group killed
after 10 s, with stdout capped at 1 MB (today's output is ~230 KB). The Hermes probe never shells
out: it reads only `provider`, `base_url` and `default` from the `model:` block of the config with
a tiny line parser (no API key is ever read; the file must be a regular file of at most 1 MB, and a
value it cannot read exactly is an error, not a guess), refuses anything but `http`/`https` and a
URL with embedded credentials, sends a bare `GET` with no auth, follows no redirect, and gives up
after 3 s or 256 KB. The endpoint is the user's own, so a private (LAN) address is fine.

**Minimal environment, and proxies.** Probes do not inherit the daemon's environment. The Codex
CLI gets only `PATH`, `HOME` and `CODEX_HOME` (if set); the Hermes request uses no proxy at all.
Proxy variables (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`) and `SSL_CERT_FILE` are deliberately
not passed: a proxy URL can carry credentials. So on a machine that reaches the internet only
through a proxy, `codex debug models` may fail to fetch the account-aware catalog — it then exits
non-zero (no list, and the free-text box appears) or falls back to its own bundled catalog — and a
Hermes endpoint reachable only through a proxy gets no list. This concerns the lists only:
sessions start from the daemon's usual session environment, not the probes' one. When an engine
reports no list the daemon log says why, once (`models: codex reports no model list: …`, with the
tail of the CLI's stderr when it exited non-zero).

The launch sheet asks for the list of the daemon it is about to launch on, shows the `main` models
as pills and the rest under **More models**, then an **Effort** row for the selected model (its
default preselected, or **Auto** when it has none), and sends the full model id plus `effort` for
both terminal and agent sessions. It does not ask until the daemon is known, and a daemon-reported
list asked for without a daemon is never shown (neither in the sheet nor in a session's chat). When
no list was reported for Codex or Hermes (an older daemon, a probe that found nothing) it shows an
optional free-text **Model name** instead — checked against the engine's model rule before it can
be sent, kept apart from the list pick and cleared when the engine, runtime or daemon changes —
with an **Effort** row from the engine's allowlist (`engine_efforts` in the answer), **Auto**
first; never a stale built-in list. A cluster session runs on no daemon, so a daemon's list is
never offered for one (the free-text box is). For a model
the list knows, the start is refused (`422`) unless the effort is one of that model's own levels;
an alias such as `sonnet`, a typed name, or a daemon-reported list when the daemon is not known
(`POST /api/runner/start`, a cluster session) gets the engine's allowlist only. The daemon passes
model and effort through each engine's own flags (table above) — in tmux, in agent-kind turns, and
inside the sandbox; the cluster pod gets them as `BLERG_RUNNER_MODEL` / `BLERG_RUNNER_EFFORT`. The
launch effort is recorded on the session. A tmux session's command is passed to tmux as separate
arguments, which tmux ≥ 3.0 runs without a shell — required for a bracketed alias such as
`sonnet[1m]` to reach the CLI intact.

**Hermes in the Local sandbox.** A sandboxed Hermes session reaches a model server on the LAN: the
container joins the `blerg-sandbox` network when it exists (else Docker's default bridge), and both
are ordinary NAT'd bridge networks — `blerg-sandbox` is not an `internal` network — so outbound
traffic to a LAN address works exactly as from the host. `~/.hermes/config.yaml` and
`~/.hermes/.env` are mounted into the container (individually, read-write), so the session uses the
same provider and `base_url` the list was probed from. The one case that differs: a `base_url` on
`localhost`/`127.0.0.1` (a model server on the same machine) means the container itself inside the
sandbox, so the list appears (the probe runs on the host) but a sandboxed session cannot reach it —
use the host's LAN address in the config, or run on **This machine**.

### Run Hermes on a local model (vLLM)

Serve a tool-capable model from an OpenAI-compatible vLLM endpoint on your LAN. Use the
model's documented tool-call and reasoning parsers; a successful `/v1/models` response alone
only proves discovery, not that an agent can call tools correctly. Start with a context and
concurrency limit that fit your machine, then test a complete tool call and its response.

In the desktop daemon account's `~/.hermes/config.yaml`, set the `model:` block:

```yaml
model:
  provider: custom
  base_url: http://model-server.local:8000/v1
  default: your-served-model-name
  context_length: 65536
```

Replace the example address with your server's LAN address and use exactly the model id returned
by its `/v1/models`. Current Hermes versions require at least 64,000 tokens of context; the
example uses 65,536. Match `context_length` to the context the vLLM server actually supports;
changing this client setting does not enlarge the server's context.

The hostname must resolve only to private or loopback addresses for Blerg
to classify the endpoint as local. Use a LAN address for a Docker child: loopback inside its
container points to the container itself.

For local classification, Hermes's `~/.hermes/.env` must contain no nonempty provider key/token
assignment, and the daemon environment must contain no nonempty `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, or `OPENROUTER_API_KEY`. Do not put a dummy provider key into these
locations: even a placeholder makes the budget conservative. Keep credentials for other engines
in their own supported credential stores. Never print credential files to check this.

Multiple Hermes children share the same inference server's concurrency and memory capacity:
starting more Blerg sessions does not reserve independent GPU capacity, and the server may queue
requests.

From a Blerg session on that desktop, run `blerg-runner budget`: Hermes should say
`local (runs on your hardware)`. Blerg classifies the endpoint and recognized credentials; the
label is not a billing guarantee. Make sure the server really runs your model and has no paid
provider fallback. Confirm that **New session**, with that
desktop selected, offers Hermes and the served model. Discovery is cached and refreshed by the
daemon; a configuration change may require its owner to reload the daemon. A missing model list
still allows a model name to be entered manually.

For delegation, first request a Hermes child with **Local sandbox** (`--engine hermes --runtime docker`) and approve it once. Check a real tool-using turn and the child's reported
runtime before granting broader permission. The approval card can create a standing rule for
Hermes, the Docker runtime, and that desktop; Settings lists the rule and lets you revoke it.
Standing rules cover repositories on that desktop, not only the repository in the first request.
Hermes standing rules default to local-only, so the desktop must still report Hermes as local
(the local-status cache lasts up to five minutes). A later matching request can start without
another card, subject to the parent's permissions and account limits. See [child-session permissions](../docs/talking-to-an-agent.md#when-the-agent-starts-other-sessions).

In-session changes (`/model`, `/effort` in the chat, or the chat's Model/Effort selectors) are
checked against the session engine's rules by the daemon before any engine sees them; a refused
one is reported in the transcript and changes nothing. A cluster resume drops a stored model or
effort that fails those rules (and logs it) rather than starting a pod that would refuse it.

**How a new engine plugs in** — what is true today, and what each piece does:

1. **Rules** — add the engine's row to `builtinEngines` in `internal/models/validate.go`: its model
   pattern and, if it takes one, its effort levels (no levels = any effort is refused with `422`).
   That table is the only place rules live. The server, the daemon and the cluster pod are
   separate processes, each with its own in-memory registry, but each registers the same table at
   startup (the package's `init`), so they cannot disagree; a daemon test fails if an `EngineSpec`
   has no row.
2. **Flags** — the engine's `EngineSpec` in `internal/daemon/engines.go` declares `ModelFlag`
   (`AgentModelFlag` if its headless agent invocation spells it differently) and, if it takes
   effort, an `EffortArgs(effort) []string` hook. Every invocation — tmux, agent-kind turns,
   sandboxed or not — renders model/effort through `modelEffortArgs`, which drops anything that
   fails the engine's rules; `NewAgentDriver` receives the spec, model and effort in
   `AgentDriverOpts`. Without the hook, effort is never passed.
3. **List (optional)** — two ways, depending on where the list lives:
   - **On the workstation** (a CLI's own models command, a locally configured endpoint): give the
     `EngineSpec` a `ListModels(ctx) ([]models.Model, error)` probe — `probeCodexModels` and
     `probeHermesModels` in `internal/daemon/modelprobe_engines.go` are the two examples. Return
     `errNoModelList` for a definite "nothing here"; any other error keeps the last good list. Bound
     your own work, never read or send credentials, and treat the output as untrusted (the prober
     runs `models.SanitizeReported` on it either way). That is all: `NewModelProber` picks up every
     spec with a probe, the report rides the daemon's hello/heartbeat, and the server already
     serves every engine with rules (except Claude) from what daemons reported.
   - **Published centrally** (like the Claude catalog): implement `models.Source`
     (`Models(ctx, daemonID)`) and register it in `server.NewModelSources`.

   Either way `GET /api/models/{engine}` serves it and the launch sheet renders it with no frontend
   change. Optional frontend data in `frontend/src/lib/engineModels.ts`: an offline fallback list in
   `BUILTIN_MODELS` (none for daemon-reported engines — a stale name is worse than none), and
   `FREE_TEXT_MODEL_ENGINES` for a free-text model box when nothing was reported. Without a list the
   engine simply has no picker.

### Usage limits

The sidebar shows, under **Where sessions run**, a thin meter per limit window for each provider a
workstation daemon can read: the five-hour window ("5h") and the weekly one ("week"), the share
used, and on hover when it resets; the bar turns amber from 70 % and red from 90 %. Limits belong
to the provider account the machine is logged in to, not to a session, so the daemon reports them
per provider (`usage_limits {provider, windows: [{id: "5h"|"7d"|"month"|"day"|"spend-week"|"capped", used_percent,
resets_at}], fetched_at}` over its socket). The server keeps the latest in memory, carries them in
`initial_state` as `daemons[].usage_limits` and pushes `daemon_limits_changed {daemon_id,
usage_limits}` when they change (`GET /api/daemons` carries them too); the cluster runtime reports
none. Codex's come from `codex app-server` (`account/rateLimits/read`), read when the daemon
connects, every 10 minutes and a few seconds after a Codex turn ends; three failed reads in a row
withdraw its meters, and a window whose reset has passed is not drawn. Each bar is a progressbar with
a spoken name ("Codex 5-hour window, 42% used, resets in 2h 10m, as of 11:50"); on a screen with no
hover the reset shows as text ("↻2h"). Claude's come from the account usage endpoint the CLI's own
usage view reads (`GET https://api.anthropic.com/api/oauth/usage`, `anthropic-beta:
oauth-2025-04-20`; `five_hour` and `seven_day`, each a `utilization` 0–100 and a `resets_at`, and
`extra_usage`, the monthly spend limit),
called with the CLI's sign-in when the daemon connects, every 10 minutes and about 5 seconds after
a Claude Code turn ends. It reads only when `claude` is installed and
`~/.claude/.credentials.json` holds a `claudeAiOauth` sign-in (an API key has no such limits), and
not while `BLERG_RUNNER_CLAUDE_USAGE=0`. The token is read from the CLI's file and never stored or
sent anywhere but Anthropic: the daemon never reads the refresh token, never refreshes the sign-in
and never writes the file, and refuses a file other users can read. While the token has expired,
or after the endpoint refuses it (401/403), it does not ask again until the file changes (the CLI
refreshes it as it runs); a 429 or 5xx backs off from 5 minutes, doubling up to an hour. While it
holds back the meter keeps its last reading for up to an hour. There is no file to read on macOS
(the CLI keeps its login in the Keychain) or for an API-key or `CLAUDE_CODE_OAUTH_TOKEN` setup, so
no Claude meter there; the daemon says so once in its log. The meter reads the account in that
file, and a daemon started with `CLAUDE_CODE_OAUTH_TOKEN` may run its sessions on another. The
endpoint and the credential file are the CLI's own internals, not a documented API: they may change
or refuse such reads at any time, and the meter then goes empty (logged once; nothing else
breaks). The percentage is floored, as the CLI shows it. A work or enterprise account has no
five-hour or weekly window; it has a monthly spend limit, which the meter draws as "month" ("Monthly
spend limit"): the share of the limit used, no reset time (the answer gives none), and "<1%" while
under one percent. A spend limit that is used up also reads "capped". A subscription with extra usage
switched on shows the month beside 5h and week, and a daily or weekly spend limit, when the answer has
one, shows as "day" and "spend wk". Only the share leaves the machine: never the amount spent, the
limit, the balance or the currency. When
Claude refuses a call for its limit and says when it resets, the row also reads "capped · resets
14:00" until then. Only percentages and reset times leave the machine: never the account id, plan,
credits or the token. A daemon that predates this sends nothing and no meter is drawn; a server that predates it logs
the report as an unknown message and ignores it. Updating the desktop daemon needs its reinstall.

### What "runs as you" means

A session started with an **agent token** runs as the token's owner, not as the operator. The
token's `on_behalf_of` becomes the session's spawning account, and the token's own id travels with
every credential fetch so core can confirm the token is still live — revoke the token and the
sessions it can start stop being able to fetch anything. On the cluster runtime, that account's own
opaque engine credential and GitHub credential are written into a per-session Secret the Job reads by
reference. Managed Codex connections instead deliver access tokens through the private broker;
refresh tokens stay in Core. Operator fallback requires a verified live principal with no personal
credential; lookup failures and revoked authority refuse startup. On a
**daemon** the session runs on that workstation with the developer's own logins, attributed to the
launcher — in the **Local sandbox** (the default for the launch sheet and for board-started
sessions alike when there is no cluster and the daemon has the image) the engine runs inside a
container that can see `/workspace` and the mounted login directories and nothing else of the
machine, with no git credentials (it can commit but not push) and, of the stack's services, a route to the
board and runner only (via `blerg-sandbox`); on **This machine** it runs on the host with full filesystem
access, which the launch sheet asks the launcher to acknowledge and a v1 start gets with
`runtime: "daemon"` (or `BLERG_BOARD_RUNNER_RUNTIME=daemon` on the board) or on a daemon without
the image. Either way the sandbox does not protect the engine login itself: it is mounted read-write
so the engine can use it. Sessions started with the **static runner key** have no owning account
at all and use the shared operator credentials.

Attribution always comes from the verified credential, never from the request body.

**An agent token only sees its own sessions.** Every per-session operation — status, message,
interrupt, stop, events, the SSE stream and result, over REST and over MCP alike — requires that the
session's spawning account is the token owner's. A session started by somebody else, and a session
with no owning account at all (one the static runner key started), answer `404 session not found`:
exactly what an id that does not exist answers, so the contract is not an oracle for which sessions
are running. The static runner key, a human or service token, and an agent token carrying
`board.admin` all keep the install-wide view.

**Resume.** Sending a message to a `disconnected` cluster session re-creates its Job. New
account-owned Claude and Codex sessions retain exact native conversation identity on a separate
PVC; replacement verifies the same account, engine and original claim UID with current owner
authorization and freshly rebuilt grants. Missing expected history refuses replacement. The
workspace is reconstructed separately and commands are not replayed. Legacy sessions retain the
archive recovery path; their resume restarts the 24 h expiry clock — a session picked up again gets a fresh window rather than being
judged on how long it was gone. The resume carries the caller's own account, so an agent token
resuming its own session re-mints that account's personal credentials; anyone else's resume (the
static runner key included) runs on the operator Secret rather than spawning a pod that holds
somebody else's tokens. A session whose status is not `disconnected` is not resumable and the
message is dropped.

**When nobody is watching.** A reconciler pass runs every **30 s** and closes out sessions whose
end nobody reported, so a callback always arrives eventually: a cluster Job that succeeded, failed
or vanished (after a **2 min** grace, since a Job that has not been created yet looks exactly like
one that is gone) finalizes the session; a `disconnected` session is left alone for **24 h** and
only then declared `session expired without resume`. Native-backed unended sessions do not
expire on this clock; their history is retained until a true ending and the configured retention
period. A desktop session whose daemon dropped is
finalized **30 min** after the disconnect with `daemon did not return`. These windows are fixed
constants, not environment variables. A status the session's own host reported always wins over the
reconciler.

An `error` a session's host reports is **not** treated as the end: that status is revivable — a
daemon that reattaches, or a pod that comes back, heals it — so it fires no callback and leaves the
session's credentials alone. The callback follows the real ending: the host's `session_ended`, a
`stop`, or one of the sweeps above.

## Repository list

The launch sheet's list is `GET /api/repos` (browser-authed), merged from three sources:

1. **your own repositories** — for every git provider you have stored a personal token for in
   core's Settings (GitHub, GitLab), the repositories that token can see: GitHub's
   `GET /user/repos` (owned, collaborator and organization-member repos; a fine-grained token
   lists only what it was granted), GitLab's `GET /api/v4/projects?membership=true` (needs the
   `read_api` scope). Most recently active first, at most 500 per provider. Only your own tokens
   are used and only you see the result. GitLab projects inside subgroups
   (`group/sub/project`) are not listed yet: the runner addresses a repository as at most
   `owner/name`;
2. **the operator's shared org**, when `BLERG_RUNNER_GITHUB_ORG` is set — everyone sees it;
3. **folders on connected daemons** that neither list covers, with the hosted repository each
   folder's origin remote points at (GitHub or GitLab, read from `.git/config`; a folder with no
   `origin` resolves through its one other hosted remote, and remotes naming different
   repositories — on one provider or two — stay unresolved rather than guessed).

Every entry carries `provider` (`"github"`, `"gitlab"`; absent only on an unresolved folder) and
entries are deduplicated by provider + `owner/name`, so a same-named repository on GitHub and on
GitLab are two entries. Your lists are cached per person for 5 minutes (which tokens you hold is
rechecked every minute, so a token removed in Settings takes its repositories with it; the same
check answers `GET /api/me/credentials`, so that answer can be up to a minute old). Past the
5 minutes the cached list is still served at once while one background refresh per provider
brings it up to date; only a list never fetched, or older than 30 minutes, makes the request wait
(the operator's org list works the same way). A provider whose refresh fails keeps its last good
list and sets `stale_at`, without affecting the others; a list merely being refreshed is not
flagged. The response carries an `ETag` and `Cache-Control: private, no-store` (the browser keeps
no copy); the launch sheet keeps the list itself and asks with `If-None-Match`, so an unchanged
list comes back as `304 Not Modified`. Picking a repository on the cluster runtime sends its
`provider` with the launch, so the pod clones from that host with your token for it.

The sheet itself opens at once from what it last knew in this browser tab (kept in
`sessionStorage` under your account, dropped on sign-out; MCP tool listings are never kept) and
refreshes underneath, with a "Refreshing …" note while it does. With nothing kept yet it shows as
soon as the daemons and cluster status answer, and the repository list streams in. Launch waits
for a fresh daemon list to confirm the chosen daemon is still connected. Hovering or focusing
**+ New** warms these answers, as does an idle moment after the app loads.

### No repository

The first entry in the launch sheet's list is **No repository**, and it is selected when the
sheet opens: Launch works straight away, on every runtime. The sheet says what it will do
before you launch. What happens depends on where the session runs:

- **Cluster:** a pod's filesystem goes away with it, so nothing needs a name. The pod works
  in an empty `/workspace/scratch`. Nothing is cloned, and the pod gets no git credential. The
  session's `repo` is `""`. A resume is another empty workspace. Only the transcript carries
  over.
- **This machine / Local sandbox:** the session still runs in a real folder on that machine's
  disk. The Local sandbox bind-mounts the same host folder at `/workspace`. So the session gets
  a new, empty folder directly under the daemon's repos root, named `.scratch-<suffix>`. The
  sheet generates a name such as `.scratch-granite-3f9a`, shows it with the repos folder, and
  offers a Rename. The daemon creates it on the new-folder path and never clones into it.
  Because the name starts with a dot, the folder never appears in the repos list (the daemon
  skips dot-folders when it reports what is on disk). **It is not deleted when the session
  ends.** Remove it yourself when you no longer need it.

The scratch name is the only record that a session has no repository: an empty `repo` (cluster)
or a `.scratch-` one (daemon). No other flag is stored. Every real repository name is refused if
it is empty or starts with a dot. Wherever a session's repo is shown or grouped, the UI reads that
name as "No repository".

API: `POST /api/sessions` takes `"no_repo": true`, plus an optional `"scratch_folder"`
(`.scratch-` followed by 1–64 letters, digits, `-` or `_`; daemon runtimes only). It is refused
with `repo`, `git_url`, `provider`, `clone` or `new_repo`. `POST /api/runner/start` and MCP
`start_session` take `"no_repo": true` too. There the daemon (any connected one, preferring one
with the sandbox image unless `runtime: daemon`) always gets a generated name. Without
`no_repo`, a blank `repo` is refused exactly as before.

### New repository (cluster)

A cluster session can start in a repository that does not exist yet. `POST /api/sessions` with
`"runtime": "cluster"`, `"new_repo": true`, `"repo": "owner/name"` (the slash is required: a bare
name is never resolved against `BLERG_RUNNER_GITHUB_ORG` for a new repository), an optional
`"provider"` (`github` by default, or `gitlab`) and an optional `"visibility"` (`private` by
default, or `public`). The launch sheet offers it as **New repository** when Cluster pod is
selected. What happens:

1. The server fetches the launcher's own token for that provider from core. With one, it asks the
   provider whether `owner/name` exists: yes is **409** (launch it as an existing repository).
   Then it creates the repository with an initial commit (`auto_init` on GitHub,
   `initialize_with_readme` on GitLab) under the user when `owner` is the token's own account,
   otherwise under that org/group. "Already exists" from the create call is a 409 too, never a
   fallback.
2. The start plan leads with a `repo` stage ("Creating repository"): `done` with
   `github.com/owner/name · private`, or `warning` with the reason when the repository could not
   be created (no token in Settings, a token that may not create under that owner, a provider
   error). The session starts either way.
3. The pod gets `BLERG_RUNNER_NEW_REPO=1`. It retries the clone a few times (a repository created
   seconds ago may not be clonable yet). When the clone keeps failing it runs `git init` on the
   session's `wip/<id>` branch with `origin` set to the clone URL (the person's token included,
   as in a normal clone), marks the `clone` stage as a warning ("Initialized an empty repository
   — no remote yet"), prepends a note to the agent's first prompt saying the remote does not
   exist and must be created before pushing, and reports the first failed push as an `error`
   event in the transcript. `PushWip` keeps trying after every turn, so the work is pushed as
   soon as the remote exists.
4. The row records `git_url` and `new_repo` (migration 035), so a resume clones from the same
   place, or initializes again if it still does not exist. **A new-repository pod never
   receives the operator's shared `BLERG_RUNNER_GIT_TOKEN`**: only the person's own token for
   the host, or none — so it can reach nothing the person cannot.

Behavior change: the cluster branch used to ignore `new_repo` (a caller sending it with an
existing repository got a clone); it now answers 409. The v1 start contract
(`POST /api/runner/start`) does not take `new_repo`.

### Launching on a repository by name

A repository does not have to be listed or checked out anywhere. `POST /api/sessions` takes either
of these, on every runtime:

- `"git_url"`: a remote URL on a registered provider's host (`https://…`, `ssh://…` or
  `git@host:owner/name.git`), parsed by `gitprovider.ParseRemoteURL`. It sets both the provider
  and `owner/name`. A `provider` or `repo` sent with it must agree with it, or the request is a `422`.
- `"clone": true` with `repo` as `owner/name` and `provider`. The two are required together. A
  named clone never falls back to GitHub by default.

A request that sends neither behaves as before. The launch sheet uses both: a typed `owner/name`
has a GitHub/GitLab picker, and a pasted URL is sent as it is (without any credentials pasted
into it). A listed repository that is not on the daemon yet is launched as a named clone too.

**On a daemon** (This machine or Local sandbox) the server picks the folder under the daemon's
repos root from what the daemon last reported. The rule is in `cloneFolderFor` and tested:

1. `<name>` or `<owner>-<name>` if it already holds this repository: used as it is, with no clone;
2. otherwise the first of `<name>`, `<owner>-<name>` that doesn't exist yet, cloned into;
3. otherwise, when both hold something else, a `409` naming both folders.

The daemon re-checks this on disk (`EnsureClonedTarget`). An existing folder whose origin is not
this repository, including one with no recognizable origin, is refused rather than cloned into or
over. A clone is written to a hidden sibling folder and renamed into place only when it finishes,
so a failed clone leaves nothing behind. A daemon older than this feature doesn't advertise
`clone_from` in its hello, and gets a `422` instead.

The clone goes over HTTPS to the provider's own host. When a clone will actually happen, the
server fetches **your own** personal token for that provider from core. It uses `personalGitKind`,
the same host rule that chooses a cluster pod's token, and never uses the operator's shared token.
It then asks the provider, with that token, whether the repository is private
(`Provider.Private`), and sends the token only where it may go
(`namedCloneSpawn`):

| repository | Local sandbox (`runtime: docker`) | This machine (`runtime: daemon`) |
|---|---|---|
| public | no token | no token |
| private | token; the clone runs **in the sandbox image** | refused (`422`, with the reason), unless the daemon sets `BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE=true` (hello `allow_host_credential_clone`), then token on the host |
| unknown (API error, or the token can't see it) | token | no token |

The rule exists because on the bare host the clone's git process runs as the daemon's user. Any
other unsandboxed session on that machine could read the token from `/proc/<pid>/environ`.
The opt-in is meant only for a daemon nobody else uses.

The token is sent in the `spawn_session` message (`git_token`) over the daemon's authenticated
websocket, and the daemon drops it as soon as the workspace is ready. How git gets it depends on
the runtime:

- **Sandbox:** the clone is a `docker run --rm -i` of the sandbox image with the usual hardening.
  The Basic credential goes in on the CLI's stdin. Inside the container, the shell's builtin
  `printf` writes it into a `.gitconfig` in a private `HOME`, so it is in no process's argv or
  environment, host or container.
- **Opted-in host:** it goes as an `http.https://<host>/.extraHeader` in `GIT_CONFIG_*`
  environment variables. This needs git 2.31 or newer, and an older git is refused with that
  reason instead of failing as if the token were wrong.

In both cases it is never in the clone URL, the clone's `.git/config`, the session's environment,
a recovery record or a log. A failed clone's error has it scrubbed out. The clone's `origin` is the
token-free HTTPS URL.

A clone cannot hold up the daemon's one-at-a-time spawn queue longer than it must:

- `kill_session` cancels it immediately. This kills git's whole process group, or removes the
  clone container.
- It never prompts: `GIT_TERMINAL_PROMPT=0`, and ssh runs in `BatchMode` with a connect timeout.
- A transfer below 1000 B/s for 60 s aborts (`GIT_HTTP_LOW_SPEED_*`).
- One attempt is capped at 30 minutes.
- An HTTPS failure other than its own timeout or a kill is retried once over ssh with the
  machine's own keys.

A daemon removes clone temp folders (`.blerg-clone-*`) older than 2 hours at start. They are left
behind when a daemon is killed mid-clone.

### Git providers

Everything provider-specific — remote-URL parsing, naming rules, the token clone URL, listing a
token's repositories — lives in `internal/gitprovider`: a `Provider` interface and a `Registry`
(`gitprovider.Default`) keyed by host and by credential kind. Nothing else names a provider.
Every start path — `POST /api/sessions`, `POST /api/runner/start`, the MCP `start_session`
tool — takes an optional `provider`. A named provider is honored strictly on every runtime: a
cluster clone comes from that provider's host, a daemon clones an absent folder from it (and
refuses an existing folder whose origin is another provider's repository), and an `owner/name`
that is not valid there is a `422`. A daemon too old to know providers is never sent a
non-GitHub one (`422`: it would clone the same `owner/name` from GitHub). With **no** provider
the repository is read as GitHub — kept only for callers that predate providers and always meant
GitHub: board cards and contract/MCP starts without the field, the launch sheet's new-folder
entries, older launch sheets, and a cluster resume of a session that recorded no clone URL. A
named clone (`clone`/`git_url`, above) never gets that default. Pass `provider` for anything not on GitHub. The list of these callers,
and why, is at the top of `internal/server/repo_provider.go`.

Adding one (Bitbucket, a self-managed GitLab via `NewGitLabAt`, GitHub Enterprise via
`NewGitHubAt`) is an implementation plus a `Register` call, plus its id in core's
`gitCredentialKinds` and a Settings entry so people can store a token for it.

## Opening a session workspace file

The browser viewer uses `GET /api/sessions/{id}/workspace-file?path=README.md` to read a file
mentioned by an agent. This route requires a signed-in **human session owner** or a human
administrator with `account.manage`. A private session is the owner's alone: to anyone else,
administrators included, it answers `404 Session not found.`, as an unknown session does. Public
transcript access does not grant filesystem access. Agent tokens, service credentials and the static runner key
cannot use this route.

The current session daemon reads the relative path from that session's workspace. Host sessions
use their working folder; container sessions use its host bind mount; cluster sessions use their
pod workspace. The daemon never executes a container command to read a file. Absolute paths,
parent traversal, backslashes, secret-looking names, symlink escapes, nonregular files and hard
links are refused. The launch folder's identity is captured before the session starts and checked
against the pinned directory handle, preventing a replaced folder from changing the read scope.
Paths are checked again against the opened file to prevent replacement races.

A `200` JSON response contains `path`, `size`, `view`, `content_type`, `truncated: false`,
`content_b64` and RFC3339 `mtime`. The viewer classification matches published artifacts.
Responses are private and not cached; file contents never become transcript events.
Refusals contain a safe `error` sentence and `error_id`: `403` for an unsafe path or filename,
`404` for a missing file, `409` for a missing/stale/busy daemon or a timeout, and `413` for a file
over its size cap. Older daemons return `409` after a ten-second daemon wait, with at most one additional second
to write the small refusal response. No engine is launched or
resumed by reading a file.

`BLERG_RUNNER_WORKSPACE_TEXT_MAX_BYTES` defaults to 2 MiB and
`BLERG_RUNNER_WORKSPACE_MEDIA_MAX_BYTES` to 25 MiB (image/PDF); both are clamped to an absolute
25 MiB maximum; invalid or nonpositive values use the defaults. Oversized files are refused rather than truncated. The server limits reads to
two globally and two per daemon through the complete HTTP response, with a ten-second deadline.
The daemon logs the session, cleaned relative path, byte count and outcome, never file content.
Secure opening supports Linux and macOS; other platforms refuse reads. Two daemon workers bound
reads and sending replies. A stalled regular filesystem read may outlive the HTTP timeout, while
the daemon remains responsive. File reply writes have a ten-second socket deadline.
See the browser-only operation in [`openapi.json`](internal/server/openapi.json).

## Configuration (env)

This is the full set — everything blerg-runner reads from the environment, in one place,
grouped by which process reads it. If you're setting this up for someone else (or an LLM is doing
it on their behalf), this table plus `install/desktop/DAEMON.md` (desktop daemon, engine-specific
detail) and `install/k8s/README.md` (cluster bring-up) is the complete picture — nothing here
requires reading Go source to discover.

### Server (`cmd/server`)

| Var | Required? | Default | What it does |
|---|---|---|---|
| `BLERG_RUNNER_DATABASE_URL` | yes | — | Postgres DSN |
| `BLERG_RUNNER_DAEMON_TOKEN` | yes | — | shared token the daemon presents on `/ws/daemon` |
| `BLERG_RUNNER_LISTEN_PORT` | no | `8080` | app-level HTTP listen port. Deliberately not `BLERG_RUNNER_PORT` — Kubernetes auto-injects that name (`BLERG_RUNNER_PORT=tcp://<clusterIP>:8080`) into every pod for a `blerg-runner` Service already in the namespace, which would silently clobber a same-named custom var |
| `BLERG_RUNNER_DATA_DIR` | no | `data` (relative to the working directory) | directory for runner's on-disk state: published mockups and screenshots, uploaded agent-config bundles, and the files agents publish with `blerg-runner publish` (`artifacts/<session>/…`; see [`docs/artifacts.md`](../docs/artifacts.md)). It needs a persistent volume to keep them across restarts, writable by the runner's user (id 65532 in the image); at startup the runner checks this and logs one `STORAGE NOT WRITABLE` line if it is not |
| `BLERG_RUNNER_MAX_FILE_MB` | no | `100` | the largest file, in MiB, a person may attach or an agent publish. An invalid value keeps the default and logs one line; a value above either session total below is lowered to that total (also logged), so the limit the runner advertises is one a file can actually meet. Behind an ingress or proxy, its body-size limit must be larger (the Kubernetes ingress allows `128m`) |
| `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB` | no | `500` | the total, in MiB, of the files people attach to one session |
| `BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB` | no | `1024` | the total, in MiB, of the files one session's agent publishes |
| `BLERG_RUNNER_KEY` | no | — | board↔runner contract key (must match the board's `BLERG_RUNNER_KEY`) |
| `BLERG_CORE_URL` / `BLERG_CORE_REGISTER_KEY` | no | — | register with, and validate tokens against, `blerg-core` (the runner works standalone with the static key if unset) |
| `BLERG_RUNNER_CORE_INTERNAL_KEY` | no | — | must equal core's `BLERG_CORE_INTERNAL_KEY`. Together with `BLERG_CORE_URL`, enables the personal-credential-first session spawn path, the per-person part of `GET /api/repos` (see [Repository list](#repository-list)), and `GET /api/me/credentials` (browser-authed; reports which personal credentials the caller has as `{"engines":[…],"git":bool,"git_providers":["github","gitlab"]}`, presence only — never a value — and `"unavailable":true` when core can't be asked); either unset means runner always uses the shared operator Secret, lists no one's own repositories, and that endpoint always answers `unavailable` |
| `BLERG_RUNNER_GITHUB_ORG` / `BLERG_RUNNER_GITHUB_TOKEN` | no | — | an optional shared GitHub organization whose repositories everyone sees in the launch sheet, alongside their own (token optional for a public org). Also the default org a bare repo name resolves against for cluster clones |
| `BLERG_RUNNER_SELF_URL` | no | — | the runner's own URL, advertised at registration. Also the base of the proposal links an agent is given when its write call is queued (`<this>/proposals?id=<id>`); without it the agent is given only the proposal id |
| `VOYAGE_API_KEY` | no | — | enables semantic (embedding) search for the agent memory / knowledge store. **Privacy: when set, the name and content of each memory an agent saves (`POST /api/agent/memories`) and the query text of each knowledge search (`POST /api/agent/knowledge-search`) is sent to Voyage AI's API (`https://api.voyageai.com/v1/embeddings`), and the returned vector is stored beside the memory in your database. Unset by default; when unset, nothing is sent to Voyage and search uses plain keyword matching.** A failed Voyage call also falls back to keyword matching, and a memory is still saved without its vector |
| `VOYAGE_MODEL` | no | `voyage-3-lite` | the Voyage embedding model to request; only read when `VOYAGE_API_KEY` is set |
| `BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE` | no | `false` | `true` = allow session completion webhooks to be delivered to private, loopback or link-local addresses. Off by default: a `callback_url` is chosen by whoever starts a session, so without this the runner refuses to become a proxy into its own network (checked at connect time, on the address actually resolved, per attempt). It also gates the contract's plain-`http://localhost` callback exception: without it, a loopback `callback_url` is refused at start with a `422`. Set it `true` when receivers legitimately live beside the runner — the desktop compose stack does |
| `BLERG_CORE_PUBLIC_URL` | no | — | core's browser-facing origin; when set, `/healthz` allows that origin (the landing page's status tiles) cross-origin |
| `BLERG_RUNNER_ALLOWED_ORIGINS` | no | — | comma-separated `scheme://host` origins allowed to open `/ws/browser` cross-origin. Same-origin requests and requests with no `Origin` header are always allowed, so this is only needed when the frontend is served from a different origin than runner's API |
| `BLERG_RUNNER_MODEL_CATALOG_URL` | no | `https://downloads.claude.ai/model-catalog/v1/catalog.json` | where the Claude model list (`GET /api/models/claude`, the launch sheet's Model/Effort picker) comes from: the public Claude Code model catalog, fetched at startup and every 6 h (https only — plain http is accepted only for a loopback host — same-host redirects only, 5 s timeout, 1 MB cap; the last good copy is kept). Set it **empty** to disable fetching — air-gapped installs — and serve the list compiled into this build instead |
| `BLERG_RUNNER_PLUGIN_MARKETPLACES` | no | `anthropics/claude-plugins-official` | comma-separated GitHub `owner/repo` marketplaces **always-on plugins** may come from (`*` = any valid `owner/repo`). When a cluster session starts for an account, the server reads that account's list from core (`POST /internal/plugins/list`, see `core/docs/CONFIG.md`), drops entries whose marketplace is not allowed here, and hands the rest to the pod as `BLERG_RUNNER_PLUGINS` (non-secret JSON) together with this allow-list, which the pod checks again before running `claude plugin marketplace add` / `claude plugin install <plugin>@<marketplace> --scope user` (no shell, 90 s per command, 4 min overall, one after another). If core cannot be reached the session still starts without plugins and its start panel says so. Claude and Codex agent sessions. Codex installs through its native `plugin marketplace add` / `plugin add` commands in a credential-free installer home, then loads copied plugin caches and frozen catalogs from a private session `CODEX_HOME`; its existing login/config/rollouts are linked, never copied. A Local sandbox or This machine session gets the same list through `SpawnSession.plugins`, sent only to a daemon whose hello advertised that engine in `plugin_engines` (legacy `plugins` permits Claude only); the daemon re-checks its own `BLERG_RUNNER_PLUGIN_MARKETPLACES`, installs into its plugin workshop under its state dir with `CLAUDE_CONFIG_DIR` (never the person's `~/.claude`) and starts Claude with one `--plugin-dir=` per plugin (`install/desktop/DAEMON.md`). Restricted sessions never load any. Keep equal to core's `BLERG_CORE_PLUGIN_MARKETPLACES` |

| `BLERG_RUNNER_METRICS_TOKEN` | no | — (endpoint off) | the bearer token a Prometheus scraper presents to `GET /metrics`. Unset: the endpoint answers `404`. Set: a missing or wrong token is `401`. Metrics are aggregates only; no session, account or title appears in them. See [`docs/telemetry.md`](../docs/telemetry.md) |
| `BLERG_RUNNER_MCP_GW_ADDR` | no | — (gateway off) | listen address of the **MCP gateway**, a second listener and `http.Server` separate from the main port (for example `:8090`). Unset disables the gateway, and with it every session with MCP connections and every cron that uses one. It also needs the database and core (`BLERG_CORE_URL` plus `BLERG_RUNNER_CORE_INTERNAL_KEY`). Never route this listener through a public ingress. Sessions on workstation daemons instead reach the gateway on the main port under `/mcp-gw/` (see `BLERG_RUNNER_MCP_GW_PUBLIC`); see [`docs/mcp-connections.md`](../docs/mcp-connections.md) |
| `BLERG_RUNNER_TRUSTED_PROXIES` | no | — (trust none) | comma-separated CIDRs or addresses of the proxies (an ingress controller) whose `X-Forwarded-For` and `X-Real-IP` the gateway's public route believes when it limits failed attempts per client: from a listed peer it takes the right-most `X-Forwarded-For` hop that is not itself listed. Unset, the client is the TCP peer, so behind a proxy every client shares one allowance. Any peer inside a listed range can claim another client's address: that evades the failure limit, never grants access. The Kubernetes install sets it from `TRUSTED_PROXIES` in `install/k8s/.env` (default: the RFC 1918 ranges and `fc00::/7`) |
| `BLERG_RUNNER_MCP_GW_PUBLIC` | no | on | the gateway's **public route**: `/mcp-gw/` on the main port, for sessions on workstation daemons that cannot reach the gateway's own listener (desktop daemons connected to a cluster install need it). It serves only grants issued for it to a session on a daemon, within seven days of their last use or their daemon reconnecting; it answers every other token with the same JSON-RPC 401 (the same 429 once the client's address is over its allowance), limits failed attempts per client address (see `BLERG_RUNNER_TRUSTED_PROXIES`; a well-formed token from an address over its allowance is still looked up, within a global budget of 100 lookups a second, so a valid token is served unless a sustained flood exhausts it), and answers a timeout in JSON-RPC. `0`, `false`, `off` or `no` switch it off: the prefix answers 404 and grants are sent with `BLERG_RUNNER_MCP_GW_URL` as before |
| `BLERG_RUNNER_MCP_GW_URL` | no | — | the address **sessions dial** to reach the gateway: an in-cluster Service URL for pods, the runner's address on the sandbox network for desktop sandbox containers. Sent to a session in its config. A daemon that reports `mcp_gateway_via_server` is told to dial the gateway through its own server address instead (`BLERG_RUNNER_SERVER_HTTP`, else derived from its WebSocket URL), where the runner serves it on the web port under `/mcp-gw/` for grants issued to daemon sessions for that route only (`BLERG_RUNNER_MCP_GW_PUBLIC`). A launch with MCP connections is refused with a specific message while it is unset |
| `BLERG_RUNNER_BOARD_URL` | no | — | the board's base address **as the runner reaches it** (absolute `http(s)`, no credentials, query or fragment; for example `http://blerg-board:8080`). A cron's failure card is posted to the board's API there, and the built-in `board` connection's MCP endpoint defaults to `<this>/mcp`. Unset or invalid: crons get no board connection and no failure card. See [`docs/crons.md`](../docs/crons.md) |
| `BLERG_RUNNER_GRANT_HOURLY_CAP` | no | `20` | how many proposals one session permission may approve on its own in an hour; past it, proposals wait for the person and say why. See [`docs/proposals.md`](../docs/proposals.md#session-permissions) |
| `BLERG_RUNNER_BOARD_MCP_URL` | no | `<BLERG_RUNNER_BOARD_URL>/mcp` | overrides the built-in `board` connection's MCP endpoint alone (same validation as above). Usually a private address: it is fetched by the runner's gateway, never by a session |
| `BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS` | no | empty | comma-separated hostnames for which an MCP connection URL may use plain `http://` (otherwise `https` only). Applies to the gateway and to the launch sheet's tool listing. Core has its own list, `BLERG_CORE_MCP_ALLOW_HTTP_HOSTS` |
| `BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS` | no | empty | comma-separated hostnames an MCP connection may reach although they resolve to a loopback, link-local, private, carrier-grade NAT or metadata address. Every other host is checked on the address actually dialled. Core has its own list, `BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS` |
| `BLERG_RUNNER_MCP_CALL_TIMEOUT_SECONDS` | no | `60` | per-call timeout for a `tools/call` to the upstream MCP server |
| `BLERG_RUNNER_MCP_MAX_CONCURRENT` | no | `4` | concurrent upstream calls per session and connection; a further call is refused immediately |
| `BLERG_RUNNER_MCP_MAX_RESULT_BYTES` | no | `262144` | cap on the text returned to the session for one call; longer results are truncated |

Set by the runner itself on a session, never by you: `BLERG_RUNNER_MCP_CONFIG` (the JSON of the
session's MCP server entries and gateway tokens, from a per-session Secret for a pod, written to a
`0600` file and then removed from the environment before the agent starts) and
`BLERG_RUNNER_RESTRICT_TOOLS` (the built-in tool allow-list of a restricted session: a cron's, or a
board-started session with connections; a launch-sheet session is never restricted). Neither is
accepted from a request's `env`.

Not environment variables, but worth knowing they exist: the session reconciler's windows are
compile-time constants — it polls every 30 s, waits 2 min before reading a missing cluster Job as
gone, leaves a `disconnected` session resumable for 24 h, and finalizes a session whose daemon
dropped after 30 min. See ["When nobody is watching"](#what-runs-as-you-means) above.

### Desktop daemon (`cmd/daemon`)

| Var | Required? | Default | What it does |
|---|---|---|---|
| `BLERG_RUNNER_SERVER_URL` | yes | — | the server's WS URL to dial |
| `BLERG_RUNNER_DAEMON_TOKEN` | yes | — | must match the server's value above |
| `BLERG_RUNNER_REPOS_ROOT` | yes | — | where session checkouts live — the **first-run default**. The folder can be changed from the app (the launch sheet's Run column: "Repos folder → Change"); the daemon saves that choice to `<state dir>/settings.json`, and from then on the saved value wins over this variable on every start. Delete the file (or its `repos_root` key) to go back to the variable |
| `BLERG_RUNNER_DAEMON_STATE_DIR` | no | `~/.blerg-runner-daemon` | where the daemon keeps its own state (`settings.json`, and `connection.json`, see [Checking a daemon token](#checking-a-daemon-token); mode 0600 in a 0700 directory). Deliberately outside the repos root. If it can't be determined or written, changing the repos folder from the app is refused rather than applied until the next restart silently undoes it |
| `BLERG_RUNNER_DAEMON_NAME` | no | hostname | how this daemon identifies itself |
| `BLERG_RUNNER_DAEMON_MODE` | no | `local` | daemon mode tag |
| `BLERG_RUNNER_SERVER_HTTP` | no | — | server's HTTP base, for the messages/data-plane APIs |
| `BLERG_RUNNER_PREVIEW_URL` | no | — | **deprecated**: only read to derive the server URL when `BLERG_RUNNER_SERVER_HTTP` is unset (a value ending in `/api/preview` gives the base before it, and the daemon logs a line asking you to set `BLERG_RUNNER_SERVER_HTTP`). It is not passed to sessions |
| `ANTHROPIC_API_KEY` | no | — | enables the native tool-calling loop (metered) — used by a host Claude agent session only when `claude` is not on the daemon's PATH (with `claude` installed, it runs your Claude Code login) |
| `CLAUDE_CODE_OAUTH_TOKEN` | no | — | enables the subscription-billed claude-code driver instead |
| `BLERG_RUNNER_AGENT_BUDGET_USD` | no | `25` | hard per-session cost cap for the native loop |
| `BLERG_RUNNER_SANDBOX_IMAGE` | no | `blerg-runner-sandbox:latest` | Local Sandbox Docker image — pin a tag or point at a private registry |
| `BLERG_RUNNER_PROVISION_CLAUDE_MD` | no | `false` | true = add/refresh the blerg-runner messaging block in ~/.claude/CLAUDE.md on every start |
| `BLERG_RUNNER_CLAUDE_USAGE` | no | on | `0` (or `false`/`off`/`no`) = do not read Claude's usage meter from the account usage endpoint with the CLI's sign-in (see [Usage limits](#usage-limits)); Claude then shows only a reactive cap |
| `BLERG_RUNNER_MCP_PROMPT_WAIT_S` | no | `30` | first-input wait for known pending native MCP servers; `0` disables holding, values over 30 are capped, invalid/negative values use 30. Read by the daemon or cluster pod process, not passed to the engine |
| `BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE` | no | `false` | true = a This machine session may clone a private repository with the launching person's own git token, on the bare host (see [Launching on a repository by name](#launching-on-a-repository-by-name)). While it clones, any other unsandboxed session on this machine could read that token — **only set this on a daemon nobody else uses** |

#### Checking a daemon token

A daemon is told it was accepted: when its `daemon_hello` sets `welcome: true`, the server answers
an accepted token with `{"type":"daemon_welcome","daemon_id":…,"name":…}` before anything else
(a session pod's carries its `session_id`), and closes a refused one with `4401`, as before. The
daemon logs `connected` only on the welcome. A server older than 0.2.1 sends no welcome and ignores
the hello field; the daemon then takes a connection that stays open for 5 seconds as accepted and
logs that the server did not confirm it. A 0.2.0 daemon never asks for a welcome, so it gets none.
For the reconnect backoff (2 s doubling to 60 s), only a confirmed connection, or one open for those
5 seconds, starts the ladder over; a `4401` never does.

The daemon (not a session pod) writes its connection state to `<state dir>/connection.json`, mode
0600, through a temporary file and a rename:
`{"state":"connecting|connected|unconfirmed|refused","server":"<url>","since":"<RFC3339>","detail":"<short reason>","pid":<process id>}`.
It writes `connecting` before its first dial, so a file another process left is never current. No
token, ever. `install/desktop/daemon/install.sh status` prints it.

`GET /api/daemon/whoami` with `Authorization: Bearer <daemon token>` checks a token without
connecting a daemon: `200 {"ok":true,"mode":"daemon"}` for `BLERG_RUNNER_DAEMON_TOKEN`,
`200 {"ok":true,"mode":"runtime"}` for a session pod's current runtime token, `401` for anything
else (`503` if a runtime token could not be checked). It has no rate limit, like the websocket
hello: the tokens are long random strings. A server older than 0.2.1 has no such route and answers
its web page (`200 text/html`) or a `404`, so a caller must treat only a `200` with a JSON body
`{"ok":true}` as accepted. The desktop installer asks it before it replaces a running daemon
(`install/desktop/DAEMON.md`, "Is the daemon connected?").

#### Changing the repos folder while the daemon runs

The browser calls `PUT /api/daemons/{id}/repos-root` with `{"repos_root": "/abs/path"}` (a
signed-in browser session, the same gate as starting or stopping a session). The server sends
that daemon a `set_repos_root` WebSocket message and waits up to 10 s for its
`repos_root_result`:

| Answer | Meaning |
|---|---|
| `200 {"repos_root": …}` | applied and saved; the value is the daemon's cleaned path |
| `422 {"error": …}` | refused, nothing changed — the reason is the daemon's (not absolute, `/` or a pseudo filesystem, not a folder, can't be created or written, another user's folder that others can write to — e.g. `/tmp` — or one of its own that any user can write to without the sticky bit, a `.blerg-runner` in it that is not the daemon user's own, the settings file can't be saved) |
| `404` | no such daemon connected (cluster runner pods are not configurable) |
| `502` / `504` | the daemon disconnected mid-request / never answered — an older daemon that predates this message logs it as unhandled and times out |

The daemon validates the folder itself — it is the one that can see its filesystem — and creates
it if it is missing (`mkdir -p`, 0755). Only **new** work moves: new spawns, the repo list in the
launch sheet (the daemon sends a heartbeat straight after, carrying the new root and its repos),
and session recovery records (moved from `<old>/.blerg-runner/sessions/` to the new root).
Sessions already running keep the absolute paths they started in. Every heartbeat now carries
`repos_root`, so the server's view (and the `daemons` row) follows a change made any other way.

### Engines (any runtime — see DAEMON.md's "Engines" section for the Claude/Codex/Hermes/OpenClaw
picture, auth, and per-engine tradeoffs)

| Var | Default | What it does |
|---|---|---|
| `BLERG_OPENCLAW_CONTEXT_CHARS` | `16000` | OpenClaw's headless replay budget (chars of prior conversation resent per turn) — raise for bigger hardware (a larger-context model, more or larger GPUs, a hosted API) |

### k8s "Cluster pod" runtime (`cmd/server`, `internal/server/k8sjobs.go`) — operator-set

Only active when `BLERG_RUNNER_AGENT_IMAGE` is set (in-cluster service-account detection also
required — see `NewJobManagerFromEnv`). See `install/k8s/README.md` for cluster bring-up and
DAEMON.md's "Cluster pod auth" for how each engine's credentials reach a pod.

| Var | Default | What it does |
|---|---|---|
| `BLERG_RUNNER_AGENT_IMAGE` | — | pod image (required to enable this runtime at all) |
| `BLERG_RUNNER_AGENT_NAMESPACE` | `blerg-runner-sessions` | k8s namespace for session Jobs |
| `BLERG_RUNNER_INTERNAL_URL` | `http://blerg-runner-server.default.svc.cluster.local:8080` | in-cluster URL pods dial back to |
| `BLERG_RUNNER_AGENT_GIT_BASE` | `https://github.com/<BLERG_RUNNER_GITHUB_ORG>`, or `https://github.com` when no org is set | git clone URL base for GitHub pods — a bare repo name is appended as `<base>/<repo>.git`; so is an `org/name` when this is set explicitly, while with only the org default an `org/name` clones from `https://github.com/org/name.git`. With neither this nor an org set, a cluster spawn must name its repo as `org/name` (a bare name is refused with 422). Repositories on another git provider (a launch with `"provider":"gitlab"`) always clone from that provider's own host, never from this base |
| `BLERG_RUNNER_AGENT_SECRET` | `blerg-runner-agent` | k8s Secret holding `ANTHROPIC_API_KEY`/`BLERG_RUNNER_DAEMON_TOKEN`/`BLERG_RUNNER_GIT_TOKEN` |
| `BLERG_RUNNER_OAUTH_SECRET_NAME` | same as `BLERG_RUNNER_AGENT_SECRET` | separate Secret for operator-synced creds (`CLAUDE_CODE_OAUTH_TOKEN`, `CODEX_AUTH_JSON`, `HERMES_ENV_CONTENTS`, `BLERG_RUNNER_GIT_TOKEN`) when rotation flows from elsewhere (e.g. Infisical) |
| `BLERG_RUNNER_HERMES_PROVIDERS` | unset | the cluster's shared Hermes provider fragment (a Hermes `config.yaml` document; install/k8s sets it from Secret `blerg-hermes-providers`). Merged into every Hermes session pod's `~/.hermes/config.yaml`, lists the providers' models in the cluster catalog, and makes Hermes available on the cluster without a `.env`. See [Shared Hermes providers](../install/k8s/CLUSTER-RUNTIME.md#shared-hermes-providers) |
| `BLERG_RUNNER_MAX_SESSIONS` | `4` | concurrent cluster session cap. An administrator can change it (1 to 64) on the runner's Cluster page (`PUT /api/cluster/settings` with `max_sessions`, `0` or `null` returns to this value; needs the `account.manage` capability); a saved value beats the environment and applies to the next session start, no restart. `GET /api/cluster/status` reports the effective `max_sessions` and this value as `max_sessions_default` |
| `BLERG_RUNNER_POD_TTL_SECONDS` | `604800` (7d) | `activeDeadlineSeconds` — the hard cap on a pod's lifetime, busy or idle |
| `BLERG_RUNNER_POD_IDLE_TIMEOUT_SECONDS` | `86400` (24h) | the pod ends itself after this long with no message sent and no turn finished (`0` = never). The session keeps its history and can be resumed. An administrator can change this and the lifetime cap on the runner's Cluster page (`PUT /api/cluster/settings`, needs the `account.manage` capability); a saved value beats the environment and applies to pods started afterwards |
| `BLERG_RUNNER_POD_CPU_REQUEST` / `BLERG_RUNNER_POD_MEM_REQUEST` | `500m` / `1Gi` | pod resource requests |
| `BLERG_RUNNER_POD_CPU_LIMIT` / `BLERG_RUNNER_POD_MEM_LIMIT` | `1` / `4Gi` | pod resource limits |
| `BLERG_RUNNER_POD_TERMINATION_GRACE_SECONDS` | `120` | grace period before a killed pod is force-stopped |
| `BLERG_RUNNER_POD_TTL_AFTER_FINISHED_SECONDS` | `3600` | how long a finished Job sticks around before k8s garbage-collects it |

A cluster session prefers the launching user's own credentials over the operator Secret:
blerg-core is asked for that account's engine credential and for its git credential of the
clone's provider, and whatever it returns is written to a per-session Secret the Job reads by
reference (never as a literal env value). A personal Claude credential beginning `sk-ant-oat` becomes
`CLAUDE_CODE_OAUTH_TOKEN` (subscription mode), anything else `ANTHROPIC_API_KEY` (API mode);
whichever of the pair is not chosen is left unset for that session rather than being filled in
from the operator Secret. The personal git credential becomes `BLERG_RUNNER_GIT_TOKEN`, and
which one is chosen by the clone URL's host: `github` for github.com, `gitlab` for gitlab.com
(one kind per registered git provider — see [Git providers](#git-providers)). The pod injects
it into the clone URL with that provider's token username (`x-access-token` for GitHub,
`oauth2` for GitLab). A verified absence of a personal credential permits operator fallback;
lookup errors or revoked authority refuse startup — except the git token, which only ever goes to the host it belongs to: the operator's
`BLERG_RUNNER_GIT_TOKEN` is offered only for a clone from the operator's own git base (github.com
by default), a personal token only for its own provider's host (so a GitLab clone gets only the
launcher's GitLab token, and an operator base on a host no provider is registered for gets the
operator token but no personal one), and a caller-supplied `git_url` on any other host is cloned
with no token at all. The cluster status payload's
`git_configured` reports whether that Secret carries a `BLERG_RUNNER_GIT_TOKEN` at all (presence
only — never the value). A cluster session records its (token-free) clone URL at start, and a
resume clones from that same URL.

Managed Codex connections use a dedicated device login under Settings. A pod receives a private
broker flag and a current-launch messaging credential, with no refresh token or copied managed
`auth.json`. Model probes use the same access-only flow. Existing snapshot imports stay snapshots;
see the [credential lifecycle contract](../docs/design/engine-credential-lifecycle.md).

Every pod receives its own runtime and messaging pair. The runtime token authorizes only that
launch's session and repository; replacement revokes the old pair and its open WebSocket authority.
The pod never receives the operator daemon master key or unrelated engines' credentials. Runtime
pods cannot be targeted as workstation daemons. Native history retains only engine conversation
files and a private identity journal, never credentials or tool grants.

### k8s pod env (`cmd/runner`) — set automatically by the Job manifest, not by you

`BLERG_RUNNER_SERVER_WS`, `BLERG_RUNNER_SERVER_HTTP`, `BLERG_RUNNER_SESSION_ID`,
`BLERG_RUNNER_REPO`, `BLERG_RUNNER_TITLE`, `BLERG_RUNNER_MODEL`, `BLERG_RUNNER_EFFORT`, `BLERG_RUNNER_ENGINE`,
`BLERG_RUNNER_INITIAL_PROMPT`, `BLERG_RUNNER_INITIAL_PROMPT_SOURCE`,
`BLERG_RUNNER_INITIAL_PROMPT_CLIENT_ID`, `BLERG_RUNNER_GIT_URL`, `BLERG_RUNNER_RESUME`,
`BLERG_RUNNER_HOME`, plus whichever credential env vars the session's engine needs
(`ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` / `CODEX_AUTH_JSON` / `HERMES_ENV_CONTENTS` /
`BLERG_RUNNER_GIT_TOKEN`) — listed here for completeness, not something you set directly.

### Desktop stack ports and secrets (`install/desktop/.env`)

Not read by any Go binary directly — these configure the docker-compose stack (board/core/runner
containers) and are generated with random secrets on first `./blerg-up.sh` run. See
`install/desktop/.env.example` for the full list (host ports, board auth, the board↔runner
contract key) — safe to hand-edit and re-run `./blerg-up.sh`, which is idempotent.

## Build & run

Built as part of the Blerg desktop or k8s stack — see `install/desktop/` and `install/k8s/` at
the repo root. To run the server image directly, build `Dockerfile.server` from the repo root
(the build context includes the shared `contracts/` module):

```
docker build -f runner/Dockerfile.server -t blerg-runner .
```

The daemon runs on the host (it drives your local `claude`); start it with:

```
export BLERG_RUNNER_SERVER_URL=ws://localhost:8083/ws/daemon
export BLERG_RUNNER_DAEMON_TOKEN=<same value the server has>
export CLAUDE_CODE_OAUTH_TOKEN=<from `claude setup-token`>
go run ./cmd/daemon
```


Child workers remain in Started sessions and the sidebar until explicitly discarded, across
running, idle and stopped states. Discard is available from each panel row and the child card's
Actions menu, with confirmation. It uses `POST /api/sessions/{parent}/children/{child}/discard`:
the parent's session credential may discard only its own children; a signed-in person must own
both parent and child. Success stops the child and records `archived_at`; a failed stop leaves it
visible for retry. Archived workers leave live/nested lists, but remain readable by URL and
included in History and Insights. Discard is idempotent. Stop alone keeps a worker listed.


A parent session's **Discard** action is separate from Stop. The signed-in human owner can
confirm `POST /api/sessions/{id}/discard` to stop the session and **all** its children, including
mid-turn workers marked Keep running. Agent, session and daemon credentials cannot invoke it.
The operation durably refuses further input, child launches and recovery before stopping workers.
Every reachable worker is stopped even if another worker is unavailable; only successfully stopped
workers are archived, and the parent is archived last. A `503` leaves discard pending and visible
for retry, with its admission fence retained across server restarts. Repeat the same request after
the worker reconnects. Success is idempotent and returns `status`, `session_id` and `archived_at`.
Discard keeps conversations, artifacts, direct links, History and Insights. It does not change
standing desktop rules, and ordinary Stop/`DELETE /api/sessions/{id}` still respects Keep running.
