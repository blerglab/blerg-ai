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
        return self.responses.pop(0)


class UploadTest(unittest.TestCase):
    def test_returns_the_id(self):
        session = FakeSession(FakeResponse(201, {"id": "p1"}))
        self.assertEqual(upload(session, "https://storage.example/p", b"x"), "p1")

    def test_server_error_raises(self):
        with self.assertRaises(UploadError):
            upload(FakeSession(FakeResponse(503)), "https://storage.example/p", b"x")
