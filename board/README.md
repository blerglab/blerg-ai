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

## Configuration

Every env var the server and CLI read — gate backends, the runner, deploy
URLs, defaults — is documented in [`docs/CONFIG.md`](docs/CONFIG.md).
Nothing beyond `DATABASE_URL` is required to start.

## License

Apache License 2.0 — see [`LICENSE`](../LICENSE).
