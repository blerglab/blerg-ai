import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import pre_merge

class GateProducerTest(unittest.TestCase):
    def telemetry(self):
        return dict(id='run', commit='sha', started_at='start', finished_at='end', seconds=0,
                    complete=True, host='canary-secret', suites=[dict(suite='runner-unit',seconds=0,outcome='pass',log='canary-secret')])

    def test_posts_sanitized_file_through_cli(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'BLERG_RUNNER_SESSION_ID':'fixture'}), patch.object(pre_merge.subprocess, 'run') as run:
            run.return_value.returncode=0
            pre_merge.publish_gate_report(self.telemetry(), Path(directory))
            body=json.loads((Path(directory)/'gate-report.json').read_text())
            self.assertNotIn('canary-secret', json.dumps(body))
            self.assertEqual(body['report']['seconds'], 0)
            self.assertEqual(run.call_args.args[0][-3:-1], ['gate','report'])
            self.assertEqual(run.call_args.kwargs['timeout'], 15)

    def test_failure_is_safe_and_does_not_raise(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'BLERG_RUNNER_SESSION_ID':'fixture'}), patch.object(pre_merge.subprocess, 'run') as run, contextlib.redirect_stderr(io.StringIO()) as output:
            run.return_value.returncode=1
            pre_merge.publish_gate_report(self.telemetry(), Path(directory))
            self.assertIn('upload unavailable', output.getvalue())
            self.assertNotIn('canary-secret', output.getvalue())

    def test_malformed_record_never_raises(self):
        broken = self.telemetry(); del broken['suites'][0]['outcome']
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'BLERG_RUNNER_SESSION_ID':'fixture'}), patch.object(pre_merge.subprocess, 'run') as run, contextlib.redirect_stderr(io.StringIO()) as output:
            pre_merge.publish_gate_report(broken, Path(directory))
            run.assert_not_called()
            self.assertIn('KeyError', output.getvalue())
            self.assertNotIn('canary-secret', output.getvalue())

    def test_no_session_does_not_spawn(self):
        with patch.dict(os.environ, {'BLERG_RUNNER_SESSION_ID':''}), patch.object(pre_merge.subprocess,'run') as run:
            pre_merge.publish_gate_report(self.telemetry(), Path('.'))
            run.assert_not_called()

    def test_orchestrator_calls_producer_with_real_summary(self):
        with tempfile.TemporaryDirectory() as directory, patch('sys.argv', ['pre_merge', '--suite', 'focused', '--logs', str(Path(directory) / 'logs')]), patch.object(pre_merge, 'matrix', return_value=[('focused', '.', ['true'])]), patch.object(pre_merge, 'append_record'), patch.object(pre_merge, 'publish_gate_report') as producer, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(pre_merge.main(), 0)
            record=producer.call_args.args[0]
            self.assertTrue(record['complete'])
            self.assertEqual(record['suites'][0]['suite'], 'focused')
            self.assertEqual(record['suites'][0]['outcome'], 'pass')
            self.assertIn('finished_at', record)
