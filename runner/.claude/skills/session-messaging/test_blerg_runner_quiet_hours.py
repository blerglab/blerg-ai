"""Urgency is an explicit ask-only delivery hint, independent of blocking style."""
import unittest
import test_blerg_runner as helpers

class QuietHoursCLI(unittest.TestCase):
    def test_urgent_no_wait_ask_carries_explicit_urgency(self):
        result, recorded = helpers.TestAskNoWait()._run(["ask", "--urgent", "--no-wait", "Release expires soon"], 1)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(recorded[0]["body"].get("urgent", False))
        self.assertTrue(recorded[0]["body"]["no_wait"])

    def test_urgent_blocking_ask_carries_explicit_urgency(self):
        server = helpers.make_stub_server(answer_after_n_calls=1)
        stop = helpers.serve(server)
        self.addCleanup(stop)
        result = helpers.run_blerg_runner(["ask", "--urgent", "Release expires soon"], env_override={
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-urgent",
            "BLERG_RUNNER_DAEMON_TOKEN": "test-urgent-token",
        })
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(server.recorded[0]["body"].get("urgent", False))
        self.assertNotIn("no_wait", server.recorded[0]["body"])
