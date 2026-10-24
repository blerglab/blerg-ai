#!/usr/bin/env bash
# All installs run in a scratch tree, with a fake HOME and Windows interop.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
if ! command -v pwsh >/dev/null 2>&1; then
  echo 'wsl_startup_test: pwsh is required (Linux PowerShell; all Windows APIs are mocked)' >&2
  exit 1
fi
export FAKE_PWSH="$(command -v pwsh)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/repo/install/desktop/daemon" "$tmp/home" "$tmp/bin" "$tmp/run/system"
cp brand.sh hostcheck.sh "$tmp/repo/install/desktop/"
cp daemon/* "$tmp/repo/install/desktop/daemon/"
for tool in bash cat rm rmdir dirname id uname grep chmod sed head cut iconv base64; do
  ln -s "$(command -v "$tool")" "$tmp/bin/$tool"
done
cat > "$tmp/bin/systemctl" <<'STUB'
#!/bin/bash
printf '%s\n' "$*" >> "$FAKE_LOG"
case "$*" in *is-enabled*) exit "${FAKE_ENABLED:-0}";; esac
STUB
cat > "$tmp/bin/loginctl" <<'STUB'
#!/bin/bash
printf '%s\n' "$*" >> "$FAKE_LOG"
case "$*" in *show-user*) echo "${FAKE_LINGER:-yes}";; esac
STUB
cat > "$tmp/bin/wslpath" <<'STUB'
#!/bin/bash
default_path="C:\\fake checkout's path\\wsl-startup.ps1"
printf '%s\n' "${FAKE_PS_PATH:-$default_path}"
STUB
cat > "$tmp/bin/powershell.exe" <<'STUB'
#!/bin/bash
printf '%s\n' "$*" >> "$FAKE_LOG"
printf '%s' "${@: -1}" | base64 -d | iconv -f UTF-16LE -t UTF-8 > "$FAKE_PS"
if [ -n "${FAKE_PS_RUNNER:-}" ]; then
  exec "$FAKE_PWSH" -NoLogo -NoProfile -NonInteractive -File "$FAKE_PS_RUNNER"
fi
exit "${FAKE_PS_RC:-0}"
STUB
chmod +x "$tmp/bin/"{systemctl,loginctl,wslpath,powershell.exe}
run() {
  : > "$tmp/log"
  set +e
  env -i PATH="$tmp/bin" HOME="$tmp/home" USER=ignored LANG=C.UTF-8 \
    WSL_DISTRO_NAME='Example Distro' BLERG_SYSTEMD_RUN_DIR="$tmp/run/system" \
    FAKE_LOG="$tmp/log" FAKE_PS="$tmp/ps" "$@" \
    bash "$tmp/repo/install/desktop/daemon/install.sh" "${args[@]}" </dev/null > "$tmp/out" 2>&1
  rc=$?
  set -e
}
fail() { cat "$tmp/out"; echo "FAIL: $*"; exit 1; }
args=(wsl-startup enable)
run
[ "$rc" = 0 ] || fail 'opt-in enable must work without a daemon rebuild/restart'
grep -q -- "-Action 'enable'" "$tmp/ps" || fail 'enable action missing'
grep -q -- "-Distro 'Example Distro'" "$tmp/ps" || fail 'distro with spaces lost'
grep -q -- "-LinuxUser '$(id -un)'" "$tmp/ps" || fail 'must use actual uid, not USER'
if grep -Eq 'restart|daemon-reload|disable|stop' "$tmp/log"; then fail 'startup command touched services'; fi
run BLERG_SYSTEMD_RUN_DIR="$tmp/missing-systemd"
[ "$rc" != 0 ] && grep -q 'systemd prerequisites' "$tmp/out" || fail 'systemd must be checked before Windows startup'
run FAKE_LINGER=no
[ "$rc" != 0 ] && grep -q 'sudo loginctl enable-linger' "$tmp/out" || fail 'missing linger must fail with repair'
if grep -q 'EncodedCommand' "$tmp/log"; then fail 'task installed despite missing linger'; fi
run FAKE_ENABLED=1
[ "$rc" != 0 ] && grep -q 'install.sh install' "$tmp/out" || fail 'missing service must fail with repair'
run FAKE_PS_RC=1
[ "$rc" != 0 ] && grep -q 'Task Scheduler' "$tmp/out" || fail 'Windows failure must be actionable'
# Uninstall must remove its own startup task before removing the Linux service.
args=(uninstall)
run
[ "$rc" = 0 ] || fail 'WSL uninstall failed'
grep -q -- "-Action 'disable'" "$tmp/ps" || fail 'uninstall left a logon task'
grep -q -- '--user disable --now blerg-runner-daemon.service' "$tmp/log" || fail 'uninstall did not remove service'
run FAKE_PS_RC=1
[ "$rc" != 0 ] || fail 'uninstall hid Windows cleanup failure'
if grep -q -- '--user disable' "$tmp/log"; then fail 'uninstall removed service before Windows cleanup succeeded'; fi
args=(wsl-startup status)
run FAKE_LINGER=no FAKE_ENABLED=1
[ "$rc" = 0 ] || fail 'status must work even if Linux service is broken'
args=(wsl-startup disable)
run FAKE_LINGER=no FAKE_ENABLED=1
[ "$rc" = 0 ] || fail 'disable must work even if Linux service is broken'
grep -q -- "-Action 'disable'" "$tmp/ps" || fail 'disable action missing'
run WSL_DISTRO_NAME=
[ "$rc" != 0 ] || fail 'missing distro must fail safely'
run WSL_DISTRO_NAME="Example';throw 'injected"
[ "$rc" != 0 ] || fail 'unsafe distro must be rejected'
mv "$tmp/bin/powershell.exe" "$tmp/bin/powershell.saved"
run
[ "$rc" != 0 ] && grep -q 'Windows interop' "$tmp/out" || fail 'missing interop must be actionable'
mv "$tmp/bin/powershell.saved" "$tmp/bin/powershell.exe"
# Protect restart recovery even with a distro's unusually strict default rate limit.
grep -q '^StartLimitIntervalSec=0$' daemon/blerg-runner-daemon.service.tmpl || fail 'temporary start failures can exhaust the systemd restart limit'
grep -q '^RestartSec=15$' daemon/blerg-runner-daemon.service.tmpl || fail 'restart loop must back off'
pwsh -NoLogo -NoProfile -NonInteractive -File ./wsl_startup_test.ps1 -Scratch "$tmp/powershell"
# Run setup/copy, worker output, and rotation under special profile paths too.
pwsh -NoLogo -NoProfile -NonInteractive -File ./wsl_startup_test.ps1 -Scratch "$tmp/profile[brackets]"
pwsh -NoLogo -NoProfile -NonInteractive -File ./wsl_startup_test.ps1 -Scratch "$tmp/"'profile[wild]*?`'
python3 ./wsl_startup_interop_test.py "$tmp" "$FAKE_PWSH"
echo 'wsl_startup_test: ok'
