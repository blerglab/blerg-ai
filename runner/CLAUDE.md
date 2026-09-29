# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

blerg-runner is a local tool for managing Claude Code projects and driving agent sessions. Keep scope small and focused on local Claude/agent management use cases.

## Plugins

This project has the following Claude Code plugins enabled (see `.claude/settings.json`):
- `frontend-design` — for building UI components
- `superpowers` — for structured planning, debugging, and development workflows

## Building & testing

Tests require the pinned Go toolchain at `$HOME/go/bin/go` (`go.mod` declares
`go 1.25`, which the system `go` is too old to satisfy). Use `make test` — it already
points at the right binary.

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
