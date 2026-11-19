"""Serialize UI suites, including nested recorder invocations."""
from contextlib import contextmanager
import fcntl
import hashlib
import os
from pathlib import Path
import subprocess


def lock_path():
    override = os.environ.get('BLERG_UI_SUITE_LOCK')
    if override:
        return Path(override)
    root = Path(__file__).resolve().parent.parent
    common = subprocess.check_output(['git', 'rev-parse', '--git-common-dir'], cwd=root, text=True).strip()
    identity = str((root / common).resolve())
    key = hashlib.sha256(identity.encode()).hexdigest()[:20]
    return Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'blerg' / 'gates' / key / 'ui-suite.lock'


def inherited_lock_fd(path, exclude=None):
    """Find a reusable open description without waiting for another process."""
    try:
        target = Path(path).stat()
    except OSError:
        return None
    descriptors = []
    for directory in ('/proc/self/fd', '/dev/fd'):
        try:
            descriptors = list(Path(directory).iterdir())
            break
        except OSError:
            continue
    for entry in descriptors:
        try:
            candidate = int(entry.name)
            if candidate == exclude:
                continue
            stat = os.fstat(candidate)
            if (stat.st_dev, stat.st_ino) != (target.st_dev, target.st_ino):
                continue
            fcntl.flock(candidate, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return candidate
        except (OSError, ValueError):
            continue
    return None


def command_lock_fds(command, *descriptors, go_binary=None):
    # Go's security tests compare a direct exec with a contained exec. A
    # controller descriptor is not part of either command's requested state.
    # Keep the lock in the parent; Go commands never need a nested UI lock.
    if command and (Path(command[0]).name == 'go' or command[0] == go_binary):
        return ()
    return tuple(sorted({fd for fd in descriptors if fd is not None}))


@contextmanager
def ui_suite_lock(enabled=True, path=None):
    if not enabled:
        yield None
        return
    path = Path(path) if path is not None else lock_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
    owned = True
    try:
        # flock's descriptor survives exec. Reuse its open file description;
        # locking a second descriptor would wait forever for our own parent.
        inherited = inherited_lock_fd(path, exclude=fd)
        if inherited is not None:
            os.close(fd)
            fd, owned = inherited, False
        if owned:
            fcntl.flock(fd, fcntl.LOCK_EX)
        yield fd
    finally:
        if owned:
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)
