"""Private file import needs no internal component key."""
import importlib.util
import json
import contextlib
import io
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('desktop_enroll', Path(__file__).with_name('enroll.py'))
enrollment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(enrollment)


class ImportCredentialTests(unittest.TestCase):
    def test_import_issuer_response_and_rotate_privately(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            source, state = root / 'issuer.json', root / 'state'
            for digit in ('a', 'b'):
                source.write_text(json.dumps({'binding': {'desktop_id': 'desktop'}, 'token': 'bdt_' + digit * 64}))
                source.chmod(0o600)
                enrollment.import_credential(source, state)
                saved = state / 'desktop-credential.json'
                self.assertEqual(json.loads(saved.read_text())['token'], 'bdt_' + digit * 64)
                self.assertEqual(saved.stat().st_mode & 0o777, 0o600)
                self.assertEqual(state.stat().st_mode & 0o777, 0o700)

    def test_shared_or_linked_source_and_shared_state_are_refused(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            source = root / 'issuer.json'
            source.write_text(json.dumps({'desktop_id': 'desktop', 'token': 'bdt_' + 'c' * 64}))
            source.chmod(0o644)
            with self.assertRaises(ValueError):
                enrollment.import_credential(source, root / 'state')
            source.chmod(0o600)
            link = root / 'link'
            link.symlink_to(source)
            with self.assertRaises(ValueError):
                enrollment.import_credential(link, root / 'state')
            state = root / 'shared'
            state.mkdir(mode=0o755)
            os.chmod(state, 0o755)
            with self.assertRaises(ValueError):
                enrollment.import_credential(source, state)


class LocalExportTests(unittest.TestCase):
    def test_operator_export_writes_private_file_for_import_without_browser_or_bearer_output(self):
        requests = []
        token = 'bdt_' + 'e' * 64
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append((self.path, self.headers.get('X-Internal-Key'), json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({'binding': {'desktop_id': 'desktop'}, 'token': token}).encode())
            def log_message(self, *args):
                pass
        with tempfile.TemporaryDirectory() as root, ThreadingHTTPServer(('127.0.0.1', 0), Handler) as server:
            root = Path(root)
            env = root / 'install.env'
            env.write_text('BLERG_CORE_INTERNAL_KEY=fixture-internal-key\n')
            env.chmod(0o600)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                result = subprocess.run([sys.executable, str(Path(__file__).with_name('enroll.py')), '--export', str(env), str(root / 'export'), f'http://127.0.0.1:{server.server_port}', 'desktop', 'person'], capture_output=True, text=True, check=False)
            finally:
                server.shutdown()
                thread.join()
            self.assertEqual(result.returncode, 0, 'operator export failed')
            self.assertNotIn(token, result.stdout + result.stderr)
            self.assertEqual(requests, [('/internal/desktops/bootstrap', 'fixture-internal-key', {'name': requests[0][2]['name'], 'desktop_id': 'desktop', 'owner_id': 'person'})])
            exported = root / 'export' / 'desktop-credential.json'
            self.assertEqual(exported.stat().st_mode & 0o777, 0o600)
            self.assertEqual(exported.parent.stat().st_mode & 0o777, 0o700)
            with contextlib.redirect_stdout(io.StringIO()) as output:
                enrollment.import_credential(exported, root / 'remote')
            self.assertNotIn(token, output.getvalue())
            self.assertEqual(json.loads((root / 'remote' / 'desktop-credential.json').read_text()), {'desktop_id': 'desktop', 'token': token})

    def test_export_requires_operator_key_even_with_an_existing_personal_file(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            env = root / 'install.env'
            env.write_text('')
            state = root / 'state'
            state.mkdir(mode=0o700)
            enrollment.store_credential(state, {'desktop_id': 'desktop', 'token': 'bdt_' + 'a' * 64})
            with self.assertRaisesRegex(ValueError, 'operator'):
                enrollment.enroll(env, state, 'http://127.0.0.1', 'desktop', 'person', require_internal=True)


if __name__ == '__main__':
    unittest.main()
