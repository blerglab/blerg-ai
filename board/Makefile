# blerg-board — build & test. Requires the pinned Go toolchain (go >= 1.25).
GO ?= go

.PHONY: build test test-db server cli web lint

build: server cli

server:
	$(GO) build -o bin/blerg-board-server ./cmd/blerg-board-server

cli:
	$(GO) build -o bin/blerg-board ./cmd/blerg-board

# Unit tests (no database required).
test:
	$(GO) test -p 1 ./...

# Integration tests against a real Postgres. Example:
#   make test-db TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5544/blerg-board_test
test-db:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) $(GO) test -p 1 ./...

web:
	cd web && npm run build

# Lint & static analysis (what CI runs for this module).
lint:
	gofmt -l . | (! grep .)
	$(GO) vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed — skipped"
	cd web && npm run lint
