#!/usr/bin/env bash
# Renders the manifests the way deploy.sh does and asserts the wiring the audits found
# missing. Runs without a cluster; the kubectl dry-run is skipped when kubectl is absent,
# or when BLERG_SKIP_KUBECTL is set (kubectl installed but no cluster to ask, as in CI:
# even a client-side dry-run resolves resource kinds against the API server).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
for f in $(grep -E '^\s+- ' "$here/kustomization.yaml" | sed 's/^\s*- //'); do cp "$here/$f" "$tmp/"; done
cp "$here/kustomization.yaml" "$tmp/"
for f in "$tmp"/*.yaml; do
  REGISTRY=reg.example TAG=t1 DOMAIN=blerg.example SCHEME=https \
  AGENT_IMAGE=reg.example/blerg-runner-agent:t1 WEBHOOK_ALLOW_PRIVATE=false \
    envsubst '${REGISTRY} ${TAG} ${DOMAIN} ${SCHEME} ${AGENT_IMAGE} ${WEBHOOK_ALLOW_PRIVATE}' < "$f" > "$f.r" && mv "$f.r" "$f"
done
fail=0
if grep -rn '\${' "$tmp"/*.yaml; then echo "FAIL: unsubstituted placeholder"; fail=1; fi
for want in BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE BLERG_CORE_INTERNAL_KEY BLERG_RUNNER_CORE_INTERNAL_KEY BLERG_CORE_LOCAL_KEY BLERG_BOARD_SECRET_KEY \
            BLERG_CORE_PUBLIC_URL BLERG_BOARD_PUBLIC_URL BLERG_BOARD_AGENT_URL RUNNER_UI_BASE \
            BLERG_RUNNER_DATA_DIR proxy-body-size runAsNonRoot 'path: /healthz' 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'; do
  grep -rqF -- "$want" "$tmp" || { echo "FAIL: $want not wired"; fail=1; }
done
grep -q 'https://board.blerg.example' "$tmp/configmap.yaml" || { echo "FAIL: SCHEME not applied to public URLs"; fail=1; }

# Cluster runtime: the agent image flows from AGENT_IMAGE into the ConfigMap,
# and the rest of the cluster-runtime knobs are pinned there too.
grep -q 'BLERG_RUNNER_AGENT_IMAGE: "reg.example/blerg-runner-agent:t1"' "$tmp/configmap.yaml" \
  || { echo "FAIL: AGENT_IMAGE not rendered into BLERG_RUNNER_AGENT_IMAGE"; fail=1; }
for want in 'BLERG_RUNNER_AGENT_NAMESPACE: "blerg-runner-sessions"' \
            'BLERG_RUNNER_INTERNAL_URL: "http://blerg-runner.blerg.svc.cluster.local:8080"' \
            'BLERG_RUNNER_MAX_SESSIONS:'; do
  grep -qF -- "$want" "$tmp/configmap.yaml" || { echo "FAIL: $want not wired"; fail=1; }
done
# BLERG_RUNNER_AGENT_GIT_BASE must stay UNSET: the runner already defaults to
# https://github.com, and setting it explicitly disables the clean 422 for bare
# repo names and overrides BLERG_RUNNER_GITHUB_ORG. Documented-only key.
grep -qE '^\s+BLERG_RUNNER_AGENT_GIT_BASE:' "$tmp/configmap.yaml" \
  && { echo "FAIL: BLERG_RUNNER_AGENT_GIT_BASE must not be set in the ConfigMap data"; fail=1; }
# CLUSTER_RUNTIME=off renders the same ConfigMap with an empty image — the
# runner's on/off switch — and must not leave a placeholder behind.
AGENT_IMAGE= WEBHOOK_ALLOW_PRIVATE=false REGISTRY=reg.example TAG=t1 DOMAIN=blerg.example SCHEME=https \
  envsubst '${REGISTRY} ${TAG} ${DOMAIN} ${SCHEME} ${AGENT_IMAGE} ${WEBHOOK_ALLOW_PRIVATE}' < "$here/configmap.yaml" > "$tmp/configmap-off.yaml"
grep -q 'BLERG_RUNNER_AGENT_IMAGE: ""' "$tmp/configmap-off.yaml" \
  || { echo "FAIL: empty AGENT_IMAGE does not render BLERG_RUNNER_AGENT_IMAGE: \"\""; fail=1; }

# A re-run with an unchanged TAG only rewrites the ConfigMap, and env-from-
# ConfigMap values are read at process start — without a restart the new
# cluster-runtime keys never reach the running runner.
grep -qF -- 'rollout restart deploy/blerg-runner' "$here/deploy.sh" \
  || { echo "FAIL: deploy.sh does not restart blerg-runner after applying config"; fail=1; }

# cluster-runtime.yaml is applied by deploy.sh directly (kubectl apply -f), NOT
# through the kustomization — kustomize's `namespace: blerg` transformer would
# rewrite its sessions-namespace RBAC and break the RoleBinding's subject.
grep -qF -- 'blerg-runner-sessions' "$here/cluster-runtime.yaml" \
  || { echo "FAIL: cluster-runtime.yaml missing blerg-runner-sessions"; fail=1; }
if grep -q '\${' "$here/cluster-runtime.yaml"; then
  echo "FAIL: cluster-runtime.yaml has a placeholder but deploy.sh applies it unrendered"; fail=1
fi
# deploy.sh hands secret values to kubectl through private files, not --from-literal
# arguments (visible in the process list). Exercise the file generation without kubectl:
# read the env file back the way kubectl does (one KEY=VALUE per line, split at the first
# "=", no quoting; blank and "#" lines skipped) and require every value byte for byte.
# shellcheck source=deploy_lib.sh
. "$here/deploy_lib.sh"
grep -q -- '--from-literal' "$here/deploy.sh" && { echo "FAIL: deploy.sh still passes --from-literal"; fail=1; }
sdir="$tmp/secretdir"; mkdir -m 700 "$sdir"
secret_args_begin "$sdir"
declare -A want=(
  [PLAIN]='abc123'
  [EQUALS]='a=b==c='
  [B64_PAD]='q83vEjRWeJq83vEjRWeJq83vEjRWeJq83vEjRWeJq8k='
  [HASH]='#not-a-comment'
  [SPACES]='  lead and trail  '
  [QUOTES]='"double" and '"'"'single'"'"' and `tick` and $DOLLAR and \back'
  [URL]='postgres://blerg:p@ss=w#rd@postgres:5432/db?sslmode=disable'
  [BLANK]=' '
)
for k in "${!want[@]}"; do secret_add "$k" "${want[$k]}"; done
nl_json=$'{\n  "a": "b=c"\n}\n'
nl_env=$'FOO=bar\n# comment\nBAZ=q u x\r\nlast'
secret_add MULTI_JSON "$nl_json"
secret_add MULTI_ENV "$nl_env"
[ -s "$SECRET_ENV_FILE" ] || { echo "FAIL: env file empty"; fail=1; }
[ "$(stat -c %a "$SECRET_ENV_FILE")" = 600 ] || { echo "FAIL: env file is not mode 0600"; fail=1; }
declare -A got=()
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue ;; esac
  got["${line%%=*}"]="${line#*=}"
done < "$SECRET_ENV_FILE"
for k in "${!want[@]}"; do
  [ "${got[$k]-__missing__}" = "${want[$k]}" ] || { echo "FAIL: $k did not round-trip through the env file"; fail=1; }
done
[ "${#got[@]}" -eq "${#want[@]}" ] || { echo "FAIL: env file has ${#got[@]} keys, want ${#want[@]}"; fail=1; }
# Multi-line values cannot live in an env file; they go through --from-file with exact bytes.
for pair in "MULTI_JSON:$nl_json" "MULTI_ENV:$nl_env"; do
  k="${pair%%:*}"; v="${pair#*:}"; f=""
  for a in "${SECRET_ARGS[@]}"; do
    case "$a" in "--from-file=$k="*) f="${a#"--from-file=$k="}" ;; esac
  done
  [ -n "$f" ] || { echo "FAIL: $k has no --from-file argument"; fail=1; continue; }
  printf '%s' "$v" | cmp -s - "$f" || { echo "FAIL: $k bytes changed in its file"; fail=1; }
  [ "$(stat -c %a "$f")" = 600 ] || { echo "FAIL: $k file is not mode 0600"; fail=1; }
  [ -n "${got[$k]+x}" ] && { echo "FAIL: $k leaked into the env file"; fail=1; }
done
# No value appears in the arguments handed to kubectl.
for a in "${SECRET_ARGS[@]}"; do
  for k in "${!want[@]}"; do
    [ "${#want[$k]}" -gt 2 ] || continue
    case "$a" in *"${want[$k]}"*) echo "FAIL: value of $k is on the kubectl command line"; fail=1 ;; esac
  done
done
( secret_add 'BAD-KEY' x 2>/dev/null ) && { echo "FAIL: invalid key name accepted"; fail=1; }
# A second Secret gets its own files.
secret_args_begin "$sdir"; secret_add ONLY 1
[ "$(cat "$SECRET_ENV_FILE")" = "ONLY=1" ] || { echo "FAIL: second Secret shares the first one's file"; fail=1; }
# deploy.sh keeps the scratch directory private and removes it on exit.
grep -qF 'work_dir="$(mktemp -d)"' "$here/deploy.sh" && grep -qF "trap 'rm -rf \"\$work_dir\"' EXIT" "$here/deploy.sh" \
  || { echo "FAIL: deploy.sh scratch dir is not mktemp -d with an exit trap"; fail=1; }

# The confirmation prompt: asked on a terminal, skipped by --yes, BLERG_ASSUME_YES, or no terminal.
stdin_is_tty() { return 0; }
deploy_needs_confirmation || { echo "FAIL: no prompt on a terminal"; fail=1; }
deploy_needs_confirmation --yes && { echo "FAIL: --yes still prompts"; fail=1; }
deploy_needs_confirmation -y && { echo "FAIL: -y still prompts"; fail=1; }
BLERG_ASSUME_YES=1 deploy_needs_confirmation && { echo "FAIL: BLERG_ASSUME_YES=1 still prompts"; fail=1; }
BLERG_ASSUME_YES=no deploy_needs_confirmation || { echo "FAIL: BLERG_ASSUME_YES=no skipped the prompt"; fail=1; }
stdin_is_tty() { return 1; }
deploy_needs_confirmation && { echo "FAIL: prompts with no terminal (nobody could answer)"; fail=1; }
deploy_must_refuse || { echo "FAIL: a run with no terminal and no --yes was not refused"; fail=1; }
deploy_must_refuse --yes && { echo "FAIL: --yes was refused with no terminal"; fail=1; }
deploy_must_refuse -y && { echo "FAIL: -y was refused with no terminal"; fail=1; }
BLERG_ASSUME_YES=1 deploy_must_refuse && { echo "FAIL: BLERG_ASSUME_YES=1 was refused with no terminal"; fail=1; }
BLERG_ASSUME_YES=no deploy_must_refuse || { echo "FAIL: BLERG_ASSUME_YES=no was not refused with no terminal"; fail=1; }
stdin_is_tty() { return 0; }
deploy_must_refuse && { echo "FAIL: a terminal run was refused"; fail=1; }
grep -qF 'kubectl config current-context' "$here/deploy.sh" || { echo "FAIL: deploy.sh does not show the kube context"; fail=1; }

if [ -n "${BLERG_SKIP_KUBECTL:-}" ]; then
  echo "skip: BLERG_SKIP_KUBECTL set (dry-run not exercised)"
elif command -v kubectl >/dev/null 2>&1; then
  kubectl apply -k "$tmp" --dry-run=client -o name >/dev/null || { echo "FAIL: kustomize dry-run"; fail=1; }
  kubectl apply -f "$here/cluster-runtime.yaml" --dry-run=client -o name >/dev/null \
    || { echo "FAIL: cluster-runtime.yaml dry-run"; fail=1; }
else
  echo "skip: kubectl not on PATH (dry-run not exercised)"
fi
[ "$fail" -eq 0 ] && echo "ok"
exit "$fail"
