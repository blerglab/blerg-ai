# blerg-runner-sandbox — the "Local sandbox" runtime image. A throwaway
# container that "Local sandbox" sessions run in on the desktop/laptop daemon
# (see ../internal/daemon/sandbox.go), as an alternative to running directly
# on the host. Only the target repo (bind-mounted at /workspace) and the
# operator's engine credentials (~/.claude, ~/.codex, and/or
# ~/.hermes/{config.yaml,.env} — see sandbox.go) go in; everything else stays
# inside the container.
#
# One image, all three CLI engines (see LaunchSheet's Engine picker) —
# simpler than three images to keep in sync for a modest size cost.
#
# Toolchain mirrors ../Dockerfile.devcontainer (the k8s "Cluster pod" runtime
# image) for environment parity between the two isolated runtimes, minus that
# image's blerg-runner PID1 binary. Both images include the repository's Go
# toolchain so isolated coding children can build and test before committing.
# Both images now install all three engine CLIs.
FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS go

FROM node:22-bookworm-slim@sha256:43ac6c60b8f89723f746e8a92ce91abd5017e627ce1ddfe4238355d3a30b772c AS node

FROM go AS shell-builder
WORKDIR /src
COPY contracts/ /src/contracts/
COPY runner/ /src/runner/
WORKDIR /src/runner
RUN GOWORK=off CGO_ENABLED=0 go build -o /out/blerg-shell-supervisor ./cmd/shell-supervisor \
    && GOWORK=off CGO_ENABLED=0 go build -o /out/blerg-workspace-explorer ./cmd/workspace-explorer

FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git curl bash tmux ripgrep jq unzip build-essential \
    python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*

# Node.js LTS, copied from the official image (same Debian release as the
# base) rather than installed by a script piped into a root shell: the node
# binary and npm only, no corepack or yarn.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Two of the three engine CLIs — each billed to the operator's own
# subscription/account login, not a metered API key —, at the exact
# versions in engines/package.json, verified against the integrity
# hashes in its package-lock.json. To move to a newer engine version, change
# it there (`npm install --package-lock-only` refreshes the lockfile) and
# rebuild. Linked into /usr/local/bin so they are on PATH in every kind of
# shell.
COPY runner/sandbox/engines/package.json runner/sandbox/engines/package-lock.json /opt/blerg-engines/
RUN cd /opt/blerg-engines && npm ci --omit=dev --no-audit --no-fund \
    && ln -s /opt/blerg-engines/node_modules/.bin/claude /usr/local/bin/claude \
    && ln -s /opt/blerg-engines/node_modules/.bin/codex /usr/local/bin/codex \
    && rm -rf /root/.npm

# Non-root user, UID 1000 to match the common single-user-desktop default so
# files written into the bind-mounted repo keep sane host-side ownership. If
# your host user's UID differs, you'll hit permission errors on write; rebuild
# with --build-arg UID=<yours> in that case.
ARG UID=1000
RUN useradd -m -u ${UID} -s /bin/bash agent
USER agent
ENV HOME=/home/agent
ENV PATH="/home/agent/.local/bin:${PATH}"

# Hermes is the odd one out: not an npm package but a source checkout set up
# by its own installer, so it runs as a build step, as the session user.
# install-hermes.sh pins the release and says what is and is not pinned.
COPY runner/sandbox/install-hermes.sh /opt/blerg-engines/install-hermes.sh
RUN /opt/blerg-engines/install-hermes.sh

COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# Keep trusted helper copies after the engine/toolchain layers: rebuilding a
# runtime adapter should not reinstall the pinned engine dependencies.
COPY --from=shell-builder /out/blerg-shell-supervisor /usr/local/bin/blerg-shell-supervisor
COPY --from=shell-builder /out/blerg-workspace-explorer /usr/local/bin/blerg-workspace-explorer

WORKDIR /workspace

# The daemon starts this detached (`docker run -d ... sleep infinity`), then
# creates a tmux session inside it via `docker exec` — see createDockerSandbox.
CMD ["sleep", "infinity"]
