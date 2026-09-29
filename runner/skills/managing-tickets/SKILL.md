---
name: managing-tickets
description: >
  Use when organizing or managing a blerg-runner ticket board — creating, refining,
  splitting, tagging, prioritizing, ordering, and moving cards — typically inside
  a board-scoped Assist session (BLERG_RUNNER_BOARD_ID is set).
---

# managing-tickets

Guides an Assist session through organizing a blerg-runner ticket board via the
`blerg-runner` CLI. The daemon injects `BLERG_RUNNER_BOARD_ID` and `BLERG_RUNNER_BOARD_TOKEN`
into every Assist session, and the CLI picks them up automatically — you rarely
need to pass `<board>` explicitly.

## Five rules to work by

### 1. Read first

Before touching anything, load the board's real state:

```bash
blerg-runner board show          # columns, ticket counts, summary
blerg-runner ticket list backlog # scan a column in detail
blerg-runner ticket get abc123   # full detail on one card
```

Never guess at what exists. If the user describes a card, look it up by name
or id before modifying it.

### 2. One idea per card

Convert loose thoughts into well-scoped tickets: one idea per card, a clear
specific title, and a body that captures intent and acceptance criteria.
Set `--repos`, `--tag`, `--priority`, and `--size` when you know them — a
fully-filled card costs the user nothing and saves triage time later.

### 3. Collaborate and confirm

Ask **one question at a time** — don't front-load a list of questions.
Before any destructive operation (split, archive, removing a dependency,
removing or renaming a column) **propose what you're about to do and wait
for the user's explicit go-ahead**.

### 4. Encode ordering as dependencies

When the user says "X must happen before Y", record that with `ticket dep add`
rather than relying on column position. Then run `board order` to surface a
ready-first execution plan — tickets whose dependencies are all in a terminal
column float to the top.

### 5. Keep the board tidy

As work finishes, move cards to a terminal ("done") column with `ticket move`.
Periodically sweep column order and tags for consistency. Never delete a
non-empty column — rename or merge it instead.

---

## CLI reference

Columns and tickets can be addressed by **id or unambiguous name** within the
board. `<board>` defaults to `$BLERG_RUNNER_BOARD_ID` for verbs that allow omitting
it. Valid `priority`: `low` | `medium` | `high` | `urgent`. Valid `size`:
`XS` | `S` | `M` | `L` | `XL`.

### Board

```bash
# Show board state — columns, ticket counts, ready/blocked breakdown
blerg-runner board show

# Print a dependency-aware ready-first ordering of all open tickets
blerg-runner board order
```

### Columns

```bash
# Add a new "Review" column after the existing "In Progress" column
blerg-runner column add --name "Review" --after "In Progress"

# Rename a column
blerg-runner column rename "Review" --name "In Review"

# Move a column to a different position
blerg-runner column move "In Review" --after "In Progress"

# Mark (or unmark) a column as terminal — tickets there count as "done"
blerg-runner column set "Shipped" --terminal
blerg-runner column set "Cancelled" --no-terminal

# Remove an empty column
blerg-runner column rm "Old Backlog"
```

### Tickets

```bash
# List tickets in a column, filtered and paginated
blerg-runner ticket list --column backlog --priority high --ready --limit 20

# Show full detail for one ticket
blerg-runner ticket get abc123

# Create a new ticket (body, repos, tags, priority, size all optional)
blerg-runner ticket create \
  --title "Add rate-limiting to the API gateway" \
  --body "Implement token-bucket rate limiting; 100 req/min per client." \
  --repos blerg-runner \
  --tag infra --tag security \
  --priority high --size M

# Update fields on an existing ticket
blerg-runner ticket update abc123 --priority urgent --add-tag performance

# Move a ticket to a column (optionally position it relative to another card)
blerg-runner ticket move abc123 --column "In Progress" --after def456

# Split one ticket into two (originals are archived; children inherit deps)
blerg-runner ticket split abc123 --into "Add rate-limit middleware" --into "Wire rate-limit config into deploy"

# Record that ticket abc123 must be done before ghi789 can start
blerg-runner ticket dep add ghi789 --on abc123

# Remove a dependency
blerg-runner ticket dep rm ghi789 --on abc123

# Archive a completed or cancelled ticket
blerg-runner ticket archive abc123
```

### Tags

```bash
# List all tags used on the board (useful before applying or cleaning up tags)
blerg-runner tag list
```

---

## Typical flow

```bash
# 1. Orient yourself
blerg-runner board show

# 2. Triage a rough idea the user just described
blerg-runner ticket create --title "Cache board-order results in Redis" \
  --body "board order is slow on large boards; cache with a 30 s TTL." \
  --tag performance --priority medium --size S

# 3. User says "that depends on the Redis connection pool work"
blerg-runner ticket dep add <new-id> --on <redis-pool-ticket-id>

# 4. See what's unblocked now
blerg-runner board order

# 5. Finish a card — move it to the terminal column
blerg-runner ticket move <id> --column "Done"
```
