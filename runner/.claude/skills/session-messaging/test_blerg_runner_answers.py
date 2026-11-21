"""Answer schema publication binds the request to the uploaded artifact ID."""
import json
import os
import tempfile
import unittest
import test_blerg_runner_review_request as review_helpers

class AnswerHandler(review_helpers.ReviewStubHandler):
    def do_POST(self):
        if self.path.endswith("/answer-requests"):
            body=json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            self.server.recorded.append({"path":self.path,"json":body})
            return self._reply(201,{"request_id":"r1"})
        return super().do_POST()

class TestAnswerPublish(unittest.TestCase):
    setUp = review_helpers.TestReviewRequest.setUp
    run_cli = review_helpers.TestReviewRequest.run_cli
    def test_publish_answer_schema(self):
        server = review_helpers.make_server(artifact_reply={"id":"a"*32,"name":"choose.html","view":"html","version":1})
        server.RequestHandlerClass = AnswerHandler
        with tempfile.TemporaryDirectory() as folder:
            page = os.path.join(folder,"choose.html")
            schema = os.path.join(folder,"result.json")
            with open(page,"w") as f:
                f.write("<!doctype html><h1>Choose</h1>")
            with open(schema,"w") as f:
                json.dump({"type":"string","enum":["a","b"]},f)
            result = self.run_cli(["publish",page,"--answer-schema",schema,"--answer-ask","Choose one"],server)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual([r["path"] for r in server.recorded], ["/api/sessions/sess-rr/artifacts","/api/sessions/sess-rr/answer-requests"])
        self.assertEqual(server.recorded[1]["json"], {"artifact":"a"*32,"ask":"Choose one","schema":{"type":"string","enum":["a","b"]}})
        self.assertIn("Answer requested",result.stdout)

    def test_answer_requires_both_flags_before_upload(self):
        server=review_helpers.make_server()
        result=self.run_cli(["publish","missing.html","--answer-ask","Choose"],server)
        self.assertNotEqual(result.returncode,0)
        self.assertIn("--answer-schema",result.stderr)
        self.assertEqual(server.recorded,[])

    def test_help_teaches_the_sandbox_result_protocol(self):
        server = review_helpers.make_server()
        result = self.run_cli(["publish", "--help"], server)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("blerg:answer", result.stdout)
        self.assertIn("additionalProperties: false", result.stdout)
        self.assertIn("16 KiB", result.stdout)
