#!/usr/bin/env bash
# Tests for hostcheck.sh (engine preflight, WSL detection) and the argument handling of
# blerg-up.sh / daemon/install.sh. Needs no docker: everything runs against a scratch HOME/PATH.
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
  for x in bash cat grep sed tr id uname dirname date awk head cut wc mkdir seq sleep; do
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
  ( export PATH="$p" HOME="$h"; unset CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_API_KEY; for kv in "$@"; do export "$kv"; done
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

# macOS keychain: claude on PATH, no credentials file -> "could not verify", never "missing".
mkbin "$tmp/bin-mac" claude uname
rm -f "$tmp/bin-mac/uname"; printf '#!/bin/sh\necho Darwin\n' > "$tmp/bin-mac/uname"; chmod +x "$tmp/bin-mac/uname"
out="$(run_pf "$tmp/bin-mac" "$tmp/h0")"
grep -q 'could not verify' <<<"$out" || t "darwin: expected 'could not verify'"
grep -q 'no coding engine is ready' <<<"$out" && t "darwin: must not say missing"

# --- argument handling
out="$(./blerg-up.sh --help 2>&1)"; rc=$?
[ "$rc" = 0 ] || t "blerg-up --help exit $rc"
for f in --no-build --no-daemon --no-open --regen-secrets --reset-admin --help; do grep -q -- "$f" <<<"$out" || t "blerg-up usage lacks $f"; done
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

# --- the marker for an explicit "no" on the Claude token question
grep -q 'BLERG_SKIP_CLAUDE_TOKEN_PROMPT' daemon/install.sh || t "skip marker missing from install.sh"

[ "$fail" = 0 ] && echo "hostcheck_test: ok"
exit "$fail"
