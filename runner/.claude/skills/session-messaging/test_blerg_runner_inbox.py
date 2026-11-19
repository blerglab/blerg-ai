import pathlib
import tempfile
import unittest
from test_blerg_runner import run_blerg_runner, serve
from test_blerg_runner_mark import make_scripted_server

class TestInbox(unittest.TestCase):
    def test_post_body_file(self):
        server = make_scripted_server([(201, {"id": "item-1"})])
        stop = serve(server)
        try:
            with tempfile.TemporaryDirectory() as folder:
                body = pathlib.Path(folder) / "summary.md"
                body.write_text("## Ready\n\nThe release is ready.", encoding="utf-8")
                result = run_blerg_runner(["inbox", "post", "--subject", "Morning summary", "--body-file", str(body), "--kind", "summary"], env_override={
                    "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                    "BLERG_RUNNER_SESSION_ID": "main", "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
                })
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(server.recorded[0]["path"], "/api/sessions/main/inbox")
                self.assertRegex(server.recorded[0]["body"].pop("client_id"), r"^[0-9a-f]{32}$")
                self.assertEqual(server.recorded[0]["body"], {"subject": "Morning summary", "body": body.read_text(), "kind": "summary"})
        finally:
            stop()

    def test_unreadable_body_file_is_refused(self):
        result = run_blerg_runner(["inbox", "post", "--subject", "Summary", "--body-file", "/missing-inbox-fixture.md"], env_override={"BLERG_RUNNER_SERVER_HTTP":"http://example.invalid", "BLERG_RUNNER_SESSION_ID":"main", "BLERG_RUNNER_SESSION_TOKEN":"fixture-token"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cannot read UTF-8 body file", result.stderr)

    def test_worker_refusal_is_reported(self):
        server = make_scripted_server([(403, {"error": "Only Main can post. Workers must go through Main."})])
        stop = serve(server)
        try:
            with tempfile.TemporaryDirectory() as folder:
                body = pathlib.Path(folder) / "summary.md"
                body.write_text("Ready.", encoding="utf-8")
                result = run_blerg_runner(["inbox", "post", "--subject", "Summary", "--body-file", str(body)], env_override={
                    "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                    "BLERG_RUNNER_SESSION_ID": "worker", "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
                })
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("Inbox item posted", result.stdout)
        finally:
            stop()

    def test_utf8_boundary_and_one_client_id_per_invocation(self):
        server = make_scripted_server([(201, {"id":"one"}),(201,{"id":"two"})])
        stop=serve(server)
        try:
            with tempfile.TemporaryDirectory() as folder:
                body=pathlib.Path(folder)/"unicode.md"
                body.write_text("é" * 131072, encoding="utf-8")
                for _ in range(2):
                    result=run_blerg_runner(["inbox","post","--subject","Unicode","--body-file",str(body)], env_override={"BLERG_RUNNER_SERVER_HTTP":f"http://127.0.0.1:{server.server_address[1]}","BLERG_RUNNER_SESSION_ID":"main","BLERG_RUNNER_SESSION_TOKEN":"fixture-token"})
                    self.assertEqual(result.returncode,0,result.stderr)
                ids=[call["body"]["client_id"] for call in server.recorded]
                self.assertEqual(len(set(ids)),2)
                for raw in server.raw:
                    self.assertLess(len(raw),270<<10)
                    self.assertIn("é".encode("utf-8"),raw)
        finally:
            stop()
