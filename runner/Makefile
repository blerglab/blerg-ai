# Use the pinned toolchain if present, otherwise fall back to whatever
# `go` is on PATH (e.g. on a laptop). Override with `make GO=/path/to/go`.
GO ?= $(shell if [ -x $(HOME)/go/bin/go ]; then echo $(HOME)/go/bin/go; else command -v go; fi)

SERVER_IMAGE     ?= blerg-runner-server:latest

COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo dev)

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# ── build ──────────────────────────────────────────────────────────────────────

.PHONY: build
build: build-server build-daemon build-runner build-frontend

.PHONY: build-runner
build-runner:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/blerg-runner ./cmd/runner

.PHONY: docker-build-devcontainer
docker-build-devcontainer: ## Repo-root context required (contracts/ replace) — confirmed by testing
	cd .. && docker build -f runner/Dockerfile.devcontainer -t blerg-runner-devcontainer:$(VERSION) -t blerg-runner-devcontainer:latest .

.PHONY: build-server
build-server:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/blerg-runner-server ./cmd/server

.PHONY: build-daemon
build-daemon:
	$(GO) build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/blerg-runner-daemon ./cmd/daemon

# Capture the updater's live call before compilation. Both helper invocations
# descend from this make process, even though recipe shells differ.
.PHONY: begin-daemon-update
begin-daemon-update:
	@bash -c 'set -a; . ./.env.daemon; set +a; if [ -x ./bin/blerg-runner-daemon ]; then ./bin/blerg-runner-daemon begin-update >/dev/null 2>&1 || true; fi'

ifneq ($(filter install-daemon restart-daemon,$(MAKECMDGOALS)),)
build-daemon: begin-daemon-update
endif

.PHONY: build-frontend
build-frontend:
	cd frontend && npm run build

.PHONY: test
test:
	$(GO) test -p 1 -timeout 45m ./...

# ── docker ─────────────────────────────────────────────────────────────────────

.PHONY: docker-build
docker-build: docker-build-server

.PHONY: docker-build-server
docker-build-server: ## Repo-root context required (contracts/ replace) — confirmed by testing
	cd .. && docker build -f runner/Dockerfile.server -t $(SERVER_IMAGE) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo dev) .

# Deploying to a cluster: use install/k8s/deploy.sh (see install/k8s/README.md).

.PHONY: vapid-keys
vapid-keys:
	@$(GO) run ./scripts/vapid/main.go


# ── daemon (local, for runner development) ─────────────────────────────────────────────────────────

SYSTEMD_USER_DIR := $(HOME)/.config/systemd/user
DAEMON_SERVICE   := $(SYSTEMD_USER_DIR)/blerg-runner-daemon.service

.PHONY: run-daemon
run-daemon: build-daemon ## Build and run daemon locally (reads .env.daemon)
	@bash -c 'set -a && . ./.env.daemon && set +a && exec ./bin/blerg-runner-daemon'

define write-service
	@mkdir -p $(SYSTEMD_USER_DIR)
	@printf '[Unit]\nDescription=Blerg Runner Daemon\nAfter=network.target\n\n[Service]\nType=simple\nKillMode=process\nWorkingDirectory=$(CURDIR)\nEnvironmentFile=$(CURDIR)/$(1)\nExecStart=$(CURDIR)/bin/blerg-runner-daemon\nRestart=always\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n' \
		> $(DAEMON_SERVICE)
endef

# The daemon observes the requesting process and writes its own planned update.
# A missing older-daemon socket keeps normal pause behavior without blocking install.
define restart-service
@bash -c 'set -a; . ./.env.daemon; set +a; ./bin/blerg-runner-daemon prepare-update || true; systemctl --user restart blerg-runner-daemon; rc=$$?; if [ $$rc -ne 0 ]; then ./bin/blerg-runner-daemon cancel-update || true; fi; exit $$rc'
endef

.PHONY: install-daemon
install-daemon: build-daemon ## Build + install systemd user service (reads .env.daemon); safe to re-run
	$(call write-service,.env.daemon)
	systemctl --user daemon-reload
	systemctl --user enable blerg-runner-daemon
	$(restart-service)
	@echo "blerg-runner-daemon installed. Run: systemctl --user status blerg-runner-daemon"

.PHONY: restart-daemon
restart-daemon: build-daemon ## Rebuild binary and restart the systemd service
	$(restart-service)

.PHONY: uninstall-daemon
uninstall-daemon: ## Stop, disable, and remove the systemd user service
	-systemctl --user disable --now blerg-runner-daemon
	rm -f $(DAEMON_SERVICE)
	systemctl --user daemon-reload

# ── dev ────────────────────────────────────────────────────────────────────────

.PHONY: dev-server
dev-server:
	$(GO) run ./cmd/server

.PHONY: dev-frontend
dev-frontend:
	cd frontend && npm run dev

# ── misc ───────────────────────────────────────────────────────────────────────

.PHONY: clean
clean:
	rm -rf bin/ frontend/dist/

.PHONY: help
help:
	@echo "blerg-runner Makefile (runner development; to install Blerg see the root README)"
	@echo ""
	@echo "Build/test:"
	@echo "  make build              server + daemon + runner CLI + frontend"
	@echo "  make docker-build-server  build the server image"
	@echo "  make test               run Go tests"
	@echo "  make vapid-keys         generate a VAPID key pair"
	@echo ""
	@echo "Local daemon (reads .env.daemon; for the supported install use install/desktop/daemon/install.sh):"
	@echo "  make run-daemon         build + run daemon in the foreground"
	@echo "  make install-daemon     build + install a systemd user service"
	@echo "  make restart-daemon     rebuild + restart the systemd service"
	@echo "  make uninstall-daemon   stop, disable, and remove the systemd service"
	@echo ""
	@echo "Dev:"
	@echo "  make dev-server         go run server"
	@echo "  make dev-frontend       vite dev server"
