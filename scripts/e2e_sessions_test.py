import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock

from e2e_sessions import BAD_LOG, Run, snapshot


class SessionHarnessTests(unittest.TestCase):
    def test_checkout_snapshot_includes_index_tree_and_ignored_files(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            def git(*args):
                subprocess.run(["git", "-C", directory, *args], check=True, capture_output=True)
            git("init", "-b", "main")
            (repo / "file").write_text("base")
            (repo / ".gitignore").write_text("ignored\n")
            git("add", ".")
            git("-c", "user.name=Example", "-c", "user.email=developer@example.com", "commit", "-qm", "base")
            before = snapshot(repo)
            (repo / "ignored").write_text("new")
            self.assertNotEqual(before, snapshot(repo))
            (repo / "ignored").unlink()
            self.assertEqual(before, snapshot(repo))
            git("checkout", "-b", "other")
            self.assertNotEqual(before, snapshot(repo))

    def test_log_diagnostics(self):
        for text in ("writer freeze timed out", "admission closed", "app-server could not start", "broken pipe", "not recovered"):
            self.assertRegex(text, BAD_LOG)
        self.assertIsNone(BAD_LOG.search("turn synchronized"))

    def test_events_page_without_losing_turn_end(self):
        run = Run.__new__(Run)
        run.seq = {}
        run.api = Mock(side_effect=[(200, {"events": [{"seq": 1, "kind": "assistant_text"}], "has_more": True}),
                                    (200, {"events": [{"seq": 2, "kind": "turn_done"}], "has_more": False})])
        self.assertEqual([e["kind"] for e in run.events("session")], ["assistant_text", "turn_done"])
        self.assertEqual(run.seq["session"], 2)
        self.assertIn("after_seq=1", run.api.call_args.args[1])

    def test_engine_error_is_not_a_successful_turn(self):
        run = Run.__new__(Run)
        run.events = Mock(return_value=[{"kind": "error"}, {"kind": "turn_done"}])
        run.until = lambda _, fn: fn()
        with self.assertRaisesRegex(AssertionError, "engine error"):
            run.turn("session")

    def test_failed_scenario_still_has_summary_row(self):
        run = Run.__new__(Run)
        run.rows = []
        with self.assertRaises(ValueError):
            with run.scenario("example"):
                raise ValueError("failed")
        self.assertEqual(run.rows[0][:2], ["example", "FAIL"])

    def test_saved_logs_redact_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            run = Run.__new__(Run)
            run.out = Path(directory)
            run.token, run.cookie = "test-agent-secret", "test-browser-secret"
            run.save("response.json", {"body": run.token + run.cookie})
            self.assertEqual(json.loads((run.out / "response.json").read_text())["body"], "[redacted][redacted]")


if __name__ == "__main__":
    unittest.main()
