#!/usr/bin/env bash
# Fast, conservative guard for the Bash 3.2 command-substitution heredoc bug.
# This is a lexical fallback, not a shell parser; CI also uses the real Bash 3.2.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
  while IFS= read -r -d '' file; do
    set -- "$@" "$file"
  done < <(find install -type f -name '*.sh' -print0)
fi
awk '
FNR == 1 { context = ""; delimiter = "" }
delimiter != "" {
  end = $0
  if (strip_tabs) sub(/^\t+/, "", end)
  if (end == delimiter) {
    if (in_sub && (single % 2 || double % 2)) {
      printf "%s:%d: odd quote count in command-substitution heredoc (Bash 3.2)\n", FILENAME, start
      failed = 1
    }
    delimiter = ""
    next
  }
  single += gsub(/\047/, "&")
  double += gsub(/"/, "&")
  next
}
/^[[:space:]]*#/ { next }
{
  context = context " " $0
  # Remove complete inner substitutions before checking the outer one.
  while (gsub(/\$\([^()]*\)/, "", context)) {}
  if (match($0, /<<-?[[:space:]]*[\047"]?[A-Za-z_][A-Za-z_0-9]*[\047"]?/)) {
    delimiter = substr($0, RSTART, RLENGTH)
    strip_tabs = delimiter ~ /^<<-/
    sub(/^<<-?[[:space:]]*/, "", delimiter)
    gsub(/[\047"]/, "", delimiter)
    in_sub = context ~ /\$\([^)]*$/
    start = FNR
    single = double = 0
  }
  # Retain an open substitution across lines, including $( followed by cat.
  if (context !~ /\$\([^)]*$/) context = ""
}
END { exit failed }
' "$@"
