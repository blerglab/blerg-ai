#!/usr/bin/env bash
# install/desktop/hostcheck.sh — host checks shared by blerg-up.sh and
# daemon/install.sh. Sourced after brand.sh, never executed directly; every
# function reads its inputs from $HOME / the environment so it can be tested
# against a scratch HOME (see hostcheck_test.sh).

# is_wsl: true inside Windows Subsystem for Linux (WSL1 or WSL2).
is_wsl() {
  [ -n "${WSL_DISTRO_NAME:-}" ] && return 0
  grep -qiE 'microsoft|wsl' "${BLERG_PROC_VERSION:-/proc/version}" 2>/dev/null
}

# wsl_without_systemd: WSL where systemd is not PID 1, so there is no
# `systemd --user` session and never will be until wsl.conf enables it.
wsl_without_systemd() {
  is_wsl && [ ! -d "${BLERG_SYSTEMD_RUN_DIR:-/run/systemd/system}" ]
}

wsl_systemd_box() {
  blerg_box "systemd is off in this WSL distribution" <<'EOF2'
The runner daemon runs as a systemd --user service, and WSL2 only runs
systemd when you turn it on. Do this once:

  1. In this distribution, add these two lines to /etc/wsl.conf
     (create the file if it does not exist):

       [boot]
       systemd=true

  2. From Windows (PowerShell or cmd), stop WSL:

       wsl --shutdown

  3. Reopen this distribution and re-run ./blerg-up.sh

Or start the stack without the daemon service this run:

  ./blerg-up.sh --no-daemon
EOF2
}

# claude_login_state [ENV_FILE] prints one of: ready | missing | unverified.
# Mirrors runner/internal/daemon/engines.go claudeAvailable (a non-empty
# accessToken in ~/.claude/.credentials.json), and additionally counts a
# CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY that the shell or .env provides,
# since the service is started with those. macOS keeps the login in the
# keychain where a script cannot see it, so a `claude` on PATH there with no
# credentials file is "unverified", never "missing".
claude_login_state() {
  local cred="$HOME/.claude/.credentials.json" envf="${1:-}"
  command -v claude >/dev/null 2>&1 || { echo missing; return 0; }
  if [ -s "$cred" ] && grep -q '"accessToken"[[:space:]]*:[[:space:]]*"[^"]' "$cred" 2>/dev/null; then
    echo ready; return 0
  fi
  if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    echo ready; return 0
  fi
  if [ -n "$envf" ] && [ -f "$envf" ] && grep -qE '^(CLAUDE_CODE_OAUTH_TOKEN|ANTHROPIC_API_KEY)=.+' "$envf" 2>/dev/null; then
    echo ready; return 0
  fi
  if [ "$(uname -s)" = "Darwin" ]; then echo unverified; else echo missing; fi
}

# detect_ready_engines [ENV_FILE] sets ENGINES_READY (space separated ids,
# claude first) and CLAUDE_STATE. Codex/hermes/openclaw mirror the daemon's
# codexAlreadyLoggedIn / hermesAlreadyConfigured / openclawAlreadyConfigured.
detect_ready_engines() {
  ENGINES_READY=""
  CLAUDE_STATE="$(claude_login_state "${1:-}")"
  [ "$CLAUDE_STATE" = ready ] && ENGINES_READY="claude"
  if command -v codex >/dev/null 2>&1 && [ -s "$HOME/.codex/auth.json" ]; then
    ENGINES_READY="$ENGINES_READY codex"
  fi
  if command -v hermes >/dev/null 2>&1; then
    if { [ -s "$HOME/.hermes/.env" ] && grep -qE '^[A-Z_]+(_API_KEY|_TOKEN)=.+' "$HOME/.hermes/.env" 2>/dev/null; } \
      || { [ -s "$HOME/.hermes/config.yaml" ] && grep -qE '^\s*base_url:\s*\S' "$HOME/.hermes/config.yaml" 2>/dev/null; }; then
      ENGINES_READY="$ENGINES_READY hermes"
    fi
  fi
  if command -v openclaw >/dev/null 2>&1 && [ -s "$HOME/.openclaw/state/openclaw.sqlite" ]; then
    ENGINES_READY="$ENGINES_READY openclaw"
  fi
  ENGINES_READY="$(echo "$ENGINES_READY" | sed -e 's/^ *//' -e 's/ *$//')"
}

# engine_preflight [ENV_FILE]: print ONE message when no engine looks usable
# and return 0 either way — it informs, it never blocks the install.
engine_preflight() {
  detect_ready_engines "${1:-}"
  [ -n "$ENGINES_READY" ] && return 0
  if [ "$CLAUDE_STATE" = unverified ]; then
    blerg_box "could not verify a Claude login" <<'EOF2'
`claude` is installed, but on macOS its login lives in the keychain, which
this script cannot read - so it can't tell whether you're signed in.
If you haven't, run `claude` once and sign in (or `claude setup-token` and
export the result as CLAUDE_CODE_OAUTH_TOKEN, then re-run ./blerg-up.sh).
Sessions fail until an engine is signed in; the install carries on either way.
EOF2
    return 0
  fi
  blerg_box "no coding engine is ready on this machine" <<'EOF2'
Blerg drives your own Claude Code (or Codex) login; it doesn't provide one.
None was found, so sessions will fail until you set one up. Installing
carries on regardless - do this whenever you like:

  Claude Code: https://docs.claude.com/en/docs/claude-code
    then run `claude` once and sign in, or run `claude setup-token` and
    export its output as CLAUDE_CODE_OAUTH_TOKEN before ./blerg-up.sh
  Codex: install it and run `codex login`
EOF2
  return 0
}

# confirm_reinstall_over_work BIN [ASSUME_YES]: before a reinstall restarts the
# running daemon, ask the newly built daemon BIN which agent sessions the
# restart would cut off (a turn running, background tasks, subagents) and, if
# there are any, list them and ask whether to go on. Returns 0 to go on, 1 when
# the person said no, 2 when there was no terminal to ask (ASSUME_YES=1 goes on
# after listing them). A binary that cannot answer never blocks the install.
confirm_reinstall_over_work() {
  local bin="$1" yes="${2:-0}" out ans
  out="$("$bin" status --open-work 2>/dev/null)" || return 0
  [ -n "$out" ] || return 0
  blerg_box "agent work is running" <<EOF2
Reinstalling restarts the daemon, and that cuts this work off:

$out

The sessions come back after the restart: what was cut off is marked
stopped, and the agent is told what it was and asked to check it before
picking it up again.
EOF2
  if [ "$yes" = 1 ]; then
    blerg_step "Reinstalling anyway (--yes)."
    return 0
  fi
  if [ ! -t 0 ]; then
    blerg_fail "Not reinstalled: there is no terminal to ask. Re-run with --yes to reinstall anyway."
    return 2
  fi
  read -r -p "  Reinstall anyway? [y/N] " ans
  case "$ans" in
    y|Y|yes|YES) return 0 ;;
  esac
  return 1
}
