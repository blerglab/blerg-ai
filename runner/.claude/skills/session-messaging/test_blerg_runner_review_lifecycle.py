"""Revision-aware review replies, including general comments and code suggestions."""

import copy
import datetime
import io
import json
import os
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest import mock
import contextlib


class TestReviewLifecycle(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with mock.patch.dict(os.environ, {
                "BLERG_RUNNER_SESSION_ID": "fixture-session",
                "BLERG_RUNNER_SESSION_TOKEN": "fixture-token",
                "BLERG_RUNNER_SERVER_HTTP": "http://example.invalid"}, clear=True):
            cls.cli = runpy.run_path(str(Path(__file__).with_name("blerg-runner")),
                                    run_name="review_lifecycle_fixture")

    def request(self, **overrides):
        return {
            "id": "request-one", "text": "Please handle the empty input", "status": "open",
            "createdAt": "2026-01-01T12:00:00Z", "revision": 1,
            "anchor": {"kind": "text", "quote": "old code", "diff": {
                "file": "src/example.ts", "side": "new", "start": 1, "end": 1,
                "hunk": 1, "fileIndex": 1, "artifactId": "source-one"}},
            "suggestion": {"original": "old code", "replacement": "new code"}, **overrides,
        }

    def review(self, request):
        return {"version": 1, "file": {"name": "changes.diff", "artifactId": "source-one"},
                "requests": [request], "submittedAt": "2026-01-02T12:00:00Z"}

    def test_reopened_request_stays_open_against_an_older_agent_reply(self):
        person = self.review(self.request(revision=2))
        agent = self.review(self.request(revision=1, status="done", reply="Earlier answer"))
        original = copy.deepcopy((person, agent))
        merged = self.cli["_merge_reviews"](person, agent)["requests"][0]
        self.assertEqual(merged["status"], "open")
        self.assertEqual(merged["revision"], 2)
        self.assertIn("Earlier answer", [item["text"] for item in merged["replies"]])
        self.assertEqual((person, agent), original)

    def test_newer_agent_revision_moves_the_merged_revision_forward(self):
        merged = self.cli["_merge_reviews"](
            self.review(self.request(revision=1)),
            self.review(self.request(revision=2, status="done", reply="New answer")))["requests"][0]
        self.assertEqual((merged["revision"], merged["status"], merged["reply"]), (2, "done", "New answer"))

    def test_same_revision_reply_wins_but_person_anchor_and_suggestion_are_preserved(self):
        person = self.review(self.request())
        mine = self.request(status="declined", reply="The existing guard covers this")
        mine["anchor"] = {"kind": "general"}
        mine["suggestion"] = {"original": "other", "replacement": "unwanted"}
        merged = self.cli["_merge_reviews"](person, self.review(mine))["requests"][0]
        self.assertEqual(merged["anchor"], person["requests"][0]["anchor"])
        self.assertEqual(merged["suggestion"], person["requests"][0]["suggestion"])
        self.assertEqual(merged["status"], "declined")

    def test_reply_history_unions_deduplicates_and_keeps_the_last_hundred(self):
        items = [{"status": "done", "text": f"Answer {i}", "createdAt": "2026-01-01T12:00:00Z", "revision": i}
                 for i in range(101)]
        person = self.review(self.request(replies=items[:100]))
        agent = self.review(self.request(replies=items[99:]))
        merged = self.cli["_merge_reviews"](person, agent)["requests"][0]
        self.assertEqual(merged["replies"], items[1:])

    def test_general_and_suggestion_descriptions_are_explicit_and_inert(self):
        self.assertEqual(self.cli["_review_where"]({"kind": "general"}), "general comment")
        request = self.request(suggestion={"original": "old code", "replacement": "$(touch marker)\n\u001b[2J"})
        with mock.patch("os.system", side_effect=AssertionError("executed a suggestion")), \
                mock.patch("subprocess.run", side_effect=AssertionError("executed a suggestion")):
            line = self.cli["_review_line"](request)
        self.assertIn("suggestion", line.lower())
        self.assertIn("new line 1", line)
        self.assertIn("$(touch marker)", line)
        self.assertNotIn("\u001b", line)

    def test_invalid_revision_and_suggestion_metadata_are_not_loaded(self):
        for bad in (True, -1, 1.5, 1_000_001, "1"):
            with self.subTest(revision=bad):
                self.assertIsNone(self.cli["_parse_review"](json.dumps(self.review(self.request(revision=bad))).encode()))
        for suggestion in ({"original": "different", "replacement": "x"},
                           {"original": "old code", "replacement": "\0"},
                           {"original": "old code", "replacement": "x" * 32769}):
            with self.subTest(suggestion=suggestion if len(str(suggestion)) < 100 else "oversize"):
                self.assertIsNone(self.cli["_parse_review"](json.dumps(self.review(self.request(suggestion=suggestion))).encode()))

    def test_invalid_reply_history_and_unanchored_suggestions_are_rejected(self):
        valid = {"status": "done", "text": "Answer", "createdAt": "2026-01-01T12:00:00Z", "revision": 0}
        for history in (None, {}, [None], [dict(valid, status="open")],
                        [dict(valid, text=3)], [dict(valid, createdAt="not a timestamp")],
                        [dict(valid, revision=True)], [valid] * 101):
            with self.subTest(history=repr(history)[:90]):
                raw = json.dumps(self.review(self.request(replies=history))).encode()
                self.assertIsNone(self.cli["_parse_review"](raw))
        for anchor in ({"kind": "general"},
                       {"kind": "text", "quote": "old code", "diff": {
                           "file": "file.ts", "side": "old", "start": 1, "end": 1, "hunk": 1, "fileIndex": 1}},
                       {"kind": "text", "quote": "old code", "diff": {
                           "file": "bad\x1b.ts", "side": "new", "start": 1, "end": 1, "hunk": 1, "fileIndex": 1}}):
            self.assertIsNone(self.cli["_parse_review"](json.dumps(self.review(self.request(anchor=anchor))).encode()))
        deletion = self.request(suggestion={"original": "old code", "replacement": ""})
        self.assertIsNotNone(self.cli["_parse_review"](json.dumps(self.review(deletion)).encode()))
        nul = self.request()
        nul["anchor"]["quote"] = "old\0code"
        nul["suggestion"]["original"] = "old\0code"
        self.assertIsNone(self.cli["_parse_review"](json.dumps(self.review(nul)).encode()))

    def test_stale_agent_cannot_overwrite_newer_person_reply(self):
        merged = self.cli["_merge_reviews"](
            self.review(self.request(revision=2, reply="Current person copy")),
            self.review(self.request(revision=1, status="done", reply="Stale agent answer")))["requests"][0]
        self.assertEqual((merged["revision"], merged["status"], merged["reply"]), (2, "open", "Current person copy"))

    def test_known_agent_status_supersedes_inferred_legacy_open_history(self):
        merged = self.cli["_merge_reviews"](
            self.review(self.request(revision=2, status="open", reply="Earlier answer")),
            self.review(self.request(revision=1, status="declined", reply="Earlier answer")))["requests"][0]
        self.assertEqual(merged["status"], "open")
        self.assertEqual(len(merged["replies"]), 1)
        self.assertEqual((merged["replies"][0]["status"], merged["replies"][0]["revision"]), ("declined", 1))

    def test_general_anchor_cannot_hide_location_metadata(self):
        for key, value in (("quote", "hidden"), ("diff", {}), ("page", 1), ("rect", {}), ("line", 1), ("occurrence", 1), ("heading", "Title"), ("pin", 1)):
            req = self.request(anchor={"kind": "general", key: value})
            req.pop("suggestion")
            with self.subTest(key=key):
                self.assertIsNone(self.cli["_parse_review"](json.dumps(self.review(req)).encode()))

    def test_reply_appends_history_without_mutating_code_or_general_metadata(self):
        for request in (self.request(reply="Earlier answer", status="done"),
                        self.request(anchor={"kind": "general"}, suggestion=None),
                        self.request(reply="Earlier answer", status="open", revision=2)):
            with self.subTest(kind=request["anchor"]["kind"]), tempfile.TemporaryDirectory() as folder:
                if request.get("suggestion") is None:
                    request.pop("suggestion", None)
                review = self.review(request)
                namespace = self.cli["cmd_review_reply"].__globals__
                published = []
                with mock.patch.dict(namespace, {
                        "REVIEW_STATE_DIR": folder,
                        "_load_reviews": lambda: {"changes.diff": copy.deepcopy(review)},
                        "cmd_publish": lambda path, name: published.append((Path(path).read_bytes(), name))}), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.cli["cmd_review_reply"]("request-one", "done", "Latest answer")
                self.assertEqual(len(published), 1)
                updated = json.loads(published[0][0])["requests"][0]
                self.assertEqual(updated["anchor"], request["anchor"])
                self.assertEqual(updated.get("suggestion"), request.get("suggestion"))
                self.assertEqual(updated["reply"], "Latest answer")
                latest = updated["replies"][-1]
                self.assertEqual((latest["status"], latest["text"], latest["revision"]), ("done", "Latest answer", request["revision"]))
                parsed = datetime.datetime.fromisoformat(latest["createdAt"].replace("Z", "+00:00"))
                self.assertEqual(parsed.utcoffset(), datetime.timedelta(0))
                if request.get("reply"):
                    self.assertIn("Earlier answer", [reply["text"] for reply in updated["replies"]])


if __name__ == "__main__":
    unittest.main()
