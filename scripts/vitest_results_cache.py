"""Share scheduling hints only; transformed code stays private to each checkout."""
import hashlib
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import tempfile

CACHE_DIRS = {
    'core/web': ('core/web/.cache/vite',),
    'board/web': ('board/web/.cache/vite',),
    'packages/chat': ('.cache/vite-chat',),
    'runner/frontend': ('.cache/vite-runner', '.cache/vite-runner-unit',
                        '.cache/vite-runner-browser', '.cache/vite-runner-performance'),
}


def best_effort(root, cwd, *, restore):
    try:
        sync(root, cwd, restore=restore)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f'Vitest scheduling cache unavailable: {type(error).__name__}', file=sys.stderr)


def shared_root(root):
    common = subprocess.check_output(['git', 'rev-parse', '--git-common-dir'], cwd=root, text=True).strip()
    key = hashlib.sha256(str((root / common).resolve()).encode()).hexdigest()[:20]
    return Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'blerg' / 'vitest-results' / key


def valid_results(path):
    data = json.loads(path.read_text())
    if not isinstance(data, dict) or not isinstance(data.get('version'), str) or not isinstance(data.get('results'), list):
        raise ValueError('invalid Vitest scheduling metadata')
    for row in data['results']:
        if not isinstance(row, list) or len(row) != 2 or not isinstance(row[0], str) or not isinstance(row[1], dict):
            raise ValueError('invalid Vitest scheduling row')
        duration = row[1].get('duration')
        if (not isinstance(duration, (int, float)) or not math.isfinite(duration) or duration < 0
                or not isinstance(row[1].get('failed'), bool)):
            raise ValueError('invalid Vitest scheduling values')
    return data


def sync(root, cwd, *, restore, shared=None):
    """Cache failures are reported to the caller, which preserves the test outcome."""
    if cwd not in CACHE_DIRS:
        return
    shared = shared if shared is not None else shared_root(root)
    for directory in CACHE_DIRS.get(cwd, ()):
        source = (shared if restore else root) / directory
        destination = (root if restore else shared) / directory
        for path in source.glob('vitest/**/results.json'):
            target = destination / path.relative_to(source)
            if restore and target.exists():
                continue
            data = valid_results(path)
            target.parent.mkdir(parents=True, exist_ok=True)
            # Atomic replacement keeps concurrent focused workers from exposing
            # partial JSON. Last-writer timing hints never skip execution.
            with tempfile.NamedTemporaryFile(mode='w', dir=target.parent, delete=False) as output:
                temporary = Path(output.name)
                try:
                    json.dump(data, output)
                    output.flush()
                    os.replace(temporary, target)
                finally:
                    temporary.unlink(missing_ok=True)
