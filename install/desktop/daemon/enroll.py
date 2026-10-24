#!/usr/bin/env python3
"""Enroll or rotate a private desktop credential; never print a bearer.

The local installer holds core's internal key in its private .env. A remote
installation without that key imports a person-issued credential file instead.
"""
import json
import os
from pathlib import Path
import socket
import stat
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def enroll(env_file, state_dir, runner_url):
    values = {}
    for line in Path(env_file).read_text().splitlines():
        if '=' in line and not line.startswith('#'):
            key, value = line.split('=', 1)
            values[key] = value.strip().strip('"').strip("'")
    state = Path(state_dir)
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = state.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('daemon state directory must be private and owned by this account')
    credential = state / 'desktop-credential.json'
    desktop_id = ''
    if credential.exists():
        info = credential.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError('desktop credential must be a private regular file')
        existing = json.loads(credential.read_text())
        desktop_id = existing.get('desktop_id', '')
        # A remote installer cannot rotate without its live personal login.
        if not values.get('BLERG_CORE_INTERNAL_KEY'):
            return
    key = values.get('BLERG_CORE_INTERNAL_KEY', '')
    if not key:
        if values.get('BLERG_RUNNER_DAEMON_TOKEN', '').startswith('bdt_') and values.get('BLERG_RUNNER_DESKTOP_ID'):
            data = {'desktop_id': values['BLERG_RUNNER_DESKTOP_ID'], 'token': values['BLERG_RUNNER_DAEMON_TOKEN']}
        else:
            raise ValueError('import your person-issued desktop credential or enroll from the local installer; the shared daemon token is deprecated')
    else:
        url = urllib.parse.urlsplit(runner_url)
        scheme = {'ws': 'http', 'wss': 'https'}.get(url.scheme, url.scheme)
        if scheme != 'https' and not (scheme == 'http' and url.hostname in ('localhost', '127.0.0.1', '::1')):
            raise ValueError('desktop enrollment requires HTTPS outside localhost')
        base = urllib.parse.urlunsplit((scheme, url.netloc, '/internal/desktops/bootstrap', '', ''))
        body = {'name': os.environ.get('BLERG_RUNNER_DAEMON_NAME') or values.get('BLERG_RUNNER_DAEMON_NAME') or socket.gethostname().lower(), 'desktop_id': desktop_id,
                'owner_id': os.environ.get('BLERG_RUNNER_INSTALLING_ACCOUNT_ID') or values.get('BLERG_RUNNER_INSTALLING_ACCOUNT_ID', '')}
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        req = urllib.request.Request(base, json.dumps(body).encode(), {'Content-Type': 'application/json', 'X-Internal-Key': key})
        try:
            with opener.open(req, timeout=15) as res:
                response = json.loads(res.read(65537))
        except urllib.error.HTTPError as error:
            if error.code == 409:
                raise ValueError('create your installing account first, then run daemon/install.sh install again; legacy desktops default to the first enabled admin') from None
            raise ValueError('desktop enrollment was refused; check the internal key and installing account') from None
        data = {'desktop_id': response['binding']['desktop_id'], 'token': response['token']}
    if not data['desktop_id'] or not data['token'].startswith('bdt_') or len(data['token']) != 68:
        raise ValueError('invalid enrollment response')
    fd, temporary = tempfile.mkstemp(prefix='.desktop-credential-', dir=state)
    try:
        with os.fdopen(fd, 'w') as out:
            os.fchmod(out.fileno(), 0o600)
            json.dump(data, out)
            out.flush()
            os.fsync(out.fileno())
        os.replace(temporary, credential)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    print('Desktop enrolled. Its owner is recorded; the credential stays in private daemon state.')


if __name__ == '__main__':
    try:
        enroll(*sys.argv[1:])
    except (ValueError, OSError, KeyError) as error:
        # Do not print response bodies, request headers, .env contents or tokens.
        print('Desktop enrollment failed: ' + (str(error) if isinstance(error, ValueError) else 'could not store or request a private credential'), file=sys.stderr)
        sys.exit(7)
