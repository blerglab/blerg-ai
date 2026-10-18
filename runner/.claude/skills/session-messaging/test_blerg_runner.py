#!/usr/bin/env python3
"""
Tests for the blerg-runner CLI.
Run with: python3 .claude/skills/session-messaging/test_blerg_runner.py
"""

import contextlib
import tempfile
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


def run_blerg_runner(args, env_override=None, timeout=10, cwd=None):
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
        cwd=cwd,
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
            "runs in a sandbox with scripts but NO network), a diff or patch (.diff/.patch, in a diff viewer; "
            "`blerg-runner diff` makes one from git).",
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


# ── checklist, pin, unpin ─────────────────────────────────────────────────────

WORK_LINE = ('When you work on a ticket, read it with the tools you have, then `blerg-runner pin` it '
             '(`blerg-runner pin --help` shows the shape); keep a checklist with `blerg-runner checklist` '
             'and tick items as you finish them.')


class TestWork(unittest.TestCase):
    def _run(self, args, server, requests=1, stdin=None):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-w",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
        }

        def serve():
            for _ in range(requests):
                server.handle_request()
        t = threading.Thread(target=serve)
        t.start()
        if stdin is None:
            result = run_blerg_runner(args, env_override=env)
        else:
            full = os.environ.copy()
            for k, v in env.items():
                if v is None:
                    full.pop(k, None)
                else:
                    full[k] = v
            result = subprocess.run([sys.executable, SCRIPT] + args, input=stdin, capture_output=True, text=True, env=full, timeout=30)
        while t.is_alive():
            try:
                socket.create_connection(server.server_address, timeout=2).close()
            except OSError:
                break
            t.join(timeout=1)
        server.server_close()
        return result

    LIST = {"items": [{"text": "Read", "done": True}, {"text": "Write tests", "done": False}], "op": "tick",
            "changed": {"index": 1, "text": "Read", "done": True}}

    def test_set_sends_the_items_and_prints_the_list(self):
        server = make_topic_server(reply=self.LIST)
        result = self._run(["checklist", "set", "Read", "Write tests"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        req = server.recorded[0]
        self.assertEqual(req["path"], "/api/sessions/sess-w/checklist")
        self.assertEqual(req["auth"], "Bearer sess-tok")
        self.assertEqual(req["body"], {"op": "set", "items": ["Read", "Write tests"]})
        self.assertEqual(result.stdout.splitlines(), ["Checklist · 1 of 2 done", "  ☑ 1. Read", "  ☐ 2. Write tests"])

    def test_tick_by_number_by_text_and_forced_text(self):
        for args, want in ((["2"], {"op": "tick", "index": 2}), (["Write tests"], {"op": "tick", "text": "Write tests"}),
                           (["--text", "2"], {"op": "tick", "text": "2"})):
            server = make_topic_server(reply=self.LIST)
            result = self._run(["checklist", "tick"] + args, server)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["body"], want, args)

    def test_add_untick_clear(self):
        for args, want in ((["add", "Docs"], {"op": "add", "item": "Docs"}), (["untick", "1"], {"op": "untick", "index": 1}),
                           (["remove", "2"], {"op": "remove", "index": 2}), (["remove", "Old step"], {"op": "remove", "text": "Old step"}),
                           (["remove", "--text", "2"], {"op": "remove", "text": "2"}), (["clear"], {"op": "clear"})):
            server = make_topic_server(reply={"items": [], "op": want["op"]})
            result = self._run(["checklist"] + args, server)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["body"], want)
        self.assertEqual(result.stdout.strip(), "Checklist: empty")

    def test_commits_ride_on_tick_and_add(self):
        for args, want in (
            (["tick", "2", "--commit", "abcdef1", "--commit", "1234567,fedcba9"],
             {"op": "tick", "index": 2, "commits": ["abcdef1", "1234567", "fedcba9"]}),
            (["add", "Docs", "--commit", "abcdef1"], {"op": "add", "item": "Docs", "commits": ["abcdef1"]}),
        ):
            server = make_topic_server(reply=self.LIST)
            result = self._run(["checklist"] + args, server)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["body"], want)

    def test_bad_commits_are_refused_before_sending(self):
        for args in (["tick", "1", "--commit", "xyz"], ["tick", "1", "--commit", ""], ["untick", "1", "--commit", "abcdef1"],
                     ["remove", "1", "--commit", "abcdef1"], ["remove"], ["remove", "1", "2"], ["set", "a", "--commit", "abcdef1"], ["clear", "--commit", "abcdef1"],
                     ["tick", "1", "--commit", ",".join(["abcdef%d" % i for i in range(10)] * 3)]):
            server = make_topic_server()
            result = self._run(["checklist"] + args, server)
            self.assertEqual(result.returncode, 1, args)
            self.assertEqual(server.recorded, [], args)
            self.assertIn("blerg-runner checklist:", result.stderr)

    def test_bad_checklist_calls_are_refused_before_sending(self):
        for args in (["set"], ["set", "  "], ["add"], ["tick"], ["tick", "1", "2"], ["clear", "x"], ["set"] + ["x"] * 51):
            server = make_topic_server()
            result = self._run(["checklist"] + args, server)
            self.assertEqual(result.returncode, 1, args)
            self.assertEqual(server.recorded, [], args)
            self.assertNotIn("Traceback", result.stderr)

    def test_a_refusal_is_one_line(self):
        server = make_topic_server(status=400, reply={"error": "there is no item 9; the checklist has 2"})
        result = self._run(["checklist", "tick", "9"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner checklist: there is no item 9; the checklist has 2 (HTTP 400", result.stderr)

    def test_an_older_server_says_so_without_a_traceback(self):
        cases = ((200, "text/html", b"<!doctype html><title>app</title>"), (404, "text/plain", b"404 page not found"),
                 (405, "text/plain", b"Method Not Allowed"), (200, "application/json", b"not json"))
        for status, ctype, raw in cases:
            for args in (["checklist", "clear"], ["unpin"]):
                server = make_topic_server(status=status, ctype=ctype, raw=raw)
                result = self._run(args, server)
                self.assertEqual(result.returncode, 1, (status, args))
                self.assertIn("this runner server is older than", result.stderr, (status, args))
                self.assertIn("nothing was recorded", result.stderr)
                self.assertNotIn("Traceback", result.stderr)

    def test_a_session_not_found_404_is_the_servers_sentence(self):
        server = make_topic_server(status=404, reply={"error": "session not found"})
        result = self._run(["checklist", "clear"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("session not found", result.stderr)

    TICKET = {"source": "jira", "key": "ABC-1", "title": "Crash on save", "status": "In Progress", "acceptance": ["Saves", "No crash"]}

    def test_pin_reads_a_file_and_says_what_is_pinned(self):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
            json.dump(self.TICKET, fh)
        try:
            server = make_topic_server(reply={"action": "update", "ticket": self.TICKET, "previous": {"status": "Open", "title": "Crash on save"},
                                              "pinned": [{"source": "jira", "key": "ABC-1"}]})
            result = self._run(["pin", fh.name], server)
        finally:
            os.unlink(fh.name)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["path"], "/api/sessions/sess-w/tickets")
        self.assertEqual(server.recorded[0]["body"], {"action": "pin", "ticket": self.TICKET})
        self.assertEqual(result.stdout.splitlines(), ["Updated jira:ABC-1 · Crash on save (status Open → In Progress)",
                                                      "Pinned now: jira:ABC-1 (the first is current)"])

    def test_pin_from_stdin_with_checklist_sets_the_acceptance(self):
        server = make_topic_server(reply={"action": "pin", "ticket": self.TICKET, "pinned": [], "items": [], "op": "set"})
        result = self._run(["pin", "-", "--checklist"], server, requests=2, stdin=json.dumps(self.TICKET))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([r["path"] for r in server.recorded], ["/api/sessions/sess-w/tickets", "/api/sessions/sess-w/checklist"])
        self.assertEqual(server.recorded[1]["body"], {"op": "set", "items": ["Saves", "No crash"]})

    def test_pin_refuses_what_is_not_a_json_object(self):
        for raw in ("not json", "[1, 2]", '"x"'):
            server = make_topic_server()
            result = self._run(["pin", "-"], server, stdin=raw)
            self.assertEqual(result.returncode, 1, raw)
            self.assertEqual(server.recorded, [], raw)
            self.assertNotIn("Traceback", result.stderr)
        server = make_topic_server()
        result = self._run(["pin", "/nonexistent/ticket.json"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("cannot read", result.stderr)

    def test_unpin_by_source_and_key_or_the_current(self):
        for args, want in (([], {"action": "unpin"}), (["jira:ABC-1"], {"action": "unpin", "source": "jira", "key": "ABC-1"}),
                           (["77"], {"action": "unpin", "key": "77"})):
            server = make_topic_server(reply={"action": "unpin", "ticket": {"source": "jira", "key": "ABC-1", "title": "T"}, "pinned": []})
            result = self._run(["unpin"] + args, server)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["body"], want)
        self.assertEqual(result.stdout.splitlines(), ["Unpinned jira:ABC-1 · T", "Nothing is pinned now."])

    def test_help_carries_the_line_and_the_mappings(self):
        for args in (["checklist", "--help"], ["pin", "--help"], ["--help"]):
            result = run_blerg_runner(args)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(" ".join(WORK_LINE.split()), " ".join(result.stdout.split()), args)
        out = run_blerg_runner(["pin", "--help"]).stdout
        for needle in ("A JIRA issue", "A Bugzilla bug", "show_bug.cgi", "fields.summary", "copies the ticket's text"):
            self.assertIn(needle, out)

    def test_noop_when_not_under_blerg(self):
        env = {k: None for k in ("BLERG_RUNNER_SESSION_ID", "BLERG_RUNNER_SERVER_HTTP",
                                  "BLERG_RUNNER_SESSION_TOKEN", "BLERG_RUNNER_DAEMON_TOKEN", "BLERG_RUNNER_BOARD_TOKEN")}
        for args in (["checklist", "clear"], ["unpin"]):
            result = run_blerg_runner(args, env_override=env)
            self.assertEqual(result.returncode, 0, result.stderr)


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


# ── diff ───────────────────────────────────────────────────────────────────────

GIT_ID = ["-c", "user.email=t@example.test", "-c", "user.name=Tester", "-c", "commit.gpgsign=false"]


class TestDiff(unittest.TestCase):
    """blerg-runner diff runs git in the working folder and publishes the patch as <slug>.diff."""

    def setUp(self):
        import tempfile
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = os.path.join(self.tmp.name, "repo")
        os.mkdir(self.repo)
        self.git("init", "-q")
        self.write("f.txt", "one\n")
        self.commit("first")

    def git(self, *args):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        env["GIT_CONFIG_NOSYSTEM"] = "1"
        env["HOME"] = self.tmp.name
        out = subprocess.run(["git", *GIT_ID, *args], cwd=self.repo, capture_output=True, env=env)
        if out.returncode != 0:
            raise AssertionError(f"git {args}: {out.stderr.decode()}")
        return out.stdout.decode().strip()

    def write(self, name, data):
        path = os.path.join(self.repo, name)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(data.encode() if isinstance(data, str) else data)

    def commit(self, msg):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", msg)
        return self.git("rev-parse", "HEAD")

    def run_diff(self, args, view="diff", limits=None, reply=None, cwd=None, cmd="diff", every=False):
        server = make_artifact_server(view=view, limits=limits, reply=reply or {
            "id": "d" * 32, "name": None, "view": view, "url": "/sessions/sess-diff?artifact=" + "d" * 32,
        })
        stop = serve(server)
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-diff",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
            "HOME": self.tmp.name,
            "GIT_CONFIG_NOSYSTEM": "1",
        }
        try:
            result = run_blerg_runner([cmd] + args, env_override=env, cwd=cwd or self.repo, timeout=30)
        finally:
            stop()
        if every:
            return result, server.recorded
        posted = server.recorded[0] if server.recorded else None
        return result, posted

    def test_a_range_is_published_as_a_diff_file(self):
        self.write("f.txt", "one\ntwo\n")
        self.commit("second")
        result, posted = self.run_diff(["HEAD~1..HEAD"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(posted["path"], "/api/sessions/sess-diff/artifacts")
        self.assertEqual(posted["name"], "head-1-head.diff")
        body = posted["body"].decode()
        self.assertIn("diff --git a/f.txt b/f.txt", body)
        self.assertIn("\n+two\n", body)
        self.assertIn("Published diff HEAD~1..HEAD · 1 file · +1 −0 — viewable in the app", result.stdout)
        self.assertIn("Link: /sessions/sess-diff?artifact=", result.stdout)

    def test_several_shas_are_their_patches_in_the_order_given(self):
        self.write("a.txt", "a\n")
        first = self.commit("add a")
        self.write("b.txt", "b\n")
        second = self.commit("add b")
        result, posted = self.run_diff([second[:9], first, "--title", "Two commits"])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = posted["body"].decode()
        self.assertEqual(posted["name"], "two-commits.diff")
        i, j = body.index(f"commit {second}"), body.index(f"commit {first}")
        self.assertLess(i, j)
        self.assertLess(body.index("diff --git a/b.txt"), j)
        self.assertGreater(body.index("diff --git a/a.txt"), j)
        self.assertIn("    add b", body)
        self.assertNotIn("t@example.test", body)  # no author email in the published patch
        self.assertIn("Published diff Two commits · 2 files · +2 −0", result.stdout)

    def test_a_merge_commit_is_its_diff_against_the_first_parent(self):
        base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-q", "-b", "side")
        self.write("side.txt", "s\n")
        self.commit("side")
        self.git("checkout", "-q", base)
        self.git("checkout", "-q", "-b", "main2")
        self.git("merge", "-q", "--no-ff", "-m", "merge side", "side")
        result, posted = self.run_diff(["HEAD"])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = posted["body"].decode()
        self.assertIn("diff --git a/side.txt b/side.txt", body)
        self.assertNotIn("diff --cc", body)

    def test_staged_is_the_index_only(self):
        self.write("f.txt", "one\nstaged\n")
        self.git("add", "f.txt")
        self.write("f.txt", "one\nstaged\nunstaged\n")
        result, posted = self.run_diff(["--staged"])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = posted["body"].decode()
        self.assertIn("+staged", body)
        self.assertNotIn("+unstaged", body)
        self.assertEqual(posted["name"], "staged-changes.diff")

    def test_worktree_is_every_uncommitted_change_with_new_files(self):
        self.write("f.txt", "one\nstaged\n")
        self.git("add", "f.txt")
        self.write("f.txt", "one\nstaged\nunstaged\n")
        self.write("dir/new file.txt", "fresh\n")
        self.write(".gitignore", "ignored.log\n")
        self.write("ignored.log", "noise\n")
        for args in (["--worktree"], []):
            result, posted = self.run_diff(args)
            self.assertEqual(result.returncode, 0, result.stderr)
            body = posted["body"].decode()
            self.assertIn("+staged", body)
            self.assertIn("+unstaged", body)
            self.assertIn("+fresh", body)
            self.assertIn("b/dir/new file.txt", body)
            self.assertNotIn("noise", body)
            self.assertEqual(posted["name"], "uncommitted-changes.diff")
            self.assertIn("3 files", result.stdout)

    def test_worktree_in_a_repository_with_no_commits(self):
        os.mkdir(os.path.join(self.tmp.name, "fresh"))
        subprocess.run(["git", "init", "-q"], cwd=os.path.join(self.tmp.name, "fresh"), check=True)
        with open(os.path.join(self.tmp.name, "fresh", "a.txt"), "w") as f:
            f.write("hello\n")
        result, posted = self.run_diff(["--worktree"], cwd=os.path.join(self.tmp.name, "fresh"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("+hello", posted["body"].decode())

    def test_runs_from_a_subfolder_and_paths_limit_the_diff(self):
        self.write("sub/x.txt", "x\n")
        self.write("-rf.txt", "dash\n")
        self.write("--output=pwned", "opt\n")
        self.commit("files")
        self.write("sub/x.txt", "x\ny\n")
        self.write("-rf.txt", "dash\nmore\n")
        self.write("--output=pwned", "opt\nmore\n")
        self.write("f.txt", "one\nother\n")
        result, posted = self.run_diff(["--worktree", "--", "-rf.txt", "--output=pwned"])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = posted["body"].decode()
        self.assertIn("a/-rf.txt", body)
        self.assertIn("a/--output=pwned", body)
        self.assertNotIn("f.txt b/f.txt", body)
        self.assertNotIn("x.txt", body)
        self.assertFalse(os.path.exists(os.path.join(self.repo, "pwned")))
        self.write("--output=fresh", "untracked\n")
        result, posted = self.run_diff(["--", "--output=fresh"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("+untracked", posted["body"].decode())
        self.assertFalse(os.path.exists(os.path.join(self.repo, "fresh")))
        result, posted = self.run_diff(["HEAD", "--", "x.txt"], cwd=os.path.join(self.repo, "sub"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("a/sub/x.txt", posted["body"].decode())

    def test_an_option_is_never_taken_for_a_revision(self):
        out = os.path.join(self.tmp.name, "written")
        for bad in ([f"--output={out}"], ["--", f"--output={out}"], ["-p"]):
            result, posted = self.run_diff(bad)
            self.assertNotEqual(result.returncode, 0, bad)
            self.assertIsNone(posted, bad)
        self.assertFalse(os.path.exists(out))
        result, posted = self.run_diff(["--end-of-options"])
        self.assertNotEqual(result.returncode, 0)
        result, posted = self.run_diff([f"HEAD..--output={out}"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(posted)
        self.assertFalse(os.path.exists(out))

    def test_an_unknown_revision_is_refused_in_words(self):
        result, posted = self.run_diff(["nosuchref"])
        self.assertEqual(result.returncode, 1)
        self.assertIsNone(posted)
        self.assertIn("not a commit", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_binary_files_are_reported_by_size_not_embedded(self):
        self.write("img.bin", b"\x00\x01\x02\x03\x04")
        self.commit("bin")
        self.write("img.bin", b"\x00\x01\x02\x03\x04\x05\xff")
        self.write("new.bin", b"\x00" * 10)
        result, posted = self.run_diff(["--worktree"])
        self.assertEqual(result.returncode, 0, result.stderr)
        body = posted["body"]
        self.assertNotIn(b"\x00", body)
        self.assertNotIn(b"GIT binary patch", body)
        self.assertIn(b"Binary files a/img.bin and b/img.bin differ (binary, 5 -> 7 bytes)", body)
        self.assertIn(b"Binary files /dev/null and b/new.bin differ (binary, 10 bytes)", body)
        result, posted = self.run_diff(["HEAD"])
        self.assertIn(b"Binary files /dev/null and b/img.bin differ (binary, 5 bytes)", posted["body"])

    def test_nothing_the_repository_names_is_run(self):
        marks = os.path.join(self.tmp.name, "marks")
        os.mkdir(marks)
        hooks = os.path.join(self.tmp.name, "hooks")
        os.mkdir(hooks)
        tool = os.path.join(self.tmp.name, "tool.sh")
        with open(tool, "w") as f:
            f.write(f'#!/bin/sh\ntouch "{marks}/$(basename "$0")-$$"\ncat\n')
        os.chmod(tool, 0o755)
        for hook in ("post-index-change", "pre-commit", "post-checkout", "reference-transaction", "fsmonitor-watchman"):
            os.symlink(tool, os.path.join(hooks, hook))
        self.write(".gitattributes", "*.txt filter=evil diff=evil\n")
        self.commit("attrs")
        for key, val in (("core.hooksPath", hooks), ("core.fsmonitor", tool),
                         ("filter.evil.clean", tool), ("filter.evil.smudge", tool), ("filter.evil.required", "true"),
                         ("diff.evil.textconv", tool), ("diff.evil.command", tool), ("diff.external", tool),
                         ("log.showSignature", "true"), ("gpg.program", tool), ("core.pager", tool),
                         ("pager.diff", tool), ("pager.show", tool), ("diff.noprefix", "true"), ("color.ui", "always"),
                         ("diff.relative", "true")):
            self.git("config", key, val)
        # A commit carrying a signature: showing it with log.showSignature would run gpg.program.
        raw = self.git("cat-file", "commit", "HEAD")
        head, _, msg = raw.partition("\n\n")
        signed = head + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\n" + msg + "\n"
        sig_path = os.path.join(self.tmp.name, "signed")
        with open(sig_path, "w") as f:
            f.write(signed)
        signed_sha = self.git("hash-object", "-t", "commit", "-w", sig_path)
        # The index learns about the fsmonitor (git status writes it); later reads would ask it.
        self.git("status")
        self.write("f.txt", "one\nchanged\n")
        for m in os.listdir(marks):
            os.unlink(os.path.join(marks, m))
        for args in (["--worktree"], ["HEAD"], ["HEAD~1..HEAD"], [signed_sha], ["--staged"]):
            if args == ["--staged"]:
                self.git("-c", "filter.evil.clean=cat", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
                         "add", "f.txt")
            result, posted = self.run_diff(args)
            self.assertEqual(result.returncode, 0, (args, result.stderr))
            self.assertEqual(os.listdir(marks), [], args)
            body = posted["body"].decode()
            self.assertNotIn("\x1b[", body)
            self.assertIn("diff --git a/", body)
        self.assertEqual(os.listdir(marks), [])

    def test_over_the_per_file_limit_is_refused_with_both_sizes(self):
        self.write("big.txt", "".join(f"line {i}\n" for i in range(2000)))
        result, posted = self.run_diff(["--worktree"], limits={"max_file_bytes": 4096})
        self.assertEqual(result.returncode, 1)
        self.assertIsNone(posted)
        self.assertIn("4.0 KB", result.stderr)
        self.assertIn("the diff is", result.stderr)

    def test_an_older_server_still_publishes_it_as_text(self):
        self.write("f.txt", "one\ntwo\n")
        result, posted = self.run_diff([], view="text")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNotNone(posted)
        self.assertIn("viewable in the app", result.stdout)

    def test_nothing_to_show_publishes_nothing(self):
        result, posted = self.run_diff([])
        self.assertEqual(result.returncode, 1)
        self.assertIsNone(posted)
        self.assertIn("no changes", result.stderr)

    def test_outside_a_repository_says_so(self):
        elsewhere = os.path.join(self.tmp.name, "plain")
        os.mkdir(elsewhere)
        result, posted = self.run_diff([], cwd=elsewhere)
        self.assertEqual(result.returncode, 1)
        self.assertIsNone(posted)
        self.assertIn("not inside a git repository", result.stderr)

    def test_staged_and_revisions_do_not_mix(self):
        result, posted = self.run_diff(["--staged", "HEAD"])
        self.assertNotEqual(result.returncode, 0)
        self.assertIsNone(posted)

    def test_checklist_commits_publish_their_diff_and_the_item_keeps_its_id(self):
        self.write("f.txt", "one\ntwo\n")
        sha = self.commit("second")
        result, posts = self.run_diff(["tick", "1", "--commit", sha[:8]], cmd="checklist", every=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([p["path"] for p in posts], ["/api/sessions/sess-diff/artifacts", "/api/sessions/sess-diff/checklist"])
        self.assertTrue(posts[0]["name"].endswith(".diff"), posts[0]["name"])
        self.assertIn(f"commit {sha}", posts[0]["body"].decode())
        body = json.loads(posts[1]["body"])
        self.assertEqual(body, {"op": "tick", "index": 1, "commits": [sha[:8]], "diff": "d" * 32})
        self.assertIn("Diff of the commits published", result.stdout)

    def test_checklist_commits_without_a_diff_keep_their_commits(self):
        self.write("f.txt", "".join(f"line {i}\n" for i in range(3000)))
        sha = self.commit("big")
        for args, limits in ((["add", "Docs", "--commit", sha], {"max_file_bytes": 2048}),
                             (["tick", "1", "--commit", "abcdef1"], None)):  # not a commit here
            result, posts = self.run_diff(args, limits=limits, cmd="checklist", every=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual([p["path"] for p in posts], ["/api/sessions/sess-diff/checklist"], args)
            body = json.loads(posts[0]["body"])
            self.assertNotIn("diff", body)
            self.assertIn("commits", body)
            self.assertIn("No diff of the commits", result.stdout)

    def test_help_has_the_guide(self):
        result = run_blerg_runner(["diff", "--help"])
        self.assertEqual(result.returncode, 0)
        self.assertIn("--staged", result.stdout)
        self.assertIn("--worktree", result.stdout)


class FilingStubHandler(TopicStubHandler):
    def do_GET(self):
        self.server.recorded.append({'path': self.path, 'method': 'GET', 'auth': self.headers.get('Authorization')})
        data = self.server.reply_body
        self.send_response(self.server.reply_status)
        self.send_header('Content-Type', self.server.reply_type)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class TestBoardFilingCLI(unittest.TestCase):
    def _run(self, args, reply, status=200, content_type='application/json'):
        server = http.server.HTTPServer(('127.0.0.1', 0), FilingStubHandler)
        server.recorded = []
        server.reply_body = json.dumps(reply).encode() if not isinstance(reply, bytes) else reply
        server.reply_status = status
        server.reply_type = content_type
        thread = threading.Thread(target=server.handle_request)
        thread.start()
        try:
            result = run_blerg_runner(['ticket'] + args, env_override={
                'BLERG_RUNNER_SERVER_HTTP': f'http://127.0.0.1:{server.server_port}',
                'BLERG_RUNNER_SESSION_ID': 'sess-filing', 'BLERG_RUNNER_SESSION_TOKEN': 'fixture-filing-token'})
        finally:
            if thread.is_alive():
                try:
                    socket.create_connection(server.server_address, timeout=2).close()
                except OSError:
                    pass
            thread.join(timeout=5)
            server.server_close()
        self.assertFalse(thread.is_alive())
        return result, server.recorded

    def test_file_queues_proposal_without_selected_board_or_direct_write(self):
        result, calls = self._run(['file', '--board', 'Another project', '--title', 'Fixture bug',
                                  '--body', 'Private reproduction', '--type', 'bug', '--sensitive',
                                  '--repo', 'target/one', '--repo', 'target/two'],
                                 {'proposal_id': 'proposal-fixture', 'state': 'pending'}, status=202)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]['path'], '/api/sessions/sess-filing/file-ticket')
        self.assertEqual(calls[0]['auth'], 'Bearer fixture-filing-token')
        self.assertEqual(calls[0]['body'], {'board': 'Another project', 'title': 'Fixture bug',
                                          'body': 'Private reproduction', 'type': 'bug', 'sensitive': True,
                                          'repos': ['target/one', 'target/two']})
        self.assertIn('awaiting approval', result.stdout)
        self.assertNotIn('Private reproduction', result.stdout + result.stderr)

    def test_accessible_board_list_is_session_scoped_read(self):
        result, calls = self._run(['boards'], {'configured': True, 'boards': [{'id': 'board-fixture', 'name': 'Other project'}]})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0]['method'], 'GET')
        self.assertEqual(calls[0]['path'], '/api/sessions/sess-filing/board-options')
        self.assertIn('Other project', result.stdout)
        self.assertIn('board-fixture', result.stdout)

    def test_search_encodes_target_and_query_and_warns_about_truncation(self):
        result, calls = self._run(['search', '--board', 'Other project', '--query', 'path/x ?'],
                                 {'cards': [{'id': 'card-fixture', 'number': 12, 'title': 'Duplicate'}], 'truncated': True})
        self.assertEqual(result.returncode, 0, result.stderr)
        query = parse_qs(urlparse(calls[0]['path']).query)
        self.assertEqual(urlparse(calls[0]['path']).path, '/api/sessions/sess-filing/board-search')
        self.assertEqual(query, {'board': ['Other project'], 'q': ['path/x ?']})
        self.assertIn('Duplicate', result.stdout)
        self.assertIn('truncated', result.stderr)

    def test_unconfigured_boards_and_older_server_are_clear(self):
        result, _ = self._run(['boards'], {'configured': False, 'boards': []})
        self.assertEqual(result.returncode, 0)
        self.assertIn('not configured', result.stdout)
        result, _ = self._run(['boards'], b'<html>Older app</html>', content_type='text/html')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('older', result.stderr)
        self.assertNotIn('Traceback', result.stderr)

    def test_errors_are_redacted_and_no_spoofed_origin_is_accepted(self):
        result, _ = self._run(['file', '--board', 'Other', '--title', 'Bug', '--body', 'Repro'],
                              {'error': 'Denied fixture-filing-token', 'error_id': 'fixture-error'}, status=403)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('fixture-filing-token', result.stderr)
        self.assertIn('403', result.stderr)
        result, calls = self._run(['file', '--board', 'Other', '--title', 'Bug', '--body', 'Repro', '--engine', 'forged'], {})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_established_title_bound_is_kept(self):
        result, _ = self._run(['file', '--board', 'Other', '--title', 'é' * 200, '--body', 'Repro'],
                              {'proposal_id': 'fixture', 'state': 'pending'}, status=202)
        self.assertEqual(result.returncode, 0, result.stderr)
        result, calls = self._run(['file', '--board', 'Other', '--title', 'x' * 201, '--body', 'Repro'], {})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_empty_fields_are_refused_before_network(self):
        for title, body in [('', 'Repro'), ('Bug', '')]:
            result, calls = self._run(['file', '--board', 'Other', '--title', title, '--body', body], {})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(calls, [])


class SessionMCPStub(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        self.server.requests.append((self.path, self.headers.get('Authorization'), body))
        if self.server.rotate_path and len(self.server.requests) == 1:
            with open(self.server.rotate_path, 'w') as f:
                f.write('fixture-refreshed')
        if self.server.redirect:
            self.send_response(302)
            self.send_header('Location', self.server.redirect)
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        response = {'jsonrpc': '2.0', 'id': body['id'], 'result': {'fixture': True}}
        if self.server.echo_text:
            response['result'] = {'content': [{'type': 'text', 'text': self.server.echo_text}]}
        raw = json.dumps(response).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


class TestSessionMCPShim(unittest.TestCase):
    def _run(self, messages, redirect=None, echo_text=None):
        import tempfile
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), SessionMCPStub)
        server.requests = []
        server.redirect = redirect
        server.echo_text = echo_text
        server.rotate_path = None
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        with tempfile.TemporaryDirectory() as home:
            state = os.path.join(home, '.blerg-runner-daemon', 'session-tokens')
            os.makedirs(state)
            server.rotate_path = os.path.join(state, 'shim-session')
            with open(server.rotate_path, 'w') as f:
                f.write('fixture-initial')
            env = {'HOME': home, 'PATH': '/usr/bin:/bin',
                   'BLERG_RUNNER_SERVER_HTTP': f'http://127.0.0.1:{server.server_port}',
                   'BLERG_RUNNER_SESSION_ID': 'shim-session', 'BLERG_RUNNER_SESSION_TOKEN': 'fixture-stale'}
            try:
                result = subprocess.run([sys.executable, SCRIPT, '_session_mcp'],
                                        input=''.join((m if isinstance(m, str) else json.dumps(m)) + '\n' for m in messages),
                                        text=True, capture_output=True, env=env, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, '')
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
        return [json.loads(line) for line in result.stdout.splitlines()], server.requests

    def test_stdio_requests_are_fixed_to_own_session_and_refresh_token(self):
        replies, calls = self._run([{'jsonrpc': '2.0', 'id': 1, 'method': 'initialize', 'params': {}},
                                   {'jsonrpc': '2.0', 'method': 'notifications/initialized'},
                                   {'jsonrpc': '2.0', 'id': 2, 'method': 'tools/list'},
                                   {'jsonrpc': '2.0', 'id': 3, 'method': 'tools/call', 'params': {'name': 'blerg_ticket_file', 'arguments': {'action': 'propose', 'board': 'Other', 'title': 'Fixture', 'body': 'Details'}}}])
        self.assertEqual([r['id'] for r in replies], [1, 2, 3])
        self.assertEqual([r[0] for r in calls], ['/api/sessions/shim-session/filing-tools'] * 3)
        self.assertEqual([r[1] for r in calls], ['Bearer fixture-initial', 'Bearer fixture-refreshed', 'Bearer fixture-refreshed'])
        self.assertEqual([r[2]['method'] for r in calls], ['initialize', 'tools/list', 'tools/call'])

    def test_plugin_request_uses_fixed_session_and_exact_approval_arguments(self):
        params = {'name': 'blerg_request_plugin', 'arguments': {'name': 'helper@example/plugins', 'reason': 'Need its review skill'}}
        replies, calls = self._run([{'jsonrpc': '2.0', 'id': 7, 'method': 'tools/call', 'params': params}])
        self.assertNotIn('error', replies[0])
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0], '/api/sessions/shim-session/filing-tools')
        self.assertEqual(calls[0][2]['params'], params)
        self.assertEqual(calls[0][1], 'Bearer fixture-initial')

    def test_redaction_preserves_json_framing(self):
        replies, _ = self._run([{'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'}],
                               echo_text='Authorization: Bearer fixture-initial\nFollow-up text')
        self.assertEqual(replies[0]['id'], 1)
        self.assertNotIn('fixture-initial', json.dumps(replies))
        self.assertIn('[redacted]', replies[0]['result']['content'][0]['text'])

    def test_invalid_and_unrelated_tools_never_reach_network(self):
        replies, calls = self._run(['not json', 'x' * (256 * 1024 + 1),
                                   {'jsonrpc': '2.0', 'id': 4, 'method': 'tools/call', 'params': {'name': 'Bash', 'arguments': {'command': 'false'}}},
                                   {'jsonrpc': '2.0', 'id': 5, 'method': 'arbitrary'}])
        self.assertEqual(len(replies), 4)
        self.assertTrue(all('error' in r for r in replies))
        self.assertEqual(calls, [])

    def test_stdio_redirect_does_not_forward_session_token(self):
        received = []
        class Target(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_GET(self):
                received.append(self.headers.get('Authorization'))
                self.send_response(200)
                self.end_headers()
            do_POST = do_GET
        target = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Target)
        thread = threading.Thread(target=target.serve_forever, daemon=True)
        thread.start()
        try:
            replies, _ = self._run([{'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'}],
                                    redirect=f'http://127.0.0.1:{target.server_port}/steal')
            self.assertIn('error', replies[0])
            self.assertEqual(received, [])
            self.assertNotIn('fixture-initial', json.dumps(replies))
        finally:
            target.shutdown()
            target.server_close()
            thread.join()


class RequestPluginTests(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.status = 202
        self.location = None
        owner = self
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass
            def do_POST(self):
                raw = self.rfile.read(int(self.headers.get('Content-Length', 0)))
                owner.requests.append((self.path, self.headers.get('Authorization'), json.loads(raw)))
                self.send_response(owner.status)
                if owner.location:
                    self.send_header('Location', owner.location)
                self.end_headers()
                self.wfile.write(json.dumps({'id':'proposal-fixture','state':'pending'}).encode())
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.env = {'BLERG_RUNNER_SERVER_HTTP': 'http://127.0.0.1:'+str(self.server.server_port),
                    'BLERG_RUNNER_SESSION_ID':'session-fixture','BLERG_RUNNER_SESSION_TOKEN':'fixture-token'}
    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
    def test_only_queues_exact_name_and_reason(self):
        reason = 'Review "this"\nwithout installing'
        result = run_blerg_runner(['request-plugin','helper@example/plugins',reason],self.env)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(self.requests,[('/api/sessions/session-fixture/plugin-requests','Bearer fixture-token',{'name':'helper@example/plugins','reason':reason})])
        self.assertIn('proposal-fixture',result.stdout)
        self.assertIn('approval',result.stdout)
    def test_bare_name_is_resolved_only_by_server(self):
        result=run_blerg_runner(['request-plugin','helper','Need its skill'],self.env)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(self.requests[0][2]['name'],'helper')
    def test_unconfigured_session_does_not_send_anything(self):
        result=run_blerg_runner(['request-plugin','helper','Need it'],{})
        self.assertEqual(result.returncode,0)
        self.assertEqual(self.requests,[])
    def test_invalid_names_and_oversized_reasons_never_reach_server(self):
        for name,reason in [('bad/command','why'),('helper@../repo','why'),('helper',''),('helper','é'*1001),('helper','why\x01')]:
            result=run_blerg_runner(['request-plugin',name,reason],self.env)
            self.assertNotEqual(result.returncode,0)
        self.assertEqual(self.requests,[])
    def test_refusal_never_claims_installation_or_echoes_token(self):
        self.status=403
        result=run_blerg_runner(['request-plugin','helper','why'],self.env)
        self.assertNotEqual(result.returncode,0)
        self.assertNotIn('fixture-token',result.stderr+result.stdout)
        self.assertNotIn('installed',result.stdout)
    def test_redirect_does_not_forward_session_authority(self):
        self.status=307
        self.location='http://127.0.0.1:'+str(self.server.server_port)+'/redirect-target'
        result=run_blerg_runner(['request-plugin','helper','why'],self.env)
        self.assertNotEqual(result.returncode,0)
        self.assertEqual(len(self.requests),1)


class TestProgress(unittest.TestCase):
    def _run(self, args, status=200, reply=None, extra_env=None):
        server = make_topic_server(status=status, reply=reply or {'ok': True})
        env = {'BLERG_RUNNER_SERVER_HTTP': f'http://127.0.0.1:{server.server_port}',
               'BLERG_RUNNER_SESSION_ID': 'sess-progress', 'BLERG_RUNNER_SESSION_TOKEN': 'fixture-progress-token'}
        env.update(extra_env or {})
        thread = threading.Thread(target=server.handle_request)
        thread.start()
        try:
            result = run_blerg_runner(['progress'] + args, env_override=env)
        finally:
            if thread.is_alive():
                try:
                    socket.create_connection(server.server_address, timeout=2).close()
                except OSError:
                    pass
            thread.join(timeout=5)
            server.server_close()
        self.assertFalse(thread.is_alive())
        return result, server.recorded

    def test_posts_bounded_counter_and_label(self):
        result, requests = self._run(['3/7', '  Running checks  '])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests, [{'path': '/api/sessions/sess-progress/progress',
                                    'auth': 'Bearer fixture-progress-token',
                                    'body': {'completed': 3, 'total': 7, 'label': 'Running checks'}}])
        self.assertIn('3/7', result.stdout)
        self.assertIn('Running checks', result.stdout)

    def test_counter_and_unicode_label_boundaries(self):
        for counter in ('0/1', '1000000/1000000'):
            with self.subTest(counter=counter):
                result, requests = self._run([counter, 'é' * 200])
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(requests[0]['body']['label'], 'é' * 200)

    def test_done_does_not_guess_previous_counter(self):
        result, requests = self._run(['--done'])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests[0]['body'], {'done': True})
        self.assertIn('done', result.stdout.lower())

    def test_refuses_invalid_counts_labels_and_done_combinations(self):
        cases = [['1/0', 'x'], ['2/1', 'x'], ['-1/3', 'x'], ['1.0/3', 'x'],
                 ['1e2/300', 'x'], ['１/３', 'x'], ['1/1000001', 'x'], ['1/2/3', 'x'],
                 ['1' * 5000 + '/2', 'x'], ['1/2'], ['1/2', ''], ['1/2', '  '],
                 ['1/2', 'x' * 201], ['1/2', 'one\ntwo'], ['1/2', '\x1b[31m'],
                 ['--done', '1/2', 'x'], ['--done', '1/2'], []]
        for args in cases:
            with self.subTest(args=args[:1]):
                result, requests = self._run(args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(requests, [])
                self.assertNotIn('Traceback', result.stderr)

    def test_refuses_caller_supplied_attribution(self):
        result, requests = self._run(['1/2', 'Checks', '--parent-call-id', 'another-agent'])
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(requests, [])

    def test_server_conflict_is_clear_and_redacted(self):
        result, _ = self._run(['--done'], status=409,
                              reply={'error': 'No progress for fixture-progress-token', 'error_id': 'fixture-error'})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('409', result.stderr)
        self.assertIn('No progress', result.stderr)
        self.assertNotIn('fixture-progress-token', result.stderr)

    def test_native_markers_use_call_id_first_and_never_guess(self):
        for metadata, expected in [({'BLERG_RUNNER_PROGRESS_CALL_ID': 'toolu_current', 'CODEX_THREAD_ID': 'native-root'}, {'call_id': 'toolu_current'}),
                                   ({'CODEX_THREAD_ID': 'native-child'}, {'native_thread_id': 'native-child'})]:
            result, requests = self._run(['1/2', 'Checks'], extra_env=metadata)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(requests[0]['body'], {'completed': 1, 'total': 2, 'label': 'Checks', **expected})
        result, requests = self._run(['--done'], extra_env={'BLERG_RUNNER_PROGRESS_CALL_ID': 'toolu_current'})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests[0]['body'], {'done': True, 'call_id': 'toolu_current'})

    def test_invalid_origin_is_refused_without_session_fallback(self):
        for key in ('BLERG_RUNNER_PROGRESS_CALL_ID', 'CODEX_THREAD_ID'):
            result, requests = self._run(['1/2', 'Checks'], extra_env={key: 'bad\nmarker'})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(requests, [])
            self.assertNotIn('bad', result.stderr)

    def test_claude_without_hook_refuses_root_and_stale_codex_fallback(self):
        for key in ('CLAUDECODE', 'CLAUDE_CODE_CHILD_SESSION', 'CLAUDE_CODE_SESSION_ID', 'CLAUDE_CODE_ENTRYPOINT'):
            result, requests = self._run(['1/2', 'Checks'], extra_env={key: 'fixture', 'CODEX_THREAD_ID': 'stale-codex-thread'})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(requests, [])
            self.assertIn('upgrade/restart', result.stderr)
            self.assertNotIn('stale-codex-thread', result.stderr)
        result, requests = self._run(['1/2', 'Checks'], extra_env={'CLAUDECODE': '1', 'BLERG_RUNNER_PROGRESS_CALL_ID': 'current-bash'})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(requests[0]['body']['call_id'], 'current-bash')

    def test_empty_marker_is_not_an_unscoped_request(self):
        for key in ('BLERG_RUNNER_PROGRESS_CALL_ID', 'CODEX_THREAD_ID'):
            result, requests = self._run(['1/2', 'Checks'], extra_env={key: ''})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(requests, [])

    def test_help_names_syntax_bounds_and_done(self):
        result = run_blerg_runner(['progress', '--help'])
        self.assertEqual(result.returncode, 0)
        self.assertIn('1000000', result.stdout)
        self.assertIn('200', result.stdout)
        self.assertIn('--done', result.stdout)


class TestProgressScopeHook(unittest.TestCase):
    def _hook(self, payload):
        raw = json.dumps(payload).encode() if not isinstance(payload, bytes) else payload
        result = subprocess.run([sys.executable, SCRIPT, '_progress_scope_hook'], input=raw,
                                capture_output=True, timeout=5,
                                env={'PATH': '/nonexistent', 'HOME': '/nonexistent'})
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        self.assertEqual(result.stderr, b'')
        return json.loads(result.stdout)

    def _input(self, command='printf ok', call_id='toolu_fixture'):
        return {'hook_event_name': 'PreToolUse', 'tool_name': 'Bash', 'tool_use_id': call_id,
                'tool_input': {'command': command, 'description': 'Fixture', 'timeout': 1234,
                               'run_in_background': True}}

    def test_wraps_only_command_preserving_fields_without_permission_decision(self):
        payload = self._input()
        result = self._hook(payload)
        self.assertEqual(set(result), {'hookSpecificOutput'})
        hook = result['hookSpecificOutput']
        self.assertEqual(set(hook), {'hookEventName', 'updatedInput'})
        self.assertEqual(hook['hookEventName'], 'PreToolUse')
        updated = hook['updatedInput']
        self.assertEqual(updated, {**payload['tool_input'], 'command':
                         'export BLERG_RUNNER_PROGRESS_CALL_ID=toolu_fixture\nprintf ok'})

    def test_bad_inputs_are_ignored_without_echoing_commands_or_ids(self):
        cases = [b'not json', b'{}' + b' ' * (256 * 1024), [], {},
                 self._input(call_id=''), self._input(call_id='x' * 257),
                 self._input(call_id='x\ny'), self._input(call_id='x\x00y'),
                 self._input(call_id='é' * 129), self._input(call_id='\ud800'),
                 {**self._input(), 'tool_name': 'Read'},
                 {**self._input(), 'hook_event_name': 'PostToolUse'},
                 {**self._input(), 'tool_input': []}, self._input(command=123)]
        for payload in cases:
            with self.subTest(payload_type=type(payload).__name__):
                self.assertEqual(self._hook(payload), {})

    def test_hook_never_executes_input_and_quotes_shell_metacharacters(self):
        with tempfile.TemporaryDirectory() as tmp:
            marker = os.path.join(tmp, 'must-not-exist')
            call_id = "fixture'; touch " + marker + "; echo '"
            command = "printf '%s' \"$BLERG_RUNNER_PROGRESS_CALL_ID\""
            updated = self._hook(self._input(command, call_id))['hookSpecificOutput']['updatedInput']['command']
            self.assertFalse(os.path.exists(marker))
            result = subprocess.run(['/bin/bash', '-c', updated], capture_output=True, text=True,
                                    env={'PATH': '/usr/bin:/bin'}, timeout=5)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, call_id)
            self.assertFalse(os.path.exists(marker))

    def test_preserves_multiline_shell_quoting_and_exit_status(self):
        command = "cat <<'END'\n$(literal)\nEND\nexit 7"
        updated = self._hook(self._input(command))['hookSpecificOutput']['updatedInput']['command']
        result = subprocess.run(['/bin/bash', '-c', updated], capture_output=True, text=True,
                                env={'PATH': '/usr/bin:/bin'}, timeout=5)
        self.assertEqual(result.returncode, 7)
        self.assertEqual(result.stdout, '$(literal)\n')

    def test_concurrent_calls_have_isolated_scope_and_override_inherited_marker(self):
        from concurrent.futures import ThreadPoolExecutor
        def run(call_id):
            command = 'printf %s "$BLERG_RUNNER_PROGRESS_CALL_ID"'
            updated = self._hook(self._input(command, call_id))['hookSpecificOutput']['updatedInput']['command']
            result = subprocess.run(['/bin/bash', '-c', updated], capture_output=True, text=True,
                                    env={'PATH': '/usr/bin:/bin', 'BLERG_RUNNER_PROGRESS_CALL_ID': 'stale'}, timeout=5)
            self.assertEqual(result.returncode, 0)
            return result.stdout
        with ThreadPoolExecutor(max_workers=2) as pool:
            self.assertEqual(list(pool.map(run, ['toolu_root', 'toolu_child'])), ['toolu_root', 'toolu_child'])


# ── session start/wait/... and budget ────────────────────────────────────────

class SessionStubHandler(http.server.BaseHTTPRequestHandler):
    """Plays self.server.script: a list of (method, path_prefix, status, body) answered in order.
    status None drops the connection without a reply. Every request is recorded."""

    def log_message(self, format, *args):
        pass

    def _serve(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(length) if length else b""
        srv = self.server
        srv.recorded.append({"method": self.command, "path": self.path, "headers": dict(self.headers.items()), "raw": raw})
        if not srv.script:
            status, body = 599, b'{"error":"script exhausted"}'
        else:
            method, prefix, status, body = srv.script[0]
            if method != self.command or prefix not in self.path:
                status, body = 598, json.dumps({"error": f"unexpected {self.command} {self.path}, wanted {method} {prefix}"}).encode()
            elif len(srv.script) > 1 or not srv.repeat_last:
                srv.script.pop(0)
        if status is None:
            self.close_connection = True
            self.connection.shutdown(socket.SHUT_RDWR)
            return
        if not isinstance(body, (bytes, bytearray)):
            body = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        if status == 429:
            self.send_header("Retry-After", "60")
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PUT = do_DELETE = _serve


CHILD = "11111111-1111-4111-8111-111111111111"
REQ = "22222222-2222-4222-8222-222222222222"
STARTED = {"child": {"id": CHILD, "engine": "codex", "model": "gpt-5", "effort": "", "runtime": "docker",
                     "repo": "", "no_repo": True, "title": "t", "keep_running": False},
           "via": "rule:r1", "plugins": "skipped",
           "budget": {"engine": "codex", "available": True, "windows": [{"id": "5h", "used_percent": 12,
                                                                            "resets_at": "2030-01-01T16:10:00Z"}]}}
PENDING = {"request_id": REQ, "status": "pending", "choice": "", "child_session_id": "", "start_error": "",
           "expires_at": "2030-01-01T12:10:00Z", "offered": ["once", "deny"]}


class TestSession(unittest.TestCase):
    def _run(self, args, script, timeout=15, repeat_last=False, extra_env=None, cmd=("session",)):
        server = http.server.HTTPServer(("127.0.0.1", 0), SessionStubHandler)
        server.recorded, server.script, server.repeat_last = [], list(script), repeat_last
        t = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
        t.start()
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-par",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
            "TZ": "UTC",
        }
        env.update(extra_env or {})
        try:
            result = run_blerg_runner(list(cmd) + args, env_override=env, timeout=timeout)
        finally:
            server.shutdown()
            server.server_close()
        return result, server.recorded

    def _body(self, rec):
        return json.loads(rec["raw"]) if rec["raw"] else {}

    def test_start_with_a_rule_prints_the_child(self):
        result, rec = self._run(["start", "--engine", "codex", "--no-repo", "do it"],
                                [("POST", "/api/sessions/sess-par/children", 201, STARTED)])
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(lines[0], f"started {CHILD} (codex gpt-5, docker, via rule:r1)")
        self.assertEqual(lines[1], "plugins: skipped")
        self.assertEqual(lines[2], "codex  5h 12% used, resets 4:10 PM")
        self.assertEqual(rec[0]["headers"]["Authorization"], "Bearer sess-tok")

    def test_start_sends_an_idempotency_key_and_the_body(self):
        result, rec = self._run(
            ["start", "--engine", "codex", "--model", "gpt-5", "--effort", "high", "--repo", "app",
             "--runtime", "docker", "--title", "T", "--keep-running", "the prompt"],
            [("POST", "/children", 201, STARTED)], extra_env={})
        self.assertEqual(result.returncode, 0, result.stderr)
        key = rec[0]["headers"]["Idempotency-Key"]
        self.assertRegex(key, r"^[0-9a-f]{32}$")
        self.assertIn(f"idempotency key: {key}", result.stderr)
        self.assertEqual(self._body(rec[0]), {"engine": "codex", "model": "gpt-5", "effort": "high", "repo": "app",
                                              "runtime": "docker", "title": "T", "keep_running": True,
                                              "prompt": "the prompt"})
        # an explicit key is used as given; no_repo is the other half of the pair
        result, rec = self._run(["start", "--no-repo", "--idempotency-key", "k-1", "p"], [("POST", "/children", 201, STARTED)])
        self.assertEqual(rec[0]["headers"]["Idempotency-Key"], "k-1")
        self.assertEqual(self._body(rec[0]), {"prompt": "p", "no_repo": True})

    def test_start_needs_a_repo_choice_and_a_prompt(self):
        result, rec = self._run(["start", "prompt"], [])
        self.assertEqual(result.returncode, 2)
        self.assertEqual(rec, [])
        result, rec = self._run(["start", "--no-repo"], [])
        self.assertEqual(result.returncode, 2)

    def test_start_retries_a_network_error_with_the_same_key(self):
        result, rec = self._run(["start", "--no-repo", "p"],
                                [("POST", "/children", None, b""), ("POST", "/children", None, b""),
                                 ("POST", "/children", 201, STARTED)])
        self.assertEqual(result.returncode, 0, result.stderr)
        rec = [r for r in rec if r["raw"]]  # a request with a body is a start (a dropped connection can be recorded twice)
        self.assertGreaterEqual(len(rec), 3)
        keys = {r["headers"]["Idempotency-Key"] for r in rec}
        self.assertEqual(len(keys), 1)
        self.assertTrue(all(self._body(r) == {"prompt": "p", "no_repo": True} for r in rec))

    def test_start_gives_up_after_three_retries_and_names_the_key(self):
        result, rec = self._run(["start", "--no-repo", "--idempotency-key", "kk", "p"],
                                [("POST", "/children", None, b"")], repeat_last=True)
        self.assertEqual(result.returncode, 1)
        self.assertGreaterEqual(len([r for r in rec if r["raw"]]), 4)
        self.assertIn("--idempotency-key kk", result.stderr)
        self.assertNotIn("sess-tok", result.stdout + result.stderr)

    def test_start_waits_then_prints_the_allowed_child(self):
        script = [("POST", "/children", 202, PENDING),
                  ("GET", f"/api/delegation-requests/{REQ}?wait=", 200, PENDING),
                  ("GET", f"/api/delegation-requests/{REQ}?wait=", 200,
                   dict(PENDING, status="allowed", choice="once", child_session_id=CHILD))]
        result, rec = self._run(["start", "--no-repo", "p"], script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [f"request {REQ}", f"started {CHILD}"])
        self.assertIn(f"waiting for the person to approve (request {REQ}, expires 12:10)", result.stderr)
        self.assertTrue(rec[1]["path"].endswith("?wait=25"), rec[1]["path"])

    def test_start_denied_exits_2(self):
        script = [("POST", "/children", 202, PENDING),
                  ("GET", "/api/delegation-requests/", 200, dict(PENDING, status="denied", choice="deny"))]
        result, _ = self._run(["start", "--no-repo", "p"], script)
        self.assertEqual(result.returncode, 2)
        self.assertIn("the person denied it", result.stderr)

    def test_allowed_with_a_start_error_exits_1(self):
        script = [("POST", "/children", 202, PENDING),
                  ("GET", "/api/delegation-requests/", 200, dict(PENDING, status="allowed", start_error="daemon offline"))]
        result, _ = self._run(["start", "--no-repo", "p"], script)
        self.assertEqual(result.returncode, 1)
        self.assertIn("daemon offline", result.stderr)

    def test_expired_exits_3(self):
        script = [("POST", "/children", 202, PENDING),
                  ("GET", "/api/delegation-requests/", 200, dict(PENDING, status="expired"))]
        result, _ = self._run(["start", "--no-repo", "p"], script)
        self.assertEqual(result.returncode, 3)
        self.assertIn("nobody answered; ask again with a new start", result.stderr)

    def test_start_timeout_says_to_wait_and_exits_3(self):
        # --timeout 1: pending forever. The request is NOT expired: `session wait` resumes it.
        script = [("POST", "/children", 202, PENDING), ("GET", "/api/delegation-requests/", 200, PENDING)]
        t0 = time.monotonic()
        result, rec = self._run(["start", "--no-repo", "--timeout", "1", "p"], script, repeat_last=True)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn(f"still waiting — run: blerg-runner session wait {REQ}", result.stdout)
        self.assertFalse([r for r in rec if r["path"].endswith("/expire")])
        self.assertLess(time.monotonic() - t0, 10)

    def test_default_timeout_is_100_and_the_cap_is_540(self):
        result, _ = run_blerg_runner(["session", "start", "--help"]), None
        self.assertIn("default 100, at most 540", " ".join(result.stdout.split()))
        # a larger value is clamped, not refused
        result, rec = self._run(["wait", REQ, "--timeout", "9999"],
                                [("GET", "/api/delegation-requests/", 200, dict(PENDING, status="denied"))])
        self.assertEqual(result.returncode, 2)
        self.assertTrue(rec[0]["path"].endswith("?wait=25"))

    def test_no_wait_prints_the_request(self):
        result, rec = self._run(["start", "--no-repo", "--no-wait", "p"], [("POST", "/children", 202, PENDING)])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"request {REQ}")
        self.assertEqual(len(rec), 1)

    def test_a_pending_request_is_printed_before_blocking(self):
        # A killed call still shows the id: stdout has it while the wait is still running.
        server = http.server.HTTPServer(("127.0.0.1", 0), SessionStubHandler)
        server.recorded, server.repeat_last = [], True
        server.script = [("POST", "/children", 202, PENDING), ("GET", "/api/delegation-requests/", 200, PENDING)]
        threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()
        env = {k: v for k, v in os.environ.items() if not k.startswith("BLERG_")}
        env.update({"BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                    "BLERG_RUNNER_SESSION_ID": "sess-par", "BLERG_RUNNER_SESSION_TOKEN": "sess-tok"})
        proc = subprocess.Popen([sys.executable, SCRIPT, "session", "start", "--no-repo", "p"], env=env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertEqual(proc.stdout.readline().strip(), f"request {REQ}")
            proc.terminate()
            proc.wait(timeout=10)
        finally:
            proc.kill()
            proc.stdout.close()
            proc.stderr.close()
            server.shutdown()
            server.server_close()

    def test_wait_resumes_an_existing_request(self):
        script = [("GET", f"/api/delegation-requests/{REQ}?wait=", 200, PENDING),
                  ("GET", f"/api/delegation-requests/{REQ}?wait=", 200,
                   dict(PENDING, status="allowed", child_session_id=CHILD))]
        result, rec = self._run(["wait", REQ], script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"started {CHILD}")
        self.assertEqual(len(rec), 2)

    def test_wait_keeps_going_through_a_server_hiccup(self):
        script = [("GET", "/api/delegation-requests/", 503, {"error": "busy"}),
                  ("GET", "/api/delegation-requests/", 200, dict(PENDING, status="allowed", child_session_id=CHILD))]
        result, _ = self._run(["wait", REQ], script)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_wait_on_an_unknown_request_fails(self):
        result, _ = self._run(["wait", REQ], [("GET", "/api/delegation-requests/", 404, {"error": "not found"})])
        self.assertEqual(result.returncode, 1)
        self.assertIn("not found", result.stderr)

    def test_prompt_file(self):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write("line one\nline two\n")
        try:
            result, rec = self._run(["start", "--no-repo", "--prompt-file", fh.name], [("POST", "/children", 201, STARTED)])
        finally:
            os.unlink(fh.name)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([self._body(r).get("prompt") for r in rec if r["raw"]][0], "line one\nline two\n")

    def test_a_refusal_prints_the_servers_sentence(self):
        result, _ = self._run(["start", "--no-repo", "p"], [("POST", "/children", 429, {"error": "too many running children"})])
        self.assertEqual(result.returncode, 1)
        self.assertIn("too many running children", result.stderr)
        self.assertIn("retry after 60s", result.stderr)

    def test_list_send_model_interrupt_stop_result(self):
        kid = {"id": CHILD, "engine": "codex", "model": "gpt-5", "status": "running", "title": "Fix it"}
        script = [("GET", "/api/sessions/sess-par/children", 200, {"children": [kid]}),
                  ("POST", f"/api/sessions/sess-par/children/{CHILD}/message", 202, {"status": "queued"}),
                  ("POST", f"/api/sessions/sess-par/children/{CHILD}/model", 202, {}),
                  ("POST", f"/api/sessions/sess-par/children/{CHILD}/interrupt", 202, {}),
                  ("POST", f"/api/sessions/sess-par/children/{CHILD}/stop", 200, {"status": "stopped"}),
                  ("GET", f"/api/sessions/sess-par/children/{CHILD}/result", 200, {"lifecycle": "running", "text": "hi"})]
        out = []
        for argv in (["list"], ["send", CHILD, "hello"], ["model", CHILD, "gpt-6", "--effort", "low"],
                     ["interrupt", CHILD], ["stop", CHILD], ["result", CHILD]):
            res, rec = self._run(argv, [script.pop(0)])
            self.assertEqual(res.returncode, 0, (argv, res.stderr))
            out.append((res.stdout, rec[0]))
        self.assertEqual(out[0][0].strip(), f"{CHILD}  codex  gpt-5  running  Fix it")
        self.assertEqual(out[1][0].strip(), "sent")
        self.assertEqual(self._body(out[1][1]), {"text": "hello"})
        self.assertEqual(self._body(out[2][1]), {"model": "gpt-6", "effort": "low"})
        self.assertEqual(out[3][0].strip(), "interrupted")
        self.assertEqual(out[4][0].strip(), "stopped")
        self.assertEqual(json.loads(out[5][0]), {"lifecycle": "running", "text": "hi"})

    def test_send_reports_an_answered_question(self):
        res, _ = self._run(["send", CHILD, "yes"], [("POST", "/children/", 202, {"answered_ask": True})])
        self.assertEqual(res.stdout.strip(), "answered its question")

    def test_another_sessions_child_is_a_plain_failure(self):
        res, _ = self._run(["stop", CHILD], [("POST", "/children/", 404, {"error": "not found"})])
        self.assertEqual(res.returncode, 1)
        self.assertIn("not found", res.stderr)

    def test_upload_and_download(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            src = os.path.join(tmp, "notes.txt")
            with open(src, "wb") as fh:
                fh.write(b"hello child")
            res, rec = self._run(["upload", CHILD, src],
                                 [("POST", f"/api/sessions/sess-par/children/{CHILD}/uploads", 201, {"id": "a1", "name": "notes.txt"})])
            self.assertEqual(res.returncode, 0, res.stderr)
            self.assertEqual(rec[0]["raw"], b"hello child")
            self.assertEqual(rec[0]["headers"]["X-Artifact-Name"], "notes.txt")
            self.assertEqual(rec[0]["headers"]["Content-Type"], "application/octet-stream")
            self.assertIn("uploaded notes.txt", res.stdout)
            listing = {"artifacts": [{"id": "old", "name": "out.txt", "version": 1, "size": 3},
                                     {"id": "new", "name": "out.txt", "version": 2, "size": 5}]}
            res, rec = self._run(["files", CHILD], [("GET", "/artifacts", 200, listing)])
            self.assertIn("new  out.txt  v2", res.stdout)
            dest = os.path.join(tmp, "got.txt")
            res, rec = self._run(["download", CHILD, "out.txt", "-o", dest],
                                 [("GET", "/artifacts", 200, listing),
                                  ("GET", f"/api/sessions/sess-par/children/{CHILD}/artifacts/new/download", 200, b"newer")])
            self.assertEqual(res.returncode, 0, res.stderr)
            with open(dest, "rb") as fh:
                self.assertEqual(fh.read(), b"newer")
            res, _ = self._run(["download", CHILD, "nope"], [("GET", "/artifacts", 200, listing)])
            self.assertEqual(res.returncode, 1)

    def test_events_prints_json_lines(self):
        evs = {"events": [{"seq": 5, "kind": "turn_done", "payload": {}}, {"seq": 6, "kind": "x", "payload": {}}]}
        res, rec = self._run(["events", CHILD, "--after", "4"], [("GET", f"/api/sessions/sess-par/children/{CHILD}/events?after_seq=4", 200, evs)])
        self.assertEqual([json.loads(l)["seq"] for l in res.stdout.splitlines()], [5, 6])

    def test_watch_returns_on_turn_done_and_prints_asks(self):
        tail = ("GET", f"/api/sessions/sess-par/children/{CHILD}/events?before_seq=0", 200, {"events": [{"seq": 10, "kind": "turn_done", "payload": {}}]})
        batch = {"events": [
            {"seq": 11, "kind": "assistant_text", "payload": {"text": "partial", "done": False}},
            {"seq": 12, "kind": "tool_call", "payload": {"tool": "Bash", "call_id": "c"}},
            {"seq": 13, "kind": "assistant_text", "payload": {"text": "All done here.", "done": True}},
            {"seq": 14, "kind": "turn_done", "payload": {"stop_reason": "end_turn"}}]}
        status = ("GET", f"/children/{CHILD}", 200, {"lifecycle": "running"})
        res, rec = self._run(["watch", CHILD], [status, tail, ("GET", f"/api/sessions/sess-par/children/{CHILD}/events?after_seq=10&wait=", 200, batch), status])
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.splitlines(), ["tool: Bash", "All done here."])
        # an open ask is printed and returns 0; an answered one is not
        asks = {"events": [
            {"seq": 11, "kind": "ask", "payload": {"body": "Delete the table?", "message_id": "m-1"}},
            {"seq": 12, "kind": "ask", "payload": {"body": "Which color?", "message_id": "m-2"}},
            {"seq": 13, "kind": "user_message", "payload": {"text": "red", "source": "ask_answer", "message_id": "m-2"}}]}
        res, _ = self._run(["watch", CHILD], [status, tail, ("GET", "/events?after_seq=10", 200, asks), status])
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.strip(), "ask [m-1]: Delete the table?")

    def test_watch_continues_past_empty_polls_and_times_out_with_3(self):
        tail = ("GET", "/events?before_seq=0", 200, {"events": []})
        script = [("GET", f"/children/{CHILD}", 200, {"lifecycle": "running"}), tail, ("GET", "/events?after_seq=0", 200, {"events": []}),
                  ("GET", f"/api/sessions/sess-par/children/{CHILD}", 200, {"lifecycle": "running"})]
        res, rec = self._run(["watch", CHILD, "--timeout", "1"], script, repeat_last=False)
        self.assertEqual(res.returncode, 3, res.stderr)
        self.assertIn(f"still waiting — run: blerg-runner session watch {CHILD}", res.stdout)

    def test_watch_returns_when_the_child_ends(self):
        script = [("GET", f"/children/{CHILD}", 200, {"lifecycle": "running"}),
                  ("GET", "/events?before_seq=0", 200, {"events": []}),
                  ("GET", "/events?after_seq=0", 200, {"events": []}),
                  ("GET", f"/api/sessions/sess-par/children/{CHILD}", 200, {"lifecycle": "ended"})]
        res, _ = self._run(["watch", CHILD, "--until", "end"], script)
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertIn("the session ended", res.stdout)

    def test_budget_lines_and_json(self):
        body = {"daemon": "laptop", "fetched_at": "2030-01-01T00:00:00Z", "engines": [
            {"engine": "claude", "available": True, "windows": [
                {"id": "5h", "used_percent": 62, "resets_at": "2030-01-01T16:10:00Z"},
                {"id": "7d", "used_percent": 31, "resets_at": "2030-01-05T00:00:00Z"}]},
            {"engine": "codex", "available": True, "windows": [], "note": "usage unknown (remote provider)"},
            {"engine": "hermes", "available": True, "local": True, "windows": [], "note": "local (runs on your hardware)"},
            {"engine": "openclaw", "available": False, "windows": []}]}
        res, rec = self._run(["budget"], [("GET", "/api/sessions/sess-par/budget", 200, body)], cmd=())
        self.assertEqual(res.returncode, 0, res.stderr)
        self.assertEqual(res.stdout.splitlines(), [
            "claude  5h 62% used, resets 4:10 PM · 7d 31%",
            "codex  usage unknown (remote provider)",
            "hermes  local (runs on your hardware)",
            "openclaw  not installed on this daemon"])
        res, _ = self._run(["budget", "--json"], [("GET", "/budget", 200, body)], cmd=())
        self.assertEqual(json.loads(res.stdout), body)
        capped = {"engines": [{"engine": "claude", "available": True, "capped_until": "2030-01-01T16:10:00Z",
                               "windows": [{"id": "5h", "used_percent": 100}, {"id": "capped", "used_percent": 100}]}]}
        res, _ = self._run(["budget"], [("GET", "/budget", 200, capped)], cmd=())
        self.assertEqual(res.stdout.strip(), "claude  5h 100% used · capped until 4:10 PM")
        res, _ = self._run(["budget"], [("GET", "/budget", 200, {"engines": [], "note": "no desktop daemon"})], cmd=())
        self.assertEqual(res.stdout.strip(), "no desktop daemon")

    def test_not_under_blerg_is_a_quiet_noop(self):
        res = run_blerg_runner(["session", "list"])
        self.assertEqual(res.returncode, 0)
        self.assertIn("not running under Blerg Runner", res.stderr)

    def test_help_carries_the_rules(self):
        res = run_blerg_runner(["session", "--help"])
        self.assertEqual(res.returncode, 0)
        for text in ("blerg-runner budget", "session start", "per provider", "destructive or irreversible",
                     "blerg-runner ask", "session wait <request_id>", "never run\n`start` with a NEW key",
                     "loopback", "--runtime daemon", "exit with code 3", "80%"):
            self.assertIn(text, res.stdout, text)

    def test_the_help_starts_with_the_line_the_daemon_injects(self):
        # daemon/claudecode.go childSessionGuideLine is the same text as SESSION_LINE.
        import re
        go = os.path.join(os.path.dirname(SCRIPT), "..", "..", "..", "internal", "daemon", "claudecode.go")
        if not os.path.exists(go):
            self.skipTest("not in the repository")
        with open(go, encoding="utf-8") as fh:
            src = fh.read()
        block = re.search(r"const childSessionGuideLine = (.*?)\n\n", src, re.S).group(1)
        parts = re.findall(r'"((?:[^"\\]|\\.)*)"', block)
        injected = "".join(p.replace('\\"', '"') for p in parts)
        res = run_blerg_runner(["session", "--help"])
        self.assertIn(injected, " ".join(res.stdout.split()))

    def test_no_token_is_printed(self):
        for as_json in (True, False):
            server = http.server.HTTPServer(("127.0.0.1", 0), EchoSecretsHandler)
            server.status, server.as_json = 500, as_json
            threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True).start()
            try:
                env = {"BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                       "BLERG_RUNNER_SESSION_ID": "sess-par", "BLERG_RUNNER_SESSION_TOKEN": SECRET_SESSION,
                       "BLERG_RUNNER_DAEMON_TOKEN": SECRET_DAEMON, "BLERG_RUNNER_BOARD_TOKEN": None}
                for argv in (["session", "start", "--no-repo", "p"], ["session", "wait", REQ, "--timeout", "1"], ["session", "list"],
                             ["session", "stop", CHILD], ["session", "watch", CHILD, "--after", "0", "--timeout", "1"],
                             ["session", "result", CHILD], ["budget"]):
                    res = run_blerg_runner(argv, env_override=env)
                    self.assertIn(res.returncode, (1, 3), (argv, res.stderr))
                    self.assertEqual(_leaks(res.stdout + res.stderr, [SECRET_SESSION, SECRET_DAEMON, SECRET_BASIC]), set(), argv)
            finally:
                server.shutdown()
                server.server_close()


@contextlib.contextmanager
def api_server(reply):
    records = []
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass
        def serve(self):
            size = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(size) if size else b""
            records.append((self.command, self.path, dict(self.headers), raw))
            status, body, headers, delay = reply(self.command, self.path, raw)
            if delay:
                time.sleep(delay)
            chunks = body if isinstance(body, list) else None
            if chunks is not None:
                body = b"".join(chunk for chunk, _ in chunks)
            elif not isinstance(body, bytes):
                body = json.dumps(body).encode()
            if isinstance(headers, list):
                try:
                    self.wfile.write(f"HTTP/1.0 {status} OK\r\n".encode())
                    self.wfile.flush()
                    for fragment, pause in headers:
                        time.sleep(pause)
                        self.wfile.write(fragment)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
                return
            self.send_response(status)
            for key, value in headers.items():
                self.send_header(key, value)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            try:
                if chunks is None:
                    self.wfile.write(body)
                else:
                    for chunk, pause in chunks:
                        time.sleep(pause)
                        self.wfile.write(chunk)
                        self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass
        do_GET = do_POST = serve
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True).start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}", records
    finally:
        server.shutdown()
        server.server_close()


def call(url, args, **kwargs):
    return run_blerg_runner(args, env_override={
        "BLERG_RUNNER_SERVER_HTTP": url, "BLERG_RUNNER_SESSION_ID": "fixture-parent",
        "BLERG_RUNNER_SESSION_TOKEN": "fixture-token", "BLERG_RUNNER_BOARD_TOKEN": None,
        "BLERG_RUNNER_DAEMON_TOKEN": None,
    }, **kwargs)


class TestSessionReview(unittest.TestCase):
    def test_legacy_authenticated_calls_never_follow_redirects(self):
        for code in (301, 302, 303, 307, 308):
            for args in (["files"], ["update", "fixture update"]):
                with self.subTest(code=code, command=args[0]):
                    with api_server(lambda *_: (200, {}, {}, 0)) as (target, seen):
                        with api_server(lambda *_: (code, {}, {"Location": target + "/other"}, 0)) as (source, _):
                            result = call(source, args)
                        self.assertEqual(seen, [], "redirect target received a legacy authenticated request")
                        self.assertNotIn("fixture-token", result.stdout + result.stderr)

    def test_sensitive_calls_never_follow_redirects(self):
        for code in (301, 302, 303, 307, 308):
            with self.subTest(code=code):
                with api_server(lambda *_: (200, {"children": []}, {}, 0)) as (target, seen):
                    with api_server(lambda *_: (code, {}, {"Location": target + "/other"}, 0)) as (source, _):
                        result = call(source, ["session", "list"])
                    self.assertEqual(result.returncode, 1)
                    self.assertEqual(seen, [], "redirect target received an authenticated request")
                    self.assertNotIn("fixture-token", result.stdout + result.stderr)

    def test_allowed_without_outcome_keeps_waiting(self):
        starting = dict(PENDING, status="allowed")
        result, seen = TestSession()._run(["wait", REQ, "--timeout", "2"], [
            ("GET", "/api/delegation-requests/", 200, starting),
            ("GET", "/api/delegation-requests/", 200, dict(starting, child_session_id=CHILD)),
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(f"started {CHILD}", result.stdout)
        self.assertEqual(len(seen), 2)

    def test_watch_drains_answer_page_for_old_peer(self):
        ask = {"seq": 1, "kind": "ask", "payload": {"message_id": "q", "body": "already answered"}}
        answer = {"seq": 2, "kind": "user_message", "payload": {"source": "ask_answer", "message_id": "q"}}
        status = ("GET", f"/children/{CHILD}", 200, {"lifecycle": "running"})
        result, seen = TestSession()._run(["watch", CHILD, "--after", "0"], [
            status, ("GET", "/events?after_seq=0", 200, {"events": [ask], "has_more": True}),
            ("GET", "/events?after_seq=1", 200, {"events": [answer, {"seq": 3, "kind": "turn_done", "payload": {}}]}), status,
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("ask [q]", result.stdout)
        self.assertEqual(len(seen), 4)

    def test_watch_reports_existing_question(self):
        snapshot = {"lifecycle": "running", "open_asks": [{"message_id": "q", "body": "existing question"}]}
        with api_server(lambda *_: (200, snapshot, {}, 0)) as (url, seen):
            result = call(url, ["session", "watch", CHILD, "--timeout", "1"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ask [q]: existing question", result.stdout)
        self.assertEqual(len(seen), 1)

    def test_watch_catches_question_between_snapshot_and_tail(self):
        path = f"/children/{CHILD}"
        result, _ = TestSession()._run(["watch", CHILD, "--timeout", "2"], [
            ("GET", path, 200, {"lifecycle": "running", "open_asks": []}),
            ("GET", "/events?before_seq=0", 200, {"events": [{"seq": 10, "kind": "ask", "payload": {"message_id": "q", "body": "race"}}]}),
            ("GET", "/events?after_seq=10", 200, {"events": []}),
            ("GET", path, 200, {"lifecycle": "running", "open_asks": [{"message_id": "q", "body": "race"}]}),
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ask [q]: race", result.stdout)

    def test_watch_prefers_authoritative_empty_snapshot(self):
        status = ("GET", f"/children/{CHILD}", 200, {"lifecycle": "running", "open_asks": []})
        result, _ = TestSession()._run(["watch", CHILD, "--after", "0"], [
            status, ("GET", "/events?after_seq=0", 200, {"events": [
                {"seq": 1, "kind": "ask", "payload": {"message_id": "q", "body": "stale"}},
                {"seq": 2, "kind": "turn_done", "payload": {}}]}), status,
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("ask [q]", result.stdout)

    def test_download_defaults_never_replace_files_or_follow_symlinks(self):
        listing = {"artifacts": [{"id": "a", "name": "out.txt", "version": 1}]}
        with tempfile.TemporaryDirectory() as folder:
            original, target = os.path.join(folder, "out.txt"), os.path.join(folder, "target")
            for path in (original, target):
                with open(path, "wb") as stream:
                    stream.write(b"keep")
            os.symlink(target, os.path.join(folder, "out-1.txt"))
            with api_server(lambda _, path, __: (200, b"fixture-token" if path.endswith("download") else listing, {}, 0)) as (url, _):
                result = call(url, ["session", "download", CHILD, "out.txt"], cwd=folder)
            self.assertEqual(result.returncode, 0, result.stderr)
            for path in (original, target):
                with open(path, "rb") as stream:
                    self.assertEqual(stream.read(), b"keep")
            with open(os.path.join(folder, "out-2.txt"), "rb") as stream:
                self.assertEqual(stream.read(), b"fixture-token", "download bytes were redacted")

    def test_explicit_download_refuses_existing_file_and_symlink(self):
        listing = {"artifacts": [{"id": "a", "name": "out.txt", "version": 1}]}
        with tempfile.TemporaryDirectory() as folder:
            target, link = os.path.join(folder, "target"), os.path.join(folder, "link")
            with open(target, "wb") as stream:
                stream.write(b"keep")
            os.symlink(target, link)
            for path in (target, link):
                with api_server(lambda _, route, __: (200, b"replace" if route.endswith("download") else listing, {}, 0)) as (url, _):
                    result = call(url, ["session", "download", CHILD, "out.txt", "-o", path])
                self.assertEqual(result.returncode, 1)
                with open(target, "rb") as stream:
                    self.assertEqual(stream.read(), b"keep")

    def test_success_json_redaction_preserves_framing(self):
        body = {"value": "Authorization: Bearer fixture-token", "nested": ["fixture-token", {"keep": "yes"}]}
        for args in (["session", "result", CHILD], ["budget", "--json"]):
            with api_server(lambda *_: (200, body, {}, 0)) as (url, _):
                result = call(url, args)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)["nested"][1]["keep"], "yes")
            self.assertNotIn("fixture-token", result.stdout + result.stderr)

    def test_success_text_and_event_json_are_redacted(self):
        with api_server(lambda *_: (200, {"children": [{"id": "c", "title": "fixture-token"}]}, {}, 0)) as (url, _):
            result = call(url, ["session", "list"])
        self.assertNotIn("fixture-token", result.stdout + result.stderr)
        with api_server(lambda *_: (200, {"events": [{"seq": 1, "payload": {"text": "Authorization: Bearer fixture-token"}}]}, {}, 0)) as (url, _):
            result = call(url, ["session", "events", CHILD])
        self.assertNotIn("fixture-token", result.stdout + result.stderr)
        self.assertEqual(json.loads(result.stdout)["seq"], 1)

    def test_watch_finished_child_replays_explicit_cursor(self):
        status = ("GET", f"/children/{CHILD}", 200, {"lifecycle": "ended", "open_asks": []})
        result, seen = TestSession()._run(["watch", CHILD, "--after", "0"], [
            status,
            ("GET", "/events?after_seq=0", 200, {"events": [
                {"seq": 1, "kind": "assistant_text", "payload": {"done": True, "text": "finished work"}}], "has_more": True}),
            ("GET", "/events?after_seq=1", 200, {"events": [
                {"seq": 2, "kind": "turn_done", "payload": {}}]}), status,
        ])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("finished work", result.stdout)
        self.assertIn("the session ended", result.stdout)
        self.assertEqual(len(seen), 4)

    def test_wait_deadline_bounds_slow_stream_body(self):
        # Each byte arrives before the original one-second inactivity timeout, but
        # the command as a whole must still finish within its one-second budget.
        chunks = [(b" ", 0.35)] * 6 + [(json.dumps(PENDING).encode(), 0.35)]
        with api_server(lambda *_: (200, chunks, {}, 0)) as (url, _):
            started = time.monotonic()
            result = call(url, ["session", "wait", REQ, "--timeout", "1"], timeout=5)
            elapsed = time.monotonic() - started
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertLess(elapsed, 1.6)

    def test_download_directory_never_overwrites(self):
        listing = {"artifacts": [{"id": "a", "name": "../out.txt", "version": 1}]}
        with tempfile.TemporaryDirectory() as folder:
            path = os.path.join(folder, "out.txt")
            with open(path, "wb") as stream:
                stream.write(b"keep")
            with api_server(lambda _, route, __: (200, b"new" if route.endswith("download") else listing, {}, 0)) as (url, _):
                result = call(url, ["session", "download", CHILD, "a", "-o", folder])
            self.assertEqual(result.returncode, 0, result.stderr)
            with open(path, "rb") as stream:
                self.assertEqual(stream.read(), b"keep")
            with open(os.path.join(folder, "out-1.txt"), "rb") as stream:
                self.assertEqual(stream.read(), b"new")

    @unittest.skipUnless(sys.platform != "win32", "POSIX request alarm")
    def test_wait_deadline_bounds_slow_headers(self):
        fragments = [(b"X-Slow: a", 0)] + [(b"b", 0.35)] * 6 + [(b"\r\nContent-Length: 2\r\n\r\n{}", 0.35)]
        with api_server(lambda *_: (200, {}, fragments, 0)) as (url, _):
            started = time.monotonic()
            result = call(url, ["session", "wait", REQ, "--timeout", "1"], timeout=5)
            elapsed = time.monotonic() - started
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertLess(elapsed, 1.6)

    @unittest.skipUnless(sys.platform != "win32", "POSIX request alarm")
    def test_request_deadline_restores_alarm_after_success_error_timeout(self):
        code = r"""
import runpy, signal, sys, time, urllib.error
ns = runpy.run_path(sys.argv[1], run_name="review_fixture")
def previous(signum, frame):
    raise AssertionError("unrelated timer fired")
for mode in ("success", "http_error", "timeout"):
    signal.signal(signal.SIGALRM, previous)
    signal.setitimer(signal.ITIMER_REAL, 10, 3)
    try:
        with ns["_session_deadline"](time.monotonic() + 0.03):
            if mode == "http_error":
                raise urllib.error.HTTPError("fixture", 403, "refused", {}, None)
            if mode == "timeout":
                time.sleep(0.2)
    except urllib.error.HTTPError:
        assert mode == "http_error"
    except TimeoutError:
        assert mode == "timeout"
    assert signal.getsignal(signal.SIGALRM) is previous
    remaining, interval = signal.getitimer(signal.ITIMER_REAL)
    assert 9 < remaining <= 10 and interval == 3
    signal.setitimer(signal.ITIMER_REAL, 0)
# A normal call without a deadline leaves an existing alarm untouched.
signal.setitimer(signal.ITIMER_REAL, 10)
with ns["_session_deadline"](None):
    assert signal.getsignal(signal.SIGALRM) is previous
signal.setitimer(signal.ITIMER_REAL, 0)
"""
        with tempfile.TemporaryDirectory() as folder:
            result = subprocess.run([sys.executable, "-c", code, SCRIPT], capture_output=True, text=True,
                                    env={"HOME": folder, "PATH": os.defpath}, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_start_post_and_wait_share_deadline(self):
        def reply(method, *_):
            return (202 if method == "POST" else 200), PENDING, {}, 0.7
        with api_server(reply) as (url, seen):
            started = time.monotonic()
            result = call(url, ["session", "start", "--no-repo", "--timeout", "1", "p"], timeout=5)
            elapsed = time.monotonic() - started
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertLess(elapsed, 1.6)
        self.assertIn(f"session wait {REQ}", result.stdout)
        self.assertEqual([r[0] for r in seen], ["POST", "GET"])

    def test_wait_immediate_inflight_polls_do_not_spin(self):
        starting = dict(PENDING, status="allowed")
        with api_server(lambda *_: (200, starting, {}, 0)) as (url, seen):
            result = call(url, ["session", "wait", REQ, "--timeout", "1"])
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertGreater(len(seen), 1)
        self.assertLess(len(seen), 30)

    def test_start_deadline_includes_unknown_reply_and_preserves_key(self):
        with api_server(lambda *_: (202, PENDING, {}, 2)) as (url, _):
            started = time.monotonic()
            result = call(url, ["session", "start", "--no-repo", "--timeout", "1", "--idempotency-key", "same-key", "p"], timeout=5)
            elapsed = time.monotonic() - started
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertLess(elapsed, 1.6)
        self.assertIn("--idempotency-key same-key", result.stdout + result.stderr)

    def test_wait_and_watch_slow_replies_obey_total_deadline(self):
        for args in (["session", "wait", REQ, "--timeout", "1"], ["session", "watch", CHILD, "--timeout", "1"]):
            with api_server(lambda *_: (200, PENDING, {}, 2)) as (url, _):
                started = time.monotonic()
                result = call(url, args, timeout=5)
                elapsed = time.monotonic() - started
            self.assertEqual(result.returncode, 3, result.stderr)
            self.assertLess(elapsed, 1.6)


if __name__ == "__main__":
    unittest.main(verbosity=2)
