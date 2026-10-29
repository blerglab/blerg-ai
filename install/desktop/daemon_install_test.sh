#!/usr/bin/env bash
# Tests for daemon/install.sh's daemon token check (GET /api/daemon/whoami before the running
# daemon is replaced), its wait for the new daemon's connection.json after the restart, and
# `install.sh status`; and for blerg-up.sh's handling of a refused token.
#
# Runs install.sh from a scratch copy of the tree with a scratch HOME and stub go, systemctl and
# curl: nothing real is built, installed, restarted or called. The token is a fake one.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
fail=0
t() { echo "FAIL: $*"; fail=1; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
export NO_COLOR=1

FAKE_TOKEN="fake-daemon-token-0123456789abcdef"

inst="$tmp/inst"
mkdir -p "$inst/repo/install/desktop/daemon" "$inst/repo/runner" "$inst/repo/bin" "$inst/home" "$inst/repos" "$inst/tmp"
cp brand.sh hostcheck.sh "$inst/repo/install/desktop/"
cp ../../runner/Makefile "$inst/repo/runner/Makefile"
cp daemon/install.sh daemon/enroll.py daemon/*.tmpl daemon/wsl-startup.ps1 "$inst/repo/install/desktop/daemon/"
echo "the binary of the running daemon" > "$inst/repo/bin/blerg-runner-daemon"

# The tools install.sh calls, from the real system; the ones under test are stubs below.
bin="$inst/bin"
mkdir -p "$bin"
for x in git python3 bash sh cat grep sed tr id dirname date awk head cut wc mkdir seq sleep mv chmod rm cmp \
         mktemp find touch kill printf env basename timeout iconv base64 tail; do
  p="$(command -v "$x" 2>/dev/null)" && [ "$p" != "$x" ] && ln -sf "$p" "$bin/$x"
done
cat > "$bin/loginctl" <<'EOF'
#!/bin/sh
case "$1" in
  show-user) echo "${FAKE_LINGER:-yes}" ;;
  enable-linger) echo "linger-user:$2" >> "$FAKE_LOG"; exit "${FAKE_LINGER_RC:-0}" ;;
esac
EOF
printf '#!/bin/sh\necho "C:\\fake\\wsl-startup.ps1"\n' > "$bin/wslpath"
printf '#!/bin/sh\necho windows-startup >> "$FAKE_LOG"\nexit "${FAKE_WINDOWS_RC:-0}"\n' > "$bin/powershell.exe"
printf '#!/bin/sh\n[ "$1" = -m ] && echo x86_64 || echo "${FAKE_OS:-Linux}"\n' > "$bin/uname"
# go: "build -o OUT" writes a daemon that reports no open work.
cat > "$bin/go" <<'EOF'
#!/bin/sh
[ "$1" = version ] && { echo "go version go1.26.9 linux/amd64"; exit 0; }
echo build >> "$FAKE_LOG"
while [ "$1" != -o ]; do shift; done
cat > "$2" <<'BIN'
#!/bin/sh
if [ "$1" = prepare-update ]; then
  echo "prepare-update:$BLERG_RUNNER_DAEMON_STATE_DIR" >> "$FAKE_LOG"
fi
if [ "$1" = cancel-update ]; then
  echo cancel-update >> "$FAKE_LOG"
fi
if [ "$1" = note-update-failed ]; then
  echo update-failed >> "$FAKE_LOG"
fi
if [ "$1" = note-update ]; then
  echo "update-receipt:$2" >> "$FAKE_LOG"
  [ -f "$2" ] || exit 1
fi
exit 0
BIN
chmod +x "$2"
EOF
# systemctl: is-active answers $FAKE_RUNNING; restart is logged and, like the new daemon, writes
# connection.json with $FAKE_CONN_STATE and pid $FAKE_PID (nothing when FAKE_CONN_STATE is empty);
# show -p MainPID answers $FAKE_PID; status prints a unit line holding the token, as systemd does.
cat > "$bin/systemctl" <<'EOF'
#!/bin/sh
case "$*" in
  *is-active*) [ "$FAKE_RUNNING" = 1 ] ;;
  *restart*)
    echo "restart" >> "$FAKE_LOG"
    [ "${FAKE_RESTART_RC:-0}" = 0 ] || exit "$FAKE_RESTART_RC"
    if [ -n "$FAKE_CONN_STATE" ]; then
      mkdir -p "$BLERG_RUNNER_DAEMON_STATE_DIR"
      printf '{"state":"%s","server":"ws://localhost:8083/ws/daemon","since":"2026-01-01T00:00:00Z","detail":"accepted by the server as \\"box\\"","pid":%s}\n' \
        "$FAKE_CONN_STATE" "${FAKE_PID:-4242}" > "$BLERG_RUNNER_DAEMON_STATE_DIR/connection.json"
    fi ;;
  *MainPID*) [ -n "$FAKE_SHOW_DELAY" ] && exec sleep "$FAKE_SHOW_DELAY"; echo "${FAKE_PID:-0}" ;;
  *status*) echo "Environment=BLERG_RUNNER_DAEMON_TOKEN=$FAKE_TOKEN_IN_UNIT"; echo "Active: active (running)" ;;
  *) echo "$*" >> "$FAKE_LOG" ;;
esac
EOF
# curl: logs its arguments, keeps what the header file held, and answers as $FAKE_CURL says.
cat > "$bin/curl" <<'EOF'
#!/bin/sh
echo "ARGS: $*" >> "$FAKE_CURL_LOG"
out=""; fmt=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -w) fmt="$2"; shift ;;
    -H) case "$2" in @*) f="${2#@}"; echo "HEADER-FILE-MODE: $(stat -c %a "$f" 2>/dev/null || stat -f %Lp "$f")" >> "$FAKE_CURL_LOG"; cat "$f" > "$FAKE_CURL_HEADER" ;; esac; shift ;;
    --noproxy|--max-time) echo "OPT: $1 $2" >> "$FAKE_CURL_LOG"; shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
echo "URL: $url" >> "$FAKE_CURL_LOG"
case "$FAKE_CURL" in
  ok)      printf '{"ok":true,"mode":"daemon"}\n' > "$out"; printf '200 application/json' ;;
  refused) printf '{"error":"unauthorized"}\n' > "$out"; printf '401 application/json' ;;
  html)    printf '<!doctype html><title>blerg</title>\n' > "$out"; printf '200 text/html; charset=utf-8' ;;
  notfound) printf '404 page not found\n' > "$out"; printf '404 text/plain; charset=utf-8' ;;
  down)    echo "curl: (7) Failed to connect to localhost port 8083 after 0 ms: Couldn't connect to server" >&2; exit 7 ;;
  *)       printf '503 application/json' ;;
esac
EOF
chmod +x "$bin/wslpath" "$bin/powershell.exe" "$bin/loginctl" "$bin/uname" "$bin/go" "$bin/systemctl" "$bin/curl"
# stat for the header-file check: from the system, under a name curl's stub can find.
p="$(command -v stat)" && ln -sf "$p" "$bin/stat"

state="$inst/state"
run_install() { # [VAR=value...] ; runs install.sh install; output in $inst/out, exit code in $rc
  rm -f "$inst/systemctl.log" "$inst/curl.log" "$inst/curl.header"
  [ "${KEEP_SERVICE:-0}" = 1 ] || rm -rf "$inst/home/.config" "$inst/home/Library"
  if [ "${FAKE_NO_ENV:-0}" = 1 ]; then
    rm -f "$inst/repo/install/desktop/.env"  # a new machine: no desktop stack, no .env
  else
  printf 'BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=true\nBLERG_RUNNER_DAEMON_TOKEN=%s\nBLERG_RUNNER_REPOS_ROOT=%s\nBLERG_RUNNER_DAEMON_NAME=test-box\nBLERG_SKIP_CLAUDE_TOKEN_PROMPT=1\n' "$FAKE_TOKEN" "$inst/repos" \
    > "$inst/repo/install/desktop/.env"
  fi
  if [ "${FAKE_DAEMON_BIN:-0}" = 1 ]; then
    # A running daemon that logs every #235 handoff step it is asked for.
    printf '#!/bin/sh\necho "daemon:$*" >> "$FAKE_LOG"\nexit 0\n' > "$inst/repo/bin/blerg-runner-daemon"
  else
    printf '#!/bin/sh\n[ "$1" = begin-update ] && echo begin-update >> "$FAKE_LOG"\nexit 0\n' > "$inst/repo/bin/blerg-runner-daemon"
    chmod +x "$inst/repo/bin/blerg-runner-daemon"
  fi
  chmod +x "$inst/repo/bin/blerg-runner-daemon"
  local start; start="$(date +%s)"
  ( export PATH="$bin" HOME="$inst/home" TMPDIR="$inst/tmp" BLERG_RUNNER_DAEMON_STATE_DIR="$state" \
      FAKE_LOG="$inst/systemctl.log" FAKE_CURL_LOG="$inst/curl.log" FAKE_CURL_HEADER="$inst/curl.header" \
      FAKE_RUNNING=1 FAKE_CURL=ok FAKE_CONN_STATE=connected FAKE_PID=4242 FAKE_TOKEN_IN_UNIT="$FAKE_TOKEN" \
      BLERG_DAEMON_CONNECT_WAIT=3 BLERG_PROC_VERSION="$inst/pv" XDG_RUNTIME_DIR="$inst/run"
    unset WSL_DISTRO_NAME CLAUDE_CODE_OAUTH_TOKEN BLERG_RUNNER_SERVER_URL http_proxy https_proxy HTTP_PROXY HTTPS_PROXY FAKE_SHOW_DELAY
    for kv in "$@"; do export "$kv"; done
    opts=("${FAKE_CMD:-install}")
    [ "${FAKE_STARTUP_FLAG:-0}" = 1 ] && opts+=(--wsl-startup)
    [ "${FAKE_INSTALL_YES:-0}" = 1 ] && opts+=(--yes)
    [ -n "${FAKE_ENROLL:-}" ] && opts+=(--enroll "$FAKE_ENROLL")
    "$inst/repo/install/desktop/daemon/install.sh" "${opts[@]}" </dev/null >"$inst/out" 2>&1 )
  rc=$?
  elapsed=$(( $(date +%s) - start ))
}
echo "Linux version 6.8.0-generic" > "$inst/pv"
restarted() { grep -q '^restart$' "$inst/systemctl.log" 2>/dev/null; }
out() { cat "$inst/out"; }
no_token_anywhere() { # the token never reaches the output, curl's command line or a leftover file
  grep -q "$FAKE_TOKEN" "$inst/out" && t "$1: the token was printed"
  grep -q "$FAKE_TOKEN" "$inst/curl.log" 2>/dev/null && t "$1: the token was on curl's command line"
  [ -z "$(ls -A "$inst/tmp")" ] || t "$1: temp files left behind: $(ls -A "$inst/tmp")"
}

# Development updates notify the running daemon; only that daemon writes evidence.
run_make_update() {
  rm -f "$inst/systemctl.log"
  mkdir -p "$inst/repo/runner/bin"
  printf '#!/bin/sh\n[ "$1" = begin-update ] && echo begin-update >> "$FAKE_LOG"\nexit 0\n' > "$inst/repo/runner/bin/blerg-runner-daemon"
  chmod +x "$inst/repo/runner/bin/blerg-runner-daemon"
  printf 'BLERG_RUNNER_DAEMON_STATE_DIR=%s\n' "$state" > "$inst/repo/runner/.env.daemon"
  ( export HOME="$inst/home" PATH="$bin:/usr/bin:/bin" FAKE_LOG="$inst/systemctl.log" \
      FAKE_RUNNING=1 FAKE_CONN_STATE=connected FAKE_PID=4242 BLERG_RUNNER_DAEMON_STATE_DIR="$state" \
      FAKE_RESTART_RC="${2:-0}"
    make -C "$inst/repo/runner" GO="$bin/go" "$1" > "$inst/make.out" 2>&1 )
  rc=$?
}
for target in install-daemon restart-daemon; do
  run_make_update "$target"
  [ "$rc" = 0 ] || t "$target failed: $(cat "$inst/make.out")"
  grep -q '^update-receipt:' "$inst/systemctl.log" && t "$target called the removed receipt helper"
  [ "$(grep -E '^(begin-update|build)$' "$inst/systemctl.log" | head -2)" = $'begin-update\nbuild' ] || t "$target did not bind the initiating call before build"
  grep -q "^prepare-update:$state$" "$inst/systemctl.log" || t "$target did not notify the daemon"
  run_make_update "$target" 7
  [ "$rc" != 0 ] || t "$target did not propagate restart failure"
  grep -q '^update-failed$' "$inst/systemctl.log" && t "$target called the removed receipt helper on failure"
  grep -q '^cancel-update$' "$inst/systemctl.log" || t "$target did not cancel failed restart"
done

# --- token accepted, daemon connects: installed, restarted, says connected
rm -rf "$state"
run_install USER=ignored
grep -qx "linger-user:$(id -un)" "$inst/systemctl.log" || t "lingering must use the UID account, not inherited USER"
[ "$rc" = 0 ] || t "accepted token: exit $rc: $(out)"
restarted || t "accepted token: the service was not restarted"
grep -q '^update-receipt:' "$inst/systemctl.log" && t "installer called the removed receipt helper"

grep -q 'accepts this daemon token' "$inst/out" || t "accepted token: not said: $(out)"
grep -q 'The daemon is connected: the server accepted its token' "$inst/out" || t "connected not reported: $(out)"
grep -q "^Authorization: Bearer $FAKE_TOKEN\$" "$inst/curl.header" 2>/dev/null || t "the token did not reach curl through its header file"
grep -q 'HEADER-FILE-MODE: 600' "$inst/curl.log" || t "header file is not 0600: $(cat "$inst/curl.log")"
grep -q '^URL: http://localhost:8083/api/daemon/whoami?name=test-box$' "$inst/curl.log" || t "whoami URL not derived from ws://localhost:8083/ws/daemon: $(cat "$inst/curl.log")"
grep -q "^OPT: --noproxy \*$" "$inst/curl.log" || t "a loopback check does not bypass the shell's proxy"
grep -q '^ARGS: -q ' "$inst/curl.log" || t "curl's first argument is not -q (a ~/.curlrc could change the check): $(cat "$inst/curl.log")"
no_token_anywhere "accepted"

# --- a remote wss server: https, no proxy bypass
rm -rf "$state"
run_install BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon
grep -q '^URL: https://runner.example.com/api/daemon/whoami?name=test-box$' "$inst/curl.log" || t "wss URL: $(cat "$inst/curl.log")"
grep -q -- '--noproxy' "$inst/curl.log" && t "a remote server's check must keep the shell's proxy"

# --- a credential in the server URL is never printed
for mode in ok down refused; do
  rm -rf "$state"
  run_install BLERG_RUNNER_SERVER_URL=wss://someone:url-secret-0000@runner.example.com/ws/daemon FAKE_CURL="$mode"
  grep -q 'url-secret-0000' "$inst/out" && t "$mode: the server URL's password was printed: $(out)"
  grep -q 'someone' "$inst/out" && t "$mode: the server URL's user was printed"
done
grep -q 'The server at https://runner.example.com refused' "$inst/out" || t "refused: the server is not named without its userinfo: $(out)"

# --- a failed service restart propagates its exit status without issuing a receipt.
run_install FAKE_RESTART_RC=7
[ "$rc" = 7 ] || t "failed restart: exit $rc, want 7: $(out)"
grep -q '^update-failed$' "$inst/systemctl.log" && t "failed restart called the removed receipt helper"

# --- refused (401): stops non-zero before anything is replaced; the running service is untouched
rm -rf "$state"
run_install FAKE_CURL=refused
[ "$rc" = 6 ] || t "refused token: exit $rc, want 6: $(out)"
grep -q 'refused this daemon token' "$inst/out" && grep -q 'left as it was' "$inst/out" || t "refused: message: $(out)"
grep -q 'Settings → Account → Your desktops' "$inst/out" && grep -q 'install --enroll CODE' "$inst/out" && grep -q 'import-credential' "$inst/out" || t "refused: missing enrollment guidance: $(out)"
restarted && t "refused: the running service was restarted"
[ -e "$inst/home/.config/systemd/user/blerg-runner-daemon.service" ] && t "refused: the unit was rewritten"
grep -q '^\[ "$1" = begin-update \]' "$inst/repo/bin/blerg-runner-daemon" || t "refused: the running daemon's binary was replaced"
[ -e "$inst/repo/bin/blerg-runner-daemon.new" ] && t "refused: a new build was left behind"
no_token_anywhere "refused"

# --- an older server: its web page (200 text/html) or a 404 is "check skipped", never "accepted"
for mode in html notfound; do
  rm -rf "$state"
  run_install FAKE_CURL="$mode"
  [ "$rc" = 0 ] || t "$mode: exit $rc: $(out)"
  grep -q 'older than 0.2.1' "$inst/out" && grep -q 'skipped' "$inst/out" || t "$mode: not reported as an older server: $(out)"
  grep -q 'accepts this daemon token' "$inst/out" && t "$mode: an older server's answer was taken as an accepted token"
  restarted || t "$mode: the install did not go on"
done

# --- the server cannot be reached: say so and go on (it may be down)
rm -rf "$state"
run_install FAKE_CURL=down
[ "$rc" = 0 ] || t "down: exit $rc: $(out)"
grep -q "Couldn't reach the server" "$inst/out" && grep -q 'Failed to connect' "$inst/out" || t "down: not reported: $(out)"
restarted || t "down: the install did not go on"
no_token_anywhere "down"

# --- the new daemon is refused (an older server, which the check skips): loud, non-zero
rm -rf "$state"
run_install FAKE_CURL=html FAKE_CONN_STATE=refused
[ "$rc" = 6 ] || t "refused after restart: exit $rc, want 6: $(out)"
grep -q 'refused its token' "$inst/out" && grep -q 'journalctl --user -u blerg-runner-daemon' "$inst/out" || t "refused after restart: message: $(out)"

# --- unconfirmed (an older server that accepted the token) is fine
rm -rf "$state"
run_install FAKE_CURL=html FAKE_CONN_STATE=unconfirmed
[ "$rc" = 0 ] && grep -q 'does not confirm it' "$inst/out" || t "unconfirmed: rc=$rc $(out)"

# --- a stale connection.json from the daemon being replaced is not the new daemon's answer
stale_file() { mkdir -p "$state"; printf '{"state":"connected","server":"ws://old","since":"2020-01-01T00:00:00Z","detail":"","pid":1111}\n' > "$state/connection.json"; touch -t 202001010000 "$state/connection.json"; }
stale_file
run_install FAKE_CONN_STATE= BLERG_DAEMON_CONNECT_WAIT=2
[ "$rc" = 0 ] || t "stale file, pid known: exit $rc (a missing answer is a warning only)"
grep -q 'The daemon is connected' "$inst/out" && t "stale file (another pid) taken as the new daemon's answer: $(out)"
grep -q 'has not reported a connection after 2s' "$inst/out" || t "stale file: no timeout warning: $(out)"
stale_file
run_install FAKE_CONN_STATE= FAKE_PID= BLERG_DAEMON_CONNECT_WAIT=2
grep -q 'The daemon is connected' "$inst/out" && t "stale file (older than the restart, pid unknown) taken as the answer: $(out)"
# ...while a file written after the restart counts when the service manager gives no pid.
rm -rf "$state"
run_install FAKE_PID=
[ "$rc" = 0 ] && grep -q 'The daemon is connected' "$inst/out" || t "fresh file, pid unknown: not accepted: $(out)"

# --- the wait is bounded and never needs a terminal
rm -rf "$state"
run_install FAKE_CONN_STATE= BLERG_DAEMON_CONNECT_WAIT=2
[ "$elapsed" -le 10 ] || t "the wait took ${elapsed}s for a 2s bound"
grep -q 'BLERG_DAEMON_CONNECT_WAIT:-30' daemon/install.sh || t "the default wait is not 30s"
awk '/^wait_for_connection\(\)/,/^}/' daemon/install.sh | grep -qw read && t "wait_for_connection reads input"
# A pid query that hangs (a sick user bus) does not stretch the bound: wall-clock, and each query cut off.
rm -rf "$state"
run_install FAKE_CONN_STATE= FAKE_SHOW_DELAY=20 BLERG_DAEMON_CONNECT_WAIT=2
[ "$rc" = 0 ] || t "hung pid query: exit $rc"
[ "$elapsed" -le 8 ] || t "a hung pid query stretched a 2s wait to ${elapsed}s"

# --- WSL setup discloses the opt-in, and noninteractive setup does not opt in.
mkdir -p "$inst/sysd"
rm -rf "$state"
run_install WSL_DISTRO_NAME='Example Distro' BLERG_SYSTEMD_RUN_DIR="$inst/sysd" FAKE_INSTALL_YES=1
[ "$rc" = 0 ] || t "WSL default install: rc=$rc $(out)"
grep -q 'Optional: ./daemon/install.sh wsl-startup enable' "$inst/out" || t "WSL setup did not disclose Windows opt-in"
grep -q 'windows-startup' "$inst/systemctl.log" && t "noninteractive WSL setup opted in without permission"
run_install WSL_DISTRO_NAME='Example Distro' BLERG_SYSTEMD_RUN_DIR="$inst/sysd" FAKE_STARTUP_FLAG=1
[ "$rc" = 0 ] || t "explicit WSL opt-in: rc=$rc $(out)"
[ "$(grep -c '^windows-startup$' "$inst/systemctl.log")" = 1 ] || t "WSL opt-in did not configure exactly one Windows task"
grep -q 'including on battery' "$inst/out" || t "WSL opt-in lacks battery disclosure"
run_install WSL_DISTRO_NAME='Example Distro' BLERG_SYSTEMD_RUN_DIR="$inst/sysd" FAKE_STARTUP_FLAG=1 FAKE_LINGER=no FAKE_LINGER_RC=1
[ "$rc" != 0 ] || t "WSL opt-in without lingering succeeded"
grep -q 'sudo loginctl enable-linger' "$inst/out" || t "WSL missing linger has no repair command"
grep -q 'windows-startup' "$inst/systemctl.log" && t "WSL configured Windows startup without linger"
run_install WSL_DISTRO_NAME='Example Distro' BLERG_SYSTEMD_RUN_DIR="$inst/sysd" FAKE_STARTUP_FLAG=1 FAKE_WINDOWS_RC=1
[ "$rc" != 0 ] && grep -q 'Task Scheduler startup enable failed' "$inst/out" || t "Windows setup failure was hidden"

# --- status prints the connection state (and still redacts the unit's token)
mkdir -p "$state"
printf '{"state":"refused","server":"wss://runner.example.com/ws/daemon","since":"2026-01-01T00:00:00Z","detail":"the server refused the daemon token (close 4401)","pid":%s}\n' "$$" > "$state/connection.json"
status_out="$( export PATH="$bin" HOME="$inst/home" BLERG_RUNNER_DAEMON_STATE_DIR="$state" FAKE_LOG="$inst/systemctl.log" \
    FAKE_TOKEN_IN_UNIT="$FAKE_TOKEN" BLERG_PROC_VERSION="$inst/pv" XDG_RUNTIME_DIR="$inst/run"
  unset WSL_DISTRO_NAME
  "$inst/repo/install/desktop/daemon/install.sh" status </dev/null 2>&1 )"
grep -q 'Connection: refused - wss://runner.example.com/ws/daemon, since 2026-01-01T00:00:00Z' <<<"$status_out" || t "status: no connection state: $status_out"
grep -q 'close 4401' <<<"$status_out" || t "status: no detail: $status_out"
grep -q "$FAKE_TOKEN" <<<"$status_out" && t "status printed the token"
printf '{"state":"connected","server":"ws://localhost:8083/ws/daemon","since":"2026-01-01T00:00:00Z","detail":"accepted by the server as \\"box\\"","pid":999999999}\n' > "$state/connection.json"
status_out="$( export PATH="$bin" HOME="$inst/home" BLERG_RUNNER_DAEMON_STATE_DIR="$state" FAKE_LOG="$inst/systemctl.log" \
    FAKE_TOKEN_IN_UNIT="$FAKE_TOKEN" BLERG_PROC_VERSION="$inst/pv" XDG_RUNTIME_DIR="$inst/run"
  "$inst/repo/install/desktop/daemon/install.sh" status </dev/null 2>&1 )"
grep -q 'accepted by the server as "box"' <<<"$status_out" || t "status: escaped detail not unescaped: $status_out"
grep -q 'the daemon is not running (process 999999999 has ended); its last state was' <<<"$status_out" || t "status: a file from a dead process not marked: $status_out"
rm -f "$state/connection.json"
status_out="$( export PATH="$bin" HOME="$inst/home" BLERG_RUNNER_DAEMON_STATE_DIR="$state" FAKE_LOG="$inst/systemctl.log" \
    FAKE_TOKEN_IN_UNIT="$FAKE_TOKEN" BLERG_PROC_VERSION="$inst/pv" XDG_RUNTIME_DIR="$inst/run"
  "$inst/repo/install/desktop/daemon/install.sh" status </dev/null 2>&1 )"
grep -q 'Connection: no record' <<<"$status_out" || t "status without a file: $status_out"

# --- blerg-up.sh: knows install.sh's refused exit code, and never prints the daemon token
grep -q '^EXIT_TOKEN_REFUSED=6$' daemon/install.sh && grep -q '^    6) # install.sh.s EXIT_TOKEN_REFUSED' blerg-up.sh \
  || t "blerg-up.sh and install.sh disagree on the token-refused exit code"
grep -q 'BLERG_RUNNER_DAEMON_TOKEN=\$DAEMON_TOKEN' blerg-up.sh && t "blerg-up.sh's manual box prints the daemon token"
grep -q '^DAEMON_TOKEN=' blerg-up.sh && t "blerg-up.sh still reads the daemon token into a variable"
# The manual box's step and footer, rendered from blerg-up.sh in a directory (with a space in its
# path) whose .env holds the fake token: the token is not in what it prints, and the export line it
# prints reads it from that .env.
step="$(sed -n '/^  if \[ "\$DAEMON_REFUSED" -eq 1 \]; then/,/^  fi$/p' blerg-up.sh)"
[ -n "$step" ] || t "blerg-up.sh: the manual box's daemon step was not found"
up="$tmp/up dir"
mkdir -p "$up"; printf 'BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=true\nBLERG_RUNNER_DAEMON_TOKEN=%s\n' "$FAKE_TOKEN" > "$up/.env"
render() { ( cd "$up" && PORT_RUNNER=8083 REPOS_ROOT_LINE="" RUNNER_DIR=/r DAEMON_REFUSED="$1" bash -c "$step"'; printf "%s\n%s\n" "$DAEMON_STEP" "$DAEMON_FOOTER"' ); }
box="$(render 0)"
grep -q "$FAKE_TOKEN" <<<"$box" && t "blerg-up.sh's manual box prints the daemon token"
grep -q 'daemon/enroll.py' <<<"$box" || t "blerg-up.sh: manual start lacks personal enrollment"
grep -q 'export BLERG_RUNNER_DAEMON_TOKEN=' <<<"$box" && t "blerg-up.sh: manual start copies the shared server secret"
grep -q "that's success" <<<"$box" || t "blerg-up.sh: the normal manual box lost its footer"
box="$(render 1)"
grep -q 'refused' <<<"$box" && grep -q 'daemon/install.sh install' <<<"$box" || t "blerg-up.sh: no fix step after a refused token: $box"
grep -q "success" <<<"$box" && t "blerg-up.sh: after a refused token the box still says success: $box"
grep -qi 'start that daemon' <<<"$box" && t "blerg-up.sh: after a refused token the box still says to start the daemon"
grep -q 'not connected' <<<"$box" || t "blerg-up.sh: the refused footer does not say the daemon is not connected: $box"
# ...and the run ends with install.sh's code 6, not 0.
tail_check="$(sed -n '/^if \[ "\$DAEMON_REFUSED" -eq 1 \]; then$/,/^fi$/p' blerg-up.sh)"
[ -n "$tail_check" ] || t "blerg-up.sh: no final check of a refused token"
( . ./brand.sh; DAEMON_REFUSED=1; eval "$tail_check"; exit 0 ) >/dev/null 2>&1; rc=$?
[ "$rc" = 6 ] || t "blerg-up.sh: a refused daemon token ends with exit $rc, want 6"
( . ./brand.sh; DAEMON_REFUSED=0; eval "$tail_check"; exit 0 ) >/dev/null 2>&1; rc=$?
[ "$rc" = 0 ] || t "blerg-up.sh: without a refusal the final check exits $rc"

# --- The service's PATH: captured from the login shell, merged with the defaults ---------------
# A fake login shell whose profile prints a banner and sets PATH to an nvm, a custom npm prefix and
# a Homebrew-like layout (plus entries that must never reach a unit: a space, a %, a relative one).
# It records its arguments and environment, so the test sees the flags and that no token reached it.
# The installer starts it with a scrubbed environment (HOME among the few kept), so it reads its
# script from files in HOME, the way a real profile would.
fake_shell="$inst/fakeshell"
cat > "$fake_shell" <<'SH'
#!/bin/sh
echo "Welcome to your shell"
echo "args:$1" >> "$HOME/.fake_shell.log"
[ -f "$HOME/.fake_shell_hang" ] && { echo $$ > "$HOME/.fake_shell.pid"; exec sleep 30; }
# A profile that waits on a separate foreground command (not an exec replacement).
[ -f "$HOME/.fake_shell_child" ] && { "$HOME/.fake_child.sh"; :; }
env >> "$HOME/.fake_shell.log"
[ -f "$HOME/.fake_shell_fail" ] && exit 1
PATH="$(cat "$HOME/.fake_login_path")"; export PATH
shift
exec /bin/sh -c "$1"
SH
chmod +x "$fake_shell"
h="$inst/home"
LOGIN_PATH="$h/.nvm/versions/node/v22.11.0/bin:$h/.npm-global/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/has space/bin:/odd%n/bin:relative/bin:/bin"
unit="$inst/home/.config/systemd/user/blerg-runner-daemon.service"
shell_env=(SHELL="$fake_shell")
login_path() { printf '%s' "$1" > "$h/.fake_login_path"; }
login_path "$LOGIN_PATH"

rm -rf "$state" "$h/.fake_shell.log"
run_install "${shell_env[@]}"
[ "$rc" = 0 ] || t "PATH capture: exit $rc: $(out)"
want="Environment=PATH=$h/.nvm/versions/node/v22.11.0/bin:$h/.npm-global/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:$h/.local/bin:/usr/local/bin:/usr/sbin:/sbin"
grep -qxF "$want" "$unit" || t "the unit's PATH is not the login shell's merged with the defaults: $(grep PATH "$unit")"
[ "$(grep -c '^Environment=PATH=' "$unit")" = 1 ] || t "the unit has more than one PATH line"
grep -q 'odd\|has space\|relative' "$unit" && t "an unsafe PATH entry reached the unit"
grep -q 'Service PATH from your login shell (fakeshell)' "$inst/out" || t "the PATH source is not said: $(out)"
grep -q '^args:-ilc$' "$h/.fake_shell.log" || t "an interactive login shell was not asked first: $(cat "$h/.fake_shell.log")"
grep -q "$FAKE_TOKEN" "$h/.fake_shell.log" && t "the daemon token reached the login shell"
grep -q '^BLERG_' "$h/.fake_shell.log" && t "installer variables reached the login shell"
no_token_anywhere "PATH capture"

# A shell that fails: the defaults, said so, and the install goes on.
rm -rf "$state" "$h/.fake_shell.log"
touch "$h/.fake_shell_fail"
run_install "${shell_env[@]}"
rm -f "$h/.fake_shell_fail"
[ "$rc" = 0 ] || t "failing shell: exit $rc: $(out)"
grep -qxF "Environment=PATH=$h/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" "$unit" || t "failing shell: not the defaults: $(grep PATH "$unit")"
grep -q "login shell's PATH could not be read" "$inst/out" || t "failing shell: not said: $(out)"
grep -q '^args:-lc$' "$h/.fake_shell.log" || t "failing shell: no plain login shell retry"

# --- refresh-path (Linux): only the PATH line changes, then a planned restart --------------
run_install "${shell_env[@]}"
before="$(grep -v '^Environment=PATH=' "$unit")"
rm -rf "$state"
login_path "$h/.volta/bin:/usr/bin:/bin"
KEEP_SERVICE=1 run_install FAKE_CMD=refresh-path "${shell_env[@]}"
[ "$rc" = 0 ] || t "refresh-path: exit $rc: $(out)"
grep -qxF "Environment=PATH=$h/.volta/bin:/usr/bin:/bin:$h/.local/bin:/usr/local/bin:/usr/sbin:/sbin" "$unit" || t "refresh-path: PATH not replaced: $(grep PATH "$unit")"
[ "$(grep -c '^Environment=PATH=' "$unit")" = 1 ] || t "refresh-path: duplicate PATH lines"
[ "$(grep -v '^Environment=PATH=' "$unit")" = "$before" ] || t "refresh-path changed more than the PATH"
[ "$(stat -c %a "$unit")" = 600 ] || t "refresh-path: the unit is not 0600"
restarted || t "refresh-path: not restarted"
grep -q 'daemon-reload' "$inst/systemctl.log" || t "refresh-path: no daemon-reload"
[ "$(cat "$state/restart-planned" 2>/dev/null)" = reinstall ] || t "refresh-path: the restart was not noted as planned"
grep -q '^build$' "$inst/systemctl.log" && t "refresh-path rebuilt the daemon"
no_token_anywhere "refresh-path"
# One precedence: the installer's capture rewrites the PATH the app's Re-detect saved.
grep -qxF "{\"path\":[\"$h/.volta/bin\",\"/usr/bin\",\"/bin\",\"$h/.local/bin\",\"/usr/local/bin\",\"/usr/sbin\",\"/sbin\"]}" "$state/login-path.json" \
  || t "refresh-path did not rewrite the saved PATH: $(cat "$state/login-path.json" 2>/dev/null)"
[ "$(stat -c %a "$state/login-path.json")" = 600 ] || t "the saved PATH is not 0600"
printf '{"path":["/opt/old-node/bin","/usr/bin"]}\n' > "$state/login-path.json"
touch "$h/.fake_shell_fail"
KEEP_SERVICE=1 run_install FAKE_CMD=refresh-path "${shell_env[@]}"
rm -f "$h/.fake_shell_fail"
grep -q old-node "$state/login-path.json" || t "an unreadable login shell must leave the saved PATH alone"
printf '{"path":["/opt/old-node/bin","/usr/bin"]}\n' > "$state/login-path.json"
KEEP_SERVICE=1 run_install FAKE_CMD=refresh-path "${shell_env[@]}"
grep -q old-node "$state/login-path.json" && t "the app's older saved PATH survived refresh-path"
# refresh-path takes #235's planned-update handoff: begin before anything changes,
# prepare right before the restart, cancel when the restart fails.
handoff() { grep -E '^(daemon:(begin|prepare|cancel)-update|restart)$' "$inst/systemctl.log" | tr '\n' ' '; }
KEEP_SERVICE=1 FAKE_DAEMON_BIN=1 run_install FAKE_CMD=refresh-path "${shell_env[@]}"
[ "$rc" = 0 ] && [ "$(handoff)" = "daemon:begin-update daemon:prepare-update restart " ] || t "refresh-path handoff: rc $rc, steps: $(handoff)"
KEEP_SERVICE=1 FAKE_DAEMON_BIN=1 run_install FAKE_CMD=refresh-path FAKE_RESTART_RC=7 "${shell_env[@]}"
[ "$rc" = 7 ] && [ "$(handoff)" = "daemon:begin-update daemon:prepare-update restart daemon:cancel-update " ] || t "refresh-path failed restart: rc $rc, steps: $(handoff)"
rm -f "$unit"
login_path "$LOGIN_PATH"
KEEP_SERVICE=1 run_install FAKE_CMD=refresh-path "${shell_env[@]}"
[ "$rc" = 1 ] && grep -q 'not installed' "$inst/out" || t "refresh-path without a service: exit $rc: $(out)"

# --- macOS (launchd): the same PATH capture in the plist, and refresh-path rewrites it there ---
cat > "$bin/launchctl" <<'EOF'
#!/bin/sh
echo "launchctl $*" >> "$FAKE_LOG"
case "$1" in
  print) [ "$FAKE_RUNNING" = 1 ] || exit 1; echo "	pid = ${FAKE_PID:-4242}" ;;
  bootstrap)
    [ "${FAKE_BOOTSTRAP_RC:-0}" = 0 ] || exit "$FAKE_BOOTSTRAP_RC"
    mkdir -p "$BLERG_RUNNER_DAEMON_STATE_DIR"
    printf '{"state":"%s","server":"ws://localhost:8083/ws/daemon","since":"2026-01-01T00:00:00Z","detail":"ok","pid":%s}\n' \
      "$FAKE_CONN_STATE" "${FAKE_PID:-4242}" > "$BLERG_RUNNER_DAEMON_STATE_DIR/connection.json" ;;
esac
exit 0
EOF
chmod +x "$bin/launchctl"
plist="$inst/home/Library/LaunchAgents/dev.blerg.runner-daemon.plist"
rm -rf "$state"
run_install FAKE_OS=Darwin "${shell_env[@]}"
[ "$rc" = 0 ] || t "macOS install: exit $rc: $(out)"
mac_path="$h/.nvm/versions/node/v22.11.0/bin:$h/.npm-global/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:$h/.local/bin:/usr/local/bin:/usr/sbin:/sbin"
grep -A1 '<key>PATH</key>' "$plist" | grep -qxF "    <string>$mac_path</string>" || t "macOS: the plist's PATH: $(grep -A1 '<key>PATH</key>' "$plist")"
python3 -c 'import plistlib,sys; d=plistlib.load(open(sys.argv[1],"rb")); assert d["EnvironmentVariables"]["PATH"]==sys.argv[2]' "$plist" "$mac_path" || t "macOS: the plist does not parse with that PATH"
grep -q 'launchctl bootstrap' "$inst/systemctl.log" || t "macOS: not bootstrapped"
rm -rf "$state"
login_path "/opt/homebrew/bin:/usr/bin:/bin"
KEEP_SERVICE=1 run_install FAKE_CMD=refresh-path FAKE_OS=Darwin "${shell_env[@]}"
[ "$rc" = 0 ] || t "macOS refresh-path: exit $rc: $(out)"
python3 - "$plist" "/opt/homebrew/bin:/usr/bin:/bin:$h/.local/bin:/usr/local/bin:/usr/sbin:/sbin" "$FAKE_TOKEN" <<'PY' || t "macOS refresh-path: plist after: $(cat "$plist")"
import plistlib, sys
d = plistlib.load(open(sys.argv[1], "rb"))
env = d["EnvironmentVariables"]
want = sys.argv[2]
assert env["PATH"] == want, env["PATH"]
assert env["BLERG_RUNNER_DAEMON_TOKEN"] == sys.argv[3]
assert open(sys.argv[1]).read().count("<key>PATH</key>") == 1
PY
[ "$(cat "$state/restart-planned" 2>/dev/null)" = reinstall ] || t "macOS refresh-path: the restart was not noted as planned"
grep -q 'launchctl bootout' "$inst/systemctl.log" && grep -q 'launchctl bootstrap' "$inst/systemctl.log" || t "macOS refresh-path: not restarted"
no_token_anywhere "macOS refresh-path"
mac_handoff() { grep -E '^(daemon:(begin|prepare|cancel)-update|launchctl bootstrap.*)$' "$inst/systemctl.log" | sed 's/^launchctl bootstrap.*/bootstrap/' | tr '\n' ' '; }
KEEP_SERVICE=1 FAKE_DAEMON_BIN=1 run_install FAKE_CMD=refresh-path FAKE_OS=Darwin "${shell_env[@]}"
[ "$rc" = 0 ] && [ "$(mac_handoff)" = "daemon:begin-update daemon:prepare-update bootstrap " ] || t "macOS refresh-path handoff: rc $rc, steps: $(mac_handoff)"
KEEP_SERVICE=1 FAKE_DAEMON_BIN=1 run_install FAKE_CMD=refresh-path FAKE_OS=Darwin FAKE_BOOTSTRAP_RC=5 "${shell_env[@]}"
[ "$rc" = 5 ] && [ "$(mac_handoff)" = "daemon:begin-update daemon:prepare-update bootstrap daemon:cancel-update " ] || t "macOS refresh-path failed bootstrap: rc $rc, steps: $(mac_handoff)"

# A profile whose foreground command outlives the timeout: the whole process group is
# stopped, on the Linux path and on the macOS path without timeout(1), and nothing it
# started is left running or finishes later.
cat > "$h/.fake_child.sh" <<'CHILD'
#!/bin/sh
echo $$ >> "$HOME/.fake_child.pids"
sleep 30
echo finished >> "$HOME/.fake_child.marker"
CHILD
chmod +x "$h/.fake_child.sh"
check_child_gone() { # LABEL
  local pids p
  pids="$(cat "$h/.fake_child.pids" 2>/dev/null)"
  [ -n "$pids" ] || t "$1: the profile's foreground command never ran"
  for p in $pids; do
    kill -0 "$p" 2>/dev/null && { t "$1: the profile's foreground command $p was left running"; kill "$p"; }
  done
  [ -f "$h/.fake_child.marker" ] && t "$1: the profile's command finished after the timeout"
  rm -f "$h/.fake_child.pids" "$h/.fake_child.marker"
}
touch "$h/.fake_shell_child"
rm -rf "$state"
run_install BLERG_LOGIN_SHELL_TIMEOUT=1 "${shell_env[@]}"
[ "$rc" = 0 ] && [ "$elapsed" -lt 15 ] || t "Linux profile child: exit $rc after ${elapsed}s: $(out)"
check_child_gone "Linux"
mv "$bin/timeout" "$inst/timeout.saved"
rm -rf "$state"
run_install FAKE_OS=Darwin BLERG_LOGIN_SHELL_TIMEOUT=1 "${shell_env[@]}"
mv "$inst/timeout.saved" "$bin/timeout"
[ "$rc" = 0 ] && [ "$elapsed" -lt 15 ] || t "macOS profile child: exit $rc after ${elapsed}s: $(out)"
check_child_gone "macOS"
rm -f "$h/.fake_shell_child"

# Stock macOS has no timeout(1): a login shell that never returns is still cut off.
mv "$bin/timeout" "$inst/timeout.saved"
touch "$h/.fake_shell_hang"
rm -rf "$state"
run_install FAKE_OS=Darwin BLERG_LOGIN_SHELL_TIMEOUT=1 "${shell_env[@]}"
rm -f "$h/.fake_shell_hang"
mv "$inst/timeout.saved" "$bin/timeout"
[ "$rc" = 0 ] || t "hanging shell without timeout(1): exit $rc: $(out)"
[ "$elapsed" -lt 15 ] || t "hanging shell without timeout(1): took ${elapsed}s"
grep -q "login shell's PATH could not be read" "$inst/out" || t "hanging shell: the fallback is not said"
python3 -c 'import plistlib,sys; d=plistlib.load(open(sys.argv[1],"rb")); assert d["EnvironmentVariables"]["PATH"].startswith(sys.argv[2])' "$plist" "$h/.local/bin:/opt/homebrew/bin" || t "hanging shell: not the macOS defaults"
hung="$(cat "$h/.fake_shell.pid" 2>/dev/null)"
[ -n "$hung" ] || t "the hanging login shell never ran"
[ -n "$hung" ] && kill -0 "$hung" 2>/dev/null && { t "the hung login shell was left running"; kill "$hung"; }
rm -f "$h/.fake_shell.pid"
rm -rf "$inst/home/Library" "$h/.fake_login_path" "$h/.fake_shell.log"

# --- Add a desktop (#193): a NEW machine with no .env enrolls with a one-time code ------------
# A fake runner answers POST /api/desktops/enroll: each code works once.
cat > "$tmp/fake_runner.py" <<'EOF'
import http.server, json, sys
codes = {'bec_' + c * 40 for c in '12'}
log = open(sys.argv[2], 'a')
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        ok = self.path == '/api/desktops/enroll' and body.get('code') in codes
        log.write('enroll name=%s ok=%s key=%s\n' % (body.get('name'), ok, self.headers.get('X-Internal-Key'))); log.flush()
        if not ok:
            self.send_response(403); self.end_headers(); return
        codes.discard(body['code'])
        self.send_response(200); self.end_headers()
        self.wfile.write(json.dumps({'binding': {'desktop_id': '00000000-0000-4000-8000-000000000193'}, 'token': 'bdt_' + 'e' * 64}).encode())
s = http.server.ThreadingHTTPServer(('127.0.0.1', 0), H)
open(sys.argv[1], 'w').write(str(s.server_port))
s.serve_forever()
EOF
python3 "$tmp/fake_runner.py" "$tmp/fake_runner.port" "$tmp/fake_runner.log" &
fake_runner=$!
for _ in $(seq 50); do [ -s "$tmp/fake_runner.port" ] && break; sleep 0.1; done
enroll_url="ws://127.0.0.1:$(cat "$tmp/fake_runner.port")/ws/daemon"
code1="bec_$(printf '1%.0s' $(seq 40))"
code2="bec_$(printf '2%.0s' $(seq 40))"
new_env="$inst/repo/install/desktop/.env"
cred="$state/desktop-credential.json"
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
no_code_anywhere() { # $1 label, $2 code
  grep -q "$2" "$inst/out" && t "$1: the enrollment code was printed"
  grep -q 'bdt_eeee' "$inst/out" && t "$1: the desktop credential was printed"
  grep -q "$2" "$inst/curl.log" 2>/dev/null && t "$1: the code reached curl"
  grep -rq "$2" "$inst/home" "$new_env" "$state" 2>/dev/null && t "$1: the code was written to disk"
}
for os in Linux Darwin; do
  code="$code1"; [ "$os" = Darwin ] && code="$code2"
  rm -rf "$state"
  run_install FAKE_OS="$os" FAKE_NO_ENV=1 FAKE_ENROLL="$code" BLERG_RUNNER_SERVER_URL="$enroll_url" \
    BLERG_RUNNER_REPOS_ROOT="$inst/repos" BLERG_RUNNER_DAEMON_NAME=new-laptop BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1
  [ "$rc" = 0 ] || t "$os enroll: exit $rc: $(out)"
  grep -q 'Desktop enrolled to you as "new-laptop"' "$inst/out" || t "$os enroll: not said: $(out)"
  [ "$(mode_of "$cred")" = 600 ] || t "$os enroll: credential is not a private file"
  grep -q '"token": "bdt_eeee' "$cred" 2>/dev/null || t "$os enroll: credential not stored"
  grep -q "^Authorization: Bearer bdt_eeee" "$inst/curl.header" 2>/dev/null || t "$os enroll: the new credential was not checked with the server"
  grep -qx "BLERG_RUNNER_SERVER_URL=$enroll_url" "$new_env" || t "$os enroll: the server was not kept in .env"
  [ "$(mode_of "$new_env")" = 600 ] || t "$os enroll: .env is not private"
  if [ "$os" = Linux ]; then svc="$inst/home/.config/systemd/user/blerg-runner-daemon.service"; else svc="$inst/home/Library/LaunchAgents/dev.blerg.runner-daemon.plist"; fi
  [ -f "$svc" ] || t "$os enroll: service not installed"
  grep -q 'bdt_' "$svc" 2>/dev/null && t "$os enroll: the credential was copied into the service"
  no_code_anywhere "$os enroll" "$code"
done
grep -q 'enroll name=new-laptop ok=True key=None' "$tmp/fake_runner.log" || t "enroll request: $(cat "$tmp/fake_runner.log")"
# The same code again is refused: exit 7, nothing replaced, the code not printed.
rm -rf "$state"
run_install FAKE_NO_ENV=1 FAKE_ENROLL="$code1" BLERG_RUNNER_SERVER_URL="$enroll_url" BLERG_RUNNER_REPOS_ROOT="$inst/repos" BLERG_SKIP_CLAUDE_TOKEN_PROMPT=1
[ "$rc" = 7 ] || t "reused code: exit $rc: $(out)"
grep -q 'already used' "$inst/out" || t "reused code: reason not said: $(out)"
restarted && t "reused code: the service was restarted"
[ -f "$cred" ] && t "reused code: a credential was stored"
no_code_anywhere "reused code" "$code1"
# An incomplete code stops before anything is built.
run_install FAKE_NO_ENV=1 FAKE_ENROLL="bec_123" BLERG_RUNNER_SERVER_URL="$enroll_url"
{ [ "$rc" = 1 ] && grep -q 'not complete' "$inst/out"; } || t "incomplete code: exit $rc: $(out)"
grep -q '^build$' "$inst/systemctl.log" 2>/dev/null && t "incomplete code: built anyway"
kill "$fake_runner" 2>/dev/null; wait "$fake_runner" 2>/dev/null

# Recovery notices use the ordinary user service, without privilege setup.
run_install
[ "$rc" = 0 ] || t "recovery notification: exit $rc: $(out)"
[ "$(grep -E '^(begin-update|build)$' "$inst/systemctl.log" | head -2)" = $'begin-update\nbuild' ] || t "installer did not bind the initiating call before build"
grep -q "^prepare-update:$state$" "$inst/systemctl.log" || t "installer did not notify the running daemon"
run_install FAKE_RESTART_RC=7
[ "$rc" = 7 ] || t "failed restart lost its status: $rc"
grep -q '^cancel-update$' "$inst/systemctl.log" || t "installer did not cancel failed restart"
grep -q 'protected-' "$inst/systemctl.log" && t "installer attempted privileged routing"

[ "$fail" = 0 ] && echo "daemon_install_test: ok"
exit "$fail"
