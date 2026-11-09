#!/bin/bash
# Points a "latest run" symlink at a release-perf evidence directory
# (Codex round 2 on BLG-453, Makefile:65): ln -sfn on a path that is a
# real directory (left over from before each run got its own timestamped
# evidence dir) creates the link *inside* it instead of replacing it. If
# something non-symlink already sits at the link path, move it aside to
# a timestamped name first, then link.
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: release-perf-latest-logs.sh <new-logs-dir> <symlink-path>" >&2
  exit 1
fi

new_dir=$1
link_path=$2

if [ -e "$link_path" ] && [ ! -L "$link_path" ]; then
  mv "$link_path" "${link_path}-stale-$(date +%Y%m%d-%H%M%S)"
fi

ln -sfn "$new_dir" "$link_path"
