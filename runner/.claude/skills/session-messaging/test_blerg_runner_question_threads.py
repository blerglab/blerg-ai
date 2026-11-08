import unittest
import json
from test_blerg_runner import run_blerg_runner, serve
from test_blerg_runner_mark import make_scripted_server

class TestQuestionThreads(unittest.TestCase):
    def test_reply_and_revise(self):
        for flag, field in [("--reply", "reply"), ("--revise", "revise")]:
            with self.subTest(flag=flag):
                server = make_scripted_server([(200, {"seq": 1})])
                stop = serve(server)
                try:
                    result = run_blerg_runner(["ask", flag, "question-id", "Use staging?"], env_override={
                        "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                        "BLERG_RUNNER_SESSION_ID": "main", "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
                    })
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(server.recorded[0]["path"], "/api/messages/question-id/thread")
                    self.assertEqual(server.recorded[0]["body"][field], "Use staging?")
                    self.assertIn("client_id", server.recorded[0]["body"])
                finally:
                    stop()

    def test_blocking_ask_yields_discussion_without_answering(self):
        server = make_scripted_server([(201, {"id": "question-id"}), (200, {"answered": False, "outcome": "discussion", "discussion": "What does this delete?"})])
        class ThreadHandler(server.RequestHandlerClass):
            def do_GET(self):
                self.server.recorded.append({"path": self.path, "body": {}})
                code, body = self.server.script[len(self.server.recorded) - 1]
                data = json.dumps(body).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
        server.RequestHandlerClass = ThreadHandler
        stop = serve(server)
        try:
            result = run_blerg_runner(["ask", "Deploy to staging?"], env_override={
                "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                "BLERG_RUNNER_SESSION_ID": "main", "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
            })
            self.assertEqual(result.returncode, 5, result.stderr)
            self.assertIn("ask --reply question-id", result.stdout)
            self.assertIn("next message", result.stdout)
            self.assertNotIn("What does this delete?", result.stdout + result.stderr)
            self.assertEqual(len(server.recorded), 2)
            self.assertIn("waiter=cli",server.recorded[1]["path"])
        finally:
            stop()
