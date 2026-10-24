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
  AGENT_IMAGE=reg.example/blerg-runner-agent:t1 WEBHOOK_ALLOW_PRIVATE=false TRUSTED_PROXIES=192.0.2.0/24 WORKSPACE_STORAGE_CLASS=bounded-fixture \
    envsubst '${REGISTRY} ${TAG} ${DOMAIN} ${SCHEME} ${AGENT_IMAGE} ${WEBHOOK_ALLOW_PRIVATE} ${TRUSTED_PROXIES} ${WORKSPACE_STORAGE_CLASS}' < "$f" > "$f.r" && mv "$f.r" "$f"
done
fail=0
if grep -rn '\${' "$tmp"/*.yaml; then echo "FAIL: unsubstituted placeholder"; fail=1; fi
for want in BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE BLERG_CORE_INTERNAL_KEY BLERG_RUNNER_CORE_INTERNAL_KEY BLERG_CORE_LOCAL_KEY BLERG_BOARD_SECRET_KEY \
            BLERG_CORE_BOARD_MINT_KEY BLERG_BOARD_DEFAULT_RUN_AS_CREATOR \
            BLERG_CORE_PUBLIC_URL BLERG_BOARD_PUBLIC_URL BLERG_BOARD_AGENT_URL RUNNER_UI_BASE \
            BLERG_RUNNER_DATA_DIR proxy-body-size runAsNonRoot 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'; do
  grep -rqF -- "$want" "$tmp" || { echo "FAIL: $want not wired"; fail=1; }
done
grep -q 'https://board.blerg.example' "$tmp/configmap.yaml" || { echo "FAIL: SCHEME not applied to public URLs"; fail=1; }
# Run as me: core and the board both read the mint key; the board never gets the internal key.
for f in core-deployment.yaml board-deployment.yaml; do
  grep -qF 'key: BLERG_CORE_BOARD_MINT_KEY' "$tmp/$f" || { echo "FAIL: $f does not read BLERG_CORE_BOARD_MINT_KEY"; fail=1; }
done
grep -q 'CORE_INTERNAL_KEY' "$tmp/board-deployment.yaml" && { echo "FAIL: the board must not get the core internal key"; fail=1; }
grep -q 'secret_add BLERG_CORE_BOARD_MINT_KEY' "$here/deploy.sh" || { echo "FAIL: deploy.sh does not write BLERG_CORE_BOARD_MINT_KEY"; fail=1; }
# The gateway's public route throttles the real client behind the ingress (TRUSTED_PROXIES).
grep -qF 'BLERG_RUNNER_TRUSTED_PROXIES: "192.0.2.0/24"' "$tmp/configmap.yaml" || { echo "FAIL: TRUSTED_PROXIES not rendered"; fail=1; }
grep -qF -- 'key: BLERG_RUNNER_TRUSTED_PROXIES, optional: true' "$tmp/runner-deployment.yaml" || { echo "FAIL: BLERG_RUNNER_TRUSTED_PROXIES not passed to the runner"; fail=1; }
# The ingress takes a body above the runner's 100 MiB per-file default, and the runner reads its
# file limits from the ConfigMap.
grep -qF 'nginx.ingress.kubernetes.io/proxy-body-size: "128m"' "$tmp/ingress.yaml" \
  || { echo "FAIL: ingress proxy-body-size is not 128m (a 100 MiB file must fit)"; fail=1; }
for want in BLERG_RUNNER_MAX_FILE_MB BLERG_RUNNER_MAX_SESSION_UPLOADS_MB BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN; do
  grep -qF -- "key: $want, optional: true" "$tmp/runner-deployment.yaml" \
    || { echo "FAIL: $want not passed to the runner"; fail=1; }
done

# Cluster runtime: the agent image flows from AGENT_IMAGE into the ConfigMap,
# and the rest of the cluster-runtime knobs are pinned there too.
grep -q 'BLERG_RUNNER_AGENT_IMAGE: "reg.example/blerg-runner-agent:t1"' "$tmp/configmap.yaml" \
  || { echo "FAIL: AGENT_IMAGE not rendered into BLERG_RUNNER_AGENT_IMAGE"; fail=1; }
for want in 'BLERG_RUNNER_AGENT_NAMESPACE: "blerg-runner-sessions"' \
            'BLERG_RUNNER_INTERNAL_URL: "http://blerg-runner.blerg.svc.cluster.local:8080"' \
            'BLERG_RUNNER_MAX_SESSIONS:' \
            'BLERG_RUNNER_SANDBOX_CLUSTER_CHILDREN: "once"'; do
  grep -qF -- "$want" "$tmp/configmap.yaml" || { echo "FAIL: $want not wired"; fail=1; }
done
grep -qF 'BLERG_RUNNER_WORKSPACE_STORAGE_CLASS: "bounded-fixture"' "$tmp/configmap.yaml" || { echo "FAIL: workspace CSI class not rendered"; fail=1; }
grep -qF 'key: BLERG_RUNNER_WORKSPACE_STORAGE_CLASS, optional: true' "$tmp/runner-deployment.yaml" || { echo "FAIL: workspace CSI class not passed to runner"; fail=1; }
# BLERG_RUNNER_AGENT_GIT_BASE must stay UNSET: the runner already defaults to
# https://github.com, and setting it explicitly disables the clean 422 for bare
# repo names and overrides BLERG_RUNNER_GITHUB_ORG. Documented-only key.
grep -qE '^\s+BLERG_RUNNER_AGENT_GIT_BASE:' "$tmp/configmap.yaml" \
  && { echo "FAIL: BLERG_RUNNER_AGENT_GIT_BASE must not be set in the ConfigMap data"; fail=1; }
# CLUSTER_RUNTIME=off renders the same ConfigMap with an empty image — the
# runner's on/off switch — and must not leave a placeholder behind.
WORKSPACE_STORAGE_CLASS= AGENT_IMAGE= WEBHOOK_ALLOW_PRIVATE=false REGISTRY=reg.example TAG=t1 DOMAIN=blerg.example SCHEME=https \
  envsubst '${REGISTRY} ${TAG} ${DOMAIN} ${SCHEME} ${AGENT_IMAGE} ${WEBHOOK_ALLOW_PRIVATE} ${WORKSPACE_STORAGE_CLASS}' < "$here/configmap.yaml" > "$tmp/configmap-off.yaml"
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

# MCP gateway: the runner serves it on its own port, exposed ONLY through an internal ClusterIP
# Service. No Ingress may route to that Service or port (the gateway must never be reachable
# from outside the cluster), and the ConfigMap must point sessions at the internal Service.
gw_svc=blerg-runner-mcp-gateway
grep -qF -- "name: $gw_svc" "$tmp/runner-deployment.yaml" || { echo "FAIL: internal gateway Service $gw_svc missing"; fail=1; }
grep -qE '^\s+containerPort: 8090' "$tmp/runner-deployment.yaml" || { echo "FAIL: runner container does not declare the gateway port 8090"; fail=1; }
gw_block="$(awk -v s="$gw_svc" 'BEGIN{RS="\n---\n"} index($0, "name: " s) && /kind: Service/ {print}' "$tmp/runner-deployment.yaml" | grep -vE '^\s*#')"
[ -n "$gw_block" ] || { echo "FAIL: could not isolate the gateway Service"; fail=1; }
grep -qE '^\s+type: ClusterIP\s*$' <<<"$gw_block" || { echo "FAIL: the gateway Service is not type: ClusterIP"; fail=1; }
if grep -qE 'NodePort|LoadBalancer|externalIPs|nodePort' <<<"$gw_block"; then echo "FAIL: the gateway Service is exposed beyond the cluster"; fail=1; fi
ing_files=""
for f in "$tmp"/*.yaml; do grep -qE '^kind: Ingress' "$f" && ing_files="$ing_files $f"; done
[ -n "$ing_files" ] || { echo "FAIL: no Ingress found to check"; fail=1; }
for f in $ing_files; do
  # Comments may talk about the gateway; only real fields count.
  if grep -vE '^\s*#' "$f" | grep -qE "mcp-gateway|8090|mcp-gw"; then
    echo "FAIL: $(basename "$f") references the MCP gateway service or port; it must never be routed by an Ingress"; fail=1
  fi
  # Every runner backend must be the web port, and nothing may name the gateway Service.
  grep -vE '^\s*#' "$f" | awk '/name: blerg-runner/{r=1;next} r&&/number:/{ if ($2!=8080) bad=1; r=0 } END{exit bad}' \
    || { echo "FAIL: an Ingress runner backend is not on port 8080"; fail=1; }
done
grep -qF 'BLERG_RUNNER_MCP_GW_ADDR: ":8090"' "$tmp/configmap.yaml" || { echo "FAIL: BLERG_RUNNER_MCP_GW_ADDR not set in the ConfigMap"; fail=1; }
grep -qF "BLERG_RUNNER_MCP_GW_URL: \"http://$gw_svc.blerg.svc.cluster.local:8090\"" "$tmp/configmap.yaml" \
  || { echo "FAIL: BLERG_RUNNER_MCP_GW_URL does not name the internal gateway Service"; fail=1; }
for v in BLERG_RUNNER_MCP_GW_ADDR BLERG_RUNNER_MCP_GW_URL BLERG_RUNNER_MCP_ALLOW_HTTP_HOSTS BLERG_RUNNER_MCP_ALLOW_PRIVATE_HOSTS \
         BLERG_RUNNER_MCP_CALL_TIMEOUT_SECONDS BLERG_RUNNER_MCP_MAX_CONCURRENT BLERG_RUNNER_MCP_MAX_RESULT_BYTES; do
  grep -qF -- "key: $v, optional: true" "$tmp/runner-deployment.yaml" || { echo "FAIL: $v is not an optional ConfigMap env of the runner"; fail=1; }
done
for v in BLERG_CORE_MCP_ALLOW_HTTP_HOSTS BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS; do
  grep -qF -- "key: $v, optional: true" "$tmp/core-deployment.yaml" || { echo "FAIL: $v is not an optional ConfigMap env of core"; fail=1; }
done
# The sessions network-policy example must let session pods reach the gateway port (and nothing about it may be
# missing from the docs an operator reads).
grep -qE '^\s+port: 8090' "$here/networkpolicy-sessions.example.yaml" || { echo "FAIL: networkpolicy-sessions.example.yaml has no rule for the gateway port"; fail=1; }
grep -qF 'blerg-runner-mcp-gateway' "$here/CLUSTER-RUNTIME.md" || { echo "FAIL: CLUSTER-RUNTIME.md does not document the gateway Service"; fail=1; }
# Session pods must not reach core, and the board only through the commented-out opt-in rule.
grep -vE '^\s*#' "$here/networkpolicy-sessions.example.yaml" | grep -qE 'app: blerg-(core|board)' && { echo "FAIL: networkpolicy-sessions.example.yaml lets session pods reach core or the board by default"; fail=1; }

# The runner's data directory is a persistent claim, not an emptyDir: the database indexes the
# files in it, so it must survive a restart. A rolling surge shares the volume on the same node;
# deploy.sh creates it before applying the Deployment,
# never re-applies or deletes it, and it stays out of the kustomization.
runner_doc="$(awk 'BEGIN{RS="\n---\n"} /kind: Deployment/ && /name: blerg-runner\n/ {print}' "$tmp/runner-deployment.yaml" | grep -vE '^\s*#')"
[ -n "$runner_doc" ] || { echo "FAIL: could not isolate the runner Deployment"; fail=1; }
# Rolling updates, read from the parsed manifests (a comment cannot satisfy these): each
# Deployment surges one ready pod before the old one goes, readiness is /readyz and liveness
# /livez, the runner's surge lands on the data volume's node, and the runner has no preStop hook
# (it must exit at once; see runner-deployment.yaml).
command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' 2>/dev/null \
  || { echo "FAIL: python3 with PyYAML is needed to check the Deployments"; fail=1; }
python3 - "$tmp" <<'PY' || fail=1
import sys, yaml
tmp = sys.argv[1]
bad = []
for component in ("runner", "core", "board"):
    name = f"blerg-{component}"
    with open(f"{tmp}/{component}-deployment.yaml") as f:
        docs = [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]
    deps = [d for d in docs if d.get("kind") == "Deployment" and d.get("metadata", {}).get("name") == name]
    if len(deps) != 1:
        bad.append(f"{component}: {len(deps)} Deployments named {name}")
        continue
    spec = deps[0]["spec"]
    strategy = spec.get("strategy") or {}
    rolling = strategy.get("rollingUpdate") or {}
    if strategy.get("type") != "RollingUpdate":
        bad.append(f"{component}: strategy.type is {strategy.get('type')!r}, not RollingUpdate")
    if rolling.get("maxUnavailable") != 0:
        bad.append(f"{component}: maxUnavailable is {rolling.get('maxUnavailable')!r}, not 0")
    if rolling.get("maxSurge") != 1:
        bad.append(f"{component}: maxSurge is {rolling.get('maxSurge')!r}, not 1")
    pod = spec["template"]["spec"]
    containers = [c for c in pod.get("containers", []) if c.get("name") == name]
    if len(containers) != 1:
        bad.append(f"{component}: no container named {name}")
        continue
    container = containers[0]
    for probe, path in (("readinessProbe", "/readyz"), ("livenessProbe", "/livez")):
        got = ((container.get(probe) or {}).get("httpGet") or {}).get("path")
        if got != path:
            bad.append(f"{component}: {probe} path is {got!r}, not {path}")
    if component == "runner":
        if (container.get("lifecycle") or {}).get("preStop") is not None:
            bad.append("runner: has a preStop hook; it must exit at once")
        terms = (((pod.get("affinity") or {}).get("podAffinity") or {})
                 .get("requiredDuringSchedulingIgnoredDuringExecution") or [])
        if not any(t.get("topologyKey") == "kubernetes.io/hostname" and
                   (t.get("labelSelector") or {}).get("matchLabels", {}).get("app") == "blerg-runner" for t in terms):
            bad.append("runner: surge is not required on the same node as a blerg-runner pod")
    elif (pod.get("terminationGracePeriodSeconds") or 30) < 26:
        bad.append(f"{component}: terminationGracePeriodSeconds is below the 5 s drain plus 20 s shutdown")
for line in bad:
    print("FAIL: " + line)
sys.exit(1 if bad else 0)
PY
grep -qE 'emptyDir' <<<"$runner_doc" && { echo "FAIL: the runner Deployment still mounts an emptyDir"; fail=1; }
grep -qE '^\s+claimName: blerg-runner-data\s*$' <<<"$runner_doc" || { echo "FAIL: the runner's data volume is not the blerg-runner-data claim"; fail=1; }
grep -A1 -E '^\s+- name: data\s*$' <<<"$runner_doc" | grep -qE '^\s+mountPath: /data\s*$' \
  || { echo "FAIL: the data volume is not mounted at /data"; fail=1; }
grep -qE '^\s+fsGroup: 65532\s*$' <<<"$runner_doc" || { echo "FAIL: the runner pod has no fsGroup for its data volume"; fail=1; }
grep -qF 'runner-data-pvc.yaml' "$here/kustomization.yaml" && { echo "FAIL: the data claim is in the kustomization (delete -k would take the files)"; fail=1; }
claim="$(runner_data_claim_render "$here/runner-data-pvc.yaml" 10Gi | grep -vE '^\s*#')"
grep -qE '^kind: PersistentVolumeClaim$' <<<"$claim" && grep -qE '^  name: blerg-runner-data$' <<<"$claim" && grep -qE '^  namespace: blerg$' <<<"$claim" \
  || { echo "FAIL: runner-data-pvc.yaml is not the blerg-runner-data claim in namespace blerg"; fail=1; }
grep -qF 'accessModes: ["ReadWriteOnce"]' <<<"$claim" || { echo "FAIL: the data claim is not ReadWriteOnce"; fail=1; }
grep -qE '^      storage: 10Gi$' <<<"$claim" || { echo "FAIL: RUNNER_DATA_SIZE not rendered into the claim"; fail=1; }
grep -q 'storageClassName' <<<"$claim" && { echo "FAIL: no storage class given, yet the claim names one"; fail=1; }
grep -q '\${' <<<"$claim" && { echo "FAIL: unsubstituted placeholder in the data claim"; fail=1; }
classed="$(runner_data_claim_render "$here/runner-data-pvc.yaml" 20Gi fast-ssd | grep -vE '^\s*#')"
awk '/^spec:/{s=1;next} s&&/^  storageClassName: fast-ssd$/{ok=1} END{exit !ok}' <<<"$classed" \
  || { echo "FAIL: RUNNER_DATA_STORAGE_CLASS not rendered under spec"; fail=1; }
( runner_data_claim_render "$here/runner-data-pvc.yaml" '10Gi; rm' 2>/dev/null ) && { echo "FAIL: a bad RUNNER_DATA_SIZE was accepted"; fail=1; }
( runner_data_claim_render "$here/runner-data-pvc.yaml" 10Gi 'Bad Class' 2>/dev/null ) && { echo "FAIL: a bad RUNNER_DATA_STORAGE_CLASS was accepted"; fail=1; }
claim_line="$(grep -nF 'runner_data_claim_render "$script_dir/runner-data-pvc.yaml"' "$here/deploy.sh" | head -1 | cut -d: -f1)"
apply_line="$(grep -nE '^kubectl apply -k "\$render_dir"' "$here/deploy.sh" | head -1 | cut -d: -f1)"
{ [ -n "$claim_line" ] && [ -n "$apply_line" ] && [ "$claim_line" -lt "$apply_line" ]; } \
  || { echo "FAIL: deploy.sh does not create the data claim before applying the Deployment"; fail=1; }
grep -qF 'runner_data_claim_check "$RUNNER_DATA_SIZE" "$RUNNER_DATA_STORAGE_CLASS"' "$here/deploy.sh" \
  || { echo "FAIL: deploy.sh does not check for an existing data claim before creating one"; fail=1; }
# runner_data_claim_check against a stubbed kubectl: absent -> 2 (create), present -> 0 (left alone,
# size and class printed, a mismatch warned about), being deleted or unreadable -> 1 (stop).
claim_check() { # $1 = what the stub answers, $2 = its exit status, $3.. = the check's arguments
  local answer="$1" status="$2"; shift 2
  kubectl() { printf '%s' "$answer"; return "$status"; }
  local rc=0
  runner_data_claim_check "$@" >"$tmp/claim.out" 2>&1 || rc=$?
  unset -f kubectl
  return "$rc"
}
rc=0; claim_check 'Error from server (NotFound): persistentvolumeclaims "blerg-runner-data" not found' 1 10Gi || rc=$?
[ "$rc" -eq 2 ] || { echo "FAIL: a missing claim is not reported as absent ($rc)"; fail=1; }
rc=0; claim_check '10Gi|microk8s-hostpath|' 0 10Gi || rc=$?
{ [ "$rc" -eq 0 ] && grep -qF '10Gi, storage class: microk8s-hostpath' "$tmp/claim.out" && ! grep -q warning "$tmp/claim.out"; } \
  || { echo "FAIL: an existing matching claim is not left alone with its size and class printed"; fail=1; }
rc=0; claim_check '10Gi|standard|' 0 20Gi fast-ssd || rc=$?
{ [ "$rc" -eq 0 ] && [ "$(grep -c warning "$tmp/claim.out")" -eq 2 ]; } \
  || { echo "FAIL: a size or class mismatch on an existing claim is not warned about"; fail=1; }
rc=0; claim_check '10Gi|standard|2026-10-16T10:00:00Z' 0 10Gi || rc=$?
{ [ "$rc" -eq 1 ] && grep -q 'being deleted' "$tmp/claim.out"; } || { echo "FAIL: a claim being deleted does not stop the deploy"; fail=1; }
rc=0; claim_check 'error: You must be logged in to the server (Unauthorized)' 1 10Gi || rc=$?
[ "$rc" -eq 1 ] || { echo "FAIL: an unreadable claim (not NotFound) does not stop the deploy ($rc)"; fail=1; }
grep -vE '^\s*#' "$here/deploy.sh" | grep -qE 'delete (pvc|persistentvolumeclaim)|blerg-runner-data.*delete' && { echo "FAIL: deploy.sh deletes the data claim"; fail=1; }

# First-run setup at the end of a deploy, against a stubbed kubectl: a fresh install prints the
# setup link from `blerg-core setup-link` (on a terminal only — the link is a credential; without
# one nothing is minted), an install with accounts prints nothing, and a failure says how to get
# the link by hand.
grep -qxF 'first_run_setup_notice' "$here/deploy.sh" || { echo "FAIL: deploy.sh does not end with the first-run setup notice"; fail=1; }
grep -q 'BLERG_BOOTSTRAP_ADMIN_PASSWORD' "$here/deploy.sh" && { echo "FAIL: deploy.sh still points at a bootstrap password in the log"; fail=1; }
setup_notice() { # $1 = stub stdout, $2 = stub stderr, $3 = stub exit status, $4 = tty (1/0)
  local o="$1" e="$2" st="$3" tty="$4"
  : > "$tmp/kubectl.calls"
  kubectl() {
    echo "$*" >> "$tmp/kubectl.calls"
    case "$*" in
      "-n blerg exec deploy/blerg-core -- /blerg-core setup-link"|"-n blerg exec deploy/blerg-core -- /blerg-core setup-link --check") ;;
      *) echo "unexpected kubectl $*" >&2; return 9 ;;
    esac
    case "$*" in *--check) ;; *) [ -n "$o" ] && echo "$o" ;; esac
    [ -n "$e" ] && echo "$e" >&2; return "$st"
  }
  stdout_is_tty() { [ "$tty" = 1 ]; }
  local rc=0
  first_run_setup_notice >"$tmp/setup.out" 2>&1 || rc=$?
  unset -f kubectl
  stdout_is_tty() { [ -t 1 ]; }
  return "$rc"
}
link='https://blerg.example/setup#code=abcDEF123'
setup_notice "$link" '' 0 1 || { echo "FAIL: setup notice failed the deploy"; fail=1; }
{ grep -qF "$link" "$tmp/setup.out" && grep -q 'FINISH SETUP' "$tmp/setup.out" && grep -q 'credential' "$tmp/setup.out"; } \
  || { echo "FAIL: a fresh install on a terminal does not print the setup link prominently"; fail=1; }
setup_notice "$link" '' 0 0 || { echo "FAIL: setup notice failed the deploy"; fail=1; }
{ ! grep -qF 'code=' "$tmp/setup.out" && grep -q 'FINISH SETUP' "$tmp/setup.out" && grep -qF '/blerg-core setup-link' "$tmp/setup.out"; } \
  || { echo "FAIL: without a terminal the link must not be printed, only how to get it"; fail=1; }
# Without a terminal nothing is minted: only the read-only --check runs, so a link the person
# already has keeps working.
[ "$(cat "$tmp/kubectl.calls")" = "-n blerg exec deploy/blerg-core -- /blerg-core setup-link --check" ] \
  || { echo "FAIL: a non-terminal deploy minted a setup code: $(cat "$tmp/kubectl.calls")"; fail=1; }
for tty in 1 0; do
  setup_notice '' 'setup is complete: an account already exists.' 1 "$tty" || { echo "FAIL: setup notice failed the deploy"; fail=1; }
  [ ! -s "$tmp/setup.out" ] || { echo "FAIL: an install with accounts must print nothing new (tty=$tty)"; fail=1; }
  setup_notice '' 'error: unable to upgrade connection' 1 "$tty" || { echo "FAIL: setup notice failed the deploy"; fail=1; }
  { grep -qF '/blerg-core setup-link' "$tmp/setup.out" && ! grep -q 'FINISH SETUP' "$tmp/setup.out"; } \
    || { echo "FAIL: an exec failure does not say how to get the link (tty=$tty)"; fail=1; }
done

# The cluster's shared Hermes provider configuration: HERMES_PROVIDERS_FILE names a local
# config.yaml fragment; deploy.sh turns it into Secret blerg/blerg-hermes-providers (key
# config.yaml) ONLY when the variable is set, the runner reads it through an optional
# secretKeyRef, and nothing about it is ever on a command line but the file's path.
grep -qF 'hermes_providers_secret "${HERMES_PROVIDERS_FILE:-}" "$script_dir"' "$here/deploy.sh" \
  || { echo "FAIL: deploy.sh does not create the shared Hermes provider Secret"; fail=1; }
grep -qE '^# HERMES_PROVIDERS_FILE=' "$here/.env.example" || { echo "FAIL: .env.example does not document HERMES_PROVIDERS_FILE"; fail=1; }
grep -qF 'secretKeyRef: { name: blerg-hermes-providers, key: config.yaml, optional: true }' "$tmp/runner-deployment.yaml" \
  || { echo "FAIL: the runner does not read the shared Hermes providers as an optional Secret"; fail=1; }
grep -A2 -F -- '- name: BLERG_RUNNER_HERMES_PROVIDERS' "$tmp/runner-deployment.yaml" | grep -qF 'blerg-hermes-providers' \
  || { echo "FAIL: BLERG_RUNNER_HERMES_PROVIDERS is not wired to blerg-hermes-providers"; fail=1; }
for doc in README.md CLUSTER-RUNTIME.md; do
  grep -qF 'HERMES_PROVIDERS_FILE' "$here/$doc" || { echo "FAIL: $doc does not document HERMES_PROVIDERS_FILE"; fail=1; }
done
[ -f "$here/hermes-providers.example.yaml" ] || { echo "FAIL: no hermes-providers.example.yaml"; fail=1; }
grep -qF 'hermes-providers.example.yaml' "$here/kustomization.yaml" && { echo "FAIL: the provider example is in the kustomization"; fail=1; }
hermes_secret() { # $1 = HERMES_PROVIDERS_FILE value, $2 = base dir; records kubectl calls
  : > "$tmp/kubectl.calls"
  kubectl() { echo "$*" >> "$tmp/kubectl.calls"; cat >/dev/null; return 0; }
  local rc=0
  hermes_providers_secret "$1" "$2" >"$tmp/hermes.out" 2>&1 </dev/null || rc=$?
  unset -f kubectl
  return "$rc"
}
hdir="$tmp/hermes"; mkdir -m 700 "$hdir"
printf 'providers:\n  local-a:\n    base_url: http://192.0.2.10:8000/v1\n    api_key: fixture-provider-secret\n' > "$hdir/providers.yaml"
hermes_secret '' "$hdir" || { echo "FAIL: an unset HERMES_PROVIDERS_FILE failed the deploy"; fail=1; }
[ ! -s "$tmp/kubectl.calls" ] || { echo "FAIL: an unset HERMES_PROVIDERS_FILE still touched the cluster: $(cat "$tmp/kubectl.calls")"; fail=1; }
hermes_secret "$hdir/providers.yaml" / || { echo "FAIL: a valid provider file was refused: $(cat "$tmp/hermes.out")"; fail=1; }
grep -qF -- "-n blerg create secret generic blerg-hermes-providers --from-file=config.yaml=$hdir/providers.yaml --dry-run=client -o yaml" "$tmp/kubectl.calls" \
  || { echo "FAIL: the provider Secret is not created from the file: $(cat "$tmp/kubectl.calls")"; fail=1; }
grep -qxF -- 'apply -f -' "$tmp/kubectl.calls" || { echo "FAIL: the provider Secret is not applied"; fail=1; }
if grep -q 'fixture-provider-secret\|192.0.2.10' "$tmp/kubectl.calls" "$tmp/hermes.out"; then echo "FAIL: provider file contents reached a command line or the log"; fail=1; fi
hermes_secret providers.yaml "$hdir" || { echo "FAIL: a path relative to install/k8s was refused"; fail=1; }
grep -qF -- "--from-file=config.yaml=$hdir/providers.yaml" "$tmp/kubectl.calls" || { echo "FAIL: a relative path is not resolved against install/k8s"; fail=1; }
: > "$hdir/empty.yaml"
head -c 65537 /dev/zero | tr '\0' '#' > "$hdir/big.yaml"
head -c 65536 /dev/zero | tr '\0' '#' > "$hdir/max.yaml"
hermes_secret "$hdir/max.yaml" / || { echo "FAIL: a 64 KiB provider file was refused"; fail=1; }
hermes_secret "$hdir/big.yaml" / || true; grep -qF '64 KiB' "$tmp/hermes.out" || { echo "FAIL: an oversized provider file does not say the 64 KiB limit"; fail=1; }
for bad in "$hdir/missing.yaml" "$hdir/empty.yaml" "$hdir/big.yaml" "$hdir"; do
  if hermes_secret "$bad" /; then echo "FAIL: HERMES_PROVIDERS_FILE=$bad was accepted"; fail=1; fi
  [ ! -s "$tmp/kubectl.calls" ] || { echo "FAIL: a refused provider file still touched the cluster ($bad)"; fail=1; }
done
# Pods reaching LAN model servers is an opt-in, commented rule naming one placeholder host, never
# an opening of the private ranges.
grep -qE '^\s+#\s+cidr: 192\.0\.2\.10/32' "$here/networkpolicy-sessions.example.yaml" \
  || { echo "FAIL: networkpolicy-sessions.example.yaml has no opt-in rule for local model servers"; fail=1; }
grep -vE '^\s*#' "$here/networkpolicy-sessions.example.yaml" | grep -qE '192\.0\.2\.' && { echo "FAIL: the model-server rule is active by default"; fail=1; }

if [ -n "${BLERG_SKIP_KUBECTL:-}" ]; then
  echo "skip: BLERG_SKIP_KUBECTL set (dry-run not exercised)"
elif command -v kubectl >/dev/null 2>&1; then
  kubectl apply -k "$tmp" --dry-run=client -o name >/dev/null || { echo "FAIL: kustomize dry-run"; fail=1; }
  kubectl apply -f "$here/cluster-runtime.yaml" --dry-run=client -o name >/dev/null \
    || { echo "FAIL: cluster-runtime.yaml dry-run"; fail=1; }
  printf '%s\n' "$claim" | kubectl apply -f - --dry-run=client -o name >/dev/null \
    || { echo "FAIL: runner-data-pvc.yaml dry-run"; fail=1; }
else
  echo "skip: kubectl not on PATH (dry-run not exercised)"
fi
[ "$fail" -eq 0 ] && echo "ok"
exit "$fail"
