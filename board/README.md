# blerg-board

An issue/feature tracker built on an inverted premise: **agents write the
cards; humans curate.** Stones stacked by whoever came before, so the next
traveler doesn't get lost.

Most trackers assume writing a ticket is expensive — a human has to notice
something, decide it matters, and type it. That friction quietly does three
jobs at once: it throttles volume, it dedupes against memory, and it filters
quality. Agents remove the friction entirely — filing becomes a function
call — so blerg-board puts something in its place on purpose: an **admission
gate**, an LLM curator on the agent write path that can deny a duplicate or
send a vague card back for revision, with a full audit trail of what it
rejected and why. Humans are expected to almost never write a card
themselves; they curate the board agents fill.

- **Admission gate** — an LLM curator on the agent write path (any
  OpenAI-compatible endpoint, local or hosted, or the Claude API) that denies
  semantic duplicates and sends vague cards back for revision, with a full
  append-only audit trail (`admission_reviews`) — including the submissions
  that never became cards.
- **MCP-first agent surface** (`/mcp`, streamable HTTP) with a deliberately
  small CLI (`blerg-board ls|get|new|move|search`) and a bundled `managing-cards` skill.
- **Per-board field schemas** — boards declare their own custom fields
  (rendered, validated, filtered generically); blerg-board never learns what an
  incident is.
- **Idempotent writes** — `dedup_key` makes re-running a sweep refresh instead
  of duplicate (never undoing human curation); `external_id` lets an external
  driver own and move its cards (e.g. mirroring an incident tracker in
  read-only).
- **Uniform versioning** — every mutation bumps `version`; `If-Match`
  everywhere.
- Three principals: a blerg-core-issued human bearer token (gate-exempt,
  otherwise capability-checked like any other), service key, board-scoped
  revocable agent tokens. Unauthenticated = 401, always.
- Optional **runner** integration spawns agent sessions straight from a
  card (bring your own driver — blerg-board brokers, it never runs sessions
  itself).

## Quickstart

The supported way to run blerg-board is as part of the whole stack, with the
desktop installer in [`install/desktop`](../install/desktop) (it needs Docker
and builds the board, core and runner from your checkout; see the Install
section of the [root README](../README.md)). Human login is entirely
blerg-core's (the board has no native password or session scheme): the stack
wires the board to its core for you. To deploy on a cluster, use the installers
in [`install/`](../install).

To hack on the code, see "Running from source" in
[CONTRIBUTING.md](../CONTRIBUTING.md); it covers core, and does not yet cover
running the board server on its own. Every variable the server reads (gate
backends, the runner, `BLERG_CORE_URL`, `BLERG_BOARD_SERVICE_KEY`) is in
[`docs/CONFIG.md`](docs/CONFIG.md). The admission gate turns itself on when
`BLERG_BOARD_INFER_URL` or `ANTHROPIC_API_KEY` is set; leave both unset to keep
writes ungated.

Once it's up: open **/tokens** (with a service-key or core-issued bearer
token) to mint an agent bearer token, and point an MCP client or the
`blerg-board` CLI at it.

Running the binary directly:

```
DATABASE_URL=postgres://… BLERG_BOARD_SERVICE_KEY=… \
BLERG_BOARD_INFER_URL=http://localhost:8000 BLERG_BOARD_INFER_MODEL=<model> ./blerg-board-server
```

`make test` / `make test-db TEST_DATABASE_URL=…` — DB-gated tests skip
without a database. To deploy, use the installers in
[`install/`](../install). See [CONTRIBUTING.md](CONTRIBUTING.md) for branch
and lint conventions.

On phones, board columns stack vertically and card sheets fill the screen. The board session
and card session views keep a full-width chat with touch-sized controls; desktop card sheets
keep the side-by-side conversation. The workspace Chromium suite covers these pages at 390 px
and 3840 px: from the repository root run `npm run test:browser -w runner/frontend -- ../../board/web/src/Mobile.browser.test.tsx --maxWorkers=1`.

## Configuration

Every env var the server and CLI read — gate backends, the runner, deploy
URLs, defaults — is documented in [`docs/CONFIG.md`](docs/CONFIG.md).
Nothing beyond `DATABASE_URL` is required to start.

Boards start sessions as a person: **Run as me** (one click in a board's
settings, beside **Run this card** on a board with no token, and checked by
default when a board is created) has blerg-core mint that person's
`run-sessions` token and stores it, so Run this card works out of the box on a
single-user install. Pasting a token stays available for service accounts. See
[Board automation identity](docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

Board automation discovers engines, models and efforts through
`GET /api/boards/{id}/launch-options` (MCP: `blerg_board_launch_options`), using
that board's stored automation identity and configured runner target. A current
session's chat uses `GET /api/runner-sessions/{id}/launch-options` to discover
choices for its existing engine and host. Credentials stay on the backend.
Board defaults and the review, discussion and board-chat roles have paired
model/effort settings; cards can override them. Changing engines clears future
board/card overrides while existing sessions keep their engine. A model-only
settings change clears its previous effort; an explicitly supplied effort wins.

Session controls carry the board's current automation identity, including after
a token is renewed. A disconnected Cluster session is offered recovery only
when the runner reports an eligible recovery attempt under that authority;
missing native history is a visible failure. Optional message `client_id` values
survive proxying for durable input receipts; retry an ambiguous send with the
same ID and content. Live model/effort updates apply to the next turn and never
change the session's engine.

## Sensitive card details and origin

A card filed from a Runner session can carry a **restricted** marker. Its body is kept separately
from ordinary card records and is returned by the card-detail read only to principals with actual
`board.admin` access to that board. A board-scoped admin capability counts; membership or
`card.read` alone does not. Other readers can still see the card's title, marker, origin and
ordinary metadata. Keep private reproduction details in the body, not the title.

Lists, search, history, events and automation inputs omit sensitive bodies, including for an
administrator; an authorized detail read is required. An administrator may update the private
body, but a normal metadata edit cannot clear it accidentally. The sensitive marker and source
origin are not mutable through ordinary card updates. This protection applies to the target
Board, not to text already shared in the originating conversation or proposal.

Source metadata identifies the Runner session, source repository, engine and verified Git commit
when available. An unborn or non-repository workspace has no verifiable commit. Source metadata
is recorded by Blerg; it is distinct from the target repositories chosen for the card. A board
that requires repositories can default to its sole configured repository; multiple configured
repositories require an explicit selection when no target was supplied.

Board administrators can enable **Include component version** in Board settings. This opt-in adds
Blerg component version and component commit metadata to newly filed cards; it does not change
older cards. The source repository commit remains a separate field. Filing through Runner always
waits for the person's [proposal approval](../docs/proposals.md#filing-a-board-card-from-a-session).

## License

Apache License 2.0 — see [`LICENSE`](../LICENSE).

### Where a board runs

In board settings under **Automation → Where it runs**, the automation owner can choose the
cluster, a desktop's Local sandbox, or that desktop's This machine host. The setting applies to
board chat, card runs, and automated workers and reviews. A single run's **Where it runs** choice
overrides it. Choosing the host needs an explicit acknowledgement, recorded with who gave it and
when: the board's sessions then run unattended, without a sandbox, as the person who runs that
desktop, and anyone who can write cards puts prompts there. A single run uses a desktop's host only
when the board's own target is that acknowledged host. Unavailable desktops are labeled with a
reason; a chosen target never falls back elsewhere. On an install with desktops and no cluster, the
board runs on a desktop without requiring a setting: the only one, or the one that has the board's
repository (none or several is refused with a message saying so). After changing the automation
owner, save its target again.
