#!/usr/bin/env bash
# Tests for video/demo/demo.sh's safety rules, without Docker or a running demo:
#   - it deletes only folders it made (marker + id), never $HOME, /, a parent of $HOME, a foreign
#     git repository, or a non-empty folder without its marker;
#   - it removes only the seeded repositories under the repos root;
#   - it signals only processes whose environment carries the demo's id (a stale pid file is ignored).
#   bash video/demo/demo_test.sh
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
pass=0 fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }

# run <cmd...>: in a subshell with demo.sh sourced (its die() exits only the subshell).
run() { ( export HOME="$T/home"; BLERG_DEMO_STATE="${STATE_UNDER_TEST:-$T/state}" BLERG_DEMO_REPOS="${REPOS_UNDER_TEST:-}"
          export BLERG_DEMO_STATE BLERG_DEMO_REPOS
          # shellcheck source=demo.sh
          . "$HERE/demo.sh"; set +e; "$@" ) >"$T/out" 2>&1; }
expect_refused() { # name, cmd...
  local name="$1"; shift
  if run "$@"; then bad "$name (was allowed)"; else grep -q refusing "$T/out" && ok "$name" || bad "$name ($(cat "$T/out"))"; fi
}
mkdir -p "$T/home"

# 1. A non-demo folder with files is never claimed or deleted.
mkdir -p "$T/precious"; echo keep > "$T/precious/file"
STATE_UNDER_TEST="$T/precious" expect_refused "a non-empty folder without the marker is refused as state" claim_state
STATE_UNDER_TEST="$T/precious" expect_refused "down refuses a non-demo state folder" cmd_down
[ -f "$T/precious/file" ] && ok "the non-demo folder's file survives" || bad "the non-demo folder's file survives"

# 2. $HOME, / and parents of $HOME are refused, as state and as repos root.
STATE_UNDER_TEST="$T/home" expect_refused "HOME as state is refused" claim_state
STATE_UNDER_TEST="/" expect_refused "/ as state is refused" claim_state
STATE_UNDER_TEST="$T" expect_refused "a parent of HOME as state is refused" claim_state
REPOS_UNDER_TEST="$T/home" expect_refused "HOME as repos root is refused" claim_state
rm -rf "$T/state"

# 3. A folder inside a git repository is refused.
mkdir -p "$T/work" && git -C "$T/work" init -q
REPOS_UNDER_TEST="$T/work/repos" expect_refused "a repos root inside a git repository is refused" claim_state
rm -rf "$T/state"

# 4. A demo state folder is claimed, and down removes it and only the seeded repos.
if run claim_state; then ok "a new state folder and repos root are claimed"; else bad "claim ($(cat "$T/out"))"; fi
REPOS="$(cat "$T/state/repos.path")"
read -r id _ < "$T/state/.blerg-demo"
read -r rid rpath rino < "$REPOS/.blerg-demo"
[ "$rid" = "$id" ] && [ "$rpath" = "$REPOS" ] && [ "$rino" = "$(stat -c %d:%i "$REPOS")" ] \
  && ok "the repos root's marker binds the id to its path and inode" || bad "the repos root's marker binds the id to its path and inode"
case "$REPOS" in "$T/home"*) bad "the default repos root is outside HOME" ;; *) ok "the default repos root is outside HOME" ;; esac
[ "$(stat -c %a "$(dirname "$REPOS")")" = 700 ] && ok "the default repos root is private (0700)" || bad "the default repos root is private"
if run bash -c ': ; exit 0' && ( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; seed_repos ) >"$T/out" 2>&1; then
  ok "the seeded repos are created"; else bad "seed_repos ($(cat "$T/out"))"; fi
mkdir -p "$REPOS/mine" && echo x > "$REPOS/mine/notes"
if ( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_repos ) >"$T/out" 2>&1; then
  [ ! -e "$REPOS/acme-photos" ] && [ -f "$REPOS/mine/notes" ] && ok "remove_repos removes the seeded repos only" || bad "remove_repos removes the seeded repos only"
else bad "remove_repos ($(cat "$T/out"))"; fi
# A seeded-name repository that the demo did not create is refused.
mkdir -p "$REPOS/acme-shop" && git -C "$REPOS/acme-shop" init -q
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_repos ) >"$T/out" 2>&1 \
  && bad "a foreign repo with a seeded name is refused" || { grep -q refusing "$T/out" && ok "a foreign repo with a seeded name is refused" || bad "foreign repo ($(cat "$T/out"))"; }
[ -d "$REPOS/acme-shop/.git" ] && ok "the foreign repo survives" || bad "the foreign repo survives"
rm -rf "$REPOS/acme-shop"
# A foreign git repository inside the state folder stops its deletion.
mkdir -p "$T/state/x" && git -C "$T/state/x" init -q
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_owned "$STATE" ) >"$T/out" 2>&1 \
  && bad "a state folder holding a foreign repo is not deleted" || ok "a state folder holding a foreign repo is not deleted"
[ -d "$T/state/x/.git" ] && ok "the repo inside survives" || bad "the repo inside survives"
rm -rf "$T/state/x"

# A folder without the marker is never deleted, even by a valid demo.
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_owned "$T/precious" ) >"$T/out" 2>&1 \
  && bad "remove_owned refuses a folder without the marker" || ok "remove_owned refuses a folder without the marker"
[ -f "$T/precious/file" ] && ok "and its file survives" || bad "and its file survives"

# A copied marker never makes another folder the demo's: not as state, not for remove_owned.
mkdir -p "$T/copy"; echo data > "$T/copy/data.txt"; cp "$T/state/.blerg-demo" "$T/copy/.blerg-demo"
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_owned "$T/copy" ) >"$T/out" 2>&1 \
  && bad "a copied marker is refused by remove_owned" || ok "a copied marker is refused by remove_owned"
[ -f "$T/copy/data.txt" ] && ok "the folder with the copied marker keeps its data" || bad "the folder with the copied marker keeps its data"
cp "$REPOS/.blerg-demo" "$T/copy/.blerg-demo"
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_owned "$T/copy" ) >"$T/out" 2>&1 \
  && bad "a marker copied from the repos root is refused" || ok "a marker copied from the repos root is refused"
STATE_UNDER_TEST="$T/copy" expect_refused "a copied state marker does not make a state folder" cmd_down
[ -f "$T/copy/data.txt" ] && ok "and the data survives down" || bad "and the data survives down"
# The same folder recreated (new inode) at the marker's path is not the demo's either.
mkdir -p "$T/moved"; ( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; claim "$T/moved" ) >/dev/null 2>&1
mv "$T/moved" "$T/moved.old"; mkdir "$T/moved"; echo data > "$T/moved/data.txt"; cp "$T/moved.old/.blerg-demo" "$T/moved/"
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; remove_owned "$T/moved" ) >"$T/out" 2>&1 \
  && bad "a folder recreated at a recorded path (new inode) is refused" || ok "a folder recreated at a recorded path (new inode) is refused"
rm -rf "$T/copy" "$T/moved" "$T/moved.old"

# A relative state path is resolved to an absolute one before it is recorded.
( cd "$T" && export HOME="$T/home" BLERG_DEMO_STATE="rel-state"; . "$HERE/demo.sh"; echo "$STATE" ) > "$T/out" 2>&1
[ "$(cat "$T/out")" = "$T/rel-state" ] && ok "a relative BLERG_DEMO_STATE becomes absolute" || bad "relative state ($(cat "$T/out"))"
( cd "$T" && export HOME="$T/home" BLERG_DEMO_STATE="$T/state" BLERG_DEMO_REPOS="rel-repos"; . "$HERE/demo.sh"; load_state; echo "$REPOS" ) > "$T/out" 2>&1
[ "$(cat "$T/out")" = "$T/rel-repos" ] && ok "a relative BLERG_DEMO_REPOS becomes absolute" || bad "relative repos ($(cat "$T/out"))"

# 5. stop_proc signals only a process carrying the demo's id.
mkdir -p "$T/state/pids"
sleep 300 & other=$!
echo "$other" > "$T/state/pids/core.pid"
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; stop_proc core ) >"$T/out" 2>&1
if kill -0 "$other" 2>/dev/null; then ok "a stale pid file does not stop an unrelated process"; else bad "a stale pid file stopped an unrelated process"; fi
[ ! -f "$T/state/pids/core.pid" ] && ok "the stale pid file is dropped" || bad "the stale pid file is dropped"
kill "$other" 2>/dev/null; wait "$other" 2>/dev/null

( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state
  printf 'HOME=%s\nKEY=YWJjZA==\n' "$T/home" | svc_env sleeper; start_proc sleeper "$T" sleep 300 ) >"$T/out" 2>&1
pid="$(cat "$T/state/pids/sleeper.pid" 2>/dev/null)"
[ "$(stat -c %a "$T/state/env/sleeper.env")" = 600 ] && ok "the service env file is 0600" || bad "the service env file is 0600"
grep -qx "KEY='YWJjZA=='" "$T/state/env/sleeper.env" && ok "a value ending in = is kept whole" || bad "a value ending in = is kept whole"
[ -n "$pid" ] && tr '\0' '\n' < "/proc/$pid/environ" | grep -qx 'KEY=YWJjZA==' && ok "the process gets the env file's values" || bad "the process gets the env file's values"
if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && ! tr '\0' ' ' < "/proc/$pid/cmdline" | grep -q HOME=; then
  ok "a demo process runs with its environment off the command line"; else bad "start_proc ($(cat "$T/out"))"; fi
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; stop_proc sleeper ) >"$T/out" 2>&1
sleep 0.3
if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then ok "a demo process is stopped"; else bad "a demo process is stopped"; kill "$pid" 2>/dev/null; fi

# A child that ignores TERM is not left behind when the leader exits.
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state
  printf 'HOME=%s\n' "$T/home" | svc_env group
  start_proc group "$T" sh -c '(trap "" TERM; exec sleep 302) & exec sleep 303' ) >"$T/out" 2>&1
sleep 0.5
gpid="$(cat "$T/state/pids/group.pid" 2>/dev/null)"
child="$(ps -eo pid=,pgid=,args= | awk -v g="$gpid" '$2 == g && /sleep 302/ {print $1}')"
( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; load_state; stop_proc group ) >"$T/out" 2>&1
sleep 0.3
if [ -n "$child" ] && ! kill -0 "$child" 2>/dev/null && ! kill -0 "$gpid" 2>/dev/null; then
  ok "stop_proc KILLs group members left after TERM"; else bad "group leftovers (child=$child)"; kill -9 "$child" "$gpid" 2>/dev/null; fi

# 6. down removes the demo's own folders.
if ( export HOME="$T/home" BLERG_DEMO_STATE="$T/state"; . "$HERE/demo.sh"; MODE=native
     docker() { return 0; }; load_state; stop_all; wipe_data
     rm -f -- "$REPOS/$MARK"; rmdir -- "$REPOS" 2>/dev/null; remove_owned "$STATE" ) >"$T/out" 2>&1; then
  [ ! -e "$T/state" ] && ok "the demo's state folder is removed" || bad "the demo's state folder is removed"
else bad "down ($(cat "$T/out"))"; fi
[ -d "$REPOS/mine" ] && ok "files the demo did not make in the repos root are kept" || bad "files kept"

echo "demo_test: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
