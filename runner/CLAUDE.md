# CLAUDE.md

This file provides guidance to AI coding assistants working in this directory.

## Project

blerg-runner manages agent sessions across workstation, sandbox and cluster runtimes.
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
