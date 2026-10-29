#!/usr/bin/env bash
# Exercise the real installer functions with fake Docker; never install/build.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/repo/runner"
awk '/^build_workspace_worker_image_if_missing\(\) \{/ {inside=1} inside {print} inside && /^}$/ {inside=0}' daemon/install.sh > "$tmp/functions.sh"
awk '/^build_sandbox_image_if_missing\(\) \{/ {inside=1} inside {print} inside && /^}$/ {inside=0}' daemon/install.sh >> "$tmp/functions.sh"
cat > "$tmp/bin/docker" <<'STUB'
#!/usr/bin/env bash
if [ "$1 $2" = 'image inspect' ]; then
  if [ "${*: -1}" = 'blerg-runner-sandbox:latest' ]; then
    [ "$3" = '--format' ] && printf '%s\n' "${SANDBOX_COMMIT:-}"
    exit 0
  fi
  printf '%s\n' "${WORKER_PROTOCOL:-}"
  exit 0
fi
printf '%s\n' "$*" >> "$BUILD_LOG"
[ "${BUILD_FAIL:-0}" != 1 ]
STUB
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH" BUILD_LOG="$tmp/build.log"
REPO_ROOT="$tmp/repo"
RUNNER_DIR="$REPO_ROOT/runner"
blerg_step() { :; }
blerg_fail() { printf '%s\n' "$*" >&2; }
blerg_box() { cat >/dev/null; }
source "$tmp/functions.sh"
export WORKER_PROTOCOL=1
build_sandbox_image_if_missing
grep -Fq -- "build -f $RUNNER_DIR/Dockerfile.workspace-worker -t blerg-workspace-worker:latest $REPO_ROOT" "$BUILD_LOG"
: > "$BUILD_LOG"
export WORKER_PROTOCOL=2
build_sandbox_image_if_missing
[ -s "$BUILD_LOG" ]
: > "$BUILD_LOG"
export WORKER_PROTOCOL=3
build_sandbox_image_if_missing
[ ! -s "$BUILD_LOG" ]
export WORKER_PROTOCOL=1 BUILD_FAIL=1
if build_sandbox_image_if_missing; then
  echo 'FAIL: worker build failure allowed installation' >&2
  exit 1
fi
unset BUILD_FAIL
# An existing sandbox image built from another commit is refreshed so its trusted helpers match.
git -C "$REPO_ROOT" init -q && git -C "$REPO_ROOT" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m fixture
head="$(git -C "$REPO_ROOT" rev-parse HEAD)"
export WORKER_PROTOCOL=3 SANDBOX_COMMIT=0000000000000000000000000000000000000000
: > "$BUILD_LOG"
build_sandbox_image_if_missing
grep -Fq -- "build -f $RUNNER_DIR/sandbox/Dockerfile" "$BUILD_LOG"
grep -Fq -- "--label org.blerg.build-commit=$head" "$BUILD_LOG"
# Built from this commit already: nothing to do.
export SANDBOX_COMMIT="$head"
: > "$BUILD_LOG"
build_sandbox_image_if_missing
[ ! -s "$BUILD_LOG" ]
# A failed update keeps the existing image and does not stop the install.
export SANDBOX_COMMIT=0000000000000000000000000000000000000000 BUILD_FAIL=1
if ! build_sandbox_image_if_missing 2>/dev/null; then
  echo 'FAIL: a failed sandbox image update stopped the install' >&2
  exit 1
fi
echo 'workspace worker installer: PASS'
