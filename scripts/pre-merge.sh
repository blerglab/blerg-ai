#!/usr/bin/env bash
# The matrix includes mandatory tagged persistent-shell wire tests; --list shows both modes.
# Keep orchestration portable: Python's standard library owns logs and timing.
set -euo pipefail
exec python3 "$(dirname "$0")/pre_merge.py" "$@"
