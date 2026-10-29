"""blerg-runner board create: a session proposes a board; the person approves it in Needs you."""
import http.server
import json
import unittest

from test_blerg_runner import run_blerg_runner, serve


class BoardCreateStub(http.server.BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        self.server.recorded.append({"path": self.path, "auth": self.headers.get("Authorization", ""),
                                     "key": self.headers.get("Idempotency-Key"), "body": json.loads(body) if body else {}})
        replies = self.server.replies
        status, reply = replies.pop(0) if len(replies) > 1 else replies[0]
        data = json.dumps(reply).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def run(args, replies):
    server = http.server.HTTPServer(("127.0.0.1", 0), BoardCreateStub)
    server.recorded = []
    server.replies = list(replies)
    stop = serve(server)
    try:
        result = run_blerg_runner(["board", "create"] + args, env_override={
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_port}",
            "BLERG_RUNNER_SESSION_ID": "sess-board", "BLERG_RUNNER_SESSION_TOKEN": "fixture-board-token"}, fast_retries=True)
    finally:
        stop()
    return result, server.recorded


PENDING = (202, {"proposal_id": "proposal-board", "state": "pending"})


class TestBoardCreateCLI(unittest.TestCase):
    def test_proposes_the_board_with_every_field(self):
        result, calls = run(["Launch", "--repo", "team/app", "--repo", "web", "--columns", "Todo, Doing,Done",
                             "--description", "Ship it", "--access", "file"], [PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["path"], "/api/sessions/sess-board/board-create")
        self.assertEqual(calls[0]["auth"], "Bearer fixture-board-token")
        self.assertEqual(calls[0]["body"], {"name": "Launch", "repos": ["team/app", "web"], "columns": ["Todo", "Doing", "Done"],
                                            "description": "Ship it", "access": "file"})
        self.assertIn("Board proposed: proposal-board", result.stdout)
        self.assertIn("blerg-runner ticket status proposal-board", result.stdout)
        self.assertIn("do not propose it again", result.stdout)

    def test_template_and_default_access(self):
        result, calls = run(["Errands", "--template", "focus-board"], [PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0]["body"], {"name": "Errands", "template": "focus-board", "access": "full"})

    def test_sends_an_idempotency_key_and_reuses_a_given_one(self):
        result, calls = run(["Launch"], [PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        key = calls[0]["key"]
        self.assertRegex(key or "", r"^[0-9a-f]{32}$")
        self.assertIn(f"idempotency key: {key}", result.stderr)
        result, calls = run(["Launch", "--idempotency-key", "fixture-key-1"],
                            [(202, {"proposal_id": "proposal-board", "state": "pending", "repeated": True})])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0]["key"], "fixture-key-1")
        self.assertIn("already sent", result.stdout)

    def test_short_idempotency_key_matches_the_runner_contract(self):
        result, calls = run(["Launch", "--idempotency-key", "k"], [PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0]["key"], "k")

    def test_ascii_space_in_a_key_matches_existing_child_callers(self):
        result, calls = run(["Launch", "--idempotency-key", "key with spaces"], [PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0]["key"], "key with spaces")

    def test_space_key_retry_hint_is_shell_quoted(self):
        result, _calls = run(["Launch", "--idempotency-key", "key with spaces"], [(200, {})])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--idempotency-key 'key with spaces'", result.stderr)

    def test_retries_an_outage_with_the_same_key(self):
        result, calls = run(["Launch"], [(503, {"error": "busy"}), PENDING])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0]["key"], calls[1]["key"])
        self.assertEqual(calls[0]["body"], calls[1]["body"])

    def test_refuses_invalid_drafts_before_sending(self):
        for args in (["a\nb"], [""], ["x", "--repo", "../etc"], ["x", "--repo", "a/b/c"], ["x", "--repo", "app", "--repo", "APP"],
                     ["x", "--template", "focus-board", "--columns", "a"], ["x", "--columns", "a,a"],
                     ["x", "--template", "Focus Board"], ["x", "--idempotency-key", "bad/key"], ["x" * 101],
                     ["Release\u202eeganam"], ["x", "--columns", "Done\u200b,Todo"], ["x", "--description", "a\u2066b"]):
            result, calls = run(args, [PENDING])
            self.assertNotEqual(result.returncode, 0, args)
            self.assertEqual(calls, [], args)
        result, calls = run(["x", "--access", "admin"], [PENDING])
        self.assertEqual(result.returncode, 2)
        self.assertEqual(calls, [])

    def test_refusal_and_decided_states(self):
        result, _ = run(["Launch"], [(409, {"error": "This session cannot be given access to a new board"})])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot be given access", result.stderr)
        result, _ = run(["Launch"], [(202, {"proposal_id": "p1", "state": "done", "repeated": True,
                                           "board": {"board_id": "b1", "board_name": "Launch", "access": "full"}})])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Board created: Launch (b1); your access: full", result.stdout)
        result, _ = run(["Launch"], [(202, {"proposal_id": "p1", "state": "rejected", "repeated": True})])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no board was created", result.stderr)

    def test_help_tells_agents_when_to_use_it(self):
        result = run_blerg_runner(["board", "create", "--help"])
        self.assertEqual(result.returncode, 0)
        self.assertIn("make a board for X", result.stdout)
        self.assertIn("--idempotency-key", result.stdout)
        result = run_blerg_runner(["board", "--help"])
        self.assertIn("create", result.stdout)


if __name__ == "__main__":
    unittest.main()
