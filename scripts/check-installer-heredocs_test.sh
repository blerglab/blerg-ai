#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# Generate text rather than embed a broken shell construct in this test.
for layout in same multiline nested; do
  for quote in "'" '"'; do
    if [ "$layout" = same ]; then
      printf 'text="$(cat <<END\n' > "$tmp/bad.sh"
    elif [ "$layout" = nested ]; then
      printf 'text="$(\ninner="$(true)"\ncat <<END\n' > "$tmp/bad.sh"
    else
      printf 'text="$(\ncat <<END\n' > "$tmp/bad.sh"
    fi
    printf 'an odd %s quote\nEND\n)"\n' "$quote" >> "$tmp/bad.sh"
    if bash scripts/check-installer-heredocs.sh "$tmp/bad.sh" > "$tmp/out"; then
      echo "FAIL: accepted $layout odd quote"; exit 1
    fi
    grep -q 'odd quote count' "$tmp/out"
  done
done
printf 'text="$(cat <<END\nbalanced '\''quotes'\'' and "quotes"\nEND\n)"\n' > "$tmp/good.sh"
bash scripts/check-installer-heredocs.sh "$tmp/good.sh"
printf 'cat <<END\nan odd '\'' quote outside a substitution\nEND\n' > "$tmp/good.sh"
bash scripts/check-installer-heredocs.sh "$tmp/good.sh"
echo 'PASS: heredoc guard fixtures (6 rejected, 2 accepted)'
