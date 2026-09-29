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
