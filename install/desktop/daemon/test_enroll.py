import contextlib
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import threading
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('desktop_enroll', Path(__file__).with_name('enroll.py'))
enrollment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(enrollment)


class DesktopEnrollmentTests(unittest.TestCase):
    def test_upgrade_is_private_atomic_and_reuses_desktop_identity(self):
        calls = []
        desktop = '00000000-0000-4000-8000-000000000174'
        bearer = 'bdt_' + 'a' * 64
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_POST(self):
                calls.append((self.headers.get('X-Internal-Key'), json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({'binding': {'desktop_id': desktop}, 'token': bearer}).encode())
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as root:
                env = Path(root) / 'env'
                env.write_text('BLERG_CORE_INTERNAL_KEY=fixture-internal-key\n')
                state = Path(root) / 'state'
                output = io.StringIO()
                with contextlib.redirect_stdout(output), mock.patch.dict(os.environ, {'BLERG_RUNNER_DAEMON_NAME': 'workstation'}):
                    enrollment.enroll(env, state, f'ws://127.0.0.1:{server.server_port}/ws/daemon')
                    enrollment.enroll(env, state, f'ws://127.0.0.1:{server.server_port}/ws/daemon')
                credential = state / 'desktop-credential.json'
                self.assertEqual(stat.S_IMODE(credential.stat().st_mode), 0o600)
                self.assertEqual(json.loads(credential.read_text()), {'desktop_id': desktop, 'token': bearer})
                self.assertEqual(calls[0][0], 'fixture-internal-key')
                self.assertEqual(calls[0][1]['name'], 'workstation')
                self.assertEqual(calls[1][1]['desktop_id'], desktop)
                self.assertNotIn(bearer, output.getvalue())
                self.assertNotIn('fixture-internal-key', output.getvalue())
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_enrollment_waits_for_active_work_confirmation(self):
        script = Path(__file__).with_name('install.sh').read_text()
        function = 'put_binary_in_place() {' + script.split('put_binary_in_place() {', 1)[1].split('\n}', 1)[0] + '\n}'
        for approved in (False, True):
            with self.subTest(approved=approved), tempfile.TemporaryDirectory() as root:
                base = Path(root)
                (base / 'old').write_text('old binary')
                (base / 'new').write_text('new binary')
                commands = '\npersonal_desktop_install() { return 0; }\nconfirm_reinstall_over_work() { return "$CONFIRM_RESULT"; }\nblerg_ok() { :; }\nenroll_desktop() { echo enrolled > "$ENROLL_MARK"; }\nput_binary_in_place 1\n'
                subprocess.run(['bash', '-c', function + commands], check=True, env={**os.environ,
                    'BIN_PATH': str(base / 'old'), 'NEW_BIN_PATH': str(base / 'new'),
                    'ENROLL_MARK': str(base / 'enrolled'), 'CONFIRM_RESULT': '0' if approved else '1',
                    'ASSUME_YES': '0', 'EXIT_WORK_RUNNING': '5'})
                self.assertEqual((base / 'enrolled').exists(), approved)
                self.assertEqual((base / 'old').read_text(), 'new binary' if approved else 'old binary')

    def test_person_credential_is_not_copied_into_service_environment(self):
        script = Path(__file__).with_name('install.sh').read_text()
        function = 'render_placeholders() {' + script.split('render_placeholders() {', 1)[1].split('\n}', 1)[0] + '\n}'
        token = 'bdt_' + 'a' * 64
        rendered = subprocess.run(['bash', '-c', function + '\nrender_placeholders'],
                                  input='token=__DAEMON_TOKEN__', text=True, capture_output=True,
                                  check=True, env={**os.environ, 'DAEMON_TOKEN': token}).stdout
        self.assertEqual(rendered, 'token=')
        self.assertNotIn(token, rendered)

    def test_shared_token_is_not_silently_installed(self):
        with tempfile.TemporaryDirectory() as root:
            env = Path(root) / 'env'
            env.write_text('BLERG_RUNNER_DAEMON_TOKEN=old-shared-credential\n')
            with self.assertRaisesRegex(ValueError, 'person-issued'):
                enrollment.enroll(env, Path(root) / 'state', 'wss://runner.example.com/ws/daemon')
            self.assertFalse((Path(root) / 'state/desktop-credential.json').exists())

    def test_nonlocal_plaintext_enrollment_is_refused(self):
        with tempfile.TemporaryDirectory() as root:
            env = Path(root) / 'env'
            env.write_text('BLERG_CORE_INTERNAL_KEY=fixture-internal-key\n')
            with self.assertRaisesRegex(ValueError, 'requires HTTPS'):
                enrollment.enroll(env, Path(root) / 'state', 'ws://runner.example.com/ws/daemon')


    def test_new_desktop_redeems_a_one_time_code_privately(self):
        code = 'bec_' + '1' * 40
        bearer = 'bdt_' + 'f' * 64
        desktop = '00000000-0000-4000-8000-000000000193'
        seen = []
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                seen.append((self.path, self.headers.get('X-Internal-Key'), body))
                if body.get('code') != code or len(seen) > 1:
                    self.send_response(403)
                    self.end_headers()
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(json.dumps({'binding': {'desktop_id': desktop, 'owner_id': 'person'}, 'token': bearer}).encode())
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as root:
                state = Path(root) / 'state'
                url = f'ws://127.0.0.1:{server.server_port}/ws/daemon'
                output = io.StringIO()
                with contextlib.redirect_stdout(output), mock.patch.dict(os.environ, {'BLERG_RUNNER_DAEMON_NAME': 'new-laptop'}):
                    enrollment.redeem(Path(root) / 'missing.env', state, url, code + '\n')
                    with self.assertRaisesRegex(ValueError, 'refused: it is wrong, expired, revoked or already used'):
                        enrollment.redeem(Path(root) / 'missing.env', state, url, code)
                credential = state / 'desktop-credential.json'
                self.assertEqual(stat.S_IMODE(credential.stat().st_mode), 0o600)
                self.assertEqual(stat.S_IMODE(state.stat().st_mode), 0o700)
                self.assertEqual(json.loads(credential.read_text()), {'desktop_id': desktop, 'token': bearer})
                self.assertEqual(seen[0][0], '/api/desktops/enroll')
                self.assertIsNone(seen[0][1])  # no internal key on a remote desktop
                self.assertEqual(seen[0][2], {'code': code, 'name': 'new-laptop'})
                self.assertNotIn(code, output.getvalue())
                self.assertNotIn(bearer, output.getvalue())
        finally:
            server.shutdown()
            server.server_close()
            thread.join()

    def test_code_redemption_refuses_plaintext_and_incomplete_codes(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaisesRegex(ValueError, 'requires HTTPS'):
                enrollment.redeem(Path(root) / 'env', Path(root) / 'state', 'ws://runner.example.com/ws/daemon', 'bec_' + '1' * 40)
            with self.assertRaisesRegex(ValueError, 'not complete'):
                enrollment.redeem(Path(root) / 'env', Path(root) / 'state', 'wss://runner.example.com/ws/daemon', 'bec_123')
            self.assertFalse((Path(root) / 'state/desktop-credential.json').exists())


if __name__ == '__main__':
    unittest.main()
