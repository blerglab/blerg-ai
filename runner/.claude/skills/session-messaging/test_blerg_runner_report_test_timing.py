"""blerg-runner report test-timing: locates the Go test-timing binary, runs it against gate
history or a bare summary.json, and either publishes the markdown report or prints it with
--no-publish. Never runs tests. Missing input or a missing binary fails with a clear reason."""

import os
import unittest

from test_blerg_runner import cli_test_env, run_blerg_runner

TESTDATA = os.path.join(
    os.path.dirname(__file__), "..", "..", "..", "internal", "testtiming", "testdata"
)
REAL_BINARY = os.path.join(os.path.dirname(__file__), "..", "..", "..", "bin", "blerg-test-timing")


def configured_env(overrides=None):
    base = {
        "BLERG_RUNNER_SERVER_HTTP": "http://127.0.0.1:1",
        "BLERG_RUNNER_SESSION_ID": "sess-report-test-timing",
        "BLERG_RUNNER_SESSION_TOKEN": "tok-report-test-timing",
    }
    base.update(overrides or {})
    return cli_test_env(base)


@unittest.skipUnless(os.access(REAL_BINARY, os.X_OK), "blerg-test-timing not built (make build-test-timing)")
class TestReportTestTimingWithRealBinary(unittest.TestCase):
    def test_no_publish_prints_markdown_from_a_bare_summary(self):
        result = run_blerg_runner(
            ["report", "test-timing", "--summary", os.path.join(TESTDATA, "summary-c4489560.json"),
             "--no-publish", "--repo", "example"],
            env_override=configured_env({"BLERG_RUNNER_TESTTIMING_BIN": REAL_BINARY}),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("# Test timing report: example", result.stdout)
        self.assertIn("```vega-lite", result.stdout)

    def test_no_publish_reads_real_gate_history(self):
        result = run_blerg_runner(
            ["report", "test-timing", "--history", os.path.join(TESTDATA, "real-history.jsonl"), "--no-publish"],
            env_override=configured_env({"BLERG_RUNNER_TESTTIMING_BIN": REAL_BINARY}),
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("5 gate run(s) in window", result.stdout)

    def test_no_input_and_no_default_history_fails_clearly(self):
        empty_cwd = os.path.dirname(__file__)
        result = run_blerg_runner(
            ["report", "test-timing", "--no-publish"],
            env_override=configured_env({"BLERG_RUNNER_TESTTIMING_BIN": REAL_BINARY}),
            cwd=empty_cwd,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No gate history", result.stderr)

    def test_bad_summary_path_fails_with_the_tool_s_reason(self):
        result = run_blerg_runner(
            ["report", "test-timing", "--summary", "/no/such/summary.json", "--no-publish"],
            env_override=configured_env({"BLERG_RUNNER_TESTTIMING_BIN": REAL_BINARY}),
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Test timing tool failed", result.stderr)


class TestReportTestTimingWithoutTheBinary(unittest.TestCase):
    def test_missing_binary_fails_clearly_without_a_traceback(self):
        result = run_blerg_runner(
            ["report", "test-timing", "--summary", os.path.join(TESTDATA, "summary-c4489560.json"), "--no-publish"],
            env_override=configured_env({"BLERG_RUNNER_TESTTIMING_BIN": None, "PATH": "/nonexistent"}),
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("blerg-test-timing not found", result.stderr)
        self.assertIn("cd runner && GOWORK=off go run ./cmd/test-timing", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def test_unconfigured_session_is_a_silent_noop(self):
        result = run_blerg_runner(
            ["report", "test-timing", "--summary", os.path.join(TESTDATA, "summary-c4489560.json"), "--no-publish"],
            env_override=cli_test_env({}),
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
