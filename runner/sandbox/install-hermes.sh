#!/usr/bin/env bash
# Installs a pinned Hermes release into $HOME. Run as the session user during
# the image build, by both session images (Dockerfile here and
# ../Dockerfile.devcontainer), so the pins live in one place.
#
# Hermes is not an npm or PyPI package: upstream ships a git checkout set up
# by its own installer script. There are tagged releases, so this installs one
# of those rather than whatever the moving install URL serves today.
#
# Pinned:
#   - the Hermes source: a release tag AND the commit it must resolve to (the
#     checkout is compared against the commit afterwards, so a moved tag fails
#     the build);
#   - the installer script: fetched from that same commit and checked against
#     a recorded sha256 before it runs;
#   - uv: a fixed release, checked against a recorded sha256 per architecture,
#     staged where the installer looks for it so it does not fetch the latest;
#   - Hermes' Python dependencies: installed from the uv.lock in that commit
#     (hash-verified). The installer silently falls back to an unpinned
#     resolve when that fails; this script fails the build instead;
#   - the Browser Use CLI: a fixed version.
#
# NOT pinned:
#   - the dependencies of the Browser Use CLI (resolved from PyPI at build
#     time, below the pinned top-level version);
#   - Hermes' Node dependencies: `npm install` against the package-lock.json
#     in that commit, which npm may amend rather than enforce;
#   - the browser build Playwright downloads for that lockfile's Playwright
#     version.
#
# Left out: the Computer Use driver. It drives a desktop, which a headless
# session container does not have, and upstream installs it by piping a script
# from another project's main branch into a shell.
#
# Dependabot does not see any of this. To move to a newer Hermes: pick a
# release tag of github.com/NousResearch/hermes-agent, set HERMES_TAG and
# HERMES_COMMIT (the commit the tag points to), download scripts/install.sh at
# that commit, read it, record its sha256 in HERMES_INSTALLER_SHA256, and
# rebuild both images.
set -euo pipefail

HERMES_RAW="https://raw.githubusercontent.com/NousResearch/hermes-agent"
HERMES_TAG="v2026.9.24" # Hermes Agent v0.21.5
HERMES_COMMIT="f97608f178d1ffeca59860195ab7da295f7c8e5f"
HERMES_INSTALLER_SHA256="2017ddf0cc7bc6cfb70d40dc9fba1d916f47dbcccf5fe73bdee2cf93a11262af"

UV_VERSION="0.12.3"
BROWSER_USE_VERSION="0.13.10"

case "$(uname -m)" in
  x86_64 | amd64)
    uv_target="x86_64-unknown-linux-gnu"
    uv_sha256="600cf9a742aca00d292673b16b5acffaa7b8c269a364ad0c2e79498dcb1fe101"
    ;;
  aarch64 | arm64)
    uv_target="aarch64-unknown-linux-gnu"
    uv_sha256="bb66cb52e7b1823aed1183630d8d8e5c958840d584a4c55ec10a4cfc168dcca2"
    ;;
  *)
    echo "install-hermes: no pinned uv build for $(uname -m)" >&2
    exit 1
    ;;
esac

hermes_home="${HERMES_HOME:-$HOME/.hermes}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Downloads retry (rate limits and dropped connections are routine on public
# hosts); the sha256 checks below still reject anything that is not the pinned file.
# uv, where the installer expects its own copy.
curl -fsSL --retry 6 --retry-delay 20 --retry-all-errors -o "$work/uv.tar.gz" \
  "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${uv_target}.tar.gz"
echo "${uv_sha256}  $work/uv.tar.gz" | sha256sum -c -
tar -xzf "$work/uv.tar.gz" -C "$work"
mkdir -p "$hermes_home/bin"
install -m 0755 "$work/uv-${uv_target}/uv" "$work/uv-${uv_target}/uvx" "$hermes_home/bin/"

# The Browser Use CLI, where the installer expects it, so it keeps this
# version instead of installing the latest.
UV_NO_CONFIG=1 UV_TOOL_BIN_DIR="$hermes_home/bin" \
  "$hermes_home/bin/uv" tool install "browser-use==${BROWSER_USE_VERSION}"

# The installer, from the pinned commit.
curl -fsSL --retry 6 --retry-delay 20 --retry-all-errors -o "$work/install.sh" \
  "${HERMES_RAW}/${HERMES_COMMIT}/scripts/install.sh"
echo "${HERMES_INSTALLER_SHA256}  $work/install.sh" | sha256sum -c -

# --skip-setup: the setup wizard needs a real terminal and real provider
# credentials, neither of which exist at build time.
bash "$work/install.sh" \
  --skip-setup --skip-computer-use \
  --branch "$HERMES_TAG" --commit "$HERMES_COMMIT" --force-commit \
  2>&1 | tee "$work/install.log"

got="$(git -C "$hermes_home/hermes-agent" rev-parse HEAD)"
if [ "$got" != "$HERMES_COMMIT" ]; then
  echo "install-hermes: checkout is at $got, expected $HERMES_COMMIT ($HERMES_TAG)" >&2
  exit 1
fi
if ! grep -q "hash-verified via uv.lock" "$work/install.log"; then
  echo "install-hermes: Python dependencies were not installed from uv.lock" >&2
  exit 1
fi

uv cache clean >/dev/null 2>&1 || "$hermes_home/bin/uv" cache clean >/dev/null 2>&1 || true
rm -rf "$HOME/.npm/_cacache"

"$HOME/.local/bin/hermes" --version
