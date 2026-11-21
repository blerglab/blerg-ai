#!/usr/bin/env bash
set -euo pipefail
# Publish the current HEAD of this (private) repository to the public
# repository as ONE commit, never its history.
#
#   scripts/release-public.sh vX.Y.Z notes.md  build the commit, print it, push nothing
#   scripts/release-public.sh --push vX.Y.Z notes.md  publish after exact-SHA CI,
#                                              create the release, then merge back
#
# The public commit has HEAD's files (minus the paths in `exclude` below) and
# the public main as its only parent, so private history cannot travel with
# it. Before building it the script requires:
#   - a clean work tree;
#   - the public main already merged into HEAD, so the release does not undo
#     anything merged publicly (dependabot, contributors);
#   - VERSION and a dated CHANGELOG heading that name the version;
#   - the release's own bounded notes file naming that same version;
#   - scripts/scrub.sh passing on exactly the files being published (set
#     BLERG_SCRUB_EXTRA in your environment for private values of your own).
#
# Configuration, in the local git config (never tracked):
#   blerg.publicRepository GitHub owner/repository (required for --push)
#   blerg.publicRemote   remote name of the public repo (default: public)
#   blerg.publicName     author name for public commits
#   blerg.publicEmail    author email for public commits
#
# --push also installs scripts/public-pre-push.sh as this clone's pre-push
# hook if no other pre-push hook is present; the hook refuses any push to the
# public repository that would carry private history.

# Files that stay private: dependency bumps run in the private repository and
# reach the public one with each release; the Claude Code plugin list is the
# maintainers' own workflow, not something to switch on for contributors; video/
# is the maintainers' demo and capture harness for marketing videos, not product.
# Entries may be folders.
exclude=(.github/dependabot.yml .claude/settings.json board/.claude/settings.json runner/.claude/settings.json video private)

push=false
candidate=""
evidence=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --push) push=true; shift ;;
    --candidate) candidate="${2:?candidate tag required}"; shift 2 ;;
    --evidence) evidence="${2:?acceptance evidence required}"; shift 2 ;;
    *) break ;;
  esac
done
version="${1:-}"
if ! [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$ ]]; then
  echo "usage: $0 [--push] vX.Y.Z[-rc.N] notes.md" >&2; exit 2
fi
bare="${version#v}"
bare="${bare%%-rc.*}"
release_notes="${2:-}"
[ -n "$release_notes" ] || { echo "release-public: supply Markdown notes with How to upgrade" >&2; exit 2; }
release_notes="$(cd "$(dirname "$release_notes")" && pwd)/$(basename "$release_notes")"

die() { echo "release-public: $*" >&2; exit 1; }

cd "$(git rev-parse --show-toplevel)"
remote=$(git config blerg.publicRemote || echo public)
repository=$(git config blerg.publicRepository || true)
go_bin="${GO:-go}"
# Build once, outside the published checkout. New release validation is Go.
release_tool=$(mktemp)
trap 'rm -f "$release_tool"' EXIT
(cd contracts && GOWORK=off "$go_bin" build -o "$release_tool" ./cmd/blerg-release)
"$release_tool" --tag "$version" --notes "$release_notes"
name=$(git config blerg.publicName) || die "set git config blerg.publicName"
email=$(git config blerg.publicEmail) || die "set git config blerg.publicEmail"
git remote get-url "$remote" >/dev/null 2>&1 || die "no remote '$remote' (git remote add $remote <url>)"

[ -z "$(git status --porcelain)" ] || die "work tree is not clean"
[ "$(tr -d '[:space:]' < VERSION)" = "$bare" ] || die "VERSION does not say $bare"
grep -qE "^## \[$bare\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$" CHANGELOG.md \
  || die "CHANGELOG.md has no '## [$bare] - YYYY-MM-DD' heading"

notes="core/internal/releasenotes/notes/$bare.json"
[ -f "$notes" ] || die "release $bare has no notes file ($notes)"
python3 - "$notes" "$bare" <<'PYNOTES' || die "release notes are invalid"
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
raw = path.read_bytes()
assert len(raw) <= 32 * 1024, "release notes exceed 32 KiB"
notes = json.loads(raw)
assert notes["version"] == sys.argv[2], "notes version does not match VERSION"
assert isinstance(notes["title"], str) and notes["title"].strip(), "notes need a title"
assert isinstance(notes["items"], list) and notes["items"], "notes need items"
assert all(isinstance(item, str) and item.strip() for item in notes["items"]), "notes items must be text"
PYNOTES

git fetch --quiet --tags "$remote"
parent=$(git rev-parse --verify --quiet "refs/remotes/$remote/main^{commit}" || true)
if [ -n "$parent" ]; then
  git merge-base --is-ancestor "$parent" HEAD \
    || die "$remote/main is not merged into HEAD; run: git merge $remote/main (a public main that predates this script shares no history: git merge -s ours --allow-unrelated-histories $remote/main)"
fi
if git ls-remote --exit-code --tags "$remote" "refs/tags/$version" >/dev/null; then
  die "$version is already tagged on $remote"
fi

# The published tree: HEAD's tree minus the excluded paths, built in a
# throwaway index so the real one is untouched.
index=$(mktemp); export_dir=$(mktemp -d)
trap 'rm -rf "$index" "$export_dir"; rm -f "$release_tool"' EXIT
GIT_INDEX_FILE="$index" git read-tree HEAD
GIT_INDEX_FILE="$index" git rm -r --cached --quiet --ignore-unmatch -- "${exclude[@]}"
tree=$(GIT_INDEX_FILE="$index" git write-tree)

if [ -z "$candidate" ] && [ -n "$parent" ] && [ "$tree" = "$(git rev-parse "$parent^{tree}")" ]; then
  die "nothing to release: $remote/main already has these files"
fi

git archive "$tree" | tar -x -C "$export_dir"
BLERG_SCRUB_ROOT="$export_dir" ./scripts/scrub.sh

commit=$(GIT_AUTHOR_NAME="$name" GIT_AUTHOR_EMAIL="$email" \
         GIT_COMMITTER_NAME="$name" GIT_COMMITTER_EMAIL="$email" \
         git commit-tree "$tree" ${parent:+-p "$parent"} -m "release: $version")

# Keep the public SHA stable across retries, including API failures after tag push.
record="refs/blerg-releases/$version"
previous=$(git rev-parse --verify --quiet "$record" || true)
if [ -n "$previous" ]; then
  [ "$(git show -s --format=%T "$previous")" = "$tree" ] && \
  [ "$(git show -s --format=%P "$previous")" = "$parent" ] \
    || die "saved release has a different tree or parent; use a new version"
  commit="$previous"
fi

echo "release-public: built $commit ($version) as $name <$email>"
if [ -n "$parent" ]; then
  git diff --stat "$parent" "$commit" | tail -1
else
  echo "first public commit: $(git ls-tree -r --name-only "$commit" | wc -l) files"
fi

if ! $push; then
  echo "dry run: nothing pushed. Inspect with: git show --stat $commit"
  exit 0
fi

hook="$(git rev-parse --git-path hooks)/pre-push"
if [ ! -e "$hook" ]; then
  ln -s "$PWD/scripts/public-pre-push.sh" "$hook"
elif [ "$(readlink -f "$hook")" != "$(readlink -f scripts/public-pre-push.sh)" ]; then
  die "a different pre-push hook is installed at $hook; chain scripts/public-pre-push.sh from it"
fi

promotion=()
if [[ "$version" != *-rc.* ]]; then
  [ -n "$candidate" ] && [ -n "$evidence" ] || die "stable publication needs --candidate vX.Y.Z-rc.N --evidence acceptance.json"
  promotion=(--candidate "$candidate" --evidence "$evidence")
fi
# Dry runs never reserve an identity. Persist only at publication.
[ -n "$previous" ] || git update-ref "$record" "$commit" ""
"$release_tool" --tag "$version" --notes "$release_notes" --sha "$commit" --parent "$parent" --remote "$remote" --repo "$repository" ${promotion[@]+"${promotion[@]}"}
git fetch --quiet "$remote"
# Record the release as merged without taking its tree: HEAD already has
# every file the release has, plus the excluded ones, which a plain merge
# would delete here once the public side has stopped carrying them. The
# first release shares no history with this repository; later ones do.
unrelated=()
[ -n "$parent" ] || unrelated=(--allow-unrelated-histories)
git merge -s ours --no-edit ${unrelated[@]+"${unrelated[@]}"} -m "Merge public release $version" "$commit"
echo "release-public: pushed $version to $remote and merged it back into $(git branch --show-current)"
