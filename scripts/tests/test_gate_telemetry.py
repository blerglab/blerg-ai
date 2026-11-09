import importlib.util
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest
import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import gate_telemetry as gate

class GateTelemetryTests(unittest.TestCase):
    def test_only_full_browser_suites_take_the_ui_lock(self):
        for cwd in ('packages/chat', 'core/web'):
            self.assertFalse(gate.needs_ui_lock(['npm', 'test'], cwd))
        self.assertTrue(gate.needs_ui_lock(['npm', 'test'], 'board/web'))
        for command in (['npm', 'test', '--', '--project', 'browser'],
                        ['vitest', 'run', '--project', 'browser', '--exclude=src/slow.browser.test.tsx'],
                        ['vitest', 'run', '--project', 'browser', '--exclude', 'src/slow.browser.test.tsx'],
                        ['npm', 'run', 'test:performance'],
                        ['vitest', 'run', '--project=browser-performance']):
            self.assertTrue(gate.needs_ui_lock(command))
        for command in (['npm', 'test', '--', '--project', 'unit'],
                        ['vitest', 'run', '--project=unit'],
                        ['vitest', 'related', '--run', 'src/Component.tsx', '--project', 'browser'],
                        ['npm', 'test', '--', '--project', 'browser', 'src/Component.browser.test.tsx'],
                        ['npm', 'run', 'build']):
            self.assertFalse(gate.needs_ui_lock(command))

    def test_performance_script_retains_vitest_test_durations(self):
        command, measured = gate.vitest_command(['npm', 'run', 'test:performance'], Path('results.json'))
        self.assertTrue(measured)
        self.assertEqual(command[:4], ['npm', 'run', 'test:performance', '--'])
        self.assertIn('--reporter=json', command)

    def test_go_package_identity_reruns_and_slowest(self):
        with tempfile.TemporaryDirectory() as tmp:
            log=Path(tmp)/'go.log'
            events=[dict(Package='p',Test='Same',Action='fail',Elapsed=2),dict(Package='p',Test='Same',Action='pass',Elapsed=1),dict(Package='q',Test='Same',Action='pass',Elapsed=3)]
            log.write_text('\n'.join(json.dumps(e) for e in events))
            got=gate.test_results(log)
            self.assertEqual(got['counts'],{'pass':2,'fail':1,'skip':0})
            self.assertEqual(got['slowest'][0]['package'],'q')
            self.assertEqual(len(got['flake_reruns']),1)
            self.assertEqual(got['flake_reruns'][0]['outcomes'],['fail','pass'])
    def test_vitest_milliseconds_and_append(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'vitest.json';p.write_text(json.dumps({'testResults':[{'name':'file','assertionResults':[{'fullName':'works','status':'passed','duration':1250}]}]}))
            Path(str(p)+'.reruns.json').write_text(json.dumps([{'test':'works','retries':1,'rerun_passed':True}]))
            got=gate.test_results(Path(tmp)/'missing.log',p)
            self.assertTrue(got['flake_reruns'][0]['rerun_passed'])
            self.assertEqual(got['slowest'][0]['seconds'],1.25)
            dest=Path(tmp)/'runs.jsonl'
            gate.append_record({'suites':[]},dest);gate.append_record({'suites':[{'suite':'a'}]},dest)
            self.assertEqual(len(dest.read_text().splitlines()),2)
    def test_worker_go_command_requests_test_events(self):
        self.assertEqual(gate.go_json_command(['go','-C','board','test','./internal/db']), ['go','-C','board','test','-json','./internal/db'])
        self.assertEqual(gate.go_json_command(['go','test','-json']), ['go','test','-json'])
        self.assertEqual(gate.go_json_command(['go','-C','test','test']), ['go','-C','test','test','-json'])
        self.assertEqual(gate.go_json_command(['go','run','test']), ['go','run','test'])

    def test_compare_uses_suite_and_test_cost(self):
        a={'suites':[{'suite':'unit','seconds':2,'tests':{'slowest':[{'test':'x','seconds':1}]}}]}
        b={'suites':[{'suite':'unit','seconds':5,'tests':{'slowest':[{'test':'x','seconds':3}]}}]}
        result=gate.compare(a,b)
        self.assertEqual(result['suites'][0]['delta_seconds'],3)
        self.assertEqual(result['tests'][0]['delta_seconds'],2)

    def test_telemetry_io_failure_is_best_effort(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent=Path(tmp)/'file';parent.write_text('not a directory')
            self.assertFalse(gate.append_record({'suites':[]},parent/'record.jsonl'))

    def test_absolute_test_paths_are_not_persisted(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'vitest.json';p.write_text(json.dumps({'testResults':[{'name':str(gate.ROOT/'runner/frontend/src/example.test.ts'),'assertionResults':[{'fullName':'works','status':'passed','duration':1}]}]}))
            got=gate.test_results(Path(tmp)/'none',p)
            self.assertEqual(got['slowest'][0]['package'],'runner/frontend/src/example.test.ts')

    def test_worker_exit_survives_telemetry_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent=Path(tmp)/'file';parent.write_text('not a directory')
            result=subprocess.run([sys.executable,str(gate.ROOT/'scripts/gate_record.py'),'--suite','fixture','--',sys.executable,'-c','raise SystemExit(7)'],env=dict(os.environ,BLERG_GATE_TELEMETRY=str(parent/'runs.jsonl')),capture_output=True,text=True)
            self.assertEqual(result.returncode,7,result.stdout+result.stderr)

    def test_rolling_median(self):
        self.assertIsNone(gate.rolling_median([]))
        self.assertEqual(gate.rolling_median([3, 1, 2]), 2)
        self.assertEqual(gate.rolling_median([4, 1, 2, 3]), 2.5)

    def test_load_history_filters_by_host_and_skips_malformed_lines(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'runs.jsonl'
            path.write_text(
                json.dumps({'host': 'a', 'suites': [{'suite': 'x', 'seconds': 1}]}) + '\n'
                'not json\n'
                + json.dumps({'host': 'b', 'suites': [{'suite': 'x', 'seconds': 2}]}) + '\n'
                + json.dumps({'host': 'a', 'suites': [{'suite': 'x', 'seconds': 3}]}) + '\n')
            records = gate.load_history(path, host='a')
            self.assertEqual([r['suites'][0]['seconds'] for r in records], [1, 3])
            self.assertEqual(gate.load_history(path), [
                {'host': 'a', 'suites': [{'suite': 'x', 'seconds': 1}]},
                {'host': 'b', 'suites': [{'suite': 'x', 'seconds': 2}]},
                {'host': 'a', 'suites': [{'suite': 'x', 'seconds': 3}]}])
            self.assertEqual(gate.load_history(Path(tmp) / 'missing.jsonl'), [])

    def _suite_row(self, seconds, pass_count=10, extra_slowest=()):
        return dict(suite='runner-go', seconds=seconds,
                    tests={'counts': {'pass': pass_count, 'fail': 0, 'skip': 0},
                             'slowest': list(extra_slowest)})

    def test_growth_warnings_flags_a_real_slowdown(self):
        history = [{'suites': [self._suite_row(100)]} for _ in range(5)]
        report = {'suites': [self._suite_row(140)]}
        warnings = gate.growth_warnings(history, report)
        self.assertEqual(len(warnings), 1)
        self.assertIn('slowdown', warnings[0])

    def test_growth_warnings_tells_added_coverage_apart_from_a_slowdown(self):
        history = [{'suites': [self._suite_row(100, pass_count=100)]} for _ in range(5)]
        report = {'suites': [self._suite_row(140, pass_count=140)]}
        warnings = gate.growth_warnings(history, report)
        self.assertEqual(len(warnings), 1)
        self.assertIn('added coverage', warnings[0])

    def test_growth_warnings_requires_a_minimum_history_before_judging(self):
        history = [{'suites': [self._suite_row(100)]} for _ in range(2)]
        report = {'suites': [self._suite_row(500)]}
        self.assertEqual(gate.growth_warnings(history, report), [])

    def test_growth_warnings_ignores_a_suite_within_the_threshold(self):
        history = [{'suites': [self._suite_row(100)]} for _ in range(5)]
        report = {'suites': [self._suite_row(110)]}
        self.assertEqual(gate.growth_warnings(history, report), [])

    def test_growth_warnings_flags_one_slow_test_even_when_its_suite_total_did_not_grow(self):
        slow_test = dict(package='runner/internal/db', test='TestFlood', seconds=60)
        fast_history_test = dict(package='runner/internal/db', test='TestFlood', seconds=20)
        history = [{'suites': [self._suite_row(500, extra_slowest=[fast_history_test])]} for _ in range(5)]
        report = {'suites': [self._suite_row(500, extra_slowest=[slow_test])]}
        warnings = gate.growth_warnings(history, report)
        self.assertEqual(len(warnings), 1)
        self.assertIn('TestFlood', warnings[0])
