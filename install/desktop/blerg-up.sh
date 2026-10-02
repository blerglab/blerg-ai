#!/usr/bin/env bash
# blerg-up.sh — "blerg up" equivalent for the desktop integration stack.
#
# Brings up postgres + blerg-core + blerg-board + blerg-runner, waits for each
# to answer its health check, then installs+starts the runner DAEMON as a
# host service (systemd --user on Linux, launchd on macOS — see daemon/
# install.sh and DAEMON.md). The daemon can't be containerized: it drives
# your own `claude` CLI and tmux directly, so it always runs on the host.
#
# Usage: ./blerg-up.sh [--no-build] [--no-daemon] [--no-open]
#                      [--regen-secrets] [--reset-admin] [--help]
#   --no-build   skip rebuilding images (fast path; --build is kept as a
#                harmless no-op synonym for old habits — rebuilding is now
#                always on by default, since Docker's layer cache makes a
#                no-op rebuild fast, and skipping it after a `git pull` is
#                exactly how a real code fix silently failed to take effect)
#   --no-daemon  skip installing the runner daemon service (manual instructions instead)
#   --no-open    don't open a browser to blerg-core when everything's up
#   --regen-secrets  regenerate any of the secrets in .env that are missing, a
#                placeholder, or too short — everything else in .env is left alone
#   --reset-admin    reset the (oldest) local admin account's password and print a new
#                one-time password (every existing session is signed out); requires the
#                stack to already be up
#   -h, --help       print this usage and exit

set -euo pipefail
umask 077

cd "$(dirname "${BASH_SOURCE[0]}")"
. ./brand.sh
. ./hostcheck.sh

# A .env from before this umask+chmod hygiene was added may still be
# group/world-readable — the common case on an upgrade, since that's exactly
# the machine that already has one. Tighten it in place every run, whether or
# not this run goes on to rewrite it: idempotent, best-effort, never fatal.
# The `|| true` is load-bearing under `set -euo pipefail`: errexit fires on
# the final command of an `&&` list too, so a genuine chmod failure (a
# read-only home, NFS, a file owned by someone else) would otherwise abort
# the whole install/upgrade instead of being skipped.
[ -f .env ] && chmod 600 .env 2>/dev/null || true

COMPOSE_FILE="docker-compose.yml"
BUILD_FLAG="--build"
INSTALL_DAEMON=1
OPEN_BROWSER=1
REGEN_SECRETS=0
RESET_ADMIN=0
usage() {
  cat <<EOF
Usage: ./blerg-up.sh [options]

Brings up the desktop stack (postgres, blerg-core, blerg-board, blerg-runner)
and installs the runner daemon as a host service.

Options:
  --no-build       skip rebuilding images (fast path; --build is accepted as a
                   no-op, rebuilding is the default)
  --no-daemon      skip installing the runner daemon service
  --no-open        don't open a browser when everything is up
  --regen-secrets  regenerate secrets in .env that are missing, placeholders or
                   too short (everything else in .env is kept)
  --reset-admin    reset the oldest local admin's password and print a new
                   one-time password (signs every session out); the stack must
                   already be up
  -h, --help       show this help
EOF
}

for arg in "$@"; do
  case "$arg" in
    --build) : ;;  # default now; kept accepted so old habits don't break
    --no-build) BUILD_FLAG="" ;;
    --no-daemon) INSTALL_DAEMON=0 ;;
    --no-open) OPEN_BROWSER=0 ;;
    --regen-secrets) REGEN_SECRETS=1 ;;
    --reset-admin) RESET_ADMIN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "blerg-up.sh: unknown option: $arg" >&2; echo >&2; usage >&2; exit 2 ;;
  esac
done

# --reset-admin (R8: admin recovery) is handled before anything else — the whole point is to
# recover access when the operator has lost the bootstrap password, so it doesn't wait on the
# rest of this script's docker/daemon/build checks. It does require the stack to already be up
# (it execs `psql` inside the running postgres container and runs blerg-core's own `users`
# subcommand against it), so a stack that was never started fails with an actionable message
# rather than a raw docker error.
if [ "$RESET_ADMIN" -eq 1 ]; then
  # Deterministic pick when more than one admin exists (e.g. an operator ran `users create
  # --role admin` for a second account): the OLDEST local admin account, by created_at — the
  # bootstrap admin EnsureBootstrapAdmin seeds is always the first row, so this reliably targets
  # it rather than a newer, arbitrary admin.
  subject="$(docker compose exec -T postgres psql -U blerg -d blerg_core -tAc "select provider_subject from accounts where provider='local' and role='admin' order by created_at limit 1")"
  [ -n "$subject" ] || { blerg_fail "No local admin account found (is the stack up? ./blerg-up.sh first)"; exit 1; }
  # The image is distroless (no shell) — run the binary's own subcommand.
  line="$(docker compose run --rm --no-deps -T --entrypoint /blerg-core -e "DATABASE_URL=postgres://blerg:blerg@postgres:5432/blerg_core?sslmode=disable" blerg-core users set-password --subject "$subject")"
  blerg_box "admin password reset" <<EOF
$line
Sign in with it at http://localhost:$(grep '^BLERG_PORT_CORE=' .env | cut -d= -f2 || echo 8081)/login — you'll be asked to choose a new password.
EOF
  exit 0
fi

blerg_banner

if [ "$(id -u)" -eq 0 ]; then
  blerg_box "hold on" <<'EOF'
Do not run this as root ('sudo ./blerg-up.sh'). It escalates specific
commands internally (via sudo) when it actually needs to — running the
whole script as root would install the runner daemon under root's own
systemd session instead of yours, so agent sessions would run as root.
EOF
  exit 1
fi

fail_block() {
  # Prints a boxed message and exits — nothing else runs after it.
  blerg_box "can't continue"
  exit 1
}

if ! command -v curl >/dev/null 2>&1; then
  fail_block <<'EOF'
curl isn't installed (or not on PATH). The health checks that decide
whether the stack came up use it, so without it healthy services would
be reported as timed out.

Install it, then re-run ./blerg-up.sh:
  Debian/Ubuntu:  sudo apt install curl
  Fedora/RHEL:    sudo dnf install curl
  macOS:          brew install curl
EOF
fi

if ! command -v docker >/dev/null 2>&1; then
  fail_block <<'EOF'
Docker isn't installed (or not on PATH) - the desktop stack runs
entirely as Docker containers.

Install it, then re-run ./blerg-up.sh:
  https://docs.docker.com/get-docker/
EOF
fi

if ! docker compose version >/dev/null 2>&1; then
  fail_block <<'EOF'
Docker is installed, but the 'docker compose' plugin isn't available.
blerg needs Compose v2 (bundled with Docker Desktop; on Linux, install
the docker-compose-plugin package), then re-run ./blerg-up.sh.
EOF
fi

if ! DOCKER_INFO_ERR="$(docker info 2>&1 >/dev/null)"; then
  if echo "$DOCKER_INFO_ERR" | grep -qi "permission denied"; then
    fail_block <<'EOF'
Docker is installed, but this user can't talk to it (permission denied)
- you're probably not in the 'docker' group yet.

Fix it, then log out and back in (group membership needs a fresh
session) and re-run ./blerg-up.sh:
  sudo usermod -aG docker $(whoami)
EOF
  elif [ "$(uname -s)" = "Linux" ] && command -v systemctl >/dev/null 2>&1; then
    blerg_step "Docker daemon isn't reachable — trying to start it (needs sudo)..."
    sudo systemctl start docker >/dev/null 2>&1 || true
    if ! docker info >/dev/null 2>&1; then
      fail_block <<'EOF'
Docker is installed, but the daemon isn't running, and starting it
automatically via sudo just now didn't work either.

Fix it yourself, then re-run ./blerg-up.sh:
  sudo systemctl start docker
EOF
    fi
  else
    fail_block <<'EOF'
Docker is installed, but the daemon isn't reachable. On macOS/Windows,
open Docker Desktop; on Linux, start the docker service.

Then re-run ./blerg-up.sh.
EOF
  fi
fi

# Informational only - never blocks. daemon/install.sh sees the marker and
# doesn't repeat the message.
engine_preflight .env
export BLERG_ENGINE_PREFLIGHT_DONE=1

if [ "$INSTALL_DAEMON" -eq 1 ]; then
  set +e
  ./daemon/install.sh preflight
  PREFLIGHT_RC=$?
  set -e
  if [ "$PREFLIGHT_RC" -eq 3 ] && wsl_without_systemd; then
    wsl_systemd_box
    exit 1
  fi
  if [ "$PREFLIGHT_RC" -eq 3 ]; then
    blerg_box "can't install the runner daemon" <<'EOF'
This account has no systemd --user session, and starting one
automatically via sudo just now didn't work either (declined, wrong
password, or no sudo).

Fix it yourself, then re-run ./blerg-up.sh:

  sudo loginctl enable-linger $(whoami)
  sudo systemctl start user@$(id -u).service

Or start the stack without the daemon service this run:

  ./blerg-up.sh --no-daemon
EOF
    exit 1
  fi
  if [ "$PREFLIGHT_RC" -eq 4 ]; then
    blerg_box "can't install the runner daemon" <<'EOF'
Your systemd --user session is running, but its D-Bus socket never
came up - a known WSL2 quirk. The fix needs an explicit restart of
that session, which kills everything ELSE running under it too (other
tmux sessions, background daemons, an in-progress Claude Code
session) - so this is not done automatically. If you're OK with that:

  sudo systemctl restart user@$(id -u).service

Then re-run ./blerg-up.sh. Or start the stack without the daemon
service this run:

  ./blerg-up.sh --no-daemon
EOF
    exit 1
  fi
fi

gen() { openssl rand -hex 32 2>/dev/null || head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# env_drop_keys KEY... — delete every `KEY=` line from .env, keep the rest.
# Deliberately not `sed -i`: GNU sed takes an optional suffix glued to -i,
# BSD/macOS sed takes a REQUIRED separate suffix argument, so
# `sed -i '<script>' .env` on a Mac swallows the script as the backup suffix
# and then fails parsing ".env" as the script. Instead: filter into a temp file
# in the same directory (same filesystem, so the rename is atomic), give it
# .env's enforced 0600, and rename it over .env.
env_drop_keys() {
  local tmp=".env.tmp.$$"
  awk -v keys="$*" '
    BEGIN { n = split(keys, k, " "); for (i = 1; i <= n; i++) drop[k[i]] = 1 }
    { eq = index($0, "="); if (eq > 1 && (substr($0, 1, eq - 1) in drop)) next; print }
  ' .env > "$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" .env
}

if [ ! -f .env ]; then
  blerg_step "No .env found — generating one with strong random secrets."
  RUNNER_KEY="$(gen)"   # board<->runner contract key (shared by both)
  cat > .env <<ENVEOF
# Auto-generated by blerg-up.sh with random secrets. Safe to delete + regenerate.
BLERG_BOARD_SERVICE_KEY=$(gen)
BLERG_RUNNER_KEY=${RUNNER_KEY}
BLERG_RUNNER_DAEMON_TOKEN=$(gen)
BLERG_CORE_REGISTER_KEY=$(gen)
BLERG_CORE_INTERNAL_KEY=$(gen)
BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32)
BLERG_BOARD_SECRET_KEY=$(openssl rand -base64 32)
ANTHROPIC_API_KEY=

# Host ports (docker-compose.yml reads these; change + re-run ./blerg-up.sh
# if a default collides with something else already running).
BLERG_PORT_CORE=8081
BLERG_PORT_BOARD=8082
BLERG_PORT_RUNNER=8083

# MCP connections and crons: the runner's gateway is set up by docker-compose.yml (on the
# blerg-sandbox network only, never a host port). Optional overrides, see .env.example:
# BLERG_CORE_MCP_ALLOW_HTTP_HOSTS=
# BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS=
# BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS=
# BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS=
ENVEOF
  blerg_ok "Wrote install/desktop/.env"
fi

# R4 (desktop-security I1): refuse a copied-but-unedited .env.example (its placeholder
# "change-me-*" values are accepted by docker compose verbatim) or any secret too short to be a
# real credential. check_secret/check_b64_key mirror contracts/secrets' own rules for the
# non-base64 and base64 (BLERG_CORE_LOCAL_KEY, 32 raw bytes) secrets respectively.
check_secret() { # $1 key; hex/opaque secrets: 16+ chars, not a placeholder (mirrors contracts/secrets.Check)
  local v vlower; v="$(grep "^$1=" .env | head -1 | cut -d= -f2-)"
  vlower="$(printf '%s' "$v" | tr '[:upper:]' '[:lower:]')"
  case "$vlower" in ""|change-me*|changeme*) return 1 ;; esac
  [ "${#v}" -ge 16 ]
}
check_b64_key() { local v; v="$(grep "^$1=" .env | head -1 | cut -d= -f2-)"; [ -n "$v" ] && [ "$(printf '%s' "$v" | base64 -d 2>/dev/null | wc -c)" -eq 32 ]; }
BAD=""
# BLERG_BOARD_SECRET_KEY encrypts each board's automation token at rest. An .env from before it
# existed (or a blank one copied from .env.example) has no value to lose, so generate one now —
# an existing value is NEVER replaced here: rotating it would make every stored token unreadable.
if ! grep -q '^BLERG_BOARD_SECRET_KEY=.' .env; then
  env_drop_keys BLERG_BOARD_SECRET_KEY
  echo "BLERG_BOARD_SECRET_KEY=$(openssl rand -base64 32)" >> .env
  blerg_ok "Generated BLERG_BOARD_SECRET_KEY (encrypts board automation tokens at rest — back it up with .env)"
fi
for k in BLERG_BOARD_SERVICE_KEY BLERG_RUNNER_KEY BLERG_RUNNER_DAEMON_TOKEN BLERG_CORE_REGISTER_KEY BLERG_CORE_INTERNAL_KEY; do check_secret "$k" || BAD="$BAD $k"; done
check_b64_key BLERG_CORE_LOCAL_KEY || BAD="$BAD BLERG_CORE_LOCAL_KEY"
check_b64_key BLERG_BOARD_SECRET_KEY || BAD="$BAD BLERG_BOARD_SECRET_KEY"
if [ -n "$BAD" ] && [ "$REGEN_SECRETS" -eq 1 ]; then
  for k in $BAD; do
    env_drop_keys "$k"
    if [ "$k" = BLERG_CORE_LOCAL_KEY ] || [ "$k" = BLERG_BOARD_SECRET_KEY ]; then echo "$k=$(openssl rand -base64 32)" >> .env; else echo "$k=$(gen)" >> .env; fi
  done
  blerg_ok "Regenerated:$BAD"; BAD=""
fi
if [ -n "$BAD" ]; then
  fail_block <<EOF
These secrets in install/desktop/.env are missing, placeholders, or too short:
 $BAD
Regenerate just those (everything else in .env is kept):
  ./blerg-up.sh --regen-secrets
EOF
fi

# Loaded here (not just left for docker-compose to interpolate) so this
# script's own health checks and printed instructions below track whatever
# ports are actually configured, including on a re-run against an existing
# .env someone hand-edited.
PORT_CORE="$(grep '^BLERG_PORT_CORE=' .env | cut -d= -f2)"
PORT_BOARD="$(grep '^BLERG_PORT_BOARD=' .env | cut -d= -f2)"
PORT_RUNNER="$(grep '^BLERG_PORT_RUNNER=' .env | cut -d= -f2)"
PORT_CORE="${PORT_CORE:-8081}"
PORT_BOARD="${PORT_BOARD:-8082}"
PORT_RUNNER="${PORT_RUNNER:-8083}"

# Refresh the two origin vars from whatever ports are actually configured,
# every run — including a re-run against an existing .env where someone
# hand-edited a BLERG_PORT_* value. This is the whole point (audit M9): a
# changed port must never silently break login by leaving a stale origin
# allowlist/audience map behind.
env_drop_keys BLERG_CORE_ALLOWED_RETURN_ORIGINS BLERG_CORE_ORIGIN_AUDIENCES
cat >> .env <<ENVEOF
BLERG_CORE_ALLOWED_RETURN_ORIGINS=http://localhost:${PORT_CORE},http://localhost:${PORT_BOARD},http://localhost:${PORT_RUNNER}
BLERG_CORE_ORIGIN_AUDIENCES=http://localhost:${PORT_CORE}=blerg-core,http://localhost:${PORT_BOARD}=blerg-board,http://localhost:${PORT_RUNNER}=blerg-runner
ENVEOF

blerg_step "Starting desktop stack (postgres, blerg-core, blerg-board, blerg-runner)..."
RUN_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker compose -f "$COMPOSE_FILE" up -d $BUILD_FLAG

wait_for() {
  local name="$1" url="$2" tries=60
  blerg_step "Waiting for $name ($url)..."
  for i in $(seq 1 "$tries"); do
    if curl -fsS -o /dev/null "$url" 2>/dev/null; then
      blerg_ok "$name"
      return 0
    fi
    sleep 2
  done
  blerg_fail "$name (timed out waiting for $url)"
  return 1
}

STATUS=0
wait_for "blerg-core"   "http://localhost:$PORT_CORE/healthz" || STATUS=1
wait_for "blerg-board"  "http://localhost:$PORT_BOARD/"        || STATUS=1
wait_for "blerg-runner" "http://localhost:$PORT_RUNNER/healthz" || STATUS=1

RUNNER_DIR="$(cd ../../runner && pwd)"
DAEMON_TOKEN="$(grep '^BLERG_RUNNER_DAEMON_TOKEN=' .env | cut -d= -f2)"
# Reflect whatever repos root was actually configured (interactively answered, or already in
# .env from a prior run) rather than a hard-coded guess — a box that prints the wrong directory
# is worse than no box at all.
REPOS_ROOT_CONFIGURED="$(grep '^BLERG_RUNNER_REPOS_ROOT=' .env | cut -d= -f2 || true)"
if [ -n "$REPOS_ROOT_CONFIGURED" ]; then
  REPOS_ROOT_LINE="export BLERG_RUNNER_REPOS_ROOT=$REPOS_ROOT_CONFIGURED"
else
  REPOS_ROOT_LINE="# set BLERG_RUNNER_REPOS_ROOT to your repos directory"
fi

DAEMON_UP=0
if [ "$INSTALL_DAEMON" -eq 1 ]; then
  blerg_step "Installing the runner daemon as a host service..."
  if ./daemon/install.sh install; then
    DAEMON_UP=1
  else
    blerg_fail "Daemon service install didn't work — falling back to manual steps (see above for why)."
  fi
fi

open_browser() {
  local url="$1" explorer=""
  if command -v explorer.exe >/dev/null 2>&1; then
    explorer="explorer.exe"
  elif [ -x /mnt/c/Windows/explorer.exe ]; then
    explorer="/mnt/c/Windows/explorer.exe"
  fi
  if command -v wslview >/dev/null 2>&1; then
    ( wslview "$url" >/dev/null 2>&1 & )
  elif [ -n "$explorer" ]; then
    # WSL2: hands off straight to Windows. Plain xdg-open/open exist inside
    # WSL but usually have no registered browser handler and silently no-op —
    # explorer.exe is the one that reliably works with no extra setup.
    # It routinely exits nonzero even on success, so never gate on its status.
    ( "$explorer" "$url" >/dev/null 2>&1 & )
  elif command -v xdg-open >/dev/null 2>&1; then
    ( xdg-open "$url" >/dev/null 2>&1 & )
  elif command -v open >/dev/null 2>&1; then
    ( open "$url" >/dev/null 2>&1 & )
  else
    return 1
  fi
}

# The bootstrap password is logged once, by the boot that creates the admin
# account. Only point at the log when THIS run's containers logged it; on a
# re-run (or if clocks disagree) point at --reset-admin instead, which always
# works.
if docker compose logs --since "$RUN_START" blerg-core 2>/dev/null | grep -q BLERG_BOOTSTRAP_ADMIN_PASSWORD; then
  ADMIN_HINT="$(cat <<HINT
   the login every component accepts. The bootstrap admin's username AND
   one-time password are in core's first-boot log:

     docker compose logs blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD

   (username = the provider_subject in that line; it is printed once, on the
   boot that created the account - if it's gone, run: $0 --reset-admin)
HINT
)"
else
  ADMIN_HINT="$(cat <<HINT
   the login every component accepts. Your admin account already exists (its
   one-time password was printed on the first run only). Forgot it, or never
   saw it? Mint a new one-time password:

     $0 --reset-admin
HINT
)"
fi

echo
if [ "$DAEMON_UP" -eq 1 ]; then
  blerg_box "blerg is up" <<EOF
1. Sign in at http://localhost:$PORT_CORE (click "Sign in"). blerg-core issues
$ADMIN_HINT

2. You'll be sent to /change-password - pick a real one, then sign in again.

3. The runner daemon is installed and running as a background service
   (check: ./daemon/install.sh status; DAEMON.md for logs/other platforms).
   Runner -> + New -> pick a repo -> Launch; that sheet defaults to the local
   sandbox. Board session / Run board also run in the local sandbox by
   default whenever the sandbox image is available. The sandbox has no git
   credentials: an agent can commit, but you push - see DAEMON.md.

This shell is done and about to exit - that's success. Go build something.
EOF
else
  blerg_box "do this next" <<EOF
1. Start the runner daemon (new terminal - it runs on this host, not
   in a container, so it can use your own claude CLI login and tmux;
   see DAEMON.md to install it as a persistent service instead):

     export BLERG_RUNNER_SERVER_URL=ws://localhost:$PORT_RUNNER/ws/daemon
     export BLERG_RUNNER_DAEMON_TOKEN=$DAEMON_TOKEN
     $REPOS_ROOT_LINE
     ( cd $RUNNER_DIR && GOWORK=off go run ./cmd/daemon )

   Not logged into claude yet? Run 'claude setup-token' first and
   export its output as CLAUDE_CODE_OAUTH_TOKEN before starting it.

2. Sign in at http://localhost:$PORT_CORE (click "Sign in"). blerg-core issues
$ADMIN_HINT

3. You'll be sent to /change-password - pick a real one, then sign in again.

That's it - the board can then claim cards and spawn real agent sessions.
The stack's running in the background; this shell is done and about to
exit - that's success, not a crash. Go start that daemon.
EOF
fi

if [ "$OPEN_BROWSER" -eq 1 ] && [ "$STATUS" -eq 0 ]; then
  open_browser "http://localhost:$PORT_CORE" || true
fi

if [ "$STATUS" -ne 0 ]; then
  blerg_fail "One or more services did not become healthy in time. Check:"
  blerg_fail "  docker compose -f $COMPOSE_FILE logs"
  exit 1
fi
