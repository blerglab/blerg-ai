#!/usr/bin/env bash
# Small entry point; release validation, backup, locking and recovery are Go.
set -euo pipefail
umask 077
root=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
build_dir=$(mktemp -d)
trap 'rm -r "$build_dir"' EXIT
if command -v go >/dev/null 2>&1; then
  (cd "$root/contracts" && GOWORK=off go build -o "$build_dir/upgrade" ./cmd/blerg-upgrade)
elif command -v docker >/dev/null 2>&1; then
  case "$(uname -s)" in Linux) host_os=linux ;; Darwin) host_os=darwin ;; *) echo 'Upgrade supports Linux and macOS.' >&2; exit 1 ;; esac
  case "$(uname -m)" in x86_64) host_arch=amd64 ;; arm64|aarch64) host_arch=arm64 ;; *) echo 'Unsupported host architecture.' >&2; exit 1 ;; esac
  docker run --rm --user "$(id -u):$(id -g)" \
    -e HOME=/tmp -e GOCACHE=/tmp/go-cache -e GOWORK=off -e GOFLAGS=-buildvcs=false \
    -e GOOS="$host_os" -e GOARCH="$host_arch" \
    -v "$root":/src:ro -v "$build_dir":/out -w /src/contracts \
    golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 \
    go build -o /out/upgrade ./cmd/blerg-upgrade
else
  echo 'Upgrade needs Go 1.26 or newer or Docker on PATH. Install one, then retry.' >&2
  exit 1
fi
cd "$root"
"$build_dir/upgrade" "$@"
