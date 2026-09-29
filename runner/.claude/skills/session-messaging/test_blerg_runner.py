#!/usr/bin/env python3
"""
Tests for the blerg-runner CLI.
Run with: python3 .claude/skills/session-messaging/test_blerg_runner.py
"""

import http.server
import json
import os
import socket
import subprocess
import sys
import threading
import time
import unittest
from urllib.parse import urlparse, parse_qs

SCRIPT = os.path.join(os.path.dirname(__file__), "blerg-runner")

# ── Stub HTTP server ───────────────────────────────────────────────────────────

class StubHandler(http.server.BaseHTTPRequestHandler):
    """Records requests and serves scripted responses."""

    def log_message(self, format, *args):
        pass  # silence access log

    def _read_body(self):
        length = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(length) if length else b""

    def do_POST(self):
        body = self._read_body()
        auth = self.headers.get("Authorization", "")
        self.server.recorded.append({
            "method": "POST",
            "path": self.path,
            "auth": auth,
            "body": json.loads(body) if body else {},
        })

        # If force_error_code is set, return that error for all requests
        if getattr(self.server, 'force_error_code', None):
            error_body = json.dumps({"error": "forced error"}).encode()
            self.send_response(self.server.force_error_code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(error_body)))
            self.end_headers()
            self.wfile.write(error_body)
            return

        if self.path == "/api/messages":
            resp = json.dumps({"id": self.server.message_id}).encode()
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)

        elif "/expire" in self.path:
            resp = b"{}"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)

        else:
            self.send_response(404)
            self.end_headers()

    def do_GET(self):
        auth = self.headers.get("Authorization", "")
        self.server.recorded.append({
            "method": "GET",
            "path": self.path,
            "auth": auth,
        })

        # If force_error_code is set, return that error for all requests
        if getattr(self.server, 'force_error_code', None):
            error_body = json.dumps({"error": "forced error"}).encode()
            self.send_response(self.server.force_error_code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(error_body)))
            self.end_headers()
            self.wfile.write(error_body)
            return

        # Simulate a two-step poll: first call returns unanswered, second answered.
        if "/answer" in self.path:
            self.server.answer_call_count += 1
            if self.server.answer_call_count < self.server.answer_after_n_calls:
                resp = json.dumps({"answered": False, "answer": None}).encode()
            else:
                resp = json.dumps({"answered": True, "answer": self.server.answer_text}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp)))
            self.end_headers()
            self.wfile.write(resp)
        else:
            self.send_response(404)
            self.end_headers()


def make_stub_server(message_id="msg-123", answer_text="yes go ahead",
                     answer_after_n_calls=2, force_error_code=None):
    """Spin up a stub HTTP server on a free port and return it."""
    server = http.server.HTTPServer(("127.0.0.1", 0), StubHandler)
    server.recorded = []
    server.message_id = message_id
    server.answer_text = answer_text
    server.answer_call_count = 0
    server.answer_after_n_calls = answer_after_n_calls
    server.force_error_code = force_error_code
    return server


def run_blerg_runner(args, env_override=None, timeout=10):
    """Run the blerg-runner script with given args and optional env overrides."""
    env = os.environ.copy()
    if env_override is not None:
        for k, v in env_override.items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v
    result = subprocess.run(
        [sys.executable, SCRIPT] + args,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
    )
    return result


def _unused_port():
    """Return a port number that is not currently in use (nothing listening)."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


# ── Tests ─────────────────────────────────────────────────────────────────────

class TestNoOpWhenUnconfigured(unittest.TestCase):
    """When Blerg Runner env vars are absent the CLI should exit 0 with a notice."""

    def _strip_env(self):
        return {
            "BLERG_RUNNER_SESSION_ID": None,
            "BLERG_RUNNER_SERVER_HTTP": None,
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }

    def test_update_noop(self):
        result = run_blerg_runner(["update", "done"], env_override=self._strip_env())
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertIn("blerg-runner", result.stderr.lower())  # notice on stderr

    def test_ask_noop(self):
        result = run_blerg_runner(["ask", "should I?"], env_override=self._strip_env())
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertIn("blerg-runner", result.stderr.lower())

    def test_note_noop(self):
        result = run_blerg_runner(["note", "idea"], env_override=self._strip_env())
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")


class TestUpdate(unittest.TestCase):
    """update subcommand POSTs the right payload with the bearer header."""

    def test_update_posts_correct_payload(self):
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-abc",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-xyz",
        }
        result = run_blerg_runner(["update", "task 1 finished"], env_override=env)
        t.join(timeout=5)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertEqual(len(server.recorded), 1)
        req = server.recorded[0]
        self.assertEqual(req["method"], "POST")
        self.assertEqual(req["path"], "/api/messages")
        self.assertEqual(req["auth"], "Bearer tok-xyz")
        self.assertEqual(req["body"]["kind"], "update")
        self.assertEqual(req["body"]["body"], "task 1 finished")
        self.assertEqual(req["body"]["session_id"], "sess-abc")

        server.server_close()

    def test_update_bearer_token(self):
        """The Authorization header must be exactly 'Bearer <token>'."""
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-abc",
            "BLERG_RUNNER_DAEMON_TOKEN": "my-secret-token",
        }
        run_blerg_runner(["update", "hi"], env_override=env)
        t.join(timeout=5)

        self.assertTrue(server.recorded, "No requests recorded")
        self.assertEqual(server.recorded[0]["auth"], "Bearer my-secret-token")

        server.server_close()


class TestAsk(unittest.TestCase):
    """ask subcommand POSTs then polls /answer and prints the answer to stdout."""

    def test_ask_posts_and_polls_answer(self):
        server = make_stub_server(
            message_id="ask-msg-1",
            answer_text="yes go ahead",
            answer_after_n_calls=2,
        )
        port = server.server_address[1]

        # We need the server to handle multiple requests: 1 POST + 2 GETs.
        def serve_n(n):
            for _ in range(n):
                server.handle_request()

        t = threading.Thread(target=serve_n, args=(3,))
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-def",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-abc",
        }
        result = run_blerg_runner(["ask", "should I proceed?", "--timeout", "30"], env_override=env)
        t.join(timeout=15)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}\nstdout: {result.stdout}")

        # POST to /api/messages
        posts = [r for r in server.recorded if r["method"] == "POST"]
        self.assertTrue(posts, "No POST recorded")
        self.assertEqual(posts[0]["path"], "/api/messages")
        self.assertEqual(posts[0]["body"]["kind"], "ask")
        self.assertEqual(posts[0]["body"]["body"], "should I proceed?")
        self.assertEqual(posts[0]["body"]["session_id"], "sess-def")

        # GET /answer
        gets = [r for r in server.recorded if r["method"] == "GET"]
        self.assertTrue(gets, "No GET recorded")
        self.assertIn("/answer", gets[0]["path"])
        self.assertIn("ask-msg-1", gets[0]["path"])

        # Answer printed to stdout
        self.assertIn("yes go ahead", result.stdout)

        server.server_close()

    def test_ask_prints_answer_to_stdout(self):
        """The answer must appear on stdout so agents can capture it."""
        server = make_stub_server(
            message_id="ask-msg-2",
            answer_text="do the blue one",
            answer_after_n_calls=1,
        )
        port = server.server_address[1]

        def serve_n(n):
            for _ in range(n):
                server.handle_request()

        t = threading.Thread(target=serve_n, args=(2,))
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-ghi",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-def",
        }
        result = run_blerg_runner(["ask", "which option?", "--timeout", "30"], env_override=env)
        t.join(timeout=10)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertIn("do the blue one", result.stdout)

        server.server_close()


class TestNote(unittest.TestCase):
    """note subcommand POSTs kind=note and returns immediately."""

    def test_note_posts_correct_payload(self):
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-note",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-note",
        }
        result = run_blerg_runner(["note", "interesting idea"], env_override=env)
        t.join(timeout=5)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertEqual(len(server.recorded), 1)
        req = server.recorded[0]
        self.assertEqual(req["method"], "POST")
        self.assertEqual(req["path"], "/api/messages")
        self.assertEqual(req["auth"], "Bearer tok-note")
        self.assertEqual(req["body"]["kind"], "note")
        self.assertEqual(req["body"]["body"], "interesting idea")
        self.assertEqual(req["body"]["session_id"], "sess-note")

        server.server_close()

    def test_note_returns_immediately(self):
        """note should return immediately after posting (not block for a reply)."""
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-note-imm",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-note-imm",
        }
        start = time.time()
        result = run_blerg_runner(["note", "quick idea"], env_override=env)
        elapsed = time.time() - start
        t.join(timeout=5)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertLess(elapsed, 5.0, "note should return quickly (not block for reply)")

        server.server_close()


class TestAskTimeoutExpiry(unittest.TestCase):
    """ask --timeout should POST to /expire and exit non-zero when time runs out."""

    def test_timeout_posts_expire_and_exits_nonzero(self):
        """Stub never answers; ask should expire and exit non-zero."""
        server = make_stub_server(
            message_id="msg-to-expire",
            answer_text="never",
            answer_after_n_calls=float('inf'),  # never returns answered:true
        )
        port = server.server_address[1]

        # Serve requests continuously — we don't know how many polls will fire
        # before the 1s deadline expires.
        t = threading.Thread(target=server.serve_forever)
        t.daemon = True
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-timeout",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-timeout",
        }
        # 1s ask timeout; allow 15s wall-clock for the subprocess to finish
        result = run_blerg_runner(
            ["ask", "will this ever be answered?", "--timeout", "1"],
            env_override=env,
            timeout=15,
        )

        server.shutdown()
        t.join(timeout=5)

        # Must exit non-zero
        self.assertNotEqual(result.returncode, 0,
            f"Expected non-zero exit; stderr: {result.stderr}")

        # Must have POSTed to the /expire endpoint
        expire_posts = [
            r for r in server.recorded
            if r["method"] == "POST" and "/expire" in r["path"]
        ]
        self.assertTrue(expire_posts,
            f"No expire POST recorded. Recorded: {server.recorded}")

        # Must emit a timeout message on stderr
        self.assertIn("timed out", result.stderr)


class TestErrorResponses(unittest.TestCase):
    """Non-2xx responses should cause exit non-zero with a stderr message."""

    def test_update_500_exits_nonzero_with_message(self):
        """A 500 from the server should make update exit non-zero with error on stderr."""
        server = make_stub_server(force_error_code=500)
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-err",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-err",
        }
        result = run_blerg_runner(["update", "hi"], env_override=env)
        t.join(timeout=5)
        server.server_close()

        self.assertNotEqual(result.returncode, 0,
            f"Expected non-zero; stderr: {result.stderr}")
        self.assertIn("500", result.stderr)

    def test_ask_500_exits_nonzero_with_message(self):
        """A 500 response to the initial ask POST should exit non-zero with error on stderr."""
        server = make_stub_server(force_error_code=500)
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-err",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-err",
        }
        result = run_blerg_runner(["ask", "question?", "--timeout", "5"], env_override=env)
        t.join(timeout=5)
        server.server_close()

        self.assertNotEqual(result.returncode, 0,
            f"Expected non-zero; stderr: {result.stderr}")
        self.assertIn("500", result.stderr)


class TestConnectionError(unittest.TestCase):
    """URLError (connection refused) should exit non-zero with a clean message."""

    def _env(self, port):
        return {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-conn",
            "BLERG_RUNNER_DAEMON_TOKEN": "tok-conn",
        }

    def test_update_connection_refused_exits_nonzero(self):
        """update to a non-listening port should exit non-zero with a clean error."""
        port = _unused_port()
        result = run_blerg_runner(["update", "hi"], env_override=self._env(port))
        self.assertNotEqual(result.returncode, 0, f"stderr: {result.stderr}")
        # Should be a clean blerg-runner: message, not a raw Python traceback
        self.assertIn("blerg-runner", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_ask_connection_refused_exits_nonzero(self):
        """ask to a non-listening port should exit non-zero with a clean error."""
        port = _unused_port()
        result = run_blerg_runner(
            ["ask", "question?", "--timeout", "5"],
            env_override=self._env(port),
        )
        self.assertNotEqual(result.returncode, 0, f"stderr: {result.stderr}")
        # Should be a clean blerg-runner: message, not a raw Python traceback
        self.assertIn("blerg-runner", result.stderr)
        self.assertNotIn("Traceback", result.stderr)


class TestBoardTokenMessaging(unittest.TestCase):
    """Assist sessions use BLERG_RUNNER_BOARD_TOKEN (no daemon token) to post messages."""

    def _board_env(self, port, board_token):
        """Return an env with ONLY the board token set — no daemon token."""
        return {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-board",
            "BLERG_RUNNER_DAEMON_TOKEN": None,   # explicitly absent
            "BLERG_RUNNER_BOARD_TOKEN": board_token,
        }

    def test_update_uses_board_token_when_no_daemon_token(self):
        """update POSTs with Authorization: Bearer <board token> when only BLERG_RUNNER_BOARD_TOKEN is set."""
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        result = run_blerg_runner(
            ["update", "board-token-update"],
            env_override=self._board_env(port, "board-tok-abc"),
        )
        t.join(timeout=5)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertEqual(len(server.recorded), 1)
        req = server.recorded[0]
        self.assertEqual(req["method"], "POST")
        self.assertEqual(req["path"], "/api/messages")
        # Must use the board token, not the daemon token.
        self.assertEqual(req["auth"], "Bearer board-tok-abc")
        self.assertEqual(req["body"]["kind"], "update")
        self.assertEqual(req["body"]["body"], "board-token-update")
        # session_id is still sent in the body.
        self.assertEqual(req["body"]["session_id"], "sess-board")

        server.server_close()

    def test_board_token_takes_precedence_over_legacy_daemon_token(self):
        """When both are set, the scoped board token wins over the legacy daemon token."""
        server = make_stub_server()
        port = server.server_address[1]
        t = threading.Thread(target=server.handle_request)
        t.start()

        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-both",
            "BLERG_RUNNER_DAEMON_TOKEN": "daemon-tok",
            "BLERG_RUNNER_BOARD_TOKEN": "board-tok",
        }
        result = run_blerg_runner(["update", "prefer-board"], env_override=env)
        t.join(timeout=5)

        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertTrue(server.recorded, "No requests recorded")
        self.assertEqual(server.recorded[0]["auth"], "Bearer board-tok")

        server.server_close()

    def test_noop_when_neither_token_nor_url(self):
        """If no server URL and no tokens are set, exit 0 with notice."""
        result = run_blerg_runner(["update", "noop"], env_override={
            "BLERG_RUNNER_SERVER_HTTP": None,
            "BLERG_RUNNER_SESSION_ID": None,
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        })
        self.assertEqual(result.returncode, 0, f"stderr: {result.stderr}")
        self.assertIn("blerg-runner", result.stderr.lower())


class TestDerivation(unittest.TestCase):
    """Fallback derivation so sessions spawned before the new env vars still work."""

    def setUp(self):
        import importlib.machinery
        self.cli = importlib.machinery.SourceFileLoader(
            "blerg_runner_derive", os.path.join(os.path.dirname(__file__), "blerg-runner")
        ).load_module()
        self._saved = dict(os.environ)

    def tearDown(self):
        os.environ.clear()
        os.environ.update(self._saved)

    def _http(self, env):
        os.environ.clear()
        os.environ.update(env)
        return self.cli._derive_server_http()

    def test_explicit_server_http_wins(self):
        self.assertEqual(self._http({"BLERG_RUNNER_SERVER_HTTP": "http://x/"}), "http://x")

    def test_preview_url_fallback(self):
        self.assertEqual(self._http({"BLERG_RUNNER_PREVIEW_URL": "http://runner.example.test/api/preview"}), "http://runner.example.test")

    def test_ws_url_fallback(self):
        self.assertEqual(self._http({"BLERG_RUNNER_SERVER_URL": "ws://runner.example.test/ws/daemon"}), "http://runner.example.test")
        self.assertEqual(self._http({"BLERG_RUNNER_SERVER_URL": "wss://runner.example.test/ws/daemon"}), "https://runner.example.test")

    def test_no_config_empty(self):
        self.assertEqual(self._http({}), "")

    def test_session_id_env_path(self):
        os.environ.clear()
        os.environ["BLERG_RUNNER_SESSION_ID"] = "abc-123"
        self.assertEqual(self.cli._derive_session_id(), "abc-123")


if __name__ == "__main__":
    unittest.main(verbosity=2)
