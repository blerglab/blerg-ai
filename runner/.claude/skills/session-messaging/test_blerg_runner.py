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
            error_body = json.dumps(getattr(self.server, 'force_error_body', None)
                                    or {"error": "forced error"}).encode()
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
            error_body = json.dumps(getattr(self.server, 'force_error_body', None)
                                    or {"error": "forced error"}).encode()
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
    # Tests must never inherit the running agent's live token or token-file path.
    env = {k: v for k, v in os.environ.items() if not k.startswith("BLERG_")}
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
    with a scripted status/JSON (server.reply_status, server.reply_json). GET .../files answers
    the session's limits (server.limits; None = an older runner that reports none)."""

    def log_message(self, format, *args):
        pass

    def do_GET(self):
        self.server.gets.append(self.path)
        sizes = getattr(self.server, "published_sizes", [])
        out = {"files": [{"id": f"f{i}", "name": f"f{i}", "size": n} for i, n in enumerate(sizes)], "used": len(sizes), "limit": 50}
        if self.server.limits is not None:
            out["limits"] = self.server.limits
        resp = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({
            "path": self.path,
            "auth": self.headers.get("Authorization", ""),
            "name": self.headers.get("X-Artifact-Name", ""),
            "ctype": self.headers.get("Content-Type", ""),
            "length": self.headers.get("Content-Length", ""),
            "body": body,
        })
        resp = json.dumps(self.server.reply_json).encode()
        self.send_response(self.server.reply_status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


def make_artifact_server(view="text", status=201, reply=None, limits=None):
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ArtifactStubHandler)
    server.recorded = []
    server.gets = []
    server.limits = limits
    server.reply_status = status
    server.reply_json = reply if reply is not None else {
        "id": "a" * 32, "size": 0, "content_type": "text/plain", "view": view,
        "url": "/sessions/sess-pub?artifact=" + "a" * 32,
    }
    return server


def serve(server):
    """Serves server on a thread until the returned stop() is called (which also closes it)."""
    t = threading.Thread(target=server.serve_forever, daemon=True)
    t.start()

    def stop():
        server.shutdown()
        t.join(timeout=5)
        server.server_close()
    return stop


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
        stop = serve(server)
        try:
            result = run_blerg_runner(["publish"] + args, env_override=self._env(server.server_address[1]), **kw)
        finally:
            stop()
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
        # An older runner reports no limits: 25 MiB is assumed.
        p = self._file("big.bin", b"")
        with open(p, "r+b") as f:
            f.truncate(25 * 1024 * 1024 + 1)  # sparse
        result, server = self._publish([p])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("25 MiB", result.stderr)
        self.assertEqual(server.recorded, [])
        self.assertEqual(server.gets, ["/api/sessions/sess-pub/files"])

    def test_the_advertised_limit_is_the_one_checked(self):
        p = self._file("big.bin", b"")
        with open(p, "r+b") as f:
            f.truncate(30 * 1024 * 1024)  # sparse: over the old 25 MiB, under the advertised 100
        server = make_artifact_server(limits={"max_file_bytes": 100 * 1024 * 1024, "max_session_upload_bytes": 1})
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(server.recorded), 1)
        self.assertEqual(server.recorded[0]["length"], str(30 * 1024 * 1024))
        self.assertEqual(len(server.recorded[0]["body"]), 30 * 1024 * 1024)

        small = make_artifact_server(limits={"max_file_bytes": 1024})
        result, _ = self._publish([self._file("k.bin", b"x" * 2048)], server=small)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("k.bin is 2.0 KB; this runner takes at most 1.0 KB per file", result.stderr)
        self.assertEqual(small.recorded, [])

    def test_the_sessions_published_total_is_checked_before_sending(self):
        p = self._file("r.bin", b"x" * 3000)
        server = make_artifact_server(limits={"max_file_bytes": 10000, "max_session_artifact_bytes": 5000})
        server.published_sizes = [1500, 1000]
        result, _ = self._publish([p], server=server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("r.bin is 2.9 KB, and this session's published files would go over their 4.9 KB total (2.4 KB used); "
                      "delete one first", result.stderr)
        self.assertEqual(server.recorded, [])
        # Under the total it goes.
        server = make_artifact_server(limits={"max_file_bytes": 10000, "max_session_artifact_bytes": 5000})
        server.published_sizes = [1000]
        result, _ = self._publish([p], server=server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(server.recorded), 1)

    def test_a_directory_zip_follows_the_advertised_limit(self):
        import random
        root = os.path.join(self.tmp.name, "d")
        os.makedirs(root)
        with open(os.path.join(root, "noise.bin"), "wb") as f:
            f.write(random.Random(1).randbytes(4096))  # does not compress
        # The zip must fit the per-file limit...
        result, server = self._publish([root], server=make_artifact_server(limits={"max_file_bytes": 2048}))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("the zipped directory is larger than the runner's 2.0 KB per file", result.stderr)
        self.assertEqual(server.recorded, [])
        # ...and at most eight times that is read.
        result, server = self._publish([root], server=make_artifact_server(limits={"max_file_bytes": 256}))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("directory is too large to zip (over 2.0 KB of files)", result.stderr)

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
        stops = [serve(art), serve(board)]
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
        try:
            result = run_blerg_runner(["publish", self.path] + extra_args, env_override=env)
        finally:
            for stop in stops:
                stop()
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
        stops = [serve(art), serve(board)]
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{art.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-pub", "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None, "BLERG_RUNNER_BOARD_TOKEN": None,
            "BLERG_BOARD_URL": f"http://127.0.0.1:{board.server_address[1]}",
            "BLERG_BOARD_TOKEN": "board-tok", "BLERG_BOARD_CARD": "card-env",
        }
        try:
            result = run_blerg_runner(["publish", "--card", self.path], env_override=env)
        finally:
            for stop in stops:
                stop()
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
            "by default 100 MiB per file and 1 GiB in total per session",
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
        if self.path == "/api/sessions/sess-fetch/files":
            # What the session published: server.published maps id -> (name, size, version, latest).
            items = [{"id": i, "name": n, "size": s, "content_type": "application/pdf", "version": v, "latest_version": l}
                     for i, (n, s, v, l) in getattr(self.server, "published", {}).items()]
            used = len(items) + len(self.server.files)
            return self._reply(200, json.dumps({"files": items, "used": used, "limit": 50}).encode())
        self._reply(404, b'{"error":"not found"}')

    def do_DELETE(self):
        self.server.recorded.append({"path": self.path, "auth": self.headers.get("Authorization", ""), "method": "DELETE"})
        base = "/api/sessions/sess-fetch/files/"
        if self.path.startswith(base):
            aid = self.path[len(base):]
            published = getattr(self.server, "published", {})
            if aid in published:
                del published[aid]
                return self._reply(204, b"")
            if aid in self.server.files:  # a person's attachment
                return self._reply(403, b'{"error":"a person attached this file"}')
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




class TestFilesAndUnpublish(TestFetch):
    """`files` and `unpublish` share the fetch stub: server.published is what the session published."""

    def setUp(self):
        super().setUp()
        self.server.published = {
            "c" * 32: ("report.md", 2048, 1, 2),
            "d" * 32: ("report.md", 4096, 2, 2),
            "e" * 32: ("chart.png", 9000, 1, 1),
        }

    def _cli(self, cmd, args, **kw):
        env = os.environ.copy()
        for k, v in self._env(**kw).items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v
        return subprocess.run([sys.executable, SCRIPT, cmd] + args, capture_output=True, text=True,
                              timeout=15, env=env, cwd=self.tmp.name)

    def test_files_lists_the_sessions_files_and_the_slot_count(self):
        result = self._cli("files", [])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("report.md  v2 (latest)  (4.0 KB)", result.stdout)
        self.assertIn("report.md  v1  (2.0 KB)", result.stdout)
        self.assertIn("chart.png", result.stdout)
        # 3 published + the 2 attachments the stub lists.
        self.assertIn("5 of 50 file slots used", result.stdout)
        self.assertTrue(all(r["auth"] == "Bearer sess-tok" for r in self.server.recorded))

    def test_unpublish_by_name_removes_the_newest_version(self):
        result = self._cli("unpublish", ["report.md"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Removed report.md v2 (4.0 KB)", result.stdout)
        self.assertIn("4 of 50 file slots used", result.stdout)
        deletes = [r for r in self.server.recorded if r.get("method") == "DELETE"]
        self.assertEqual([r["path"].rsplit("/", 1)[1] for r in deletes], ["d" * 32])
        self.assertNotIn("d" * 32, self.server.published)
        self.assertIn("c" * 32, self.server.published)

    def test_unpublish_all_versions_and_by_id(self):
        result = self._cli("unpublish", ["report.md", "--all-versions"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("c" * 32, self.server.published)
        self.assertNotIn("d" * 32, self.server.published)
        result = self._cli("unpublish", ["e" * 32])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Removed chart.png", result.stdout)

    def test_unpublish_refuses_an_unknown_name_and_a_persons_attachment(self):
        result = self._cli("unpublish", ["nothing.txt"])
        self.assertEqual(result.returncode, 1)
        self.assertIn("no file 'nothing.txt' published by this session", result.stderr)
        # An attachment's id is not among the session's files: not found, nothing deleted.
        result = self._cli("unpublish", ["a" * 32])
        self.assertEqual(result.returncode, 1)
        self.assertIn("no file", result.stderr)
        self.assertEqual([r for r in self.server.recorded if r.get("method") == "DELETE"], [])

    def test_noop_when_not_under_blerg(self):
        result = self._cli("files", [], BLERG_RUNNER_SESSION_ID=None, BLERG_RUNNER_SERVER_HTTP=None,
                           BLERG_RUNNER_SESSION_TOKEN=None, BLERG_RUNNER_PREVIEW_URL=None, BLERG_RUNNER_SERVER_URL=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("not running under blerg runner", result.stderr.lower())


# ── review (a person's review of a published file) ───────────────────────────

class ReviewStubHandler(AttachmentStubHandler):
    """The fetch stub plus POST /api/sessions/{id}/artifacts, so `review reply` can publish."""

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({
            "method": "POST", "path": self.path, "auth": self.headers.get("Authorization", ""),
            "name": self.headers.get("X-Artifact-Name", ""), "body": body,
        })
        if self.path != "/api/sessions/sess-fetch/artifacts":
            return self._reply(404, b'{"error":"not found"}')
        if self.server.publish_status:
            return self._reply(self.server.publish_status, json.dumps({"error": self.server.publish_error}).encode())
        self.server.publish_count += 1
        resp = json.dumps({"id": "f" * 32, "name": self.headers.get("X-Artifact-Name", ""),
                           "version": self.server.publish_count, "previous": self.server.publish_count - 1 or None,
                           "view": "json", "url": "/sessions/sess-fetch?artifact=" + "f" * 32}).encode()
        self._reply(201, resp)


def _review_file(name, requests, version=3, edit=None):
    out = {"version": 1, "file": {"name": name, "artifactId": "a" * 32, "artifactVersion": version},
           "requests": requests, "submittedAt": "2026-10-06T10:00:00Z"}
    if edit:
        out["edit"] = edit
    return json.dumps(out).encode()


def _req(rid, text, quote=None, heading=None, page=None, status="open", reply=None, pin=None):
    anchor = {"kind": "region" if pin else "text"}
    if quote is not None:
        anchor["quote"] = quote
    if heading is not None:
        anchor["heading"] = heading
    if page is not None:
        anchor["page"] = page
    if pin is not None:
        anchor["pin"] = pin
    r = {"id": rid, "anchor": anchor, "text": text, "status": status, "createdAt": "2026-10-06T09:00:00Z"}
    if reply is not None:
        r["reply"] = reply
    return r


class TestReview(unittest.TestCase):
    """`review list` merges the person's review file with the session's replies and shows what is
    still open; `review reply` records an answer and publishes `<file>.review.json`."""

    _env = TestFetch._env
    _stop = TestFetch._stop

    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ReviewStubHandler)
        self.server.recorded = []
        self.server.status = 0
        self.server.listed_size = {}
        self.server.versions = {}
        self.server.publish_count = 0
        self.server.publish_status = 0
        self.server.publish_error = ""
        self.server.published = {}
        self.server.files = {
            "a" * 32: ("report.md", b"# Report\n\nedited by the person\n"),
            "b" * 32: ("report.md.review.json", _review_file("report.md", [
                _req("k7x2", "this is the median, not the mean", quote="the mean rose by 12%", heading="Results", page=2),
                _req("m3q9", "say how many weeks", quote="we sampled weekly", heading="Method"),
            ], edit={"artifactId": "a" * 32, "diff": "@@ -1 +1 @@\n-a\n+b\n"})),
        }
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self._stop)

    def _review(self, args, **kw):
        env = os.environ.copy()
        for k, v in self._env(**kw).items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v
        return subprocess.run([sys.executable, SCRIPT, "review"] + args, capture_output=True, text=True,
                              timeout=15, env=env, cwd=self.tmp.name)

    def _published(self):
        return [r for r in self.server.recorded if r.get("method") == "POST"]

    def _state(self, name="report.md"):
        with open(os.path.join(self.tmp.name, "attachments", ".reviews", name + ".review.json"), "rb") as f:
            return json.load(f)

    # list ──
    def test_list_shows_each_open_request_with_its_id_place_quote_and_ask(self):
        result = self._review(["list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(lines[0], "report.md (v3): 2 open requests")
        self.assertEqual(lines[1], '  [k7x2] under "Results", page 2 "the mean rose by 12%" — this is the median, not the mean')
        self.assertEqual(lines[2], '  [m3q9] under "Method" "we sampled weekly" — say how many weeks')
        self.assertEqual(len(lines), 3)
        self.assertTrue(all(r["auth"] == "Bearer sess-tok" for r in self.server.recorded))
        self.assertEqual(self._published(), [])

    def test_list_merges_the_sessions_replies_and_hides_answered_requests(self):
        # The session's copy (what it last published) says k7x2 is done; the person's file still has it open.
        os.makedirs(os.path.join(self.tmp.name, "attachments", ".reviews"))
        with open(os.path.join(self.tmp.name, "attachments", ".reviews", "report.md.review.json"), "wb") as f:
            f.write(_review_file("report.md", [
                _req("k7x2", "this is the median, not the mean", quote="the mean rose by 12%", status="done", reply="fixed"),
            ]))
        self.server.published = {"c" * 32: ("report.md.review.json", 300, 1, 1)}
        result = self._review(["list", "report.md"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("report.md (v3): 1 open request\n", result.stdout)
        self.assertIn("[m3q9]", result.stdout)
        self.assertNotIn("[k7x2]", result.stdout)

    def test_list_takes_the_newest_version_of_the_persons_review_file(self):
        self.server.files["c" * 32] = ("report.md.review.json", _review_file("report.md", [
            _req("z1z1", "newer ask", quote="x" * 80),
        ], version=4))
        self.server.versions = {"b" * 32: (1, 2), "c" * 32: (2, 2), "a" * 32: (1, 1)}
        result = self._review(["list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("report.md (v4): 1 open request", result.stdout)
        self.assertIn('[z1z1] (no place) "' + "x" * 59 + '…" — newer ask', result.stdout)
        self.assertNotIn("k7x2", result.stdout)

    def test_list_with_nothing_open_says_so_and_exits_zero(self):
        self.server.files["b" * 32] = ("report.md.review.json", _review_file("report.md", [
            _req("k7x2", "x", status="declined", reply="no"),
        ]))
        result = self._review(["list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "no open requests\n")
        result = self._review(["list", "other.md"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "no open requests: no review of other.md\n")

    def test_list_mentions_a_published_copy_this_folder_cannot_read(self):
        self.server.published = {"c" * 32: ("report.md.review.json", 300, 1, 1)}
        result = self._review(["list"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("2 open requests", result.stdout)
        self.assertIn("another folder", result.stderr)

    def test_a_pin_of_a_marked_up_image_is_shown_by_its_number(self):
        self.server.files["d" * 32] = ("shot.png.review.json", _review_file("shot.png", [
            _req("p1p1", "the title is clipped", pin=1),
        ], version=1))
        result = self._review(["list", "shot.png"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "shot.png (v1): 1 open request\n  [p1p1] pin 1 — the title is clipped\n")

    # reply ──
    def test_reply_records_status_and_line_and_publishes_the_merged_file_under_the_right_name(self):
        result = self._review(["reply", "k7x2", "done", "Changed mean to median in Results"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Replied to [k7x2] in report.md: done — Changed mean to median in Results", result.stdout)
        self.assertIn("Published report.md.review.json", result.stdout)
        self.assertIn("1 open request left in report.md", result.stdout)
        posts = self._published()
        self.assertEqual(len(posts), 1)
        self.assertEqual(posts[0]["path"], "/api/sessions/sess-fetch/artifacts")
        self.assertEqual(posts[0]["name"], "report.md.review.json")
        body = json.loads(posts[0]["body"])
        self.assertEqual(body["file"], {"name": "report.md", "artifactId": "a" * 32, "artifactVersion": 3})
        self.assertEqual(body["edit"]["artifactId"], "a" * 32)  # the person's fields are kept
        by_id = {r["id"]: r for r in body["requests"]}
        self.assertEqual(by_id["k7x2"]["status"], "done")
        self.assertEqual(by_id["k7x2"]["reply"], "Changed mean to median in Results")
        self.assertEqual(by_id["k7x2"]["text"], "this is the median, not the mean")
        self.assertEqual(by_id["m3q9"]["status"], "open")
        self.assertNotIn("reply", by_id["m3q9"])
        self.assertEqual(self._state(), body)

    def test_replies_accumulate_and_a_second_reply_to_an_id_overwrites(self):
        self.assertEqual(self._review(["reply", "k7x2", "done", "first"]).returncode, 0)
        self.assertEqual(self._review(["reply", "m3q9", "declined", "six weeks is in the appendix"]).returncode, 0)
        result = self._review(["reply", "k7x2", "declined", "on reflection it is the mean"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("No open requests left in report.md", result.stdout)
        posts = self._published()
        self.assertEqual([p["name"] for p in posts], ["report.md.review.json"] * 3)
        by_id = {r["id"]: r for r in json.loads(posts[-1]["body"])["requests"]}
        self.assertEqual((by_id["k7x2"]["status"], by_id["k7x2"]["reply"]), ("declined", "on reflection it is the mean"))
        self.assertEqual((by_id["m3q9"]["status"], by_id["m3q9"]["reply"]), ("declined", "six weeks is in the appendix"))
        listed = self._review(["list"])
        self.assertEqual(listed.stdout, "no open requests\n")

    def test_reply_to_an_unknown_id_fails_and_publishes_nothing(self):
        result = self._review(["reply", "nope", "done", "x"])
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner review: no request 'nope'", result.stderr)
        self.assertIn("review list", result.stderr)
        self.assertEqual(self._published(), [])
        self.assertFalse(os.path.exists(os.path.join(self.tmp.name, "attachments", ".reviews")))

    def test_reply_needs_a_known_status_and_a_non_empty_line(self):
        result = self._review(["reply", "k7x2", "maybe", "x"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("done", result.stderr)
        result = self._review(["reply", "k7x2", "done", "   "])
        self.assertEqual(result.returncode, 1)
        self.assertIn("non-empty line", result.stderr)
        self.assertEqual(self._published(), [])

    def test_a_refused_publish_is_one_clear_line(self):
        self.server.publish_status = 409
        self.server.publish_error = "This file already has 20 versions; delete an old one first."
        result = self._review(["reply", "k7x2", "done", "x"])
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner publish: This file already has 20 versions", result.stderr)
        self.assertEqual(len(result.stderr.strip().splitlines()), 1, result.stderr)

    # the rest ──
    def test_noop_when_not_under_blerg(self):
        result = self._review(["list"], BLERG_RUNNER_SESSION_ID=None, BLERG_RUNNER_SERVER_HTTP=None,
                              BLERG_RUNNER_SESSION_TOKEN=None, BLERG_RUNNER_PREVIEW_URL=None, BLERG_RUNNER_SERVER_URL=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("not running under blerg runner", result.stderr.lower())

    def test_help_explains_the_loop(self):
        for args in (["review", "--help"], ["--help"]):
            result = run_blerg_runner(args)
            self.assertEqual(result.returncode, 0, result.stderr)
            low = " ".join(result.stdout.split())
            for needle in ("apply it first", "author's own wording", "each request", "the file the review was published from",
                           "Publish the file again", "review reply <id> done|declined", "review list"):
                self.assertIn(needle, low, args)


# ── topic ─────────────────────────────────────────────────────────────────────

TOPIC_LINE = ('When you start a piece of work the user asked for that takes several steps, run '
              '`blerg-runner topic "<short imperative title>"`; when it is finished or dropped, '
              '`blerg-runner topic --done "<one-line outcome>"`, and when the work landed commits, pass them: '
              '`--commit <sha>`. One topic at a time; not for a quick question, '
              'and not from a subagent.')


class TopicStubHandler(http.server.BaseHTTPRequestHandler):
    """Stands in for POST /api/sessions/{id}/topics: records the body, answers as scripted."""

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({
            "path": self.path,
            "auth": self.headers.get("Authorization", ""),
            "body": json.loads(body) if body else {},
        })
        data = self.server.reply_body
        self.send_response(self.server.reply_status)
        self.send_header("Content-Type", self.server.reply_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def make_topic_server(status=201, reply=None, ctype="application/json", raw=None):
    server = http.server.HTTPServer(("127.0.0.1", 0), TopicStubHandler)
    server.recorded = []
    server.reply_status = status
    server.reply_type = ctype
    server.reply_body = raw if raw is not None else json.dumps(reply or {}).encode()
    return server


class TestTopic(unittest.TestCase):
    def _run(self, args, server):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-top",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }
        t = threading.Thread(target=server.handle_request)
        t.start()
        result = run_blerg_runner(["topic"] + args, env_override=env)
        if t.is_alive():
            # The CLI refused before sending anything: unblock the stub.
            try:
                socket.create_connection(server.server_address, timeout=2).close()
            except OSError:
                pass
        t.join(timeout=5)
        server.server_close()
        return result

    def test_start_posts_the_title_and_says_so(self):
        server = make_topic_server(reply={"id": "tp_1", "title": "Build the History tab", "action": "start", "closed": None})
        result = self._run(["Build the History tab"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "Topic started: Build the History tab")
        req = server.recorded[0]
        self.assertEqual(req["path"], "/api/sessions/sess-top/topics")
        self.assertEqual(req["auth"], "Bearer sess-tok")
        self.assertEqual(req["body"], {"action": "start", "title": "Build the History tab"})

    def test_a_start_over_an_open_topic_names_the_one_it_closed(self):
        server = make_topic_server(reply={"id": "tp_2", "title": "Build x", "action": "start",
                                          "closed": {"id": "tp_1", "title": "Fix y"}})
        result = self._run(["Build x"], server)
        self.assertEqual(result.stdout.strip(), "Topic started: Build x (closed: Fix y)")

    def test_done_with_an_outcome(self):
        server = make_topic_server(reply={"id": "tp_1", "title": "Build x", "action": "done", "outcome": "Shipped on main"})
        result = self._run(["--done", "Shipped on main"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["body"], {"action": "done", "outcome": "Shipped on main"})
        self.assertEqual(result.stdout.strip(), "Topic done: Build x (Shipped on main)")

    def test_done_without_an_outcome(self):
        server = make_topic_server(reply={"id": "tp_1", "title": "Build x", "action": "done"})
        result = self._run(["--done"], server)
        self.assertEqual(server.recorded[0]["body"], {"action": "done", "outcome": ""})
        self.assertEqual(result.stdout.strip(), "Topic done: Build x")

    def test_done_with_commits_sends_them_in_order(self):
        server = make_topic_server(reply={"id": "tp_1", "title": "Build x", "action": "done", "outcome": "Shipped",
                                          "commits": [{"sha": "1a2b3c4"}, {"sha": "9f8e7d6"}]})
        result = self._run(["--done", "Shipped", "--commit", "1a2b3c4", "--commit", "9f8e7d6"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["body"], {"action": "done", "outcome": "Shipped",
                                                      "commits": [{"sha": "1a2b3c4"}, {"sha": "9f8e7d6"}]})
        self.assertEqual(result.stdout.strip(), "Topic done: Build x (Shipped) · 2 commits")

    def test_commits_given_as_one_comma_separated_list(self):
        server = make_topic_server(reply={"id": "tp_1", "title": "Build x", "action": "done",
                                          "commits": [{"sha": "1a2b3c4"}]})
        sha40 = "0123456789abcdef0123456789abcdef01234567"
        result = self._run(["--done", "--commit", f"1a2b3c4, {sha40}", "--commit", "ABCDEF0"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["body"]["commits"], [{"sha": "1a2b3c4"}, {"sha": sha40}, {"sha": "ABCDEF0"}])
        self.assertEqual(server.recorded[0]["body"]["outcome"], "")
        self.assertEqual(result.stdout.strip(), "Topic done: Build x · 1 commit")

    def test_a_bad_sha_is_refused_before_sending(self):
        for bad in ("1a2b3c", "xyz1234", "1a2b3c4d" * 6, "1a2b3c4,g123456", " , "):
            server = make_topic_server()
            result = self._run(["--done", "--commit", bad], server)
            self.assertNotEqual(result.returncode, 0, bad)
            self.assertIn("7 to 40 hex", result.stderr, bad)
            self.assertEqual(server.recorded, [], bad)

    def test_more_than_fifty_commits_are_refused(self):
        server = make_topic_server()
        shas = ",".join(f"{i:07x}" for i in range(51))
        result = self._run(["--done", "--commit", shas], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("at most 50", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_commits_with_a_start_are_refused(self):
        server = make_topic_server()
        result = self._run(["Build x", "--commit", "1a2b3c4"], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--commit goes with --done", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_a_title_with_done_is_refused_before_sending(self):
        server = make_topic_server()
        result = self._run(["Build x", "--done"], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not both", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_no_title_and_no_done_is_refused(self):
        server = make_topic_server()
        result = self._run([], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("give a title", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_nothing_open_is_one_clear_line(self):
        server = make_topic_server(status=409, reply={"error": "no topic is open"})
        result = self._run(["--done"], server)
        self.assertEqual(result.returncode, 1)
        port = server.server_address[1]
        self.assertEqual(result.stderr.strip(),
                         f"blerg-runner topic: no topic is open (HTTP 409 from http://127.0.0.1:{port}/api/sessions/sess-top/topics)")

    def test_an_older_server_says_so(self):
        server = make_topic_server(status=200, ctype="text/html", raw=b"<!doctype html><title>app</title>")
        result = self._run(["Build x"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("does not know topics", result.stderr)

    def test_noop_when_not_under_blerg(self):
        env = {k: None for k in ("BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_SERVER_HTTP",
                                  "BLERG_RUNNER_SESSION_TOKEN", "BLERG_RUNNER_DAEMON_TOKEN", "BLERG_RUNNER_BOARD_TOKEN")}
        result = run_blerg_runner(["topic", "Build x"], env_override=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("not running under blerg runner", result.stderr.lower())

    def test_help_carries_the_instruction(self):
        for args in (["topic", "--help"], ["--help"]):
            result = run_blerg_runner(args)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(" ".join(TOPIC_LINE.split()), " ".join(result.stdout.split()), args)


class ProxyStubHandler(http.server.BaseHTTPRequestHandler):
    """Stands in for an HTTP proxy: records the absolute URL it was asked for and answers 201."""

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        self.rfile.read(length)
        self.server.seen.append(self.path)
        resp = json.dumps({"id": "m1"}).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)


class TestProxiesAndDiagnostics(unittest.TestCase):
    """Requests to a local server never go through a proxy; others honour the environment's. A failure
    says the status, the server's sentence and error id, the address (no user:password) and, for a
    connection error, which proxy variables are set by name only."""

    def _env(self, server_http, **extra):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": server_http,
            "BLERG_RUNNER_SESSION_ID": "sess-px",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok-0123456789",
            "BLERG_RUNNER_DAEMON_TOKEN": None, "BLERG_RUNNER_BOARD_TOKEN": None,
        }
        for k in ("HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"):
            env[k] = None
        env.update(extra)
        return env

    def test_a_local_server_is_reached_around_the_proxy(self):
        dead = f"http://user:secret@127.0.0.1:{_unused_port()}"
        for host in ("127.0.0.1", "localhost"):
            server = make_stub_server()
            stop = serve(server)
            try:
                result = run_blerg_runner(["update", "hi"], env_override=self._env(
                    f"http://{host}:{server.server_address[1]}", HTTP_PROXY=dead, http_proxy=dead, ALL_PROXY=dead))
            finally:
                stop()
            self.assertEqual(result.returncode, 0, f"{host}: {result.stderr}")
            self.assertEqual([r["path"] for r in server.recorded], ["/api/messages"], host)

    def test_another_host_goes_through_the_environments_proxy(self):
        proxy = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ProxyStubHandler)
        proxy.seen = []
        stop = serve(proxy)
        try:
            result = run_blerg_runner(["update", "hi"], env_override=self._env(
                "http://runner.example.test:8443", http_proxy=f"http://127.0.0.1:{proxy.server_address[1]}"))
        finally:
            stop()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(proxy.seen, ["http://runner.example.test:8443/api/messages"])

    def test_a_connection_error_names_the_proxy_variables_never_their_values(self):
        port = _unused_port()
        result = run_blerg_runner(["update", "hi"], env_override=self._env(
            f"http://127.0.0.1:{port}", HTTPS_PROXY="http://user:hunter2pw@proxy.example.test:3128", no_proxy="example.org"))
        self.assertEqual(result.returncode, 1)
        err = result.stderr
        self.assertIn(f"connection error to http://127.0.0.1:{port}/api/messages", err)
        self.assertIn("proxy variables set: HTTPS_PROXY, no_proxy", err)
        self.assertIn("127.0.0.1 is dialled directly", err)
        self.assertNotIn("hunter2pw", err)
        self.assertNotIn("proxy.example.test", err)
        self.assertEqual(len(err.strip().splitlines()), 1, err)

        result = run_blerg_runner(["update", "hi"], env_override=self._env(f"http://127.0.0.1:{port}"))
        self.assertIn("no proxy variables are set", result.stderr)

    def test_a_remote_host_says_whether_no_proxy_covers_it(self):
        dead = f"http://user:hunter2pw@127.0.0.1:{_unused_port()}"
        for no_proxy, want in (("", "runner.example.test is not covered by NO_PROXY"),
                               (".example.test", "runner.example.test is covered by NO_PROXY")):
            env = self._env("http://runner.example.test:9", http_proxy=dead)
            if no_proxy:
                env["NO_PROXY"] = no_proxy
            result = run_blerg_runner(["update", "hi"], env_override=env, timeout=20)
            self.assertEqual(result.returncode, 1)
            self.assertIn(want, result.stderr)
            self.assertNotIn("hunter2pw", result.stderr)

    def test_a_malformed_proxy_variable_is_named_never_printed(self):
        for bad in ("http:/user:s3cretpw@proxy.example.test:3128", "user:s3cretpw@"):
            result = run_blerg_runner(["update", "hi"], env_override=self._env(
                "http://runner.example.test:9", HTTP_PROXY=bad, http_proxy=bad), timeout=20)
            self.assertEqual(result.returncode, 1, bad)
            self.assertNotIn("Traceback", result.stderr)
            self.assertNotIn("s3cretpw", result.stderr)
            self.assertNotIn("proxy.example.test", result.stderr)
            if bad.startswith("http:/"):  # urllib's ValueError, which quotes the value
                self.assertIn("a proxy variable or the address is not a valid URL", result.stderr)
            self.assertIn("proxy variables set: HTTP_PROXY, http_proxy", result.stderr)

    def test_the_scrub_removes_userinfo_up_to_the_last_at(self):
        import importlib.machinery
        cli = importlib.machinery.SourceFileLoader("blerg_runner_cli", SCRIPT).load_module()
        self.assertEqual(cli._scrub("see http://user:p@ss@proxy.example.test:8080/x"), "see http://proxy.example.test:8080/x")
        self.assertEqual(cli._scrub("http://host.example.test/a@b and https://u@h.example.test"),
                         "http://host.example.test/a@b and https://h.example.test")
        self.assertEqual(cli._scrub("bad\x1b[31mred\x1b[0m"), "bad[31mred[0m")

    def test_the_servers_address_is_printed_without_its_userinfo(self):
        port = _unused_port()
        result = run_blerg_runner(["update", "hi"], env_override=self._env(f"http://me:pw123456@127.0.0.1:{port}"))
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("pw123456", result.stderr)
        self.assertIn(f"http://127.0.0.1:{port}/api/messages", result.stderr)

    def test_an_error_reply_gives_the_sentence_the_id_the_status_and_the_url(self):
        server = make_stub_server(force_error_code=500)
        server.force_error_body = {"error": "The runner could not record the message because its database did not answer.",
                                   "error_id": "ab12cd34"}
        stop = serve(server)
        try:
            result = run_blerg_runner(["update", "hi"], env_override=self._env(f"http://127.0.0.1:{server.server_address[1]}"))
        finally:
            stop()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stderr.strip(),
                         "blerg-runner: The runner could not record the message because its database did not answer. "
                         f"(HTTP 500 from http://127.0.0.1:{server.server_address[1]}/api/messages; error id ab12cd34)")

    def test_a_token_in_a_reply_is_never_printed(self):
        server = make_stub_server(force_error_code=401)
        server.force_error_body = {"error": "bad token sess-tok-0123456789"}
        stop = serve(server)
        try:
            result = run_blerg_runner(["update", "hi"], env_override=self._env(f"http://127.0.0.1:{server.server_address[1]}"))
        finally:
            stop()
        self.assertNotIn("sess-tok-0123456789", result.stderr)
        self.assertIn("bad token [token]", result.stderr)

    def test_an_older_servers_plain_text_is_shown_trimmed(self):
        server = make_topic_server(status=502, ctype="text/plain", raw=b"runner unreachable\n" + b"x" * 500)
        stop = serve(server)
        try:
            env = self._env(f"http://127.0.0.1:{server.server_address[1]}")
            result = run_blerg_runner(["topic", "Build x"], env_override=env)
        finally:
            stop()
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner topic: runner unreachable x", result.stderr)
        self.assertIn("… (HTTP 502 from", result.stderr)
        self.assertLess(len(result.stderr), 400)

    def test_publish_failure_carries_the_error_id(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "a.txt")
            with open(p, "wb") as f:
                f.write(b"hello")
            server = make_artifact_server(status=500, reply={
                "error": "The runner could not save the file because its disk is full; free some space.", "error_id": "0badf00d"})
            stop = serve(server)
            try:
                result = run_blerg_runner(["publish", p], env_override=self._env(f"http://127.0.0.1:{server.server_address[1]}"))
            finally:
                stop()
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner publish: The runner could not save the file because its disk is full", result.stderr)
        self.assertIn("HTTP 500 from http://127.0.0.1:", result.stderr)
        self.assertIn("/api/sessions/sess-px/artifacts; error id 0badf00d)", result.stderr)


SECRET_SESSION = "Zq7XkP9wLmR2vT4yHs"
SECRET_DAEMON = "Dm3nWq8ZrT5kYp2xJv"
SECRET_BASIC = "cHh1c2VyOnB4UGFzc1cwcmQ="  # base64 of a proxy's user:password


def _leaks(text, secrets):
    """The substrings of 4 characters or more of any secret that text contains."""
    found = set()
    for sec in secrets:
        for i in range(len(sec) - 3):
            if sec[i:i + 4] in text:
                found.add(sec[i:i + 4])
    return found


class EchoSecretsHandler(http.server.BaseHTTPRequestHandler):
    """Answers every request with server.status and a body echoing credentials: the session token, an
    Authorization Bearer header, a Proxy-Authorization Basic header and the daemon token, placed so
    that a 200-character cut falls inside a token. server.as_json wraps it as the runner's error JSON."""

    def log_message(self, format, *args):
        pass

    def _answer(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        if length:
            self.rfile.read(length)
        text = ("x" * 190 + f" Bearer {SECRET_SESSION}\r\nAuthorization: Bearer {SECRET_SESSION}\r\n"
                f"Proxy-Authorization: Basic {SECRET_BASIC}\r\nraw {SECRET_SESSION} daemon {SECRET_DAEMON}\r\n"
                f"token={SECRET_SESSION} basic {SECRET_BASIC}")
        if self.server.as_json:
            body = json.dumps({"error": text, "error_id": SECRET_SESSION}).encode()
            ctype = "application/json"
        else:
            body, ctype = text.encode(), "text/plain"
        self.send_response(self.server.status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_DELETE = do_PATCH = _answer


class TestNoSecretIsPrinted(unittest.TestCase):
    """Whatever a server, a proxy or the environment says, no credential reaches the output: not
    whole, not cut, not from a malformed proxy variable."""

    def _env(self, server_http, **extra):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": server_http,
            "BLERG_RUNNER_SESSION_ID": "sess-sec",
            "BLERG_RUNNER_SESSION_TOKEN": SECRET_SESSION,
            "BLERG_RUNNER_DAEMON_TOKEN": SECRET_DAEMON,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }
        for k in ("HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"):
            env[k] = None
        env.update(extra)
        return env

    def test_an_echoing_reply_leaks_nothing_on_any_failure_path(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            f = os.path.join(d, "a.txt")
            with open(f, "wb") as fh:
                fh.write(b"hello")
            commands = (["update", "hi"], ["note", "hi"], ["publish", f], ["fetch", "--all", "--out", d],
                        ["fetch", "--list"], ["files"], ["unpublish", "x"], ["topic", "Build x"], ["topic", "--done"],
                        ["review", "list"])
            for status in (401, 403, 500, 502):
                for as_json in (False, True):
                    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), EchoSecretsHandler)
                    server.status, server.as_json = status, as_json
                    stop = serve(server)
                    try:
                        for args in commands:
                            result = run_blerg_runner(args, env_override=self._env(f"http://127.0.0.1:{server.server_address[1]}"))
                            out = result.stdout + result.stderr
                            self.assertNotEqual(result.returncode, 0, (args, status, as_json))
                            self.assertNotIn("Traceback", out, (args, status))
                            self.assertEqual(_leaks(out, (SECRET_SESSION, SECRET_DAEMON, SECRET_BASIC)), set(),
                                             (args, status, as_json, out))
                    finally:
                        stop()

    def test_a_malformed_proxy_variable_leaks_nothing(self):
        pw = "PassW0rdX"
        shapes = ("http://pxq9z.example.test:3128@user:" + pw, "pxq9z.example.test:3128@user:" + pw,
                  "http://user@pxq9z.example.test:" + pw, f"http://user:{pw}@pxq9z.example.test:80:{pw}",
                  f"http:/u:{pw}@h", f"http://u:{pw}@[::1", f"http://u:Päss{pw}@höst:3128",
                  f"http://u:{pw}\n@h:3128", f"socks5://u:{pw}@h:1080", f"http://u:{pw}@h:99999", f"http://u:{pw}@:3128")
        for bad in shapes:
            for var in ("HTTP_PROXY", "ALL_PROXY"):
                result = run_blerg_runner(["update", "hi"], env_override=self._env(
                    "http://runner.example.test:9", **{var: bad}), timeout=20)
                out = result.stdout + result.stderr
                self.assertEqual(result.returncode, 1, (bad, out))
                self.assertNotIn("Traceback", out, bad)
                self.assertEqual(_leaks(out, (pw, "pxq9z")), set(), (bad, out))
                self.assertIn(f"proxy variables set: {var}", out, bad)


class TestSessionTokenFile(unittest.TestCase):
    """A token the server re-issued (the daemon writes it to the session's token file) is used
    before BLERG_RUNNER_SESSION_TOKEN, which may be a revoked one the session started with."""

    SID = "0f0e0d0c-0b0a-4908-8706-050403020100"

    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def _env(self, port, **extra):
        # A clean environment: nothing from the shell running the tests leaks in.
        env = {k: None for k in os.environ if k.startswith("BLERG_")}
        env.update({
            "HOME": self.tmp.name,
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
            "BLERG_RUNNER_SESSION_ID": self.SID,
        })
        env.update(extra)
        return env

    def _write(self, state_dir, token):
        d = os.path.join(state_dir, "session-tokens")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, self.SID), "w") as f:
            f.write(token + "\n")

    def _update(self, env, server):
        # A daemon thread: a CLI that sends nothing must fail the test, not hang it.
        t = threading.Thread(target=server.handle_request, daemon=True)
        t.start()
        result = run_blerg_runner(["update", "hi"], env_override=env)
        t.join(timeout=5)
        server.server_close()
        return result

    def test_file_in_state_dir_wins_over_env(self):
        server = make_stub_server()
        state = os.path.join(self.tmp.name, "state")
        self._write(state, "file-token")
        env = self._env(server.server_address[1], BLERG_RUNNER_DAEMON_STATE_DIR=state,
                        BLERG_RUNNER_SESSION_TOKEN="env-token")
        result = self._update(env, server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["auth"], "Bearer file-token")

    def test_default_state_dir_is_under_home(self):
        server = make_stub_server()
        self._write(os.path.join(self.tmp.name, ".blerg-runner-daemon"), "home-token")
        env = self._env(server.server_address[1], BLERG_RUNNER_SESSION_TOKEN="env-token")
        result = self._update(env, server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["auth"], "Bearer home-token")

    def test_file_alone_is_enough(self):
        server = make_stub_server()
        self._write(os.path.join(self.tmp.name, ".blerg-runner-daemon"), "home-token")
        result = self._update(self._env(server.server_address[1]), server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["auth"], "Bearer home-token")

    def test_env_token_without_a_file(self):
        server = make_stub_server()
        env = self._env(server.server_address[1], BLERG_RUNNER_SESSION_TOKEN="env-token")
        result = self._update(env, server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["auth"], "Bearer env-token")

    def test_revoked_token_is_named_without_the_token(self):
        server = make_stub_server(force_error_code=401)
        server.force_error_body = {"error": "session token revoked or expired", "code": "session_token_revoked"}
        env = self._env(server.server_address[1], BLERG_RUNNER_SESSION_TOKEN="env-secret-token")
        result = self._update(env, server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("token revoked or expired — restart the session or wait for a refreshed token", result.stderr)
        self.assertNotIn("env-secret-token", result.stderr + result.stdout)

    def test_plain_401_is_not_called_revoked(self):
        server = make_stub_server(force_error_code=401)
        env = self._env(server.server_address[1], BLERG_RUNNER_SESSION_TOKEN="env-token")
        result = self._update(env, server)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("revoked", result.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
