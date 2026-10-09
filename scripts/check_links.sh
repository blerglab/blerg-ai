#!/usr/bin/env bash
set -euo pipefail
# Documentation link check. Offline, bash and standard tools only.
#
# 1. Every relative link in a tracked Markdown file must point at a file or
#    directory that exists, and a #fragment must match a heading in the target
#    (GitHub slug rules: lowercase, punctuation dropped, spaces to dashes,
#    repeated headings get -1, -2 ...). External links (http, https, mailto)
#    are not fetched.
# 2. No tracked file may mention a path the open-source cleanup deleted.
#
# BLERG_LINKS_ROOT points the check at another directory (the self-test uses
# it). Failures print file:line and the exit status is 1.
root="${BLERG_LINKS_ROOT:-.}"
cd "$root"

# Deleted paths and retired terms. Built from fragments so this file does not
# match itself.
stale='docs/super''powers|\.super''powers|runner/k''8s|deploy/exam''ples|multi-de''ployer|board/LI''CENSE'
# Files allowed to name them: ignore rules keep the old directories out, and
# recorded terminal captures are fixtures, not documentation.
stale_skip='(^|/)\.gitignore$|/testdata/|^scripts/check_links(_test)?\.sh$'

list_files() {
  if command -v git >/dev/null && git rev-parse --show-toplevel >/dev/null 2>&1 \
     && [ -z "$(git rev-parse --show-prefix)" ]; then
    git ls-files --cached --others --exclude-standard
  else
    find . -type f -not -path './.git/*' -not -path '*/node_modules/*' | sed 's|^\./||'
  fi
}
files=$(list_files | while IFS= read -r f; do if [ -f "$f" ]; then printf '%s\n' "$f"; fi; done)

fail=0
bad() { echo "$1:$2: $3"; fail=1; }

# slug <heading text>: GitHub's anchor for a heading.
slug() {
  printf '%s' "$1" \
    | sed -E 's/!?\[([^]]*)\]\([^)]*\)/\1/g; s/`//g; s/<[^>]*>//g; s/[*~]//g' \
    | tr 'A-Z' 'a-z' \
    | sed -E 's/[^a-z0-9 _-]//g; s/ /-/g'
}

# anchors_of <file>: one slug per heading, outside code fences, with GitHub's
# numeric suffix for repeats.
anchors_of() {
  awk '/^(```|~~~)/ { fence = !fence; next } !fence && /^(#|##|###|####|#####|######)[ \t]/ {
         sub(/^#+[ \t]+/, ""); sub(/[ \t]+#*[ \t]*$/, ""); print }' "$1" \
  | while IFS= read -r h; do slug "$h"; echo; done \
  | awk 'NF { n = seen[$0]++; print (n ? $0 "-" n : $0) }'
}

# links_of <file>: "line<TAB>target" for each inline or reference-style link,
# skipping code fences and inline code spans.
links_of() {
  awk '
    /^(```|~~~)/ { fence = !fence; next }
    fence { next }
    {
      line = $0
      gsub(/`[^`]*`/, "", line)
      if (match(line, /^( |  |   )?\[[^]]+\]:[ \t]+[^ \t]+/)) {
        t = substr(line, RSTART, RLENGTH); sub(/^( |  |   )?\[[^]]+\]:[ \t]+/, "", t)
        print NR "\t" t
      }
      while (match(line, /\]\([^)]*\)/)) {
        t = substr(line, RSTART + 2, RLENGTH - 3)
        line = substr(line, RSTART + RLENGTH)
        sub(/^</, "", t); sub(/>.*$/, "", t)
        sub(/[ \t]+["\047].*$/, "", t)
        print NR "\t" t
      }
    }' "$1"
}

while IFS= read -r md; do
  dir=$(dirname "$md")
  while IFS=$'\t' read -r ln target; do
    [ -n "$target" ] || continue
    case "$target" in
      http://*|https://*|mailto:*|tel:*|data:*) continue ;;
    esac
    path=${target%%#*}
    frag=""
    case "$target" in *'#'*) frag=${target#*#} ;; esac
    path=${path%%\?*}
    path=${path//%20/ }
    if [ -z "$path" ]; then
      dest="$md"
    elif [ "${path#/}" != "$path" ]; then
      dest="${path#/}"
    else
      dest="$dir/$path"
    fi
    if [ ! -e "$dest" ]; then
      bad "$md" "$ln" "broken link: $target (no such file or directory)"
      continue
    fi
    if [ -n "$frag" ] && [ -f "$dest" ]; then
      case "$dest" in
        *.md|*.markdown)
          want=$(printf '%s' "$frag" | tr 'A-Z' 'a-z')
          if ! anchors_of "$dest" | grep -Fxq -- "$want"; then
            bad "$md" "$ln" "broken anchor: $target (no such heading in $dest)"
          fi ;;
      esac
    fi
  done < <(links_of "$md")
done < <(printf '%s\n' "$files" | grep -E '\.md$' || true)

# Stale references, in every text file.
while IFS= read -r f; do
  grep -Iq . "$f" 2>/dev/null || continue
  while IFS=: read -r ln rest; do
    bad "$f" "$ln" "mentions a deleted path or retired term: $(printf '%s' "$rest" | cut -c1-80)"
  done < <(grep -nE "$stale" "$f" || true)
done < <(printf '%s\n' "$files" | grep -Ev "$stale_skip" || true)

if [ "$fail" -ne 0 ]; then
  echo "check_links: broken references found" >&2
  exit 1
fi
echo "check_links: ok"
