"""Execute installer interop/consent with fake HOME, services, and Windows APIs."""

import errno
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import subprocess
import sys
import time
import unittest


SCRATCH = Path(sys.argv.pop(1)).resolve()
PWSH = sys.argv.pop(1)
DESKTOP = Path(__file__).resolve().parent
INSTALLER = SCRATCH / "repo/install/desktop/daemon/install.sh"


class InteropTests(unittest.TestCase):
    def setUp(self):
        self.env = {
            "HOME": str(SCRATCH / "home"),
            "PATH": str(SCRATCH / "bin"),
            "LANG": "C.UTF-8",
            "TMPDIR": str(SCRATCH),
            "XDG_CACHE_HOME": str(SCRATCH / "cache"),
            "POWERSHELL_TELEMETRY_OPTOUT": "1",
            "WSL_DISTRO_NAME": "Example Distro",
            "BLERG_SYSTEMD_RUN_DIR": str(SCRATCH / "run/system"),
            "FAKE_LOG": str(SCRATCH / "log"),
            "FAKE_PS": str(SCRATCH / "ps"),
            "FAKE_PWSH": PWSH,
        }
        self.runner = SCRATCH / "evaluate.ps1"
        self.runner.write_text(
            "$ErrorActionPreference = 'Stop'\n"
            "& ([scriptblock]::Create((Get-Content -Raw -LiteralPath $env:FAKE_PS)))\n"
        )
        self.env["FAKE_PS_RUNNER"] = str(self.runner)

    def install(self, *args):
        (SCRATCH / "log").write_text("")
        return subprocess.run(
            ["/bin/bash", str(INSTALLER), *args], env=self.env,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, text=True, timeout=30,
        )

    def test_path_is_data_and_arguments_round_trip(self):
        # Match powershell.exe's default error policy: a broken path must not
        # be able to continue into an injected suffix and report success.
        self.runner.write_text(
            "$ErrorActionPreference = 'Continue'\n"
            "& ([scriptblock]::Create((Get-Content -Raw -LiteralPath $env:FAKE_PS)))\n"
        )
        # PowerShell's ASCII/smart quote delimiters, double quotes, backticks,
        # dollar signs, spaces, and a harmless attempted command suffix.
        names = ["straight ' apostrophe", "smart ‘ ’ ‚ ‛ “ ” quotes",
                 "attack’; Write-Output injection-canary; #",
                 'other " ` $() characters']
        for name in names:
            with self.subTest(path=name):
                worker = SCRATCH / name / "wsl-startup.ps1"
                worker.parent.mkdir(exist_ok=True)
                worker.write_text(
                    "param($Action, $Distro, $LinuxUser)\n"
                    "@{Path=$PSCommandPath; Action=$Action; Distro=$Distro; "
                    "LinuxUser=$LinuxUser} | ConvertTo-Json -Compress\n"
                )
                self.env["FAKE_PS_PATH"] = str(worker)
                result = self.install("wsl-startup", "status")
                self.assertEqual(result.returncode, 0, result.stdout)
                self.assertNotIn("injection-canary\n", result.stdout)
                # Exactly one JSON output: no injected suffix output/errors.
                self.assertEqual(json.loads(result.stdout), {
                    "Path": str(worker), "Action": "status",
                    "Distro": "Example Distro",
                    "LinuxUser": subprocess.check_output(["id", "-un"], text=True).strip(),
                })

    def test_cleanup_failures_preserve_service(self):
        wrapper = SCRATCH / "fake-windows.ps1"
        wrapper.write_text(r"""
param($Action, $Distro, $LinuxUser)
$ErrorActionPreference = 'Stop'
$requestedAction = $Action
. $env:FAKE_WORKER -Action status -Distro $Distro -LinuxUser $LinuxUser
function Get-BlergAccount { 'EXAMPLE\Example User' }
function Get-ScheduledTask {
    [CmdletBinding()] param($TaskName)
    $task = [pscustomobject]@{TaskName = "Blerg-WSL-$env:FAKE_KEY"; TaskPath = '\'; State = 'Running'}
    if ($env:FAKE_FAILURE -eq 'denied') { Write-Error 'fake lookup denied' -Category PermissionDenied; return }
    if ($env:FAKE_FAILURE -eq 'provider') { $task; Write-Error 'fake provider failed' -Category ResourceUnavailable; return }
    if ($env:FAKE_FAILURE -ne 'absent') { $task }
}
function Get-ScheduledTaskInfo {
    [CmdletBinding()] param($TaskName, $TaskPath)
    [pscustomobject]@{LastTaskResult = 267009}
}
function Stop-ScheduledTask {
    [CmdletBinding()] param($TaskName, $TaskPath)
    if ($env:FAKE_FAILURE -eq 'stop') { Write-Error 'fake stop failed'; return }
    Add-Content -LiteralPath $env:FAKE_TASK_STATE -Value 'stopped'
}
function Unregister-ScheduledTask {
    [CmdletBinding(SupportsShouldProcess)] param($TaskName, $TaskPath)
    if ($env:FAKE_FAILURE -eq 'unregister') { Write-Error 'fake unregister failed'; return }
    Set-Content -LiteralPath $env:FAKE_TASK_STATE -Value 'removed'
}
Invoke-BlergStartup $requestedAction $Distro $LinuxUser
""")
        self.env["FAKE_PS_PATH"] = str(wrapper)
        self.env["FAKE_WORKER"] = str(INSTALLER.parent / "wsl-startup.ps1")
        self.env["LOCALAPPDATA"] = str(SCRATCH / "windows-data")
        # Use the same identity hash as the real worker, without any live identity.
        user = subprocess.check_output(["id", "-un"], text=True).strip()
        self.env["FAKE_KEY"] = hashlib.sha256(
            f"EXAMPLE\\Example User\nExample Distro\n{user}".encode()
        ).hexdigest()[:24].upper()
        state = SCRATCH / "task-state"
        self.env["FAKE_TASK_STATE"] = str(state)
        unit = SCRATCH / "home/.config/systemd/user/blerg-runner-daemon.service"
        unit.parent.mkdir(parents=True, exist_ok=True)
        for failure in ("denied", "provider", "stop", "unregister", "absent", "success"):
            actions = [("uninstall",)]
            if failure in ("denied", "provider"):
                actions += [("wsl-startup", "disable"), ("wsl-startup", "status")]
            for args in actions:
                with self.subTest(failure=failure, action=args):
                    self.env["FAKE_FAILURE"] = failure
                    state.write_text("present\n")
                    unit.write_text("fake installed unit\n")
                    result = self.install(*args)
                    log = (SCRATCH / "log").read_text()
                    if failure in ("absent", "success"):
                        self.assertEqual(result.returncode, 0, result.stdout)
                        self.assertFalse(unit.exists())
                        self.assertIn("--user disable --now", log)
                        if failure == "success":
                            self.assertEqual(state.read_text().strip(), "removed")
                    else:
                        self.assertNotEqual(result.returncode, 0, result.stdout)
                        self.assertEqual(unit.read_text(), "fake installed unit\n")
                        self.assertNotIn("--user disable", log)
                        self.assertNotIn("daemon-reload", log)
                        self.assertNotIn("removed", state.read_text())
                        self.assertNotIn("startup disabled", result.stdout)
                        self.assertNotIn("not enabled", result.stdout)
                        self.assertIn("Task Scheduler startup", result.stdout)

    def test_interactive_consent_defaults_to_no_even_with_yes(self):
        source = INSTALLER.read_text()
        offer = re.search(r"^offer_wsl_startup\(\) \{.*?^\}", source, re.M | re.S).group()
        program = "\n".join([
            "set -eu", "is_wsl() { return 0; }",
            'blerg_step() { printf "%s\\n" "$*"; }',
            'wsl_startup() { echo consent-registration; }',
            "WSL_STARTUP=0", "ASSUME_YES=1", offer, "offer_wsl_startup",
        ])
        for answer in (b"\n", b"n\n", b"y\n", b"Y\n"):
            with self.subTest(answer=answer):
                master, slave = pty.openpty()
                proc = subprocess.Popen(["/bin/bash", "-c", program], env=self.env,
                                        stdin=slave, stdout=slave, stderr=slave,
                                        start_new_session=True)
                os.close(slave)
                output = b""
                answered = False
                deadline = time.monotonic() + 10
                try:
                    while time.monotonic() < deadline:
                        if not select.select([master], [], [], 0.1)[0]:
                            continue
                        try:
                            part = os.read(master, 4096)
                        except OSError as exc:
                            if exc.errno == errno.EIO:
                                break
                            raise
                        if not part:
                            break
                        output += part
                        if b"[y/N]" in output and not answered:
                            os.write(master, answer)
                            answered = True
                    self.assertEqual(proc.wait(timeout=1), 0, output)
                finally:
                    os.close(master)
                    if proc.poll() is None:
                        proc.kill()  # Only this test's child, never a host process.
                        proc.wait()
                self.assertTrue(answered, output)
                self.assertIn(b"including on battery", output)
                self.assertEqual(b"consent-registration" in output, answer in (b"y\n", b"Y\n"))

    def test_already_enabled_task_is_not_offered_again(self):
        source = INSTALLER.read_text()
        offer = re.search(r"^offer_wsl_startup\(\) \{.*?^\}", source, re.M | re.S).group()
        program = "\n".join([
            "set -eu", "is_wsl() { return 0; }",
            'blerg_step() { printf "%s\\n" "$*"; }',
            'wsl_startup() { if [ "$1" = status ]; then echo "Task Scheduler: Blerg WSL Example; state: Ready; last result: 0"; else echo consent-registration; fi; }',
            "WSL_STARTUP=0", offer, "offer_wsl_startup",
        ])
        master, slave = pty.openpty()
        proc = subprocess.run(["/bin/bash", "-c", program], env=self.env, stdin=slave,
                              capture_output=True, timeout=10)
        os.close(slave)
        os.close(master)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn(b"already enabled", proc.stdout)
        self.assertNotIn(b"[y/N]", proc.stdout)
        self.assertNotIn(b"consent-registration", proc.stdout)

    def test_ci_and_make_include_required_suite(self):
        root = DESKTOP.parent.parent
        workflow = (root / ".github/workflows/ci.yml").read_text().split("\n  scripts:\n", 1)[1]
        self.assertRegex(workflow, r"(?m)^\s+(?:- )?run: bash install/desktop/wsl_startup_test\.sh$")
        target = (root / "Makefile").read_text().split("\ncheck-scripts:\n", 1)[1].split("\nbuild:", 1)[0]
        self.assertIn("\tbash ./install/desktop/wsl_startup_test.sh\n", target)
        # A developer/CI without pwsh must fail instead of claiming a partial pass.
        # Supply every other suite dependency so an unrelated missing tool cannot
        # masquerade as a deliberate refusal to skip PowerShell behavior.
        without_pwsh = SCRATCH / "without-pwsh"
        without_pwsh.mkdir(exist_ok=True)
        for tool in ("bash", "cat", "rm", "rmdir", "dirname", "id", "uname", "grep",
                     "chmod", "sed", "head", "cut", "iconv", "base64", "mktemp",
                     "mkdir", "cp", "ln", "env", "mv", "python3"):
            (without_pwsh / tool).symlink_to(shutil.which(tool))
        result = subprocess.run(["/bin/bash", str(DESKTOP / "wsl_startup_test.sh")],
                                env=dict(self.env, PATH=str(without_pwsh)), stdin=subprocess.DEVNULL,
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("pwsh is required", result.stderr)


if __name__ == "__main__":
    unittest.main()
