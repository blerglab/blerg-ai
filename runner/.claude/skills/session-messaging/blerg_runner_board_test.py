#!/usr/bin/env python3
"""
Tests for blerg-runner board/ticket/column/tag subcommands.

Run with:
  cd runner
  python3 -m pytest .claude/skills/session-messaging/blerg_runner_board_test.py -q
"""

import http.server
import json
import os
import subprocess
import sys
import threading

import pytest

SCRIPT = os.path.join(os.path.dirname(__file__), "blerg-runner")

# Fixed IDs used in tests (valid UUID shape so resolution is bypassed).
TEST_BOARD_ID = "aaaaaaaa-0000-1111-2222-bbbbbbbbbbbb"
TEST_BOARD_TOKEN = "board-tok-test"
COL_UUID = "cccccccc-1111-2222-3333-dddddddddddd"
COL_UUID_2 = "cccccccc-5555-6666-7777-dddddddddddd"
TICKET_UUID = "11111111-aaaa-bbbb-cccc-222222222222"
OTHER_UUID = "ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb"

# Board responses used by the stub.
CLEAR_BOARD = {
    "id": TEST_BOARD_ID,
    "name": "Test Board",
    "description": None,
    "repos": [],
    "default_daemon_id": None,
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:00Z",
    "columns": [
        {
            "id": COL_UUID,
            "board_id": TEST_BOARD_ID,
            "rank": "a",
            "name": "In Progress",
            "is_terminal": False,
            "created_at": "2026-01-01T00:00:00Z",
        },
    ],
    "tickets": [
        {
            "id": TICKET_UUID,
            "board_id": TEST_BOARD_ID,
            "column_id": COL_UUID,
            "title": "Fix the bug",
            "body": None,
            "priority": "medium",
            "size": None,
            "rank": "a",
            "archived_at": None,
            "session_id": None,
            "version": 1,
            "created_at": "2026-01-01T00:00:00Z",
            "updated_at": "2026-01-01T00:00:00Z",
        },
    ],
}

# Two columns with the same name → ambiguous resolution.
AMBIGUOUS_BOARD = {
    **CLEAR_BOARD,
    "columns": [
        {
            "id": "col-aaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
            "board_id": TEST_BOARD_ID,
            "rank": "a",
            "name": "Doing",
            "is_terminal": False,
            "created_at": "2026-01-01T00:00:00Z",
        },
        {
            "id": "col-bbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
            "board_id": TEST_BOARD_ID,
            "rank": "b",
            "name": "Doing",
            "is_terminal": False,
            "created_at": "2026-01-01T00:00:00Z",
        },
    ],
}

# ── Stub HTTP server ───────────────────────────────────────────────────────────


class BoardStubHandler(http.server.BaseHTTPRequestHandler):
    """Records all requests and returns canned responses."""

    def log_message(self, fmt, *args):
        pass  # silence access log

    def _read_body(self):
        n = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(n) if n else b""

    def _record(self, method, body_bytes):
        auth = self.headers.get("Authorization", "")
        body = {}
        if body_bytes:
            try:
                body = json.loads(body_bytes)
            except Exception:
                pass
        rec = {"method": method, "path": self.path, "auth": auth, "body": body}
        self.server.recorded.append(rec)
        return rec

    def _send(self, status, data):
        enc = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(enc)))
        self.end_headers()
        self.wfile.write(enc)

    def _path(self):
        """Path without query string."""
        return self.path.split("?")[0]

    def do_GET(self):
        self._record("GET", b"")
        p = self._path()
        if p.startswith("/api/boards/") and p.endswith("/order"):
            self._send(200, {"tickets": []})
        elif p.startswith("/api/boards/") and p.endswith("/tickets"):
            self._send(200, {"tickets": [], "next_cursor": ""})
        elif p.startswith("/api/boards/"):
            self._send(200, self.server.board_response)
        elif p.startswith("/api/tickets/"):
            tid = p.split("/api/tickets/")[1]
            self._send(200, {
                "id": tid,
                "board_id": TEST_BOARD_ID,
                "column_id": COL_UUID,
                "title": "Ticket",
                "body": None,
                "priority": "medium",
                "size": None,
                "rank": "a",
                "archived_at": None,
                "session_id": None,
                "version": 1,
                "created_at": "2026-01-01T00:00:00Z",
                "updated_at": "2026-01-01T00:00:00Z",
                "repos": [],
                "tags": ["backend"],
                "depends_on": [],
                "blocks": [],
            })
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        body = self._read_body()
        self._record("POST", body)
        p = self._path()
        if p.startswith("/api/boards/") and p.endswith("/tickets"):
            self._send(201, {
                "id": "ticket-new-001",
                "board_id": TEST_BOARD_ID,
                "column_id": None,
                "title": "New Ticket",
                "body": None,
                "priority": "medium",
                "size": None,
                "rank": "z",
                "archived_at": None,
                "session_id": None,
                "version": 1,
                "created_at": "2026-01-01T00:00:00Z",
                "updated_at": "2026-01-01T00:00:00Z",
            })
        elif p.startswith("/api/boards/") and p.endswith("/columns"):
            self._send(201, {
                "id": "col-new-001",
                "board_id": TEST_BOARD_ID,
                "rank": "z",
                "name": "New Column",
                "is_terminal": False,
                "created_at": "2026-01-01T00:00:00Z",
            })
        elif "/api/tickets/" in p and p.endswith("/split"):
            self._send(200, {"tickets": []})
        elif "/api/tickets/" in p and "/dependencies" in p:
            self._send(201, {})
        elif "/api/tickets/" in p and p.endswith("/archive"):
            self._send(200, {})
        else:
            self.send_response(404)
            self.end_headers()

    def do_PATCH(self):
        body = self._read_body()
        self._record("PATCH", body)
        p = self._path()
        if p.startswith("/api/tickets/"):
            tid = p.split("/api/tickets/")[1]
            self._send(200, {
                "id": tid,
                "board_id": TEST_BOARD_ID,
                "column_id": COL_UUID,
                "title": "Updated",
                "body": None,
                "priority": "medium",
                "size": None,
                "rank": "a",
                "archived_at": None,
                "session_id": None,
                "version": 2,
                "created_at": "2026-01-01T00:00:00Z",
                "updated_at": "2026-01-01T00:00:00Z",
            })
        elif p.startswith("/api/columns/"):
            cid = p.split("/api/columns/")[1]
            self._send(200, {
                "id": cid,
                "board_id": TEST_BOARD_ID,
                "rank": "a",
                "name": "Updated Column",
                "is_terminal": False,
                "created_at": "2026-01-01T00:00:00Z",
            })
        else:
            self.send_response(404)
            self.end_headers()

    def do_DELETE(self):
        self._record("DELETE", b"")
        self.send_response(204)
        self.end_headers()


def make_stub(board_response=None):
    server = http.server.HTTPServer(("127.0.0.1", 0), BoardStubHandler)
    server.recorded = []
    server.board_response = board_response if board_response is not None else CLEAR_BOARD
    return server


def run_cli(args, env_extra=None, timeout=10):
    """Run the blerg-runner script as a subprocess with a clean env."""
    # Start with a minimal env (strip all blerg-runner vars).
    env = {
        "HOME": os.environ.get("HOME", "/tmp"),
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "LANG": os.environ.get("LANG", "C.UTF-8"),
        # Strip all blerg-runner env vars.
        "BLERG_RUNNER_SERVER_HTTP": "",
        "BLERG_RUNNER_SESSION_ID": "",
        "BLERG_RUNNER_DAEMON_TOKEN": "",
        "BLERG_RUNNER_BOARD_ID": "",
        "BLERG_RUNNER_BOARD_TOKEN": "",
    }
    if env_extra:
        env.update(env_extra)
    return subprocess.run(
        [sys.executable, SCRIPT] + args,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
    )


def _board_env(port):
    """Standard board env pointing at the stub server."""
    return {
        "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{port}",
        "BLERG_RUNNER_BOARD_ID": TEST_BOARD_ID,
        "BLERG_RUNNER_BOARD_TOKEN": TEST_BOARD_TOKEN,
    }


def _serve_n(server, n):
    """Handle exactly n requests in this thread (for one-shot stubs)."""
    for _ in range(n):
        server.handle_request()


# ── TEST: missing board env ────────────────────────────────────────────────────


class TestMissingBoardEnv:
    """Missing BLERG_RUNNER_BOARD_ID / BLERG_RUNNER_BOARD_TOKEN → exit 0 + notice."""

    def test_board_show_exits_0(self):
        result = run_cli(["board", "show"])
        assert result.returncode == 0, f"stderr: {result.stderr}"
        assert "blerg-runner" in result.stderr.lower(), \
            f"Expected notice on stderr; got: {result.stderr!r}"

    def test_ticket_create_exits_0(self):
        result = run_cli(["ticket", "create", TEST_BOARD_ID, "--title", "x"])
        assert result.returncode == 0, f"stderr: {result.stderr}"
        assert "blerg-runner" in result.stderr.lower()

    def test_column_add_exits_0(self):
        result = run_cli(["column", "add", TEST_BOARD_ID, "--name", "New"])
        assert result.returncode == 0, f"stderr: {result.stderr}"

    def test_tag_list_exits_0(self):
        result = run_cli(["tag", "list", TEST_BOARD_ID])
        assert result.returncode == 0, f"stderr: {result.stderr}"

    def test_only_token_missing_exits_0(self):
        """BOARD_ID present but TOKEN absent → still exit 0."""
        result = run_cli(
            ["board", "show"],
            env_extra={
                "BLERG_RUNNER_SERVER_HTTP": "http://127.0.0.1:9999",
                "BLERG_RUNNER_BOARD_ID": TEST_BOARD_ID,
                # no BLERG_RUNNER_BOARD_TOKEN
            },
        )
        assert result.returncode == 0, f"stderr: {result.stderr}"


# ── TEST: ticket create ────────────────────────────────────────────────────────


class TestTicketCreate:
    """ticket create posts to the right path with the right JSON and Bearer token."""

    def test_post_path_and_auth(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            [
                "ticket", "create", TEST_BOARD_ID,
                "--title", "Fix the login bug",
                "--priority", "high",
            ],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts, "No POST recorded"
        req = posts[0]
        assert req["path"] == f"/api/boards/{TEST_BOARD_ID}/tickets", \
            f"Wrong path: {req['path']}"
        assert req["auth"] == f"Bearer {TEST_BOARD_TOKEN}", \
            f"Wrong auth: {req['auth']}"
        assert req["body"]["title"] == "Fix the login bug"
        assert req["body"]["priority"] == "high"

    def test_create_defaults_board_to_env(self):
        """With NO board positional, create targets $BLERG_RUNNER_BOARD_ID."""
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "create", "--title", "No board arg"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts, "No POST recorded"
        assert posts[0]["path"] == f"/api/boards/{TEST_BOARD_ID}/tickets", \
            f"Expected default board path; got: {posts[0]['path']}"
        assert posts[0]["body"]["title"] == "No board arg"

    def test_post_includes_body_and_tags(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            [
                "ticket", "create", TEST_BOARD_ID,
                "--title", "New feature",
                "--body", "Details here",
                "--priority", "medium",
                "--size", "M",
                "--tag", "backend",
                "--tag", "api",
            ],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        req = posts[0]
        assert req["body"]["title"] == "New feature"
        assert req["body"]["body"] == "Details here"
        assert req["body"]["priority"] == "medium"
        assert req["body"]["size"] == "M"
        assert "backend" in req["body"].get("tags", [])
        assert "api" in req["body"].get("tags", [])

    def test_post_with_column_uuid(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            [
                "ticket", "create", TEST_BOARD_ID,
                "--title", "With column",
                "--column", COL_UUID,
            ],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        assert posts[0]["body"]["column_id"] == COL_UUID


# ── TEST: ticket move ──────────────────────────────────────────────────────────


class TestTicketMove:
    """ticket move patches the ticket with column_id."""

    def test_move_by_uuid_sets_column_id(self):
        stub = make_stub()
        port = stub.server_address[1]
        # PATCH /api/tickets/{id} only — both args are UUIDs, no resolution needed.
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "move", TICKET_UUID, "--column", COL_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        patches = [r for r in stub.recorded if r["method"] == "PATCH"]
        assert patches, "No PATCH recorded"
        req = patches[0]
        assert req["path"] == f"/api/tickets/{TICKET_UUID}", \
            f"Wrong path: {req['path']}"
        assert req["auth"] == f"Bearer {TEST_BOARD_TOKEN}"
        assert req["body"]["column_id"] == COL_UUID

    def test_move_with_after_uuid(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            [
                "ticket", "move", TICKET_UUID,
                "--column", COL_UUID,
                "--after", OTHER_UUID,
            ],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        patches = [r for r in stub.recorded if r["method"] == "PATCH"]
        assert patches
        body = patches[0]["body"]
        assert body["column_id"] == COL_UUID
        assert body["after"] == OTHER_UUID


# ── TEST: ambiguous name resolution ───────────────────────────────────────────


class TestAmbiguousName:
    """Name that matches multiple columns → non-zero exit + 'ambiguous' in stderr."""

    def test_ambiguous_column_name_column_rm(self):
        stub = make_stub(board_response=AMBIGUOUS_BOARD)
        port = stub.server_address[1]
        # Serve forever in background; CLI will GET board then bail.
        t = threading.Thread(target=stub.serve_forever)
        t.daemon = True
        t.start()

        result = run_cli(
            ["column", "rm", "Doing"],
            env_extra=_board_env(port),
        )

        stub.shutdown()
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode != 0, \
            f"Expected non-zero exit for ambiguous name; stderr: {result.stderr}"
        assert "ambiguous" in result.stderr.lower(), \
            f"Expected 'ambiguous' in stderr; got: {result.stderr!r}"

    def test_not_found_column_name(self):
        stub = make_stub()  # board has only "In Progress", not "Backlog"
        port = stub.server_address[1]
        t = threading.Thread(target=stub.serve_forever)
        t.daemon = True
        t.start()

        result = run_cli(
            ["column", "rm", "Backlog"],
            env_extra=_board_env(port),
        )

        stub.shutdown()
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode != 0, \
            f"Expected non-zero exit for missing name; stderr: {result.stderr}"
        assert "not found" in result.stderr.lower(), \
            f"Expected 'not found' in stderr; got: {result.stderr!r}"


# ── TEST: existing update/ask/note still work ──────────────────────────────────


class TestExistingCommandsUnbroken:
    """The new subcommands must not break the existing update / ask / note paths."""

    def test_update_noop_when_unconfigured(self):
        result = run_cli(["update", "progress"])
        assert result.returncode == 0, f"stderr: {result.stderr}"

    def test_ask_noop_when_unconfigured(self):
        result = run_cli(["ask", "should I proceed?"])
        assert result.returncode == 0, f"stderr: {result.stderr}"

    def test_note_noop_when_unconfigured(self):
        result = run_cli(["note", "idea"])
        assert result.returncode == 0, f"stderr: {result.stderr}"


# ── TEST: board show ───────────────────────────────────────────────────────────


class TestBoardShow:
    """board show GETs /api/boards/{id} and prints columns + tickets."""

    def test_show_default_board(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["board", "show"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        gets = [r for r in stub.recorded if r["method"] == "GET"]
        assert gets, "No GET recorded"
        assert f"/api/boards/{TEST_BOARD_ID}" in gets[0]["path"]
        assert gets[0]["auth"] == f"Bearer {TEST_BOARD_TOKEN}"
        # Human-readable output should mention column and ticket names.
        assert "In Progress" in result.stdout
        assert "Fix the bug" in result.stdout

    def test_show_explicit_board_id(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["board", "show", TEST_BOARD_ID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"


# ── TEST: column add ───────────────────────────────────────────────────────────


class TestColumnAdd:
    def test_add_column(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["column", "add", TEST_BOARD_ID, "--name", "Review"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        req = posts[0]
        assert req["path"] == f"/api/boards/{TEST_BOARD_ID}/columns"
        assert req["auth"] == f"Bearer {TEST_BOARD_TOKEN}"
        assert req["body"]["name"] == "Review"

    def test_add_terminal_column(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["column", "add", TEST_BOARD_ID, "--name", "Done", "--terminal"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        assert posts[0]["body"]["terminal"] is True


# ── TEST: column rm ────────────────────────────────────────────────────────────


class TestColumnRm:
    def test_rm_by_uuid(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["column", "rm", COL_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        deletes = [r for r in stub.recorded if r["method"] == "DELETE"]
        assert deletes
        assert deletes[0]["path"] == f"/api/columns/{COL_UUID}"
        assert deletes[0]["auth"] == f"Bearer {TEST_BOARD_TOKEN}"


# ── TEST: column move anchor name resolution ──────────────────────────────────


class TestColumnMoveAnchorResolution:
    """column move --after <NAME> resolves the anchor name to its column id."""

    def test_move_after_name_resolves_to_id(self):
        # CLEAR_BOARD has a column "In Progress" → COL_UUID. The target column
        # is passed as a UUID (no resolution); only the anchor is a name.
        stub = make_stub()
        port = stub.server_address[1]
        # GET board (anchor resolution) + PATCH; serve_forever to be safe.
        t = threading.Thread(target=stub.serve_forever)
        t.daemon = True
        t.start()

        result = run_cli(
            ["column", "move", COL_UUID_2, "--after", "In Progress"],
            env_extra=_board_env(port),
        )

        stub.shutdown()
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        patches = [r for r in stub.recorded if r["method"] == "PATCH"]
        assert patches, "No PATCH recorded"
        req = patches[0]
        assert req["path"] == f"/api/columns/{COL_UUID_2}", \
            f"Wrong path: {req['path']}"
        # The anchor name "In Progress" must be resolved to COL_UUID, not
        # passed through raw.
        assert req["body"]["after"] == COL_UUID, \
            f"Expected anchor resolved to {COL_UUID}; got: {req['body'].get('after')!r}"


# ── TEST: ticket get ───────────────────────────────────────────────────────────


class TestTicketGet:
    def test_get_by_uuid(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "get", TICKET_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        gets = [r for r in stub.recorded if r["method"] == "GET"]
        assert gets
        assert gets[0]["path"] == f"/api/tickets/{TICKET_UUID}"
        assert gets[0]["auth"] == f"Bearer {TEST_BOARD_TOKEN}"


# ── TEST: ticket dep add ───────────────────────────────────────────────────────


class TestTicketDepAdd:
    def test_dep_add_posts_to_dependencies(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "dep", "add", TICKET_UUID, "--on", OTHER_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        req = posts[0]
        assert req["path"] == f"/api/tickets/{TICKET_UUID}/dependencies"
        assert req["body"]["depends_on_ticket_id"] == OTHER_UUID


# ── TEST: ticket dep rm ────────────────────────────────────────────────────────


class TestTicketDepRm:
    def test_dep_rm_deletes_dependency(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "dep", "rm", TICKET_UUID, "--on", OTHER_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        deletes = [r for r in stub.recorded if r["method"] == "DELETE"]
        assert deletes
        assert deletes[0]["path"] == f"/api/tickets/{TICKET_UUID}/dependencies/{OTHER_UUID}"


# ── TEST: ticket archive ───────────────────────────────────────────────────────


class TestTicketArchive:
    def test_archive_posts_to_archive_endpoint(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "archive", TICKET_UUID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        assert posts[0]["path"] == f"/api/tickets/{TICKET_UUID}/archive"


# ── TEST: ticket split ─────────────────────────────────────────────────────────


class TestTicketSplit:
    def test_split_posts_titles(self):
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            [
                "ticket", "split", TICKET_UUID,
                "--into", "Part A",
                "--into", "Part B",
            ],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        posts = [r for r in stub.recorded if r["method"] == "POST"]
        assert posts
        req = posts[0]
        assert req["path"] == f"/api/tickets/{TICKET_UUID}/split"
        assert req["body"]["titles"] == ["Part A", "Part B"]


# ── TEST: tag list ─────────────────────────────────────────────────────────────


class TestTagList:
    def test_tag_list_prints_sorted_tags(self):
        stub = make_stub()
        port = stub.server_address[1]
        # tag list: GET board (1 req) + GET each ticket (1 req) = 2 reqs
        t = threading.Thread(target=_serve_n, args=(stub, 2))
        t.start()

        result = run_cli(
            ["tag", "list", TEST_BOARD_ID],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        # The stub returns tag "backend" for each individual ticket GET.
        assert "backend" in result.stdout

    def test_tag_list_defaults_board_to_env(self):
        """With NO board positional, tag list reads $BLERG_RUNNER_BOARD_ID."""
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 2))
        t.start()

        result = run_cli(
            ["tag", "list"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        gets = [r for r in stub.recorded if r["method"] == "GET"]
        assert gets, "No GET recorded"
        assert f"/api/boards/{TEST_BOARD_ID}" in gets[0]["path"]
        assert "backend" in result.stdout


# ── TEST: ticket list defaults board ──────────────────────────────────────────


class TestTicketListDefaultBoard:
    def test_list_defaults_board_to_env(self):
        """With NO board positional, ticket list targets $BLERG_RUNNER_BOARD_ID."""
        stub = make_stub()
        port = stub.server_address[1]
        t = threading.Thread(target=_serve_n, args=(stub, 1))
        t.start()

        result = run_cli(
            ["ticket", "list"],
            env_extra=_board_env(port),
        )
        t.join(timeout=5)
        stub.server_close()

        assert result.returncode == 0, f"stderr: {result.stderr}"
        gets = [r for r in stub.recorded if r["method"] == "GET"]
        assert gets, "No GET recorded"
        assert gets[0]["path"].startswith(f"/api/boards/{TEST_BOARD_ID}/tickets")
