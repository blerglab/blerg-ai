#!/usr/bin/env python3
"""Run the native P4 security gate: python3 scripts/test-workspace-macos.py."""
import datetime
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tempfile


def run():
    if platform.system() != "Darwin":
        raise SystemExit("Native macOS required; cross-compilation is not acceptance")
    root = Path(__file__).resolve().parent.parent
    for tool in ("go", "git", "cc", "hdiutil"):
        if not shutil.which(tool):
            raise SystemExit("Missing prerequisite: " + tool)
    def git(*args):
        return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()
    def clean():
        return not git("status", "--porcelain", "--untracked-files=normal")
    if not clean():
        raise SystemExit("Run from a clean committed checkout (including untracked source)")
    head = git("rev-parse", "HEAD")
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    evidence = root / "workspace-macos-evidence" / (head[:12] + "-" + stamp)
    evidence.mkdir(parents=True)
    metadata = {"system": platform.system(), "machine": platform.machine(), "commit": head,
                "started": stamp, "race": True, "clean_before": True, "complete": False,
                "accepted": False, "native_host_accepted": False, "pending": []}
    metadata_path = evidence / "metadata.json"
    def save():
        metadata_path.write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
    save()
    print("Native evidence:", evidence, flush=True)
    try:
        with tempfile.TemporaryDirectory(prefix="private-", dir=evidence) as private:
            scratch = Path(private)
            for directory in ("home", "cache", "tmp", "modules"):
                (scratch / directory).mkdir()
            go = shutil.which("go")
            env = {"PATH": os.pathsep.join(dict.fromkeys([str(Path(go).parent), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"])),
                   "HOME": str(scratch / "home"), "LANG": "C.UTF-8", "GOWORK": "off", "GOENV": "off",
                   "GOTOOLCHAIN": "auto", "CGO_ENABLED": "1", "GOCACHE": str(scratch / "cache"),
                   "GOTMPDIR": str(scratch / "tmp"), "TMPDIR": str(scratch / "tmp"), "GOMODCACHE": str(scratch / "modules")}
            # A present but unusable Docker installation must fail its tests,
            # not turn into an absence waiver. Detect the same PATH tests use.
            metadata["docker_present"] = shutil.which("docker", path=env["PATH"]) is not None
            if metadata["docker_present"]:
                env["BLERG_EXPLORER_DOCKER_TEST"] = "1"
            def output(*args):
                return subprocess.check_output(args, cwd=root / "runner", env=env, text=True).strip()
            metadata["go"] = output(go, "version")
            metadata["goos"], metadata["goarch"] = output(go, "env", "GOOS", "GOARCH").splitlines()
            metadata["git"] = output("git", "--version")
            save()
            logs = []
            failures = []
            metadata["test_exit_codes"] = {}
            for package, name, subset in [("daemon", "daemon", ["-run", "TestWorkspace"]), ("hostgit", "hostgit", []), ("workspacepolicy", "policy", [])]:
                log = evidence / ("workspace-" + name + ".json")
                logs.append(str(log))
                command = [go, "test", "-race", "-json", "-p", "1", "-timeout", "45m", "./internal/" + package, *subset, "-count=1"]
                print("Running", " ".join(command), flush=True)
                diagnostics = evidence / ("workspace-" + name + ".stderr.log")
                with log.open("w", encoding="utf-8") as target, diagnostics.open("w", encoding="utf-8") as stderr:
                    try:
                        subprocess.run(command, cwd=root / "runner", env=env, stdout=target, stderr=stderr, check=True)
                    except subprocess.CalledProcessError as error:
                        metadata["test_exit_codes"][package] = error.returncode
                        failures.append(error)
                    else:
                        metadata["test_exit_codes"][package] = 0
                save()
            metadata["commit_after"] = git("rev-parse", "HEAD")
            metadata["clean_after"] = clean()
            metadata["complete"] = not failures
            save()
            if failures:
                # Keep every package's evidence, but never accept a failing command,
                # even if its output lacks a final JSON failure record.
                raise failures[0]
            try:
                subprocess.run([sys.executable, str(root / "scripts/check-workspace-macos-gate.py"), "--metadata", str(metadata_path), "--commit", head, *logs], check=True)
            finally:
                # The checker records host success separately from full
                # acceptance; never overwrite Docker pending with accepted=True.
                metadata.update(json.loads(metadata_path.read_text(encoding="utf-8")))
    finally:
        metadata["finished"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        save()
    print("PASS: native macOS host guarantees; workspace acceptance:",
          "accepted" if metadata["accepted"] else "NOT accepted (Docker pending)", "; retain", evidence)


if __name__ == "__main__":
    run()
