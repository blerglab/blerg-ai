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
cp daemon/install.sh daemon/*.tmpl daemon/wsl-startup.ps1 "$inst/repo/install/desktop/daemon/"
echo "the binary of the running daemon" > "$inst/repo/bin/blerg-runner-daemon"

# The tools install.sh calls, from the real system; the ones under test are stubs below.
bin="$inst/bin"
mkdir -p "$bin"
for x in bash sh cat grep sed tr id dirname date awk head cut wc mkdir seq sleep mv chmod rm cmp \
         mktemp find touch kill printf env basename timeout iconv base64; do
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
printf '#!/bin/sh\n[ "$1" = -m ] && echo x86_64 || echo Linux\n' > "$bin/uname"
# go: "build -o OUT" writes a daemon that reports no open work.
printf '#!/bin/sh\n[ "$1" = version ] && { echo "go version go1.26.9 linux/amd64"; exit 0; }\nprintf "#!/bin/sh\\nexit 0\\n" > "$3"; chmod +x "$3"\n' > "$bin/go"
# systemctl: is-active answers $FAKE_RUNNING; restart is logged and, like the new daemon, writes
# connection.json with $FAKE_CONN_STATE and pid $FAKE_PID (nothing when FAKE_CONN_STATE is empty);
# show -p MainPID answers $FAKE_PID; status prints a unit line holding the token, as systemd does.
cat > "$bin/systemctl" <<'EOF'
#!/bin/sh
case "$*" in
  *is-active*) [ "$FAKE_RUNNING" = 1 ] ;;
  *restart*)
    echo "restart" >> "$FAKE_LOG"
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
  rm -rf "$inst/home/.config"
  printf 'BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=true\nBLERG_RUNNER_DAEMON_TOKEN=%s\nBLERG_RUNNER_REPOS_ROOT=%s\nBLERG_SKIP_CLAUDE_TOKEN_PROMPT=1\n' "$FAKE_TOKEN" "$inst/repos" \
    > "$inst/repo/install/desktop/.env"
  echo "the binary of the running daemon" > "$inst/repo/bin/blerg-runner-daemon"
  local start; start="$(date +%s)"
  ( export PATH="$bin" HOME="$inst/home" TMPDIR="$inst/tmp" BLERG_RUNNER_DAEMON_STATE_DIR="$state" \
      FAKE_LOG="$inst/systemctl.log" FAKE_CURL_LOG="$inst/curl.log" FAKE_CURL_HEADER="$inst/curl.header" \
      FAKE_RUNNING=1 FAKE_CURL=ok FAKE_CONN_STATE=connected FAKE_PID=4242 FAKE_TOKEN_IN_UNIT="$FAKE_TOKEN" \
      BLERG_DAEMON_CONNECT_WAIT=3 BLERG_PROC_VERSION="$inst/pv" XDG_RUNTIME_DIR="$inst/run"
    unset WSL_DISTRO_NAME CLAUDE_CODE_OAUTH_TOKEN BLERG_RUNNER_SERVER_URL http_proxy https_proxy HTTP_PROXY HTTPS_PROXY FAKE_SHOW_DELAY
    for kv in "$@"; do export "$kv"; done
    opts=(install)
    [ "${FAKE_STARTUP_FLAG:-0}" = 1 ] && opts+=(--wsl-startup)
    [ "${FAKE_INSTALL_YES:-0}" = 1 ] && opts+=(--yes)
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

# --- token accepted, daemon connects: installed, restarted, says connected
rm -rf "$state"
run_install USER=ignored
grep -qx "linger-user:$(id -un)" "$inst/systemctl.log" || t "lingering must use the UID account, not inherited USER"
[ "$rc" = 0 ] || t "accepted token: exit $rc: $(out)"
restarted || t "accepted token: the service was not restarted"
grep -q 'accepts this daemon token' "$inst/out" || t "accepted token: not said: $(out)"
grep -q 'The daemon is connected: the server accepted its token' "$inst/out" || t "connected not reported: $(out)"
grep -q "^Authorization: Bearer $FAKE_TOKEN\$" "$inst/curl.header" 2>/dev/null || t "the token did not reach curl through its header file"
grep -q 'HEADER-FILE-MODE: 600' "$inst/curl.log" || t "header file is not 0600: $(cat "$inst/curl.log")"
grep -q '^URL: http://localhost:8083/api/daemon/whoami$' "$inst/curl.log" || t "whoami URL not derived from ws://localhost:8083/ws/daemon: $(cat "$inst/curl.log")"
grep -q "^OPT: --noproxy \*$" "$inst/curl.log" || t "a loopback check does not bypass the shell's proxy"
grep -q '^ARGS: -q ' "$inst/curl.log" || t "curl's first argument is not -q (a ~/.curlrc could change the check): $(cat "$inst/curl.log")"
no_token_anywhere "accepted"

# --- a remote wss server: https, no proxy bypass
rm -rf "$state"
run_install BLERG_RUNNER_SERVER_URL=wss://runner.example.com/ws/daemon
grep -q '^URL: https://runner.example.com/api/daemon/whoami$' "$inst/curl.log" || t "wss URL: $(cat "$inst/curl.log")"
grep -q -- '--noproxy' "$inst/curl.log" && t "a remote server's check must keep the shell's proxy"

# --- a credential in the server URL is never printed
for mode in ok down refused; do
  rm -rf "$state"
  run_install BLERG_RUNNER_SERVER_URL=wss://someone:url-secret-0000@runner.example.com/ws/daemon FAKE_CURL="$mode"
  grep -q 'url-secret-0000' "$inst/out" && t "$mode: the server URL's password was printed: $(out)"
  grep -q 'someone' "$inst/out" && t "$mode: the server URL's user was printed"
done
grep -q 'The server at https://runner.example.com refused' "$inst/out" || t "refused: the server is not named without its userinfo: $(out)"

# --- refused (401): stops non-zero before anything is replaced; the running service is untouched
rm -rf "$state"
run_install FAKE_CURL=refused
[ "$rc" = 6 ] || t "refused token: exit $rc, want 6: $(out)"
grep -q 'refused this daemon token' "$inst/out" && grep -q 'left as it was' "$inst/out" || t "refused: message: $(out)"
grep -q 'install/desktop/.env' "$inst/out" && grep -q 'BLERG_RUNNER_DAEMON_TOKEN' "$inst/out" || t "refused: does not say where the token comes from: $(out)"
restarted && t "refused: the running service was restarted"
[ -e "$inst/home/.config/systemd/user/blerg-runner-daemon.service" ] && t "refused: the unit was rewritten"
[ "$(cat "$inst/repo/bin/blerg-runner-daemon")" = "the binary of the running daemon" ] || t "refused: the running daemon's binary was replaced"
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

[ "$fail" = 0 ] && echo "daemon_install_test: ok"
exit "$fail"
