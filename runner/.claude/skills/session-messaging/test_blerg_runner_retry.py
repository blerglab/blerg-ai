import email.message
import http.client
import http.server
import threading
import io
import runpy
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock


class Reply(io.BytesIO):
    def __init__(self, code=200, body=b'{}', content_type='application/json'):
        super().__init__(body)
        self.code = code
        self.headers = email.message.Message()
        self.headers['Content-Type'] = content_type

    def peek(self, _size):
        return self.getvalue()[self.tell():]


class TestDeployRetry(unittest.TestCase):
    def setUp(self):
        self.cli = runpy.run_path(str(Path(__file__).with_name('blerg-runner')))
        self.open = self.cli['_open']
        self.opener = mock.Mock()
        self.patch = mock.patch.dict(self.open.__globals__, {
            '_DIRECT_OPENER': self.opener, '_ENV_OPENER': self.opener,
            '_SESSION_DIRECT_OPENER': self.opener, '_SESSION_ENV_OPENER': self.opener,
        })
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.now = 0
        self.sleeps = []
        def sleep(delay):
            self.sleeps.append(delay)
            self.now += delay
        self.clock = mock.patch.object(self.cli['time'], 'monotonic', side_effect=lambda: self.now)
        self.sleep = mock.patch.object(self.cli['time'], 'sleep', side_effect=sleep)
        self.clock.start()
        self.sleep.start()
        self.addCleanup(self.clock.stop)
        self.addCleanup(self.sleep.stop)

    def error(self, code):
        return urllib.error.HTTPError('https://runner.example.com/api', code, 'gateway', {}, io.BytesIO(b'busy'))

    def test_gateway_errors_retry_then_recover(self):
        errors = [self.error(code) for code in (502, 503, 504)]
        self.opener.open.side_effect = errors + [Reply()]
        with self.open(urllib.request.Request('https://runner.example.com/api'), 30) as reply:
            self.assertEqual(reply.read(), b'{}')
        self.assertEqual(self.sleeps, [1, 2, 4])
        self.assertTrue(all(error.closed for error in errors))

    def test_retry_budget_survives_twenty_second_deploy(self):
        self.opener.open.side_effect = [self.error(503) for _ in range(5)] + [Reply()]
        with self.open(urllib.request.Request('https://runner.example.com/api'), 30):
            pass
        self.assertEqual(self.sleeps, [1, 2, 4, 8, 8])

    def test_success_and_client_error_replies_are_never_retried(self):
        # An HTML file a person attached is served as a 200 (application/octet-stream or text/html):
        # it is the answer, not an outage page. A 4xx is the server's answer too, HTML or not.
        html = b'<!DOCTYPE html><html>an attachment</html>'
        for code, content_type in ((200, 'text/html'), (200, 'application/octet-stream'), (201, 'text/html')):
            with self.subTest(code=code, content_type=content_type):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = [Reply(code, html, content_type), Reply()]
                with self.open(urllib.request.Request('https://runner.example.com/api'), 30) as reply:
                    self.assertEqual(reply.read(), html)
                self.assertEqual(self.opener.open.call_count, 1)
        for code in (400, 404, 409, 500):
            with self.subTest(code=code):
                self.opener.open.reset_mock()
                page = urllib.error.HTTPError('https://runner.example.com/api', code, 'no', {'Content-Type': 'text/html'},
                                              io.BytesIO(b'<html>not found</html>'))
                self.opener.open.side_effect = [page, Reply()]
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    self.open(urllib.request.Request('https://runner.example.com/api'), 30)
                self.assertEqual(caught.exception.code, code)
                self.assertEqual(self.opener.open.call_count, 1)
        self.assertEqual(self.sleeps, [])

    def test_gateway_html_page_is_retried(self):
        page = urllib.error.HTTPError('https://runner.example.com/api', 503, 'busy', {'Content-Type': 'text/html'},
                                      io.BytesIO(b'<html>503 Service Temporarily Unavailable</html>'))
        self.opener.open.side_effect = [page, Reply()]
        with self.open(urllib.request.Request('https://runner.example.com/api'), 30) as reply:
            self.assertEqual(reply.read(), b'{}')
        self.assertEqual(self.sleeps, [1])

    def test_refused_or_reset_connection_retries_idempotent_requests(self):
        for failure in (ConnectionRefusedError(111, 'refused'), ConnectionResetError(104, 'reset'),
                        http.client.RemoteDisconnected('closed')):
            for wrapped in (failure, urllib.error.URLError(failure)):
                with self.subTest(failure=type(failure).__name__, wrapped=type(wrapped).__name__):
                    self.sleeps.clear()
                    self.opener.open.side_effect = [wrapped, Reply()]
                    with self.open(urllib.request.Request('https://runner.example.com/api'), 30) as reply:
                        self.assertEqual(reply.read(), b'{}')
                    self.assertEqual(self.sleeps, [1])

    def test_a_server_that_stays_down_is_reported_within_the_no_reply_budget(self):
        self.opener.open.side_effect = urllib.error.URLError(ConnectionRefusedError(111, 'refused'))
        with self.assertRaises(urllib.error.URLError) as caught:
            self.open(urllib.request.Request('https://runner.example.com/api'), 30)
        self.assertEqual(caught.exception.reason, 'connection refused')
        self.assertLessEqual(self.now, self.cli['NO_REPLY_RETRY_BUDGET'])
        self.assertGreater(len(self.sleeps), 1)

    def test_a_caller_with_its_own_loop_turns_no_reply_retries_off(self):
        self.opener.open.side_effect = [urllib.error.URLError(ConnectionResetError(104, 'reset')), Reply()]
        req = urllib.request.Request('https://runner.example.com/api', data=b'{}', headers={'Idempotency-Key': 'k'})
        with self.assertRaises(urllib.error.URLError):
            self.open(req, 30, retry_no_reply=False)
        self.assertEqual(self.opener.open.call_count, 1)
        # A gateway reply is still retried for it.
        self.opener.open.reset_mock()
        self.opener.open.side_effect = [self.error(503), Reply()]
        with self.open(req, 30, retry_no_reply=False):
            pass
        self.assertEqual(self.sleeps, [1])

    def test_refused_connection_on_unkeyed_post_or_timeout_is_not_retried(self):
        cases = (
            (urllib.request.Request('https://runner.example.com/api', data=b'{}'),
             urllib.error.URLError(ConnectionRefusedError(111, 'refused'))),
            (urllib.request.Request('https://runner.example.com/api'), urllib.error.URLError(TimeoutError('timed out'))),
        )
        for req, failure in cases:
            with self.subTest(failure=failure):
                self.opener.open.reset_mock()
                self.opener.open.side_effect = [failure, Reply()]
                with self.assertRaises(urllib.error.URLError) as caught:
                    self.open(req, 30)
                self.assertIsInstance(caught.exception, self.cli['_RequestError'])
                self.assertEqual(self.opener.open.call_count, 1)
        self.assertEqual(self.sleeps, [])

    def test_retry_attempts_fit_inside_the_budget(self):
        # Each attempt here hangs for its whole timeout before the gateway answers 503: the budget
        # counts that time, so a retry never waits past it and no retry starts after it.
        timeouts = []
        def slow_gateway(_req, timeout):
            timeouts.append(timeout)
            self.now += timeout
            raise self.error(503)
        self.opener.open.side_effect = slow_gateway
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api'), 120)
        self.assertEqual(timeouts[0], 120)  # the first attempt keeps its own timeout
        self.assertEqual(len(timeouts), 1)  # it used up the budget, so nothing is retried

        timeouts.clear()
        self.now = 0
        def gateway_after_ten_seconds(_req, timeout):
            timeouts.append(timeout)
            self.now += min(timeout, 10)
            raise self.error(503)
        self.opener.open.side_effect = gateway_after_ten_seconds
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api'), 120)
        self.assertLessEqual(self.now, self.cli['RETRY_BUDGET'])
        self.assertTrue(all(t <= self.cli['RETRY_BUDGET'] for t in timeouts[1:]), timeouts)

    def test_caller_deadline_caps_the_budget(self):
        self.opener.open.side_effect = self.error(503)
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api'), 30, deadline=5)
        self.assertLessEqual(sum(self.sleeps), 5)

    def test_unsafe_post_is_not_retried(self):
        self.opener.open.side_effect = self.error(503)
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api', data=b'{}'), 30)
        self.assertEqual(self.sleeps, [])

    def test_idempotent_methods_and_keyed_post_retry(self):
        for method in ('GET', 'HEAD', 'OPTIONS', 'PUT', 'DELETE', 'POST', 'PATCH'):
            with self.subTest(method=method):
                self.opener.open.side_effect = [self.error(503), Reply()]
                req = urllib.request.Request('https://runner.example.com/api', method=method)
                if method in ('POST', 'PATCH'):
                    req.add_header('Idempotency-Key', 'fixture-key')
                with self.open(req, 30):
                    pass
                self.assertEqual(self.opener.open.call_args.args[0], req)

    def test_non_gateway_error_is_not_retried(self):
        self.opener.open.side_effect = self.error(409)
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api'), 30)
        self.assertEqual(self.sleeps, [])

    def test_exhaustion_is_bounded(self):
        self.opener.open.side_effect = self.error(503)
        with self.assertRaises(urllib.error.HTTPError):
            self.open(urllib.request.Request('https://runner.example.com/api'), 30)
        self.assertLessEqual(sum(self.sleeps), 30)
        self.assertGreaterEqual(sum(self.sleeps), 23)

    def test_session_call_uses_same_retry_policy(self):
        self.opener.open.side_effect = [self.error(503), Reply()]
        call = self.cli['_call_full']
        # SERVER_HTTP and the session id are read from the environment when the CLI loads; set them
        # here so the test does not depend on the caller's environment.
        with mock.patch.dict(call.__globals__, {'_session_token': lambda: 'fixture-token',
                                                'SERVER_HTTP': 'https://runner.example.com',
                                                'SESSION_ID': 'fixture-session'}):
            status, body, _headers = call('POST', '/api/sessions', headers={'Idempotency-Key': 'fixture-key'})
        self.assertEqual((status, body), (200, b'{}'))
        self.assertEqual(self.sleeps, [1])

    def test_html_post_without_key_is_not_retried(self):
        self.opener.open.return_value = Reply(200, b'<html>busy</html>', 'text/html')
        req = urllib.request.Request('https://runner.example.com/api', data=b'{}')
        with self.open(req, 30) as reply:
            self.assertEqual(reply.read(), b'<html>busy</html>')
        self.assertEqual(self.opener.open.call_count, 1)
        self.assertEqual(self.sleeps, [])

    def test_keyed_stream_is_rewound(self):
        data = io.BytesIO(b'fixture file')
        sent = []
        def send(req, **_kwargs):
            sent.append(req.data.read())
            if len(sent) == 1:
                raise self.error(503)
            return Reply()
        self.opener.open.side_effect = send
        req = urllib.request.Request('https://runner.example.com/api', data=data,
                                     headers={'Idempotency-Key': 'fixture-key'})
        with self.open(req, 30):
            pass
        self.assertEqual(sent, [b'fixture file', b'fixture file'])


class TestHtmlDownloadOverHttp(unittest.TestCase):
    """A real local server, the CLI's real opener: a 200 HTML attachment is fetched once."""

    def test_html_attachment_is_fetched_once(self):
        hits = []
        body = b'<!DOCTYPE html><html><body>a page the person attached</body></html>'

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                hits.append(self.path)
                self.send_response(200)
                self.send_header('Content-Type', 'application/octet-stream')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_args):
                pass

        server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        cli = runpy.run_path(str(Path(__file__).with_name('blerg-runner')))
        with mock.patch.object(cli['time'], 'sleep', side_effect=AssertionError('retried a 200')):
            url = f'http://127.0.0.1:{server.server_address[1]}/api/sessions/s/uploads/u'
            with cli['_open'](urllib.request.Request(url), 30) as reply:
                self.assertEqual(reply.read(), body)
        self.assertEqual(len(hits), 1)
