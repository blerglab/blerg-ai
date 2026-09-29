# Contributing to Blerg

Thanks for your interest. Blerg is a monorepo of three services and their installers:

| Path | What it is |
|---|---|
| `contracts/` | Shared Go types and token formats |
| `core/` | Control plane: accounts, login, agent tokens, the credential vault |
| `board/` | Kanban board that agents write to and humans curate |
| `runner/` | Launches and watches agent sessions; includes the workstation daemon |
| `install/` | Desktop (Docker Compose) and Kubernetes installers |

Component-specific notes live next to the code, for example
[`board/CONTRIBUTING.md`](board/CONTRIBUTING.md) and the `README.md` in each component.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the
  approach before you spend time on it.
- Security problems go through [`SECURITY.md`](SECURITY.md), never a public issue.
- By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development setup

**Prerequisites**

- **Go 1.25 or newer.** Check with `go version`. Go 1.21+ downloads the toolchain named in
  `go.mod` by itself; an older `go` (the one Debian and Ubuntu package is 1.18) cannot, and
  fails with confusing errors. Install a current Go from <https://go.dev/dl/> and put it
  first on your `PATH`.
- **Node.js 22 or newer** with npm, for the three frontends.
- `git`, and `tmux` (some daemon tests drive a real tmux and skip without it).
- **golangci-lint v2.13.1**, the version CI pins, for `make lint`
  ([install](https://golangci-lint.run/docs/welcome/install/)). It must be built with Go 1.25 or newer.
- **Docker with Compose v2 is optional.** It is only for `make db-up` (a throwaway
  Postgres), for running the full stack with the desktop installer, and for the
  Compose check in `make check-scripts`, which skips without it.

**Module layout.** `contracts/` and `core/` are in the Go workspace (`go.work`) and build
from the repository root. `runner/` and `board/` are standalone modules that are not in
the workspace: run their Go commands from inside the module directory with `GOWORK=off`,
or Go reports `directory prefix . does not contain modules listed in go.work`.

```
go test ./contracts/... ./core/...           # from the repository root
cd runner && GOWORK=off go test ./...
cd board  && GOWORK=off go test ./...
```

## Tests

Everything below passes on a fresh checkout with no database. Tests that need Postgres
skip unless it is configured.

**Make targets** (from the repository root; they mirror the jobs in `.github/workflows/ci.yml`):

| Target | Runs | Needs |
|---|---|---|
| `make test` | `go vet`, then `go test` on contracts, core, runner and board | nothing; database tests run only if the variables below are set |
| `make test-web` | `tsc --noEmit`, tests and production build in each frontend | `npm ci` in each first; Chromium for `runner/frontend` |
| `make lint` | gofmt and golangci-lint on all four Go modules, then ESLint in each frontend (`make lint-go` and `make lint-web` run the halves) | golangci-lint; `npm ci` in each frontend |
| `make check-scripts` | installer, messaging-CLI, scrub and link checks | `python3` with `pytest` (`pip install pytest`); the `docker compose` CLI makes the Compose check run instead of skip |
| `make db-up` / `make db-down` | start / stop Postgres from `docker-compose.yml` | **Docker** |

Lint failures block a merge. A `//nolint` or `eslint-disable` must name the rule and give
a reason. `make test` is not a full CI run: also run `make lint`, `make test-web` and
`make check-scripts`.

**Running one test.**

```
go test ./contracts/logsafe -run TestSanitize -v                  # root, workspace modules
cd runner && GOWORK=off go test ./internal/server -run TestName   # runner or board
cd runner/frontend && npm test -- --project unit src/components/AgentChatView.test.tsx
```

**With a database (needs Postgres 16, for example from Docker).** The variables are
`DATABASE_URL` for `contracts/` and `core/`, and `TEST_DATABASE_URL` for `runner/` and
`board/`. Give each module its own empty database, because runner and board tests share
theirs (`-p 1` runs packages one at a time for that reason).
`make db-up` starts Postgres on `localhost:5432` (user and password `blerg`, database
`blerg_core`); create the other two by hand:

```
make db-up                                                     # needs Docker
docker compose exec postgres createdb -U blerg blerg_runner_test
docker compose exec postgres createdb -U blerg blerg_board_test

export DATABASE_URL='postgres://blerg:blerg@localhost:5432/blerg_core?sslmode=disable'
go test ./contracts/... ./core/...
cd runner && GOWORK=off TEST_DATABASE_URL='postgres://blerg:blerg@localhost:5432/blerg_runner_test?sslmode=disable' go test -p 1 ./...
cd board  && GOWORK=off TEST_DATABASE_URL='postgres://blerg:blerg@localhost:5432/blerg_board_test?sslmode=disable'  go test -p 1 ./...
```

With `DATABASE_URL` exported, `make test` runs the core database tests too. Set
`TEST_DATABASE_URL` per module, as above, not for `make test`, so runner and board do not
share a database.

Known caveat: three tests in `core/internal/identity` (`TestRevokeAccountEverywhereKillsAgentTokens`,
`TestRevokeAccountEverywhereMarksAgentTokenRowsRevoked`,
`TestCreateAgentTokenNeverPersistsABornDeadToken`) compare the database's clock with the local
one and fail if they differ by about a second. Run Postgres on the same machine, or
expect those three to fail against a remote database whose clock runs ahead. That is
not a bug in your change. CI also configures a git identity for tests that commit
(`git config --global user.name`, `user.email`, `init.defaultBranch main`).

**Frontends.** Each has the same steps CI runs, in this order; run all of them, because the
production build type-checks files that the test run does not. `npm ci` (not `npm install`)
gives the locked versions.

```
cd core/web        && npm ci && npm run lint && npx tsc --noEmit && npm test && npm run build
cd board/web       && npm ci && npm run lint && npx tsc --noEmit && npm test && npm run build
cd runner/frontend && npm ci && npx playwright install chromium && npm run lint && npx tsc --noEmit && npm test && npm run build
```

`runner/frontend` also has browser tests that need a real Chromium; without one `npm test`
fails with "Executable doesn't exist". `npx playwright install chromium` downloads it once
(add `--with-deps` on Linux to install the system libraries; that needs sudo).

**Installer scripts, messaging CLI, scrub and links** (`make check-scripts` runs them all):

```
BLERG_SKIP_KUBECTL=1 ./install/desktop/compose_test.sh   # skips without `docker compose`
BLERG_SKIP_KUBECTL=1 ./install/k8s/deploy_test.sh        # skips the kubectl dry-run; no cluster needed
bash ./install/desktop/hostcheck_test.sh                 # engine preflight; needs no docker
(cd runner/.claude/skills/session-messaging && python3 -m unittest test_blerg_runner && python3 -m pytest -q blerg_runner_board_test.py)
./scripts/scrub.sh && ./scripts/scrub_test.sh            # no hostnames, addresses or personal paths
./scripts/check_links_test.sh && ./scripts/check_links.sh   # doc links and stale references
```

Run these from a git checkout: outside one, the scrub and link scripts scan every file
under the directory, including caches you put there.

## Running from source

The whole stack (core, board, runner and the workstation daemon) is run with the desktop
installer, which builds it from your checkout: see the Install section of the
[README](README.md). It needs Docker.

For core alone, with any Postgres (Docker's from `make db-up`, or your own):

```
export DATABASE_URL='postgres://blerg:blerg@localhost:5432/blerg_core?sslmode=disable'
export BLERG_CORE_LOCAL_KEY="$(openssl rand -base64 32)"   # encrypts the credential vault; keep it to reuse the database
export BLERG_CORE_COOKIE_SECURE=false                       # plain http://localhost only
export BLERG_CORE_LISTEN=127.0.0.1:8080                    # host:port; keeps this insecure-cookie dev instance off your network
make run
```

The first boot logs the one-time admin password once (`BLERG_BOOTSTRAP_ADMIN_PASSWORD`).
Every variable is listed in [`core/docs/CONFIG.md`](core/docs/CONFIG.md). Running the board
and runner servers from source is not covered here.

## What we look for in a change

- **Tests first for bugs.** Reproduce the bug in a test, watch it fail, then fix it.
- **Fail closed.** When a credential, a capability or a piece of configuration is
  missing, refuse with a clear reason. Do not fall back to something broader.
- **Keep secrets out of everything.** No credential may reach a log line, an error
  message, an API response, an event or a file on disk in clear text. Code that
  handles one needs a test that proves it.
- **Nothing specific to one person's setup.** No real hostnames, IP addresses, account
  names or home-directory paths in tracked files, tests included. Use `example.com`,
  `example-org/example-repo` and the documentation address ranges.
- **Extend by registering, not by branching.** Engines, git providers and model
  sources each have a registry. A new one should be a new entry, not a new `if`.
- **Match the code around you**: naming, comment density and idiom.
- **Update the docs in the same change** when you change behaviour, a flag or an
  environment variable.

## Commits and pull requests

- Branch from `main`. Name branches `feat/…`, `fix/…`, `docs/…`, `chore/…`,
  `refactor/…`, `test/…` or `ci/…`.
- Write commit subjects as a short summary that starts with the area you changed, for
  example `daemon: refuse a repos root another user can write to`; a `fix(daemon):`
  style prefix is fine too. Nothing enforces the form.
- Keep a pull request to one logical change. The pull request template asks what you
  tested and what you could not; answer both.
- CI must pass. It runs the same checks as `make test`, `make lint`, `make test-web`
  and `make check-scripts` above, with Postgres and Chromium available.

## Versions and releases

One version covers the whole repository: core, board, runner and the installers are released
together. It is pre-1.0 semver, `0.MINOR.PATCH`. A minor release may contain new features and
breaking changes (each one called out in the changelog); a patch release contains fixes only.
The root [`VERSION`](VERSION) file holds the current version and [`CHANGELOG.md`](CHANGELOG.md)
records what changed, in [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format.

To cut a release, from an up-to-date `main` with CI green:

1. Move the `Unreleased` entries in `CHANGELOG.md` under a new `## [X.Y.Z] - YYYY-MM-DD`
   heading, and update the compare links at the bottom.
2. Put `X.Y.Z` in `VERSION`.
3. Commit both as `release: vX.Y.Z` through a pull request, and merge it.
4. Tag the merge commit `vX.Y.Z` and push the tag.
5. Create a GitHub release for the tag, using that version's changelog entry as the notes.

The repository has no release automation and no `make release` target; the steps above are the
whole process.

**How a running service reports its version.** Each service is stamped at build time from a
`VERSION` build argument, and reports `dev` when none was given.

| Service | Where to read it | What stamps it |
|---|---|---|
| core | `version` in `GET /api/site` (public) and in the `GET /agents` manifest; the web footer | `BLERG_VERSION` in the image environment, set from the `VERSION` build argument |
| board | `GET /api/version`; the `GET /agents` manifest | `-X main.version`, from the `VERSION` build argument |
| runner server and daemon | the server's `GET /agents` manifest, the version the UI shows for the server and each daemon, and the version core's component registry lists | `-X main.version`, from the `VERSION` build argument, or `git describe` in `runner/Makefile` |

The Kubernetes installer passes the image `TAG` from `install/k8s/.env` as that build argument,
so set `TAG` to the release version (for example `v0.1.0`) to have the cluster report it. The
desktop stack passes `BLERG_VERSION` from your environment and otherwise builds with `dev`; and
the workstation daemon that `daemon/install.sh` builds is stamped `dev` today.

## Licence

Blerg is licensed under the [Apache License 2.0](LICENSE). By submitting a
contribution you agree that it is licensed under the same terms, as set out in
section 5 of the licence.
