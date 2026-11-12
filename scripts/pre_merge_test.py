"""Orchestration regressions: a failed suite must not hide the next result."""
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import pre_merge


class PreMergeTest(unittest.TestCase):
    def test_go_defaults_to_path_and_honors_explicit_override(self):
        for requested in (None, '/fixture/toolchain/go'):
            with self.subTest(requested=requested), tempfile.TemporaryDirectory() as directory:
                environment = dict(os.environ)
                environment.pop('GO', None)
                if requested is not None:
                    environment['GO'] = requested
                with patch.dict(os.environ, environment, clear=True), \
                        patch('sys.argv', ['pre_merge', '--list', '--logs', directory]), \
                        patch.object(pre_merge.Path, 'home', return_value=Path(directory)), \
                        patch.object(pre_merge.shutil, 'which', side_effect=lambda name: '/fixture/path/go' if name == 'go' else None), \
                        patch.object(pre_merge, 'matrix', return_value=[]) as matrix, \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), 0)
                self.assertEqual(matrix.call_args.args[0], requested or '/fixture/path/go')

    def test_full_format_check_includes_verification_module(self):
        command = next(command for name, _, command in pre_merge.matrix('go', 2) if name == 'gofmt')
        self.assertIn('./scripts/verification', command[-1])

    def test_make_test_uses_the_bounded_gate_without_losing_modules(self):
        command = pre_merge.subprocess.check_output(['make', '-n', 'test'], text=True)
        self.assertNotIn('go test -p 1', command)
        for module in ('contracts', 'core', 'runner', 'board'):
            self.assertIn('--suite ' + module + '-go', command)
            self.assertIn('--suite ' + module + '-vet', command)

    def test_explicit_vet_removes_only_the_duplicate_implicit_vet(self):
        suite = ('runner-go', 'runner', ['go', 'test', '-json', '-p=2', './...'])
        command = pre_merge.prepare_go_command(suite, {'runner-go', 'runner-vet'}, 2)
        self.assertIn('-vet=off', command)
        self.assertIn('go-test-shards/main.go', ' '.join(command))
        command = pre_merge.prepare_go_command(suite, {'runner-go'}, 2)
        self.assertNotIn('-vet=off', command)
        wire = ('persistent-shell-wire-go', 'runner', ['go', 'test', './internal/server'])
        self.assertEqual(pre_merge.prepare_go_command(wire, {'runner-vet'}, 2), wire[2])

    def test_markdown_outside_docs_stays_in_its_region(self):
        names = lambda paths: [suite[0] for suite in pre_merge.select_changed(pre_merge.matrix('go', 2), paths)[0]]
        self.assertIn('runner-go', names(['runner/skills/coordinating/SKILL.md']))
        self.assertNotIn('runner-go', names(['docs/features.md']))
        self.assertNotIn('runner-go', names(['README.md']))

    def setUp(self):
        producer = patch.object(pre_merge, "publish_gate_report")
        producer.start()
        self.addCleanup(producer.stop)
        # Synthetic matrices exercise the real recorder without adding fake
        # gates to the developer's measurement history.
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        environment = patch.dict(os.environ, {'BLERG_GATE_TELEMETRY': str(Path(directory.name) / 'runs.jsonl')})
        environment.start()
        self.addCleanup(environment.stop)

    def region_names(self, *paths):
        suites, skipped, _ = pre_merge.select_changed(pre_merge.matrix('go', 2), paths)
        self.assertFalse(set(skipped) & {name for name, _, _ in suites})
        return {name for name, _, _ in suites}

    def test_frontend_regions_keep_complete_suites(self):
        names = self.region_names('runner/frontend/src/ExplorerDock.tsx')
        self.assertEqual(names, {'runner-' + kind for kind in
                                ('lint', 'types', 'unit', 'browser', 'performance', 'build')} | {'scrub', 'links', 'theme-check', 'theme-colors'} | set(pre_merge.IMAGE_REGIONS))
        selected, _, _ = pre_merge.select_changed(pre_merge.matrix('go', 2), ['runner/frontend/src/ExplorerDock.tsx'])
        for name, _, command in selected:
            self.assertNotIn('related', command)
            self.assertFalse(any('ExplorerDock' in arg for arg in command))
        for app, path in [('core', 'core/web/src/a.ts'), ('board', 'board/web/src/a.ts')]:
            self.assertEqual(self.region_names(path),
                             {app + '-' + kind for kind in ('lint', 'types', 'tests', 'build')} | {'scrub', 'links', 'theme-check', 'theme-colors'} | set(pre_merge.IMAGE_REGIONS))

    def test_focused_frontend_keeps_serial_performance_but_backend_does_not(self):
        import touched_tests
        suites = pre_merge.matrix('go', 2)
        for path in ('runner/frontend/src/hooks/useQuestionItems.ts', 'packages/chat/src/model.ts'):
            selected = touched_tests.select(suites, pre_merge.ROOT, [path], packages=[])
            performance = [command for name, _, command in selected if name == 'runner-performance']
            self.assertEqual(performance, [['npm', 'run', 'test:performance']])
        selected = touched_tests.select(suites, pre_merge.ROOT, ['runner/internal/server/api.go'], packages=[])
        self.assertNotIn('runner-performance', [name for name, _, _ in selected])

    def test_focused_performance_keeps_final_widths_and_propagates_failure(self):
        for code in (0, 1):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory:
                logs = Path(directory) / 'logs'
                def run(command, **kwargs):
                    self.assertEqual(kwargs['env']['VITE_BLERG_TEST_PHASE'], 'final')
                    return type('Result', (), {'returncode': code})()
                with patch('sys.argv', ['pre_merge', '--changed-since', 'HEAD', '--logs', str(logs)]), \
                        patch('touched_tests.changed_files', return_value=['runner/frontend/src/hooks/useQuestionItems.ts']), \
                        patch.object(pre_merge, 'matrix', return_value=[('runner-performance', 'runner/frontend', ['npm', 'run', 'test:performance'])]), \
                        patch.object(pre_merge, 'run_suite', side_effect=run), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), code)
                report = json.loads((logs / 'summary.json').read_text())
                self.assertEqual([(row['suite'], row['exit_code']) for row in report['suites']], [('runner-performance', code)])

    def test_theme_colors_runs_for_web_changes_only(self):
        self.assertIn('theme-colors', self.region_names('brand/tokens.css'))
        self.assertIn('theme-colors', self.region_names('packages/chat/src/theme/brand.css'))
        self.assertNotIn('theme-colors', self.region_names('runner/internal/server/a.go'))

    def test_images_selected_for_each_build_input(self):
        for path in ('core/web/src/a.ts', 'board/web/src/a.ts',
                     'runner/frontend/src/a.ts', 'packages/chat/src/a.ts',
                     'packages/settings/a.ts', 'brand/a.css', 'core/Dockerfile',
                     'board/Dockerfile', 'runner/Dockerfile.server'):
            with self.subTest(path=path):
                self.assertTrue(set(pre_merge.IMAGE_REGIONS) <= self.region_names(path))
        self.assertFalse(set(pre_merge.IMAGE_REGIONS) & self.region_names('docs/a.md'))

    def test_image_suites_use_the_go_command(self):
        for name, _, command in pre_merge.matrix('go', 2):
            if name in pre_merge.IMAGE_REGIONS:
                self.assertEqual(command[:3], ['go', 'run', './contracts/cmd/image-build/main.go'])

    def test_images_share_a_scheduler_resource(self):
        for name in pre_merge.IMAGE_REGIONS:
            self.assertIn('image-build', pre_merge.suite_resources((name, '.', ['build'])))

    def test_image_timeout_cleans_up_and_keeps_report(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            with patch('sys.argv', ['pre_merge', '--suite', 'core-image', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'run_suite', side_effect=pre_merge.subprocess.TimeoutExpired('build', 3600)), \
                    patch.object(pre_merge.subprocess, 'run') as cleanup, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            cleanup.assert_called_once()
            self.assertIn('-cleanup', cleanup.call_args.args[0])
            row = json.loads((logs / 'summary.json').read_text())['suites'][0]
            self.assertEqual(row['exit_code'], 124)
            self.assertEqual(row['outcome'], 'fail')

    def test_docker_unavailable_is_named_skip_with_timing(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            def run(command, **kwargs):
                kwargs['stdout'].write('SKIP core-image: Docker is unavailable\n')
                return pre_merge.subprocess.CompletedProcess(command, 1)  # go run wraps exit 77
            with patch('sys.argv', ['pre_merge', '--suite', 'core-image', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            row = json.loads((logs / 'summary.json').read_text())['suites'][0]
            self.assertEqual(row['outcome'], 'skip')
            self.assertIn('Docker is unavailable', row['reason'])
            self.assertGreaterEqual(row['seconds'], 0)

    def test_shared_frontend_regions_widen_to_consumers(self):
        self.assertEqual(self.region_names('packages/chat/src/a.ts'),
                         self.region_names('packages/chat/src/a.ts', 'runner/frontend/src/a.ts', 'board/web/src/a.ts'))
        self.assertNotIn('core-tests', self.region_names('packages/chat/src/a.ts'))
        for path in ('packages/settings/a.ts', 'brand/tokens.css'):
            names = self.region_names(path)
            self.assertTrue({'core-tests', 'board-tests', 'chat-tests', 'runner-browser'} <= names)
            self.assertNotIn('runner-go', names)

    def test_go_region_and_scoped_formatting(self):
        for module in ('core', 'board', 'runner'):
            names = self.region_names(module + '/internal/a.go')
            self.assertTrue({module + '-go', module + '-vet', module + '-go-lint', 'gofmt', 'lint-pin'} <= names)
            self.assertFalse(any(name.endswith(('-types', '-build', '-browser')) for name in names))
        suites, _, _ = pre_merge.select_changed(pre_merge.matrix('go', 2), ['board/internal/a.go'])
        command = next(command for name, _, command in suites if name == 'gofmt')
        self.assertIn("['./board']", command[-1])
        self.assertNotIn('./runner', command[-1])
        self.assertTrue({module + '-go' for module in ('contracts', 'core', 'board', 'runner')} <=
                        self.region_names('contracts/token.go'))

    def test_docs_desktop_cli_and_mixed_changes(self):
        self.assertEqual(self.region_names('docs/guide.md', 'CONTRIBUTING.md', 'runner/README.md'), {'scrub', 'links', 'theme-check'})
        self.assertEqual(self.region_names('install/desktop/start.sh'), {'desktop-scripts', 'scrub', 'links', 'theme-check'})
        self.assertEqual(self.region_names('runner/.claude/skills/session-messaging/blerg_runner.py'),
                         {'cli-unittest', 'cli-pytest', 'scrub', 'links', 'theme-check'})
        self.assertEqual(self.region_names('core/internal/a.go', 'board/web/src/a.ts'),
                         self.region_names('core/internal/a.go') | self.region_names('board/web/src/a.ts'))
        self.assertEqual(self.region_names(), {'scrub', 'links', 'theme-check'})
        self.assertEqual(self.region_names('runner/internal/clock.go'), self.region_names('runner/internal/a.go'))

    def test_global_and_unknown_paths_select_everything(self):
        all_names = {name for name, _, _ in pre_merge.matrix('go', 2)}
        for path in ('core/go.mod', 'runner/go.sum', 'board/web/package-lock.json',
                     'packages/chat/yarn.lock', 'runner/frontend/pnpm-lock.yaml',
                     'go.work', 'scripts/pre_merge.py', 'contracts/cmd/image-build/main.go', '.github/workflows/ci.yml',
                     'Makefile', '.golangci.yml', 'new-region/source.xyz'):
            with self.subTest(path=path):
                self.assertEqual(self.region_names(path), all_names)

    def test_every_matrix_suite_has_a_reachable_region(self):
        suites = pre_merge.matrix('go', 2)
        reached = set()
        for prefix, _ in pre_merge.PATH_REGIONS:
            reached |= self.region_names(prefix + 'example.txt')
        # Gate-only scripts deliberately run only for global changes. Requiring
        # a registered region as well prevents a new suite hiding in that fallback.
        reached |= {name for name, _, _ in suites if pre_merge.suite_regions(name) == {'gate'}}
        self.assertEqual(reached, {name for name, _, _ in suites})
        for name, _, _ in suites:
            self.assertTrue(pre_merge.suite_regions(name), name)
        self.assertEqual(self.region_names('scripts/pre_merge.py'), {name for name, _, _ in suites})

    def test_changed_report_records_skips_without_passes_and_final_browser_phase(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            def run(command, **kwargs):
                self.assertEqual(kwargs['env']['VITE_BLERG_TEST_PHASE'], 'final')
                return type('Result', (), {'returncode': 0})()
            output = io.StringIO()
            with patch('sys.argv', ['pre_merge', '--changed', 'HEAD', '--logs', str(logs)]), \
                    patch('touched_tests.changed_files', return_value=['runner/frontend/src/a.ts']), \
                    patch.object(pre_merge, 'run_suite', side_effect=run), contextlib.redirect_stdout(output):
                self.assertEqual(pre_merge.main(), 0)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertFalse(report['full_matrix'])
            self.assertTrue(report['complete'])
            self.assertTrue(report['skipped_suites'])
            self.assertTrue(all(row['outcome'] == 'skip' and 'exit_code' not in row for row in report['skipped_suites']))
            self.assertLess(output.getvalue().index('SKIP core-go:'), output.getvalue().index('Running '))

    def test_release_ignores_changed_and_focused_selection(self):
        with patch('sys.argv', ['pre_merge', '--release', '--changed', 'HEAD', '--suite', 'daemon-race', '--list']), \
                patch('touched_tests.changed_files') as changed, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(pre_merge.main(), 0)
        changed.assert_not_called()
        self.assertIn('RUN  core-go', output.getvalue())
        self.assertIn('RUN  runner-browser', output.getvalue())

    def test_runtime_smoke_counts_preserve_named_skips(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'host.jsonl').write_text(json.dumps(dict(Action='pass', Test='TestAcceptanceHostSmoke', Elapsed=2)) + '\n')
            (root / 'sandbox.jsonl').write_text(json.dumps(dict(Action='skip', Test='TestAcceptanceSandboxSmoke', Elapsed=0)) + '\n')
            result = pre_merge.runtime_smoke_results(root)
            self.assertEqual(result['counts'], {'pass': 1, 'fail': 0, 'skip': 1})
            self.assertEqual([r['test'] for r in result['slowest']],
                             ['TestAcceptanceHostSmoke', 'TestAcceptanceSandboxSmoke'])

    def test_runtime_smoke_selects_only_local_runtimes(self):
        suites = {name: command for name, _, command in pre_merge.matrix('go', 2)}
        self.assertEqual(suites['runtime-smoke-local'],
                         ['go', 'run', './cmd/runtime-smoke', '-go', 'go', '-runtimes', 'host,sandbox'])

    def test_release_retains_three_daemon_passes(self):
        suites = {name: command for name, _, command in pre_merge.matrix('go', 2, release=True)}
        self.assertIn('-count=3', suites['daemon-race'])

    def test_parallel_scheduler_respects_budget_and_barriers(self):
        import threading
        import time
        suites = [('runner-performance', '.', []), ('runner-go', 'runner', []),
                  ('daemon-race', 'runner', []), ('runner-unit', 'runner/frontend', []),
                  ('runner-browser', 'runner/frontend', []), ('host-tracing-preflight', '.', [])]
        active, peak, order = set(), [], []
        lock = threading.Lock()
        overlap = threading.Barrier(2)
        def execute(suite):
            name = suite[0]
            with lock:
                if name in ('host-tracing-preflight', 'runner-performance'):
                    self.assertFalse(active)
                active.add(name)
                self.assertFalse({'runner-unit', 'runner-browser'} <= active)
                peak.append(len(active))
                order.append(name)
            if name in ('runner-go', 'daemon-race'):
                overlap.wait(timeout=2)
            time.sleep(.01)
            with lock:
                active.remove(name)
            return name
        results = list(pre_merge.scheduled_results(suites, 2, execute, lambda: False))
        self.assertEqual(order[0], 'host-tracing-preflight')
        self.assertEqual(order[-1], 'runner-performance')
        self.assertEqual(max(peak), 2)
        self.assertEqual(sorted(results), sorted(s[0] for s in suites))

    def test_scheduler_drains_active_work_without_admitting_after_stop(self):
        import threading
        ready = threading.Barrier(2)
        release = threading.Event()
        stopped = False
        def execute(suite):
            ready.wait(timeout=2)
            if suite[0] == 'slow':
                release.wait(timeout=2)
            return suite[0]
        suites = [(name, '.', []) for name in ('failed', 'slow', 'never')]
        results = pre_merge.scheduled_results(suites, 2, execute, lambda: stopped)
        self.assertEqual(next(results), 'failed')
        stopped = True
        release.set()
        self.assertEqual(list(results), ['slow'])

    def test_lint_processes_share_a_scheduler_resource(self):
        self.assertEqual(pre_merge.suite_resources(('core-go-lint', 'core', [])),
                         pre_merge.suite_resources(('runner-go-lint', 'runner', [])))
        self.assertFalse(pre_merge.suite_resources(('runner-go', 'runner', [])))

    def test_full_ui_suites_and_chat_build_share_resources(self):
        resources = pre_merge.suite_resources
        browser = resources(('runner-browser', 'runner/frontend', ['npm', 'test', '--', '--project', 'browser']))
        board = resources(('board-tests', 'board/web', ['npm', 'test']))
        self.assertTrue(browser & board)
        build = resources(('chat-build', 'packages/chat', ['npm', 'run', 'build']))
        self.assertTrue(build & browser)
        self.assertTrue(build & board)
        self.assertFalse(browser & resources(('board-types', 'board/web', ['npx', 'tsc'])))

    def test_release_path_uses_release_flag(self):
        command = pre_merge.subprocess.check_output(
            ['make', '-n', 'test-daemon-race'], cwd=pre_merge.ROOT, text=True)
        self.assertIn('--release --suite daemon-race', command)

    def test_theme_check_suite_actually_runs_from_the_repo_root(self):
        # A regression test for a real bug: the suite's command did `cd scripts/themecheck` and
        # then passed `..`, which resolves to scripts/ (not the repo root) from there, so the
        # checker failed to open brand/tokens.css on every run. This executes the exact suite
        # command pre_merge.py would run, not a string match on it.
        name, cwd, command = next((n, c, cmd) for n, c, cmd in pre_merge.matrix('go', 2) if n == 'theme-check')
        result = pre_merge.subprocess.run(command, cwd=pre_merge.ROOT / cwd, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_scheduler_refills_before_the_long_suite_finishes(self):
        import threading
        short_done = threading.Event()
        def execute(suite):
            if suite[0] == 'long':
                self.assertTrue(short_done.wait(timeout=2))
            if suite[0] == 'replacement':
                short_done.set()
            return suite[0]
        suites = [(name, '.', []) for name in ('long', 'short', 'replacement')]
        self.assertEqual(sorted(pre_merge.scheduled_results(suites, 2, execute, lambda: False)),
                         ['long', 'replacement', 'short'])

    def test_memory_budget_uses_cgroup_headroom(self):
        values = {'/proc/meminfo': 'MemAvailable: 33554432 kB',
                  '/sys/fs/cgroup/memory.max': str(8 * 1024**3),
                  '/sys/fs/cgroup/memory.current': str(3 * 1024**3)}
        def read(path):
            if str(path) not in values:
                raise FileNotFoundError
            return values[str(path)]
        with patch.object(Path, 'read_text', read):
            self.assertEqual(pre_merge.memory_budget(), 5 * 1024**3)
        with patch.object(Path, 'read_text', side_effect=OSError):
            self.assertEqual(pre_merge.parallel_slots(32, 6, pre_merge.memory_budget()), 1)

    def test_cancellation_kills_suite_process_group(self):
        import threading
        import time
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'survived'
            child = 'import pathlib,time; time.sleep(.5); pathlib.Path(' + repr(str(marker)) + ').touch()'
            parent = 'import subprocess,sys,time; subprocess.Popen([sys.executable,"-c",' + repr(child) + ']); time.sleep(60)'
            cancelled = threading.Event()
            timer = threading.Timer(.2, cancelled.set)
            timer.start()
            try:
                with self.assertRaises(KeyboardInterrupt):
                    pre_merge.run_suite([pre_merge.sys.executable, '-c', parent], timeout=10,
                                        cancel_event=cancelled)
            finally:
                timer.join()
            time.sleep(.6)
            self.assertFalse(marker.exists())

    def test_memory_and_cpu_bound_concurrency(self):
        self.assertEqual(pre_merge.parallel_slots(32, 6, 31 * 1024**3), 5)
        self.assertEqual(pre_merge.parallel_slots(32, 6, 5 * 1024**3), 1)
        self.assertEqual(pre_merge.parallel_slots(2, 2, 31 * 1024**3), 1)

    def test_deadline_kills_a_descendant_that_ignores_termination(self):
        import time
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'survived'
            child = 'import pathlib,time; time.sleep(.5); pathlib.Path(' + repr(str(marker)) + ').touch()'
            parent = 'import subprocess,sys,time; subprocess.Popen([sys.executable,"-c",' + repr(child) + ']); print("started",flush=True); time.sleep(60)'
            with (Path(directory) / 'output').open('w') as output:
                with self.assertRaises(pre_merge.subprocess.TimeoutExpired):
                    pre_merge.run_suite([pre_merge.sys.executable, '-c', parent], timeout=.2, stdout=output)
            time.sleep(.6)
            self.assertFalse(marker.exists())

    def test_every_suite_has_a_finite_deadline(self):
        for name, _, _ in pre_merge.matrix('go', 2):
            self.assertGreater(pre_merge.suite_timeout(name), 0)
            self.assertLessEqual(pre_merge.suite_timeout(name), 5400)

    def test_timeout_names_the_suite_and_continues(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            suites = [('hang', '.', ['hang']), ('next', '.', ['next'])]
            def run(command, **kwargs):
                if command == ['hang']:
                    raise pre_merge.subprocess.TimeoutExpired(command, kwargs['timeout'])
                return pre_merge.subprocess.CompletedProcess(command, 0)
            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict(os.environ, {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=suites), \
                    patch.object(pre_merge, 'run_suite', side_effect=run), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertEqual([row['exit_code'] for row in report['suites']], [124, 0])
            self.assertIn('hang timed out', (logs / 'hang.log').read_text())

    def test_go_commands_do_not_inherit_the_gate_control_descriptor(self):
        from ui_suite_lock import ui_suite_lock
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lock = root / 'gate.lock'
            go = root / 'go-versioned'
            go.write_text('#!' + pre_merge.sys.executable + '\n'
                          'import os, pathlib\n'
                          'target = pathlib.Path(os.environ["BLERG_UI_SUITE_LOCK"]).stat()\n'
                          'for entry in pathlib.Path("/proc/self/fd").iterdir():\n'
                          ' try: found = os.fstat(int(entry.name))\n'
                          ' except OSError: continue\n'
                          ' if (found.st_dev, found.st_ino) == (target.st_dev, target.st_ino): raise SystemExit(9)\n')
            go.chmod(0o700)
            with ui_suite_lock(path=lock), \
                    patch.dict('os.environ', {'BLERG_UI_SUITE_LOCK': str(lock), 'GO': str(go)}), \
                    patch('sys.argv', ['pre_merge', '--suite', 'fixture-go', '--logs', str(root / 'logs')]), \
                    patch.object(pre_merge, 'matrix', return_value=[('fixture-go', '.', [str(go), 'test'])]), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)

    def test_ordinary_go_tests_reuse_cache_but_race_and_wire_proofs_stay_fresh(self):
        suites = {name: command for name, _, command in pre_merge.matrix('go', 4)}
        for module in ('contracts', 'core', 'board', 'runner'):
            self.assertFalse(any(arg.startswith('-count=') for arg in suites[f'{module}-go']))
        self.assertIn('-count=1', suites['daemon-race'])
        for name in ('runtime-race', 'persistent-shell-wire-go', 'persistent-shell-wire-race'):
            self.assertIn('-count=1', suites[name])

    def test_outer_gate_lock_survives_a_non_browser_subprocess(self):
        from ui_suite_lock import ui_suite_lock
        real_run = pre_merge.run_suite
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            lock = Path(directory) / 'gate.lock'
            code = 'from ui_suite_lock import ui_suite_lock\nwith ui_suite_lock(): print("nested")'
            suites = [('nested', '.', [pre_merge.sys.executable, '-c', code])]
            def bounded_run(*args, **kwargs):
                kwargs['timeout'] = 2
                return real_run(*args, **kwargs)
            with ui_suite_lock(path=lock), \
                    patch.dict('os.environ', {'BLERG_UI_SUITE_LOCK': str(lock), 'PYTHONPATH': str(pre_merge.ROOT / 'scripts')}), \
                    patch('sys.argv', ['pre_merge', '--suite', 'nested', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'matrix', return_value=suites), \
                    patch.object(pre_merge, 'run_suite', side_effect=bounded_run), \
                    patch.object(pre_merge, 'start_record', return_value={}), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)
            self.assertEqual((logs / 'nested.log').read_text().strip(), 'nested')

    def test_gate_sets_its_browser_phase_instead_of_inheriting_a_reduced_matrix(self):
        for focused, inherited, expected in ((False, 'touched', 'final'), (True, 'final', 'touched')):
            with self.subTest(focused=focused), tempfile.TemporaryDirectory() as directory:
                logs = Path(directory) / 'logs'
                argv = ['pre_merge', '--logs', str(logs)] + (['--changed-since', 'HEAD'] if focused else [])
                suites = [('runner-unit', '.', ['unit'])]
                def run(command, **kwargs):
                    self.assertEqual(kwargs['env']['VITE_BLERG_TEST_PHASE'], expected)
                    self.assertEqual(kwargs['env']['BLERG_KEEPER_UID_REQUIRED'], '1')
                    return type('Result', (), {'returncode': 0})()
                with patch('sys.argv', argv), \
                        patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture',
                                                  'VITE_BLERG_TEST_PHASE': inherited}), \
                        patch.object(pre_merge, 'matrix', return_value=suites), \
                        patch('touched_tests.changed_files', return_value=[]), \
                        patch('touched_tests.select', return_value=suites), \
                        patch.object(pre_merge, 'run_suite', side_effect=run), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), 0)

    def test_go_results_distinguish_cached_packages_from_executed_tests(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'go.log'
            events = [dict(Action='pass', Package='fixture', Test='TestOne', Elapsed=4),
                      dict(Action='output', Package='fixture', Output='ok fixture (cached)\n'),
                      dict(Action='pass', Package='fixture', Elapsed=0)]
            path.write_text('\n'.join(json.dumps(event) for event in events))
            results = pre_merge.go_results(path)
            self.assertEqual(results['counts'], {'pass': 1, 'fail': 0, 'skip': 0})
            self.assertEqual(results['cached_packages'], ['fixture'])

    def test_database_modules_use_the_measured_package_budget(self):
        for workers in (1, 2, 4, 6):
            with self.subTest(workers=workers):
                suites = {name: command for name, _, command in pre_merge.matrix('go', workers)}
                for module in ('core', 'board'):
                    self.assertIn(f'-p={min(2, workers)}', suites[f'{module}-go'])
                    self.assertIn('-parallel=4', suites[f'{module}-go'])
                for name in ('contracts-go', 'runner-go', 'daemon-race', 'runtime-race'):
                    self.assertIn(f'-p={workers}', suites[name])

    def test_runner_plain_packages_excludes_what_the_race_suites_already_cover(self):
        go = str(Path.home() / 'go/bin/go')
        if not Path(go).is_file():
            self.skipTest('no go toolchain at ~/go/bin/go')
        packages = pre_merge.runner_plain_packages(go)
        self.assertNotIn('./...', packages)
        for excluded in pre_merge.RACE_ONLY_PACKAGES:
            self.assertNotIn(excluded.replace('./', 'github.com/blerglab/blerg-ai/runner/'), packages)
        self.assertTrue(any('runner/internal/db' in p for p in packages))

    def test_runner_plain_packages_falls_back_to_everything_without_a_usable_go(self):
        self.assertEqual(pre_merge.runner_plain_packages('/no/such/go'), ['./...'])

    def test_runner_go_drops_race_only_packages_when_daemon_and_runtime_race_also_run(self):
        import threading
        go = str(Path.home() / 'go/bin/go')
        if not Path(go).is_file():
            self.skipTest('no go toolchain at ~/go/bin/go')
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            lock = threading.Lock()
            seen = []
            def capture(command, **kwargs):
                with lock:
                    seen.append(list(command))
                return pre_merge.subprocess.CompletedProcess(command, 0)
            with patch('sys.argv', ['pre_merge', '--suite', 'runner-go', '--suite', 'daemon-race',
                                    '--suite', 'runtime-race', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'run_suite', side_effect=capture), \
                    patch.dict(os.environ, {'GO': go}), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)
            runner_go_command = next(c for c in seen if '-race' not in c)
            self.assertNotIn('./...', runner_go_command)
            self.assertNotIn('./internal/daemon', runner_go_command)
            self.assertNotIn('./internal/ownershell', runner_go_command)
            race_commands = [c for c in seen if '-race' in c]
            self.assertEqual(len(race_commands), 2)

    def test_runner_go_keeps_everything_when_race_suites_are_not_also_selected(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            seen = []
            def capture(command, **kwargs):
                seen.append(list(command))
                return pre_merge.subprocess.CompletedProcess(command, 0)
            with patch('sys.argv', ['pre_merge', '--suite', 'runner-go', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'run_suite', side_effect=capture), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)
            self.assertIn('./...', seen[0])

    def test_gate_uses_the_supplied_budget_up_to_six_workers(self):
        for budget, expected in (('1', 1), ('4', 4), ('6', 6), ('32', 6)):
            with self.subTest(budget=budget), patch('sys.argv', ['pre_merge', '--list']), \
                    patch.dict('os.environ', {'BLERG_TOOL_WORKERS': budget}), \
                    patch.object(pre_merge, 'matrix', return_value=[]) as matrix, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)
                self.assertEqual(matrix.call_args.args[1], expected)

    def test_scheduling_cache_failure_preserves_the_test_exit_status(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            suites = [('runner-unit', 'runner/frontend', ['npm', 'test', '--', '--project', 'unit'])]
            with patch('sys.argv', ['pre_merge', '--suite', 'runner-unit', '--logs', str(logs)]), \
                    patch.object(pre_merge, 'matrix', return_value=suites), \
                    patch('vitest_results_cache.sync', side_effect=OSError('cache unavailable')), \
                    patch.object(pre_merge, 'run_suite', return_value=type('Result', (), {'returncode': 7})()), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertEqual(report['suites'][0]['exit_code'], 7)
            self.assertTrue(report['complete'])

    def test_touched_mode_never_claims_full_matrix_or_requires_tracing_for_unit(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            with patch('sys.argv', ['pre_merge', '--changed-since', 'HEAD', '--logs', str(logs)]), \
                    patch('touched_tests.changed_files', return_value=['runner/frontend/src/example.ts']), \
                    patch('touched_tests.select', return_value=[('runner-unit', '.', ['unit'])]), \
                    patch.object(pre_merge, 'run_suite', return_value=type('Result', (), {'returncode': 0})()), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertFalse(report['full_matrix'])
            self.assertTrue(report['complete'])
            self.assertEqual([row['suite'] for row in report['suites']], ['runner-unit'])

    def test_matrix_preserves_ci_checks_and_caps_test_workers(self):
        suites = {name: (cwd, command) for name, cwd, command in pre_merge.matrix('go', 2)}
        for module in ('contracts', 'core', 'board', 'runner'):
            for kind in ('vet', 'go', 'go-lint'):
                self.assertIn(f'{module}-{kind}', suites)
            self.assertIn('-p=2', suites[f'{module}-go'][1])
        for app in ('core', 'board', 'chat', 'runner'):
            for kind in ('lint', 'types', 'build'):
                self.assertIn(f'{app}-{kind}', suites)
        for name in ('core-tests', 'board-tests', 'chat-tests', 'runner-unit', 'runner-browser'):
            self.assertIn('--maxWorkers=2', suites[name][1])
        self.assertIn('-count=1', suites['daemon-race'][1])
        self.assertIn('runner-performance', suites)
        self.assertIn('cli-unittest', suites)
        self.assertIn('cli-pytest', suites)
        self.assertIn('scripts', suites)

    def test_persistent_wire_runs_fresh_normal_and_race_with_explicit_tag(self):
        suites = {name: (cwd, command) for name, cwd, command in pre_merge.matrix('go', 1)}
        for name in ('persistent-shell-wire-go', 'persistent-shell-wire-race'):
            cwd, command = suites[name]
            self.assertEqual(cwd, 'runner')
            for arg in ('-tags=persistent_shell_test', '-count=1', '-json', '-p=1',
                        '-parallel=1', './internal/server',
                        '-run=^TestPersistentShellRealWireReconnectAndWithdrawal$'):
                self.assertIn(arg, command)
            self.assertEqual('-race' in command, name.endswith('-race'))

    def test_wire_suite_requires_the_named_test_to_pass_instead_of_skip_or_missing(self):
        for action in ('pass', 'skip', 'fail', None, 'duplicate'):
            with self.subTest(action=action), tempfile.TemporaryDirectory() as directory:
                logs = Path(directory) / 'logs'

                def run(command, **kwargs):
                    if action:
                        event = {'Action': 'pass' if action == 'duplicate' else action,
                                 'Test': 'TestPersistentShellRealWireReconnectAndWithdrawal'}
                        kwargs['stdout'].write((json.dumps(event) + '\n') * (2 if action == 'duplicate' else 1))
                    return type('Result', (), {'returncode': 0})()

                with patch('sys.argv', ['pre_merge', '--suite', 'persistent-shell-wire-go', '--logs', str(logs)]), \
                        patch.object(pre_merge, 'matrix', return_value=[('persistent-shell-wire-go', '.', ['go'])]), \
                        patch.object(pre_merge, 'run_suite', side_effect=run), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), int(action != 'pass'))
                report = json.loads((logs / 'summary.json').read_text())
                self.assertEqual(report['suites'][0]['exit_code'], int(action != 'pass'))

    def test_failure_keeps_running_and_saves_both_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            suites = [('failed', '.', ['failure']), ('passed', '.', ['success'])]

            def run(command, **kwargs):
                kwargs['stdout'].write('full suite output\n')
                return type('Result', (), {'returncode': int(command[0] == 'failure')})()

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=suites), \
                    patch.object(pre_merge, 'run_suite', side_effect=run), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertTrue(report['complete'])
            self.assertEqual([row['exit_code'] for row in report['suites']], [1, 0])
            self.assertEqual((logs / 'passed.log').read_text(), 'full suite output\n')

    def test_app_server_test_failure_is_not_a_resource_failure(self):
        events = [
            {'Action': 'output', 'Package': 'fixture', 'Test': 'TestExpectedStartupFailure',
             'Output': 'Codex app-server could not start: fixture startup rejection'},
            {'Action': 'pass', 'Package': 'fixture', 'Test': 'TestExpectedStartupFailure'},
            {'Action': 'output', 'Package': 'fixture', 'Test': 'TestRace',
             'Output': 'WARNING: DATA RACE'},
            {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestRace'},
        ]
        output = '\n'.join(json.dumps(event) for event in events)
        self.assertFalse(pre_merge.has_resource_failure(output))

    def test_race_is_named_and_does_not_hide_later_suites(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            events = [
                {'Action': 'output', 'Package': 'fixture', 'Test': 'TestStartup',
                 'Output': 'Codex app-server could not start: fixture rejection'},
                {'Action': 'pass', 'Package': 'fixture', 'Test': 'TestStartup'},
                {'Action': 'output', 'Package': 'fixture', 'Test': 'TestRace',
                 'Output': 'WARNING: DATA RACE'},
                {'Action': 'output', 'Package': 'fixture', 'Test': 'TestRace',
                 'Output': 'claude-code: process exited: generation=1 pid=123 status=signal: killed intentional=false driver_kill=true shutdown=true'},
                {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestRace'},
            ]
            def run(command, **kwargs):
                if command == ['one']:
                    kwargs['stdout'].write('\n'.join(json.dumps(event) for event in events))
                return pre_merge.subprocess.CompletedProcess(command, int(command == ['one']))
            captured = io.StringIO()
            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict(os.environ, {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=[('one-go', '.', ['one']), ('next', '.', ['next'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                    contextlib.redirect_stdout(captured):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 2)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertTrue(report['complete'])
            self.assertIn('data race reported', report['suites'][0]['failure_detail'])
            self.assertIn('TestRace', captured.getvalue())

    def test_owned_shutdown_log_is_not_a_resource_failure_without_test_scope(self):
        diagnostic = ('claude-code: process exited: generation=1 pid=123 '
                      'status=signal: killed intentional=false driver_kill=true shutdown=true\n')
        events = [
            {'Action': 'output', 'Package': 'fixture', 'Output': diagnostic},
            {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestOther'},
        ]
        self.assertFalse(pre_merge.has_resource_failure('\n'.join(json.dumps(event) for event in events)))

    def test_resource_diagnostic_reports_the_actual_cause(self):
        self.assertIn('cannot allocate memory', pre_merge.resource_failure('fork: cannot allocate memory'))

    def test_resource_failure_stops_without_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'

            def run(command, **kwargs):
                kwargs['stdout'].write('fork: Resource temporarily unavailable\n')
                return type('Result', (), {'returncode': 1})()

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture',
                                              'BLERG_TOOL_WORKERS': '1'}), \
                    patch.object(pre_merge, 'matrix', return_value=[('one', '.', ['one']), ('two', '.', ['two'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 1)
            self.assertFalse(json.loads((logs / 'summary.json').read_text())['complete'])

    def test_expected_child_kill_in_passing_go_test_does_not_hide_later_suites(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            events = [
                {'Action': 'output', 'Package': 'fixture', 'Test': 'TestKillChild',
                 'Output': 'process exited: signal: killed driver_kill=true\n'},
                {'Action': 'pass', 'Package': 'fixture', 'Test': 'TestKillChild'},
                {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestOther'},
            ]

            def run(command, **kwargs):
                if command == ['one']:
                    kwargs['stdout'].write('\n'.join(json.dumps(event) for event in events))
                return type('Result', (), {'returncode': int(command == ['one'])})()

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=[('one-go', '.', ['one']), ('two', '.', ['two'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 2)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertTrue(report['complete'])
            self.assertEqual([row['exit_code'] for row in report['suites']], [1, 0])

    def test_expected_process_limit_does_not_hide_later_tracing_suites(self):
        package = 'github.com/blerglab/blerg-ai/runner/internal/processcontainment'
        diagnostic = ('2026/01/01 12:00:00 writer helper pid=123: '
                      'blerg: process limit reached (4096 tasks); retry with less '
                      'parallelism (GOMAXPROCS, go -p, make -j, test workers).\n')
        events = [
            {'Action': 'output', 'Package': package,
             'Test': 'TestForkBombDoesNotEndTheSession', 'Output': diagnostic},
            {'Action': 'pass', 'Package': package, 'Test': 'TestForkBombDoesNotEndTheSession'},
            {'Action': 'fail', 'Package': 'daemon',
             'Test': 'TestActualFileRepliesShareShellControlAndDataScheduler'},
        ]
        output = '\n'.join(json.dumps(event) for event in events)
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'

            def run(command, **kwargs):
                if command == ['runner']:
                    kwargs['stdout'].write(output)
                return pre_merge.subprocess.CompletedProcess(command, int(command == ['runner']))

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict(os.environ, {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=[
                        ('runner-go', '.', ['runner']), ('tracing', '.', ['tracing'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 2)
            self.assertTrue(json.loads((logs / 'summary.json').read_text())['complete'])
        # Missing proof, a failed repetition, or a genuine runtime refusal must stop.
        for receipt in ('fail', 'skip', None):
            unproven = [events[0]]
            if receipt:
                unproven.append(dict(events[1], Action=receipt))
            self.assertTrue(pre_merge.has_resource_failure(
                '\n'.join(json.dumps(event) for event in unproven)))
        self.assertTrue(pre_merge.has_resource_failure(output + '\n' + json.dumps(
            dict(events[1], Action='fail'))))
        self.assertTrue(pre_merge.has_resource_failure(diagnostic))
        self.assertTrue(pre_merge.has_resource_failure(output + '\nfork: Cannot fork'))

    def test_gitignore_selects_no_product_suites(self):
        suites, skipped, _ = pre_merge.select_changed(pre_merge.matrix('go', 2), ['.gitignore'])
        self.assertNotIn('runner-go', [suite[0] for suite in suites])

    def test_a_test_name_about_the_limit_is_not_a_resource_failure(self):
        self.assertFalse(pre_merge.has_resource_failure(
            'stderr | ChatView.notices.test.tsx > the process limit line is attributed to Blerg'))

    def test_killed_suite_or_node_oom_stops_without_retry(self):
        for code, diagnostic in [(-9, ''), (137, ''), (1, 'signal: killed'), (134, 'FATAL ERROR: JavaScript heap out of memory')]:
            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory:
                logs = Path(directory) / 'logs'

                def run(command, **kwargs):
                    kwargs['stdout'].write(diagnostic)
                    return type('Result', (), {'returncode': code})()

                with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                        patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture',
                                                  'BLERG_TOOL_WORKERS': '1'}), \
                        patch.object(pre_merge, 'matrix', return_value=[('one', '.', ['one']), ('two', '.', ['two'])]), \
                        patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), 1)
                    self.assertEqual(call.call_count, 1)
                self.assertFalse(json.loads((logs / 'summary.json').read_text())['complete'])

    def test_go_json_never_hides_unproven_kills_or_resource_diagnostics(self):
        for outcome, diagnostic in [('fail', 'signal: killed'),
                                    ('skip', 'signal: killed'),
                                    ('pass', 'fork: Resource temporarily unavailable'),
                                    ('pass', 'writer admission closed')]:
            with self.subTest(outcome=outcome, diagnostic=diagnostic):
                events = [
                    {'Action': 'output', 'Package': 'fixture', 'Test': 'TestChild',
                     'Output': diagnostic},
                    {'Action': outcome, 'Package': 'fixture', 'Test': 'TestChild'},
                ]
                self.assertTrue(pre_merge.has_resource_failure('\n'.join(json.dumps(event) for event in events)))
        repeated = [
            {'Action': 'output', 'Package': 'fixture', 'Test': 'TestChild', 'Output': 'signal: killed'},
            {'Action': 'pass', 'Package': 'fixture', 'Test': 'TestChild'},
            {'Action': 'fail', 'Package': 'fixture', 'Test': 'TestChild'},
        ]
        self.assertTrue(pre_merge.has_resource_failure('\n'.join(json.dumps(event) for event in repeated)))

    def test_resource_failure_on_last_suite_is_still_incomplete(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'

            def run(command, **kwargs):
                kwargs['stdout'].write('fork: Resource temporarily unavailable\n')
                return type('Result', (), {'returncode': 1})()

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=[('only', '.', ['only'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
            self.assertFalse(json.loads((logs / 'summary.json').read_text())['complete'])

    def test_tracing_preflight_failure_marks_gate_incomplete(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            suites = [('host-tracing-preflight', '.', ['probe']), ('later', '.', ['later'])]
            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture'}), \
                    patch.object(pre_merge, 'matrix', return_value=suites), \
                    patch.object(pre_merge, 'run_suite', return_value=type('Result', (), {'returncode': 1})()) as call, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 1)
            self.assertFalse(json.loads((logs / 'summary.json').read_text())['complete'])

    def test_containment_failure_stops_and_records_time(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'

            def run(command, **kwargs):
                kwargs['stdout'].write('writer admission closed\n')
                return type('Result', (), {'returncode': 1})()

            with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                    patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture', 'BLERG_TOOL_WORKERS': '1'}), \
                    patch.object(pre_merge, 'matrix', return_value=[('one', '.', ['one']), ('two', '.', ['two'])]), \
                    patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 1)
                self.assertEqual(call.call_count, 1)
            report = json.loads((logs / 'summary.json').read_text())
            self.assertFalse(report['complete'])
            self.assertEqual(report['stopped_reason'], 'resource-or-containment-failure')
            self.assertRegex(report['suites'][0]['finished_at'], r'Z$')

    def test_all_reported_containment_diagnostics_stop_the_gate(self):
        for diagnostic in ('admission closed', 'process limit exceeded',
                           'fork: Cannot fork'):
            with self.subTest(diagnostic=diagnostic), tempfile.TemporaryDirectory() as directory:
                logs = Path(directory) / 'logs'

                def run(command, **kwargs):
                    kwargs['stdout'].write(diagnostic)
                    return type('Result', (), {'returncode': 1})()

                with patch('sys.argv', ['pre_merge', '--logs', str(logs)]), \
                        patch.dict('os.environ', {'DATABASE_URL': 'fixture', 'TEST_DATABASE_URL': 'fixture',
                                                  'BLERG_TOOL_WORKERS': '1'}), \
                        patch.object(pre_merge, 'matrix', return_value=[('one', '.', ['one']), ('two', '.', ['two'])]), \
                        patch.object(pre_merge, 'run_suite', side_effect=run) as call, \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(pre_merge.main(), 1)
                    self.assertEqual(call.call_count, 1)
                self.assertFalse(json.loads((logs / 'summary.json').read_text())['complete'])

    def test_go_event_counts_include_repetitions_and_subtests(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'go.log'
            events = [{'Action': 'pass', 'Test': 'TestOne', 'Elapsed': 2},
                      {'Action': 'pass', 'Test': 'TestOne', 'Elapsed': 1},
                      {'Action': 'skip', 'Test': 'TestOne/subtest', 'Elapsed': 0},
                      {'Action': 'pass', 'Package': 'fixture', 'Elapsed': 3}]
            path.write_text('diagnostic\n' + '\n'.join(json.dumps(event) for event in events))
            results = pre_merge.go_results(path)
            self.assertEqual(results['counts'], {'pass': 2, 'fail': 0, 'skip': 1})
            self.assertEqual(results['slowest'][0]['seconds'], 2)

    def test_full_gate_requires_both_database_settings(self):
        with patch('sys.argv', ['pre_merge']), patch.dict('os.environ', {}, clear=True), \
                contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                pre_merge.main()
            self.assertEqual(error.exception.code, 2)

    def test_summary_survives_telemetry_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            logs = Path(directory) / 'logs'
            def broken(record):
                self.assertTrue((logs / 'summary.json').exists())
                raise OSError('unavailable recorder')
            with patch('sys.argv', ['pre_merge', '--suite', 'focused', '--logs', str(logs)]), patch.object(pre_merge, 'matrix', return_value=[('focused', '.', ['true'])]), patch.object(pre_merge, 'append_record', side_effect=broken), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(pre_merge.main(), 0)


LOCK_BASE = {
    'name': 'blerg-workspace', 'lockfileVersion': 3, 'requires': True,
    'packages': {
        '': {'name': 'blerg-workspace'},
        'board/web': {'name': 'blerg-board-web', 'version': '0.1.0'},
        'packages/chat': {'name': '@blerglab/chat', 'version': '1.53.0'},
        'runner/frontend': {'version': '0.1.0'},
        'node_modules/left-pad': {'version': '1.0.0', 'resolved': 'https://x/left-pad-1.0.0.tgz', 'integrity': 'sha1'},
    },
}


class LockfileRegionTest(unittest.TestCase):
    """BLG-477: a workspace package's version-only bump selects its region, not everything."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.env = dict(os.environ, GIT_AUTHOR_NAME='t', GIT_AUTHOR_EMAIL='t@t',
                        GIT_COMMITTER_NAME='t', GIT_COMMITTER_EMAIL='t@t')
        self.git('init', '-q', '-b', 'main')
        self.write(LOCK_BASE)
        self.git('add', '-A')
        self.git('commit', '-q', '-m', 'base')
        self.base = self.git('rev-parse', 'HEAD').strip()

    def git(self, *command):
        import subprocess
        return subprocess.check_output(['git', *command], cwd=self.root, env=self.env, text=True)

    def write(self, lock, name='package-lock.json'):
        (self.root / name).write_text(json.dumps(lock, indent=2))

    def classify(self, lock=None, raw=None, name='package-lock.json'):
        if raw is not None:
            (self.root / name).write_text(raw)
        else:
            self.write(lock, name)
        return pre_merge.classify_lockfile_change(self.root, self.base, name)

    def test_version_only_workspace_bump_is_regional(self):
        bumped = json.loads(json.dumps(LOCK_BASE))
        bumped['packages']['packages/chat']['version'] = '1.53.1'
        regions, note = self.classify(bumped)
        self.assertEqual(regions, {'chat-web', 'runner-web', 'board-web'} | set(pre_merge.IMAGE_REGIONS))
        self.assertIn('version bump only', note)
        self.assertIn('packages/chat', note)

    def test_select_changed_narrows_suites_for_a_version_only_bump(self):
        bumped = json.loads(json.dumps(LOCK_BASE))
        bumped['packages']['board/web']['version'] = '0.1.1'
        self.write(bumped)
        suites, skipped, reason = pre_merge.select_changed(
            pre_merge.matrix('go', 2), ['package-lock.json'], root=self.root, base=self.base)
        names = {name for name, _, _ in suites}
        self.assertIn('board-lint', names)
        self.assertIn('board-tests', names)
        self.assertNotIn('runner-go', names)
        self.assertNotIn('runner-lint', names)
        self.assertIn('runner-lint', skipped)
        self.assertIn('version bump only', reason)

    def test_a_real_dependency_change_stays_global(self):
        changed = json.loads(json.dumps(LOCK_BASE))
        changed['packages']['node_modules/left-pad']['version'] = '1.0.1'
        regions, note = self.classify(changed)
        self.assertIsNone(regions)
        self.assertIn('dependency changed', note)

    def test_an_added_or_removed_workspace_package_stays_global(self):
        changed = json.loads(json.dumps(LOCK_BASE))
        changed['packages']['packages/settings'] = {'name': '@blerglab/settings', 'version': '0.1.0'}
        regions, note = self.classify(changed)
        self.assertIsNone(regions)
        self.assertIn('added or removed', note)

    def test_a_workspace_change_beyond_version_stays_global(self):
        changed = json.loads(json.dumps(LOCK_BASE))
        changed['packages']['packages/chat']['dependencies'] = {'left-pad': '^1.0.0'}
        regions, note = self.classify(changed)
        self.assertIsNone(regions)
        self.assertIn('beyond its version field', note)

    def test_malformed_lockfile_fails_closed_to_global_with_a_reason(self):
        regions, note = self.classify(raw='not json')
        self.assertIsNone(regions)
        self.assertIn('could not be parsed', note)

    def test_a_new_lockfile_with_no_prior_version_fails_closed_to_global(self):
        self.git('rm', '-q', 'package-lock.json')
        self.git('commit', '-q', '-m', 'remove lockfile')
        base = self.git('rev-parse', 'HEAD').strip()
        self.write(LOCK_BASE)
        regions, note = pre_merge.classify_lockfile_change(self.root, base, 'package-lock.json')
        self.assertIsNone(regions)
        self.assertIn('new lockfile', note)

    def test_core_web_s_own_lockfile_version_bump_is_regional(self):
        core_lock = {'name': 'blerg-core-web', 'packages': {'': {'name': 'blerg-core-web', 'version': '0.1.0'}}}
        (self.root / 'core' / 'web').mkdir(parents=True)
        self.write(core_lock, 'core/web/package-lock.json')
        self.git('add', '-A')
        self.git('commit', '-q', '-m', 'add core lockfile')
        base = self.git('rev-parse', 'HEAD').strip()
        core_lock['packages']['']['version'] = '0.1.1'
        self.write(core_lock, 'core/web/package-lock.json')
        regions, note = pre_merge.classify_lockfile_change(self.root, base, 'core/web/package-lock.json')
        self.assertEqual(regions, {'core-web'} | set(pre_merge.IMAGE_REGIONS))
        self.assertIn('version bump only', note)

    def test_an_unrecognized_lockfile_path_fails_closed_to_global(self):
        regions, note = pre_merge.classify_lockfile_change(self.root, self.base, 'board/web/package-lock.json')
        self.assertIsNone(regions)
        self.assertIn('not a recognized workspace lockfile', note)


if __name__ == '__main__':
    unittest.main()
