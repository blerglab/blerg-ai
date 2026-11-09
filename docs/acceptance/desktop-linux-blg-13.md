# Desktop Linux install drill (BLG-13)

Date: November 1, 2026. Scope: Linux userspace only, for the 0.6.0 candidate.
Working estimate: size M, risk medium, complexity high because isolation and
upgrade recovery need systemd and real databases. Board access was unavailable;
the supplied ticket was pinned locally, and the board estimates were not changed.
Baseline main: `ab133ab32038095eff85daf9c4fa3f0a757b60d2`; its `VERSION` is
`0.5.4`. macOS and WSL2 desktop installation remain **untested**. This is
partial release evidence, not approval to close the multiplatform ticket.

## Isolation and preparation

Used a fresh Ubuntu 24.04 container named `blg-13-linux-drill`, labeled
`blerg.drill=blg-13`, with systemd 255, a disposable non-root account, a
passwordless sudo rule, and its own Docker engine. The container had no host
bind mounts, Docker socket, published ports, or persistent volumes. Limits:
two CPUs and 8 GiB memory. No host daemon, user units, configuration directories,
or existing containers were changed.

The container shares its host's WSL kernel. This checks Linux userspace with
systemd, not a native Linux VM or Windows integration. No Windows commands or
logon tasks were tested. A privileged container was needed for systemd and
nested Docker: the documented install builds and runs Compose services, then
builds the daemon and its sandbox. A fake Docker CLI would not establish that.

Installed `systemd`, `systemd-sysv`, `dbus-user-session`, `sudo`, `git`, `curl`,
`openssl`, `ca-certificates`, `python3`, and `uidmap` from Ubuntu packages, then
Docker from its public installation script. Docker engine/CLI were 29.9.2 and
Compose was 5.6.1. The README requests Compose v2; this available newer Compose
CLI was used, so this is not specific v2 compatibility evidence.

Transferred main as a Git bundle and cloned its `main` branch inside the
container. No working tree, configuration, login, or repository credential from
the host was mounted. This was a local copy of main, not a fresh network clone.
Installed an executable `codex` stub in the disposable account's local bin and
an empty JSON authentication fixture. The stub makes no model calls and is not
an authenticated engine test.

Provisioning corrections, recorded separately from installer defects:

- Docker's `/tmp` tmpfs defaulted to `noexec`; the initial shell regression run
  failed to execute its scratch programs. Reran with an executable scratch
  directory (`TMPDIR` under the disposable home); later remounted only the
  container's `/tmp` executable for normal build behavior.
- The first bundle location under `/tmp` was unavailable to the clone command;
  moved it to `/opt`. A bundle containing only `main` has no remote HEAD, so
  selected `main` explicitly. These were drill setup errors.
- The daemon regression suite initially lacked `make`: eight assertions failed
  in 69 seconds. Added development-only `make`, `gcc` (for race tests), and
  ShellCheck to the disposable image. The rerun passed in 60 seconds.

## Documented installation

Ran the README's command as the disposable non-root account, with only the stub
engine added to PATH:

```sh
cd install/desktop
./blerg-up.sh
```

1. Docker/Compose preflight and creation of fresh `.env` succeeded. The installer
   started its own systemd user manager. No prior Blerg state was present.
2. The first attempt failed pulling Postgres: nested overlay extraction could
   not convert a whiteout file (`operation not permitted`). No application
   service or daemon had started. This is a nested-storage limitation.
3. Changed only the disposable engine's `/etc/docker/daemon.json` to the
   following, restarted that engine, and repeated the same install command:

   ```json
   {"storage-driver":"vfs","features":{"containerd-snapshotter":false}}
   ```

   VFS passed the failing extraction. It is much slower: Go base-image
   preparation alone took 245.2 seconds. The retained `.env` came only from
   the first attempt in this disposable install.

## Regression fix

Python 3 is required by `daemon/enroll.py` but was absent from the README's
requirements and installer preflight. Added failing checks first for the
normal desktop entrypoint, daemon preflight, and direct daemon installation,
with Python removed from the fixture PATH. Seven assertions failed: there was
no repair message, and the desktop entrypoint reached Docker first.

The fix checks Python before Docker builds or daemon changes and gives package
installation commands. Help, status, and uninstall remain available without
Python. The README now lists the enrollment dependency. The existing scripts
CI job already runs these regression tests; no new language or test runner was
introduced.

## Upgrade coverage (BLG-171)

Pending final verification.

## Verification and cleanup

Pending final verification.

## CI decision and remaining acceptance

Do not add the full clean-install drill to the fast CI gate: the observed cold
build already exceeds a few minutes before service startup or sandbox build.
Keep the prerequisite regressions in the existing scripts job. A scheduled or
manual real-install job would need a separate runtime budget and disposable
systemd machine.

macOS, WSL2 startup and Windows integration, genuine engine login/model calls,
interactive browser behavior, and previous-public-release-to-candidate upgrade
acceptance are not established by this drill.
