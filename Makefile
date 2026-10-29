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
	@for m in contracts core runner board; do (cd $$m && GOWORK=off $(GOLANGCI_LINT) run --config=$(LINT_CONFIG) ./...) || exit 1; done

lint-web:
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npm run lint) || exit 1; done

# The Go checks CI runs (go vet, then go test) on all four modules. Tests that need
# Postgres skip unless DATABASE_URL (contracts + core) or TEST_DATABASE_URL (runner,
# board) is set in the environment. Fixtures create isolated databases; the configured
# database user needs CREATEDB. Packages run in parallel. gofmt is checked by `make lint`.
test:
	@for m in contracts core runner board; do (cd $$m && GOWORK=off go vet ./... && GOWORK=off go test -p 1 -timeout 45m ./...) || exit 1; done

# Release gate: three fresh, uncached daemon runs with the race detector (#177).
.PHONY: test-daemon-race
test-daemon-race:
	@for pass in 1 2 3; do (cd runner && GOWORK=off go test -race ./internal/daemon -count=1 -timeout 45m) || exit 1; done

# The frontend steps CI runs after lint: type-check, tests, production build. The
# Runner needs Chromium and Firefox; the Board uses Chromium. Playwright reuses its cache.
test-web:
	cd runner/frontend && npx playwright install chromium firefox
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npx tsc --noEmit && npm test -- $(WEB_TEST_ARGS) && npm run build) || exit 1; done

# The installer-script (needs Linux pwsh), messaging-CLI (needs pytest: pip install pytest), scrub and link
# checks CI runs.
check-scripts:
	BLERG_SKIP_KUBECTL=1 ./install/desktop/compose_test.sh
	BLERG_SKIP_KUBECTL=1 ./install/k8s/deploy_test.sh
	bash ./install/desktop/hostcheck_test.sh
	bash ./install/desktop/daemon_install_test.sh
	PYTHONDONTWRITEBYTECODE=1 python3 ./install/desktop/daemon/enroll_test.py
	bash ./install/desktop/wsl_startup_test.sh
	cd runner/.claude/skills/session-messaging && python3 -m unittest test_blerg_runner test_blerg_runner_review_request && python3 -m pytest -q blerg_runner_board_test.py
	bash video/demo/demo_test.sh
	python3 -m unittest discover -s video/demo -p 'test_*.py' && python3 -m unittest discover -s video/build -p 'test_*.py'
	./scripts/check_lint_pin.sh
	./scripts/scrub.sh
	./scripts/scrub_test.sh
	./scripts/check_links_test.sh
	./scripts/check_links.sh

build: ; go build -o bin/blerg-core ./core/cmd/blerg-core
db-up:  ; docker compose up -d postgres
db-down:; docker compose down
run:    ; go run ./core/cmd/blerg-core
