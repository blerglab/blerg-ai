#!/usr/bin/env python3
"""The real-pod opt-in is selected explicitly; a skipped test is never success."""
import os
import subprocess

name = 'TestAcceptanceClusterSmoke'
listing = subprocess.run(['/acceptance/server.test', '-test.list=^'+name+'$'], capture_output=True, text=True, check=True)
if name not in listing.stdout.splitlines():
    raise SystemExit('smoke test missing from image')
if os.environ.get('P6_ACCEPTANCE_SMOKE') != '1' or not os.environ.get('TEST_DATABASE_URL'):
    raise SystemExit('explicit smoke opt-in and isolated database required')
raise SystemExit(subprocess.run(['/acceptance/server.test', '-test.v', '-test.run=^'+name+'$', '-test.timeout=14m'], stdin=subprocess.DEVNULL).returncode)
