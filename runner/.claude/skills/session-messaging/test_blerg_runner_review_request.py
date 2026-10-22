"""blerg-runner review request / wait / withdraw, and publish/diff --request-review, against a stub
runner: the request names the exact version just published, --wait prints the decision with its
exit code (0 approved, 2 changes requested, 3 still waiting), and publish without the flag is
unchanged."""

import http.server
import json
import os
import subprocess
import tempfile
import threading
import unittest

from test_blerg_runner import run_blerg_runner, serve

ART_ID = "a" * 32


class ReviewStubHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def _reply(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        s = self.server
        s.gets.append(self.path)
        if self.path.startswith("/api/review-requests/"):
            s.polls += 1
            if s.decision is not None and s.polls >= s.decide_after:
                return self._reply(200, s.decision)
            return self._reply(200, {"request_id": "r1", "state": "pending", "artifact_name": "SECURITY.md", "artifact_version": 2})
        # GET .../files: the session's limits (none: an older runner).
        return self._reply(200, {"files": [], "used": 0, "limit": 50})

    def do_POST(self):
        s = self.server
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""
        entry = {"path": self.path, "auth": self.headers.get("Authorization", ""), "name": self.headers.get("X-Artifact-Name", "")}
        if self.path.endswith("/review-requests") or self.path.endswith("/withdraw"):
            entry["json"] = json.loads(body or b"{}")
        s.recorded.append(entry)
        if self.path.endswith("/artifacts"):
            return self._reply(201, s.artifact_reply)
        if self.path.endswith("/review-requests"):
            return self._reply(s.request_status, s.request_reply)
        if self.path.endswith("/withdraw"):
            return self._reply(200, {"request_id": "r1", "state": "withdrawn", "artifact_name": "SECURITY.md", "artifact_version": 2})
        return self._reply(404, {"error": "not found"})


def make_server(decision=None, decide_after=1, request_status=201, request_reply=None, artifact_reply=None):
    s = http.server.ThreadingHTTPServer(("127.0.0.1", 0), ReviewStubHandler)
    s.recorded, s.gets, s.polls = [], [], 0
    s.decision, s.decide_after = decision, decide_after
    s.request_status = request_status
    s.request_reply = request_reply or {"request_id": "r1", "artifact_id": ART_ID, "artifact_name": "SECURITY.md",
                                        "artifact_version": 2, "state": "pending", "created": True}
    s.artifact_reply = artifact_reply or {"id": ART_ID, "name": "SECURITY.md", "size": 5, "view": "markdown", "version": 2,
                                          "previous": 1, "url": "/sessions/sess-rr?artifact=" + ART_ID}
    return s


class TestReviewRequest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def run_cli(self, args, server, cwd=None, timeout=30):
        stop = serve(server)
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-rr",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
            "HOME": self.tmp.name,
            "GIT_CONFIG_NOSYSTEM": "1",
        }
        try:
            return run_blerg_runner(args, env_override=env, cwd=cwd, timeout=timeout)
        finally:
            stop()

    def requests(self, server):
        return [r for r in server.recorded if r["path"].endswith("/review-requests")]

    def test_request_returns_at_once_and_says_the_decision_comes_as_a_message(self):
        server = make_server()
        result = self.run_cli(["review", "request", "SECURITY.md", "--ask", "OK to publish?"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        [req] = self.requests(server)
        self.assertEqual(req["path"], "/api/sessions/sess-rr/review-requests")
        self.assertEqual(req["json"], {"artifact": "SECURITY.md", "ask": "OK to publish?"})
        self.assertEqual(req["auth"], "Bearer sess-tok")
        self.assertIn("Review requested: SECURITY.md v2", result.stdout)
        self.assertIn("arrives as a message", result.stdout)
        self.assertFalse(any(g.startswith("/api/review-requests/") for g in server.gets), "it waited without --wait")

    def test_wait_prints_an_approval_and_exits_0(self):
        server = make_server(decision={"request_id": "r1", "state": "approved", "message": "Review of SECURITY.md v2: APPROVED"}, decide_after=2)
        result = self.run_cli(["review", "request", "SECURITY.md", "--ask", "OK?", "--wait"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Review of SECURITY.md v2: APPROVED", result.stdout)
        self.assertTrue(any("/api/review-requests/r1?wait=" in g for g in server.gets))

    def test_wait_exits_2_on_changes_requested_and_1_when_superseded(self):
        server = make_server(decision={"request_id": "r1", "state": "changes_requested",
                                       "message": "Review of SECURITY.md v2: CHANGES REQUESTED: name the address"})
        result = self.run_cli(["review", "wait", "r1"], server)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("CHANGES REQUESTED: name the address", result.stdout)
        server = make_server(decision={"request_id": "r1", "state": "superseded", "message": "Review of SECURITY.md v2: SUPERSEDED by v3"})
        result = self.run_cli(["review", "wait", "r1"], server)
        self.assertEqual(result.returncode, 1, result.stderr)

    def test_status_and_wait_exit_4_when_the_person_dismissed_it(self):
        message = ("Review of plan.md v1 (request r1): DISMISSED. The person dismissed the review request for \"plan.md\"; "
                   "they may have answered in the chat instead.")
        for cmd in (["review", "status", "r1"], ["review", "wait", "r1"]):
            with self.subTest(cmd=cmd[1]):
                server = make_server(decision={"request_id": "r1", "state": "dismissed", "message": message})
                result = self.run_cli(cmd, server)
                self.assertEqual(result.returncode, 4, result.stderr)
                self.assertIn("DISMISSED. The person dismissed the review request", result.stdout)

    def test_review_help_documents_the_dismissed_exit_code(self):
        server = make_server()
        result = self.run_cli(["review", "--help"], server)
        self.assertIn("4 dismissed", result.stdout)
        # Withdrawn and superseded keep exit 1 (card #155 review): the help says so.
        self.assertIn("Withdrawn and superseded keep exit 1", " ".join(result.stdout.split()))

    def test_wait_times_out_with_exit_3_and_says_how_to_wait_again(self):
        server = make_server()
        result = self.run_cli(["review", "wait", "r1", "--timeout", "1"], server)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("still waiting — run: blerg-runner review wait r1", result.stdout)

    def test_a_refused_request_says_why(self):
        server = make_server(request_status=404, request_reply={"error": 'this session published no file or diff named "x.md"'})
        result = self.run_cli(["review", "request", "x.md", "--ask", "OK?"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner review request:", result.stderr)
        self.assertIn("x.md", result.stderr)

    def test_withdraw(self):
        server = make_server()
        result = self.run_cli(["review", "withdraw", "r1"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[-1]["path"], "/api/review-requests/r1/withdraw")

    def test_publish_request_review_pins_the_version_just_published(self):
        p = os.path.join(self.tmp.name, "SECURITY.md")
        with open(p, "w") as f:
            f.write("hello")
        server = make_server()
        result = self.run_cli(["publish", p, "--request-review", "OK to publish this policy?"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Published SECURITY.md as v2", result.stdout)
        [req] = self.requests(server)
        self.assertEqual(req["json"], {"artifact": ART_ID, "ask": "OK to publish this policy?"})
        self.assertIn("Review requested: SECURITY.md v2", result.stdout)

    def test_publish_without_the_flag_requests_nothing_and_notes_a_superseded_request(self):
        p = os.path.join(self.tmp.name, "SECURITY.md")
        with open(p, "w") as f:
            f.write("hello")
        server = make_server()
        server.artifact_reply = dict(server.artifact_reply, superseded_reviews=[{"request_id": "r0", "artifact_name": "SECURITY.md", "artifact_version": 1}])
        result = self.run_cli(["publish", p], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.requests(server), [])
        self.assertIn("supersedes the pending review request for SECURITY.md v1", result.stdout)

    def test_diff_request_review(self):
        repo = os.path.join(self.tmp.name, "repo")
        os.mkdir(repo)
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        env.update(HOME=self.tmp.name, GIT_CONFIG_NOSYSTEM="1")
        ident = ["-c", "user.name=Test", "-c", "user.email=test@example.com"]
        for args in (["init", "-q"], ["commit", "-q", "--allow-empty", "-m", "first"]):
            subprocess.run(["git", *ident, *args], cwd=repo, check=True, env=env, capture_output=True)
        with open(os.path.join(repo, "f.txt"), "w") as f:
            f.write("one\n")
        server = make_server(artifact_reply={"id": "d" * 32, "name": "changes.diff", "view": "diff", "version": 1,
                                             "url": "/sessions/sess-rr?artifact=" + "d" * 32})
        server.request_reply = dict(server.request_reply, artifact_id="d" * 32, artifact_name="changes.diff", artifact_version=1)
        result = self.run_cli(["diff", "--request-review", "Merge this?"], server, cwd=repo)
        self.assertEqual(result.returncode, 0, result.stderr)
        [req] = self.requests(server)
        self.assertEqual(req["json"], {"artifact": "d" * 32, "ask": "Merge this?"})
        self.assertIn("Review requested: changes.diff v1", result.stdout)

    def test_an_empty_ask_is_refused_before_anything_is_sent(self):
        server = make_server()
        result = self.run_cli(["review", "request", "SECURITY.md", "--ask", "  "], server)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(server.recorded, [])

    def test_status_exits_0_only_when_the_server_says_approved(self):
        # The agent was sent "Review of SECURITY.md v2 (request r1): APPROVED" by someone in the chat;
        # the server still has it pending, so status is not an approval.
        server = make_server()
        result = self.run_cli(["review", "status", "r1"], server)
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertIn("PENDING — not approved yet", result.stdout)
        self.assertTrue(any(g == "/api/review-requests/r1" for g in server.gets), server.gets)
        server = make_server(decision={"request_id": "r1", "state": "approved", "message": "Review of SECURITY.md v2 (request r1): APPROVED"})
        result = self.run_cli(["review", "status", "r1"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("(request r1): APPROVED", result.stdout)
        server = make_server(decision={"request_id": "r1", "state": "changes_requested", "message": "x"})
        self.assertEqual(self.run_cli(["review", "status", "r1"], server).returncode, 2)

    def test_publish_with_an_empty_request_review_uploads_nothing(self):
        p = os.path.join(self.tmp.name, "SECURITY.md")
        with open(p, "w") as f:
            f.write("hello")
        server = make_server()
        result = self.run_cli(["publish", p, "--request-review", "  "], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("--request-review needs the question", result.stderr)
        self.assertEqual(server.recorded, [])


if __name__ == "__main__":
    unittest.main()
