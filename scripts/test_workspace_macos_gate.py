"""Validator regressions, not substitutes for native execution."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


gate = load('check-workspace-macos-gate')
runner = load('test-workspace-macos')


class NativeGateTests(unittest.TestCase):
    def setUp(self):
        self.private = tempfile.TemporaryDirectory()
        self.addCleanup(self.private.cleanup)
        self.log = Path(self.private.name) / 'events.json'
        self.meta = dict(system='Darwin', goos='darwin', goarch='arm64', machine='arm64',
                         commit='head', commit_after='head', race=True, clean_before=True,
                         clean_after=True, complete=True)
        self.events = [dict(Action='pass', Test=name) for name in gate.required]
        self.events += [dict(Action='pass', Package='github.com/blerglab/blerg-ai/runner/internal/' + name) for name in ('daemon', 'hostgit', 'workspacepolicy')]

    def validate(self):
        self.log.write_text(''.join(json.dumps(event) + '\n' for event in self.events))
        with contextlib.redirect_stdout(io.StringIO()):
            gate.validate(self.meta, 'head', [self.log])

    def test_complete_native_gate(self):
        self.validate()

    def test_missing_failed_skipped_or_invalid_events(self):
        original = self.events[:]
        for change in ('missing', 'fail', 'skip', 'malformed'):
            with self.subTest(change=change):
                self.events = original[:-1]
                if change != 'missing':
                    self.events += [dict(Action=change, Test='TestWorkspaceProbe')]
                with self.assertRaises(SystemExit):
                    self.validate()

    def test_missing_required_test_even_with_completed_packages(self):
        self.events.pop(0)
        with self.assertRaises(SystemExit):
            self.validate()

    def test_requires_native_current_clean_completed_head(self):
        for field, value in [('system', 'Linux'), ('goos', 'linux'), ('goarch', 'amd64'), ('machine', None), ('goarch', None),
                             ('commit', 'old'), ('commit_after', 'changed'), ('race', False),
                             ('clean_before', False), ('clean_after', False), ('complete', False)]:
            with self.subTest(field=field), patch.dict(self.meta, {field: value}):
                with self.assertRaises(SystemExit):
                    self.validate()

    def test_runner_records_actual_commands_and_cleans_its_private_cache(self):
        self.exercise_runner(False)

    def test_runner_keeps_failure_evidence_without_acceptance(self):
        self.exercise_runner(True)

    def exercise_runner(self, fail):
        root = Path(self.private.name)
        calls = []
        def output(args, **kwargs):
            if args[0] == 'git':
                return '' if 'status' in args else 'head'
            return 'darwin\narm64' if args[1] == 'env' else 'version'
        def execute(args, **kwargs):
            calls.append(args)
            if args[1] == 'test':
                self.assertNotIn('GOOS', kwargs['env'])
                self.assertNotIn('BLERG_RUNNER_SESSION_TOKEN', kwargs['env'])
                self.assertEqual(kwargs['env']['CGO_ENABLED'], '1')
                self.assertIsNot(kwargs['stderr'], runner.subprocess.STDOUT,
                                 'Go download/build diagnostics must not corrupt JSON evidence')
                kwargs['stderr'].write('go: downloading example.test/fixture v1.0.0\n')
                kwargs['stdout'].write(''.join(json.dumps(event) + '\n' for event in self.events))
                if fail and './internal/daemon' in args:
                    raise runner.subprocess.CalledProcessError(1, args)
            else:
                with open(args[3], encoding='utf-8') as source:
                    gate.validate(json.load(source), args[5], args[6:])
        with patch.object(runner, '__file__', str(root / 'scripts/test-workspace-macos.py')), \
             patch.object(runner.platform, 'system', return_value='Darwin'), \
             patch.object(runner.platform, 'machine', return_value='arm64'), \
             patch.object(runner.shutil, 'which', side_effect=lambda name: '/usr/bin/' + name), \
             patch.object(runner.subprocess, 'check_output', side_effect=output), \
             patch.object(runner.subprocess, 'run', side_effect=execute), \
             contextlib.redirect_stdout(io.StringIO()):
            if fail:
                with self.assertRaises(runner.subprocess.CalledProcessError):
                    runner.run()
            else:
                runner.run()
        evidence = next((root / 'workspace-macos-evidence').iterdir())
        metadata = json.loads((evidence / 'metadata.json').read_text())
        self.assertEqual(metadata['accepted'], not fail)
        self.assertEqual(metadata['complete'], not fail)
        self.assertFalse(list(evidence.glob('private-*')))
        self.assertEqual(len(calls), 3 if fail else 4)
        self.assertTrue(all('-race' in command and '-json' in command for command in calls[:3]))
        self.assertEqual(metadata['test_exit_codes'], dict(daemon=1 if fail else 0, hostgit=0, workspacepolicy=0))
        self.assertEqual({path.name for path in evidence.glob('workspace-*.json')},
                         {'workspace-daemon.json', 'workspace-hostgit.json', 'workspace-policy.json'})
        self.assertEqual(len(list(evidence.glob('workspace-*.stderr.log'))), 3)

    def test_runner_rejects_cross_compile_before_writing(self):
        with patch.object(runner.platform, 'system', return_value='Linux'), patch.object(runner.tempfile, 'TemporaryDirectory') as temporary:
            with self.assertRaisesRegex(SystemExit, 'Native macOS required'):
                runner.run()
            temporary.assert_not_called()


if __name__ == '__main__':
    unittest.main()
