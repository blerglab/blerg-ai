"""Fake engine workload: real UID/filesystem, launch environment and shipped CLI."""
import http.server
import json
import os
import pathlib
import subprocess
import threading

assert os.geteuid() == 1000
assert os.stat('/tmp').st_mode & 0o1777 == 0o1777
assert os.environ['BLERG_RUNNER_SESSION_ID'] == '00000000-0000-4000-8000-000000000570'
assert os.environ['BLERG_RUNNER_SESSION_TOKEN']
assert os.environ['BLERG_RUNNER_BOARD_ID'] == 'fixture-board'
assert os.environ['BLERG_RUNNER_BOARD_TOKEN'] == 'fixture-board-grant'
assert os.environ['BLERG_RUNNER_SERVER_HTTP'] == 'http://localhost:19191'
assert 'BLERG_RUNNER_DAEMON_TOKEN' not in os.environ
for private in ['/run/blerg-workspace-private/snapshot', '/workspace/.blerg-runner-daemon/fixture-token']:
    assert not os.access(private, os.R_OK)
    assert not os.access(private, os.W_OK)
skills = list(pathlib.Path('/tmp').glob('blerg-session-skills-*/skill-packs/*/skills/managing-tickets/SKILL.md'))
assert len(skills) == 1
assert 'Manage tickets' in skills[0].read_text()
assert skills[0].stat().st_uid == 1001
assert not os.access(skills[0], os.W_OK)
assert not os.access(skills[0].parent, os.W_OK)
root = skills[0].parents[4]
moved = root.with_name(root.name + '.moved')
denied = False
try:
    root.rename(moved)
    moved.rename(root)
except PermissionError:
    denied = True
assert denied, 'agent could rename the trusted skill root'
card = {'id': '11111111-1111-4111-8111-111111111111', 'number': 555,
        'board_id': 'fixture-board', 'title': 'Cluster fixture card'}
calls = []

class Gateway(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, value):
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(value).encode())

    def do_GET(self):
        assert self.headers['Authorization'] == 'Bearer ' + os.environ['BLERG_RUNNER_SESSION_TOKEN']
        assert self.path.endswith('/board-access')
        calls.append('access')
        self.reply({'configured': True, 'boards': [{'id': 'fixture-board', 'level': 'read'}]})

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        gateway = self.path == '/mcp-gw/board'
        if gateway:
            assert self.headers['Authorization'] == 'Bearer fixture-board-grant'
            assert request['method'] == 'tools/call'
            tool = request['params']['name']
        else:
            assert self.headers['Authorization'] == 'Bearer ' + os.environ['BLERG_RUNNER_SESSION_TOKEN']
            assert self.path.endswith('/board-tools')
            assert request['board'] == 'fixture-board'
            tool = request['tool']
        assert tool == 'blerg_card_get'
        calls.append('gateway' if gateway else 'session')
        result = {'content': [{'type': 'text', 'text': json.dumps(card)}]}
        self.reply({'jsonrpc': '2.0', 'id': request['id'], 'result': result} if gateway else result)

server = http.server.HTTPServer(('localhost', 19191), Gateway)
thread = threading.Thread(target=server.serve_forever)
thread.start()
try:
    result = subprocess.run(['python3', '/fixture/blerg-runner', 'ticket', 'get', '555'],
                            capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    assert 'Cluster fixture card' in result.stdout, result.stderr
    assert 'gateway' in calls
    # The session token must also work when board access is resolved dynamically.
    session_env = dict(os.environ)
    for key in ('BLERG_RUNNER_BOARD_TOKEN', 'BLERG_RUNNER_BOARD_GATEWAY_URL'):
        session_env.pop(key, None)
    result = subprocess.run(['python3', '/fixture/blerg-runner', 'ticket', 'get', '555'],
                            env=session_env, capture_output=True, text=True, timeout=15)
    assert result.returncode == 0 and 'Cluster fixture card' in result.stdout, result.stderr
    assert 'access' in calls and 'session' in calls
finally:
    server.shutdown()
    thread.join()
    server.server_close()
print('agent skill read, credential isolation, session environment and ticket get passed')

# Commit with the generated Job's default identity, without per-repo configuration.
pathlib.Path('worker.txt').write_text('cluster worker\n')
subprocess.run(['git', 'add', 'worker.txt'], check=True)
subprocess.run(['git', 'commit', '-qm', 'worker result'], check=True)
print(json.dumps({'type': 'result', 'subtype': 'success', 'session_id': 'fixture-engine'}), flush=True)
