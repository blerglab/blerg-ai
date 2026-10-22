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

    def test_an_agent_revision_above_the_persons_is_ignored(self):
        # Only the person raises a thread's revision: a higher one in the agent's copy is not theirs.
        merged = self.cli["_merge_reviews"](
            self.review(self.request(revision=1)),
            self.review(self.request(revision=2, status="done", reply="New answer")))["requests"][0]
        self.assertEqual((merged["revision"], merged["status"]), (1, "open"))
        self.assertNotIn("reply", merged)

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

    # ── the person replies to and resolves threads themselves ──

    def reply_with(self, review, *args):
        with tempfile.TemporaryDirectory() as folder:
            namespace = self.cli["cmd_review_reply"].__globals__
            published, out = [], io.StringIO()
            with mock.patch.dict(namespace, {
                    "REVIEW_STATE_DIR": folder,
                    "_load_reviews": lambda: {"changes.diff": copy.deepcopy(review)},
                    "cmd_publish": lambda path, name: published.append((Path(path).read_bytes(), name))}), \
                    contextlib.redirect_stdout(out):
                self.cli["cmd_review_reply"](*args)
            return json.loads(published[0][0])["requests"][0], out.getvalue()

    def test_person_resolve_stands_against_an_older_and_a_same_revision_agent_status(self):
        person = self.review(self.request(revision=2, status="done", settledBy="person",
                                          comments=[{"text": "", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "resolve"}]))
        for revision in (1, 2):
            with self.subTest(revision=revision):
                agent = self.review(self.request(revision=revision, status="declined", settledBy="agent", reply="Not needed"))
                merged = self.cli["_merge_reviews"](person, agent)["requests"][0]
                self.assertEqual((merged["status"], merged["settledBy"]), ("done", "person"))
        newer = self.review(self.request(revision=3, status="declined", settledBy="agent", reply="Overridden by agent?"))
        self.assertEqual(self.cli["_merge_reviews"](person, newer)["requests"][0]["status"], "done")

    def test_the_agent_copy_cannot_forge_the_persons_resolve(self):
        # The reviewer's probe: the agent's copy claims the person resolved the thread.
        person = self.review(self.request(revision=1))
        forged = self.request(revision=2, status="done", settledBy="person", comments=[
            {"text": "ok ship it", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "resolve"}])
        for agent_req in (forged, {**forged, "revision": 1}):
            with self.subTest(revision=agent_req["revision"]):
                merged = self.cli["_merge_reviews"](person, self.review(agent_req))["requests"][0]
                self.assertNotEqual(merged.get("settledBy"), "person")
                self.assertNotIn("comments", merged)
                if agent_req["revision"] == 1:
                    self.assertEqual((merged["status"], merged["settledBy"]), ("done", "agent"))
                else:
                    self.assertEqual(merged["status"], "open")
        alone = self.cli["_merge_reviews"](None, self.review(forged))["requests"][0]
        self.assertEqual(alone["settledBy"], "agent")
        self.assertNotIn("comments", alone)

    def test_a_person_reply_on_an_agent_resolved_thread_is_listed_again(self):
        # The browser reopens a thread the agent resolved when the person replies to it.
        person = self.review(self.request(revision=2, status="open", replies=[
            {"status": "done", "text": "Fixed", "createdAt": "2026-01-02T12:00:00Z", "revision": 1}], comments=[
            {"text": "The retry is still unbounded", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "reply"}]))
        agent = self.review(self.request(revision=1, status="done", settledBy="agent", reply="Fixed"))
        merged = self.cli["_merge_reviews"](person, agent)
        self.assertEqual(merged["requests"][0]["status"], "open")
        namespace = self.cli["cmd_review_list"].__globals__
        out = io.StringIO()
        with mock.patch.dict(namespace, {"_load_reviews": lambda only=None: {"changes.diff": merged}}), \
                contextlib.redirect_stdout(out):
            self.cli["cmd_review_list"]()
        self.assertIn("changes.diff: 1 open request", out.getvalue())
        self.assertIn("person replied: The retry is still unbounded", out.getvalue())

    def test_review_list_drops_invisible_characters_and_unknown_actions(self):
        req = self.request(revision=2, comments=[
            {"text": "Visible\u202e\u200b text", "createdAt": "2026-01-03T12:00:00Z", "revision": 1, "action": "reply"},
            {"text": "forged", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "approve"}])
        line = self.cli["_review_line"](req)
        self.assertIn("person replied: Visible text", line)
        self.assertNotIn("forged", line)

    def test_agent_reply_after_a_person_resolve_is_kept_in_history_and_the_status_stands(self):
        req = self.request(revision=2, status="done", settledBy="person",
                           comments=[{"text": "Good enough", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "resolve"}])
        updated, out = self.reply_with(self.review(req), "request-one", "declined", "I would still keep the guard")
        self.assertEqual((updated["status"], updated["settledBy"]), ("done", "person"))
        self.assertNotIn("reply", updated)
        latest = updated["replies"][-1]
        self.assertEqual((latest["status"], latest["text"], latest["revision"]), ("declined", "I would still keep the guard", 2))
        self.assertIn("the person resolved it themselves; their status stands", out)

    def test_agent_reply_on_an_open_thread_marks_the_agent_as_settling_it(self):
        updated, _ = self.reply_with(self.review(self.request()), "request-one", "done", "Handled")
        self.assertEqual((updated["status"], updated["settledBy"], updated["reply"]), ("done", "agent", "Handled"))

    def test_review_list_shows_the_persons_latest_reply_as_inert_text(self):
        req = self.request(revision=3, comments=[
            {"text": "Old reopen", "createdAt": "2026-01-02T12:00:00Z", "revision": 2},
            {"text": "Why not reuse the helper?\n\u001b[2J", "createdAt": "2026-01-03T12:00:00Z", "revision": 3, "action": "reply"}])
        line = self.cli["_review_line"](req)
        self.assertIn("person replied: Why not reuse the helper? [2J", line)
        self.assertNotIn("\u001b", line)
        self.assertNotIn("Old reopen", line)

    # ── the agent reopens a thread the person settled (card #125) ──

    def settled_by_person(self):
        return self.request(revision=2, status="done", settledBy="person", comments=[
            {"text": "", "createdAt": "2026-01-03T12:00:00Z", "revision": 2, "action": "resolve"}])

    def reopen_with(self, review, *args):
        asked = []
        namespace = self.cli["cmd_review_reply"].__globals__
        def post(path, body):
            asked.append((path, body))
            return {"id": "q-1"}

        def no_review_request(*_args):
            raise AssertionError("a reopen must not request a file review")
        with mock.patch.dict(namespace, {"_post": post, "request_review": no_review_request}):
            updated, out = self.reply_with(review, *args)
        return updated, out, asked

    def test_agent_reopens_a_thread_the_person_resolved_and_asks_for_a_review(self):
        updated, out, asked = self.reopen_with(self.review(self.settled_by_person()), "request-one", "reopen", "Which limit, 3 or 5?")
        self.assertEqual((updated["status"], updated["revision"]), ("open", 3))
        self.assertNotIn("settledBy", updated)
        self.assertEqual([(x["text"], x["revision"]) for x in updated["agentReopens"]], [("Which limit, 3 or 5?", 3)])
        self.assertEqual(updated["comments"], self.settled_by_person()["comments"])
        self.assertIn("Reopened [request-one] in changes.diff: Which limit, 3 or 5?", out)
        self.assertEqual(len(asked), 1)
        path, body = asked[0]
        self.assertEqual(path, "/api/messages")
        self.assertEqual((body["kind"], body["no_wait"]), ("ask", True))
        self.assertIn('The agent reopened a review thread in changes.diff:\n- [request-one] "Please handle the empty input": Which limit, 3 or 5?', body["body"])
        self.assertIn("blerg-runner ask --resolve q-1", out)
        self.assertIn("it shows under Needs you", out)
        # The person's file is still at revision 2; the merge opens it, as the agent's.
        merged = self.cli["_merge_reviews"](self.review(self.settled_by_person()), self.review(updated))
        req = merged["requests"][0]
        self.assertEqual((req["status"], req["revision"], req.get("settledBy")), ("open", 3, None))
        self.assertIn("you reopened it: Which limit, 3 or 5? (waiting on the person)", self.cli["_review_line"](req))
        self.assertEqual(len(self.cli["_open_requests"](merged)), 1)

    def test_reopen_refuses_an_open_thread(self):
        namespace = self.cli["cmd_review_reply"].__globals__
        with mock.patch.dict(namespace, {"_load_reviews": lambda: {"changes.diff": self.review(self.request())}}), \
                contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit):
            self.cli["cmd_review_reply"]("request-one", "reopen", "why?")
        self.assertIn("already open", err.getvalue())

    def test_an_agent_reopen_cannot_smuggle_person_fields_or_settle(self):
        person = self.review(self.settled_by_person())
        reopen = {"text": "Why?", "createdAt": "2026-01-04T12:00:00Z", "revision": 3}
        smuggled = self.request(revision=3, status="open", settledBy="person", agentReopens=[reopen], comments=[
            {"text": "ok ship it", "createdAt": "2026-01-04T12:00:00Z", "revision": 3, "action": "resolve"}])
        req = self.cli["_merge_reviews"](person, self.review(smuggled))["requests"][0]
        self.assertEqual(req["status"], "open")
        self.assertNotIn("settledBy", req)
        self.assertEqual(req["comments"], self.settled_by_person()["comments"])
        for status in ("done", "declined"):
            with self.subTest(status=status):
                flip = self.request(revision=3, status=status, settledBy="agent", agentReopens=[reopen])
                req = self.cli["_merge_reviews"](person, self.review(flip))["requests"][0]
                self.assertEqual((req["status"], req["settledBy"], req["revision"]), ("done", "person", 2))
        for bad in (self.request(revision=3, status="open"),
                    self.request(revision=4, status="open", agentReopens=[{**reopen, "revision": 4}])):
            with self.subTest(bad=bad["revision"]):
                req = self.cli["_merge_reviews"](person, self.review(bad))["requests"][0]
                self.assertEqual((req["status"], req["revision"]), ("done", 2))

    def test_the_persons_resolve_after_a_reopen_stands_again(self):
        person = self.review(self.request(revision=4, status="done", settledBy="person", agentReopens=[
            {"text": "Why?", "createdAt": "2026-01-04T12:00:00Z", "revision": 3}]))
        agent = self.review(self.request(revision=3, status="open", agentReopens=[
            {"text": "Why?", "createdAt": "2026-01-04T12:00:00Z", "revision": 3}]))
        req = self.cli["_merge_reviews"](person, agent)["requests"][0]
        self.assertEqual((req["status"], req["settledBy"], req["revision"]), ("done", "person", 4))
        self.assertEqual(len(req["agentReopens"]), 1)

    # ── #125 fix round 1 ──

    def test_shared_vectors(self):
        root = Path(__file__).resolve().parents[4]
        vectors = json.loads((root / "packages/chat/src/model/testdata/agent-reopen-vectors.json").read_text(encoding="utf-8"))
        for v in vectors["text"]:
            with self.subTest(text=v["text"][:20], repeat=v.get("repeat")):
                self.assertEqual(self.cli["_valid_reopen_text"](v["text"] * v.get("repeat", 1)), v["valid"])
        for v in vectors["revision"]:
            with self.subTest(revision=v["json"]):
                self.assertEqual(self.cli["_valid_revision"](json.loads(v["json"])), v["valid"])
        for v in vectors["applies"]:
            with self.subTest(case=v["name"]):
                def parsed(fields):
                    req = self.request(**fields)
                    req.pop("suggestion")
                    review = self.cli["_parse_review"](json.dumps(self.review(req)).encode())
                    return review["requests"][0] if review else None
                person, agent = parsed(v["person"]), parsed(v["agent"])
                self.assertIsNotNone(person)
                self.assertEqual(bool(agent) and self.cli["_agent_reopen_applies"](person, agent), v["applies"])
        for v in vectors["parse"]:
            with self.subTest(parse=v["name"]):
                req = self.request(**v["request"])
                req.pop("suggestion")
                self.assertEqual(self.cli["_parse_review"](json.dumps(self.review(req)).encode()) is not None, v["parses"])

    def test_i1_an_unrelated_submit_does_not_let_the_agent_settle_a_thread_it_reopened(self):
        # The reviewer's sequence: person rejects A, agent reopens A, person submits on B, agent answers done on A.
        rejected = self.request(id="A", revision=1, status="declined", settledBy="person", comments=[
            {"text": "", "createdAt": "2026-01-03T12:00:00Z", "revision": 1, "action": "reject"}])
        b = self.request(id="B")
        reopened_a, _out, _asked = self.reopen_with(self.review(rejected), "A", "reopen", "Why reject? It is cheap.")
        self.assertEqual((reopened_a["status"], reopened_a["revision"]), ("open", 2))
        merged = self.cli["_merge_reviews"]({**self.review(rejected), "requests": [rejected, b]},
                                            {**self.review(reopened_a), "requests": [reopened_a, b]})
        carried = merged["requests"][0]  # the person's Submit on B carries A forward, untouched
        self.assertEqual((carried["status"], carried["revision"]), ("open", 2))
        person2 = {**self.review(carried), "requests": [carried, {**b, "revision": 1, "comments": [
            {"text": "Use the logger", "createdAt": "2026-01-04T12:00:00Z", "revision": 1, "action": "reply"}]}]}
        # The agent answers done on A: its own CLI keeps A open (it is waiting on the person).
        answered, out = self.reply_with({**self.review(carried), "requests": [carried]}, "A", "done", "did it anyway")
        self.assertEqual(answered["status"], "open")
        self.assertIn("it stays open", out)
        # Even a hand-made copy saying done at that revision does not settle it over the person's carry-forward.
        forced = {**carried, "status": "done", "settledBy": "agent", "reply": "did it anyway"}
        a = self.cli["_merge_reviews"](person2, {**self.review(forced), "requests": [forced, b]})["requests"][0]
        self.assertEqual(a["status"], "open")
        self.assertNotEqual(a.get("settledBy"), "agent")
        # Once the person acts on A (a reply above the reopen), the agent's answers count again.
        acted = {**carried, "revision": 3, "comments": [*carried["comments"],
                 {"text": "Fine, add it", "createdAt": "2026-01-05T12:00:00Z", "revision": 3, "action": "reply"}]}
        done = {**acted, "status": "done", "settledBy": "agent", "reply": "Added"}
        a = self.cli["_merge_reviews"]({**self.review(acted), "requests": [acted]}, {**self.review(done), "requests": [done]})["requests"][0]
        self.assertEqual((a["status"], a["settledBy"]), ("done", "agent"))

    def test_i2_a_bad_reopen_entry_is_left_out_and_the_cli_refuses_to_write_one(self):
        req = self.request(revision=2, status="open", agentReopens=[
            {"text": "\ufeff", "createdAt": "x", "revision": 2}, {"text": "x" * 2001, "createdAt": "x", "revision": 2},
            {"text": "ok", "createdAt": "x", "revision": 2.5}, {"text": "Why?", "createdAt": "2026-01-04T12:00:00Z", "revision": 2}])
        parsed = self.cli["_parse_review"](json.dumps(self.review(req)).encode())
        self.assertIsNotNone(parsed)
        # Entries at a valid revision are kept without their reason; the fractional one is left out.
        self.assertEqual([x["text"] for x in parsed["requests"][0]["agentReopens"]], ["", "", "Why?"])
        namespace = self.cli["cmd_review_reply"].__globals__
        for reason in ("\ufeff\u200b", "x" * 2001):
            with self.subTest(reason=reason[:5]), mock.patch.dict(namespace, {"_load_reviews": lambda: {"changes.diff": self.review(self.settled_by_person())}}), \
                    contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit):
                self.cli["cmd_review_reply"]("request-one", "reopen", reason)
            self.assertIn("the reason must say something", err.getvalue())

    def test_m1_only_the_applied_reopen_is_kept_from_the_agent_copy(self):
        person = self.review(self.settled_by_person())  # revision 2
        fake = [{"text": "old", "createdAt": "2030-01-01T00:00:00Z", "revision": 1}, {"text": "planted", "createdAt": "x", "revision": 2}]
        same = self.cli["_merge_reviews"](person, self.review(self.request(revision=2, status="done", agentReopens=fake)))["requests"][0]
        self.assertNotIn("agentReopens", same)
        real = {"text": "Why?", "createdAt": "2026-01-04T12:00:00Z", "revision": 3}
        applied = self.cli["_merge_reviews"](person, self.review(self.request(revision=3, status="open", agentReopens=[*fake, real])))["requests"][0]
        self.assertEqual(applied["agentReopens"], [real])

    def test_m4_one_reopen_per_person_action_and_a_done_in_the_same_call_keeps_it_open(self):
        reopened, _out, _asked = self.reopen_with(self.review(self.settled_by_person()), "request-one", "reopen", "Why?")
        namespace = self.cli["cmd_review_reply"].__globals__
        with mock.patch.dict(namespace, {"_load_reviews": lambda: {"changes.diff": self.review(reopened)}}), \
                contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit):
            self.cli["cmd_review_reply"]("request-one", "reopen", "Again?")
        self.assertIn("already open", err.getvalue())
        # Reopen then done in one call on an agent-settled thread: it stays open (waiting), so the
        # question is still asked once; with an agent-settled thread the person never acted on, the
        # answer is kept as history.
        agent_settled = self.request(revision=1, status="done", settledBy="agent", reply="Fixed")
        updated, _out, asked = self.reopen_with(self.review(agent_settled), "request-one", "reopen", "Wait, which file?",
                                                ["request-one", "done", "Never mind"])
        self.assertEqual(updated["status"], "open")
        self.assertEqual(len(asked), 1)

    def test_m5_reopen_refuses_at_the_revision_cap(self):
        capped = self.request(revision=1000000, status="done", settledBy="person")
        namespace = self.cli["cmd_review_reply"].__globals__
        with mock.patch.dict(namespace, {"_load_reviews": lambda: {"changes.diff": self.review(capped)}}), \
                contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit):
            self.cli["cmd_review_reply"]("request-one", "reopen", "Why?")
        self.assertIn("revision limit", err.getvalue())

    # ── #125 fix round 2 ──

    def test_r1_a_reopen_whose_reason_cannot_be_shown_still_keeps_the_thread_waiting(self):
        carried = self.request(revision=2, status="open", comments=[
            {"text": "", "createdAt": "2026-01-03T12:00:00Z", "revision": 1, "action": "reject"}],
            agentReopens=[{"text": "\u0890", "createdAt": "2026-01-04T12:00:00Z", "revision": 2}])
        parsed = self.cli["_parse_review"](json.dumps(self.review(carried)).encode())
        self.assertEqual(parsed["requests"][0]["agentReopens"], [{"text": "", "createdAt": "2026-01-04T12:00:00Z", "revision": 2}])
        done = {**carried, "status": "done", "settledBy": "agent", "reply": "did it anyway"}
        merged = self.cli["_merge_reviews"](parsed, self.review(done))["requests"][0]
        self.assertEqual(merged["status"], "open")
        # And the CLI refuses to write such a reason (Cn and Cf count as invisible).
        namespace = self.cli["cmd_review_reply"].__globals__
        for reason in ("\u0890", "\U00013439", "\u0378"):
            with self.subTest(reason=reason), mock.patch.dict(namespace, {"_load_reviews": lambda: {"changes.diff": self.review(self.settled_by_person())}}), \
                    contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit):
                self.cli["cmd_review_reply"]("request-one", "reopen", reason)
            self.assertIn("the reason must say something", err.getvalue())

    def test_r2_one_record_from_an_applied_reopen(self):
        person = self.review(self.settled_by_person())
        many = [{"text": f"spam {i}", "createdAt": "2026-01-04T12:00:00Z", "revision": 3} for i in range(100)]
        merged = self.cli["_merge_reviews"](person, self.review(self.request(revision=3, status="open", agentReopens=many)))["requests"][0]
        self.assertEqual(merged["agentReopens"], [{"text": "spam 99", "createdAt": "2026-01-04T12:00:00Z", "revision": 3}])

    def test_r4_one_question_per_file_for_all_threads_reopened_in_a_call(self):
        one = self.settled_by_person()
        two = {**self.settled_by_person(), "id": "request-two", "text": "Second ask"}
        review = {**self.review(one), "requests": [one, two]}
        asked = []
        namespace = self.cli["cmd_review_reply"].__globals__
        with tempfile.TemporaryDirectory() as folder, mock.patch.dict(namespace, {
                "REVIEW_STATE_DIR": folder, "_load_reviews": lambda: {"changes.diff": copy.deepcopy(review)},
                "cmd_publish": lambda path, name: None,
                "_post": lambda path, body: asked.append(body) or {"id": "q-9"}}), contextlib.redirect_stdout(io.StringIO()) as out:
            self.cli["cmd_review_reply"]("request-one", "reopen", "First?", ["request-two", "reopen", "Second?"])
        self.assertEqual(len(asked), 1)
        self.assertIn("The agent reopened 2 review threads in changes.diff:", asked[0]["body"])
        self.assertIn('- [request-one] "Please handle the empty input": First?', asked[0]["body"])
        self.assertIn('- [request-two] "Second ask": Second?', asked[0]["body"])
        self.assertIn("blerg-runner ask --resolve q-9", out.getvalue())


if __name__ == "__main__":
    unittest.main()
