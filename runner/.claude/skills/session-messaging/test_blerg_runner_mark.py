"""blerg-runner mark against a stub runner: one mark posts kind, text, by, commits, card and at to
the session's marks route; --import sends a JSON-lines file in chunks the server takes; a bad line or
argument is refused before anything is sent."""

import http.server
import json
import os
import tempfile
import unittest

from test_blerg_runner import make_topic_server, run_blerg_runner, serve


class ScriptedHandler(http.server.BaseHTTPRequestHandler):
    """Answers each POST with the next (status, body) of server.script; records raw bodies."""

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b""
        self.server.raw.append(raw)
        self.server.recorded.append({"path": self.path, "body": json.loads(raw)})
        self.server.keys.append(self.headers.get("Idempotency-Key"))
        if getattr(self.server, "drop_first", False) and len(self.server.recorded) == 1:
            self.close_connection = True
            return
        status, body = self.server.script[min(len(self.server.recorded), len(self.server.script)) - 1]
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def make_scripted_server(script):
    server = http.server.HTTPServer(("127.0.0.1", 0), ScriptedHandler)
    server.recorded, server.raw, server.keys, server.script = [], [], [], script
    return server


class TestMark(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def _run(self, args, server, **extra):
        env = {
            "BLERG_RUNNER_SERVER_HTTP": f"http://127.0.0.1:{server.server_address[1]}",
            "BLERG_RUNNER_SESSION_ID": "sess-mk",
            "BLERG_RUNNER_SESSION_TOKEN": "sess-tok",
            "BLERG_RUNNER_DAEMON_TOKEN": None,
            "BLERG_RUNNER_BOARD_TOKEN": None,
            "BLERG_RUNNER_BOARD_ID": None,
        }
        env.update(extra)
        stop = serve(server)
        try:
            return run_blerg_runner(["mark"] + args, env_override=env)
        finally:
            stop()

    def _file(self, lines):
        path = os.path.join(self.tmp.name, "strokes.jsonl")
        with open(path, "w", encoding="utf-8") as f:
            f.write("\n".join(lines) + "\n")
        return path

    def test_a_decision_posts_its_fields_and_says_whose(self):
        server = make_topic_server(reply={"marks": [{"kind": "decision", "by": "person", "text": "Ship Friday"}], "recorded": 1})
        result = self._run(["decision", "Ship Friday", "--by", "person", "--commit", "abc1234,def5678", "--card", "115",
                            "--at", "2026-10-21T15:56Z"], server, BLERG_RUNNER_BOARD_ID="board-1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "Marked Decision (the person's): Ship Friday")
        req = server.recorded[0]
        self.assertEqual(req["path"], "/api/sessions/sess-mk/marks")
        self.assertEqual(req["auth"], "Bearer sess-tok")
        self.assertEqual(req["body"], {"kind": "decision", "text": "Ship Friday", "by": "person",
                                       "commits": ["abc1234", "def5678"], "card": 115, "board_id": "board-1",
                                       "at": "2026-10-21T15:56Z"})

    def test_a_milestone_without_options_sends_only_kind_and_text(self):
        server = make_topic_server(reply={"marks": [{"kind": "milestone", "by": "agent", "text": "Merged"}], "recorded": 1})
        result = self._run(["milestone", "Merged"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["body"], {"kind": "milestone", "text": "Merged"})
        self.assertEqual(result.stdout.strip(), "Marked Milestone: Merged")

    def test_text_is_cleaned_before_truncation_and_empty_invisible_text_is_refused(self):
        server = make_topic_server()
        result = self._run(["finding", "\u2060" * 300 + "Visible\u202e words"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(server.recorded[0]["body"]["text"], "Visible words")
        server = make_topic_server()
        result = self._run(["finding", "\u200b\u2060\ufeff\u202e"], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(server.recorded, [])

    def test_lost_live_mark_reply_retries_the_same_body_and_key(self):
        server = make_scripted_server([(201, {"recorded": 0, "duplicates": 1})])
        server.drop_first = True
        result = self._run(["milestone", "Merged"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(server.recorded), 2)
        self.assertEqual(server.raw[0], server.raw[1])
        self.assertTrue(server.keys[0])
        self.assertEqual(server.keys[0], server.keys[1])

    def test_bad_arguments_are_refused_before_sending(self):
        for args, words in [
            ([], "give the kind"),
            (["decision"], "give the text"),
            (["attention", "x"], "decision, milestone or finding"),
            (["finding", "  "], "give the text"),
            (["finding", "x", "--commit", "nope"], "not a commit sha"),
            (["finding", "x", "--card", "0"], "card number"),
            (["finding", "x", "--at", "yesterday"], "RFC 3339"),
        ]:
            server = make_topic_server()
            result = self._run(args, server)
            self.assertNotEqual(result.returncode, 0, args)
            self.assertIn(words, result.stderr, args)
            self.assertEqual(server.recorded, [], args)

    def test_the_server_refusal_is_said(self):
        server = make_topic_server(status=403, reply={"error": "forbidden"})
        result = self._run(["finding", "x"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("blerg-runner mark:", result.stderr)
        self.assertIn("forbidden", result.stderr)

    def test_an_older_server_records_nothing_and_says_so(self):
        server = make_topic_server(status=200, ctype="text/html", raw=b"<html></html>")
        result = self._run(["finding", "x"], server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("does not know marks yet", result.stderr)

    def test_import_sends_the_lines_in_chunks(self):
        lines = [json.dumps({"at": "2026-01-10T09:%02dZ" % (i % 60), "kind": "finding", "text": f"finding {i}"}) for i in range(250)]
        lines.insert(3, "")
        lines.append(json.dumps({"at": "2026-01-10T10:00Z", "kind": "decision", "by": "owner", "text": "Ship", "card": 7, "commit": "abc1234"}))
        server = make_topic_server(reply={"recorded": 200, "duplicates": 0})
        result = self._run(["--import", self._file(lines)], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(server.recorded), 2)
        first, second = server.recorded[0]["body"]["marks"], server.recorded[1]["body"]["marks"]
        self.assertEqual((len(first), len(second)), (200, 51))
        self.assertEqual(second[-1], {"kind": "decision", "text": "Ship", "by": "owner", "commits": ["abc1234"], "card": 7, "at": "2026-01-10T10:00Z"})
        self.assertEqual(result.stdout.strip(), "Imported 400 marks")

    def test_import_says_what_was_already_recorded(self):
        server = make_topic_server(reply={"recorded": 0, "duplicates": 1})
        result = self._run(["--import", self._file([json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "x"})])], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "Imported 0 marks (1 already recorded)")
        # A single line still goes as a one-mark body.
        self.assertEqual(server.recorded[0]["body"]["kind"], "finding")

    def test_a_bad_import_line_is_refused_naming_it(self):
        for line, words in [
            ("not json", "not a JSON object"),
            ("[1]", "not a JSON object"),
            (json.dumps({"kind": "finding", "text": "x"}), "needs its time"),
            (json.dumps({"at": "2026-01-10T09:00Z", "kind": "nope", "text": "x"}), "decision, milestone or finding"),
            (json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "x", "html": "<b>"}), "unknown field 'html'"),
            (json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "x", "card": "7"}), "card number"),
        ]:
            server = make_topic_server()
            path = self._file([json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "ok"}), line])
            result = self._run(["--import", path], server)
            self.assertNotEqual(result.returncode, 0, line)
            self.assertIn(":2: ", result.stderr, line)
            self.assertIn(words, result.stderr, line)
            self.assertEqual(server.recorded, [], line)

    def test_import_takes_no_other_arguments(self):
        server = make_topic_server()
        result = self._run(["decision", "x", "--import", self._file(["{}"])], server)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("takes no other arguments", result.stderr)
        self.assertEqual(server.recorded, [])

    def test_import_chunks_by_size_cuts_long_text_and_sends_utf8(self):
        sha = ["%07x" % (0xabc0000 + i) for i in range(10)]
        text = "日本語" + "🎉" * 400  # far over 240 characters, 4 bytes each: 200 lines pass 200 KB
        lines = [json.dumps({"at": "2026-01-10T09:%02d:00Z" % (i % 60), "kind": "finding", "text": text, "commits": sha}, ensure_ascii=False)
                 for i in range(200)]
        server = make_scripted_server([(201, {"recorded": 1, "duplicates": 0})])
        result = self._run(["--import", self._file(lines)], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertGreater(len(server.raw), 1)
        for raw in server.raw:
            self.assertLessEqual(len(raw), 200_000)
            self.assertIn("日本語".encode("utf-8"), raw)  # not \\u-escaped
        sent = [m for r in server.recorded for m in r["body"]["marks"]]
        self.assertEqual(len(sent), 200)
        self.assertTrue(all(len(m["text"]) == 240 and m["text"].endswith("…") for m in sent))
        self.assertIn("200 texts cut to 240 characters", result.stdout)

    def test_import_checks_every_line_before_sending(self):
        good = json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "ok"})
        for line, words in [
            (json.dumps({"at": "2026-02-30T10:00Z", "kind": "finding", "text": "x"}), "not a real date"),
            (json.dumps({"at": "2999-01-01T00:00:00Z", "kind": "finding", "text": "x"}), "in the future"),
            (json.dumps({"at": "1999-12-31T23:59:00Z", "kind": "finding", "text": "x"}), "before 2000"),
            (json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "x", "card": 10_000_001}), "card number from 1 to"),
            (json.dumps({"at": "2026-01-10T09:00Z", "kind": "finding", "text": "x", "card": 3, "board_id": "../x"}), "not a board id"),
        ]:
            # The bad line comes after 250 good ones: the first chunk would have gone out.
            server = make_scripted_server([(201, {"recorded": 200, "duplicates": 0})])
            result = self._run(["--import", self._file([good] * 250 + [line])], server)
            self.assertNotEqual(result.returncode, 0, line)
            self.assertIn(":251: ", result.stderr, line)
            self.assertIn(words, result.stderr, line)
            self.assertEqual(server.recorded, [], line)

    def test_a_refused_later_chunk_names_what_was_recorded(self):
        lines = [json.dumps({"at": "2026-01-10T09:%02dZ" % (i % 60), "kind": "finding", "text": f"f{i}"}) for i in range(300)]
        lines.insert(0, "")  # line numbers are the file's, blank lines included
        server = make_scripted_server([(201, {"recorded": 150, "duplicates": 50}),
                                       (400, {"error": "mark 3: something the server refused"})])
        result = self._run(["--import", self._file(lines)], server)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(len(server.recorded), 2)
        self.assertIn("strokes.jsonl:204:", result.stderr)  # the chunk's third mark, as a file line
        self.assertIn("Recorded before it: lines 2-201", result.stderr)
        self.assertIn("150 new, 50 already recorded", result.stderr)
        self.assertIn("nothing from line 202 on", result.stderr)
        self.assertIn("again is safe", result.stderr)

    def test_import_names_only_the_board_a_line_names(self):
        lines = [json.dumps({"at": "2026-01-10T09:00Z", "kind": "milestone", "text": "a", "card": 4}),
                 json.dumps({"at": "2026-01-10T09:01Z", "kind": "milestone", "text": "b", "card": 5, "board_id": "other-board"})]
        server = make_scripted_server([(201, {"recorded": 2, "duplicates": 0})])
        result = self._run(["--import", self._file(lines)], server, BLERG_RUNNER_BOARD_ID="session-board")
        self.assertEqual(result.returncode, 0, result.stderr)
        sent = server.recorded[0]["body"]["marks"]
        self.assertNotIn("board_id", sent[0])
        self.assertEqual(sent[1]["board_id"], "other-board")


if __name__ == "__main__":
    unittest.main()
