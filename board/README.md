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
- Optional **runner** integration spawns agent workspaces straight from a
  card (bring your own driver — blerg-board brokers, it never runs workspaces
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

On phones, board columns stack vertically and card sheets fill the screen. The board channel
and card workspace views keep a full-width chat with touch-sized controls; desktop card sheets
keep the side-by-side conversation. The workspace Chromium suite covers these pages at 390 px
and 3840 px: from the repository root run `npm run test:browser -w runner/frontend -- ../../board/web/src/Mobile.browser.test.tsx --maxWorkers=1`.

## Configuration

Every env var the server and CLI read — gate backends, the runner, deploy
URLs, defaults — is documented in [`docs/CONFIG.md`](docs/CONFIG.md).
Nothing beyond `DATABASE_URL` is required to start.

Boards start workspaces as a person: **Run as me** (one click in a board's
settings, beside **Run this card** on a board with no token, and checked by
default when a board is created) has blerg-core mint that person's
`run-sessions` token and stores it, so Run this card works out of the box on a
single-user install. Pasting a token stays available for service accounts. See
[Board automation identity](docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

Board automation discovers engines, models and efforts through
`GET /api/boards/{id}/launch-options` (MCP: `blerg_board_launch_options`), using
that board's stored automation identity and configured runner target. A current
workspace's chat uses `GET /api/runner-sessions/{id}/launch-options` to discover
choices for its existing engine and host. Credentials stay on the backend.
Board defaults and the review, discussion and board-chat roles have paired
model/effort settings; cards can override them. Changing engines clears future
board/card overrides while existing workspaces keep their engine. A model-only
settings change clears its previous effort; an explicitly supplied effort wins.

Workspace controls carry the board's current automation identity, including after
a token is renewed. A disconnected Cluster workspace is offered recovery only
when the runner reports an eligible recovery attempt under that authority;
missing native history is a visible failure. Optional message `client_id` values
survive proxying for durable input receipts; retry an ambiguous send with the
same ID and content. Live model/effort updates apply to the next turn and never
change the workspace's engine.

## Sensitive card details and origin

A card filed from a Runner workspace can carry a **restricted** marker. Its body is kept separately
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
waits for the person's [proposal approval](../docs/proposals.md#filing-a-board-card-from-a-workspace).

## License

Apache License 2.0 — see [`LICENSE`](../LICENSE).

### Where a board runs

In board settings under **Automation → Where it runs**, the automation owner can choose the
cluster, a desktop's Local sandbox, or that desktop's This machine host. The setting applies to
board chat, card runs, and automated workers and reviews. A single run's **Where it runs** choice
overrides it. Choosing the host needs an explicit acknowledgement, recorded with who gave it and
when: the board's workspaces then run unattended, without a sandbox, as the person who runs that
desktop, and anyone who can write cards puts prompts there. A single run uses a desktop's host only
when the board's own target is that acknowledged host. Unavailable desktops are labeled with a
reason; a chosen target never falls back elsewhere. On an install with desktops and no cluster, the
board runs on a desktop without requiring a setting: the only one, or the one that has the board's
repository (none or several is refused with a message saying so). After changing the automation
owner, save its target again.

A board repo given as a desktop folder name (for example `app`) runs on a desktop, which finds the
folder, but the cluster clones `owner/name`. Unless the install has a default organization or git
base (or the board has a git base), **Where it runs** shows **Repos for the cluster**: pick the
desktop this folder lives on, see `app → owner/app` from that desktop's git remote, and save it; the
board and its cards then name `owner/app`, in the same place (the board's first repo stays its
workspaces' working directory). Only the picked desktop is asked. Desktops have no owner in the runner,
so the board cannot check whose desktop it is: resolving is kept to a signed-in board admin who owns
the board's automation token, and nothing is saved until they confirm. A folder with no remote stays
desktop-only, and the cluster is not offered for it. A board's repos keep the order they were given;
the first is the working directory of board channels.


### Board keys and card references

Choose a unique key of 2–5 uppercase letters or digits, starting with a letter when creating a board. For example,
a board named Blerg suggests `BLG`, and one named Launch Night suggests `LN`. Card numbers remain per board: `BLG-205`
and `LN-6` resolve without a session-specific board. Board admins can change the key in Board
settings; old keys remain reserved aliases, so existing references continue to work.

Upgrade migration 029 leaves existing boards' keys unassigned. An admin confirms or edits the
suggestion in Board settings. The upgrade preserves all card numbers and existing references.
Until confirmation, CLI output uses `board:<board-id>#<number>`; both that legacy form and bare
`#N` (when one board is unambiguous) continue to work. MCP card tools accept `reference: "BLG-205"`
as an alternative to `card_id` or `board_id` plus `number`.

#### One-time upgrade confirmation

Immediately after upgrading, a board admin opens each existing board's **Board settings → Board key**,
verifies the suggested key (for example `BLG` for a board named Blerg) and clicks **Confirm key**.
If a suggestion has a numeric suffix, another board or a reserved alias already owns the
shorter key: inspect the owner before choosing an alternative. Confirm once; subsequent
upgrades preserve the keys. Verify by opening an existing `BLG-N` and `AD-N` reference.
This is an admin action, not an automatic migration assignment.

Migration 030 tightens validation to require an initial letter. If a pre-release installation
already assigned keys beginning with a digit, fix those keys and remove their invalid aliases
before upgrading; the migration refuses them rather than silently renaming references.
All valid old keys stay reserved permanently and cannot be reused for another board.

Suggestions come from the server and check current keys and aliases for availability; saving
still checks for concurrent claims. Reference readers cache a key map for the session/viewer,
so unknown prose such as `UTF-8` and `SHA-256` stays text without card reads. A real board
using one of those keys intentionally makes it a reference. Reload the workspace after confirming
or renaming a key to refresh its discovery snapshot. Chips show the canonical current key,
even when the message used `#N`, `board:<id>#N`, or an old alias.

### Done-card retention

The board shows cards that entered an explicitly terminal column in the last
14 days. **N older** expands older cards; **Load more older cards** loads the next
page, and **Show recent** collapses them. The in-place search and tag, type and
priority filters query older cards too. Chips, column counts and older counts
include every matching live card, even before its details are loaded. Card links
and the search dropdown also find older cards.

Board admins can optionally set **Auto-archive done after (days)** and **Keep
cards with tags** in board settings. Enabling archiving shows an eligible-card
count and asks for confirmation. The API settings are `auto_archive_done_days`
(default `0`, disabled; maximum 36500) and `auto_archive_keep_tags` (default
`["to-test"]`; any matching tag keeps the card). Only explicitly terminal columns
qualify. Time starts when the card enters its column; comments and edits do not
restart it. During upgrade, missing entry history starts a fresh clock; reorders
do not count as entries. Stuck cards and cards with active runner sessions are kept. Mirrored
boards cannot enable retention. This setting is intended for boards without releases.

The sweep runs every five minutes, processes up to 100 oldest eligible cards
**per board**, and records a service archive event. Archived cards remain
searchable with `include_archived=1` and can be moved back.

`GET /api/boards/{id}/cards/view` requires `card.read`. It returns `cards`, `older`,
`totals`, `matched`, facet `chips`, older-card `loaded` counts and `next` cursors
keyed by column. Repeat `expand=column-id` to include the first older page.
`filter` is JSON with `q`, `tag`, `type` and `priority`; active filters include
older matches automatically. `limit` defaults to 50 (maximum 100). To fetch the
next older page, send `older_column=column-id` and its `cursor`, preserving the
filter. Only that column's next older cards are returned. Board admins can preview
retention with `POST /api/boards/{id}/archive-preview` and the two settings.

Sessions with Full board access backed by a person's sign-in can archive through
`blerg_card_archive` or `blerg-runner ticket archive`. Lower access levels, crons,
and agent-token access cannot archive. The current permission is checked on every
call, using the same rule as column management.

## Release planning

Use **Releases** on a board to create a named release, set its target date, and
mark it planned or in progress. Choose a card's **Release** in its sheet. The
board can filter by release (including No release) or group live cards by release.
Open a release to see Done of total, counts by column, open work and blockers.
Its search covers card numbers, titles and bodies; card buttons open the full
card, with its existing commit links.

**Cut release** marks it released, archives its Done cards into the release
record, and moves all live open cards to the next release you select. The cut
is atomic. It requires a next release when open work remains and refuses active
card sessions; stop those sessions first. Archived Done cards stay searchable
and linked from the release, including cards archived by retention before the cut.
Released card assignments and release settings stay fixed. Mirrored boards
cannot create, edit or cut releases.

REST exposes `GET/POST /api/boards/{id}/releases`,
`GET/PATCH /api/boards/{id}/releases/{release}` and
`POST /api/boards/{id}/releases/{release}/close` with `next_release_id`.
Create and edit bodies contain `name`, `target_date` (YYYY-MM-DD or null), and
`status` (`planned` or `in progress`). Only cutting sets `released`.
Cards accept `release_id`; an empty string clears it. Card list/search accepts
`release_id` (UUID or `none`) and `include_archived=1` for released work.
The board view's JSON filter accepts `release` with the same values.
MCP equivalents are `blerg_release_list`, `blerg_release_get`,
`blerg_release_save` and `blerg_release_close`. Reads require `card.read`;
release writes require `card.write` with a person behind the access, using the
same rule as archiving. Every rollover/archive records the acting principal in
the card's trail and bumps its version.

After rollout, assign existing version-prefixed cards to releases before removing
the prefixes from their titles. Upgrading does not rename cards or guess release
statuses. Draft release notes and epics remain separate future work.

Restoring a card from a released record clears its release assignment and leaves a restored note in the record. Restoring while its release is active removes its archived Done snapshot. Archived cards must be restored before changing their release. Cuts check admission for every Done card being archived and refuse starting, running, or waiting sessions on those cards; idle and disconnected sessions do not block. Released records retain the cut time and actor. An active release can be deleted, clearing its card assignments.

Agents can use `blerg-runner ticket release list|get|save|close|delete|assign` with `--board` to select a board. Release writes require Full access with a person behind it. `save --name NAME` creates a release; `--release UUID` edits one. `assign CARD --release UUID` assigns a card; omitting `--release` clears it.

### Board settings

The top-bar Model button contains only the four role models and their effort
settings. Board settings opens a viewport-sized dialog with General, Repos, Runs,
Filed tickets, and Models sections. Board admins can save changes; other readers
see the effective configuration. Runs includes the automation identity and where
workspaces run. Automatic chooses an available desktop or cluster; a desktop runs
locally and the cluster uses a remote sandbox.

Runs as displays You (username) for your own credential, or the other credential
owner's username. If Core cannot resolve the name, the dialog says it is unavailable
and never substitutes an account UUID. The board's credential-bound identity read
uses `POST /internal/tokens/board-automation/identity` on Core with the board mint
key and the credential as proof. It returns only `username`, has no account lookup
parameter, and requires a live human board session or a valid automation agent
token. `GET /api/boards/{id}/automation/identity` exposes that name to board readers when they open settings.
### Release and milestone progress

Open **Releases** and choose a release to see its burn-up chart. Switch to burn-down to
see remaining work. Scope, Done, and confirmed Done (Done without `to-test`) come from the
card audit trail. Daily bars show added and finished work. The data table provides every
plotted value, including remaining work. A milestone uses an existing board tag; enter it
under **Milestone progress** to chart that tag across the board.

Card count is the default. Size weighting uses S=1, M=2, L=4, XL=8, with unestimated cards
counting as 1. The finish range extrapolates the last seven days' net Done rate at 0.5× to
1.5× with fixed scope; it shows a fixed-scope velocity scenario. Reopened work
reduces Done. Archived unfinished cards leave scope; archived Done work stays finished.
Released records stop at their release date. Daily totals use UTC dates.

Older audit events did not retain release assignment, size, or tag snapshots. Their move
history is available, but earlier scope and confirmation may be inferred from current
values; those charts display a notice. New events retain the values needed for exact
reconstruction. Deleted columns in older history also lack their original Done semantics.

`GET /api/boards/{id}/releases/{release}` includes `burn` with daily `count` and `size`
series. Dates are UTC. Released charts stay at the cut and show how many cards were restored afterward. If chart history cannot be read, the release remains available with a chart notice.

`GET /api/boards/{id}/burn?milestone=tag` returns the same chart shape for a tag;
both require board card-read access.

### Retained commit summaries (BLG-404)

When work lands, its writer publishes `commit_summary` through the existing
`PATCH /api/cards/{id}` or `blerg_card_update` call, before ending the workspace.
Read the card first and supply its `if_match` version (including a literal `0`).
This is an explicit publication: commit subjects, authors and relative file names
become visible to **all card readers**, just like the card body. Reading the live
Git panel does not silently publish private workspace metadata.

`commit_summary` replaces the saved list of repositories. Each entry contains
`repository` (a host-independent label), `session_id`, `branch`, `base`,
`commits: [{sha,message,author,date}]`, and `files: [{path,adds,dels,binary?}]`.
Dates use RFC3339 and SHAs use full lowercase hex (40 or 64 characters).
Use immutable tip and base SHAs for stable full-diff comparisons. Rename stats may
include `old_path`. Multi-repository entries also carry `member_id`, `generation`
and `launch_generation` from the live panel. Keep the session link on the card.
Use `truncated: true` when the list is incomplete. Limits are 20 repositories,
100 commits and 300 files per repository, and 128 KiB for the complete payload.
An empty list clears the summary; omitting the property leaves it unchanged.

The saved panel survives session deletion, workspace cleanup and runner outages.
It stores metadata and counts only; patch/blob fields and absolute file paths are
rejected. Full diffs still use the existing owner-authorized live desktop path,
without a Git-host API. The diff page explains disconnected desktops, missing
objects, expired workspace bindings or an unavailable session. Saved metadata
does not grant access to local repository bytes. Existing cards start with `[]`
(migration 040); summaries are not backfilled from unavailable workspaces.
