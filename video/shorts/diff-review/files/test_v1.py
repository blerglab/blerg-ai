import unittest

from photos.upload import UploadError, upload


class FakeResponse:
    def __init__(self, status, body=None):
        self.status_code = status
        self._body = body or {}

    def json(self):
        return self._body

    def raise_for_status(self):
        if self.status_code >= 400:
            raise RuntimeError(self.status_code)


class FakeSession:
    def __init__(self, *responses):
        self.responses = list(responses)
        self.calls = 0

    def put(self, url, data, timeout):
        self.calls += 1
        r = self.responses.pop(0)
        if isinstance(r, Exception):
            raise r
        return r


URL = "https://storage.example/p"


class UploadTest(unittest.TestCase):
    def test_returns_the_id(self):
        session = FakeSession(FakeResponse(201, {"id": "p1"}))
        self.assertEqual(upload(session, URL, b"x"), "p1")

    def test_retries_server_errors_then_succeeds(self):
        waits = []
        session = FakeSession(FakeResponse(503), ConnectionError(), FakeResponse(201, {"id": "p2"}))
        self.assertEqual(upload(session, URL, b"x", sleep=waits.append), "p2")
        self.assertEqual(waits, [0.5, 1.0])

    def test_gives_up_after_the_last_retry(self):
        session = FakeSession(*[FakeResponse(503)] * 3)
        with self.assertRaises(UploadError):
            upload(session, URL, b"x", retries=2, sleep=lambda s: None)
        self.assertEqual(session.calls, 3)

    def test_client_errors_are_not_retried(self):
        session = FakeSession(FakeResponse(403))
        with self.assertRaises(RuntimeError):
            upload(session, URL, b"x", sleep=lambda s: None)
        self.assertEqual(session.calls, 1)
