#!/usr/bin/env bash
# A seeded Blerg for filming: its own ports, databases, daemon and state, separate from any real
# install on this machine. Nothing personal is on screen: a demo account, demo boards and cards,
# demo repositories and sessions whose agent is scripted (video/demo/fake_claude.py), so a
# capture is repeatable and makes no model calls.
#
#   demo.sh up       build what is missing, start everything, seed it (a running demo is reset)
#   demo.sh reset    back to the seeded state: empty databases, fresh repos, seeded again
#   demo.sh stop     stop the processes and containers, keep builds and data
#   demo.sh down     remove everything: processes, containers, volumes, images it built, state
#   demo.sh status   what is running
#   demo.sh env      the URLs, and where the demo login is written
#
# Modes (BLERG_DEMO_MODE):
#   native (default)  core, board and runner run as host binaries built from this checkout, against
#                     a Postgres container. Lightest: no image builds (a few hundred MB of state).
#   docker            the desktop compose stack (install/desktop/docker-compose.yml) under its own
#                     project name `blerg-demo`, with its own ports, volumes and sandbox network.
#                     It builds three images (`blerg-demo-*`); `down` removes them again.
# In both modes the runner daemon runs on the host, as in a real desktop install, with its own
# HOME, repos root and state, and the scripted engine first on its PATH. Linux (and WSL) only.
#
# Settings (environment): BLERG_DEMO_STATE (default ~/.local/state/blerg-demo), BLERG_DEMO_REPOS
# (default: a private mktemp folder under $TMPDIR, recorded in the state), BLERG_DEMO_SHORT (the
# short whose scripted agent runs; video/capture/run.sh sets it), BLERG_DEMO_USER (alex),
# BLERG_DEMO_PORT_CORE/BOARD/RUNNER/PG (28081/28082/28083/28432).
# Keep the ports below the kernel's ephemeral range (/proc/sys/net/ipv4/ip_local_port_range): a port
# inside it is sometimes held by an outgoing connection, and the server then cannot bind it.
#
# What it deletes: only what it made. The state folder and the repos root carry a .blerg-demo
# marker with this demo's id bound to the folder's own path and device:inode (and listed in the
# state's record), so a copied or moved marker never counts; a folder without a valid one, $HOME, /, a parent of $HOME, or a folder that is
# in or holds a git repository the demo did not create is refused. Under the repos root only the
# seeded repositories (video/demo/repos/*) are removed, and only when their .git carries the id.
# A process is signaled only when its environment carries the id (a pid file can outlive a reboot).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
STATE="$(realpath -m -- "${BLERG_DEMO_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/blerg-demo}")"
MODE="${BLERG_DEMO_MODE:-native}"
# The repos root is shown in every session header, so the default is a neutral, private mktemp
# folder (under $TMPDIR, not $HOME), chosen at the first `up` and recorded in the state.
REPOS=""
DEMO_ID=""
MARK=.blerg-demo
P_CORE="${BLERG_DEMO_PORT_CORE:-28081}"
P_BOARD="${BLERG_DEMO_PORT_BOARD:-28082}"
P_RUNNER="${BLERG_DEMO_PORT_RUNNER:-28083}"
P_PG="${BLERG_DEMO_PORT_PG:-28432}"
PROJECT=blerg-demo
PG_CONTAINER=blerg-demo-postgres
PG_VOLUME=blerg-demo-pgdata
CORE="http://localhost:$P_CORE"
BOARD="http://localhost:$P_BOARD"
RUNNER="http://localhost:$P_RUNNER"

say() { printf '\033[1m[demo]\033[0m %s\n' "$*"; }
die() { printf '\033[31m[demo]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- what the demo may delete
real() { realpath -m -- "$1"; }

# guard_path <dir>: refuse paths that can never be the demo's own folder.
guard_path() {
  local p home gitdir
  [ -n "$1" ] || die "refusing an empty path"
  p="$(real "$1")"; home="$(real "$HOME")"
  case "$p" in /|"$home") die "refusing $p: never a demo folder" ;; esac
  case "$home/" in "$p"/*) die "refusing $p: it contains your home folder" ;; esac
  local q="$p"; while [ ! -d "$q" ]; do q="$(dirname "$q")"; done  # nearest existing folder
  if git -C "$q" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    gitdir="$(git -C "$q" rev-parse --absolute-git-dir 2>/dev/null || true)"
    stamped "$(git -C "$q" rev-parse --show-toplevel 2>/dev/null)" "$gitdir/blerg-demo-id" \
      || die "refusing $p: it is inside a git repository"
  fi
}

# foreign_git <dir>: prints the first git repository under dir the demo did not create.
foreign_git() {
  local g
  while IFS= read -r g; do
    stamped "$(dirname "$g")" "$g/blerg-demo-id" || { echo "$g"; return 0; }
  done < <(find "$1" -xdev -name .git -prune -print 2>/dev/null)
  return 1
}

# A stamp is "<id> <realpath> <device:inode>" of the folder it was made for. The state folder's own
# marker is the root of trust; every other stamp is also listed in $STATE/owned.
ident() { stat -c '%d:%i' -- "$1" 2>/dev/null; }

# stamp <dir> <file>: bind this demo's id to dir (as it is now) in file, and record it.
stamp() {
  local p; p="$(real "$1")"
  printf '%s %s %s\n' "$DEMO_ID" "$p" "$(ident "$p")" > "$2"
  [ "$p" = "$STATE" ] || printf '%s %s\n' "$p" "$(ident "$p")" >> "$STATE/owned"
}

# stamped <dir> <file>: file holds this demo's stamp for exactly this folder (same path and inode).
stamped() {
  [ -n "$DEMO_ID" ] && [ -n "$1" ] && [ -d "$1" ] && [ -f "$2" ] || return 1
  local id path ino p
  read -r id path ino < "$2" || return 1
  p="$(real "$1")"
  [ "$id" = "$DEMO_ID" ] && [ "$path" = "$p" ] && [ "$ino" = "$(ident "$p")" ] || return 1
  [ "$p" = "$STATE" ] || grep -qxF "$p $ino" "$STATE/owned" 2>/dev/null
}

# owned <dir>: the folder carries this demo's marker, made for this very folder.
owned() { stamped "$1" "$1/$MARK"; }

# claim <dir>: make dir the demo's (new or empty), or accept it when it already carries the id.
claim() {
  guard_path "$1"
  if [ -e "$1" ]; then
    [ -d "$1" ] || die "refusing $1: not a folder"
    owned "$1" && return 0
    [ -z "$(ls -A "$1")" ] || die "refusing $1: it exists, is not empty and is not this demo's (no $MARK marker)"
  fi
  mkdir -p -m 700 "$1"
  stamp "$1" "$1/$MARK"
}

# remove_owned <dir>: rm -rf, only for a folder that passes every check above.
remove_owned() {
  local g
  [ -e "$1" ] || return 0
  guard_path "$1"
  owned "$1" || die "refusing to delete $1: it has no $MARK marker with this demo's id"
  if g="$(foreign_git "$1")"; then die "refusing to delete $1: it holds a git repository the demo did not create ($g)"; fi
  rm -rf -- "$1"
}

# load_state: the id and repos root of an existing demo state (DEMO_ID stays "" when there is none).
load_state() {
  DEMO_ID=""; REPOS="${BLERG_DEMO_REPOS:+$(real "$BLERG_DEMO_REPOS")}"
  if [ -f "$STATE/$MARK" ]; then
    read -r DEMO_ID _ < "$STATE/$MARK" || true
    stamped "$STATE" "$STATE/$MARK" \
      || die "refusing $STATE: its $MARK marker was made for another folder (copied or moved)"
    guard_path "$STATE"
    [ -n "$REPOS" ] || REPOS="$(cat "$STATE/repos.path" 2>/dev/null || true)"
  elif [ -e "$STATE" ] && [ -n "$(ls -A "$STATE" 2>/dev/null)" ]; then
    die "refusing $STATE: it is not empty and is not a demo state folder (no $MARK marker)"
  fi
}

# claim_state: create or accept the state folder and the repos root; record the repos root.
claim_state() {
  load_state
  [ -n "$DEMO_ID" ] || DEMO_ID="$(openssl rand -hex 12)"
  claim "$STATE"
  if [ -z "$REPOS" ]; then
    local tmp; tmp="$(real "$(mktemp -d "${TMPDIR:-/tmp}/blerg-demo.XXXXXXXX")")"
    stamp "$tmp" "$tmp/$MARK"
    REPOS="$tmp/repos"
  fi
  claim "$REPOS"
  printf '%s\n' "$REPOS" > "$STATE/repos.path"
}

# remove_repos: the seeded repositories only (each .git carries the id).
remove_repos() {
  [ -n "$REPOS" ] && [ -d "$REPOS" ] || return 0
  owned "$REPOS" || die "refusing to touch $REPOS: no $MARK marker with this demo's id"
  local src name
  for src in "$HERE"/repos/*/; do
    name="$(basename "$src")"
    [ -e "$REPOS/$name" ] || continue
    stamped "$REPOS/$name" "$REPOS/$name/.git/blerg-demo-id" \
      || die "refusing to delete $REPOS/$name: not a repository this demo seeded"
    rm -rf -- "${REPOS:?}/$name"
  done
}

compose() {
  docker compose -p "$PROJECT" --env-file "$STATE/demo.env" \
    -f "$REPO/install/desktop/docker-compose.yml" -f "$HERE/compose.demo.yml" "$@"
}

go_bin() { # the first Go 1.26 or newer: $GO, the PATH's, /usr/local/go, ~/sdk/go*
  local g v sdk
  local cands=("${GO:-}" "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go)
  while IFS= read -r sdk; do cands+=("$sdk"); done < <(compgen -G "$HOME/sdk/go*/bin/go" | sort -rV)
  for g in "${cands[@]}"; do
    [ -n "$g" ] && [ -x "$g" ] || continue
    v="$("$g" env GOVERSION 2>/dev/null | sed 's/^go//')"
    case "$v" in 1.2[6-9]*|1.[3-9][0-9]*) echo "$g"; return ;; esac
  done
  die "Go 1.26 or newer is needed to build the demo binaries (set GO=/path/to/go)"
}

disk_check() {
  # The images, Postgres and the Go/npm caches land on Docker's and this filesystem's disk.
  local avail
  avail="$(df -Pk "$STATE" 2>/dev/null | awk 'NR==2 {print $4}')"
  if [ -n "$avail" ] && [ "$avail" -lt $((4 * 1024 * 1024)) ]; then
    say "warning: less than 4 GB free under $STATE"
  fi
}

gen() { openssl rand -hex 32; }

write_env() {
  [ -f "$STATE/demo.env" ] && return
  (umask 077; cat > "$STATE/demo.env" <<EOF
# Generated by video/demo/demo.sh for the demo stack only. Throwaway secrets.
BLERG_BOARD_SERVICE_KEY=$(gen)
BLERG_RUNNER_KEY=$(gen)
BLERG_RUNNER_DAEMON_TOKEN=$(gen)
BLERG_CORE_REGISTER_KEY=$(gen)
BLERG_CORE_INTERNAL_KEY=$(gen)
BLERG_CORE_BOARD_MINT_KEY=$(gen)
BLERG_CORE_LOCAL_KEY=$(openssl rand -base64 32)
BLERG_BOARD_SECRET_KEY=$(openssl rand -base64 32)
BLERG_PORT_CORE=$P_CORE
BLERG_PORT_BOARD=$P_BOARD
BLERG_PORT_RUNNER=$P_RUNNER
BLERG_BIND_ADDR=127.0.0.1
BLERG_RUNNER_MODEL_CATALOG_URL=
BLERG_VERSION=demo
EOF
  )
}

envval() { grep "^$1=" "$STATE/demo.env" | head -1 | cut -d= -f2-; }

# ---------------------------------------------------------------- build
build_native() {
  local go; go="$(go_bin)"
  mkdir -p "$STATE/bin" "$STATE/root/bin" "$STATE/web"
  local commit; commit="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo dev)"
  if [ "${REBUILD:-0}" = 1 ] || [ ! -x "$STATE/bin/blerg-core" ]; then
    say "building the Go binaries ($commit)"
    (cd "$REPO/core" && GOWORK=off "$go" build -o "$STATE/bin/blerg-core" ./cmd/blerg-core)
    (cd "$REPO/board" && GOWORK=off "$go" build -o "$STATE/bin/blerg-board-server" ./cmd/blerg-board-server)
    (cd "$REPO/runner" && GOWORK=off "$go" build -ldflags "-X main.commit=$commit" -o "$STATE/bin/blerg-runner-server" ./cmd/server)
  fi
  build_daemon
  if [ "${REBUILD:-0}" = 1 ] || [ ! -f "$STATE/web/runner/index.html" ]; then
    [ -x "$REPO/node_modules/.bin/vite" ] || die "run npm ci at the repository root first (the web apps are built with its vite)"
    say "building the web apps for core on :$P_CORE"
    local d out
    for d in core/web board/web runner/frontend; do
      out="$STATE/web/${d%%/*}"
      (cd "$REPO/$d" && VITE_CORE_PORT="$P_CORE" "$REPO/node_modules/.bin/vite" build --logLevel error --outDir "$out" --emptyOutDir)
    done
  fi
}

build_daemon() {
  # The daemon derives the messaging CLI's location from its own path (<root>/bin/<daemon>,
  # <root>/runner/...), so it lives in a small root that points at this checkout's runner/.
  mkdir -p "$STATE/root/bin"
  ln -sfn "$REPO/runner" "$STATE/root/runner"
  if [ "${REBUILD:-0}" = 1 ] || [ ! -x "$STATE/root/bin/blerg-runner-daemon" ]; then
    local go; go="$(go_bin)"
    say "building the runner daemon"
    (cd "$REPO/runner" && GOWORK=off "$go" build -o "$STATE/root/bin/blerg-runner-daemon" ./cmd/daemon)
  fi
}

# ---------------------------------------------------------------- processes
# svc_env <name>: the service's environment, KEY=value lines on stdin, into a 0600 file, so secrets
# never go on a command line (where `ps` would show them).
svc_env() {
  mkdir -p -m 700 "$STATE/env"
  local f="$STATE/env/$1.env" line k v
  (umask 077; : > "$f")
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    k="${line%%=*}"; v="${line#*=}"  # values may end in "=" (base64)
    case "$v" in *"'"*) die "a value for $k holds a single quote" ;; esac
    printf "%s='%s'\n" "$k" "$v" >> "$f"
  done
}

# start_proc <name> <workdir> <command...>: runs the command with only the environment in
# $STATE/env/<name>.env plus BLERG_DEMO_ID (how stop_proc recognizes it), in its own session.
# setsid may fork, so the process records its own pid (the shell that then execs the command).
start_proc() {
  local name="$1" dir="$2"; shift 2
  mkdir -p "$STATE/logs" "$STATE/pids"
  rm -f "$STATE/pids/$name.pid"
  # shellcheck disable=SC2016  # the single-quoted script is for the inner sh
  (cd "$dir" && exec env -i BLERG_DEMO_ID="$DEMO_ID" PATH=/usr/bin:/bin setsid sh -c \
    'echo $$ > "$0"; set -a; . "$1"; set +a; shift; exec "$@"' \
    "$STATE/pids/$name.pid" "$STATE/env/$name.env" "$@") >>"$STATE/logs/$name.log" 2>&1 < /dev/null &
  for _ in $(seq 1 50); do [ -s "$STATE/pids/$name.pid" ] && return 0; sleep 0.1; done
}

# ours <pid>: the process is alive and was started by this demo (its environment carries our id).
ours() {
  [ -n "$DEMO_ID" ] && [ -r "/proc/$1/environ" ] || return 1
  tr '\0' '\n' < "/proc/$1/environ" 2>/dev/null | grep -qx "BLERG_DEMO_ID=$DEMO_ID"
}

stop_proc() {
  local f="$STATE/pids/$1.pid" pid pgid target
  [ -f "$f" ] || return 0
  pid="$(cat "$f")"
  case "$pid" in ''|*[!0-9]*) rm -f "$f"; return 0 ;; esac
  if ours "$pid"; then
    # The whole group only when the process leads it (start_proc's setsid), never another group.
    pgid="$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ')"
    target="$pid"; [ "$pgid" = "$pid" ] && target="-$pid"
    kill -TERM -- "$target" 2>/dev/null || true
    for _ in $(seq 1 50); do { ours "$pid" || [ -n "$(group_left "$pid")" ]; } || break; sleep 0.2; done
    if ours "$pid"; then kill -KILL -- "$target" 2>/dev/null || true; fi
    # Whatever is left of the group the demo created (the leader gone, a child that ignored TERM):
    # KILL each member that carries the demo's id; nothing else is touched.
    if [ "$target" = "-$pid" ]; then
      local m
      for m in $(group_left "$pid"); do kill -KILL "$m" 2>/dev/null || true; done
    fi
  elif kill -0 "$pid" 2>/dev/null; then
    say "not stopping pid $pid ($f): it is not a demo process (a stale pid file)"
  fi
  rm -f "$f"
}

# group_left <pgid>: live members of that process group that carry this demo's id.
group_left() {
  local m
  for m in $(ps -eo pid=,pgid= 2>/dev/null | awk -v g="$1" '$2 == g {print $1}'); do
    ours "$m" && echo "$m"
  done
}

running() { local f="$STATE/pids/$1.pid"; [ -f "$f" ] && ours "$(cat "$f")"; }

wait_http() { # url, name
  for _ in $(seq 1 120); do curl -fsS -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.5; done
  die "$2 did not come up ($1); see $STATE/logs/"
}

pg_up() {
  if ! docker container inspect "$PG_CONTAINER" >/dev/null 2>&1; then
    docker run -d --name "$PG_CONTAINER" --label blerg-demo=1 -e POSTGRES_USER=blerg -e POSTGRES_PASSWORD=blerg \
      -e POSTGRES_DB=blerg_core -p "127.0.0.1:$P_PG:5432" -v "$PG_VOLUME:/var/lib/postgresql/data" \
      -v "$REPO/install/desktop/init-multi-db.sh:/docker-entrypoint-initdb.d/init-multi-db.sh:ro" postgres:16 >/dev/null
  else
    docker start "$PG_CONTAINER" >/dev/null
  fi
  for _ in $(seq 1 60); do
    docker exec "$PG_CONTAINER" pg_isready -U blerg -d blerg_runner >/dev/null 2>&1 \
      && docker exec "$PG_CONTAINER" psql -U blerg -d blerg_runner -c 'select 1' >/dev/null 2>&1 && return 0
    sleep 1
  done
  die "Postgres did not come up"
}

services_native() {
  local db="postgres://blerg:blerg@127.0.0.1:$P_PG"
  mkdir -p "$STATE/data/runner" "$STATE/run/runner/frontend" "$STATE/home"
  ln -sfn "$STATE/web/runner" "$STATE/run/runner/frontend/dist"
  local base="HOME=$STATE/home
PATH=/usr/bin:/bin
LANG=C.UTF-8
TZ=UTC"
  svc_env core <<ENV
$base
DATABASE_URL=$db/blerg_core?sslmode=disable
BLERG_CORE_LISTEN=127.0.0.1:$P_CORE
BLERG_CORE_WEB_DIST=$STATE/web/core
BLERG_CORE_PUBLIC_URL=$CORE
BLERG_CORE_REGISTER_KEY=$(envval BLERG_CORE_REGISTER_KEY)
BLERG_CORE_INTERNAL_KEY=$(envval BLERG_CORE_INTERNAL_KEY)
BLERG_CORE_LOCAL_KEY=$(envval BLERG_CORE_LOCAL_KEY)
BLERG_CORE_BOARD_MINT_KEY=$(envval BLERG_CORE_BOARD_MINT_KEY)
BLERG_CORE_ALLOWED_RETURN_ORIGINS=$CORE,$BOARD,$RUNNER
BLERG_CORE_ORIGIN_AUDIENCES=$CORE=blerg-core,$BOARD=blerg-board,$RUNNER=blerg-runner
BLERG_CORE_COOKIE_SECURE=false
BLERG_SETUP_ALLOW_LOCAL=true
BLERG_SETUP_PUBLISHED_ADDR=127.0.0.1
BLERG_VERSION=demo
ENV
  start_proc core "$STATE" "$STATE/bin/blerg-core"
  wait_http "$CORE/healthz" core
  svc_env runner <<ENV
$base
BLERG_RUNNER_DATABASE_URL=$db/blerg_runner?sslmode=disable
BLERG_RUNNER_LISTEN_PORT=$P_RUNNER
BLERG_RUNNER_DAEMON_TOKEN=$(envval BLERG_RUNNER_DAEMON_TOKEN)
BLERG_RUNNER_KEY=$(envval BLERG_RUNNER_KEY)
BLERG_CORE_URL=$CORE
BLERG_CORE_REGISTER_KEY=$(envval BLERG_CORE_REGISTER_KEY)
BLERG_CORE_PUBLIC_URL=$CORE
BLERG_RUNNER_CORE_INTERNAL_KEY=$(envval BLERG_CORE_INTERNAL_KEY)
BLERG_RUNNER_SELF_URL=$RUNNER
BLERG_RUNNER_DATA_DIR=$STATE/data/runner
BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE=true
BLERG_RUNNER_MODEL_CATALOG_URL=
BLERG_RUNNER_BOARD_URL=$BOARD
ENV
  start_proc runner "$STATE/run/runner" "$STATE/bin/blerg-runner-server"
  wait_http "$RUNNER/healthz" runner
  svc_env board <<ENV
$base
DATABASE_URL=$db/blerg_board?sslmode=disable
BLERG_BOARD_ADDR=127.0.0.1:$P_BOARD
BLERG_BOARD_WEB_DIR=$STATE/web/board
BLERG_BOARD_SERVICE_KEY=$(envval BLERG_BOARD_SERVICE_KEY)
BLERG_BOARD_SECRET_KEY=$(envval BLERG_BOARD_SECRET_KEY)
BLERG_RUNNER_URL=$RUNNER
BLERG_RUNNER_KEY=$(envval BLERG_RUNNER_KEY)
BLERG_CORE_URL=$CORE
BLERG_CORE_REGISTER_KEY=$(envval BLERG_CORE_REGISTER_KEY)
BLERG_CORE_BOARD_MINT_KEY=$(envval BLERG_CORE_BOARD_MINT_KEY)
BLERG_CORE_PUBLIC_URL=$CORE
BLERG_BOARD_SELF_URL=$BOARD
BLERG_BOARD_PUBLIC_URL=$BOARD
BLERG_BOARD_AGENT_URL=$BOARD
RUNNER_UI_BASE=$RUNNER/sessions
BLERG_BOARD_RUNNER_RUNTIME=daemon
ENV
  start_proc board "$STATE" "$STATE/bin/blerg-board-server"
  wait_http "$BOARD/healthz" board
}

services_docker() {
  say "starting the compose project $PROJECT (builds its images the first time)"
  compose up -d --build
  wait_http "$CORE/healthz" core
  wait_http "$RUNNER/healthz" runner
  wait_http "$BOARD/healthz" board
}

daemon_up() {
  mkdir -p "$STATE/home" "$STATE/fakebin" "$STATE/tmux" "$STATE/fake"
  cat > "$STATE/fakebin/claude" <<SHIM
#!/bin/sh
# The scripted engine for the demo daemon (video/demo/fake_claude.py).
exec python3 "$HERE/fake_claude.py" --scenarios "$REPO/video/shorts" --seed-scenario "$HERE/seed-agent.yaml" \\
  --active-file "$STATE/active-short" "\$@"
SHIM
  chmod +x "$STATE/fakebin/claude"
  svc_env daemon <<ENV
HOME=$STATE/home
LANG=C.UTF-8
TZ=UTC
TERM=xterm-256color
PATH=$STATE/fakebin:$STATE/home/.local/bin:/usr/local/bin:/usr/bin:/bin
TMUX_TMPDIR=$STATE/tmux
BLERG_RUNNER_SERVER_URL=ws://localhost:$P_RUNNER/ws/daemon
BLERG_RUNNER_SERVER_HTTP=$RUNNER
BLERG_RUNNER_DAEMON_TOKEN=$(envval BLERG_RUNNER_DAEMON_TOKEN)
BLERG_RUNNER_REPOS_ROOT=$REPOS
BLERG_RUNNER_DAEMON_NAME=demo-laptop
BLERG_RUNNER_CLAUDE_USAGE=0
BLERG_DEMO_FAKE_LOG=$STATE/logs/fake-claude.log
BLERG_DEMO_FAKE_STATE=$STATE/fake
GIT_AUTHOR_NAME=Demo Agent
GIT_AUTHOR_EMAIL=agent@example.com
GIT_COMMITTER_NAME=Demo Agent
GIT_COMMITTER_EMAIL=agent@example.com
ENV
  start_proc daemon "$STATE/home" "$STATE/root/bin/blerg-runner-daemon"
}

seed_repos() {
  local src name dst
  for src in "$HERE"/repos/*/; do
    name="$(basename "$src")"
    dst="$REPOS/$name"
    [ ! -e "$dst" ] || die "$dst already exists (remove_repos runs first)"
    cp -r "$src" "$dst"
    git -C "$dst" init -q -b main && stamp "$dst" "$dst/.git/blerg-demo-id"
    (cd "$dst" && git add -A \
      && GIT_AUTHOR_NAME="Demo Dev" GIT_AUTHOR_EMAIL=dev@example.com GIT_COMMITTER_NAME="Demo Dev" \
         GIT_COMMITTER_EMAIL=dev@example.com GIT_AUTHOR_DATE="2026-01-05T10:00:00Z" GIT_COMMITTER_DATE="2026-01-05T10:00:00Z" \
         git -c commit.gpgsign=false commit -qm "Initial import" && git tag base)
  done
}

seed() {
  say "seeding (account, boards, cards, sessions)"
  BLERG_DEMO_CORE="$CORE" BLERG_DEMO_BOARD="$BOARD" BLERG_DEMO_RUNNER="$RUNNER" BLERG_DEMO_STATE="$STATE" \
    BLERG_DEMO_USER="${BLERG_DEMO_USER:-alex}" node "$HERE/seed.mjs"
}

# ---------------------------------------------------------------- commands
stop_all() {
  [ -n "$DEMO_ID" ] || return 0
  stop_proc daemon
  if [ "$MODE" = docker ]; then
    { [ -f "$STATE/demo.env" ] && compose stop >/dev/null 2>&1; } || true
  else
    stop_proc board; stop_proc runner; stop_proc core
    docker stop "$PG_CONTAINER" >/dev/null 2>&1 || true
  fi
}

wipe_data() {
  # Fixed names under the state folder, which load_state/claim_state verified as this demo's.
  { [ -n "$DEMO_ID" ] && owned "$STATE"; } || die "refusing to wipe $STATE: not this demo's state folder"
  rm -rf -- "${STATE:?}/home" "${STATE:?}/data" "${STATE:?}/fake" "${STATE:?}/tmux" "${STATE:?}/logs" "${STATE:?}/seed.json"
  remove_repos
  if [ "$MODE" = docker ]; then
    compose down -v --remove-orphans >/dev/null 2>&1 || true
  else
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    docker volume rm "$PG_VOLUME" >/dev/null 2>&1 || true
  fi
}

cmd_up() {
  command -v docker >/dev/null || die "docker is needed (Postgres runs in a container)"
  claim_state
  disk_check
  write_env
  if running daemon || running core || { [ "$MODE" = docker ] && compose ps -q 2>/dev/null | grep -q .; }; then
    say "already running: resetting"
  fi
  stop_all
  wipe_data
  if [ -n "${BLERG_DEMO_SHORT:-}" ]; then echo "$BLERG_DEMO_SHORT" > "$STATE/active-short"; else rm -f "$STATE/active-short"; fi
  local p
  for p in "$P_CORE" "$P_BOARD" "$P_RUNNER" "$P_PG"; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      die "port $p is taken by something else; pick others with BLERG_DEMO_PORT_CORE/BOARD/RUNNER/PG"
    fi
  done
  if [ "$MODE" = docker ]; then build_daemon; services_docker; else build_native; pg_up; services_native; fi
  seed_repos
  daemon_up
  seed
  cmd_env
}

cmd_down() {
  load_state
  if [ -z "$DEMO_ID" ]; then say "no demo state at $STATE: nothing to remove"; return 0; fi
  stop_all
  wipe_data
  if [ "$MODE" = docker ] || docker image ls -q "$PROJECT-*" 2>/dev/null | grep -q .; then
    { [ -f "$STATE/demo.env" ] && compose down -v --rmi local --remove-orphans >/dev/null 2>&1; } || true
    docker image ls --format '{{.Repository}}:{{.Tag}}' | grep "^$PROJECT-" | xargs -r docker rmi >/dev/null 2>&1 || true
  fi
  if [ -n "$REPOS" ] && [ -d "$REPOS" ] && owned "$REPOS"; then
    local parent; parent="$(dirname "$REPOS")"
    rm -f -- "$REPOS/$MARK"
    rmdir -- "$REPOS" 2>/dev/null || say "kept $REPOS: it holds files the demo did not make"
    if owned "$parent"; then rm -f -- "$parent/$MARK"; rmdir -- "$parent" 2>/dev/null || true; fi
  fi
  remove_owned "$STATE"
  say "removed the demo: processes, containers, volumes, images, the seeded repos and $STATE"
  say "(postgres:16 is shared with the desktop install and is kept)"
}

cmd_status() {
  local n
  for n in core runner board daemon; do
    if running "$n"; then echo "$n: running (pid $(cat "$STATE/pids/$n.pid"))"; else echo "$n: stopped"; fi
  done
  if [ "$MODE" = docker ]; then compose ps 2>/dev/null || true
  else docker ps --filter "name=$PG_CONTAINER" --format '{{.Names}}: {{.Status}}'; fi
}

cmd_env() {
  cat <<EOF
core    $CORE
board   $BOARD
runner  $RUNNER
repos   ${REPOS:-(none yet)}
login   $STATE/seed.json (demo account and ids; the password is a fixed demo one)
logs    $STATE/logs/
EOF
}

main() {
  case "${1:-}" in
    up|reset) cmd_up ;;
    stop) load_state; stop_all ;;
    down) cmd_down ;;
    status) load_state; cmd_status ;;
    env) load_state; cmd_env ;;
    *) sed -n '2,34p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  esac
}

# Sourced (demo_test.sh): functions only.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
