#!/usr/bin/env bash
# install/desktop/brand.sh — shared terminal branding for blerg's install
# scripts. Sourced, never executed directly. Falls back to plain text
# automatically when stdout isn't a terminal (piped/redirected) or NO_COLOR
# is set, so output stays clean for logs and scripts.

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  BLERG_ORANGE=$'\033[38;2;255;106;61m'
  BLERG_BOLD=$'\033[1m'
  BLERG_RESET=$'\033[0m'
else
  BLERG_ORANGE=""
  BLERG_BOLD=""
  BLERG_RESET=""
fi

# One-line wordmark banner — the same "blerg█" block-cursor identity used on
# the web UI, printed once when a script starts.
blerg_banner() {
  echo
  echo "  ${BLERG_BOLD}blerg${BLERG_RESET}${BLERG_ORANGE}█${BLERG_RESET}"
}

# blerg_box ["title"] <<'EOF'
# plain-text body lines, no ANSI codes of their own — the box colors its own
# borders only, so width math never has to account for invisible escapes.
# EOF
blerg_box() {
  local title="${1:-}" width=0 line
  local -a lines=()
  while IFS= read -r line; do
    lines+=("$line")
    [ "${#line}" -gt "$width" ] && width="${#line}"
  done
  local title_min=0
  [ -n "$title" ] && title_min=$((${#title} + 4))
  [ "$title_min" -gt "$width" ] && width="$title_min"
  [ "$width" -lt 1 ] && width=1

  local rule=""
  local i
  for ((i = 0; i < width + 2; i++)); do rule="${rule}─"; done

  if [ -n "$title" ]; then
    local trule="" tn=$((width + 2 - ${#title} - 3))
    [ "$tn" -lt 0 ] && tn=0
    for ((i = 0; i < tn; i++)); do trule="${trule}─"; done
    echo "${BLERG_ORANGE}╭─ ${BLERG_BOLD}${title}${BLERG_RESET}${BLERG_ORANGE} ${trule}╮${BLERG_RESET}"
  else
    echo "${BLERG_ORANGE}╭${rule}╮${BLERG_RESET}"
  fi
  for line in "${lines[@]}"; do
    # printf's %-*s width is byte-based, not character-based — an em dash or
    # any multi-byte UTF-8 char in $line would throw off padding even though
    # ${#line} itself is correct here. Pad manually instead.
    local pad=$((width - ${#line})) spaces=""
    for ((i = 0; i < pad; i++)); do spaces="${spaces} "; done
    echo "${BLERG_ORANGE}│${BLERG_RESET} ${line}${spaces} ${BLERG_ORANGE}│${BLERG_RESET}"
  done
  echo "${BLERG_ORANGE}╰${rule}╯${BLERG_RESET}"
}

blerg_ok()   { echo "  ${BLERG_ORANGE}✓${BLERG_RESET} $*"; }
blerg_step() { echo "  ${BLERG_ORANGE}›${BLERG_RESET} $*"; }
blerg_fail() { echo "  ${BLERG_ORANGE}✗${BLERG_RESET} $*" >&2; }
