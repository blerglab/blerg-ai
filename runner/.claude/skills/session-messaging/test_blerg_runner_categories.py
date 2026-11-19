"""Main's category command uses its session credential and action-based route."""
import unittest
from test_blerg_runner import run_blerg_runner, serve
from test_blerg_runner_mark import make_scripted_server

class TestCategories(unittest.TestCase):
    def test_file_channel(self):
        server = make_scripted_server([(200, {"categories": []})])
        stop = serve(server)
        try:
            result = run_blerg_runner(["category", "file", "design", "--category", "research"], env_override={
                "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                "BLERG_RUNNER_SESSION_ID": "main",
                "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
            })
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["path"], "/api/sessions/main/categories")
            self.assertEqual(server.recorded[0]["body"], {"action": "file", "session": "design", "id": "research"})
        finally:
            stop()
