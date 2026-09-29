# Contributing to blerg-board

## Branches

`main` is always releasable. Work happens on named branches:

- `feat/<kebab-case>` — new behavior (e.g. `feat/add-label-search`)
- `fix/<kebab-case>` — bug fixes (e.g. `fix/correct-chore-api`)
- `chore/` · `docs/` · `refactor/` · `test/` · `ci/` — the rest
- `wip/<session-id>` — reserved for runner sessions (machine-generated)

CI does not enforce branch names; they keep the history readable.

## Versioning

The board is released with the rest of the repository under one version: see
[Versions and releases](../CONTRIBUTING.md#versions-and-releases) in the root
`CONTRIBUTING.md`, and the root `CHANGELOG.md`. Pre-1.0 semver applies, and breaking changes
belong in minor bumps with a note in the changelog.

The Docker build stamps its `VERSION` build argument into the binary; check a running server
with `GET /api/version`. It reports `dev` if the image was built without one.

## Lint & static analysis

- Go: `gofmt`, `go vet`, and `golangci-lint` (see `.golangci.yml` at the repository root)
- TypeScript: `tsc -b --noEmit` (strict) and ESLint (`web/eslint.config.js`)

Run everything with `make lint` (from `board/`).

## Tests

`make test` (unit) and `make test-db TEST_DATABASE_URL=...` (integration,
needs Postgres; keep `-p 1` — packages share the database).
