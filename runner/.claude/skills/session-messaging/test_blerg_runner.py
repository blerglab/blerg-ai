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


# ── publish ───────────────────────────────────────────────────────────────────

class ArtifactStubHandler(http.server.BaseHTTPRequestHandler):
    """Stands in for POST /api/sessions/{id}/artifacts: records the raw body and answers
    with a scripted status/JSON (server.reply_status, server.reply_json)."""

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({
            "path": self.path,
            "auth": self.headers.get("Authorization", ""),
            "name": self.headers.get("X-Artifact-Name", ""),
            "ctype": self.headers.get("Content-Type", ""),
            "body": body,
        })
        resp = json.dumps(self.server.reply_json).encode()
        self.send_response(self.server.reply_status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


def make_artifact_server(view="text", status=201, reply=None):
    server = http.server.HTTPServer(("127.0.0.1", 0), ArtifactStubHandler)
    server.recorded = []
    server.reply_status = status
    server.reply_json = reply if reply is not None else {
        "id": "a" * 32, "size": 0, "content_type": "text/plain", "view": view,
        "url": "/sessions/sess-pub?artifact=" + "a" * 32,
    }
    return server


class TestPublish(unittest.TestCase):
    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def _file(self, name, data=b"hello"):
        p = os.path.join(self.tmp.name, name)
        with open(p, "wb") as f:
            f.write(data)
        return p

    def _env(self, port, **extra):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": "sess-pub",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,  # never present in a session
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }
        env.update(extra)
        return env

    def _publish(self, args, server=None, **kw):
        server = server or make_artifact_server()
        t = threading.Thread(target=server.handle_request)
        t.start()
        result = run_blerg_runner(["publish"] + args, env_override=self._env(server.server_address[1]), **kw)
        if t.is_alive():
            # The CLI never reached the server (validation failed first): unblock the stub.
            try:
                socket.create_connection(server.server_address, timeout=2).close()
            except OSError:
                pass
        t.join(timeout=5)
        server.server_close()
        return result, server

    def test_noop_when_not_under_blerg(self):
        p = self._file("report.md")
        env = {k: None for k in ("BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_SERVER_HTTP",
                                  "BLERG_RUNNER_SESSION_TOKEN", "BLERG_RUNNER_DAEMON_TOKEN", "BLERG_RUNNER_BOARD_TOKEN")}
        result = run_blerg_runner(["publish", p], env_override=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("not running under blerg runner", result.stderr.lower())

    def test_posts_raw_bytes_with_session_token(self):
        data = bytes(range(256)) * 10  # binary, not JSON
        p = self._file("blob.bin", data)
        result, server = self._publish([p])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(server.recorded), 1)
        req = server.recorded[0]
        self.assertEqual(req["path"], "/api/sessions/sess-pub/artifacts")
        self.assertEqual(req["auth"], "Bearer sess-tok")
        self.assertEqual(req["name"], "blob.bin")
        self.assertEqual(req["body"], data)

    def test_name_option_is_percent_encoded(self):
        p = self._file("a.txt")
        result, server = self._publish([p, "--name", "résumé v2.txt"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["name"], "r%C3%A9sum%C3%A9%20v2.txt")

    def test_success_prints_link_size_and_viewable(self):
        p = self._file("report.md", b"x" * 2048)
        server = make_artifact_server(view="markdown")
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published report.md", result.stdout)
        self.assertIn("2.0 KB", result.stdout)
        self.assertIn("viewable in the app", result.stdout)
        self.assertIn("/sessions/sess-pub?artifact=", result.stdout)
        self.assertNotIn("no in-app preview", result.stdout)

    def test_first_version_prints_no_version_note(self):
        p = self._file("report.md", b"x" * 2048)
        server = make_artifact_server(view="markdown", reply={
            "id": "a" * 32, "name": "report.md", "version": 1, "previous": None, "view": "markdown",
            "url": "/sessions/sess-pub?artifact=" + "a" * 32,
        })
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published report.md (2.0 KB) \u2014 viewable in the app", result.stdout)
        self.assertNotIn("previous", result.stdout)
        self.assertNotIn(" as v", result.stdout)

    def test_a_new_version_says_which_one_and_what_it_follows(self):
        p = self._file("report.md")
        server = make_artifact_server(view="markdown", reply={
            "id": "a" * 32, "name": "report.md", "version": 3, "previous": 2, "view": "markdown",
            "url": "/sessions/sess-pub?artifact=" + "a" * 32,
        })
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published report.md as v3 (previous: v2) \u2014 viewable in the app", result.stdout)
        self.assertIn("/sessions/sess-pub?artifact=", result.stdout)

    def test_a_new_version_of_a_download_only_file_keeps_the_hint(self):
        p = self._file("memo.docx", b"PK\x03\x04")
        server = make_artifact_server(view="none", reply={
            "id": "a" * 32, "name": "memo.docx", "version": 2, "previous": 1, "view": "none", "url": "/sessions/sess-pub?artifact=x",
        })
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published memo.docx as v2 (previous: v1) \u2014 no in-app preview (download only)", result.stdout)
        self.assertIn("pdf, html or markdown", result.stdout)

    def test_a_server_that_sends_no_version_still_prints_the_old_line(self):
        p = self._file("report.md", b"x")
        result, _ = self._publish([p], server=make_artifact_server(view="markdown"))
        self.assertIn("Published report.md (1 B) \u2014 viewable in the app", result.stdout)

    def test_version_limit_is_one_clear_line(self):
        p = self._file("report.md")
        msg = "This file already has 20 versions; delete an old one first."
        result, _ = self._publish([p], server=make_artifact_server(status=409, reply={"error": msg}))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(msg, result.stderr)
        self.assertEqual(len(result.stderr.strip().splitlines()), 1, result.stderr)

    def test_download_only_type_prints_status_and_a_hint(self):
        p = self._file("report.docx", b"PK\x03\x04")
        server = make_artifact_server(view="none")
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published report.docx", result.stdout)
        self.assertIn("no in-app preview (download only)", result.stdout)
        self.assertIn("pdf, html or markdown", result.stdout)

    def test_missing_file_is_an_error(self):
        result, server = self._publish([os.path.join(self.tmp.name, "nope.txt")])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-runner publish:", result.stderr)
        self.assertIn("nope.txt", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_too_large_file_is_refused_before_sending(self):
        p = self._file("big.bin", b"")
        with open(p, "r+b") as f:
            f.truncate(25 * 1024 * 1024 + 1)  # sparse
        result, server = self._publish([p])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("25 MiB", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_server_errors_are_one_clear_line_and_nonzero(self):
        p = self._file("a.txt")
        for status, body, needle in (
            (413, {"error": "file is larger than 25 MiB"}, "larger than 25 MiB"),
            (409, {"error": "session has ended"}, "session has ended"),
            (403, {"error": "forbidden"}, "not allowed"),
            (401, {"error": "unauthorized"}, "not allowed"),
            (500, {"error": "boom"}, "boom"),
        ):
            with self.subTest(status=status):
                result, _ = self._publish([p], server=make_artifact_server(status=status, reply=body))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("blerg-runner publish:", result.stderr)
                self.assertIn(needle, result.stderr)
                self.assertEqual(len(result.stderr.strip().splitlines()), 1, result.stderr)

    def test_connection_error_is_nonzero(self):
        p = self._file("a.txt")
        result = run_blerg_runner(["publish", p], env_override=self._env(_unused_port()))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-runner publish:", result.stderr)

    def test_directory_is_zipped_skipping_git_node_modules_and_symlinks(self):
        import io
        import zipfile
        root = os.path.join(self.tmp.name, "proj")
        os.makedirs(os.path.join(root, "src"))
        os.makedirs(os.path.join(root, ".git"))
        os.makedirs(os.path.join(root, "node_modules", "dep"))
        for rel, data in (("README.md", b"hi"), ("src/main.py", b"print(1)"), (".git/config", b"secret"),
                          ("node_modules/dep/index.js", b"x")):
            with open(os.path.join(root, rel), "wb") as f:
                f.write(data)
        outside = self._file("outside.txt", b"outside")
        os.symlink(outside, os.path.join(root, "link.txt"))
        os.symlink(self.tmp.name, os.path.join(root, "linkdir"))
        result, server = self._publish([root])
        self.assertEqual(result.returncode, 0, result.stderr)
        req = server.recorded[0]
        self.assertEqual(req["name"], "proj.zip")
        names = sorted(zipfile.ZipFile(io.BytesIO(req["body"])).namelist())
        self.assertEqual(names, ["proj/README.md", "proj/src/main.py"])

    def test_empty_directory_is_an_error(self):
        root = os.path.join(self.tmp.name, "empty")
        os.makedirs(os.path.join(root, ".git"))
        result, server = self._publish([root])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("nothing to publish", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_directory_name_option_gets_a_zip_extension(self):
        root = os.path.join(self.tmp.name, "d")
        os.makedirs(root)
        with open(os.path.join(root, "f"), "wb") as f:
            f.write(b"1")
        result, server = self._publish([root, "--name", "bundle"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["name"], "bundle.zip")


class BoardCardStubHandler(http.server.BaseHTTPRequestHandler):
    """Stands in for the board service: records PATCH /api/cards/{id} and answers with a scripted status."""

    def log_message(self, format, *args):
        pass

    def do_PATCH(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({
            "path": self.path,
            "auth": self.headers.get("Authorization", ""),
            "body": json.loads(body) if body else {},
        })
        resp = json.dumps(self.server.reply_json).encode()
        self.send_response(self.server.reply_status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


def make_board_server(status=200, reply=None):
    server = http.server.HTTPServer(("127.0.0.1", 0), BoardCardStubHandler)
    server.recorded = []
    server.reply_status = status
    server.reply_json = reply if reply is not None else {"id": "card-1"}
    return server


class TestPublishToCard(unittest.TestCase):
    """`publish --card` also attaches the file to a board card (the card session's own, or a named one)."""

    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = os.path.join(self.tmp.name, "report.md")
        with open(self.path, "wb") as f:
            f.write(b"hello")

    def _run(self, extra_args, board_env=True, board_status=200, artifact_reply=None, board_url=None):
        art = make_artifact_server(reply=artifact_reply)
        board = make_board_server(status=board_status)
        threads = [threading.Thread(target=art.handle_request), threading.Thread(target=board.handle_request)]
        for th in threads:
            th.start()
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{art.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-pub",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
            "BLERG_BOARD_URL": None, "BLERG_BOARD_TOKEN": None, "BLERG_BOARD_CARD": None,
        }
        if board_env:
            env.update({
                "BLERG_BOARD_URL": board_url or f"http://127.0.0.1:{board.server_address[1]}",
                "BLERG_BOARD_TOKEN": "board-tok",
                "BLERG_BOARD_CARD": "card-env",
            })
        result = run_blerg_runner(["publish", self.path] + extra_args, env_override=env)
        for srv in (art, board):  # unblock a stub the CLI never called
            try:
                socket.create_connection(srv.server_address, timeout=2).close()
            except OSError:
                pass
        for th in threads:
            th.join(timeout=5)
        art.server_close()
        board.server_close()
        return result, art, board

    def test_card_flag_attaches_to_the_sessions_own_card(self):
        result, _, board = self._run(["--card"], artifact_reply={
            "id": "f" * 32, "name": "report.md", "version": 3, "view": "markdown",
            "url": "/sessions/sess-pub?artifact=" + "f" * 32})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(board.recorded), 1)
        req = board.recorded[0]
        self.assertEqual(req["path"], "/api/cards/card-env")
        self.assertEqual(req["auth"], "Bearer board-tok")
        self.assertEqual(req["body"], {"add_links": [{
            "kind": "artifact", "url": "/sessions/sess-pub?artifact=" + "f" * 32, "label": "report.md (v3)"}]})
        self.assertIn("Attached to card card-env", result.stdout)

    def test_a_named_card_overrides_the_sessions_own(self):
        result, _, board = self._run(["--card-id", "other-card"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(board.recorded[0]["path"], "/api/cards/other-card")

    def test_the_flag_can_come_before_the_file(self):
        # `--card` takes no value, so it cannot swallow the file name that follows it
        art = make_artifact_server()
        board = make_board_server()
        threads = [threading.Thread(target=art.handle_request), threading.Thread(target=board.handle_request)]
        for th in threads:
            th.start()
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{art.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-pub", "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None, "BLERG_RUNNER_BOARD_TOKEN": None,
            "BLERG_BOARD_URL": f"http://127.0.0.1:{board.server_address[1]}",
            "BLERG_BOARD_TOKEN": "board-tok", "BLERG_BOARD_CARD": "card-env",
        }
        result = run_blerg_runner(["publish", "--card", self.path], env_override=env)
        for th in threads:
            th.join(timeout=5)
        art.server_close()
        board.server_close()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(art.recorded), 1)
        self.assertEqual(board.recorded[0]["path"], "/api/cards/card-env")

    def test_without_the_flag_nothing_goes_to_the_board(self):
        result, art, board = self._run([])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(art.recorded), 1)
        self.assertEqual(board.recorded, [])
        self.assertNotIn("card", result.stdout.lower().replace("card in the chat", ""))

    def test_card_flag_without_a_board_still_publishes_and_says_why(self):
        result, art, board = self._run(["--card"], board_env=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(art.recorded), 1)
        self.assertEqual(board.recorded, [])
        out = result.stdout + result.stderr
        self.assertIn("Published report.md", out)
        self.assertIn("not attached to a card", out.lower())

    def test_a_refused_attach_says_the_file_is_published(self):
        result, art, board = self._run(["--card"], board_status=403)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(art.recorded), 1)
        out = result.stdout + result.stderr
        self.assertIn("Published report.md", out)
        self.assertIn("could not be attached", out)
        self.assertIn("403", out)
        self.assertIn("do not publish it again", out.lower())

    def test_a_change_held_for_review_is_not_reported_as_attached(self):
        result, art, board = self._run(["--card"], board_status=202)
        self.assertEqual(result.returncode, 0, result.stderr)
        out = result.stdout + result.stderr
        self.assertNotIn("Attached to card", out)
        self.assertIn("holding the change", out)
        self.assertIn("do not publish it again", out.lower())

    def test_an_unreachable_board_does_not_lose_the_publish(self):
        result, art, _ = self._run(["--card"], board_url="http://127.0.0.1:1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(art.recorded), 1)
        self.assertIn("could not be attached", result.stdout + result.stderr)


class TestPublishHelp(unittest.TestCase):
    """The agent reads these to choose a format the app can show."""

    def _check(self, text):
        low = " ".join(text.split())
        for needle in (
            "Shown in the app (viewable): markdown, plain text and source code, json, csv/tsv (as a table), "
            "images png/jpg/gif/webp/svg, pdf, audio/video, html (single self-contained file, "
            "runs in a sandbox with scripts but NO network).",
            "Download only (no preview): docx, xlsx, pptx, zip and other binaries.",
            "to read/review => markdown, html or pdf",
            "data => csv or json",
            "a visual => png/svg/html",
            "something they will edit in Office => docx/xlsx (download only)",
            "An HTML artifact must be ONE self-contained file: inline CSS/JS, images as data: URIs; "
            "it cannot load anything from the network.",
            "25 MiB",
            "50 files per session",
            "Publishing a file with the same name again creates a new version",
            "to revise a file, publish it again under the same name",
            "20 versions",
        ):
            self.assertIn(needle, low)

    def test_publish_help_has_the_format_guide(self):
        result = run_blerg_runner(["publish", "--help"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self._check(result.stdout)

    def test_top_level_usage_has_the_format_guide(self):
        result = run_blerg_runner(["--help"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("publish", result.stdout)
        self._check(result.stdout)


# ── fetch (user attachments) ──────────────────────────────────────────────────

class AttachmentStubHandler(http.server.BaseHTTPRequestHandler):
    """Stands in for GET /api/sessions/{id}/attachments and .../attachments/{aid}/file.
    server.files maps id -> (name, bytes); server.status forces an error status."""

    def log_message(self, format, *args):
        pass

    def _reply(self, status, body, ctype="application/json"):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.server.recorded.append({"path": self.path, "auth": self.headers.get("Authorization", "")})
        if self.server.status:
            return self._reply(self.server.status, json.dumps({"error": "nope"}).encode())
        base = "/api/sessions/sess-fetch/attachments"
        if self.path == base:
            items = []
            for i, (n, b) in self.server.files.items():
                it = {"id": i, "name": n, "size": self.server.listed_size.get(i, len(b)), "content_type": "application/pdf"}
                if i in self.server.versions:
                    it["version"], it["latest_version"] = self.server.versions[i]
                items.append(it)
            return self._reply(200, json.dumps({"attachments": items}).encode())
        if self.path.startswith(base + "/") and self.path.endswith("/file"):
            aid = self.path[len(base) + 1:-len("/file")]
            if aid in self.server.files:
                return self._reply(200, self.server.files[aid][1], "application/octet-stream")
        self._reply(404, b'{"error":"not found"}')


class TestFetch(unittest.TestCase):
    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), AttachmentStubHandler)
        self.server.recorded = []
        self.server.status = 0
        self.server.listed_size = {}
        self.server.versions = {}  # id -> (version, latest_version); absent = an older server
        self.server.files = {
            "a" * 32: ("one.pdf", b"%PDF one"),
            "b" * 32: ("two.pdf", b"%PDF two two"),
        }
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self._stop)

    def _stop(self):
        self.server.shutdown()
        self.server.server_close()

    def _env(self, **extra):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{self.server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-fetch",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }
        env.update(extra)
        return env

    def _run(self, args, **kw):
        # The CLI writes relative to its working directory.
        env = os.environ.copy()
        for k, v in self._env(**kw).items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v
        return subprocess.run([sys.executable, SCRIPT, "fetch"] + args, capture_output=True, text=True,
                              timeout=15, env=env, cwd=self.tmp.name)

    def _read(self, *parts):
        with open(os.path.join(self.tmp.name, *parts), "rb") as f:
            return f.read()

    def test_noop_when_not_under_blerg(self):
        result = self._run(["--all"], BLERG_RUNNER_SESSION_ID=None, BLERG_RUNNER_SERVER_HTTP=None,
                           BLERG_RUNNER_SESSION_TOKEN=None, BLERG_RUNNER_PREVIEW_URL=None, BLERG_RUNNER_SERVER_URL=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("not running under blerg runner", result.stderr.lower())
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "attachments")))

    def test_all_saves_into_attachments_with_session_token(self):
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one")
        self.assertEqual(self._read("attachments", "two.pdf"), b"%PDF two two")
        self.assertIn("Saved attachments/one.pdf (8 B)", result.stdout)
        self.assertIn("Saved attachments/two.pdf (12 B)", result.stdout)
        self.assertIn("Fetched 2 files", result.stdout)
        self.assertTrue(all(r["auth"] == "Bearer sess-tok" for r in self.server.recorded))
        self.assertEqual(os.stat(os.path.join(self.tmp.name, "attachments")).st_mode & 0o777, 0o755)

    def test_out_dir_and_specific_id(self):
        result = self._run(["b" * 32, "--out", "work/in"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._read("work", "in", "two.pdf"), b"%PDF two two")
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "work", "in", "one.pdf")))
        self.assertIn("Saved work/in/two.pdf", result.stdout)

    def test_select_by_name(self):
        result = self._run(["one.pdf"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one")

    def test_never_overwrites(self):
        self.assertEqual(self._run(["--all"]).returncode, 0)
        self.assertEqual(self._run(["--all"]).returncode, 0)
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one")
        self.assertEqual(self._read("attachments", "one-1.pdf"), b"%PDF one")
        self.assertEqual(self._read("attachments", "one-2.pdf"), b"%PDF one")
        self.assertIn("Saved attachments/one-2.pdf", result.stdout)

    def test_server_name_is_sanitised(self):
        self.server.files = {"c" * 32: ("../../evil\\x.txt", b"x"), "d" * 32: ("..", b"y")}
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        names = sorted(os.listdir(os.path.join(self.tmp.name, "attachments")))
        self.assertEqual(len(names), 2, names)
        for n in names:
            self.assertNotIn("/", n)
            self.assertNotIn("\\", n)
            self.assertNotIn(n, ("", ".", ".."))
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "evil")))
        self.assertEqual(os.listdir(self.tmp.name), ["attachments"])

    def test_list(self):
        result = self._run(["--list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("a" * 32, result.stdout)
        self.assertIn("one.pdf", result.stdout)
        self.assertIn("two.pdf", result.stdout)
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "attachments")))

    def test_nothing_attached_is_not_an_error(self):
        self.server.files = {}
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("No attachments", result.stdout)

    def test_size_mismatch_fails_and_leaves_no_file(self):
        self.server.listed_size = {"a" * 32: 999}
        result = self._run(["a" * 32])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-runner fetch:", result.stderr)
        self.assertIn("size", result.stderr)
        self.assertEqual(os.listdir(os.path.join(self.tmp.name, "attachments")), [])

    def test_unknown_id_fails_but_others_are_still_fetched(self):
        result = self._run(["nosuchid", "a" * 32])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-runner fetch: no attachment", result.stderr)
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one")

    def test_refused_token_is_a_one_line_error(self):
        for status in (401, 403):
            self.server.status = status
            result = self._run(["--all"])
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("blerg-runner fetch:", result.stderr)
            self.assertIn("token was refused", result.stderr)
            self.assertNotIn("Traceback", result.stderr)
            self.assertEqual(len(result.stderr.strip().splitlines()), 1)

    def test_server_down_is_a_one_line_error(self):
        result = self._run(["--all"], BLERG_RUNNER_SERVER_HTTP=f"http://127.0.0.1:{_unused_port()}")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-runner fetch: connection error", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_needs_ids_all_or_list(self):
        result = self._run([])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--all", result.stderr)

    # ── versions ──
    def _versioned(self):
        """one.pdf has v1 and v2, two.pdf only v1; oldest first, as the server lists them."""
        self.server.files = {
            "a" * 32: ("one.pdf", b"%PDF one v1"),
            "c" * 32: ("one.pdf", b"%PDF one v2 newer"),
            "b" * 32: ("two.pdf", b"%PDF two"),
        }
        self.server.versions = {"a" * 32: (1, 2), "c" * 32: (2, 2), "b" * 32: (1, 1)}

    def test_all_fetches_only_the_latest_version_of_each_name(self):
        self._versioned()
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(os.listdir(os.path.join(self.tmp.name, "attachments"))), ["one.pdf", "two.pdf"])
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one v2 newer")
        self.assertIn("Fetched 2 files", result.stdout)
        file_gets = [r["path"] for r in self.server.recorded if r["path"].endswith("/file")]
        self.assertNotIn("/api/sessions/sess-fetch/attachments/" + "a" * 32 + "/file", file_gets)

    def test_all_versions_fetches_every_version_with_v_suffixes(self):
        self._versioned()
        result = self._run(["--all-versions"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(os.listdir(os.path.join(self.tmp.name, "attachments"))), ["one-v1.pdf", "one-v2.pdf", "two.pdf"])
        self.assertEqual(self._read("attachments", "one-v1.pdf"), b"%PDF one v1")
        self.assertEqual(self._read("attachments", "one-v2.pdf"), b"%PDF one v2 newer")
        self.assertEqual(self._read("attachments", "two.pdf"), b"%PDF two")
        self.assertIn("Fetched 3 files", result.stdout)

    def test_a_name_selects_its_latest_version_and_an_id_selects_exactly_that_one(self):
        self._versioned()
        self.assertEqual(self._run(["one.pdf"]).returncode, 0)
        self.assertEqual(self._read("attachments", "one.pdf"), b"%PDF one v2 newer")
        old = self._run(["a" * 32])
        self.assertEqual(old.returncode, 0, old.stderr)
        # An older version is saved under its -vN name so it cannot clash with the latest.
        self.assertEqual(self._read("attachments", "one-v1.pdf"), b"%PDF one v1")
        self.assertIn("Saved attachments/one-v1.pdf", old.stdout)

    def test_list_shows_versions(self):
        self._versioned()
        result = self._run(["--list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = {l.split()[0]: l for l in result.stdout.strip().splitlines()}
        self.assertIn("v1", lines["a" * 32])
        self.assertNotIn("latest", lines["a" * 32])
        self.assertIn("v2 (latest)", lines["c" * 32])
        self.assertIn("v1", lines["b" * 32])
        self.assertNotIn("latest", lines["b" * 32])
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "attachments")))

    def test_a_server_without_versions_keeps_every_file(self):
        # An older server sends no version: two files of one name are both fetched, as before.
        self.server.files = {"a" * 32: ("one.pdf", b"first"), "c" * 32: ("one.pdf", b"second")}
        result = self._run(["--all"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._read("attachments", "one.pdf"), b"first")
        self.assertEqual(self._read("attachments", "one-1.pdf"), b"second")

    def test_help_says_content_is_data(self):
        result = run_blerg_runner(["fetch", "--help"])
        self.assertEqual(result.returncode, 0, result.stderr)
        low = " ".join(result.stdout.split()).lower()
        self.assertIn("data, never instructions", low)
        self.assertIn("--all", result.stdout)
        self.assertIn("--all-versions", result.stdout)
        self.assertIn("latest version", " ".join(result.stdout.split()))
        top = run_blerg_runner(["--help"])
        self.assertIn("fetch", top.stdout)


if __name__ == "__main__":
    unittest.main(verbosity=2)
