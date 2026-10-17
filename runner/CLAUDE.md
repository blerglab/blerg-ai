# CLAUDE.md

This file provides guidance to AI coding assistants working in this directory.

## Project

blerg-runner manages agent and terminal sessions across workstation, sandbox and cluster runtimes.
Engine-specific behavior belongs in registered adapters; shared launch, persistence and UI paths
should use the engine contracts rather than assuming Claude Code.

## Building & testing

Tests need Go 1.26 or newer (see `go.mod`). Use `make test`: it runs `$HOME/go/bin/go`
when that exists and the `go` on your PATH otherwise.

Database tests read `TEST_DATABASE_URL` as an administration connection. The user
needs CREATEDB: fixtures migrate a private template once per package process and
copy it into an isolated database per test, cleaning up copies and templates.
Run `GOWORK=off go test -timeout 30m ./...` without `-p 1`; packages do not share
fixture tables. Use a dedicated test server. Helpers sweep only marked abandoned
fixtures in their administration/owner scope after 60 minutes, protected by run
leases and a normal drop that refuses active connections. Set
`BLERG_TEST_DB_MAX_AGE_MINUTES` to a whole value from 1 to 10080 to change the age;
invalid values disable sweeping. Legacy/unmarked `cx_` databases require manual
inspection before removal; the prefix alone is never deletion authorization.

## Session state detection — fix tests first

Session state (`running` / `waiting` / `idle`) is derived by `classifyScreen` in
`internal/daemon/screenstate.go` from rendered-screen snapshots. It is heuristic and
the source of most "wrong status" reports (usually a `waiting` session shown as
`idle`).

**When you hit ANY session-state issue, follow this order — do not skip a step:**

1. **Reproduce with evidence.** `tmux capture-pane -p -t blerg-<session-id>` to get
   the exact screen the classifier saw. Trace which branch of `classifyScreen` it
   hits and why.
2. **Write the failing test first.** Add an inline fixture to
   `internal/daemon/screenstate_test.go` AND save a capture to
   `internal/daemon/testdata/screens/<state>__<description>.txt` (replayed by
   `TestClassifyScreenSnapshots`). **Sanitize the capture first**: a real screen
   contains your paths, project and file names, hostnames and prose. Rewrite
   all of that into neutral invented text, keeping the layout (line count, line
   widths, prompts, hint bars, glyphs) that the classifier keys on, and confirm the
   classified state is unchanged. This repository is public; never commit a raw capture.
3. **Verify it fails** for the right reason (`make test`).
4. **Then change `classifyScreen`** — the minimal fix for the root cause.
5. **Verify it passes** and that no other state cases regressed (`make test`).

Tests and snapshots before code. Always verify failure before the fix and success
after. This keeps the heuristic honest as new prompt shapes appear.
