#!/usr/bin/env python3
"""Only for the disposable Docker build context, NEVER run against a checkout.

Hostpath does not enforce workspace capacity. The smoke image explicitly omits
this admission check, retaining identity, object budgets, protected storage,
process containment and credential rules. Production source/build is unchanged.
"""
from pathlib import Path


def overlay(source):
    old = 'return newDiskGit(public, private, session, true, expected...)'
    if source.count(old) != 1:
        raise ValueError('capacity admission seam changed; review the smoke image')
    return source.replace(old, 'return newDiskGit(public, private, session, false, expected...)')


if __name__ == '__main__':
    import json
    import sys
    root = Path(sys.argv[1]).resolve()
    if str(root) != '/src/runner':
        raise SystemExit('only the isolated image build source is supported')
    original = root / 'internal/clusterworkspace/git_linux.go'
    replacement = Path('/out/smoke_git_linux.go')
    replacement.write_text(overlay(original.read_text()))
    Path('/out/smoke-overlay.json').write_text(json.dumps({'Replace': {str(original): str(replacement)}}))
