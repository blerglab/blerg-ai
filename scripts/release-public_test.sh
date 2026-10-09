#!/usr/bin/env bash
set -euo pipefail
# Proves scripts/release-public.sh and scripts/public-pre-push.sh keep private
# history out of the public repository. Everything happens in throwaway
# repositories, so the result never depends on this one.
src="$(cd "$(dirname "$0")/.." && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset BLERG_SCRUB_EXTRA
export GIT_CONFIG_NOSYSTEM=1 HOME="$tmp/home"
mkdir "$HOME"
git config --global user.name "Private Dev"
git config --global user.email "dev@private.invalid"
git config --global init.defaultBranch main

ok() { echo "OK: $1"; }
fail() { echo "FAIL: $1"; exit 1; }
# must_fail <description> <command...>: the command must exit non-zero.
must_fail() {
  local what="$1"; shift
  if "$@" >"$tmp/out" 2>&1; then cat "$tmp/out"; fail "$what was allowed"; fi
  ok "$what refused: $(grep -m1 -E "release-public:|public-pre-push:|scrub:" "$tmp/out")"
}

git init -q --bare "$tmp/private.git"
git init -q --bare "$tmp/blerg-ai.git"
git clone -q "$tmp/private.git" "$tmp/work" 2>/dev/null
cd "$tmp/work"
mkdir -p scripts .github
cp "$src"/scripts/{release-public.sh,public-pre-push.sh,scrub.sh} scripts/

# Private history: an early commit holds a value that must never be published.
echo "deploy host 192.168.7.7" > hosts.txt  # scrub:allow (test fixture)
git add -A && git commit -qm "private: homelab notes"
git rm -q hosts.txt
echo "updates: []" > .github/dependabot.yml
echo "0.1.0" > VERSION
printf '# Changelog\n\n## [0.1.0] - 2026-01-01\n' > CHANGELOG.md
echo "hello" > app.txt
git add -A && git commit -qm "private: scrubbed"
git push -q origin main
git remote add public "$tmp/blerg-ai.git"

must_fail "a release with no public identity" scripts/release-public.sh v0.1.0
git config blerg.publicName "Public Name"
git config blerg.publicEmail "1+public@users.noreply.github.com"

must_fail "a version VERSION does not name" scripts/release-public.sh v0.2.0
echo dirty > app.txt
must_fail "a release from a dirty tree" scripts/release-public.sh v0.1.0
git checkout -q app.txt

scripts/release-public.sh v0.1.0 >/dev/null
[ -z "$(git ls-remote "$tmp/blerg-ai.git")" ] || fail "dry run pushed something"
ok "dry run pushes nothing"

scripts/release-public.sh --push v0.1.0 >/dev/null
pub() { git --git-dir="$tmp/blerg-ai.git" "$@"; }
[ "$(pub rev-list --count main)" = 1 ] || fail "first release is not a single commit"
[ "$(pub log -1 --format='%an <%ae>' main)" = "Public Name <1+public@users.noreply.github.com>" ] \
  || fail "first release has the wrong author"
pub cat-file -e main:app.txt || fail "first release lost app.txt"
! pub cat-file -e main:.github/dependabot.yml 2>/dev/null || fail "dependabot.yml was published"
[ "$(pub rev-parse v0.1.0)" = "$(pub rev-parse main)" ] || fail "v0.1.0 tag missing"
git merge-base --is-ancestor "$(pub rev-parse main)" HEAD || fail "release not merged back"
[ -f .github/dependabot.yml ] || fail "merge back dropped dependabot.yml"
ok "first release is one commit, right author, tagged, merged back"

must_fail "pushing private main to the public repo" git push -q public main
must_fail "pushing a private commit to a public branch" git push -q public HEAD~1:refs/heads/leak
[ "$(pub rev-list --count --all)" = 1 ] || fail "a refused push still landed"

# A dependency bump merged on the public side.
git clone -q "$tmp/blerg-ai.git" "$tmp/outside" 2>/dev/null
(cd "$tmp/outside" && echo "dep v2" > dep.txt && mkdir -p .github \
  && echo "updates: []" > .github/dependabot.yml && git add -A \
  && git commit -qm "bump dep; a file the public side had before it was excluded" \
  && git push -q origin main)

echo "feature" >> app.txt
echo "0.2.0" > VERSION
printf '\n## [0.2.0] - 2026-02-01\n' >> CHANGELOG.md
git commit -qam "private: feature"
git fetch -q public
must_fail "a release that would undo a public merge" scripts/release-public.sh v0.2.0

git merge -q --no-edit public/main
echo "peer at 10.9.9.9" > leak.txt  # scrub:allow (test fixture)
git add leak.txt && git commit -qm "private: oops"
must_fail "a release the scrub rejects" scripts/release-public.sh v0.2.0
git rm -q leak.txt && git commit -qm "private: fix"

scripts/release-public.sh --push v0.2.0 >/dev/null
[ "$(pub rev-list --count main)" = 3 ] || fail "second release is not one commit on top"
pub cat-file -e main:dep.txt || fail "second release undid the public bump"
grep -q feature <(pub show main:app.txt) || fail "second release lost the feature"
! pub cat-file -e main:.github/dependabot.yml 2>/dev/null || fail "second release kept dependabot.yml public"
[ -f .github/dependabot.yml ] || fail "merge back dropped dependabot.yml because the public side had it"
must_fail "re-releasing a published version" scripts/release-public.sh v0.2.0
ok "second release keeps public merges and adds one commit"
