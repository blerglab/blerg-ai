#!/usr/bin/env bash
set -euo pipefail
root="${BLERG_SCRUB_ROOT:-.}"
# Forbidden patterns: private-network IPv4 addresses (the RFC1918 LAN ranges
# and the RFC6598 carrier-grade NAT range that overlay VPNs hand out) and an
# internal hostname suffix. Scoped to those ranges specifically (not any
# dotted-quad) so loopback, documentation and public IPs mentioned
# legitimately don't trip it. (Comments here deliberately avoid spelling out
# the actual forbidden strings, since this file is itself scanned.)
#
# Extra private patterns: set BLERG_SCRUB_EXTRA to an extended regular
# expression and it is added as one more alternation. Keep such patterns
# OUTSIDE the repository (your shell profile, a CI secret): naming a private
# value in a tracked file would itself leak it.
#
# Escape hatch: a line containing the marker "scrub:allow" is ignored. Use it
# only where a test genuinely needs a private-range address (for example a
# test that private addresses are refused); prefer the documentation ranges
# of RFC 5737 everywhere else.
oct='[0-9]{1,3}'
patterns='(^|[^0-9])10\.'"$oct"'\.'"$oct"'\.'"$oct"'([^0-9]|$)'
patterns+='|(^|[^0-9])172\.(1[6-9]|2[0-9]|3[01])\.'"$oct"'\.'"$oct"'([^0-9]|$)'
patterns+='|(^|[^0-9])192\.168\.'"$oct"'\.'"$oct"'([^0-9]|$)'
patterns+='|(^|[^0-9])100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.'"$oct"'\.'"$oct"'([^0-9]|$)'
patterns+='|blerg''lab\.com'
if [ -n "${BLERG_SCRUB_EXTRA:-}" ]; then
  patterns+="|${BLERG_SCRUB_EXTRA}"
fi

raw=$(mktemp)
hits=$(mktemp)
trap 'rm -f "$raw" "$hits"' EXIT

# Inside a work tree, scan what would be published: tracked files plus new
# files that are not ignored. Elsewhere (an export, a test directory) walk the
# tree, skipping build output. grep exits 1 for "no match" and xargs turns
# that into 123; both are fine here, so the verdict comes from whether any
# hit survives the allow filter.
list_files() {
  if (cd "$root" && command -v git >/dev/null \
      && [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" = true ]); then
    (cd "$root" && git ls-files -z -co --exclude-standard) \
      | while IFS= read -r -d '' f; do
          if [ -f "$root/$f" ] && [ "${f##*/}" != scrub_test.sh ]; then
            printf '%s\0' "$root/$f"
          fi
        done
  else
    find "$root" \
         \( -type d \( -name .git -o -name bin -o -name node_modules -o -name dist \
            -o -path '*/.claude/worktrees' \) -prune \) \
         -o \( -type f ! -name 'scrub_test.sh' -print0 \)
  fi
}
list_files | { xargs -0 -r grep -HInE -e "$patterns" -- 2>/dev/null || true; } >"$raw"
{ grep -v 'scrub:allow' "$raw" || true; } >"$hits"

if [ -s "$hits" ]; then
  echo "scrub: forbidden private values found:"; cat "$hits"; exit 1
fi
echo "scrub: clean"
