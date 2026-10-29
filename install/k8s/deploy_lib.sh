#!/usr/bin/env bash
# Helpers sourced by deploy.sh (and exercised without a cluster by deploy_test.sh).
# kubectl is only ever called as a plain command, so deploy_test.sh can stub it and run anywhere.

# Capture explicit legacy aliases before deploy.sh supplies their defaults.
# Exact BLERG_RUNNER_* names win when both forms are supplied.
runner_config_aliases() {
  local pair alias_name key value
  for pair in TRUSTED_PROXIES:BLERG_RUNNER_TRUSTED_PROXIES \
              WEBHOOK_ALLOW_PRIVATE:BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE \
              WORKSPACE_STORAGE_CLASS:BLERG_RUNNER_WORKSPACE_STORAGE_CLASS; do
    alias_name="${pair%%:*}"; key="${pair#*:}"
    if [[ -v "$alias_name" && ! -v "$key" ]]; then
      value="${!alias_name}"
      if [ "$alias_name" = WEBHOOK_ALLOW_PRIVATE ]; then
        case "${value,,}" in 1|true|yes) value=true ;; *) value=false ;; esac
      fi
      printf -v "$key" '%s' "$value"
      export "$key"
    fi
  done
  return 0
}

# Image selection belongs to this deploy, not the previous live ConfigMap:
# advance TAG and honor the off switch even when an older image is present.
runner_config_agent_image() {
  if [ "$1" = off ]; then
    AGENT_IMAGE=""
  else
    AGENT_IMAGE="${BLERG_RUNNER_AGENT_IMAGE-$2/blerg-runner-agent:$3}"
  fi
  export BLERG_RUNNER_AGENT_IMAGE="$AGENT_IMAGE"
}

# --- Confirmation before touching a cluster -------------------------------------------

# True when stdin is a terminal. A function so tests can override it.
stdin_is_tty() { [ -t 0 ]; }
stdout_is_tty() { [ -t 1 ]; }

# deploy_assumes_yes [--yes|-y ...]: succeeds when the operator already said yes, by
# --yes/-y or BLERG_ASSUME_YES=1/true/yes.
deploy_assumes_yes() {
  local a
  for a in "$@"; do
    case "$a" in --yes|-y) return 0 ;; esac
  done
  case "$(printf '%s' "${BLERG_ASSUME_YES:-}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes) return 0 ;;
  esac
  return 1
}

# deploy_needs_confirmation [--yes|-y ...]: succeeds when there is a terminal to ask
# "is this the right cluster?" on and the operator has not already said yes.
deploy_needs_confirmation() {
  deploy_assumes_yes "$@" && return 1
  stdin_is_tty
}

# deploy_must_refuse [--yes|-y ...]: succeeds when the deploy must NOT go ahead: there is
# no terminal to ask on and nobody said yes. A non-interactive run (an agent, CI, ssh
# without a tty) has to opt in explicitly, so it can never deploy to whichever cluster
# kubectl happens to point at by accident.
deploy_must_refuse() {
  deploy_assumes_yes "$@" && return 1
  stdin_is_tty && return 1
  return 0
}

# --- Cluster authorization safety notice -----------------------------------------------

# Probe a permission no Blerg Role grants, using the runner's identity and normal SA groups.
# A grant can mean AlwaysAllow or overly broad operator-managed bindings; either defeats our
# namespace boundary. This works before the ServiceAccount exists on a first install.
# This is advisory: errors (including an operator who cannot impersonate) are inconclusive,
# never evidence of enforced RBAC, and neither errors nor grants add a confirmation prompt.
cluster_authorization_notice() {
  local answer status=0
  answer="$(kubectl auth can-i get secrets --namespace=kube-system \
    --as=system:serviceaccount:blerg:blerg-runner-agent-launcher \
    --as-group=system:serviceaccounts --as-group=system:serviceaccounts:blerg \
    --as-group=system:authenticated --request-timeout=10s 2>/dev/null)" || status=$?
  if [ "$status" -eq 0 ] && [ "$answer" = yes ]; then
    echo "WARNING: the runner service account can get secrets in kube-system." >&2
    echo "Cluster authorization is effectively AlwaysAllow or has overly broad grants." >&2
    echo "Blerg's RBAC boundaries (no pods/exec, namespace-scoped Roles) are not enforced." >&2
    echo "Enable RBAC (on MicroK8s: microk8s enable rbac) and remove overly broad grants," >&2
    echo "then rerun this installer to check again. Continuing deployment." >&2
  elif { [ "$status" -eq 0 ] || [ "$status" -eq 1 ]; } && [ "$answer" = no ]; then
    : # Expected denial. This probe does not certify every permission in the cluster.
  else
    echo "WARNING: could not verify cluster authorization as the runner service account." >&2
    echo "Check kubectl connectivity and impersonation permissions, then rerun this installer." >&2
    echo "Ensure RBAC is enabled (on MicroK8s: microk8s enable rbac). Continuing deployment." >&2
  fi
  return 0
}

# --- Building a Secret without secret values on any command line ------------------------
#
# kubectl's --from-literal=KEY=value puts the value in the process list. Instead the
# values go into a private file read with --from-env-file. That format is one KEY=VALUE
# per line with no quoting: everything after the first "=" is the value, byte for byte
# ("=", "#", spaces, quotes and base64 padding are all literal), but a value cannot
# contain a newline. A value that does (a JSON document, the contents of a .env file) is
# written to its own private file and passed with --from-file=KEY=path, which also keeps
# the bytes exactly. Either way the same keys and values end up in the Secret.
#
#   secret_args_begin <private-dir>   start a Secret (dir must exist, mode 0700)
#   secret_add KEY VALUE              add one key
#   "${SECRET_ARGS[@]}"               the kubectl arguments describing the keys added
#
# The caller owns the directory and removes it on exit (trap).

secret_args_begin() {
  SECRET_DIR="$1"
  [ -d "$SECRET_DIR" ] || { echo "secret_args_begin: $SECRET_DIR is not a directory" >&2; return 1; }
  SECRET_SEQ=$((${SECRET_SEQ:-0} + 1))
  SECRET_ENV_FILE="$SECRET_DIR/secret-$SECRET_SEQ.env"
  ( umask 077; : > "$SECRET_ENV_FILE" )
  chmod 600 "$SECRET_ENV_FILE"
  SECRET_ARGS=("--from-env-file=$SECRET_ENV_FILE")
}

secret_add() {
  local key="$1" value="$2"
  case "$key" in
    ''|[0-9]*|*[!A-Za-z0-9_]*)
      echo "secret_add: invalid Secret key name: $key" >&2; return 1 ;;
  esac
  case "$value" in
    *$'\n'*|*$'\r'*)
      # Not representable on one env-file line. Keep exact bytes in a file instead.
      local f="$SECRET_DIR/secret-$SECRET_SEQ.$key"
      ( umask 077; printf '%s' "$value" > "$f" )
      chmod 600 "$f"
      SECRET_ARGS+=("--from-file=$key=$f")
      ;;
    *)
      printf '%s=%s\n' "$key" "$value" >> "$SECRET_ENV_FILE"
      ;;
  esac
}

# --- The runner's data volume ---------------------------------------------------------------
#
# runner-data-pvc.yaml is rendered here rather than through the kustomization: the claim is
# created once and then left alone (its size and class are fixed once it is bound), and an
# optional storage class has to add a line, not fill one in.

# runner_data_size_ok SIZE: a Kubernetes quantity such as 10Gi or 500Mi.
runner_data_size_ok() {
  [[ "${1:-}" =~ ^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$ ]]
}

# runner_data_class_ok CLASS: empty (the cluster's default StorageClass) or a StorageClass name.
runner_data_class_ok() {
  [ -z "${1:-}" ] || [[ "$1" =~ ^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$ ]]
}

# runner_data_claim_render MANIFEST SIZE [CLASS]: prints the claim with its size, and with
# `storageClassName: CLASS` under spec when CLASS is set.
runner_data_claim_render() {
  local manifest="$1" size="$2" class="${3:-}"
  runner_data_size_ok "$size" || { echo "RUNNER_DATA_SIZE is not a Kubernetes quantity (e.g. 10Gi): $size" >&2; return 1; }
  runner_data_class_ok "$class" || { echo "RUNNER_DATA_STORAGE_CLASS is not a StorageClass name: $class" >&2; return 1; }
  RUNNER_DATA_SIZE="$size" envsubst '${RUNNER_DATA_SIZE}' < "$manifest" |
    awk -v class="$class" '{ print } class != "" && /^spec:[[:space:]]*$/ { print "  storageClassName: " class }'
}

# runner_data_claim_check WANT_SIZE [WANT_CLASS]: what deploy.sh does about an existing claim.
#   0  the claim exists and can be used: its size and class are printed, with a warning when they
#      differ from the settings (which only apply when the claim is created)
#   2  there is no claim: create it
#   1  stop: the claim is being deleted, or kubectl could not say whether it exists (never apply
#      over a live claim on a guess)
runner_data_claim_check() {
  local want_size="$1" want_class="${2:-}" out size class deleting
  if ! out="$(kubectl -n blerg get persistentvolumeclaim blerg-runner-data \
      -o 'jsonpath={.spec.resources.requests.storage}{"|"}{.spec.storageClassName}{"|"}{.metadata.deletionTimestamp}' 2>&1)"; then
    case "$out" in
      *NotFound*|*"not found"*) return 2 ;;
    esac
    echo "cannot read persistentvolumeclaim/blerg-runner-data, so not touching it: $out" >&2
    return 1
  fi
  IFS='|' read -r size class deleting <<<"$out"
  if [ -n "$deleting" ]; then
    echo "persistentvolumeclaim/blerg-runner-data is being deleted (since $deleting); wait until it is gone, then re-run deploy.sh" >&2
    return 1
  fi
  echo "==> Runner data claim blerg-runner-data exists (${size:-unknown size}, storage class: ${class:-none}); leaving it as it is"
  if [ "$size" != "$want_size" ]; then
    echo "    warning: RUNNER_DATA_SIZE=$want_size differs from the claim's $size; it applies only when the claim is created (README.md, \"Runner data volume\")" >&2
  fi
  if [ -n "$want_class" ] && [ "$class" != "$want_class" ]; then
    echo "    warning: RUNNER_DATA_STORAGE_CLASS=$want_class differs from the claim's ${class:-none}; a bound claim's class cannot change" >&2
  fi
  return 0
}

# --- Workspace storage ------------------------------------------------------------------------
#
# Cluster workspace claims use "standard" storage unless WORKSPACE_STORAGE_CLASS names a
# size-limited class ("bounded"). Standard claims go in the class Blerg's own data claim uses, so
# a cluster that can run Blerg can run workspaces. See README.md, "Workspace storage".

# workspace_default_class: prints the class for standard workspace claims
# (BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS). An explicit BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS (from
# .env or the environment, even empty) is used as it is; otherwise it is the class of the
# blerg-runner-data claim as stored on the object, read without waiting for it to bind (a new
# claim gets the cluster's default class when it is created). Prints nothing when the claim has
# no class. Returns 1 (stop) when the claim cannot be read or the class is not a class name:
# deploying on a guess would point every new workspace at the wrong storage.
workspace_default_class() {
  local out
  if [[ -v BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS ]]; then
    runner_data_class_ok "$BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS" \
      || { echo "BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS is not a StorageClass name: $BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS" >&2; return 1; }
    printf '%s' "$BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS"
    return 0
  fi
  if ! out="$(kubectl -n blerg get persistentvolumeclaim blerg-runner-data -o 'jsonpath={.spec.storageClassName}' 2>&1)"; then
    echo "cannot read the storage class of persistentvolumeclaim/blerg-runner-data, so not choosing one for workspaces: $out" >&2
    return 1
  fi
  if ! runner_data_class_ok "$out"; then
    echo "persistentvolumeclaim/blerg-runner-data reports an unexpected storage class, so not choosing one for workspaces: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

# rendered_config_value FILE KEY: prints KEY's value from a rendered blerg-config ConfigMap
# (one `  KEY: "json string"` line, as render_runner_config.py and the template write it).
# Prints nothing for a missing key; returns 1 when the file or the value cannot be read.
rendered_config_value() {
  python3 - "$1" "$2" <<'PY'
import json, re, sys
path, key = sys.argv[1:]
for line in open(path, encoding="utf-8"):
    m = re.match(r"^  (\w+): (.*)$", line.rstrip("\n"))
    if m and m[1] == key:
        sys.stdout.write(json.loads(m[2]))
        break
PY
}

# workspace_storage_notice RENDERED_CONFIGMAP: says which workspace storage mode this deploy
# configured, from the values the runner will actually read. Always returns 0.
workspace_storage_notice() {
  local bounded default
  if ! bounded="$(rendered_config_value "$1" BLERG_RUNNER_WORKSPACE_STORAGE_CLASS)" \
      || ! default="$(rendered_config_value "$1" BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS)"; then
    echo "==> Workspace storage: could not read the rendered configuration (see README.md, \"Workspace storage\")"
    return 0
  fi
  if [ -n "$bounded" ]; then
    echo "==> Workspace storage: bounded ($bounded)."
    return 0
  fi
  if [ -n "$default" ]; then
    echo "==> Workspace storage: standard (class $default; no size limit per workspace)."
  else
    echo "==> Workspace storage: standard (cluster default class; no size limit per workspace)."
    echo "    The workspace class setting (BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS) is empty, so workspace claims"
    echo "    name no class and use the cluster's default StorageClass. With no default class they cannot bind:"
    echo "    mark a class as the default, or set BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS."
  fi
  echo "    Set WORKSPACE_STORAGE_CLASS to a size-limited StorageClass for bounded workspaces (README.md, \"Workspace storage\")."
  return 0
}

# first_run_setup_notice: the end of a deploy. A fresh install has no account yet: core is in
# first-run setup mode, and the person creates the first (admin) account by opening a single-use
# setup link and choosing a username and password. An install that already has an account prints
# nothing. Always returns 0: the deploy itself succeeded either way.
#
# On a terminal the link is fetched (`blerg-core setup-link`, run in the core pod; it mints a
# fresh code) and printed prominently. Without a terminal (an agent, a log file) nothing is minted
# and no link is printed: `setup-link --check` only asks whether setup is open, and the output
# tells the person the command to run in their own terminal. So a non-terminal re-run never
# invalidates a link the person already has.
first_run_setup_notice() {
  local out err rc=0
  err="$(mktemp)"
  if ! stdout_is_tty; then
    kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link --check >/dev/null 2>"$err" || rc=$?
    if [ "$rc" -eq 0 ]; then
      rm -f "$err"
      cat <<NOTICE

==> FINISH SETUP: this install has no account yet. The person who will own it runs this in their
    own terminal to get the setup link (a credential, so it is not printed here):
    kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
    then opens it and chooses their username and password.
NOTICE
      return 0
    fi
  else
    out="$(kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link 2>"$err")" || rc=$?
    if [ "$rc" -eq 0 ] && [ -n "$out" ]; then
      rm -f "$err"
      cat <<NOTICE

==> FINISH SETUP: create the first (admin) account by opening this link and
    choosing your username and password:

    $out

    The link is a credential until it is used: anyone holding it can create
    the admin account. It works once, for 24 hours. Do not paste it into chats
    or tickets. For a fresh one (the old one stops working):
    kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
NOTICE
      return 0
    fi
  fi
  if grep -q 'setup is complete' "$err" 2>/dev/null; then
    rm -f "$err"
    return 0
  fi
  rm -f "$err"
  cat <<NOTICE
==> Could not ask core whether first-run setup is still open. If this is a fresh install, get
    the setup link with:
    kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
NOTICE
  return 0
}

# --- The cluster's shared Hermes provider configuration ---------------------------------------
#
# hermes_providers_secret FILE BASE_DIR: when FILE (HERMES_PROVIDERS_FILE) is set, writes it into
# Secret blerg/blerg-hermes-providers under the key config.yaml; the runner reads that key through
# an optional secretKeyRef and hands it to every Hermes session pod (CLUSTER-RUNTIME.md, "Shared
# Hermes providers"). A relative FILE is taken relative to BASE_DIR (install/k8s). Unset: nothing
# is done, and an existing Secret is left as it is. The file goes to kubectl by path, never on a
# command line, and nothing from it is printed. Returns 1 for a missing, empty or over-64-KiB file.
hermes_providers_secret() {
  local file="${1:-}" base="${2:-.}" size
  [ -n "$file" ] || return 0
  case "$file" in /*) ;; *) file="$base/$file" ;; esac
  if [ ! -f "$file" ] || [ ! -r "$file" ]; then
    echo "HERMES_PROVIDERS_FILE is not a readable file: $file" >&2
    return 1
  fi
  size="$(wc -c < "$file" | tr -d ' ')"
  # 64 KiB: the runner and every Hermes pod receive it as one environment entry, and Linux
  # refuses to start a process with any single entry over 128 KiB.
  if [ "$size" -eq 0 ] || [ "$size" -gt 65536 ]; then
    echo "HERMES_PROVIDERS_FILE must be a Hermes config.yaml fragment of 1 byte to 64 KiB ($size bytes): $file" >&2
    return 1
  fi
  echo "==> Writing blerg/blerg-hermes-providers from HERMES_PROVIDERS_FILE"
  kubectl -n blerg create secret generic blerg-hermes-providers --from-file=config.yaml="$file" --dry-run=client -o yaml \
    | kubectl apply -f -
}
