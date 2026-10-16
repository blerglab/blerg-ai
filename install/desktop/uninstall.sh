#!/usr/bin/env bash
# Remove the desktop stack this folder's blerg-up.sh installed, so a fresh
# ./blerg-up.sh starts from nothing.
#
# Usage: ./uninstall.sh [--purge] [--yes]
#
#   default   stop and remove the containers, the database and runner-data
#             volumes and the images this stack built; remove the daemon's
#             service and the links it made; remove the built daemon binary.
#             Accounts, sessions and settings in the database are gone.
#   --purge   also remove the daemon's own state folder (its token and saved
#             repos-folder setting), this stack's .env (the only copy of
#             BLERG_CORE_LOCAL_KEY and the stack's secrets) and the sandbox
#             image. Nothing under your repos folder, and nothing of your
#             Claude/Codex/Hermes logins, is ever touched.
#   --yes     do not ask before removing.
#
# Left for you on purpose (see README.md, "Uninstall"): the base images the
# builds pulled, Docker's build cache, and the repositories you cloned.
set -euo pipefail

cd "$(dirname "$0")"
PURGE=0
YES=0
for arg in "$@"; do
  case "$arg" in
    --purge) PURGE=1 ;;
    --yes|-y) YES=1 ;;
    -h|--help)
      sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

echo "This removes the Blerg desktop stack in $(pwd):"
echo "  - containers, the database and runner-data volumes, the images it built"
echo "  - the runner daemon service and the links it made"
echo "  - the built daemon binary (../../bin)"
if [ "$PURGE" = 1 ]; then
  echo "  - the daemon's state folder (~/.blerg-runner-daemon), this stack's .env, the sandbox image"
fi
echo "Your repos folder and your engine logins are not touched."
if [ "$YES" != 1 ]; then
  printf 'Continue? [y/N] '
  read -r answer
  case "$answer" in y|Y|yes|YES) ;; *) echo "nothing removed"; exit 1 ;; esac
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  echo "==> Stopping the stack and removing its volumes and images"
  docker compose down -v --rmi local --remove-orphans || true
else
  echo "==> docker compose not available; skipping the stack (remove its containers and volumes by hand)"
fi

if [ -x ./daemon/install.sh ]; then
  echo "==> Removing the runner daemon service"
  ./daemon/install.sh uninstall || true
fi

echo "==> Removing the built daemon binary"
rm -rf ../../bin

if [ "$PURGE" = 1 ]; then
  echo "==> Removing the daemon's state folder, this stack's .env and the sandbox image"
  rm -rf "$HOME/.blerg-runner-daemon"
  rm -f .env
  if command -v docker >/dev/null 2>&1; then
    docker rmi blerg-runner-sandbox:latest >/dev/null 2>&1 || true
  fi
fi

echo "==> Done. ./blerg-up.sh installs again from nothing."
