# CLAUDE.md

blerg-board is an issue/feature tracker where agents write the cards and humans
curate. Sessions spawned from blerg-board cards work in this repo.

## Build & test

- Go server: `go build ./...`; tests need Postgres:
  `TEST_DATABASE_URL='postgres://...' go test -p 1 ./...` (shared DB — keep -p 1)
- Web UI: `npm ci` at the REPOSITORY root (board/web is a member of the npm
  workspace there, with packages/chat and runner/frontend), then
  `cd web && npm run build`. A session's conversation is the shared chat,
  `@blerglab/chat`, mounted by `web/src/SessionChat.tsx` over the `/api/chat`
  proxy (`internal/api/chat.go`); change the chat in `packages/chat`, not here.
  The SVG charts and `MdText` (card bodies) are still the board's own.

## Conventions

- The trail-signage palette lives in web/src/styles.css custom properties —
  new UI derives from those tokens, no new hex values
- Card writes go through the admission gate; read /onboard (in-cluster:
  curl -s "$BLERG_BOARD_URL/onboard") before filing or moving cards
- Migrations: internal/db/migrations/NNN_name.sql, embedded + auto-run
