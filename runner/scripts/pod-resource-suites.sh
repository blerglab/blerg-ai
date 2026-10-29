#!/usr/bin/env bash
# Called by TestPodToolResourcesReachChildProcess in an owned 1-CPU/4Gi
# Docker fixture after the production pod environment initializer has run.
set -euo pipefail
resource_evidence() {
  if [ -r /sys/fs/cgroup/memory.events ]; then
    cat /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max
    [ ! -r /sys/fs/cgroup/memory.peak ] || cat /sys/fs/cgroup/memory.peak
    cat /sys/fs/cgroup/memory.events
    awk '$1 == "oom_kill" && $2 != 0 { bad=1 } END { exit bad }' /sys/fs/cgroup/memory.events
  elif [ -r /sys/fs/cgroup/memory/memory.oom_control ]; then
    cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us /sys/fs/cgroup/cpu/cpu.cfs_period_us
    cat /sys/fs/cgroup/memory/memory.limit_in_bytes /sys/fs/cgroup/memory/memory.max_usage_in_bytes
    cat /sys/fs/cgroup/memory/memory.oom_control
    awk '$1 == "oom_kill" && $2 != 0 { bad=1 } END { exit bad }' /sys/fs/cgroup/memory/memory.oom_control
  else
    echo "No cgroup memory evidence available" >&2; return 1
  fi
}
trap 'suite_status=$?; resource_evidence || suite_status=1; exit "$suite_status"' EXIT
if [ -r /sys/fs/cgroup/memory.max ]; then
  memory_limit="$(cat /sys/fs/cgroup/memory.max)"
  read -r quota period < /sys/fs/cgroup/cpu.max
else
  memory_limit="$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes)"
  quota="$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us)"
  period="$(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us)"
fi
[ "$memory_limit" = 4294967296 ] && [ "$quota" = "$period" ] || {
  echo "Expected an enforced 1-CPU/4Gi cgroup" >&2; exit 1;
}
unset BLERG_POD_RESOURCE_SUITE BLERG_POD_RESOURCE_SCRIPT
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[ "${BLERG_TOOL_WORKERS:-}" = 1 ] && [ "${GOMAXPROCS:-}" = 1 ] || {
  echo "Expected the 1-CPU/4Gi pod resource budget" >&2; exit 1;
}
node -e 'const v8 = require("node:v8"); if (v8.getHeapStatistics().heap_size_limit > 1200 * 1024 * 1024) throw Error("Node heap exceeds pod budget")'
phase="${BLERG_POD_RESOURCE_PHASE:-all}"
case "$phase" in all|frontend|go) ;; *) echo "Unknown resource suite phase" >&2; exit 1 ;; esac
if [ "$phase" != go ]; then
  cd "$repo_root/runner/frontend"
  npm test -- --configLoader=runner
fi
if [ "$phase" != frontend ]; then
  cd "$repo_root/runner"
  go test -p 1 -timeout 45m ./internal/runner ./internal/daemon ./internal/server
fi
