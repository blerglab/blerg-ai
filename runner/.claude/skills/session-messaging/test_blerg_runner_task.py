"""Task acknowledgement uses only the current session's route."""
import unittest
from test_blerg_runner import run_blerg_runner, serve
from test_blerg_runner_mark import make_scripted_server

class TestTask(unittest.TestCase):
    def test_clear(self):
        server = make_scripted_server([(200, {"task_id": "task"})])
        stop = serve(server)
        try:
            result = run_blerg_runner(["task", "clear", "task"], env_override={
                "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                "BLERG_RUNNER_SESSION_ID": "main",
                "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
            })
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["path"], "/api/sessions/main/tasks/task/clear")
            self.assertEqual(server.recorded[0]["body"], {})
        finally:
            stop()
