#!/usr/bin/env bash
# Tests for hostcheck.sh (engine preflight, WSL detection), the argument handling of blerg-up.sh /
# daemon/install.sh and the planned-restart marker install.sh leaves the daemon.
# Needs no docker: everything runs against a scratch HOME/PATH.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
HERE="$PWD"
fail=0
t() { echo "FAIL: $*"; fail=1; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

. ./brand.sh
. ./hostcheck.sh
export NO_COLOR=1

# A PATH holding only what the checks call, so `claude`/`codex`/`curl` presence is ours to choose.
mkbin() { # dir tool...
  mkdir -p "$1"; local d="$1"; shift; rm -f "$d"/*
  for x in python3 bash cat grep sed tr id uname dirname date awk head cut wc mkdir seq sleep; do
    p="$(command -v "$x" 2>/dev/null)" && ln -sf "$p" "$d/$x"
  done
  for x in "$@"; do rm -f "$d/$x"; printf '#!/bin/sh\nexit 0\n' > "$d/$x"; chmod +x "$d/$x"; done
}

# --- WSL detection
echo "Linux version 6.6.0.1-microsoft-standard-WSL2" > "$tmp/pv-wsl"
echo "Linux version 6.8.0-generic (buildd@lcy02)" > "$tmp/pv-plain"
( unset WSL_DISTRO_NAME; BLERG_PROC_VERSION="$tmp/pv-wsl" is_wsl ) || t "is_wsl missed microsoft in /proc/version"
( unset WSL_DISTRO_NAME; BLERG_PROC_VERSION="$tmp/pv-plain" is_wsl ) && t "is_wsl true on a plain kernel"
( WSL_DISTRO_NAME=Ubuntu BLERG_PROC_VERSION="$tmp/pv-plain" is_wsl ) || t "is_wsl missed WSL_DISTRO_NAME"
mkdir -p "$tmp/sysd"
( BLERG_PROC_VERSION="$tmp/pv-wsl" BLERG_SYSTEMD_RUN_DIR="$tmp/nosysd" wsl_without_systemd ) || t "wsl without systemd not detected"
( BLERG_PROC_VERSION="$tmp/pv-wsl" BLERG_SYSTEMD_RUN_DIR="$tmp/sysd" wsl_without_systemd ) && t "wsl with systemd flagged"
box="$(wsl_systemd_box)"
grep -q 'systemd=true' <<<"$box" && grep -q 'wsl --shutdown' <<<"$box" && grep -q '/etc/wsl.conf' <<<"$box" || t "wsl box lacks exact steps"

# --- engine preflight
mkbin "$tmp/bin-none"
mkbin "$tmp/bin-claude" claude
mkbin "$tmp/bin-codex" codex
run_pf() { # PATH-dir HOME [env...] ; prints preflight output
  local p="$1" h="$2"; shift 2
  ( export PATH="$p" HOME="$h"; unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY CODEX_HOME; for kv in "$@"; do export "$kv"; done
    engine_preflight "$h/.env" )
}
mkdir -p "$tmp/h0"
out="$(run_pf "$tmp/bin-none" "$tmp/h0")"
grep -q 'no coding engine is ready' <<<"$out" || t "nothing installed: no message"
[ "$(grep -c 'no coding engine is ready' <<<"$out")" = 1 ] || t "message not printed exactly once"
grep -q 'sessions will fail' <<<"$out" || t "message doesn't say sessions fail"

out="$(run_pf "$tmp/bin-claude" "$tmp/h0")"
grep -q 'no coding engine is ready' <<<"$out" || t "claude installed but not logged in: expected message"

mkdir -p "$tmp/h1/.claude"; echo '{"claudeAiOauth":{"accessToken":"sk-x"}}' > "$tmp/h1/.claude/.credentials.json"
[ -z "$(run_pf "$tmp/bin-claude" "$tmp/h1")" ] || t "claude logged in: expected silence"
[ -n "$(run_pf "$tmp/bin-none" "$tmp/h1")" ] || t "credentials without a claude binary must still warn"
echo '{"claudeAiOauth":{"accessToken":""}}' > "$tmp/h1/.claude/.credentials.json"
[ -n "$(run_pf "$tmp/bin-claude" "$tmp/h1")" ] || t "empty accessToken counted as logged in"

[ -z "$(run_pf "$tmp/bin-claude" "$tmp/h0" CLAUDE_CODE_OAUTH_TOKEN=tok)" ] || t "oauth token env: expected silence"
[ -z "$(run_pf "$tmp/bin-claude" "$tmp/h0" ANTHROPIC_API_KEY=k)" ] || t "api key env: expected silence"
echo 'ANTHROPIC_API_KEY=' > "$tmp/h0/.env"
[ -n "$(run_pf "$tmp/bin-claude" "$tmp/h0")" ] || t "blank ANTHROPIC_API_KEY in .env counted as usable"
echo 'CLAUDE_CODE_OAUTH_TOKEN=abc' > "$tmp/h0/.env"
[ -z "$(run_pf "$tmp/bin-claude" "$tmp/h0")" ] || t "token in .env: expected silence"
rm -f "$tmp/h0/.env"

mkdir -p "$tmp/h2/.codex"; echo '{}' > "$tmp/h2/.codex/auth.json"
[ -z "$(run_pf "$tmp/bin-codex" "$tmp/h2")" ] || t "codex logged in: expected silence"
[ -n "$(run_pf "$tmp/bin-none" "$tmp/h2")" ] || t "codex auth without binary should not count"
mkdir -p "$tmp/custom-codex"; echo '{}' > "$tmp/custom-codex/auth.json"
[ -z "$(run_pf "$tmp/bin-codex" "$tmp/h0" CODEX_HOME="$tmp/custom-codex")" ] || t "custom Codex home was not discovered"
[ -n "$(run_pf "$tmp/bin-codex" "$tmp/h2" CODEX_HOME="$tmp/missing-codex")" ] || t "custom empty Codex home borrowed default credentials"

# macOS keychain: claude on PATH, no credentials file -> "could not verify", never "missing".
mkbin "$tmp/bin-mac" claude uname
rm -f "$tmp/bin-mac/uname"; printf '#!/bin/sh\necho Darwin\n' > "$tmp/bin-mac/uname"; chmod +x "$tmp/bin-mac/uname"
out="$(run_pf "$tmp/bin-mac" "$tmp/h0")"
grep -q 'could not verify' <<<"$out" || t "darwin: expected 'could not verify'"
grep -q 'no coding engine is ready' <<<"$out" && t "darwin: must not say missing"

# --- argument handling
out="$(./blerg-up.sh --help 2>&1)"; rc=$?
[ "$rc" = 0 ] || t "blerg-up --help exit $rc"
for f in --no-build --no-daemon --no-open --yes --regen-secrets --reset-admin --help; do grep -q -- "$f" <<<"$out" || t "blerg-up usage lacks $f"; done
out="$(./blerg-up.sh --bogus 2>&1)"; rc=$?
[ "$rc" != 0 ] || t "blerg-up unknown flag exited 0"
grep -q 'unknown option: --bogus' <<<"$out" && grep -q 'Usage:' <<<"$out" || t "blerg-up unknown flag: no message/usage"
out="$(./daemon/install.sh --help 2>&1)"; rc=$?
[ "$rc" = 0 ] && grep -q 'preflight' <<<"$out" || t "install.sh --help"
out="$(./daemon/install.sh bogus 2>&1)"; rc=$?
[ "$rc" != 0 ] && grep -q 'Usage:' <<<"$out" || t "install.sh unknown subcommand"
out="$(./daemon/install.sh 2>&1)"; rc=$?
[ "$rc" != 0 ] && grep -q 'Usage:' <<<"$out" || t "install.sh no subcommand"

# --- curl prerequisite fails fast (before docker is even looked for)
mkbin "$tmp/bin-nocurl"
out="$(PATH="$tmp/bin-nocurl" ./blerg-up.sh --no-daemon 2>&1)"; rc=$?
[ "$rc" != 0 ] && grep -q "curl isn't installed" <<<"$out" || t "missing curl not reported (rc=$rc)"

# --- first-run setup: where the person creates the first account (stub curl and docker)
mkbin "$tmp/bin-setup"
cat > "$tmp/bin-setup/curl" <<'EOS'
#!/bin/sh
[ -n "$STUB_PROVIDER" ] || exit 7
printf '%s' "$STUB_PROVIDER"
EOS
cat > "$tmp/bin-setup/docker" <<'EOS'
#!/bin/sh
echo "$*" >> "$STUB_DOCKER_LOG"
[ "$*" = "compose exec -T blerg-core /blerg-core setup-link" ] || exit 9
[ -n "$STUB_LINK" ] || { echo "setup is complete" >&2; exit 1; }
echo "$STUB_LINK"
EOS
chmod +x "$tmp/bin-setup/curl" "$tmp/bin-setup/docker"
fsu() { ( export PATH="$tmp/bin-setup" STUB_DOCKER_LOG="$tmp/docker.log"; for kv in "$@"; do export "$kv"; done; first_run_setup_url 9001 "${MINT:-1}" ); }
: > "$tmp/docker.log"
out="$(fsu 'STUB_PROVIDER={"id":"local","setup":"open"}')"; rc=$?
[ "$rc" = 0 ] && [ "$out" = "http://localhost:9001/setup" ] || t "setup open: got '$out' rc=$rc, want the plain /setup page"
[ -s "$tmp/docker.log" ] && t "setup open must not mint a setup code"
out="$(fsu 'STUB_PROVIDER={"id":"local","setup":"code"}' 'STUB_LINK=http://localhost:9001/setup#code=abc')"; rc=$?
[ "$rc" = 0 ] && [ "$out" = "http://localhost:9001/setup#code=abc" ] || t "setup needs a code: got '$out' rc=$rc, want the setup-link output"
# Not a terminal: nothing minted, nothing printed, 2 so the caller prints the command instead.
: > "$tmp/docker.log"
out="$(MINT=0 fsu 'STUB_PROVIDER={"id":"local","setup":"code"}' 'STUB_LINK=http://localhost:9001/setup#code=abc')"; rc=$?
[ "$rc" = 2 ] && [ -z "$out" ] && [ ! -s "$tmp/docker.log" ] || t "no terminal: a setup code was minted or printed (rc=$rc)"
out="$(fsu 'STUB_PROVIDER={"id":"local","setup":"code"}')"; rc=$?
[ "$rc" != 0 ] && [ -z "$out" ] || t "setup-link refused: expected nothing (got '$out' rc=$rc)"
out="$(fsu 'STUB_PROVIDER={"id":"local"}')"; rc=$?
[ "$rc" != 0 ] && [ -z "$out" ] || t "set up already: expected nothing (got '$out' rc=$rc)"
out="$(fsu)"; rc=$?
[ "$rc" != 0 ] && [ -z "$out" ] || t "core unreachable: expected nothing (got '$out' rc=$rc)"
# blerg-up.sh sends people to that page, not to a password in the log, and keeps --reset-admin.
grep -q 'first_run_setup_url "$PORT_CORE"' blerg-up.sh || t "blerg-up.sh does not look for first-run setup"
grep -q 'open_browser "${SETUP_URL:-http://localhost:$PORT_CORE}"' blerg-up.sh || t "blerg-up.sh does not open the setup page"
grep -q 'BLERG_BOOTSTRAP_ADMIN_PASSWORD' blerg-up.sh && t "blerg-up.sh still points at a bootstrap password in the log"
grep -q 'users set-password --subject' blerg-up.sh || t "--reset-admin lost its users set-password call"
grep -q 'first_run_setup_url "$PORT_CORE" "$SETUP_MINT"' blerg-up.sh && grep -q '\[ -t 1 \] && SETUP_MINT=1' blerg-up.sh \
  || t "blerg-up.sh does not limit minting a setup link to a terminal"

# --- the question before reinstalling over running agent work
mkdir -p "$tmp/rw"
printf '#!/bin/sh\n[ "$1 $2" = "status --open-work" ] || exit 9\nprintf "session s1 (proj): a turn is running, 1 subagent\\n  - subagent Explore: Find the callers\\n"\n' > "$tmp/rw/busy"
printf '#!/bin/sh\nexit 0\n' > "$tmp/rw/idle"
printf '#!/bin/sh\nexit 2\n' > "$tmp/rw/old"
chmod +x "$tmp/rw/busy" "$tmp/rw/idle" "$tmp/rw/old"
out="$(confirm_reinstall_over_work "$tmp/rw/idle" </dev/null 2>&1)"; rc=$?
[ "$rc" = 0 ] && [ -z "$out" ] || t "nothing running: expected silence and go (rc=$rc, $out)"
out="$(confirm_reinstall_over_work "$tmp/rw/old" </dev/null 2>&1)"; rc=$?
[ "$rc" = 0 ] || t "a binary that cannot answer must not block (rc=$rc)"
out="$(confirm_reinstall_over_work "$tmp/rw/busy" </dev/null 2>&1)"; rc=$?
[ "$rc" = 2 ] || t "work running, no terminal: expected rc 2, got $rc"
grep -q 'session s1 (proj): a turn is running' <<<"$out" && grep -q 'subagent Explore: Find the callers' <<<"$out" || t "work not listed: $out"
grep -q -- '--yes' <<<"$out" || t "no terminal: does not say --yes"
out="$(confirm_reinstall_over_work "$tmp/rw/busy" 1 </dev/null 2>&1)"; rc=$?
[ "$rc" = 0 ] && grep -q 'session s1' <<<"$out" && grep -q 'anyway (--yes)' <<<"$out" || t "--yes: expected the list and go (rc=$rc)"
out="$(./daemon/install.sh install --bogus 2>&1)"; rc=$?
[ "$rc" != 0 ] && grep -q 'unknown option: --bogus' <<<"$out" || t "install.sh unknown option"
grep -q -- '--yes' <<<"$(./daemon/install.sh --help 2>&1)" || t "install.sh usage lacks --yes"
grep -q '^EXIT_WORK_RUNNING=5$' daemon/install.sh && grep -q '^    5) # install.sh.s EXIT_WORK_RUNNING' blerg-up.sh \
  || t "blerg-up.sh and install.sh disagree on the work-running exit code"

# --- a reinstall over a running daemon tells the next daemon the restart is planned: install.sh
# writes restart-planned (with its reason) into the daemon's state directory before it restarts the
# service (runner/internal/daemon/restartcause.go reads it). Runs install.sh from a scratch copy of
# the tree against stub go/systemctl, so nothing real is built, installed or restarted.
inst="$tmp/inst"
mkdir -p "$inst/repo/install/desktop/daemon" "$inst/repo/runner" "$inst/repo/bin" "$inst/home" "$inst/repos"
cp brand.sh hostcheck.sh "$inst/repo/install/desktop/"
cp daemon/install.sh daemon/*.tmpl "$inst/repo/install/desktop/daemon/"
printf 'BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=true\nBLERG_RUNNER_DAEMON_TOKEN=tok\nBLERG_RUNNER_REPOS_ROOT=%s\nBLERG_SKIP_CLAUDE_TOKEN_PROMPT=1\n' "$inst/repos" > "$inst/repo/install/desktop/.env"
mkbin "$inst/bin" loginctl
for x in mv chmod rm cmp; do p="$(command -v "$x")" && ln -sf "$p" "$inst/bin/$x"; done
rm -f "$inst/bin/uname"
printf '#!/bin/sh\n[ "$1" = -m ] && echo x86_64 || echo Linux\n' > "$inst/bin/uname"
# go: build locates -o OUT after optional build flags and writes a daemon whose content is $FAKE_BUILD and that reports no open work.
printf '#!/bin/sh\n[ "$1" = version ] && { echo "go version go1.26.9 linux/amd64"; exit 0; }\nwhile [ "$1" != -o ]; do shift; done\nprintf "#!/bin/sh\\n# %%s\\nexit 0\\n" "$FAKE_BUILD" > "$2"; chmod +x "$2"\n' > "$inst/bin/go"
# systemctl: is-active answers $FAKE_RUNNING; restart logs what the marker held at that moment.
printf '#!/bin/sh\ncase "$*" in\n  *is-active*) [ "$FAKE_RUNNING" = 1 ] ;;\n  *restart*) echo "restart marker=$(cat "$BLERG_RUNNER_DAEMON_STATE_DIR/restart-planned" 2>/dev/null)" >> "$FAKE_LOG" ;;\nesac\n' > "$inst/bin/systemctl"
chmod +x "$inst/bin/uname" "$inst/bin/go" "$inst/bin/systemctl"
run_install() { # RUNNING BUILD ; runs install.sh install, prints the systemctl log
  rm -rf "$inst/state" "$inst/systemctl.log"
  ( export PATH="$inst/bin" HOME="$inst/home" BLERG_RUNNER_DAEMON_STATE_DIR="$inst/state" FAKE_LOG="$inst/systemctl.log" \
      FAKE_RUNNING="$1" FAKE_BUILD="$2" BLERG_PROC_VERSION="$tmp/pv-plain" XDG_RUNTIME_DIR="$inst/run" BLERG_DAEMON_CONNECT_WAIT=0
    unset WSL_DISTRO_NAME CLAUDE_CODE_OAUTH_TOKEN BLERG_RUNNER_SERVER_URL
    "$inst/repo/install/desktop/daemon/install.sh" install </dev/null >"$inst/out" 2>&1 ) || { echo "install.sh failed:"; cat "$inst/out"; }
  cat "$inst/systemctl.log" 2>/dev/null
}
[ "$(run_install 1 build-a)" = "restart marker=update" ] || t "over a running daemon, a new build: no 'update' marker before the restart ($(cat "$inst/systemctl.log" 2>/dev/null))"
[ "$(cat "$inst/state/restart-planned" 2>/dev/null)" = update ] || t "the update marker was not left for the daemon"
[ "$(run_install 1 build-a)" = "restart marker=reinstall" ] || t "over a running daemon, the same build: no 'reinstall' marker before the restart"
[ "$(run_install 0 build-b)" = "restart marker=" ] || t "no daemon running, yet a planned-restart marker"
[ ! -e "$inst/state/restart-planned" ] || t "no daemon running, yet a marker was left"

# --- a host Go older than the release runner/go.mod names is not used: with no docker to fall back
# to, the install stops and says which Go it needs (the stub reports go1.26.0).
printf '#!/bin/sh\n[ "$1" = version ] && { echo "go version go1.26.0 linux/amd64"; exit 0; }\nexit 1\n' > "$inst/bin/go-old"
cp "$inst/bin/go" "$inst/bin/go-new"; cp "$inst/bin/go-old" "$inst/bin/go"; chmod +x "$inst/bin/go"
run_install 0 build-old >/dev/null
grep -q 'Need either go >= 1.26.9 or docker' "$inst/out" || t "an old host Go was accepted, or the message does not name the needed Go ($(tail -3 "$inst/out"))"
cp "$inst/bin/go-new" "$inst/bin/go"; chmod +x "$inst/bin/go"
[ "$(run_install 1 build-a)" = "restart marker=update" ] || t "a go1.26.9 host was not used for the build"

# --- the Go release the installer requires is the one runner/go.mod names, in both places
gomod="$(sed -n 's/^go \([0-9.]*\)$/\1/p' ../../runner/go.mod)"
grep -q "^GO_MIN_VERSION=$gomod\$" daemon/install.sh || t "GO_MIN_VERSION in install.sh is not runner/go.mod's go $gomod"
grep -q "golang:$gomod@sha256:" daemon/install.sh || t "the installer's Docker fallback image is not golang:$gomod"

# --- the marker for an explicit "no" on the Claude token question
grep -q 'BLERG_SKIP_CLAUDE_TOKEN_PROMPT' daemon/install.sh || t "skip marker missing from install.sh"

# --- the Docker client config's noProxy, which the compose services keep (blerg-up.sh exports it)
if command -v python3 >/dev/null 2>&1; then
  dcfg="$tmp/dockercfg"; mkdir -p "$dcfg"
  cat > "$dcfg/config.json" <<'JSON'
{"proxies":{"default":{"httpProxy":"http://proxy.invalid:3128","noProxy":"a.invalid, b.invalid,,"},
 "unix:///var/run/docker.sock":{"noProxy":"sock.invalid"},"tcp://other.invalid:2376":{"noProxy":"other.invalid"}}}
JSON
  [ "$(DOCKER_CONFIG="$dcfg" DOCKER_HOST= docker_client_no_proxy)" = "sock.invalid" ] || t "docker_client_no_proxy: the default socket key is not used"
  [ "$(DOCKER_CONFIG="$dcfg" DOCKER_HOST=tcp://other.invalid:2376 docker_client_no_proxy)" = "other.invalid" ] || t "docker_client_no_proxy: DOCKER_HOST key not used"
  [ "$(DOCKER_CONFIG="$dcfg" DOCKER_HOST=tcp://third.invalid:1 docker_client_no_proxy)" = "a.invalid,b.invalid" ] || t "docker_client_no_proxy: no fallback to default / list not trimmed"
  out="$(DOCKER_CONFIG="$dcfg" DOCKER_HOST=tcp://third.invalid:1 docker_client_no_proxy)"
  ! grep -q 'proxy.invalid' <<<"$out" || t "docker_client_no_proxy printed a proxy address"
  printf '{not json' > "$dcfg/config.json"
  DOCKER_CONFIG="$dcfg" docker_client_no_proxy >/dev/null 2>&1 || t "docker_client_no_proxy must not fail on a broken config"
  [ -z "$(DOCKER_CONFIG="$dcfg" docker_client_no_proxy)" ] || t "docker_client_no_proxy printed something for a broken config"
  [ -z "$(DOCKER_CONFIG="$tmp/none" docker_client_no_proxy)" ] || t "docker_client_no_proxy printed something without a config"
else
  echo "skip: python3 unavailable, docker_client_no_proxy not tested"
fi
grep -q '^BLERG_DOCKER_NO_PROXY="$(docker_client_no_proxy)"' blerg-up.sh && grep -q '^export BLERG_DOCKER_NO_PROXY' blerg-up.sh \
  || t "blerg-up.sh does not export BLERG_DOCKER_NO_PROXY"

[ "$fail" = 0 ] && echo "hostcheck_test: ok"
exit "$fail"
