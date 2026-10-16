#!/usr/bin/env bash
# One golangci-lint version for CI and `make lint`, so a local pass means a CI pass.
#   scripts/check_lint_pin.sh            the Makefile and the CI workflow name the same version
#   scripts/check_lint_pin.sh <binary>   ... and <binary> is that version (used by `make lint-go`)
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
make_v=$(sed -n 's/^GOLANGCI_LINT_VERSION[[:space:]]*:\{0,1\}=[[:space:]]*//p' "$root/Makefile" | head -n1)
ci_v=$(sed -n 's/^[[:space:]]*version:[[:space:]]*\(v[0-9][0-9.]*\)[[:space:]]*$/\1/p' "$root/.github/workflows/ci.yml" | sort -u)
if [ -z "$make_v" ] || [ "$make_v" != "$ci_v" ]; then
  echo "golangci-lint pin differs: Makefile has '${make_v}', .github/workflows/ci.yml has '${ci_v}'." >&2
  exit 1
fi
[ $# -eq 0 ] && exit 0
have=$("$1" version 2>&1 | sed -n 's/^golangci-lint has version \([0-9][0-9.]*\).*/v\1/p')
if [ "$have" != "$make_v" ]; then
  echo "$1 is golangci-lint '${have:-unknown}', but CI pins $make_v. Install $make_v: https://golangci-lint.run/docs/welcome/install/" >&2
  exit 1
fi
