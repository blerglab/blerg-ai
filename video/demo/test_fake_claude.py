"""The scripted engine speaks the daemon's streaming protocol and runs a scenario's turn.

    python3 -m unittest discover -s video/demo -p 'test_*.py'  (and -s video/build)
"""
import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
try:
    import yaml  # noqa: F401  (the engine reads its scenarios with PyYAML)
except ImportError:
    raise unittest.SkipTest("PyYAML missing")


class FakeClaudeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = self.tmp.name
        os.makedirs(os.path.join(root, "shorts", "s"))
        with open(os.path.join(root, "shorts", "s", "agent.yaml"), "w") as f:
            f.write(textwrap.dedent("""\
                pace: 0
                turns:
                  - id: one
                    match: "hello"
                    steps:
                      - say: Hi there.
                      - run: echo ran
                      - write: {path: out.txt, from: body.txt}
                  - id: two
                    after: one
                    match: "again"
                    steps:
                      - say: Second.
                """))
        with open(os.path.join(root, "shorts", "s", "body.txt"), "w") as f:
            f.write("content\n")
        self.work = os.path.join(root, "work")
        os.makedirs(self.work)
        self.env = dict(os.environ, BLERG_DEMO_FAKE_STATE=os.path.join(root, "state"), BLERG_RUNNER_SESSION_ID="sess-1")
        self.scen = os.path.join(root, "shorts")

    def tearDown(self):
        self.tmp.cleanup()

    def converse(self, *texts, extra=()):
        lines = "".join(json.dumps({"type": "user", "uuid": f"u{i}", "message": {"role": "user", "content": t}}) + "\n"
                        for i, t in enumerate(texts))
        p = subprocess.run([sys.executable, os.path.join(HERE, "fake_claude.py"), "--scenarios", self.scen,
                            "-p", "--input-format", "stream-json", "--output-format", "stream-json", *extra],
                           input=lines, capture_output=True, text=True, cwd=self.work, env=self.env, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        return [json.loads(x) for x in p.stdout.splitlines() if x.strip()]

    def test_turn_runs_its_steps_and_ends_with_a_result(self):
        out = self.converse("hello")
        self.assertEqual(out[0]["subtype"], "init")
        self.assertTrue(out[1]["isReplay"])
        self.assertEqual(out[1]["uuid"], "u0")
        blocks = [b for o in out if o["type"] == "assistant" for b in o["message"]["content"]]
        self.assertEqual(blocks[0], {"type": "text", "text": "Hi there."})
        self.assertEqual([b["name"] for b in blocks if b["type"] == "tool_use"], ["Bash", "Write"])
        results = [b for o in out if o["type"] == "user" and not o.get("isReplay") for b in o["message"]["content"]]
        self.assertEqual(results[0]["content"], "ran")
        self.assertEqual(out[-1]["type"], "result")
        with open(os.path.join(self.work, "out.txt")) as f:
            self.assertEqual(f.read(), "content\n")

    def test_after_orders_turns_and_unknown_messages_get_a_line(self):
        out = self.converse("again", "hello", "again")
        texts = [b["text"] for o in out if o["type"] == "assistant" for b in o["message"]["content"] if b["type"] == "text"]
        self.assertIn("nothing is scripted", texts[0])
        self.assertEqual(texts[1:], ["Hi there.", "Second."])
        self.assertEqual(sum(1 for o in out if o["type"] == "result"), 3)

    def test_the_active_short_and_the_seed_scenario_are_the_only_ones_loaded(self):
        root = self.tmp.name
        os.makedirs(os.path.join(self.scen, "other"))
        with open(os.path.join(self.scen, "other", "agent.yaml"), "w") as f:
            f.write("pace: 0\nturns:\n  - id: x\n    match: hello\n    steps:\n      - say: From the other short.\n")
        seed = os.path.join(root, "seed.yaml")
        with open(seed, "w") as f:
            f.write("pace: 0\nturns:\n  - id: s\n    match: seeded\n    steps:\n      - say: Seeded answer.\n")
        active = os.path.join(root, "active")
        with open(active, "w") as f:
            f.write("s\n")
        out = self.converse("hello", "seeded", extra=("--seed-scenario", seed, "--active-file", active))
        texts = [b["text"] for o in out if o["type"] == "assistant" for b in o["message"]["content"] if b["type"] == "text"]
        self.assertEqual(texts, ["Hi there.", "Seeded answer."])
        # Without an active short every short loads, and "other" (sorted first) answers.
        os.remove(active)
        self.env["BLERG_RUNNER_SESSION_ID"] = "sess-2"
        out = self.converse("hello", extra=("--active-file", active))
        texts = [b["text"] for o in out if o["type"] == "assistant" for b in o["message"]["content"] if b["type"] == "text"]
        self.assertEqual(texts[0], "From the other short.")

    def test_control_requests_are_acknowledged(self):
        p = subprocess.run([sys.executable, os.path.join(HERE, "fake_claude.py"), "--scenarios", self.scen,
                            "-p", "--input-format", "stream-json"],
                           input=json.dumps({"type": "control_request", "request_id": "r1", "request": {"subtype": "interrupt"}}) + "\n",
                           capture_output=True, text=True, env=self.env, timeout=30)
        out = json.loads(p.stdout.splitlines()[0])
        self.assertEqual(out["response"]["request_id"], "r1")

    def test_help_and_version_answer_like_the_cli(self):
        for flag in ("--help", "--version"):
            p = subprocess.run([sys.executable, os.path.join(HERE, "fake_claude.py"), "--scenarios", self.scen, flag],
                               capture_output=True, text=True, timeout=30)
            self.assertEqual(p.returncode, 0)
            self.assertTrue(p.stdout.strip())


if __name__ == "__main__":
    unittest.main()
