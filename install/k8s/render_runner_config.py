#!/usr/bin/env python3
"""Overlay optional runner ConfigMap values: environment > live > template.

Read only the Deployment's optional ConfigMap references, never arbitrary
BLERG_RUNNER_* environment variables (some of those contain credentials).
JSON strings are valid YAML scalars and preserve empty and multiline values.
"""
import json
import os
import re
import sys
from pathlib import Path


def render(template, deployment, live, environ):
    # None means kubectl found no ConfigMap: only that is a fresh install.
    # An existing install with no key used the binary's historical 4Gi limit.
    existing = live is not None
    live = dict(live or {})
    if existing:
        live.setdefault("BLERG_RUNNER_POD_MEM_LIMIT", "4Gi")
    keys = set(re.findall(
        r"configMapKeyRef: \{ name: blerg-config, key: (\w+), optional: true \}",
        deployment,
    ))
    values = {key: environ.get(key, live[key]) if key in live else environ[key]
              for key in keys if key in environ or key in live}
    lines = [line for line in template.splitlines()
             if not ((match := re.match(r"^  (\w+):", line)) and match[1] in values)]
    lines.extend(f"  {key}: {json.dumps(value)}" for key, value in sorted(values.items()))
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    deployment = Path(sys.argv[1]).read_text()
    config = json.loads(Path(sys.argv[2]).read_text() or "{}")
    live = (config.get("data") or {}) if config else None
    sys.stdout.write(render(sys.stdin.read(), deployment, live, os.environ))
