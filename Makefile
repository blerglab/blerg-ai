.PHONY: fmt lint lint-go lint-web test test-web check-scripts build db-up db-down run

# The golangci-lint version CI pins (.github/workflows/ci.yml must name the same one:
# scripts/check_lint_pin.sh checks that). It is built with Go 1.27, so it loads Go 1.26
# modules. One configuration, at the repository root, covers all four Go modules.
GOLANGCI_LINT_VERSION := v2.13.1
GOLANGCI_LINT ?= golangci-lint
LINT_CONFIG   := $(CURDIR)/.golangci.yml
# packages/chat, runner/frontend and board/web are one npm workspace rooted here (`npm ci` at the
# repository root installs all three); core/web has its own install.
FRONTENDS     := core/web board/web packages/chat runner/frontend

fmt:    ; gofmt -w ./contracts ./core ./board ./runner

# Everything the CI lint steps run: the Go linter on contracts + core (through
# go.work) and on runner and board (standalone modules, workspace off), then
# ESLint in each frontend. The frontends need `npm ci` to have been run (at the root for the
# workspace, in core/web for that one).
lint: lint-go lint-web

lint-go:
	@./scripts/check_lint_pin.sh $(GOLANGCI_LINT)
	@test -z "$$(gofmt -l ./contracts ./core ./board ./runner)" || { echo "gofmt needed:"; gofmt -l ./contracts ./core ./board ./runner; exit 1; }
	$(GOLANGCI_LINT) run --config=$(LINT_CONFIG) ./contracts/... ./core/...
	cd runner && GOWORK=off $(GOLANGCI_LINT) run --config=$(LINT_CONFIG) ./...
	cd board && GOWORK=off $(GOLANGCI_LINT) run --config=$(LINT_CONFIG) ./...

lint-web:
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npm run lint) || exit 1; done

# The Go checks CI runs (go vet, then go test) on all four modules. Tests that need
# Postgres skip unless DATABASE_URL (contracts + core) or TEST_DATABASE_URL (runner,
# board) is set in the environment. Fixtures create isolated databases; the configured
# database user needs CREATEDB. Packages run in parallel. gofmt is checked by `make lint`.
test:
	go vet ./contracts/... ./core/...
	go test -timeout 30m ./contracts/... ./core/...
	cd runner && export GOWORK=off && go vet ./... && go test -timeout 30m ./...
	cd board && export GOWORK=off && go vet ./... && go test -timeout 30m ./...

# The frontend steps CI runs after lint: type-check, tests, production build. The
# runner frontend's browser tests need Chromium: npx playwright install chromium.
test-web:
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npx tsc --noEmit && npm test && npm run build) || exit 1; done

# The installer-script, messaging-CLI (needs pytest: pip install pytest), scrub and link
# checks CI runs.
check-scripts:
	BLERG_SKIP_KUBECTL=1 ./install/desktop/compose_test.sh
	BLERG_SKIP_KUBECTL=1 ./install/k8s/deploy_test.sh
	bash ./install/desktop/hostcheck_test.sh
	bash ./install/desktop/daemon_install_test.sh
	cd runner/.claude/skills/session-messaging && python3 -m unittest test_blerg_runner && python3 -m pytest -q blerg_runner_board_test.py
	./scripts/check_lint_pin.sh
	./scripts/scrub.sh
	./scripts/scrub_test.sh
	./scripts/check_links_test.sh
	./scripts/check_links.sh

build: ; go build -o bin/blerg-core ./core/cmd/blerg-core
db-up:  ; docker compose up -d postgres
db-down:; docker compose down
run:    ; go run ./core/cmd/blerg-core
