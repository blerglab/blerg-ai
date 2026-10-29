"""Validator regressions, not substitutes for native execution."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
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
                         clean_after=True, complete=True, docker_present=True)
        self.events = [dict(Action='pass', Test=name) for name in gate.required]
        self.docker = dict(Action='pass', Test='TestWorkspaceExplorerProductionDockerImage',
                           Package='github.com/blerglab/blerg-ai/runner/internal/daemon')
        self.events.append(self.docker)
        self.events += [dict(Action='pass', Package='github.com/blerglab/blerg-ai/runner/internal/' + name) for name in ('daemon', 'hostgit', 'workspacepolicy')]

    def validate(self):
        self.log.write_text(''.join(json.dumps(event) + '\n' for event in self.events))
        with contextlib.redirect_stdout(io.StringIO()) as output:
            gate.validate(self.meta, 'head', [self.log])
        return output.getvalue()

    def test_complete_native_gate(self):
        self.validate()
        self.assertTrue(self.meta['accepted'])
        self.assertTrue(self.meta['native_host_accepted'])
        self.assertEqual(self.meta['pending'], [])

    def test_exact_docker_skip_is_named_pending_without_acceptance(self):
        self.meta['docker_present'] = False
        self.docker['Action'] = 'skip'
        output = self.validate()
        self.assertTrue(self.meta['native_host_accepted'])
        self.assertFalse(self.meta['accepted'])
        self.assertEqual(self.meta['pending'], [dict(test=self.docker['Test'], package=self.docker['Package'],
                                                   reason='requires Docker Desktop on macOS and the locally built production sandbox image',
                                                   accepted=False)])
        self.assertIn(self.docker['Test'], output)
        self.assertIn('pending: requires Docker Desktop on macOS', output)
        self.assertIn('NOT accepted', output)

    def test_docker_absent_allows_full_acceptance_when_every_case_passes(self):
        self.meta['docker_present'] = False
        self.validate()
        self.assertTrue(self.meta['accepted'])
        self.assertEqual(self.meta['pending'], [])

    def test_a_pass_cannot_hide_a_listed_skip_in_another_log(self):
        self.meta['docker_present'] = False
        self.events.append(dict(self.docker, Action='skip'))
        self.validate()
        self.assertFalse(self.meta['accepted'])
        self.assertEqual(len(self.meta['pending']), 1)

    def test_listed_skip_with_docker_present_fails(self):
        self.docker['Action'] = 'skip'
        with self.assertRaisesRegex(SystemExit, self.docker['Test']):
            self.validate()
        self.assertFalse(self.meta['accepted'])
        self.assertFalse(self.meta['native_host_accepted'])

    def test_skips_outside_exact_package_and_test_allow_list_fail(self):
        self.meta['docker_present'] = False
        for name, package in [('TestWorkspaceOtherDockerTest', self.docker['Package']),
                              ('TestOtherProbe', self.docker['Package']),
                              (self.docker['Test'] + '/child', self.docker['Package']),
                              (self.docker['Test'], 'another/package')]:
            with self.subTest(name=name, package=package):
                self.events.append(dict(Action='skip', Test=name, Package=package))
                with self.assertRaises(SystemExit):
                    self.validate()
                self.events.pop()

    def test_exact_timing_waiver_is_accepted_and_reported(self):
        name = 'TestWorkspaceSearchLargeIndexUnderContention'
        self.events.append(dict(Action='skip', Test=name, Package=self.docker['Package']))
        output = self.validate()
        self.assertTrue(self.meta['accepted'])
        self.assertTrue(self.meta['native_host_accepted'])
        self.assertEqual(self.meta['pending'], [])
        self.assertIn('waived: BLG-319', output)
        self.assertNotIn('none skipped', output)

    def test_timing_waivers_are_exact_and_never_required(self):
        self.assertFalse(gate.timing_waivers.keys() & gate.required)
        name = 'TestWorkspaceSearchLargeIndexUnderContention'
        for test, package in [(name + '/child', self.docker['Package']), (name, 'another/package'),
                              (name + 'X', self.docker['Package'])]:
            with self.subTest(test=test, package=package):
                self.events.append(dict(Action='skip', Test=test, Package=package))
                with self.assertRaisesRegex(SystemExit, 'unlisted skip'):
                    self.validate()
                self.events.pop()

    def test_a_waived_test_that_fails_still_fails(self):
        self.events.append(dict(Action='fail', Test='TestWorkspaceSearchLargeIndexUnderContention', Package=self.docker['Package']))
        with self.assertRaises(SystemExit):
            self.validate()

    def test_linux_quota_guest_does_not_gain_a_native_docker_skip_waiver(self):
        self.meta['docker_present'] = False
        self.events.append(dict(Action='skip', Test='TestWorkspaceQuotaGuest', Package=self.docker['Package']))
        with self.assertRaisesRegex(SystemExit, 'unlisted skip: .*TestWorkspaceQuotaGuest'):
            self.validate()
        self.assertFalse(self.meta['accepted'])
        self.assertFalse(self.meta['native_host_accepted'])

    def test_docker_case_cannot_disappear_or_fail_even_when_absent(self):
        for present in (False, True):
            for action in ('missing', 'fail'):
                with self.subTest(present=present, action=action):
                    self.meta['docker_present'] = present
                    self.events.remove(self.docker)
                    if action == 'fail':
                        self.events.append(dict(self.docker, Action='fail'))
                    with self.assertRaises(SystemExit):
                        self.validate()
                    if action == 'fail':
                        self.events.pop()
                    self.events.append(self.docker)

    def test_checker_persists_pending_and_clears_stale_acceptance_on_failure(self):
        metadata = Path(self.private.name) / 'metadata.json'
        self.meta['docker_present'] = False
        self.docker['Action'] = 'skip'
        self.log.write_text(''.join(json.dumps(event) + '\n' for event in self.events))
        metadata.write_text(json.dumps(self.meta))
        with contextlib.redirect_stdout(io.StringIO()):
            gate.check(metadata, 'head', [self.log])
        result = json.loads(metadata.read_text())
        self.assertTrue(result['native_host_accepted'])
        self.assertFalse(result['accepted'])
        self.assertEqual(result['pending'][0]['test'], self.docker['Test'])
        result.update(accepted=True, native_host_accepted=True, docker_present=True)
        metadata.write_text(json.dumps(result))
        with self.assertRaises(SystemExit):
            gate.check(metadata, 'head', [self.log])
        result = json.loads(metadata.read_text())
        self.assertFalse(result['accepted'])
        self.assertFalse(result['native_host_accepted'])

    def test_cli_exit_and_metadata_distinguish_pending_from_forbidden_skip(self):
        metadata = Path(self.private.name) / 'metadata.json'
        self.docker['Action'] = 'skip'
        self.log.write_text(''.join(json.dumps(event) + '\n' for event in self.events))
        for present in (False, True):
            with self.subTest(docker_present=present):
                metadata.write_text(json.dumps(dict(self.meta, docker_present=present, accepted=True)))
                result = subprocess.run([sys.executable, '-B', gate.__file__, '--metadata', str(metadata),
                                         '--commit', 'head', str(self.log)], capture_output=True, text=True, check=False)
                self.assertEqual(result.returncode, 1 if present else 0, result.stdout + result.stderr)
                self.assertIn(self.docker['Test'], result.stdout + result.stderr)
                if not present:
                    self.assertIn('pending: requires Docker Desktop on macOS', result.stdout)
                    self.assertIn('NOT accepted', result.stdout)
                recorded = json.loads(metadata.read_text())
                self.assertFalse(recorded['accepted'])
                self.assertEqual(recorded['native_host_accepted'], not present)

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
                             ('clean_before', False), ('clean_after', False), ('complete', False),
                             ('docker_present', None), ('docker_present', 'false')]:
            with self.subTest(field=field), patch.dict(self.meta, {field: value}):
                with self.assertRaises(SystemExit):
                    self.validate()

    def test_runner_records_actual_commands_and_cleans_its_private_cache(self):
        self.exercise_runner(False)

    def test_runner_keeps_failure_evidence_without_acceptance(self):
        self.exercise_runner(True)

    def test_runner_preserves_pending_without_accepting_it(self):
        self.docker['Action'] = 'skip'
        self.exercise_runner(False, docker_present=False)

    def exercise_runner(self, fail, docker_present=True):
        root = Path(self.private.name)
        calls = []
        docker_paths = []
        def which(name, **kwargs):
            if name == 'docker':
                docker_paths.append(kwargs['path'])
                if not docker_present:
                    return None
            return '/usr/bin/' + name
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
                self.assertEqual(kwargs['env'].get('BLERG_EXPLORER_DOCKER_TEST'), '1' if docker_present else None)
                self.assertEqual(docker_paths, [kwargs['env']['PATH']])
                self.assertIsNot(kwargs['stderr'], runner.subprocess.STDOUT,
                                 'Go download/build diagnostics must not corrupt JSON evidence')
                kwargs['stderr'].write('go: downloading example.test/fixture v1.0.0\n')
                kwargs['stdout'].write(''.join(json.dumps(event) + '\n' for event in self.events))
                if fail and './internal/daemon' in args:
                    raise runner.subprocess.CalledProcessError(1, args)
            else:
                gate.check(args[3], args[5], args[6:])
        with patch.object(runner, '__file__', str(root / 'scripts/test-workspace-macos.py')), \
             patch.object(runner.platform, 'system', return_value='Darwin'), \
             patch.object(runner.platform, 'machine', return_value='arm64'), \
             patch.object(runner.shutil, 'which', side_effect=which), \
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
        self.assertEqual(metadata['docker_present'], docker_present)
        self.assertEqual(metadata['accepted'], not fail and docker_present)
        self.assertEqual(metadata['native_host_accepted'], not fail)
        self.assertEqual(len(metadata['pending']), 1 if not docker_present else 0)
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
