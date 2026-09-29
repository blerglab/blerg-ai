# Use the pinned toolchain if present, otherwise fall back to whatever
# `go` is on PATH (e.g. on a laptop). Override with `make GO=/path/to/go`.
GO ?= $(shell if [ -x $(HOME)/go/bin/go ]; then echo $(HOME)/go/bin/go; else command -v go; fi)

SERVER_IMAGE     ?= blerg-runner-server:latest
DAEMON_IMAGE     ?= blerg-runner-daemon:latest

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
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/blerg-runner-daemon ./cmd/daemon

.PHONY: build-frontend
build-frontend:
	cd frontend && npm run build

.PHONY: test
test:
	$(GO) test ./...

# ── docker ─────────────────────────────────────────────────────────────────────

.PHONY: docker-build
docker-build: docker-build-server docker-build-daemon

.PHONY: docker-build-server
docker-build-server: ## Repo-root context required (contracts/ replace) — confirmed by testing
	cd .. && docker build -f runner/Dockerfile.server -t $(SERVER_IMAGE) --build-arg VERSION=$(VERSION) .

# NOTE: this target (and Dockerfile.daemon) predates
# DAEMON.md's current architecture — the daemon "can't be containerized: it
# needs your own Claude auth and your own tmux, so it always runs directly
# on the host." Nothing in install/desktop/ or install/k8s/ references any
# of these; they're vestigial. Left as-is (not fixed, not removed) —
# confirmed broken the same way the other two were (repo-root context
# needed) but fixing a build for a deployment model the project no longer
# uses isn't worth it; deleting it is a bigger call than this pass should
# make unasked.
.PHONY: docker-build-daemon
docker-build-daemon:
	docker build -f Dockerfile.daemon -t $(DAEMON_IMAGE) --build-arg VERSION=$(VERSION) .

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

.PHONY: install-daemon
install-daemon: build-daemon ## Build + install systemd user service (reads .env.daemon); safe to re-run
	$(call write-service,.env.daemon)
	systemctl --user daemon-reload
	systemctl --user enable blerg-runner-daemon
	systemctl --user restart blerg-runner-daemon
	@echo "blerg-runner-daemon installed. Run: systemctl --user status blerg-runner-daemon"

.PHONY: restart-daemon
restart-daemon: build-daemon ## Rebuild binary and restart the systemd service
	systemctl --user restart blerg-runner-daemon

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
