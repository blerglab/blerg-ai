#!/usr/bin/env bash
# install/desktop/daemon/configure-openclaw-endpoint.sh — interactive wizard
# for registering a self-hosted OpenAI-compatible inference endpoint (an
# on-prem GPU box running vLLM) as a custom OpenClaw
# provider, so it shows up as `<provider>/<model>` in the launch sheet.
#
# This exists because Tier 1 discovery (daemon/install.sh) can only report
# what's already configured — there's no way to auto-detect "someone has a
# vLLM box at some LAN IP" the way a local CLI login can be detected.
# It has to be declared, so this makes declaring it a few prompts instead of
# hand-writing JSON5 (see DAEMON.md's OpenClaw section for that manual form).
#
# Usage: ./configure-openclaw-endpoint.sh
# Safe to re-run: re-registering the same provider id overwrites its entry
# (openclaw's own "merge" catalog mode, not additive duplication).

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
. ../brand.sh

blerg_banner

if ! command -v openclaw >/dev/null 2>&1; then
  blerg_fail "openclaw isn't installed or isn't on PATH."
  blerg_fail "Install it first; OpenClaw's instructions are at https://openclaw.ai"
  exit 1
fi

if [ ! -t 0 ]; then
  blerg_fail "This wizard needs an interactive terminal (stdin isn't a TTY)."
  exit 1
fi

blerg_box "OpenClaw custom endpoint" <<'EOF'
Registers a self-hosted OpenAI-compatible inference endpoint (vLLM, an
on-prem GPU box, etc.) as a named OpenClaw provider, so it shows up as
<provider>/<model> in the launch sheet's Model field.

This only writes OpenClaw's own config (~/.openclaw/openclaw.json) — it
does not touch, restart, or reconfigure the inference box itself. If the
box is running vLLM, it separately needs to have been started with
--enable-auto-tool-choice --tool-call-parser <name> (e.g. "hermes" for a
Qwen3 model) or every turn will 400 with a tool-choice error — confirmed
by testing, not a hypothetical warning.
EOF
echo

read -r -p "  Provider id (short name, e.g. 'mybox'): " PROVIDER_ID
if [ -z "$PROVIDER_ID" ]; then
  blerg_fail "Provider id can't be empty — nothing changed."
  exit 1
fi
case "$PROVIDER_ID" in
  *[!a-zA-Z0-9_-]*)
    blerg_fail "Provider id must be letters, digits, '-', or '_' only — nothing changed."
    exit 1
    ;;
esac

read -r -p "  Base URL (e.g. http://192.0.2.10:8000/v1): " BASE_URL
if [ -z "$BASE_URL" ]; then
  blerg_fail "Base URL can't be empty — nothing changed."
  exit 1
fi

read -r -p "  Model id as the server reports it (e.g. qwen3-30b): " MODEL_ID
if [ -z "$MODEL_ID" ]; then
  blerg_fail "Model id can't be empty — nothing changed."
  exit 1
fi

read -r -p "  Display name [$MODEL_ID]: " MODEL_NAME
MODEL_NAME="${MODEL_NAME:-$MODEL_ID}"

read -r -p "  Context window in tokens [32768]: " CONTEXT_WINDOW
CONTEXT_WINDOW="${CONTEXT_WINDOW:-32768}"
case "$CONTEXT_WINDOW" in
  ''|*[!0-9]*)
    blerg_fail "Context window must be a plain number — nothing changed."
    exit 1
    ;;
esac

# Best-effort reachability probe — a typo'd host/port is the single most
# common failure mode here, and this catches it before writing any config.
# Never blocks: an unreachable endpoint might just be asleep or firewalled
# from this machine specifically, so a failed probe only warns.
blerg_step "Checking $BASE_URL/models ..."
if command -v curl >/dev/null 2>&1 && curl -fsS -m 5 "$BASE_URL/models" >/dev/null 2>&1; then
  blerg_ok "Endpoint responded."
else
  blerg_fail "Couldn't reach $BASE_URL/models from this machine (may be fine — could be"
  blerg_fail "asleep, still starting, or only reachable from elsewhere). Continuing anyway."
fi

# json_escape: minimal JSON string escaping for values embedded in the
# patch below (backslash and double-quote — the only two characters that
# would otherwise break the JSON string literal). Not a full JSON encoder;
# sufficient because these are single-line identifiers/URLs, never
# multi-line or containing control characters.
json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

PATCH_FILE="$(mktemp)"
trap 'rm -f "$PATCH_FILE"' EXIT
cat > "$PATCH_FILE" <<EOF
{
  models: {
    providers: {
      "$(json_escape "$PROVIDER_ID")": {
        baseUrl: "$(json_escape "$BASE_URL")",
        api: "openai-completions",
        apiKey: "not-needed",
        models: [
          { id: "$(json_escape "$MODEL_ID")", name: "$(json_escape "$MODEL_NAME")", contextWindow: $CONTEXT_WINDOW }
        ]
      }
    }
  }
}
EOF

blerg_step "Validating (dry run)..."
if ! openclaw config patch --file "$PATCH_FILE" --dry-run; then
  blerg_fail "openclaw rejected this config — nothing was written. See the error above."
  exit 1
fi

read -r -p "  Apply this? [Y/n] " CONFIRM
if [ "$CONFIRM" = "n" ] || [ "$CONFIRM" = "N" ]; then
  blerg_step "Cancelled — nothing changed."
  exit 0
fi

if openclaw config patch --file "$PATCH_FILE"; then
  blerg_ok "Registered. Use engine OpenClaw with model:"
  echo
  echo "    $PROVIDER_ID/$MODEL_ID"
  echo
  blerg_ok "(Re-run this script anytime to add another endpoint or update this one.)"
else
  blerg_fail "openclaw config patch failed applying for real — see the error above."
  exit 1
fi
