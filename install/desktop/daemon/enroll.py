#!/usr/bin/env python3
"""Enroll or rotate a private desktop credential; never print a bearer.

The local installer holds core's internal key in its private .env. A new
desktop redeems a person's one-time enrollment code (install.sh install
--enroll CODE; the code arrives on stdin). A remote installation without either
imports a person-issued credential file instead.
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


def enroll(env_file, state_dir, runner_url, desktop_id="", owner_id="", require_internal=False):
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
    if credential.exists() and not require_internal:
        info = credential.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError('desktop credential must be a private regular file')
        existing = json.loads(credential.read_text())
        desktop_id = existing.get('desktop_id', '')
        # A remote installer cannot rotate without its live personal login.
        if not values.get('BLERG_CORE_INTERNAL_KEY'):
            return
    key = values.get('BLERG_CORE_INTERNAL_KEY', '')
    if require_internal and not key:
        raise ValueError('local export requires the installation operator and core internal key')
    if not key:
        if values.get('BLERG_RUNNER_DAEMON_TOKEN', '').startswith('bdt_') and values.get('BLERG_RUNNER_DESKTOP_ID'):
            data = {'desktop_id': values['BLERG_RUNNER_DESKTOP_ID'], 'token': values['BLERG_RUNNER_DAEMON_TOKEN']}
        else:
            raise ValueError('in Settings → Account → Your desktops, select Add a desktop and run the command it shows (install.sh install --enroll CODE), or import a person-issued credential with daemon/install.sh import-credential /path/to/credential.json, then run daemon/install.sh install')
    else:
        url = urllib.parse.urlsplit(runner_url)
        scheme = {'ws': 'http', 'wss': 'https'}.get(url.scheme, url.scheme)
        if scheme != 'https' and not (scheme == 'http' and url.hostname in ('localhost', '127.0.0.1', '::1')):
            raise ValueError('desktop enrollment requires HTTPS outside localhost')
        base = urllib.parse.urlunsplit((scheme, url.netloc, '/internal/desktops/bootstrap', '', ''))
        body = {'name': os.environ.get('BLERG_RUNNER_DAEMON_NAME') or values.get('BLERG_RUNNER_DAEMON_NAME') or socket.gethostname().lower(), 'desktop_id': desktop_id,
                'owner_id': owner_id or os.environ.get('BLERG_RUNNER_INSTALLING_ACCOUNT_ID') or values.get('BLERG_RUNNER_INSTALLING_ACCOUNT_ID', '')}
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
    store_credential(state, data)
    print('Desktop enrolled. Its owner is recorded; the credential stays in private daemon state.')


def read_env(env_file):
    values = {}
    path = Path(env_file)
    if path.exists():
        for line in path.read_text().splitlines():
            if '=' in line and not line.startswith('#'):
                key, value = line.split('=', 1)
                values[key] = value.strip().strip('"').strip("'")
    return values


def private_state(state_dir):
    state = Path(state_dir)
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = state.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('daemon state directory must be private and owned by this account')
    return state


REDEEM_REFUSED = ('the enrollment code was refused: it is wrong, expired, revoked or already used. '
                  'In Settings → Account → Your desktops, select Add a desktop and run the new command.')


def redeem(env_file, state_dir, runner_url, code):
    """Exchange a one-time enrollment code for this new desktop's private credential."""
    code = code.strip()
    if not code.startswith('bec_') or len(code) != 44:
        raise ValueError('the enrollment code is not complete; copy the whole command again from Settings → Account → Your desktops')
    values = read_env(env_file)
    state = private_state(state_dir)
    url = urllib.parse.urlsplit(runner_url)
    scheme = {'ws': 'http', 'wss': 'https'}.get(url.scheme, url.scheme)
    loopback = url.hostname in ('localhost', '127.0.0.1', '::1')
    if scheme != 'https' and not (scheme == 'http' and loopback):
        raise ValueError('desktop enrollment requires HTTPS (wss://) outside localhost')
    netloc = url.netloc.rsplit('@', 1)[-1]
    base = urllib.parse.urlunsplit((scheme, netloc, '/api/desktops/enroll', '', ''))
    name = os.environ.get('BLERG_RUNNER_DAEMON_NAME') or values.get('BLERG_RUNNER_DAEMON_NAME') or socket.gethostname().lower()
    handlers = [NoRedirect()]
    if loopback:
        handlers.append(urllib.request.ProxyHandler({}))
    opener = urllib.request.build_opener(*handlers)
    req = urllib.request.Request(base, json.dumps({'code': code, 'name': name}).encode(), {'Content-Type': 'application/json'})
    try:
        with opener.open(req, timeout=20) as res:
            response = json.loads(res.read(65537))
    except urllib.error.HTTPError as error:
        if error.code in (400, 403):
            raise ValueError(REDEEM_REFUSED) from None
        if error.code == 429:
            raise ValueError('too many enrollment attempts; wait a minute, then run the command again') from None
        if error.code == 404:
            raise ValueError('this server cannot enroll a desktop with a code; update it to Blerg 0.5 or later') from None
        raise ValueError('the server could not enroll this desktop (HTTP %d); select Add a desktop for a new code and try again' % error.code) from None
    except (urllib.error.URLError, OSError):
        raise ValueError('could not reach the server; check BLERG_RUNNER_SERVER_URL and your network, then run the command again') from None
    if not isinstance(response, dict) or not isinstance(response.get('binding'), dict):
        raise ValueError('invalid enrollment response')
    store_credential(state, {'desktop_id': response['binding'].get('desktop_id'), 'token': response.get('token')})
    print('Desktop enrolled to you as "%s". Its credential stays in private daemon state.' % name)


def store_credential(state, data):
    credential = state / 'desktop-credential.json'
    if not isinstance(data, dict) or not isinstance(data.get('desktop_id'), str) or not data['desktop_id'] or not isinstance(data.get('token'), str) or not data['token'].startswith('bdt_') or len(data['token']) != 68:
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


def import_credential(source, state_dir):
    path = Path(source)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_size > 65536:
        raise ValueError('credential import requires a private regular file owned by this account')
    with path.open() as src:
        if not os.path.samestat(info, os.fstat(src.fileno())):
            raise ValueError('credential import source changed')
        data = json.load(src)
    if not isinstance(data, dict):
        raise ValueError('invalid enrollment response')
    if 'binding' in data:
        data = {'desktop_id': data['binding']['desktop_id'], 'token': data['token']}
    state = Path(state_dir)
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = state.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('daemon state directory must be private and owned by this account')
    store_credential(state, data)
    print('Desktop credential imported privately. Run daemon/install.sh install to reconnect.')


if __name__ == '__main__':
    try:
        if sys.argv[1:2] == ['--export']:
            enroll(*sys.argv[2:], require_internal=True)
        elif sys.argv[1:2] == ['--redeem']:
            redeem(*sys.argv[2:5], sys.stdin.readline())
        elif sys.argv[1:2] == ['--import']:
            import_credential(*sys.argv[2:])
        else:
            enroll(*sys.argv[1:])
    except (ValueError, OSError, KeyError, TypeError) as error:
        # Do not print response bodies, request headers, .env contents or tokens.
        print('Desktop enrollment failed: ' + (str(error) if isinstance(error, ValueError) else 'could not store or request a private credential'), file=sys.stderr)
        sys.exit(7)
