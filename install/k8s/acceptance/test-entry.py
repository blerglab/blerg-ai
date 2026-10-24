#!/usr/bin/env python3
"""Test image entry point. Credential never enters argv, output or result logs."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile

name = "TestClusterWorkspaceRealPodWriterFence"
listing = subprocess.run(["/acceptance/server.test", "-test.list=^" + name + "$"], capture_output=True, text=True, check=True)
if name not in listing.stdout.splitlines():
    raise SystemExit("acceptance test missing from image")
sa = Path("/var/run/secrets/kubernetes.io/serviceaccount")
with tempfile.TemporaryDirectory(prefix="acceptance-auth-") as directory:
    config = Path(directory) / "client.json"
    fd = os.open(config, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump({"clusters": [{"cluster": {"certificate-authority-data": base64.b64encode((sa / "ca.crt").read_bytes()).decode()}}],
                   "users": [{"user": {"token": (sa / "token").read_text().strip()}}]}, f)
    env = {k: os.environ[k] for k in ("PATH", "HOME", "P3_TEST_NAMESPACE", "P3_TEST_EXCLUDE_NODES", "P3_TEST_POD_IMAGE") if k in os.environ}
    env.update(P3_TEST_KUBECONFIG_JSON=str(config), P3_TEST_KUBE_URL="https://kubernetes.default.svc", GOWORK="off")
    raise SystemExit(subprocess.run(["/acceptance/server.test", "-test.v", "-test.run=^" + name + "$", "-test.timeout=8m"], env=env, stdin=subprocess.DEVNULL).returncode)
