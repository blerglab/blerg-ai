import contextlib
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

loader = importlib.machinery.SourceFileLoader('gate_cli', str(Path(__file__).with_name('blerg-runner')))
spec = importlib.util.spec_from_loader(loader.name, loader)
cli = importlib.util.module_from_spec(spec)
loader.exec_module(cli)

class GateReportTest(unittest.TestCase):
    def test_posts_only_sanitized_projection(self):
        body = dict(run_id='run', revision=1, commit='sha', started_at='start', finished_at='end',
                    host='canary-secret', report=dict(seconds=0, complete=True, token='canary-secret',
                    suites=[dict(suite='runner-unit', seconds=0, outcome='pass', log='canary-secret')]))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'gate.json'
            path.write_text(json.dumps(body))
            with mock.patch.object(cli, '_work_post', return_value={}) as post, contextlib.redirect_stdout(io.StringIO()) as output:
                cli.cmd_gate_report(str(path))
            args = post.call_args.args
            self.assertEqual(args[1], 'gate-reports')
            self.assertNotIn('canary-secret', json.dumps(args[2]))
            self.assertEqual(args[2]['report']['seconds'], 0)
            self.assertIn('not verified', output.getvalue())

    def test_parser_dispatches_gate_report(self):
        with mock.patch.object(cli, '_configured', return_value=True), mock.patch.object(cli, 'cmd_gate_report') as report, mock.patch('sys.argv', ['blerg-runner', 'gate', 'report', 'gate.json']):
            cli.main()
        report.assert_called_once_with('gate.json')

    def test_invalid_file_never_posts(self):
        with mock.patch.object(cli, '_work_post') as post, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            cli.cmd_gate_report('/missing-fixture-gate.json')
        post.assert_not_called()
