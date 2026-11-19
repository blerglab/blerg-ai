#!/usr/bin/env bash
# Focused Core / Runner / gateway proofs. Uses only caller-provided test DBs.
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL must identify the isolated test database}"
: "${TEST_DATABASE_URL:?TEST_DATABASE_URL must identify the same test database}"
if [[ "$DATABASE_URL" != "$TEST_DATABASE_URL" ]]; then
  echo 'live_data_process_test_databases_must_match' >&2
  exit 1
fi
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_command="${BLERG_GO_BINARY:-go}"
fixture_dir="$(mktemp -d)"
cleanup() {
  python3 - "$fixture_dir" <<'PY'
import pathlib, shutil, sys
root = pathlib.Path(sys.argv[1])
shutil.rmtree(root)
PY
}
trap cleanup EXIT
cd "$repo_root/core"
GOWORK=off "$go_command" test -c -o "$fixture_dir/core.test" ./cmd/blerg-core
core_digest="$(sha256sum "$fixture_dir/core.test" | cut -d ' ' -f 1)"
cd "$repo_root/runner"
BLERG_LIVE_PROCESS_CORE_BINARY="$fixture_dir/core.test" \
BLERG_LIVE_PROCESS_CORE_VERSION="$core_digest" GOWORK=off \
  "$go_command" test ./internal/server -run '^TestLiveDataCrossProcess' -timeout 30m "$@"
