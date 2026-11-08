#!/usr/bin/env bash
# Tests for release-perf-latest-logs.sh (Codex round 2 on BLG-453,
# Makefile:65): the "latest run" symlink must survive an old real
# directory left at its path, and must correctly replace an existing
# symlink from a prior run — without make ever needing systemd-run or
# the real release-perf binary to exercise either case.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
SCRIPT="$PWD/release-perf-latest-logs.sh"
fail=0
t() { echo "FAIL: $*"; fail=1; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# A real leftover directory (not a symlink) at the link path: ln -sfn on
# this would create the link *inside* it instead of replacing it — the
# exact bug this script exists to avoid.
link="$tmp/latest"
mkdir -p "$link"
echo "stale run evidence" > "$link/report.md"
new1="$tmp/run-20261101-000000"
mkdir -p "$new1"

"$SCRIPT" "$new1" "$link"

if [ ! -L "$link" ]; then
  t "link path is not a symlink after running against a real leftover directory"
elif [ "$(readlink "$link")" != "$new1" ]; then
  t "symlink points to $(readlink "$link"), want $new1"
fi
stale=$(find "$tmp" -maxdepth 1 -name 'latest-stale-*' -type d)
if [ -z "$stale" ]; then
  t "the old real directory was not moved aside"
elif [ ! -f "$stale/report.md" ]; then
  t "the moved-aside directory lost its own content"
fi

# Running it again, now that the link path is itself a symlink (the
# normal repeat-run case): must replace it, not nest under it or inside
# whatever it points to.
new2="$tmp/run-20261102-000000"
mkdir -p "$new2"
"$SCRIPT" "$new2" "$link"
if [ ! -L "$link" ]; then
  t "link path is not a symlink after a second, normal run"
elif [ "$(readlink "$link")" != "$new2" ]; then
  t "symlink was not replaced to point at the second run's dir (got $(readlink "$link"))"
fi
if [ -e "$new1/latest" ]; then
  t "ln created the link inside the first run's own directory instead of replacing it"
fi

# No leftover at all (the very first run on a clean machine): must
# create the symlink, not require the path to already exist.
link2="$tmp/latest2"
new3="$tmp/run-20261103-000000"
mkdir -p "$new3"
"$SCRIPT" "$new3" "$link2"
if [ ! -L "$link2" ] || [ "$(readlink "$link2")" != "$new3" ]; then
  t "did not create the symlink when nothing existed at the link path yet"
fi

if [ "$fail" -eq 0 ]; then echo "ok"; fi
exit $fail
