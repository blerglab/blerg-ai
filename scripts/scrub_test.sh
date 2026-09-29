#!/usr/bin/env bash
set -euo pipefail
# Proves the scrub rejects what it must and accepts what it may. Each case
# scans its own throwaway directory, so the result never depends on the repo.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset BLERG_SCRUB_EXTRA

# must_fail <name> <description>: the scrub must reject directory $tmp/<name>.
must_fail() {
  if BLERG_SCRUB_ROOT="$tmp/$1" ./scripts/scrub.sh >/dev/null; then
    echo "FAIL: scrub passed on $2"; exit 1
  fi
  echo "OK: scrub correctly rejected $2"
}
# must_pass <name> <description>: the scrub must accept directory $tmp/<name>.
must_pass() {
  if ! BLERG_SCRUB_ROOT="$tmp/$1" ./scripts/scrub.sh >/dev/null; then
    echo "FAIL: scrub rejected $2"; exit 1
  fi
  echo "OK: scrub correctly accepted $2"
}

mkdir "$tmp/lan"
echo "server at 10.0.0.1" > "$tmp/lan/leak.txt"
must_fail lan "a LAN IP"

mkdir "$tmp/cgnat"
echo "peer at 100.101.102.103" > "$tmp/cgnat/leak.txt"
must_fail cgnat "an overlay-VPN (CGNAT range) IP"

# Just outside the CGNAT range on both sides, plus documentation addresses.
mkdir "$tmp/public"
printf '%s\n' "100.63.0.1" "100.128.0.1" "192.0.2.7" "198.51.100.7" "203.0.113.7" \
  > "$tmp/public/ok.txt"
must_pass public "public and documentation IPs"

mkdir "$tmp/allow"
echo 'refuse("10.0.0.1") // scrub:allow' > "$tmp/allow/fixture.txt"
must_pass allow "a line carrying the allow marker"

# The marker excuses its own line only.
mkdir "$tmp/allowline"
printf '%s\n' 'refuse("10.0.0.1") // scrub:allow' 'server at 10.0.0.2' \
  > "$tmp/allowline/fixture.txt"
must_fail allowline "an unmarked line next to a marked one"

mkdir "$tmp/extra"
echo "made by examplecorp" > "$tmp/extra/note.txt"
must_pass extra "an extra-pattern word when no extra pattern is set"
export BLERG_SCRUB_EXTRA='[Ee]xample[Cc]orp'
must_fail extra "a word matching BLERG_SCRUB_EXTRA"
must_pass public "clean files while BLERG_SCRUB_EXTRA is set"
unset BLERG_SCRUB_EXTRA

echo "scrub_test: all cases passed"
