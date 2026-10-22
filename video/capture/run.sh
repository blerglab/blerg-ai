#!/usr/bin/env bash
# Record a short's takes against a freshly reset demo install, one take per device.
#   video/capture/run.sh <short> [desktop|phone|all]     (default: all)
# The demo is reset before every take, so each one starts from the same seeded state, with the
# short's scripted agent (agent.yaml) active.
# Takes land in video/out/takes/ (or $BLERG_VIDEO_OUT/takes/).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VIDEO="$(dirname "$HERE")"
short="${1:?usage: run.sh <short> [desktop|phone|all]}"
which="${2:-all}"
script="$VIDEO/shorts/$short/capture.mjs"
[ -f "$script" ] || { echo "no $script" >&2; exit 1; }
case "$which" in all) devices="desktop phone" ;; desktop|phone) devices="$which" ;; *) echo "device: desktop|phone|all" >&2; exit 2 ;; esac
for d in $devices; do
  echo "== $short / $d: resetting the demo"
  BLERG_DEMO_SHORT="$short" "$VIDEO/demo/demo.sh" reset >/dev/null
  echo "== $short / $d: recording"
  node "$script" "$d"
done
