#!/usr/bin/env bash
# Run any `go` command inside a golang:1.25 container (host Go is too old).
# Files are created as the host user so they stay editable; module/build caches
# persist in ~/.cache; DATABASE_URL points at the Postgres from `make db-up`.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$HOME/.cache/blerg-go" "$HOME/.cache/blerg-gocache"
exec docker run --rm --network host \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp \
  -e GOFLAGS=-buildvcs=false \
  -e GOPATH=/gopath -e GOCACHE=/gocache \
  -e DATABASE_URL="${DATABASE_URL:-postgres://blerg:blerg@localhost:5432/blerg_core}" \
  -v "$ROOT":/src -w /src \
  -v "$HOME/.cache/blerg-go":/gopath \
  -v "$HOME/.cache/blerg-gocache":/gocache \
  golang:1.25@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 go "$@"
