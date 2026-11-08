"""The command recommends; it never launches a channel."""
import unittest
from test_blerg_runner import run_blerg_runner, serve
from test_blerg_runner_mark import make_scripted_server

class TestSideChannel(unittest.TestCase):
    def test_recommend(self):
        server = make_scripted_server([(201, {"state": "pending"})])
        stop = serve(server)
        try:
            result = run_blerg_runner(["side-channel", "TDD skill", "--from", "12", "--to", "16", "--context", "Develop a reusable TDD skill."], env_override={
                "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
                "BLERG_RUNNER_SESSION_ID": "main", "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
            })
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(server.recorded[0]["path"], "/api/sessions/main/channel-recommendations")
            self.assertEqual(server.recorded[0]["body"], {"topic": "TDD skill", "from_seq": 12, "to_seq": 16, "context": "Develop a reusable TDD skill."})
        finally:
            stop()
