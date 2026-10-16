#!/usr/bin/env bash
# Helpers sourced by deploy.sh (and exercised without a cluster by deploy_test.sh).
# Nothing here calls kubectl, so it can be tested anywhere.

# --- Confirmation before touching a cluster -------------------------------------------

# True when stdin is a terminal. A function so tests can override it.
stdin_is_tty() { [ -t 0 ]; }

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
