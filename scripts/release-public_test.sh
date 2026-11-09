#!/usr/bin/env bash
set -euo pipefail
# Proves scripts/release-public.sh and scripts/public-pre-push.sh keep private
# history out of the public repository. Everything happens in throwaway
# repositories, so the result never depends on this one.
src="$(cd "$(dirname "$0")/.." && pwd)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset BLERG_SCRUB_EXTRA
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
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
cp -R "$src/contracts" contracts
printf '## How to upgrade\nDesktop: back up, then run the installer.\nKubernetes: back up, then deploy.\n' > notes.md
cp "$src"/scripts/{release-public.sh,public-pre-push.sh,scrub.sh} scripts/

# Private history: an early commit holds a value that must never be published.
echo "deploy host 192.168.7.7" > hosts.txt  # scrub:allow (test fixture)
git add -A && git commit -qm "private: homelab notes"
git rm -q hosts.txt
echo "updates: []" > .github/dependabot.yml
mkdir -p video/demo && echo "demo" > video/demo/demo.sh && echo "# Video" > video/README.md
echo "0.1.0" > VERSION
printf '# Changelog\n\n## [0.1.0] - 2026-01-01\n' > CHANGELOG.md
echo "hello" > app.txt
git add -A && git commit -qm "private: scrubbed"
git push -q origin main
git remote add public "$tmp/blerg-ai.git"

must_fail "a release with no public identity" scripts/release-public.sh v0.1.0 notes.md
git config blerg.publicName "Public Name"
git config blerg.publicEmail "1+public@users.noreply.github.com"

must_fail "a version VERSION does not name" scripts/release-public.sh v0.2.0 notes.md
echo dirty > app.txt
must_fail "a release from a dirty tree" scripts/release-public.sh v0.1.0 notes.md
git checkout -q app.txt

must_fail "a release without its own notes" scripts/release-public.sh v0.1.0 notes.md
mkdir -p core/internal/releasenotes/notes
printf '{"version":"0.1.0","title":"First release","items":["Start a workspace."]}\n' > core/internal/releasenotes/notes/0.1.0.json
git add -A && git commit -qm "private: release notes"
scripts/release-public.sh v0.1.0 notes.md >/dev/null
[ -z "$(git ls-remote "$tmp/blerg-ai.git")" ] || fail "dry run pushed something"
[ -z "$(git for-each-ref refs/blerg-releases/)" ] || fail "dry run saved a release ref"
ok "dry run pushes nothing"

must_fail "stable publication without candidate acceptance" scripts/release-public.sh --push v0.1.0 notes.md
[ -z "$(git ls-remote "$tmp/blerg-ai.git")" ] || fail "failed gate published something"
# Use the actual release CLI and hook. Only GitHub HTTP and remote identity
# discovery are faked; every fetch/push reaches a disposable local bare remote.
real_git=$(command -v git)
mkdir "$tmp/fakes"
export BLERG_TEST_REAL_GIT="$real_git"
cat > "$tmp/fakes/git" <<'GIT'
#!/usr/bin/env bash
if [ "$*" = "remote get-url --push public" ]; then
  echo https://github.com/example-org/example-repo.git
else
  exec "$BLERG_TEST_REAL_GIT" "$@"
fi
GIT
cat > "$tmp/fakes/gh" <<'GH'
#!/usr/bin/env python3
import json, subprocess, sys, pathlib
args = sys.argv[1:]
if args[:2] == ['release', 'create']:
    pathlib.Path('.git/fake-release').write_text(args[2])
    sys.exit(0)
path = args[1]
if '/actions/runs?' in path:
    import urllib.parse
    q = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
    runs = [dict(id=i+1, run_number=1, run_attempt=1, head_sha=q['head_sha'][0], head_branch=q['branch'][0], event='push', path='.github/workflows/'+w, status='completed', conclusion='success') for i,w in enumerate(['ci.yml','workspace-explorer.yml'])]
    print(json.dumps(dict(workflow_runs=runs)))
elif '/jobs?' in path:
    print(json.dumps(dict(jobs=[dict(name='descriptor-traversal ('+p+'-latest)',status='completed',conclusion='success') for p in ['ubuntu','macos']])))
elif '/releases/tags/' in path:
    tag = pathlib.Path('.git/fake-release').read_text()
    print(json.dumps(dict(tag_name=tag,draft=False,prerelease='-rc.' in tag)))
else:
    sys.exit('unexpected fake API call')
GH
chmod +x "$tmp/fakes/"*
export PATH="$tmp/fakes:$PATH"
git config blerg.publicRepository example-org/example-repo
seed_export() {
  local version="$1" tree
  scripts/release-public.sh --push "$version-rc.1" notes.md >"$tmp/rc-out" 2>&1 || { cat "$tmp/rc-out"; fail "RC push"; }
  # Candidates must leave main untouched.
  if [ "$version" = v0.1.0 ]; then
    ! git --git-dir="$tmp/blerg-ai.git" show-ref --verify --quiet refs/heads/main || fail "RC advanced main"
  fi
  tree=$(git rev-parse "refs/tags/$version-rc.1^{tree}")
  printf '{"owner_accepted":true,"candidate_tree":"%s","linux_desktop":"fixture","macos_desktop":"fixture","kubernetes":"fixture"}\n' "$tree" > "$tmp/evidence.json"
  scripts/release-public.sh --push --candidate "$version-rc.1" --evidence "$tmp/evidence.json" "$version" notes.md >"$tmp/stable-out" 2>&1 || { cat "$tmp/stable-out"; fail "stable push"; }
}
seed_export v0.1.0
pub() { git --git-dir="$tmp/blerg-ai.git" "$@"; }
[ "$(pub rev-list --count main)" = 1 ] || fail "first release is not a single commit"
[ "$(pub log -1 --format='%an <%ae>' main)" = "Public Name <1+public@users.noreply.github.com>" ] \
  || fail "first release has the wrong author"
pub cat-file -e main:app.txt || fail "first release lost app.txt"
! pub cat-file -e main:.github/dependabot.yml 2>/dev/null || fail "dependabot.yml was published"
! pub cat-file -e main:video 2>/dev/null || fail "the video/ folder was published"
[ "$(pub rev-parse v0.1.0)" = "$(pub rev-parse main)" ] || fail "v0.1.0 tag missing"
git merge-base --is-ancestor "$(pub rev-parse main)" HEAD || fail "release not merged back"
[ -f .github/dependabot.yml ] || fail "merge back dropped dependabot.yml"
[ -f video/demo/demo.sh ] || fail "merge back dropped the private video/ folder"
ok "first release is one commit, right author, tagged, merged back"

public_count=$(pub rev-list --count --all)
must_fail "pushing private main to the public repo" git push -q public main
must_fail "pushing a private commit to a public branch" git push -q public HEAD~1:refs/heads/leak
[ "$(pub rev-list --count --all)" = "$public_count" ] || fail "a refused push still landed"

# A dependency bump merged on the public side.
git clone -q "$tmp/blerg-ai.git" "$tmp/outside" 2>/dev/null
(cd "$tmp/outside" && echo "dep v2" > dep.txt && mkdir -p .github \
  && echo "updates: []" > .github/dependabot.yml && git add -A \
  && git commit -qm "bump dep; a file the public side had before it was excluded" \
  && git push -q origin main)

echo "feature" >> app.txt
echo "0.2.0" > VERSION
printf '{"version":"0.2.0","title":"Next release","items":["Keep working."]}\n' > core/internal/releasenotes/notes/0.2.0.json
git add core/internal/releasenotes/notes/0.2.0.json
printf '\n## [0.2.0] - 2026-02-01\n' >> CHANGELOG.md
git commit -qam "private: feature"
git fetch -q public
must_fail "a release that would undo a public merge" scripts/release-public.sh v0.2.0 notes.md

git merge -q --no-edit public/main
echo "peer at 10.9.9.9" > leak.txt  # scrub:allow (test fixture)
git add leak.txt && git commit -qm "private: oops"
must_fail "a release the scrub rejects" scripts/release-public.sh v0.2.0 notes.md
git rm -q leak.txt && git commit -qm "private: fix"

seed_export v0.2.0
[ "$(pub rev-list --count main)" = 3 ] || fail "second release is not one commit on top"
pub cat-file -e main:dep.txt || fail "second release undid the public bump"
grep -q feature <(pub show main:app.txt) || fail "second release lost the feature"
! pub cat-file -e main:.github/dependabot.yml 2>/dev/null || fail "second release kept dependabot.yml public"
[ -f .github/dependabot.yml ] || fail "merge back dropped dependabot.yml because the public side had it"
must_fail "re-releasing a published version" scripts/release-public.sh v0.2.0 notes.md
ok "second release keeps public merges and adds one commit"
