#!/usr/bin/env bash
# install/desktop/daemon/install.sh — install, remove, or check the blerg-runner
# daemon as a host service. The daemon is NOT containerized: it drives your
# local `claude` CLI and tmux directly, so it has to run on the host, as you.
#
# Supported: systemd --user (Linux), launchd (macOS). Anything else: see
# ../DAEMON.md for the manual (run-it-in-a-terminal) fallback.
#
# Usage:
#   install.sh install     build the daemon binary, install + start the service
#                          (over a running daemon with agent work in progress it
#                          lists that work and asks first; --yes skips the question)
#   install.sh uninstall   stop and remove the service (binary is left in place)
#   install.sh status      show whether it's running and its connection state
#   install.sh preflight   check only (used by blerg-up.sh before doing anything
#                           else); tries to self-heal a missing systemd --user
#                           session via sudo. Exit codes: 0 fine, 3 needs a
#                           privileged fix that sudo escalation couldn't do,
#                           1 any other failure.
#
# Engines: install auto-detects which of claude/codex/hermes/openclaw are
# already set up on this machine (see discover_engines) and reports them —
# purely informational, the launch sheet's Engine picker is what actually
# selects one per session. Only Claude gets an interactive setup prompt here
# (it's the default engine, and the one with no equivalent of "just works if
# the CLI is installed and logged in" ambiguity to worry about): if
# CLAUDE_CODE_OAUTH_TOKEN is set in your shell when you run `install`, it's
# baked into the service and saved to .env for future installs; if it's not
# set anywhere AND no other engine was detected either, the service is
# installed without it and only works once you get `claude` logged in (not
# guaranteed on macOS, where logins can live in a locked keychain) or set up
# any other engine — see DAEMON.md's Engines section.

set -euo pipefail
umask 077

cd "$(dirname "${BASH_SOURCE[0]}")"            # install/desktop/daemon
. ../brand.sh
. ../hostcheck.sh

usage() {
  cat <<EOF
Usage: $0 {install [--yes] [--wsl-startup]|uninstall|status|preflight|wsl-startup {enable|disable|status}}

  install     build the daemon binary, install and start the service. A
              reinstall over a running daemon that has agent work in
              progress lists it and asks first; --yes reinstalls anyway.
              With no terminal to ask, it stops with exit code 5. A daemon
              token the server refuses stops it with exit code 6.
              --wsl-startup opts in to Windows sign-in startup (WSL2).
  wsl-startup {enable|disable|status}
              manage Windows sign-in startup without restarting the daemon.
              enable creates a Windows logon task that keeps this distro
              running while signed in (including on battery), with retries
  uninstall   stop and remove the service (the binary is left in place)
  status      show whether it is running, and its connection to the server
  preflight   check only (used by blerg-up.sh); tries to start a missing
              systemd --user session via sudo. Exit codes: 0 fine, 3 needs a
              privileged fix, 4 session up but its bus is not, 1 other failure
  -h, --help  show this help
EOF
}

CMD="${1:-}"
case "$CMD" in
  install|uninstall|status|preflight|wsl-startup) ;;
  -h|--help|help) usage; exit 0 ;;
  *)
    [ -n "$CMD" ] && echo "install.sh: unknown subcommand: $CMD" >&2
    echo >&2
    usage >&2
    exit 1 ;;
esac
shift
ASSUME_YES=0
WSL_STARTUP=0
STARTUP_ACTION=""
if [ "$CMD" = wsl-startup ]; then
  STARTUP_ACTION="${1:-}"
  case "$STARTUP_ACTION" in
    enable|disable|status) shift ;;
    *) usage >&2; exit 1 ;;
  esac
fi
for arg in "$@"; do
  case "$arg" in
    --yes|-y) [ "$CMD" = install ] && ASSUME_YES=1 && continue ;;
    --wsl-startup) [ "$CMD" = install ] && WSL_STARTUP=1 && continue ;;
  esac
  echo "install.sh: unknown option: $arg" >&2
  echo >&2
  usage >&2
  exit 1
done

if [ "$(id -u)" -eq 0 ]; then
  # This script always runs as you; it escalates only two specific commands
  # internally (via sudo) when it needs to. If the whole script runs as root
  # instead — 'sudo ./install.sh' or 'sudo ./blerg-up.sh' — 'systemctl --user'
  # installs and runs the daemon under ROOT's own session instead of yours:
  # agent sessions would then execute as root, and it wouldn't see your real
  # ~/.claude login or ~/repositories. Never do this.
  blerg_box "hold on" <<'EOF'
Do not run this as root ('sudo ./install.sh' / 'sudo ./blerg-up.sh').
It escalates specific commands internally when it actually needs to.
EOF
  exit 1
fi

DESKTOP_DIR="$(cd .. && pwd)"                  # install/desktop
REPO_ROOT="$(cd ../../.. && pwd)"
RUNNER_DIR="$REPO_ROOT/runner"
BIN_DIR="$REPO_ROOT/bin"
BIN_PATH="$BIN_DIR/blerg-runner-daemon"
# install's exit code when it stopped because agent work is running and there
# was no terminal to ask (blerg-up.sh says so instead of "didn't work").
EXIT_WORK_RUNNING=5
# install's exit code when the server refused the daemon token: before anything
# was replaced (the running daemon left as it was), or by the daemon it started.
EXIT_TOKEN_REFUSED=6
# The build lands here first, and replaces BIN_PATH only once the reinstall goes
# ahead (put_binary_in_place): a "no" leaves the running daemon's binary alone.
NEW_BIN_PATH="$BIN_PATH.new"
ENV_FILE="$DESKTOP_DIR/.env"
SERVICE_NAME="blerg-runner-daemon"
OS="$(uname -s)"

# A .env / unit / plist written before this umask+chmod hygiene was added may
# still be group/world-readable — the common case on an upgrade, since that's
# exactly the machine that already has one. Tighten whatever exists in place
# on every invocation (install, status, uninstall, preflight): idempotent,
# best-effort, never fatal. The rewrite paths in install_systemd/install_launchd
# additionally chmod right after they render a fresh unit/plist; this covers
# everything else (an existing file this run never rewrites, or a command
# that never reaches those functions at all).
tighten_existing_secrets() {
  # Every chmod below ends in `|| true`, not just a trailing `true` after the
  # whole function: under `set -euo pipefail`, errexit fires on the final
  # command of an `&&` list too (`[ -f f ] && false` aborts the script even
  # though it's followed by more lines) — so a genuine chmod failure (a
  # read-only home, NFS, a file owned by someone else) must be swallowed at
  # each individual site, not just at the end, or it takes down the whole
  # install/upgrade instead of being skipped.
  [ -f "$ENV_FILE" ] && chmod 600 "$ENV_FILE" 2>/dev/null || true
  [ -f "$HOME/.config/systemd/user/$SERVICE_NAME.service" ] && chmod 600 "$HOME/.config/systemd/user/$SERVICE_NAME.service" 2>/dev/null || true
  [ -f "$HOME/Library/LaunchAgents/dev.blerg.runner-daemon.plist" ] && chmod 600 "$HOME/Library/LaunchAgents/dev.blerg.runner-daemon.plist" 2>/dev/null || true
  true
}
[ "$CMD" = wsl-startup ] || tighten_existing_secrets

env_val() {
  # Strips one pair of surrounding quotes, if present — systemd already does
  # this for a unit's Environment= line, but launchd's plist keeps a quoted
  # value's quotes verbatim, so BLERG_RUNNER_PROVISION_CLAUDE_MD="true" would
  # silently fail its `== "true"` check on macOS unless we strip it here too,
  # once, for every platform, at the source.
  local v
  v="$(grep "^$1=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2-)" || true
  case "$v" in
    \"*\") v="${v%\"}"; v="${v#\"}" ;;
    \'*\') v="${v%\'}"; v="${v#\'}" ;;
  esac
  printf '%s' "$v"
}

go_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) echo amd64 ;;
  esac
}

go_os() {
  case "$OS" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    *) blerg_fail "$OS: unsupported for service install — see ../DAEMON.md"; exit 1 ;;
  esac
}

# The Go release go.mod asks for (runner/go.mod). A host go that is older would have to fetch it
# first, which needs network access the installer cannot rely on, so it is not used.
GO_MIN_VERSION=1.26.9

# version_ge A B: succeeds when dotted version A >= B (numbers only; a missing part is 0).
version_ge() {
  local IFS=. a b i
  read -ra a <<<"$1"
  read -ra b <<<"$2"
  for i in 0 1 2; do
    if [ "${a[i]:-0}" -gt "${b[i]:-0}" ]; then return 0; fi
    if [ "${a[i]:-0}" -lt "${b[i]:-0}" ]; then return 1; fi
  done
  return 0
}

find_host_go() {
  # Mirrors runner/Makefile's own toolchain selection: a pinned go at ~/go/bin/go
  # if present (hosts often lag), else whatever `go` is on PATH. Either must be at
  # least GO_MIN_VERSION, the release runner/go.mod names.
  local candidate have
  for candidate in "$HOME/go/bin/go" "$(command -v go 2>/dev/null || true)"; do
    [ -n "$candidate" ] && [ -x "$candidate" ] || continue
    have="$("$candidate" version 2>/dev/null | sed -n 's/.* go\([0-9][0-9]*\.[0-9][0-9]*\(\.[0-9][0-9]*\)\{0,1\}\).*/\1/p')"
    [ -n "$have" ] || continue
    version_ge "$have" "$GO_MIN_VERSION" && { echo "$candidate"; return 0; }
  done
  return 1
}

build_binary() {
  # An earlier build inside a container ran as root and left a root-owned
  # bin/ (or binary) behind; building as you then fails with an obscure
  # "permission denied". Say what to run instead.
  if { [ -e "$BIN_DIR" ] && [ ! -w "$BIN_DIR" ]; } || { [ -e "$BIN_PATH" ] && [ ! -w "$BIN_PATH" ]; }; then
    blerg_box "can't write the daemon binary" <<EOF
$BIN_DIR (or the binary in it) isn't writable by you - most likely an
earlier build left it owned by root. Give it back, then re-run:

  sudo chown -R $(id -un):$(id -gn) $BIN_DIR
EOF
    exit 1
  fi
  mkdir -p "$BIN_DIR"
  blerg_step "Building blerg-runner-daemon ($(go_os)/$(go_arch))..."
  # GOWORK=off: the repo-root go.work only lists ./contracts and ./core, which
  # puts any build under the repo tree into workspace mode and rejects
  # ./runner as a non-member module.
  if HOST_GO="$(find_host_go)"; then
    ( cd "$RUNNER_DIR" && GOOS="$(go_os)" GOARCH="$(go_arch)" GOWORK=off "$HOST_GO" build -o "$NEW_BIN_PATH" ./cmd/daemon )
  elif command -v docker >/dev/null 2>&1; then
    # No suitable host go — build inside a matching container instead. Only
    # the BUILD is containerized; the resulting binary runs directly on the
    # host (it needs host tmux/claude/network). It runs as you (--user), like
    # scripts/go.sh, so bin/ stays yours: that needs a writable HOME and
    # module/build caches, which the image doesn't give a non-root uid.
    mkdir -p "$HOME/.cache/blerg-go" "$HOME/.cache/blerg-gocache"
    docker run --rm \
      --user "$(id -u):$(id -g)" \
      -e HOME=/tmp -e GOPATH=/gopath -e GOCACHE=/gocache \
      -e GOOS="$(go_os)" -e GOARCH="$(go_arch)" -e GOWORK=off -e GOFLAGS=-buildvcs=false \
      -v "$HOME/.cache/blerg-go":/gopath \
      -v "$HOME/.cache/blerg-gocache":/gocache \
      -v "$REPO_ROOT":/src -w /src/runner \
      golang:1.26.9@sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6 go build -o "/src/bin/blerg-runner-daemon.new" ./cmd/daemon
  else
    blerg_fail "Need either go >= $GO_MIN_VERSION or docker on PATH to build the daemon."
    exit 1
  fi
}

# The host daemon may be Mach-O. Its trusted Git/quota helper is always a
# Linux executable in Docker Desktop's VM; install the matching protocol image.
build_workspace_worker_image_if_missing() {
  local protocol
  protocol="$(docker image inspect --format '{{index .Config.Labels "org.blerg.workspace-worker-protocol"}}' blerg-workspace-worker:latest 2>/dev/null || true)"
  [ "$protocol" = 3 ] && return 0
  blerg_step "Building the contained workspace worker image..."
  if docker build -f "$RUNNER_DIR/Dockerfile.workspace-worker" -t blerg-workspace-worker:latest "$REPO_ROOT"; then
    return 0
  fi
  blerg_fail "Workspace worker image build failed; the daemon was not installed. Re-run after fixing the build."
  return 1
}

build_sandbox_image_if_missing() {
  # "Local sandbox" is the default runtime for terminal sessions (see
  # DAEMON.md). No docker on PATH is a structural absence, not a failure —
  # blerg-up.sh's own preflight already hard-stops on that earlier, so
  # skip quietly here. But a build that's ATTEMPTED and doesn't finish
  # (network blip, disk space, Ctrl-C) stops the whole install rather than
  # going on to start a daemon whose default runtime silently doesn't work —
  # a half-working install is worse than an install that refuses to start.
  command -v docker >/dev/null 2>&1 || return 0
  if docker image inspect blerg-runner-sandbox:latest >/dev/null 2>&1; then
    build_workspace_worker_image_if_missing || return 1
    return 0
  fi
  blerg_step "Building the local-sandbox session image (one-time, a minute or two)..."
  # Deliberately not silenced: this pulls a base image, apt-get's a dev
  # toolchain, and npm-installs the claude CLI — real progress output here
  # beats a silent multi-minute pause that looks hung.
  if docker build -f "$RUNNER_DIR/sandbox/Dockerfile" --build-arg UID="$(id -u)" -t blerg-runner-sandbox:latest "$RUNNER_DIR/.."; then
    build_workspace_worker_image_if_missing || return 1
    return 0
  fi

  blerg_box "sandbox image build didn't finish" <<EOF
'Local sandbox' is the default runtime for terminal sessions, so the
daemon isn't being installed or started until this builds cleanly --
otherwise the first session launch would fail on a broken default.

Fix whatever stopped it (network, disk space, or you canceled it),
then re-run. Or build it by hand first to see the full error:

  docker build -f $RUNNER_DIR/sandbox/Dockerfile --build-arg UID=$(id -u) -t blerg-runner-sandbox:latest $RUNNER_DIR/..
EOF
  exit 1
}

# put_binary_in_place RUNNING: the last step before the service is (re)started.
# RUNNING=1 when the daemon is running now: the new build is asked which agent
# sessions the restart would cut off, and when there are some the person
# decides (confirm_reinstall_over_work, hostcheck.sh). A "no" ends the install
# here with nothing changed; otherwise the new build replaces the binary, and
# RESTART_REASON says what the restart is for: "update" when the build differs
# from the running one, "reinstall" when it is the same (note_planned_restart).
RESTART_REASON=""
put_binary_in_place() {
  if [ "$1" = 1 ]; then
    RESTART_REASON=update
    cmp -s "$NEW_BIN_PATH" "$BIN_PATH" 2>/dev/null && RESTART_REASON=reinstall
    local rc=0
    confirm_reinstall_over_work "$NEW_BIN_PATH" "$ASSUME_YES" || rc=$?
    if [ "$rc" -ne 0 ]; then
      rm -f "$NEW_BIN_PATH"
      if [ "$rc" -eq 1 ]; then
        blerg_ok "Not reinstalled: the daemon was left running as it was."
        exit 0
      fi
      exit "$EXIT_WORK_RUNNING"
    fi
  fi
  if personal_desktop_install; then
    local enrollment_rc=0
    ( enroll_desktop ) || enrollment_rc=$?
    if [ "$enrollment_rc" -ne 0 ]; then
      rm -f "$NEW_BIN_PATH"
      exit "$enrollment_rc"
    fi
    # Service rendering reads the file; it never embeds this personal bearer.
    DAEMON_TOKEN=""
  fi
  mv -f "$NEW_BIN_PATH" "$BIN_PATH"
}

# note_planned_restart: right before the service restarts a running daemon,
# leave it a marker in its state directory saying the restart is planned and
# why (RESTART_REASON). The next daemon reads and removes it
# (runner/internal/daemon/restartcause.go) and tells the agent sessions the
# restart cut off that nothing they ran caused it; without it they are told
# the daemon stopped unexpectedly. Nothing when no daemon was running. Never
# fatal: a marker that cannot be written only changes those words.
daemon_state_dir() { local configured; configured="${BLERG_RUNNER_DAEMON_STATE_DIR:-$(env_val BLERG_RUNNER_DAEMON_STATE_DIR)}"; printf '%s' "${configured:-$HOME/.blerg-runner-daemon}"; }
note_planned_restart() {
  [ -n "$RESTART_REASON" ] || return 0
  local dir
  dir="$(daemon_state_dir)"
  if mkdir -p "$dir" 2>/dev/null &&
     printf '%s\n' "$RESTART_REASON" 2>/dev/null > "$dir/restart-planned.tmp" &&
     mv -f "$dir/restart-planned.tmp" "$dir/restart-planned" 2>/dev/null; then
    return 0
  fi
  blerg_step "Warning: couldn't note the planned restart in $dir; the sessions it cuts off will be told the daemon stopped unexpectedly."
  return 0
}

# --- Does the server accept the daemon token? -------------------------------
# Before the running daemon is replaced, the token about to be installed is
# shown to the server's GET /api/daemon/whoami (check_daemon_token); after the
# service (re)starts, the new daemon's connection.json says how its link went
# (wait_for_connection). The token travels only in a 0600 header file curl reads
# (-H @file), never on a command line, and is never printed.

# daemon_http_base: the server's HTTP base from SERVER_URL (ws -> http,
# wss -> https, without /ws/daemon), e.g. ws://localhost:8083/ws/daemon ->
# http://localhost:8083.
daemon_http_base() {
  local u="$SERVER_URL"
  case "$u" in
    wss://*) u="https://${u#wss://}" ;;
    ws://*)  u="http://${u#ws://}" ;;
  esac
  u="${u%%\?*}"; u="${u%%#*}"; u="${u%/}"; u="${u%/ws/daemon}"; u="${u%/}"
  printf '%s' "$u"
}

# url_display URL: the URL as messages show it, without user:password@ (a
# credential someone put into BLERG_RUNNER_SERVER_URL is never printed).
url_display() {
  local scheme="" rest="$1"
  case "$rest" in
    *://*) scheme="${rest%%://*}://"; rest="${rest#*://}" ;;
  esac
  local auth="${rest%%/*}" path=""
  [ "$auth" = "$rest" ] || path="/${rest#*/}"
  printf '%s%s%s' "$scheme" "${auth##*@}" "$path"
}

# with_timeout SECONDS CMD...: CMD, cut off after SECONDS where timeout(1)
# exists (a sick user bus can block a systemctl call for many seconds).
with_timeout() {
  local secs="$1"; shift
  if command -v timeout >/dev/null 2>&1; then timeout "$secs" "$@"; else "$@"; fi
}

# url_is_loopback URL: the URL's host is this machine, so a proxy from the
# shell (a corporate http_proxy) must not be asked for it.
url_is_loopback() {
  local h="${1#*://}"
  h="${h%%/*}"; h="${h##*@}"
  case "$h" in
    \[*\]*) h="${h%%]*}"; h="${h#[}" ;;
    *) h="${h%%:*}" ;;
  esac
  case "$h" in
    localhost|localhost.|127.*|::1) return 0 ;;
  esac
  return 1
}

# The temp files of the token check, removed however the script ends.
TOKEN_CHECK_HDR=""
TOKEN_CHECK_BODY=""
RESTART_MARK=""
remove_token_check_files() {
  rm -f "${TOKEN_CHECK_HDR:-}" "${TOKEN_CHECK_BODY:-}" "${TOKEN_CHECK_BODY:+$TOKEN_CHECK_BODY.err}" "${RESTART_MARK:-}"
}
trap remove_token_check_files EXIT

token_refused_box() { # what was refused, where the token comes from, how to fix it
  blerg_box "daemon token refused" <<EOF
$1

Enroll this desktop for its recorded owner again, then rerun ./daemon/install.sh install.
Its person-issued credential is stored in $(daemon_state_dir)/desktop-credential.json.
An ownership change invalidates old credentials. Legacy BLERG_RUNNER_DAEMON_TOKEN
in $ENV_FILE is supported only with the explicit deprecated compatibility switch.
See DAEMON.md, "The server refused the daemon token".
EOF
}

# check_daemon_token: ask the server whether it accepts DAEMON_TOKEN. Only a
# 401 stops the install (exit EXIT_TOKEN_REFUSED, nothing replaced). A server
# older than 0.2.1 has no such route: it answers its web page (200 text/html)
# or a 404, and the check is skipped. A server that cannot be reached may
# simply be down: say so and go on.
check_daemon_token() {
  local base url out code ctype err shown
  base="$(daemon_http_base)"
  url="$base/api/daemon/whoami"
  shown="$(url_display "$base")"
  if ! command -v curl >/dev/null 2>&1; then
    blerg_step "Couldn't check the daemon token with the server first (curl isn't installed); going on."
    return 0
  fi
  # 0600: umask 077 above.
  if ! TOKEN_CHECK_HDR="$(mktemp 2>/dev/null)" || ! TOKEN_CHECK_BODY="$(mktemp 2>/dev/null)"; then
    blerg_step "Couldn't check the daemon token with the server first (no temporary file); going on."
    return 0
  fi
  printf 'Authorization: Bearer %s\n' "$DAEMON_TOKEN" > "$TOKEN_CHECK_HDR"
  err="$TOKEN_CHECK_BODY.err"
  # -q first: no ~/.curlrc (a --fail there would hide the 401, a --trace would write the header
  # to disk). --noproxy for this machine: a corporate proxy in the shell must not get the
  # localhost check.
  if url_is_loopback "$base"; then
    out="$(curl -q -sS --proto =http,https --noproxy '*' --max-time 10 -H "@$TOKEN_CHECK_HDR" -o "$TOKEN_CHECK_BODY" -w '%{http_code} %{content_type}' "$url" 2>"$err")" || out="fail $?"
  else
    out="$(curl -q -sS --proto =http,https --max-time 10 -H "@$TOKEN_CHECK_HDR" -o "$TOKEN_CHECK_BODY" -w '%{http_code} %{content_type}' "$url" 2>"$err")" || out="fail $?"
  fi
  rm -f "$TOKEN_CHECK_HDR"
  code="${out%% *}"
  ctype="${out#* }"
  case "$code" in
    fail)
      blerg_step "Couldn't reach the server at $shown to check the daemon token first ($(head -1 "$err" 2>/dev/null | sed 's/^curl: ([0-9]*) //')); going on - it may simply be down."
      ;;
    200)
      if case "$ctype" in application/json*) true ;; *) false ;; esac &&
         grep -q '"ok"[[:space:]]*:[[:space:]]*true' "$TOKEN_CHECK_BODY" 2>/dev/null; then
        blerg_ok "The server at $shown accepts this daemon token."
      else
        blerg_step "The server at $shown is older than 0.2.1 and can't check a daemon token first; skipped."
      fi
      ;;
    404)
      blerg_step "The server at $shown is older than 0.2.1 and can't check a daemon token first; skipped."
      ;;
    401)
      rm -f "$TOKEN_CHECK_BODY" "$err"
      token_refused_box "The server at $shown refused this daemon token, so nothing
was installed or restarted: a daemon running now was left as it was."
      exit "$EXIT_TOKEN_REFUSED"
      ;;
    *)
      blerg_step "The server at $shown answered $code to the daemon token check; going on without it."
      ;;
  esac
  rm -f "$TOKEN_CHECK_BODY" "$err"
  return 0
}

# conn_field NAME FILE: a string field of connection.json (one line of JSON the
# daemon writes), with \" unescaped; conn_pid FILE: its pid.
conn_field() {
  sed -nE 's/.*"'"$1"'":"(([^"\\]|\\.)*)".*/\1/p' "$2" 2>/dev/null | sed -e 's/\\"/"/g' | head -1
}
conn_pid() { sed -nE 's/.*"pid":([0-9]+).*/\1/p' "$1" 2>/dev/null | head -1; }
connection_file() { printf '%s/connection.json' "$(daemon_state_dir)"; }

# mark_restart: right before the service (re)starts. A connection.json from
# before this point (the daemon that is being replaced) is never taken as the
# new daemon's answer.
mark_restart() {
  RESTART_MARK="$(mktemp 2>/dev/null)" || RESTART_MARK=""   # removed on exit (remove_token_check_files)
}

# conn_file_is_new FILE PID: written by the daemon just started. With its pid
# known (the service manager says), only that pid's file counts; otherwise one
# written since mark_restart (not older than the mark: file times can be as
# coarse as a clock tick, so "the same time" counts).
conn_file_is_new() {
  [ -f "$1" ] || return 1
  if [ -n "$2" ]; then
    [ "$(conn_pid "$1")" = "$2" ]
  else
    [ -n "$RESTART_MARK" ] && [ -f "$RESTART_MARK" ] && ! [ "$RESTART_MARK" -nt "$1" ]
  fi
}

# wait_for_connection PID_FN LOG_HINT: up to BLERG_DAEMON_CONNECT_WAIT seconds
# (default 30) for the new daemon to say how its connection went. connected or
# unconfirmed: fine. refused: loud, and returns 1. No answer in time: a
# warning only, never a failure, and it never waits on input.
wait_for_connection() {
  local file pid state="" max="${BLERG_DAEMON_CONNECT_WAIT:-30}" deadline
  case "$max" in ''|*[!0-9]*) max=30 ;; esac
  # Wall-clock, not a count of tries: on a sick user bus one pid query can block for seconds.
  deadline=$((SECONDS + max))
  file="$(connection_file)"
  blerg_step "Waiting up to ${max}s for the daemon to connect to the server..."
  while :; do
    pid="$("$1" 2>/dev/null || true)"
    case "$pid" in ''|0|*[!0-9]*) pid="" ;; esac
    if conn_file_is_new "$file" "$pid"; then
      state="$(conn_field state "$file")"
      case "$state" in
        connected)
          blerg_ok "The daemon is connected: the server accepted its token."
          return 0 ;;
        unconfirmed)
          blerg_ok "The daemon is connected (the server is older than 0.2.1 and does not confirm it)."
          return 0 ;;
        refused)
          token_refused_box "The daemon was installed and started, but the server
refused its token. It keeps retrying, more and more slowly, until
the token is fixed. Its log: $2"
          return 1 ;;
      esac
    fi
    [ "$SECONDS" -ge "$deadline" ] && break
    sleep 1
  done
  blerg_step "Warning: the daemon has not reported a connection after ${max}s (${state:-no report yet})."
  blerg_step "  It may still be starting. Check: ./daemon/install.sh status, and its log: $2"
  return 0
}

# print_connection_state: connection.json in plain words, for `status`.
print_connection_state() {
  local file pid state
  file="$(connection_file)"
  if [ ! -f "$file" ]; then
    echo "Connection: no record in $file (a daemon older than 0.2.1 keeps none)."
    return 0
  fi
  state="$(conn_field state "$file")"
  pid="$(conn_pid "$file")"
  if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
    echo "Connection: the daemon is not running (process $pid has ended); its last state was:"
    echo "  ${state:-unknown} - $(conn_field server "$file"), since $(conn_field since "$file")"
  else
    echo "Connection: ${state:-unknown} - $(conn_field server "$file"), since $(conn_field since "$file")"
  fi
  echo "  $(conn_field detail "$file")"
  return 0
}

require_env_file() {
  [ -f "$ENV_FILE" ] || {
    blerg_fail "No $ENV_FILE — run install/desktop/blerg-up.sh first."
    exit 1
  }
}

render_placeholders() {
  local service_token="$DAEMON_TOKEN"
  # Person credentials are loaded only from the private state file.
  case "$service_token" in bdt_*) service_token="" ;; esac
  # Reads a template on stdin, writes the substituted result to stdout.
  # '#' delimiter because values here are paths/URLs, never '#'.
  sed -e "s#__BIN_PATH__#$BIN_PATH#g" \
      -e "s#__SERVER_URL__#$SERVER_URL#g" \
      -e "s#__DAEMON_TOKEN__#$service_token#g" \
      -e "s#__REPOS_ROOT__#$REPOS_ROOT#g" \
      -e "s#__LOG_DIR__#$LOG_DIR#g"
}

# Optional daemon env vars beyond the template's fixed ones, as KEY=VALUE
# lines. Each is taken from your shell if set there, else from $ENV_FILE, so
# putting one in install/desktop/.env and re-running `install.sh install`
# (or blerg-up.sh) bakes it into the unit/plist. The daemon itself never
# reads .env — only its service environment.
OPTIONAL_DAEMON_ENV="BLERG_RUNNER_DAEMON_STATE_DIR BLERG_RUNNER_DAEMON_NAME BLERG_RUNNER_PROVISION_CLAUDE_MD BLERG_RUNNER_ALLOW_HOST_CREDENTIAL_CLONE BLERG_RUNNER_CLAUDE_USAGE"
daemon_extra_env() {
  local k v
  if [ -n "${OAUTH_TOKEN:-}" ]; then printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\n' "$OAUTH_TOKEN"; fi
  for k in $OPTIONAL_DAEMON_ENV; do
    v="${!k:-}"
    [ -n "$v" ] || v="$(env_val "$k")"
    if [ -n "$v" ]; then printf '%s=%s\n' "$k" "$v"; fi
  done
}

# replace_marker_line MARKER BLOCK — copy stdin to stdout with the line that is
# exactly MARKER replaced by BLOCK (which may span several lines; an empty
# BLOCK just drops the line). Not sed: an s/// replacement cannot hold a raw
# newline (GNU and BSD sed both reject it as an unterminated `s' command), and
# a token value could contain sed metacharacters. BLOCK travels through awk's
# ENVIRON (POSIX; GNU awk, mawk and macOS's BSD awk all have it), which unlike
# `awk -v` does no backslash-escape processing on the value.
replace_marker_line() {
  BLERG_MARKER_BLOCK="$2" awk -v marker="$1" '
    $0 == marker { if (ENVIRON["BLERG_MARKER_BLOCK"] != "") print ENVIRON["BLERG_MARKER_BLOCK"]; next }
    { print }
  '
}

# sed, not ${s//&/&amp;}: bash 5.2's patsub_replacement makes & in that
# replacement mean "the match", while macOS's bash 3.2 treats it literally.
xml_escape() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

claude_already_logged_in() {
  # Linux only: `claude` stores its login at ~/.claude/.credentials.json. This
  # is an internal format, not a documented API — never treat a failed check
  # as "not logged in", only as "can't tell" (falls through to asking).
  [ "$OS" = "Linux" ] || return 1
  local cred="$HOME/.claude/.credentials.json"
  [ -s "$cred" ] || return 1
  if command -v jq >/dev/null 2>&1; then
    [ -n "$(jq -r '.claudeAiOauth.accessToken // empty' "$cred" 2>/dev/null)" ]
  else
    grep -q '"accessToken"[[:space:]]*:[[:space:]]*"[^"]' "$cred" 2>/dev/null
  fi
}

codex_already_logged_in() {
  command -v codex >/dev/null 2>&1 || return 1
  [ -s "$(codex_login_home)/auth.json" ]
}

hermes_already_configured() {
  command -v hermes >/dev/null 2>&1 || return 1
  # Two independent ways hermes ends up configured, checked separately since
  # either alone is sufficient: a real provider key in .env (OpenRouter,
  # Anthropic, etc.), or a custom/local base_url in config.yaml (e.g. a
  # keyless local Ollama backend — confirmed a real, working setup by
  # testing, not assumed). Best-effort like claude_already_logged_in above —
  # a miss here just means "can't tell", not "definitely not configured".
  local env="$HOME/.hermes/.env" cfg="$HOME/.hermes/config.yaml"
  if [ -s "$env" ] && grep -qE '^[A-Z_]+(_API_KEY|_TOKEN)=.+' "$env" 2>/dev/null; then
    return 0
  fi
  [ -s "$cfg" ] && grep -qE '^\s*base_url:\s*\S' "$cfg" 2>/dev/null
}

openclaw_already_configured() {
  command -v openclaw >/dev/null 2>&1 || return 1
  [ -s "$HOME/.openclaw/state/openclaw.sqlite" ]
}

# discover_engines is informational, not authoritative — the launch sheet's
# Engine picker at session-launch time is the real source of truth. It exists
# so install doesn't nag about Claude specifically when another engine is
# already usable, and so a person (or an LLM configuring this on their
# behalf) sees what's actually available without hunting through DAEMON.md.
discover_engines() {
  ENGINES_FOUND=""
  if claude_already_logged_in; then ENGINES_FOUND="$ENGINES_FOUND claude"; fi
  if codex_already_logged_in; then ENGINES_FOUND="$ENGINES_FOUND codex"; fi
  if hermes_already_configured; then ENGINES_FOUND="$ENGINES_FOUND hermes"; fi
  if openclaw_already_configured; then ENGINES_FOUND="$ENGINES_FOUND openclaw"; fi
  ENGINES_FOUND="$(echo "$ENGINES_FOUND" | sed -e 's/^ *//' -e 's/ *$//')"
}

# has_non_claude_engine: true if discover_engines found anything besides
# claude — used to decide whether the Claude-specific prompts below are worth
# bothering someone with right now (they can always add Claude auth later).
has_non_claude_engine() {
  local e
  for e in $ENGINES_FOUND; do
    [ "$e" = "claude" ] || return 0
  done
  return 1
}

# remember_skip_claude_token_prompt records an explicit "no token" answer in
# .env so a re-run doesn't ask again while no engine is configured.
remember_skip_claude_token_prompt() {
  [ "$(env_val BLERG_SKIP_CLAUDE_TOKEN_PROMPT)" = "1" ] && return 0
  echo "BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1" >> "$ENV_FILE"
  blerg_step "Won't ask again (saved BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1 to .env; delete that line to be asked)."
}

# Enrollment may fence a legacy socket. Wait until the existing active-work
# confirmation has allowed replacement, before changing its persisted owner.
personal_desktop_install() {
  [ "$(env_val BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS)" != "true" ] || [ -n "$(env_val BLERG_CORE_INTERNAL_KEY)" ]
}
enroll_desktop() {
  python3 "$DESKTOP_DIR/daemon/enroll.py" "$ENV_FILE" "$(daemon_state_dir)" "$SERVER_URL" || return 7
  DAEMON_TOKEN="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$(daemon_state_dir)/desktop-credential.json")"
  check_daemon_token
}

resolve_config() {
  require_env_file
  # The daemon dials the runner SERVER, which is not necessarily the desktop
  # stack on this machine: an explicit BLERG_RUNNER_SERVER_URL (exported, or in
  # .env) points it at a hosted runner instead — e.g. the k8s install's
  # wss://runner.<DOMAIN>/ws/daemon (see install/k8s/README.md, "Connecting the
  # workstation daemon"). Otherwise it's the local compose stack's port.
  PORT_RUNNER="$(env_val BLERG_PORT_RUNNER)"
  SERVER_URL="${BLERG_RUNNER_SERVER_URL:-$(env_val BLERG_RUNNER_SERVER_URL)}"
  [ -n "$SERVER_URL" ] || SERVER_URL="ws://localhost:${PORT_RUNNER:-8083}/ws/daemon"
  DAEMON_TOKEN="$(env_val BLERG_RUNNER_DAEMON_TOKEN)"
  if ! personal_desktop_install; then
    blerg_step "Deprecated shared desktop credential: enroll a person-owned desktop before 0.6."
  fi


  REPOS_ROOT="$(env_val BLERG_RUNNER_REPOS_ROOT)"
  if [ -z "$REPOS_ROOT" ]; then
    local default_root="$HOME/repositories"
    if [ -t 0 ]; then
      read -r -p "  Where do your repos live? [$default_root] " REPOS_ROOT
      REPOS_ROOT="${REPOS_ROOT:-$default_root}"
      case "$REPOS_ROOT" in
        "~"|"~/"*) REPOS_ROOT="$HOME${REPOS_ROOT#\~}" ;;  # read doesn't expand ~
      esac
    else
      # Not interactive (piped/scripted) — can't ask, and silently guessing
      # ~/repositories previously meant a wrong repos root went unnoticed
      # until session launch failed. Fail loudly and actionably instead.
      blerg_fail "BLERG_RUNNER_REPOS_ROOT is not set and there is no terminal to ask — export it, or add BLERG_RUNNER_REPOS_ROOT=/path to install/desktop/.env"
      exit 1
    fi
    echo "BLERG_RUNNER_REPOS_ROOT=$REPOS_ROOT" >> "$ENV_FILE"
    blerg_ok "Using $REPOS_ROOT as your repos root (saved to .env; change it there anytime)."
  fi
  mkdir -p "$REPOS_ROOT"

  discover_engines
  if [ -n "$ENGINES_FOUND" ]; then
    blerg_ok "Engines detected and ready to use: $ENGINES_FOUND"
  else
    blerg_step "No engine detected as ready yet (checked claude/codex/hermes/openclaw)."
  fi
  if command -v openclaw >/dev/null 2>&1 && ! openclaw_already_configured; then
    # openclaw is installed but has no working provider yet — most likely
    # someone who wants a self-hosted endpoint (on-prem GPU box) and hasn't
    # registered one, since that's the one setup step no auto-detection can
    # do for them (see configure-openclaw-endpoint.sh's own doc comment).
    blerg_step "openclaw is installed but has no provider configured yet."
    blerg_step "Got a self-hosted inference box (vLLM, an on-prem GPU)? Run:"
    blerg_step "  ./configure-openclaw-endpoint.sh"
  fi

  OAUTH_TOKEN="${CLAUDE_CODE_OAUTH_TOKEN:-$(env_val CLAUDE_CODE_OAUTH_TOKEN)}"
  if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "$(env_val CLAUDE_CODE_OAUTH_TOKEN)" ]; then
    echo "CLAUDE_CODE_OAUTH_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN" >> "$ENV_FILE"
  fi

  CONFIRMED_LOGGED_IN=0
  if [ -z "$OAUTH_TOKEN" ] && claude_already_logged_in; then
    blerg_ok "Detected an existing 'claude' login at \$HOME/.claude — no token needed."
    CONFIRMED_LOGGED_IN=1
  elif [ -z "$OAUTH_TOKEN" ] && has_non_claude_engine; then
    # Claude isn't the only option anymore — don't nag about it specifically
    # when something else (Codex/Hermes/OpenClaw) is already usable. Pick it
    # in the launch sheet's Engine picker; Claude auth can be added later.
    blerg_ok "Claude auth not detected, but this engine is already usable: $ENGINES_FOUND — skipping Claude setup."
    blerg_ok "(To also enable Claude: run 'claude setup-token', then re-run this install.)"
  elif [ -z "$OAUTH_TOKEN" ] && [ "$(env_val BLERG_SKIP_CLAUDE_TOKEN_PROMPT)" = "1" ]; then
    # An earlier run got an explicit "no" — don't ask again on every re-run.
    # Delete the line from .env (or export CLAUDE_CODE_OAUTH_TOKEN) to change it.
    blerg_step "Not asking about a Claude token (BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1 in .env)."
  elif [ -z "$OAUTH_TOKEN" ] && [ -t 0 ]; then
    blerg_box "no engine auth detected" <<EOF
Checked \$CLAUDE_CODE_OAUTH_TOKEN, $ENV_FILE, \$HOME/.claude, \$HOME/.codex,
\$HOME/.hermes, and \$HOME/.openclaw and found no ready engine. Claude needs
one of:
  (a) 'claude' already logged in as you on this machine, or
  (b) a token from 'claude setup-token'
(Codex/Hermes/OpenClaw are alternatives — see DAEMON.md's Engines section
for their own setup; this prompt only covers Claude.)
EOF
    read -r -p "  Already logged in and want to skip this? [y/N] " already_logged_in
    if [ "$already_logged_in" = "y" ] || [ "$already_logged_in" = "Y" ]; then
      CONFIRMED_LOGGED_IN=1
      remember_skip_claude_token_prompt
    else
      echo
      echo "  Run 'claude setup-token' now (in another terminal — it needs its"
      echo "  own browser/prompt flow), then paste the token it prints here:"
      read -r -s -p "  CLAUDE_CODE_OAUTH_TOKEN (blank to skip): " pasted_token
      echo
      if [ -n "$pasted_token" ]; then
        OAUTH_TOKEN="$pasted_token"
        echo "CLAUDE_CODE_OAUTH_TOKEN=$OAUTH_TOKEN" >> "$ENV_FILE"
      else
        remember_skip_claude_token_prompt
      fi
    fi
  fi

  if [ -z "$OAUTH_TOKEN" ] && [ "$CONFIRMED_LOGGED_IN" -ne 1 ] && [ -z "$ENGINES_FOUND" ] \
     && [ -z "${BLERG_ENGINE_PREFLIGHT_DONE:-}" ]; then
    blerg_fail "No engine auth found at all — installing anyway. The service will only"
    blerg_fail "work once at least one engine is set up (see DAEMON.md's Engines"
    blerg_fail "section). For Claude specifically: run 'claude setup-token', then:"
    blerg_fail "  CLAUDE_CODE_OAUTH_TOKEN=<token> $0 install"
  fi
}

# Windows sign-in and distro boot are separate startup layers. Lingering starts
# the user manager at distro boot; the Windows task boots and keeps WSL alive.
wsl_startup() {
  local action="$1" linux_user ps_path encoded
  if [ "$OS" != Linux ] || ! is_wsl || [ -z "${WSL_DISTRO_NAME:-}" ]; then
    blerg_fail "Windows sign-in startup requires WSL2. Run this inside the registered distro as its Linux account (WSL_DISTRO_NAME must be set)."
    return 1
  fi
  linux_user="$(id -un)"
  # These values are later passed through a Windows task command line. Reject
  # shell/PowerShell metacharacters rather than trusting cross-shell quoting.
  if [[ ! "$WSL_DISTRO_NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9._\ -]*$ ]] ||
     [[ ! "$linux_user" =~ ^[a-zA-Z_][a-zA-Z0-9_.-]*\$?$ ]]; then
    blerg_fail "Unsupported distro or Linux account name for Windows startup; use letters, numbers, spaces (distro only), dots, underscores, or hyphens. See DAEMON.md."
    return 1
  fi
  if [ "$action" = enable ]; then
    if wsl_without_systemd || ! systemctl --user is-enabled --quiet "$SERVICE_NAME.service"; then
      blerg_fail "Install the enabled systemd daemon first: ./daemon/install.sh install (see DAEMON.md for WSL2 systemd prerequisites)."
      return 1
    fi
    if [ "$(loginctl show-user "$(id -u)" -p Linger --value 2>/dev/null)" != yes ]; then
      blerg_fail "Distro boot requires lingering: run sudo loginctl enable-linger $linux_user, then retry."
      return 1
    fi
    blerg_step "Opting in: a Windows Task Scheduler logon task will start this distro and keep it running while you are signed in, including on battery. No elevation or Windows password is used."
  fi
  if ! command -v powershell.exe >/dev/null 2>&1 || ! command -v wslpath >/dev/null 2>&1; then
    blerg_fail "Windows interop (powershell.exe and wslpath) is required. Enable WSL interop and Windows PATH, or see DAEMON.md for manual Windows setup."
    return 1
  fi
  ps_path="$(wslpath -w "$DESKTOP_DIR/daemon/wsl-startup.ps1")" || return 1
  # Transport the path as data: PowerShell recognizes Unicode quote delimiters
  # too. Encoding an interpolated expression alone would preserve an injection.
  # The other arguments have already passed the action/name allowlists.
  ps_path="$(printf '%s' "$ps_path" | base64 -w0)" || return 1
  encoded="$(printf "& ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))) -Action '%s' -Distro '%s' -LinuxUser '%s'" \
    "$ps_path" "$action" "$WSL_DISTRO_NAME" "$linux_user" | iconv -f UTF-8 -t UTF-16LE | base64 -w0)" || return 1
  if ! powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand "$encoded"; then
    blerg_fail "Windows Task Scheduler startup $action failed. Check Task Scheduler permissions and Windows PowerShell policy; retry ./daemon/install.sh wsl-startup $action. See DAEMON.md."
    return 1
  fi
}

offer_wsl_startup() {
  is_wsl || return 0
  local answer=""
  if [ "$WSL_STARTUP" = 1 ]; then
    wsl_startup enable
    return
  fi
  blerg_step "WSL does not start this distro after Windows sign-in by itself."
  blerg_step "Optional: ./daemon/install.sh wsl-startup enable creates a Windows logon task that starts and keeps this distro running while signed in, including on battery."
  if [ -t 0 ]; then
    read -r -p "  Enable that Windows sign-in task? [y/N] " answer
    case "$answer" in y|Y) wsl_startup enable ;; esac
  fi
  return 0
}

install_systemd() {
  resolve_config
  if ! personal_desktop_install; then check_daemon_token; fi
  build_binary
  build_sandbox_image_if_missing
  UNIT_DIR="$HOME/.config/systemd/user"
  UNIT_PATH="$UNIT_DIR/$SERVICE_NAME.service"
  mkdir -p "$UNIT_DIR"

  local extra_lines="" kv
  while IFS= read -r kv; do
    [ -n "$kv" ] || continue
    extra_lines="${extra_lines:+$extra_lines
}Environment=$kv"
  done <<EOF
$(daemon_extra_env)
EOF
  LOG_DIR=""  # unused by the systemd template; journald captures stdout/stderr

  local running=0
  systemctl --user is-active --quiet "$SERVICE_NAME.service" 2>/dev/null && running=1
  put_binary_in_place "$running"

  render_placeholders < "$SERVICE_NAME.service.tmpl" | replace_marker_line __EXTRA_ENV__ "$extra_lines" > "$UNIT_PATH"
  chmod 600 "$UNIT_PATH"

  systemctl --user daemon-reload
  systemctl --user enable "$SERVICE_NAME.service"
  note_planned_restart
  mark_restart
  systemctl --user restart "$SERVICE_NAME.service"
  if ! loginctl enable-linger "$(id -un)" 2>/dev/null; then
    # Usually harmless and only a warning: the daemon is running now, it just
    # isn't allowed to outlive your last login session.
    blerg_step "Warning: couldn't enable 'lingering' for $(id -un) (it needs polkit or root)."
    blerg_step "  The daemon is running now, but it will stop when you log out of this machine."
    blerg_step "  To start at distro boot and keep it running after logout: sudo loginctl enable-linger $(id -un)"
  fi

  offer_wsl_startup

  blerg_ok "Installed and started. Logs: journalctl --user -u $SERVICE_NAME -f"
  wait_for_connection systemd_daemon_pid "journalctl --user -u $SERVICE_NAME -f" || exit "$EXIT_TOKEN_REFUSED"
}

systemd_daemon_pid() { with_timeout 3 systemctl --user show -p MainPID --value "$SERVICE_NAME.service"; }

# The daemon links these two into your home on every start
# (runner/internal/daemon/provision.go). Uninstall removes each one only while
# it is still a symlink into this checkout — never a real file, never a link
# someone pointed somewhere else — then drops ~/.claude/skills if that left it
# empty. ~/.claude itself is left alone: it is also where Claude Code keeps
# your login and settings.
remove_provisioned_links() {
  # The daemon derives the checkout from its own executable path, which Linux
  # reports with symlinks resolved, so accept this checkout's physical path too.
  local pair link rel got dir
  for pair in \
    "$HOME/.local/bin/blerg-runner|.claude/skills/session-messaging/blerg-runner" \
    "$HOME/.claude/skills/managing-tickets|skills/managing-tickets"; do
    link="${pair%%|*}"; rel="${pair#*|}"
    [ -L "$link" ] || continue
    got="$(readlink "$link")"
    for dir in "$RUNNER_DIR" "$(cd "$RUNNER_DIR" && pwd -P)"; do
      if [ "$got" = "$dir/$rel" ]; then
        rm -f "$link" && blerg_ok "Removed the $link link."
        break
      fi
    done
  done
  rmdir "$HOME/.claude/skills" 2>/dev/null || true
}

uninstall_systemd() {
  # Remove only this distro/account's opt-in task, before removing its service.
  # If Windows is inaccessible, leave the service installed and explain repair.
  if is_wsl; then wsl_startup disable || return 1; fi
  systemctl --user disable --now "$SERVICE_NAME.service" 2>/dev/null || true
  rm -f "$HOME/.config/systemd/user/$SERVICE_NAME.service"
  systemctl --user daemon-reload
  remove_provisioned_links
  blerg_ok "Removed."
}

# redact_secrets reads status/print output on stdin and blots out secret
# values before they reach the terminal or a log. Two passes, deliberately
# in this order:
#   (a) cosmetic — a key=value/key => value/key: value rewrite that covers
#       the renderings we know about (systemd's `Environment=K=V` inside
#       `status -l`, launchd's `K => V`), so a human skimming the output
#       still sees the key name.
#   (b) the actual guarantee — whatever the real token values are (read
#       fresh from $ENV_FILE / the resolved OAuth token, when available),
#       replace every literal occurrence of them, regardless of what
#       surrounds them. This is what makes redaction format-agnostic: even
#       a renderer we didn't anticipate (e.g. the plist's `<key>...</key>`
#       + `<string>...</string>` pair, which has no `=` at all) can't leak
#       the value through, because the value itself is gone.
redact_secrets() {
  local out
  out="$(sed -E 's/(DAEMON_TOKEN|OAUTH_TOKEN|API_KEY)[[:space:]]*(=>|=|:)[[:space:]]*[^[:space:]]+/\1\2 <redacted>/g')"

  local daemon_token oauth_token
  daemon_token="$(env_val BLERG_RUNNER_DAEMON_TOKEN)"
  oauth_token="${OAUTH_TOKEN:-$(env_val CLAUDE_CODE_OAUTH_TOKEN)}"

  # sed-escape a literal value for use as a BRE pattern (., *, [, ], ^, $)
  # plus the | delimiter used below — tokens are hex/base64/opaque strings
  # and essentially never contain a literal '|'.
  local esc
  esc() { printf '%s' "$1" | sed -e 's/[][\.*^$|]/\\&/g'; }

  if [ -n "$daemon_token" ]; then
    out="$(printf '%s' "$out" | sed -e "s|$(esc "$daemon_token")|<redacted>|g")"
  fi
  if [ -n "$oauth_token" ]; then
    out="$(printf '%s' "$out" | sed -e "s|$(esc "$oauth_token")|<redacted>|g")"
  fi
  printf '%s\n' "$out"
}

status_systemd() {
  systemctl --user status "$SERVICE_NAME.service" --no-pager -l 2>&1 | redact_secrets || true
  print_connection_state
}

install_launchd() {
  resolve_config
  if ! personal_desktop_install; then check_daemon_token; fi
  build_binary
  build_sandbox_image_if_missing
  LABEL="dev.blerg.runner-daemon"
  PLIST_DIR="$HOME/Library/LaunchAgents"
  PLIST_PATH="$PLIST_DIR/$LABEL.plist"
  LOG_DIR="$HOME/Library/Logs/blerg"
  mkdir -p "$PLIST_DIR" "$LOG_DIR"

  local extra_entries="" kv
  while IFS= read -r kv; do
    [ -n "$kv" ] || continue
    extra_entries="${extra_entries:+$extra_entries
}    <key>$(xml_escape "${kv%%=*}")</key>
    <string>$(xml_escape "${kv#*=}")</string>"
  done <<EOF
$(daemon_extra_env)
EOF

  local running=0
  launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1 && running=1
  put_binary_in_place "$running"

  render_placeholders < dev.blerg.runner-daemon.plist.tmpl | replace_marker_line __EXTRA_ENV__ "$extra_entries" > "$PLIST_PATH"
  chmod 600 "$PLIST_PATH"

  note_planned_restart
  mark_restart
  launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
  launchctl enable "gui/$(id -u)/$LABEL"

  blerg_ok "Installed and started. Logs: $LOG_DIR/blerg-runner-daemon.log"
  wait_for_connection launchd_daemon_pid "$LOG_DIR/blerg-runner-daemon.log" || exit "$EXIT_TOKEN_REFUSED"
}

launchd_daemon_pid() { with_timeout 3 launchctl print "gui/$(id -u)/dev.blerg.runner-daemon" | awk '$1 == "pid" && $2 == "=" { print $3; exit }'; }

uninstall_launchd() {
  LABEL="dev.blerg.runner-daemon"
  launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/$LABEL.plist" 2>/dev/null || true
  rm -f "$HOME/Library/LaunchAgents/$LABEL.plist"
  remove_provisioned_links
  blerg_ok "Removed."
}

status_launchd() {
  launchctl print "gui/$(id -u)/dev.blerg.runner-daemon" 2>&1 | redact_secrets | head -20
  print_connection_state
}

ensure_systemd_user_session() {
  # "Failed to connect to bus" has two very different causes, and only one is
  # safe to fix automatically:
  #  - the per-user systemd instance was never started (XDG_RUNTIME_DIR unset,
  #    or user@<uid>.service genuinely inactive) — 'systemctl start' fixes
  #    this for free, nothing else is running under it yet, so it's safe.
  #  - user@<uid>.service is ALREADY active but its D-Bus session socket never
  #    came up (a known WSL2 quirk even with dbus-user-session installed) —
  #    the only fix is 'systemctl restart', which tears down and respawns the
  #    whole user manager, killing every OTHER process it's supervising (other
  #    tmux sessions, background daemons, an in-progress Claude Code session).
  #    Never do that without the human explicitly asking for it.
  # Returns: 0 fixed/fine, 1 tried the safe fix and it didn't help, 2 the
  # session is active-but-broken and needs a human-approved restart.
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  systemctl --user show-environment >/dev/null 2>&1 && return 0

  # WSL with systemd switched off can never have a user session; sudo won't
  # help, so don't prompt for a password just to fail.
  wsl_without_systemd && return 1

  if systemctl is-active --quiet "user@$(id -u).service" 2>/dev/null; then
    return 2
  fi

  if command -v sudo >/dev/null 2>&1; then
    blerg_step "No systemd --user session for this account — starting one (needs sudo)..."
    sudo systemctl start "user@$(id -u).service" >/dev/null 2>&1 || true
    sudo loginctl enable-linger "$USER" >/dev/null 2>&1 || true
  fi

  systemctl --user show-environment >/dev/null 2>&1 && return 0
  # It's possible 'start' above raced with the unit becoming active without
  # fixing the bus — re-check which failure mode we're actually in now.
  systemctl is-active --quiet "user@$(id -u).service" 2>/dev/null && return 2
  return 1
}

# Startup management never starts/restarts a user manager or the daemon.
if [ "$CMD" = wsl-startup ]; then
  wsl_startup "$STARTUP_ACTION"
  exit $?
fi
if [ "$WSL_STARTUP" = 1 ] && { [ "$OS" != Linux ] || ! is_wsl; }; then
  blerg_fail "--wsl-startup is only supported inside WSL2."
  exit 1
fi

case "$OS" in
  Linux)
    if wsl_without_systemd; then
      [ "$CMD" = "preflight" ] && exit 3
      wsl_systemd_box
      exit 1
    fi
    if ! command -v systemctl >/dev/null 2>&1; then
      [ "$CMD" = "preflight" ] && exit 0
      blerg_fail "systemd not available — see ../DAEMON.md for the manual path."
      exit 1
    fi
    SESSION_RC=0
    ensure_systemd_user_session || SESSION_RC=$?
    if [ "$SESSION_RC" -ne 0 ]; then
      if [ "$CMD" = "preflight" ]; then
        [ "$SESSION_RC" -eq 2 ] && exit 4 || exit 3
      fi
      if [ "$SESSION_RC" -eq 2 ]; then
        blerg_box "can't reach your systemd session" <<'EOF'
It's active but its D-Bus socket never came up (a known WSL2 quirk) -
see install/desktop/DAEMON.md#failed-to-connect-to-bus for the fix. It
requires restarting your session, which kills everything else running
under it, so it's not done automatically.
EOF
      else
        blerg_box "can't reach your systemd session" <<'EOF'
No reachable systemd --user session, even after trying to start one
with sudo - see install/desktop/DAEMON.md#failed-to-connect-to-bus
EOF
      fi
      exit 1
    fi
    [ "$CMD" = "preflight" ] && exit 0
    case "$CMD" in
      install) install_systemd ;;
      uninstall) uninstall_systemd ;;
      status) status_systemd ;;
    esac
    ;;
  Darwin)
    [ "$CMD" = "preflight" ] && exit 0
    case "$CMD" in
      install) install_launchd ;;
      uninstall) uninstall_launchd ;;
      status) status_launchd ;;
    esac
    ;;
  *)
    [ "$CMD" = "preflight" ] && exit 0
    blerg_fail "$OS: no automated service install — see ../DAEMON.md for the manual path."
    exit 1
    ;;
esac
