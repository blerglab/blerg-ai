#!/usr/bin/env bash
set -euo pipefail
# Proves the link check rejects what it must and accepts what it may. Each
# case builds its own throwaway tree, so the result never depends on the repo.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
script="$(pwd)/scripts/check_links.sh"

# must_fail <name> <description>: the check must reject directory $tmp/<name>.
must_fail() {
  if BLERG_LINKS_ROOT="$tmp/$1" "$script" >/dev/null 2>&1; then
    echo "FAIL: check passed on $2"; exit 1
  fi
  echo "OK: check correctly rejected $2"
}
# must_pass <name> <description>: the check must accept directory $tmp/<name>.
must_pass() {
  if ! BLERG_LINKS_ROOT="$tmp/$1" "$script" >/dev/null 2>&1; then
    echo "FAIL: check rejected $2"; exit 1
  fi
  echo "OK: check correctly accepted $2"
}

mkdir -p "$tmp/clean/docs"
printf '%s\n' '# Title' '' '## Getting Started (Fast)' '' 'Text.' '## Twice' '## Twice' \
  > "$tmp/clean/docs/guide.md"
printf '%s\n' '[g](docs/guide.md)' '[a](docs/guide.md#getting-started-fast)' \
  '[d](docs/)' '[r](docs/guide.md#twice-1)' '[self](#intro)' '[ext](https://example.com/x)' \
  '`[code](nowhere.md)`' '' '```' '[fenced](nowhere.md)' '```' '' '# Intro' \
  > "$tmp/clean/README.md"
must_pass clean "valid file, directory, anchor and external links"

mkdir "$tmp/missing"
echo '[x](nope.md)' > "$tmp/missing/README.md"
must_fail missing "a link to a missing file"

mkdir "$tmp/anchor"
echo '# One' > "$tmp/anchor/a.md"
echo '[x](a.md#two)' > "$tmp/anchor/README.md"
must_fail anchor "a link to a missing heading"

mkdir "$tmp/stale"
echo '// see docs/super''powers/x' > "$tmp/stale/main.go"
must_fail stale "a mention of a deleted path in source"

echo "check_links_test: all cases passed"
