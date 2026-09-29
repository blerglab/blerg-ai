#!/usr/bin/env bash
# Builds the Blerg images, pushes them to your own registry, generates the
# blerg-secrets Secret, and applies the install/k8s kustomization. Cluster
# runtime is on by default: the agent image, the blerg-runner-sessions
# namespace/RBAC and the blerg-runner-agent Secret are all set up too — set
# CLUSTER_RUNTIME=off to skip them.
#
# Usage:
#   cd install/k8s
#   cp .env.example .env && $EDITOR .env   # set REGISTRY/TAG/DOMAIN
#   ./deploy.sh [--yes]
#
# Before building or applying anything, deploy.sh prints the kubectl context
# and cluster it will act on and asks for confirmation. --yes (or -y), or
# BLERG_ASSUME_YES=1, skips the question. A run with no terminal on stdin (an agent,
# CI, ssh without a tty) is REFUSED unless it passes one of those explicitly.
#
# Or without a .env file, export REGISTRY/TAG/DOMAIN (and optional SECRET_*
# overrides) before running. See .env.example for what's recognized.
#
# Requires: docker, kubectl (with a working context pointed at your cluster).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
# shellcheck source=deploy_lib.sh
. "$script_dir/deploy_lib.sh"

for arg in "$@"; do
  case "$arg" in
    --yes|-y) ;;
    *) echo "usage: $0 [--yes]" >&2; exit 2 ;;
  esac
done

# One private scratch directory (mode 0700) for rendered manifests and the Secret
# files below; removed on every exit path.
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

if [ -f "$script_dir/.env" ]; then
  # shellcheck disable=SC1090
  set -a; source "$script_dir/.env"; set +a
fi

REGISTRY="${REGISTRY:-localhost:32000}"
TAG="${TAG:-latest}"
DOMAIN="${DOMAIN:-blerg.local}"
# Optional: name of a cert-manager ClusterIssuer already installed in your
# cluster (e.g. a Let's Encrypt issuer). When set, deploy.sh requests a TLS
# cert for the ingress via cert-manager's ingress-shim and BLERG_CORE_*'s
# origin/audience values switch from http:// to https:// to match. Leave
# unset for plain HTTP (the previous, still-default behavior).
CLUSTER_ISSUER="${CLUSTER_ISSUER:-}"
SCHEME="${SCHEME:-$([ -n "$CLUSTER_ISSUER" ] && echo https || echo http)}"
# Cluster runtime (per-session agent pods) is on by default: users bring their
# own engine credentials in Settings, so a default install needs no operator
# secrets at all. Set CLUSTER_RUNTIME=off (or false/0/no) to skip the agent
# image, the agent Secret, the sessions namespace and its RBAC entirely.
CLUSTER_RUNTIME="${CLUSTER_RUNTIME:-on}"
# Set to 1 (or true/yes) when you manage the blerg-runner-agent Secret yourself (sealed
# secrets, external-secrets, a manual kubectl apply) — deploy.sh then leaves it
# completely alone. See cluster-agent-secret.example.yaml.
AGENT_SECRET_EXTERNAL="${AGENT_SECRET_EXTERNAL:-}"
AGENT_NAMESPACE=blerg-runner-sessions

# Normalise both switches to a single canonical value each, so an operator
# writing off/false/0/no (or 1/true/yes) gets what they obviously meant. The
# "external" switch in particular must fail CLOSED on an unrecognized value:
# anything we don't recognize means deploy.sh manages the Secret, which is the
# safe, self-healing default.
lower() { printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]'; }
case "$(lower "$CLUSTER_RUNTIME")" in
  off|false|0|no) cluster_runtime=off ;;
  *) cluster_runtime=on ;;
esac
case "$(lower "$AGENT_SECRET_EXTERNAL")" in
  1|true|yes) agent_secret_external=yes ;;
  *) agent_secret_external=no ;;
esac
# Completion webhooks refuse private/loopback callback targets unless this
# renders "true" into BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE. Needed on installs
# whose public hostnames resolve to a private network (a homelab behind NAT).
case "$(lower "${WEBHOOK_ALLOW_PRIVATE:-}")" in
  1|true|yes) WEBHOOK_ALLOW_PRIVATE=true ;;
  *) WEBHOOK_ALLOW_PRIVATE=false ;;
esac

if [ "$cluster_runtime" = off ]; then
  AGENT_IMAGE=""
else
  AGENT_IMAGE="${REGISTRY}/blerg-runner-agent:${TAG}"
fi

command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl is required" >&2; exit 1; }
# envsubst renders the manifests and openssl generates BLERG_CORE_LOCAL_KEY on a first deploy —
# check both up front, before any image is built or pushed, instead of failing half-way.
command -v envsubst >/dev/null || { echo "envsubst is required (package: gettext / gettext-base)" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl is required (generates BLERG_CORE_LOCAL_KEY on a first deploy)" >&2; exit 1; }

# Say which cluster this will change, and make the operator agree, before anything is
# built, pushed or applied.
kube_context="$(kubectl config current-context 2>/dev/null || true)"
[ -n "$kube_context" ] || { echo "kubectl has no current context; point it at your cluster first" >&2; exit 1; }
kube_server="$(kubectl config view --minify -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null || true)"
echo "============================================================"
echo "  kubectl context: ${kube_context}"
echo "  cluster server:  ${kube_server:-unknown}"
echo "  This will push images to ${REGISTRY} and change namespace/blerg"
echo "  on that cluster."
echo "============================================================"
if deploy_must_refuse "$@"; then
  echo "refusing to deploy: there is no terminal to confirm the cluster above on." >&2
  echo "Check that context and cluster are the ones you mean, then re-run with --yes" >&2
  echo "(or BLERG_ASSUME_YES=1) to say so explicitly." >&2
  exit 2
fi
if deploy_needs_confirmation "$@"; then
  printf 'Deploy to this cluster? [y/N] '
  read -r answer || answer=""
  case "$answer" in
    y|Y|yes|YES) ;;
    *) echo "aborted" >&2; exit 1 ;;
  esac
fi

rand() { openssl rand -hex 24 2>/dev/null || head -c 48 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 48; }
rand_b64() { openssl rand -base64 32; }

echo "==> Building images (registry=${REGISTRY}, tag=${TAG})"
docker build -f "$repo_root/core/Dockerfile" --build-arg VERSION="$TAG" -t "${REGISTRY}/blerg-core:${TAG}" "$repo_root"
docker build -f "$repo_root/board/Dockerfile" --build-arg VERSION="$TAG" -t "${REGISTRY}/blerg-board:${TAG}" "$repo_root"
docker build -f "$repo_root/runner/Dockerfile.server" --build-arg VERSION="$TAG" -t "${REGISTRY}/blerg-runner:${TAG}" "$repo_root"
if [ -n "$AGENT_IMAGE" ]; then
  # No --build-arg VERSION here: Dockerfile.devcontainer declares no ARG
  # VERSION, and passing an unused build arg only earns a docker warning.
  docker build -f "$repo_root/runner/Dockerfile.devcontainer" -t "$AGENT_IMAGE" "$repo_root"
fi

echo "==> Pushing images to ${REGISTRY}"
docker push "${REGISTRY}/blerg-core:${TAG}"
docker push "${REGISTRY}/blerg-board:${TAG}"
docker push "${REGISTRY}/blerg-runner:${TAG}"
[ -n "$AGENT_IMAGE" ] && docker push "$AGENT_IMAGE"

echo "==> Ensuring namespace/blerg exists"
kubectl get namespace blerg >/dev/null 2>&1 || kubectl create namespace blerg

echo "==> Generating blerg-secrets (existing values are pinned via SECRET_* env vars, otherwise reused from the live Secret, otherwise randomized)"
# Re-running this script (e.g. to roll out a new TAG) must NEVER rotate a
# value Postgres or a component already has on disk out from under it — a
# fresh random POSTGRES_PASSWORD on a second run breaks auth against a
# Postgres data directory that already initialized with the first run's
# password, and crash-loops every component. So every value here is:
# an explicit SECRET_* override > the value already live in the cluster's
# blerg-secrets Secret > (only if this is a genuinely first deploy) random.
secret_exists=false
kubectl get secret blerg-secrets -n blerg >/dev/null 2>&1 && secret_exists=true || true

existing() { # $1 = secret key
  [ "$secret_exists" = true ] || return 0
  kubectl get secret blerg-secrets -n blerg -o "jsonpath={.data.$1}" 2>/dev/null | base64 -d 2>/dev/null || true
}

pick() { # $1 = override value (may be empty), $2 = secret key to fall back to, else random
  [ -n "$1" ] && { echo "$1"; return; }
  cur="$(existing "$2")"
  [ -n "$cur" ] && { echo "$cur"; return; }
  rand
}

pick_b64() { # same as pick, but falls back to a base64-encoded 32-byte random value
  [ -n "$1" ] && { echo "$1"; return; }
  cur="$(existing "$2")"
  [ -n "$cur" ] && { echo "$cur"; return; }
  rand_b64
}

POSTGRES_PASSWORD="$(pick "${SECRET_POSTGRES_PASSWORD:-}" POSTGRES_PASSWORD)"
BLERG_CORE_REGISTER_KEY="$(pick "${SECRET_BLERG_CORE_REGISTER_KEY:-}" BLERG_CORE_REGISTER_KEY)"
BLERG_BOARD_SERVICE_KEY="$(pick "${SECRET_BLERG_BOARD_SERVICE_KEY:-}" BLERG_BOARD_SERVICE_KEY)"
BLERG_RUNNER_KEY="$(pick "${SECRET_BLERG_RUNNER_KEY:-}" BLERG_RUNNER_KEY)"
BLERG_RUNNER_DAEMON_TOKEN="$(pick "${SECRET_BLERG_RUNNER_DAEMON_TOKEN:-}" BLERG_RUNNER_DAEMON_TOKEN)"
# Shared static secret core and runner both need the same value for — core
# validates it on internal endpoints, runner presents it back. Same shape as
# every other pick() key: SECRET_* override > live value > random.
BLERG_CORE_INTERNAL_KEY="$(pick "${SECRET_BLERG_CORE_INTERNAL_KEY:-}" BLERG_CORE_INTERNAL_KEY)"
# core's local KMS key: base64 of 32 random bytes, not a hex token.
BLERG_CORE_LOCAL_KEY="$(pick_b64 "${SECRET_BLERG_CORE_LOCAL_KEY:-}" BLERG_CORE_LOCAL_KEY)"
# board's at-rest key for automation tokens: base64 of 32 random bytes, same precedence. A redeploy
# keeps the live value — rotating it would make every stored automation token undecryptable.
BLERG_BOARD_SECRET_KEY="$(pick_b64 "${SECRET_BLERG_BOARD_SECRET_KEY:-}" BLERG_BOARD_SECRET_KEY)"
# ANTHROPIC_API_KEY/GITHUB_TOKEN are allowed to be genuinely empty (the
# features they back are simply unavailable until set) — never randomized.
ANTHROPIC_API_KEY="${SECRET_ANTHROPIC_API_KEY:-$(existing ANTHROPIC_API_KEY)}"
BLERG_CORE_GITHUB_TOKEN="${SECRET_BLERG_CORE_GITHUB_TOKEN:-$(existing BLERG_CORE_GITHUB_TOKEN)}"
BLERG_RUNNER_GITHUB_TOKEN="${SECRET_BLERG_RUNNER_GITHUB_TOKEN:-$(existing BLERG_RUNNER_GITHUB_TOKEN)}"

# Values go to kubectl through private files, never as command-line arguments (which any
# local user can read from the process list). Optional, possibly-empty tokens are
# omitted entirely rather than set to "" — the deployments' `optional: true`
# secretKeyRefs only mean anything if an unset value is actually absent from the Secret,
# not present-but-empty (M11). See deploy_lib.sh.
secret_args_begin "$work_dir"
secret_add POSTGRES_PASSWORD "$POSTGRES_PASSWORD"
secret_add DATABASE_URL_CORE "postgres://blerg:${POSTGRES_PASSWORD}@postgres:5432/blerg_core?sslmode=disable"
secret_add DATABASE_URL_BOARD "postgres://blerg:${POSTGRES_PASSWORD}@postgres:5432/blerg_board?sslmode=disable"
secret_add BLERG_RUNNER_DATABASE_URL "postgres://blerg:${POSTGRES_PASSWORD}@postgres:5432/blerg_runner?sslmode=disable"
secret_add BLERG_CORE_REGISTER_KEY "$BLERG_CORE_REGISTER_KEY"
secret_add BLERG_BOARD_SERVICE_KEY "$BLERG_BOARD_SERVICE_KEY"
secret_add BLERG_RUNNER_KEY "$BLERG_RUNNER_KEY"
secret_add BLERG_RUNNER_DAEMON_TOKEN "$BLERG_RUNNER_DAEMON_TOKEN"
secret_add BLERG_CORE_INTERNAL_KEY "$BLERG_CORE_INTERNAL_KEY"
secret_add BLERG_CORE_LOCAL_KEY "$BLERG_CORE_LOCAL_KEY"
secret_add BLERG_BOARD_SECRET_KEY "$BLERG_BOARD_SECRET_KEY"
[ -n "$ANTHROPIC_API_KEY" ] && secret_add ANTHROPIC_API_KEY "$ANTHROPIC_API_KEY"
[ -n "$BLERG_CORE_GITHUB_TOKEN" ] && secret_add BLERG_CORE_GITHUB_TOKEN "$BLERG_CORE_GITHUB_TOKEN"
[ -n "$BLERG_RUNNER_GITHUB_TOKEN" ] && secret_add BLERG_RUNNER_GITHUB_TOKEN "$BLERG_RUNNER_GITHUB_TOKEN"

kubectl create secret generic blerg-secrets \
  --namespace blerg "${SECRET_ARGS[@]}" \
  --dry-run=client -o yaml | kubectl apply -f -

# The shared Secret cluster-runtime session pods mount. It deliberately needs
# NO human credential to be useful: BLERG_RUNNER_DAEMON_TOKEN (which the pod
# uses to authenticate back to blerg-runner) is the only required key, and it
# is the same value blerg-secrets already holds. Engine credentials are
# optional here — users add their own in Settings, and the runner injects them
# per session — so each one is only written when an operator explicitly
# supplies it, on the same SECRET_* override > live value > omitted ladder as
# blerg-secrets (M11: an absent key must be absent, not present-but-empty).
if [ "$cluster_runtime" = off ]; then
  echo "==> Skipping agent Secret (CLUSTER_RUNTIME=off)"
elif [ "$agent_secret_external" = yes ]; then
  echo "==> Skipping agent Secret (AGENT_SECRET_EXTERNAL set — you manage ${AGENT_NAMESPACE}/blerg-runner-agent yourself)"
else
  echo "==> Ensuring namespace/${AGENT_NAMESPACE} exists"
  # cluster-runtime.yaml declares this namespace too, but that is applied
  # further down — the Secret below needs it to exist now.
  kubectl create namespace "$AGENT_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

  echo "==> Generating ${AGENT_NAMESPACE}/blerg-runner-agent"
  agent_secret_exists=false
  kubectl get secret blerg-runner-agent -n "$AGENT_NAMESPACE" >/dev/null 2>&1 && agent_secret_exists=true || true

  existing_agent() { # $1 = secret key
    [ "$agent_secret_exists" = true ] || return 0
    kubectl get secret blerg-runner-agent -n "$AGENT_NAMESPACE" -o "jsonpath={.data.$1}" 2>/dev/null | base64 -d 2>/dev/null || true
  }

  # Must match blerg-secrets' value — blerg-runner validates one daemon token,
  # not a per-namespace one.
  secret_args_begin "$work_dir"
  secret_add BLERG_RUNNER_DAEMON_TOKEN "$BLERG_RUNNER_DAEMON_TOKEN"
  AGENT_ANTHROPIC_API_KEY="${SECRET_ANTHROPIC_API_KEY:-$(existing_agent ANTHROPIC_API_KEY)}"
  AGENT_CLAUDE_CODE_OAUTH_TOKEN="${SECRET_CLAUDE_CODE_OAUTH_TOKEN:-$(existing_agent CLAUDE_CODE_OAUTH_TOKEN)}"
  AGENT_BLERG_RUNNER_GIT_TOKEN="${SECRET_BLERG_RUNNER_GIT_TOKEN:-$(existing_agent BLERG_RUNNER_GIT_TOKEN)}"
  AGENT_CODEX_AUTH_JSON="${SECRET_CODEX_AUTH_JSON:-$(existing_agent CODEX_AUTH_JSON)}"
  AGENT_HERMES_ENV_CONTENTS="${SECRET_HERMES_ENV_CONTENTS:-$(existing_agent HERMES_ENV_CONTENTS)}"
  [ -n "$AGENT_ANTHROPIC_API_KEY" ] && secret_add ANTHROPIC_API_KEY "$AGENT_ANTHROPIC_API_KEY"
  [ -n "$AGENT_CLAUDE_CODE_OAUTH_TOKEN" ] && secret_add CLAUDE_CODE_OAUTH_TOKEN "$AGENT_CLAUDE_CODE_OAUTH_TOKEN"
  [ -n "$AGENT_BLERG_RUNNER_GIT_TOKEN" ] && secret_add BLERG_RUNNER_GIT_TOKEN "$AGENT_BLERG_RUNNER_GIT_TOKEN"
  [ -n "$AGENT_CODEX_AUTH_JSON" ] && secret_add CODEX_AUTH_JSON "$AGENT_CODEX_AUTH_JSON"
  [ -n "$AGENT_HERMES_ENV_CONTENTS" ] && secret_add HERMES_ENV_CONTENTS "$AGENT_HERMES_ENV_CONTENTS"

  kubectl create secret generic blerg-runner-agent \
    --namespace "$AGENT_NAMESPACE" "${SECRET_ARGS[@]}" \
    --dry-run=client -o yaml | kubectl apply -f -
fi

echo "==> Rendering manifests (REGISTRY=${REGISTRY} TAG=${TAG} DOMAIN=${DOMAIN}) and applying"
render_dir="$work_dir/render"
mkdir "$render_dir"
# Render only the files kustomization.yaml actually lists (M10) — copying
# every *.yaml in the directory also swept up template-only files like
# secret.example.yaml and cluster-runtime.yaml that were never meant to be
# applied by this script.
for f in $(grep -E '^\s+- ' "$script_dir/kustomization.yaml" | sed 's/^\s*- //'); do
  cp "$script_dir/$f" "$render_dir/"
done
cp "$script_dir/kustomization.yaml" "$render_dir/"
for f in "$render_dir"/*.yaml; do
  REGISTRY="$REGISTRY" TAG="$TAG" DOMAIN="$DOMAIN" SCHEME="$SCHEME" AGENT_IMAGE="$AGENT_IMAGE" WEBHOOK_ALLOW_PRIVATE="$WEBHOOK_ALLOW_PRIVATE" \
    envsubst '${REGISTRY} ${TAG} ${DOMAIN} ${SCHEME} ${AGENT_IMAGE} ${WEBHOOK_ALLOW_PRIVATE}' < "$f" > "$f.rendered"
  mv "$f.rendered" "$f"
done

# cluster-runtime.yaml is applied directly, NOT via the kustomization: that
# kustomization sets `namespace: blerg`, and kustomize's namespace transformer
# would rewrite this file's Role/RoleBinding (and the RoleBinding's
# ServiceAccount subject namespace) into `blerg`, breaking the RBAC that only
# works scoped to the sessions namespace. It contains no ${...} placeholders,
# so it needs no rendering either.
if [ "$cluster_runtime" = off ]; then
  echo "==> Skipping cluster-runtime namespace/RBAC (CLUSTER_RUNTIME=off)"
else
  echo "==> Applying cluster-runtime namespace/RBAC"
  kubectl apply -f "$script_dir/cluster-runtime.yaml"
fi

kubectl apply -k "$render_dir"
# env-from-ConfigMap values are read once, at process start. A re-run with an
# unchanged TAG leaves the Deployment's pod spec byte-identical, so `apply -k`
# changes only the ConfigMap and nothing restarts — the runner would keep
# running with the old (or absent) cluster-runtime keys. Restart it
# unconditionally: it's cheap and always correct.
kubectl -n blerg rollout restart deploy/blerg-runner
kubectl -n blerg rollout status deploy/blerg-core deploy/blerg-board deploy/blerg-runner --timeout=180s

if [ -n "$CLUSTER_ISSUER" ]; then
  echo "==> Requesting a TLS cert via ClusterIssuer ${CLUSTER_ISSUER} (cert-manager ingress-shim)"
  kubectl annotate ingress blerg -n blerg "cert-manager.io/cluster-issuer=${CLUSTER_ISSUER}" --overwrite
  kubectl patch ingress blerg -n blerg --type merge -p \
    "{\"spec\":{\"tls\":[{\"hosts\":[\"${DOMAIN}\",\"board.${DOMAIN}\",\"runner.${DOMAIN}\"],\"secretName\":\"blerg-tls\"}]}}"
else
  # Idempotent the other way too: if CLUSTER_ISSUER was set on a previous run
  # and is now unset, remove the cert-manager annotation and any spec.tls a
  # prior run left behind — otherwise ingress-shim keeps renewing a cert
  # nobody asked for any more, and SCHEME=http traffic still gets redirected
  # by a stale TLS block.
  kubectl annotate ingress blerg -n blerg cert-manager.io/cluster-issuer- >/dev/null 2>&1 || true
  kubectl patch ingress blerg -n blerg --type json -p '[{"op":"remove","path":"/spec/tls"}]' >/dev/null 2>&1 || true
fi

echo "==> Done. See README.md for how to reach the landing page and connect the runner daemon."
if [ "$cluster_runtime" = off ]; then
  echo "==> Cluster runtime: off (CLUSTER_RUNTIME=off)"
else
  echo "==> Cluster runtime: on (users add their own credentials at ${SCHEME}://${DOMAIN}/settings)"
fi
echo "==> First login (bootstrap admin password — printed only by the FIRST boot that created the account):"
echo "    kubectl -n blerg logs deploy/blerg-core | grep BLERG_BOOTSTRAP_ADMIN_PASSWORD"
echo "    If that log is gone, mint a fresh one-time password instead:"
echo "    kubectl -n blerg exec deploy/blerg-core -- /blerg-core users set-password --subject <username>"
