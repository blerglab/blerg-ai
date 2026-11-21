.PHONY: check-format test-verification pre-merge check-cli check-other-scripts fmt lint lint-go lint-web lint-theme test test-web check-scripts build db-up db-down run release-perf release-perf-accept

# The golangci-lint version CI pins (.github/workflows/ci.yml must name the same one:
# scripts/check_lint_pin.sh checks that). It is built with Go 1.27, so it loads Go 1.26
# modules. One configuration covers the production and verification modules.
GOLANGCI_LINT_VERSION := v2.13.1
GOLANGCI_LINT ?= golangci-lint
LINT_CONFIG   := $(CURDIR)/.golangci.yml
GO_MODULES    := contracts core board runner scripts/verification
GO_FORMAT_DIRS := $(addprefix ./,$(GO_MODULES))
TOOL_WORKERS  ?= $(or $(BLERG_TOOL_WORKERS),2)
# packages/chat, runner/frontend and board/web are one npm workspace rooted here (`npm ci` at the
# repository root installs all three); core/web has its own install.
FRONTENDS     := core/web board/web packages/chat runner/frontend

fmt:    ; gofmt -w $(GO_FORMAT_DIRS)

check-format:
	@test -z "$$(gofmt -l $(GO_FORMAT_DIRS))" || { echo "gofmt needed:"; gofmt -l $(GO_FORMAT_DIRS); exit 1; }

pre-merge:
	@./scripts/pre-merge.sh

# Everything the CI lint steps run: the Go linter on contracts + core (through
# go.work), runner and board, and the verification tools (workspace off), then
# ESLint in each frontend, then the theme-token audit. The frontends need `npm ci` to have been
# run (at the root for the workspace, in core/web for that one).
lint: lint-go lint-web lint-theme

lint-go: check-format
	@./scripts/check_lint_pin.sh $(GOLANGCI_LINT)
	@for m in $(GO_MODULES); do (cd $$m && GOWORK=off GOMAXPROCS=$(TOOL_WORKERS) $(GOLANGCI_LINT) run --concurrency=$(TOOL_WORKERS) --config=$(LINT_CONFIG) ./...) || exit 1; done

lint-web:
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npm run lint) || exit 1; done

# BLG-473: every color in runner/frontend/src, packages/chat/src, packages/board, board/web/src
# and core/web/src must come from the central theme tokens, so a bundled or uploaded theme applies
# everywhere with no code change. scripts/themecheck/README.md has the rules and the allow-list
# marker syntax.
lint-theme:
	@cd scripts/themecheck && GOWORK=off go run . $(CURDIR)

# Production Go checks plus the verification-tool prerequisite. Tests that need
# Postgres skip unless DATABASE_URL (contracts + core) or TEST_DATABASE_URL (runner,
# board) is set in the environment. Fixtures create isolated databases; the configured
# database user needs CREATEDB. Packages run in parallel. gofmt is checked by `make lint`.
test: test-verification
	@./scripts/pre-merge.sh --suite contracts-vet --suite contracts-go \
		--suite core-vet --suite core-go --suite runner-vet --suite runner-go \
		--suite board-vet --suite board-go

# Release gate: three fresh, uncached daemon runs with the race detector (#177).
.PHONY: test-daemon-race
test-daemon-race:
	@./scripts/pre-merge.sh --release --suite daemon-race

# Release performance check (BLG-453): run once near a release cut on a
# quiet machine, never as part of pre-merge. See docs/pre-merge.md's
# "Release performance check" section and docs/design/release-performance.md.
# Each run gets its own timestamped evidence directory, never a shared one a
# prior run's leftover binaries/dist/candidate could stick around in (MED e,
# Codex review of d7e50390). /tmp/blerg-release-perf-logs always points at the
# most recent run, so release-perf-accept's default --candidate path still
# works; scripts/release-perf-latest-logs.sh keeps that symlink update safe
# against a real leftover directory from before this existed (Codex round 2:
# plain `ln -sfn` would create the link *inside* it instead of replacing it).
# The run's own exit status survives that update (rc=$$?; ...; exit $$rc) —
# losing it here would let a refusal or a flagged regression exit 0.
release-perf:
	@cd runner && GOWORK=off go build -o /tmp/blerg-release-perf ./cmd/release-perf
	@logs=/tmp/blerg-release-perf-logs-$$(date +%Y%m%d-%H%M%S); \
	systemd-run --user --scope --unit="blerg-release-perf-$$(date +%s)" \
		--property=CPUWeight=10 --property=IOWeight=10 --collect \
		env TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
		/tmp/blerg-release-perf --logs $$logs $(RELEASE_PERF_ARGS); \
	rc=$$?; \
	./scripts/release-perf-latest-logs.sh $$logs /tmp/blerg-release-perf-logs; \
	exit $$rc

# Promotes a release-perf candidate baseline to the official, committed one
# (docs/design/release-performance.md's "Baseline acceptance"). Run only
# after the owner has accepted the release. VERSION and CANDIDATE default to
# the usual evidence-directory path and output location; override either
# with VERSION=0.6.0 RELEASE_TAG=v0.6.0.
release-perf-accept:
	@cd runner && GOWORK=off go build -o /tmp/blerg-release-perf-accept ./cmd/release-perf-accept
	@/tmp/blerg-release-perf-accept \
		--candidate $(or $(CANDIDATE),/tmp/blerg-release-perf-logs/$(VERSION)-candidate.json) \
		--out docs/performance-baselines/$(VERSION).json \
		--release-tag $(RELEASE_TAG)

# The frontend steps CI runs after lint: type-check, tests, production build. The
# Runner needs Chromium and Firefox; the Board uses Chromium. Playwright reuses its cache.
test-web:
	cd runner/frontend && npx playwright install chromium firefox
	node scripts/test-default-theme-colors.mjs
	@for d in $(FRONTENDS); do echo "== $$d"; (cd $$d && npx tsc --noEmit && npm test -- $(WEB_TEST_ARGS) && npm run build) || exit 1; done

# The installer-script (needs Linux pwsh), messaging-CLI (needs pytest: pip install pytest), scrub and link
# checks CI runs.
check-scripts: check-cli check-other-scripts check-installer-syntax

.PHONY: check-installer-syntax check-installer-bash32
check-installer-syntax:
	bash scripts/check-installer-heredocs_test.sh
	bash scripts/check-installer-heredocs.sh

check-installer-bash32: check-installer-syntax
	docker run --rm -v "$(CURDIR):/src:ro" -w /src bash:3.2 bash scripts/check-installer-syntax.sh

check-cli:
	cd runner/.claude/skills/session-messaging && python3 -m unittest test_blerg_runner test_blerg_runner_review_request && python3 -m pytest -q blerg_runner_board_test.py

# Shared by make test, script checks, the local gate, and CI.
test-verification:
	cd scripts/verification && GOWORK=off GOMAXPROCS=$(TOOL_WORKERS) go vet -p=$(TOOL_WORKERS) ./...
	cd scripts/verification && GOWORK=off GOMAXPROCS=$(TOOL_WORKERS) go test -vet=off -p=$(TOOL_WORKERS) ./...
	cd scripts/verification && GOWORK=off GOMAXPROCS=$(TOOL_WORKERS) go build -p=$(TOOL_WORKERS) ./...

check-other-scripts: test-verification
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/coordinating_skill_test.py
	BLERG_SKIP_KUBECTL=1 ./install/desktop/compose_test.sh
	BLERG_SKIP_KUBECTL=1 ./install/k8s/deploy_test.sh
	bash ./install/desktop/hostcheck_test.sh
	bash ./install/desktop/daemon_install_test.sh
	bash ./install/desktop/workspace_worker_image_test.sh
	PYTHONDONTWRITEBYTECODE=1 python3 ./install/desktop/daemon/enroll_test.py
	bash ./install/desktop/wsl_startup_test.sh
	bash video/demo/demo_test.sh
	python3 -m unittest discover -s video/demo -p 'test_*.py' && python3 -m unittest discover -s video/build -p 'test_*.py'
	./scripts/check_lint_pin.sh
	./scripts/scrub.sh
	./scripts/scrub_test.sh
	./scripts/release-public_test.sh
	./scripts/check_links_test.sh
	./scripts/check_links.sh
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p pre_merge_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -p 'test_*.py'
	node --check scripts/gate_vitest_reporter.mjs
	node --test runner/frontend/scripts/redact.test.mjs runner/frontend/scripts/serverTiming.test.mjs
	./scripts/release-perf-latest-logs_test.sh

build: ; go build -o bin/blerg-core ./core/cmd/blerg-core
db-up:  ; docker compose up -d postgres
db-down:; docker compose down
run:    ; go run ./core/cmd/blerg-core
