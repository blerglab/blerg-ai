#!/usr/bin/env bash
set -euo pipefail
# pre-push hook for the private repository: refuses any push to the public
# repository that would carry private history. scripts/release-public.sh
# installs it; to install by hand:
#   ln -s ../../scripts/public-pre-push.sh .git/hooks/pre-push
#
# A push counts as public when its remote is blerg.publicRemote (default:
# public) or its URL names the public repository. Every commit it would send
# must then be a single-parent or root commit whose parent the public
# repository already has, and none may be reachable from the private remote
# (blerg.privateRemote, default: origin). That is exactly the shape
# release-public.sh produces; anything else is private history.

remote_name="$1"
remote_url="$2"
public_remote=$(git config blerg.publicRemote || echo public)
private_remote=$(git config blerg.privateRemote || echo origin)

case "$remote_url" in
  */blerg-ai|*/blerg-ai.git|*/blerg-ai/) ;;
  *) [ "$remote_name" = "$public_remote" ] || exit 0 ;;
esac

zero=0000000000000000000000000000000000000000
# The URL as configured may carry a credential; never echo that part.
shown_url=$(printf '%s' "$remote_url" | sed -E 's|^([a-z]+://)[^/@]*@|\1|')
refuse() { echo "public-pre-push: refusing push to $shown_url: $*" >&2; exit 1; }

while read -r local_ref local_sha remote_ref remote_sha; do
  [ "$local_sha" = "$zero" ] && continue  # a deletion sends no commits
  known=(--remotes="$remote_name")
  [ "$remote_sha" = "$zero" ] || known+=("$remote_sha")
  # Commits this ref would send that the public side does not already have.
  for c in $(git rev-list "$local_sha" --not "${known[@]}"); do
    if [ -n "$(git for-each-ref --contains "$c" "refs/remotes/$private_remote")" ]; then
      refuse "$local_ref includes $c from the private repository"
    fi
    for p in $(git rev-list --parents -n1 "$c" | cut -d' ' -f2-); do
      [ "$p" = "$c" ] && continue
      if [ -n "$(git rev-list -n1 "$p" --not "${known[@]}")" ]; then
        refuse "$local_ref includes $c, whose parent $p is not public; publish with scripts/release-public.sh"
      fi
    done
  done
done
