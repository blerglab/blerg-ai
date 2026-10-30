#!/usr/bin/env bash
# Pre-release only: see docs/session-e2e.md.
set -euo pipefail
exec python3 "$(dirname "$0")/e2e_sessions.py" "$@"
