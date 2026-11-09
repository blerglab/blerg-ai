#!/usr/bin/env python3
from ui_suite_lock import command_lock_fds, inherited_lock_fd, ui_suite_lock
"""Run the complete local verification matrix, preserving individual evidence."""
import argparse
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from gate_telemetry import (append_record, growth_warnings, load_average, load_history, needs_ui_lock,
                            stamp, start_record, test_results, vitest_command)
import vitest_results_cache

ROOT = Path(__file__).resolve().parent.parent


def publish_gate_report(telemetry, logs):
    """Best-effort session-scoped producer; never forward raw logs/host metadata.

    A reporting problem of any kind must never change the gate's result."""
    if not os.environ.get('BLERG_RUNNER_SESSION_ID'):
        return
    try:
        envelope = {key: telemetry[key] for key in ('commit', 'started_at', 'finished_at')}
        envelope.update(run_id=telemetry['id'], revision=1,
                        report=dict(seconds=telemetry['seconds'], complete=telemetry['complete'],
                                    suites=[{key: suite[key] for key in ('suite', 'seconds', 'outcome')}
                                            for suite in telemetry['suites']]))
        path = logs / 'gate-report.json'
        command = [sys.executable, str(ROOT / 'runner/.claude/skills/session-messaging/blerg-runner'),
                   'gate', 'report', str(path)]
        path.write_text(json.dumps(envelope) + '\n')
        result = subprocess.run(command, timeout=15, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        if result.returncode:
            print(f'Gate dataset upload unavailable (exit {result.returncode}; gate result unchanged).', file=sys.stderr)
    except subprocess.TimeoutExpired:
        print('Gate dataset upload unavailable (timed out; gate result unchanged).', file=sys.stderr)
    except Exception as error:  # noqa: BLE001 - reporting must never fail a gate
        print(f'Gate dataset upload unavailable ({type(error).__name__}; gate result unchanged).', file=sys.stderr)


def suite_timeout(name):
    if name.endswith('-image'):
        return 3600
    if name.endswith('-go') or name.endswith('-race'):
        return 5400
    if name in ('runner-browser', 'runner-performance'):
        return 1800
    return 900


def run_suite(command, *, timeout, cancel_event=None, **kwargs):
    """A deadline covers the whole process tree, including orphaned test helpers."""
    with subprocess.Popen(command, start_new_session=True, stdin=subprocess.DEVNULL, **kwargs) as process:
        try:
            deadline = time.monotonic() + timeout
            while True:
                if cancel_event is not None and cancel_event.is_set():
                    raise KeyboardInterrupt
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise subprocess.TimeoutExpired(command, timeout)
                try:
                    returncode = process.wait(timeout=min(.25, remaining))
                    break
                except subprocess.TimeoutExpired:
                    continue
        except BaseException:
            # Kill the group even if the immediate command has already exited.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait()
            raise
    return subprocess.CompletedProcess(command, returncode)


def gofmt_command(modules):
    directories = ['./' + module for module in modules]
    formatting = ("import subprocess, sys; "
                  f"r=subprocess.run(['gofmt', '-l'] + {directories!r}, "
                  "capture_output=True, text=True); print(r.stdout, end=''); "
                  "print(r.stderr, end='', file=sys.stderr); "
                  "sys.exit(r.returncode or bool(r.stdout))")
    return [sys.executable, '-c', formatting]


# daemon-race and runtime-race run these packages with -race: a strictly stronger
# check (the same test assertions, plus race detection) than the plain build. Running
# them a third time in runner-go's ./... adds minutes without adding coverage.
RACE_ONLY_PACKAGES = ('./internal/daemon', './internal/ownershell',
                      './internal/shellruntime', './internal/processcontainment')


def runner_plain_packages(go):
    """All of runner's packages except the ones only the race suites need to cover.
    Falls back to everything if `go list` cannot resolve them, so a broken toolchain
    never silently drops coverage."""
    env = dict(os.environ, GOWORK='off')
    try:
        excluded = set(subprocess.check_output(
            [go, 'list', *RACE_ONLY_PACKAGES], cwd=ROOT / 'runner', env=env, text=True,
            stderr=subprocess.DEVNULL).split())
        packages = subprocess.check_output(
            [go, 'list', './...'], cwd=ROOT / 'runner', env=env, text=True,
            stderr=subprocess.DEVNULL).split()
    except (OSError, subprocess.CalledProcessError):
        return ['./...']
    remaining = [p for p in packages if p not in excluded]
    return remaining or ['./...']


def prepare_go_command(suite, selected, workers):
    name, _, command = suite
    command = list(command)
    if command[1:2] == ['test'] and name in {module + '-go' for module in ('contracts', 'core', 'board', 'runner')}:
        # Explicit full vet is a superset of go test's implicit vet subset.
        # Both receipts remain mandatory; a failed vet still fails the gate.
        if name[:-3] + '-vet' in selected:
            command.insert(2, '-vet=off')
        if name == 'runner-go' and workers > 1:
            command = [command[0], 'run', str(ROOT / 'scripts/verification/cmd/go-test-shards/main.go'), *command]
    return command


def matrix(go, workers, release=False):
    suites = []

    def add(name, cwd, *command):
        suites.append((name, cwd, list(command)))

    # The shell security tests require tracing. Refuse a restricted execution
    # environment up front instead of reporting hundreds of shell failures.
    probe = ("import ctypes, os, sys; "
             "libc=ctypes.CDLL(None, use_errno=True); "
             "result=libc.ptrace(0, 0, 0, 0) if sys.platform == 'linux' else 0; "
             "print('Host tracing available' if result == 0 else "
             "'Host tracing denied; full shell verification requires PTRACE_TRACEME'); "
             "sys.exit(0 if result == 0 else 1)")
    add('host-tracing-preflight', '.', sys.executable, '-c', probe)
    for module in ('contracts', 'core', 'board', 'runner'):
        add(f'{module}-vet', module, go, 'vet', f'-p={workers}', './...')
        # Measured module runs are faster at two concurrent packages.
        # Keep the CPU budget and all tests unchanged.
        package_workers = min(workers, 2) if module in ('core', 'board') else workers
        add(f'{module}-go', module, go, 'test', '-json',
            f'-p={package_workers}', '-parallel=4', '-timeout=45m', './...')
    add('session-regressions', 'runner', go, 'run', './cmd/session-suite', '-go', go)
    add('runtime-smoke-local', 'runner', go, 'run', './cmd/runtime-smoke',
        '-go', go, '-runtimes', 'host,sandbox')
    add('daemon-race', 'runner', go, 'test', '-json', '-race', f'-count={3 if release else 1}',
        f'-p={workers}', '-parallel=4', '-timeout=45m', './internal/daemon')
    add('runtime-race', 'runner', go, 'test', '-json', '-race', '-count=1',
        f'-p={workers}', '-parallel=4', '-timeout=30m', './internal/ownershell',
        './internal/shellruntime', './internal/processcontainment')
    for mode in ('go', 'race'):
        flags = ['-race'] if mode == 'race' else []
        add(f'persistent-shell-wire-{mode}', 'runner', go, 'test', '-json',
            *flags, '-count=1', '-tags=persistent_shell_test', f'-p={workers}',
            '-parallel=1', '-timeout=5m',
            '-run=^TestPersistentShellRealWireReconnectAndWithdrawal$', './internal/server')
    lint = os.environ.get('GOLANGCI_LINT', 'golangci-lint')
    add('lint-pin', '.', './scripts/check_lint_pin.sh', lint)
    add('gofmt', '.', *gofmt_command(('contracts', 'core', 'board', 'runner', 'scripts/verification')))
    for module in ('contracts', 'core', 'board', 'runner'):
        add(f'{module}-go-lint', module, lint, 'run',
            f'--concurrency={workers}', f'--config={ROOT / ".golangci.yml"}', './...')
    add('verification-go-lint', 'scripts/verification', lint, 'run',
        f'--concurrency={workers}', f'--config={ROOT / ".golangci.yml"}', './...')
    for app, cwd in (('core', 'core/web'), ('board', 'board/web'),
                     ('chat', 'packages/chat'), ('runner', 'runner/frontend')):
        add(f'{app}-lint', cwd, 'npm', 'run', 'lint')
        add(f'{app}-types', cwd, 'npx', '--no-install', 'tsc', '--noEmit')
        if app == 'runner':
            add('runner-unit', cwd, 'npm', 'test', '--', '--project', 'unit', f'--maxWorkers={workers}')
            add('runner-browser', cwd, 'npm', 'test', '--', '--project', 'browser', f'--maxWorkers={workers}')
            add('runner-performance', cwd, 'npm', 'run', 'test:performance')
        else:
            add(f'{app}-tests', cwd, 'npm', 'test', '--', f'--maxWorkers={workers}')
        add(f'{app}-build', cwd, 'npm', 'run', 'build')
    for app in ('core', 'board', 'runner'):
        add(f'{app}-image', '.', go, 'run', './contracts/cmd/image-build/main.go', app)
    cli = 'runner/.claude/skills/session-messaging'
    add('cli-unittest', cli, sys.executable, '-m', 'unittest',
        'test_blerg_runner', 'test_blerg_runner_review_request')
    add('cli-pytest', cli, sys.executable, '-m', 'pytest', '-q',
        '--durations=30', 'blerg_runner_board_test.py')
    add('desktop-scripts', '.', 'bash', '-ec',
        './install/desktop/compose_test.sh; '
        'bash install/desktop/hostcheck_test.sh; '
        'bash install/desktop/daemon_install_test.sh; '
        'bash install/desktop/workspace_worker_image_test.sh; '
        'python3 install/desktop/daemon/enroll_test.py; '
        'bash install/desktop/wsl_startup_test.sh')
    add('scrub', '.', 'bash', '-ec', './scripts/scrub.sh; ./scripts/scrub_test.sh')
    add('links', '.', 'bash', '-ec', './scripts/check_links_test.sh; ./scripts/check_links.sh')
    add('theme-check', '.', 'bash', '-ec',
        f'cd scripts/themecheck && GOWORK=off {shlex.quote(go)} test ./... && '
        f'GOWORK=off {shlex.quote(go)} run . ../..')
    # Resolved default component colors for every app (make test-web runs it too): a theme token
    # change shows up here before a public CI run does.
    add('theme-colors', '.', 'node', 'scripts/test-default-theme-colors.mjs')
    add('scripts', '.', 'bash', '-ec',
        'make test-verification; '
        'python3 scripts/coordinating_skill_test.py; ./install/k8s/deploy_test.sh; '
        'bash video/demo/demo_test.sh; '
        "python3 -m unittest discover -s video/demo -p 'test_*.py'; "
        "python3 -m unittest discover -s video/build -p 'test_*.py'; "
        './scripts/release-public_test.sh; '
        'python3 -m unittest discover -s scripts -p pre_merge_test.py; '
        "python3 -m unittest discover -s scripts/tests -p 'test_*.py'; "
        'node --check scripts/gate_vitest_reporter.mjs')
    return suites


# Ordered: frontend and CLI paths must precede their parent Go modules.
IMAGE_REGIONS = ('core-image', 'board-image', 'runner-image')
WEB_REGIONS = ('core-web', 'board-web', 'chat-web', 'runner-web')
PATH_REGIONS = (
    ('core/Dockerfile', IMAGE_REGIONS),
    ('board/Dockerfile', IMAGE_REGIONS),
    ('runner/Dockerfile', IMAGE_REGIONS),
    ('core/web/', ('core-web',) + IMAGE_REGIONS),
    ('board/web/', ('board-web',) + IMAGE_REGIONS),
    ('runner/frontend/', ('runner-web',) + IMAGE_REGIONS),
    ('packages/chat/', ('chat-web', 'runner-web', 'board-web') + IMAGE_REGIONS),
    ('packages/settings/', WEB_REGIONS + IMAGE_REGIONS),
    ('packages/board/', ('runner-web', 'board-web') + IMAGE_REGIONS),
    ('brand/', WEB_REGIONS + IMAGE_REGIONS),
    ('runner/.claude/skills/session-messaging/', ('cli',)),
    ('contracts/', ('contracts-go', 'core-go', 'board-go', 'runner-go')),
    ('core/', ('core-go',)), ('board/', ('board-go',)),
    ('runner/', ('runner-go',)),
    ('install/desktop/', ('desktop',)),
    ('docs/', ()),
    ('.gitignore', ()),
)


def suite_regions(name):
    if name in IMAGE_REGIONS:
        return {name}
    if name in ('scrub', 'links', 'theme-check'):
        return {'always'}
    if name == 'theme-colors':
        return set(WEB_REGIONS)
    if name in ('lint-pin', 'gofmt'):
        return {module + '-go' for module in ('contracts', 'core', 'board', 'runner')}
    if name in ('host-tracing-preflight', 'runtime-smoke-local', 'session-regressions', 'daemon-race',
                'runtime-race', 'persistent-shell-wire-go', 'persistent-shell-wire-race'):
        return {'runner-go'}
    if name.startswith('cli-'):
        return {'cli'}
    if name in ('scripts', 'verification-go-lint'):
        return {'gate'}
    if name == 'desktop-scripts':
        return {'desktop'}
    for module in ('contracts', 'core', 'board', 'runner'):
        if name in (module + '-vet', module + '-go', module + '-go-lint'):
            return {module + '-go'}
    for app in ('core', 'board', 'chat', 'runner'):
        if name in {app + '-' + kind for kind in
                    ('lint', 'types', 'tests', 'unit', 'browser', 'performance', 'build')}:
            return {app + '-web'}
    return set()


# A workspace member's own version bump (left side of the pair) maps to the directory
# PATH_REGIONS already scopes for that workspace. '' is the lockfile's own root project
# entry: indeterminate (not a product workspace), so it always stays global.
LOCKFILE_WORKSPACE_DIRS = {
    'package-lock.json': {'': None, 'board/web': 'board/web/', 'packages/chat': 'packages/chat/',
                          'runner/frontend': 'runner/frontend/'},
    'core/web/package-lock.json': {'': 'core/web/'},
}


def classify_lockfile_change(root, base, file):
    """A workspace package's version-only bump selects its own region; anything else, or
    anything we cannot cleanly parse, stays global (fail closed)."""
    workspace_dirs = LOCKFILE_WORKSPACE_DIRS.get(file)
    if workspace_dirs is None:
        return None, f'{file} is not a recognized workspace lockfile'
    try:
        old_text = subprocess.check_output(['git', 'show', f'{base}:{file}'], cwd=root,
                                           stderr=subprocess.DEVNULL, text=True)
    except subprocess.CalledProcessError:
        return None, f'{file} has no version at {base} (new lockfile)'
    new_file = Path(root) / file
    if not new_file.is_file():
        return None, f'{file} was removed'
    try:
        old_packages = json.loads(old_text)['packages']
        new_packages = json.loads(new_file.read_text())['packages']
        if not isinstance(old_packages, dict) or not isinstance(new_packages, dict):
            raise ValueError('packages is not an object')
    except (ValueError, KeyError, TypeError) as error:
        return None, f'{file} could not be parsed as a lockfile: {error}'
    changed_keys = {key for key in old_packages.keys() | new_packages.keys()
                    if old_packages.get(key) != new_packages.get(key)}
    if not changed_keys:
        return None, f'{file} diff carries no package entry changes'
    regions = set()
    for key in changed_keys:
        if 'node_modules' in key:
            return None, f'{file}: a real dependency changed ({key})'
        old_entry, new_entry = old_packages.get(key), new_packages.get(key)
        if old_entry is None or new_entry is None:
            return None, f'{file}: a workspace package was added or removed ({key or "root"})'
        if {f for f in set(old_entry) | set(new_entry) if old_entry.get(f) != new_entry.get(f)} != {'version'}:
            return None, f'{file}: {key or "root"} changed beyond its version field'
        directory = workspace_dirs.get(key)
        if directory is None:
            return None, f'{file}: {key or "root"} is not a scoped workspace'
        match = next((value for prefix, value in PATH_REGIONS if directory == prefix), None)
        if match is None:
            return None, f'{file}: no region mapped for {directory}'
        regions.update(match)
    return regions, f'{file}: workspace version bump only ({", ".join(sorted(changed_keys) or ["?"])})'


def select_changed(suites, files, root=None, base=None):
    """Select complete regions; unknown inputs conservatively require everything."""
    regions = {'always'}
    notes = []
    for file in files:
        path = Path(file)
        if path.name in ('package-lock.json', 'npm-shrinkwrap.json', 'pnpm-lock.yaml'):
            if path.name == 'package-lock.json' and root is not None and base is not None:
                lockfile_regions, note = classify_lockfile_change(root, base, file)
                if lockfile_regions is not None:
                    regions.update(lockfile_regions)
                    notes.append(note)
                    continue
                return suites, {}, f'global build or gate input: {file} ({note})'
            return suites, {}, f'global build or gate input: {file}'
        if (path.name in ('go.mod', 'go.sum', 'go.work', 'go.work.sum')
                or path.name.endswith(('.lock', '.lockb'))
                or file.startswith(('scripts/', '.github/', 'contracts/cmd/image-build/'))):
            return suites, {}, f'global build or gate input: {file}'
        # Markdown beneath a module can be embedded product content, such as skills.
        if path.suffix == '.md' and (len(path.parts) == 1 or file.startswith('docs/')
                                     or path.name in ('README.md', 'CLAUDE.md', 'AGENTS.md')):
            continue
        match = next((value for prefix, value in PATH_REGIONS if file.startswith(prefix)), None)
        if match is not None:
            regions.update(match)
        else:
            return suites, {}, f'unmapped path: {file}'
    selected = [suite for suite in suites if suite_regions(suite[0]) & regions]
    modules = [module for module in ('contracts', 'core', 'board', 'runner')
               if module + '-go' in regions]
    selected = [(name, cwd, gofmt_command(modules) if name == 'gofmt' else command)
                for name, cwd, command in selected]
    skipped = {name: 'outside changed regions: ' + ', '.join(sorted(regions - {'always'}))
               if regions != {'always'} else 'documentation-only or no changed paths'
               for name, _, _ in suites if not suite_regions(name) & regions}
    reason = 'changed regions: ' + ', '.join(sorted(regions))
    if notes:
        reason += '; ' + '; '.join(notes)
    return selected, skipped, reason


def go_results(path, required_test=None):
    """Keep exact test-event counts and timings, including repeated passes."""
    counts = {key: 0 for key in ('pass', 'fail', 'skip')}
    slow = []
    cached_packages = set()
    required_counts = dict(counts)
    for line in path.read_text(errors='replace').splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        if event.get('Package') and not event.get('Test') and re.search(r'^ok\s+.*\s+\(cached\)\s*$', event.get('Output', '')):
            cached_packages.add(event['Package'])
        if 'Test' not in event:
            continue
        action = event.get('Action')
        if action in counts:
            counts[action] += 1
            if event['Test'] == required_test:
                required_counts[action] += 1
            slow.append({'test': event['Test'], 'seconds': event.get('Elapsed', 0), 'result': action})
    result = dict(counts=counts, slowest=sorted(slow, key=lambda row: row['seconds'], reverse=True)[:30],
                  cached_packages=sorted(cached_packages))
    if required_test:
        result['required_test_passed'] = required_counts == {'pass': 1, 'fail': 0, 'skip': 0}
    return result


def runtime_smoke_results(evidence):
    """Retain nested Go receipts, especially named skips, in gate telemetry."""
    reports = [test_results(evidence / (runtime + '.jsonl')) for runtime in ('host', 'sandbox')]
    return dict(counts={key: sum(report['counts'][key] for report in reports)
                        for key in ('pass', 'fail', 'skip')},
                slowest=sorted([row for report in reports for row in report['slowest']],
                               key=lambda row: row['seconds'], reverse=True)[:30],
                flake_reruns=[])


def resource_failure(output):
    resource = re.compile(r'resource temporarily unavailable|cannot allocate memory|out of memory|failed to create new OS thread|process limit (?:exceeded|reached)|cannot fork|writer freeze|admission closed', re.I)
    # Go tests intentionally kill child processes. An owned shutdown record proves
    # the cause independently of test attribution; otherwise require a passing test
    # with no failed repetition before exempting its kill diagnostics.
    passed, failed = set(), set()
    lines = []
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            event = None
        if not isinstance(event, dict):
            event = None
        identity = (event.get('Package'), event.get('Test')) if event else None
        if event and event.get('Test'):
            if event.get('Action') == 'pass':
                passed.add(identity)
            elif event.get('Action') == 'fail':
                failed.add(identity)
        lines.append((event.get('Output', '') if event else line, identity))
    for line, identity in lines:
        # A bounded task refusal is expected in containment tests, but only a
        # passing receipt (with no failed repetition) proves it was contained.
        expected_limit = re.fullmatch(
            r'(?:\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} '
            r'writer helper pid=\d+: )?blerg: process limit reached \(\d+ tasks\); retry with less parallelism '
            r'\(GOMAXPROCS, go -p, make -j, test workers\)\.', line.strip())
        if not (expected_limit and identity in passed and identity not in failed):
            match = resource.search(line)
            if match:
                return match.group(0)
        # This lifecycle record proves an owned shutdown, even when a late
        # background log is attributed to an unrelated failing test/repetition.
        if re.search(r'\bclaude-code: process exited:.*\bstatus=signal: killed\b.*\bdriver_kill=true\b.*\bshutdown=true\b', line):
            continue
        if re.search(r'signal: killed', line, re.I) and (identity not in passed or identity in failed):
            return 'signal: killed' + (f' in {identity[1]}' if identity and identity[1] else '')
    return None


def has_resource_failure(output):
    return resource_failure(output) is not None


def suite_failure_detail(output, code):
    resource = resource_failure(output)
    if resource:
        return resource
    failed = []
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and event.get('Action') == 'fail' and event.get('Test'):
            if event['Test'] not in failed:
                failed.append(event['Test'])
    details = ['data race reported'] if 'WARNING: DATA RACE' in output else []
    if failed:
        details.append('failed tests: ' + ', '.join(failed[:5]))
    return '; '.join(details) or f'suite exited with status {code}'


def memory_budget():
    """Use available host memory, bounded by the session's cgroup headroom."""
    try:
        available = next(int(line.split()[1]) * 1024 for line in
                         Path('/proc/meminfo').read_text().splitlines()
                         if line.startswith('MemAvailable:'))
        for root, limit, used in (('/sys/fs/cgroup', 'memory.max', 'memory.current'),
                                  ('/sys/fs/cgroup/memory', 'memory.limit_in_bytes', 'memory.usage_in_bytes')):
            try:
                available = min(available, max(0, int((Path(root) / limit).read_text()) -
                                               int((Path(root) / used).read_text())))
            except (OSError, ValueError):
                pass
        return available
    except (OSError, StopIteration, ValueError):
        return 4 * 1024**3


def parallel_slots(requested, workers, memory):
    # Reserve 2 GiB for the session/OS and 4 GiB for each concurrent suite.
    # A small session retains the previous single-suite behavior.
    return max(1, min(requested // workers, (memory - 2 * 1024**3) // (4 * 1024**3)))


def suite_resources(suite):
    name, cwd, command = suite
    resources = set()
    if name in IMAGE_REGIONS:
        resources.add('image-build')
    if name.endswith('-go-lint'):
        resources.add('golangci-lint')
    if cwd.endswith(('/web', '/frontend', '/chat')):
        resources.add(cwd)
    if needs_ui_lock(command, cwd):
        # Threads share descriptors: do not mistake another UI worker's
        # lock descriptor for an inherited lock from a parent gate.
        resources.add('ui-suite')
    if name == 'chat-build':
        # Consumers can read the workspace package's dist during their builds.
        resources.update(('core/web', 'board/web', 'runner/frontend', 'packages/chat'))
    return resources


def scheduled_results(suites, slots, execute, stopped):
    """Refill available slots; drain running suites after a stop diagnostic."""
    preflight = [suite for suite in suites if suite[0] == 'host-tracing-preflight']
    performance = [suite for suite in suites if suite[0] == 'runner-performance']
    pending = [suite for suite in suites if suite not in preflight + performance]
    priority = {'runner-go': 0, 'daemon-race': 1, 'runtime-race': 2,
                'runner-browser': 3, 'scripts': 4, 'cli-unittest': 5}
    pending.sort(key=lambda suite: priority.get(suite[0], 6))
    for suite in preflight:
        yield execute(suite)
        if stopped():
            return
    with ThreadPoolExecutor(max_workers=slots) as pool:
        running = {}
        while running or (pending and not stopped()):
            resources = set().union(*(suite_resources(suite) for suite in running.values()))
            for suite in pending[:]:
                if stopped() or len(running) >= slots:
                    break
                # Frontend tools share output and Vitest scheduling caches.
                if suite_resources(suite) & resources:
                    continue
                running[pool.submit(execute, suite)] = suite
                resources.update(suite_resources(suite))
                pending.remove(suite)
            finished, _ = wait(running, return_when=FIRST_COMPLETED)
            for future in finished:
                del running[future]
                yield future.result()
    for suite in performance:
        if stopped():
            return
        yield execute(suite)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--logs', type=Path, help='new directory for full logs and summary.json')
    parser.add_argument('--list', action='store_true', help='print suite names without running')
    parser.add_argument('--suite', action='append', help='run only this suite (repeatable; not a full gate)')
    parser.add_argument('--changed', metavar='BASE', help='run complete suites for changed regions since BASE (includes working files)')
    parser.add_argument('--changed-since', metavar='COMMIT', help='touched verification, including uncommitted files; never a full gate')
    parser.add_argument('--release', action='store_true', help='run everything with three daemon race passes; ignores selection filters')
    args = parser.parse_args()
    try:
        requested = int(os.environ.get('BLERG_TOOL_WORKERS', '2'))
        if requested < 1:
            raise ValueError
        workers = min(6, requested)
    except ValueError:
        parser.error('BLERG_TOOL_WORKERS must be a positive integer')
    # Prefer the pinned toolchain installed under ~/go/bin when present: a
    # distribution Go earlier on PATH can be older than go.mod requires.
    # Hosted runners have no such install and use Go from PATH.
    home_go = Path.home() / 'go/bin/go'
    go = os.environ.get('GO') or (str(home_go) if home_go.is_file() else 'go')
    if not Path(go).is_file():
        go = shutil.which(go) or go
    suites = matrix(go, workers, release=args.release)
    if args.changed and args.changed_since:
        parser.error('--changed and --changed-since are mutually exclusive')
    skipped = {}
    selection_reason = 'complete matrix'
    full_matrix = args.release or (not args.suite and not args.changed_since and not args.changed)
    if args.changed and not args.release:
        from touched_tests import changed_files
        try:
            suites, skipped, selection_reason = select_changed(
                suites, changed_files(ROOT, args.changed), root=ROOT, base=args.changed)
        except (OSError, ValueError, subprocess.CalledProcessError) as error:
            parser.error(f'could not determine changed regions: {error}')
        full_matrix = not skipped
    if args.changed_since and not args.release:
        from touched_tests import changed_files, select
        try:
            files = changed_files(ROOT, args.changed_since)
            # Preserve the legacy focused mode's bundled script checks.
            focused = [(name, cwd, ['make', 'check-other-scripts'] if name == 'scripts' else command)
                       for name, cwd, command in suites if name not in ('desktop-scripts', 'scrub', 'links')]
            suites = select(focused, ROOT, files, go=go, workers=workers)
        except (OSError, ValueError, subprocess.CalledProcessError) as error:
            parser.error(f'could not determine touched tests: {error}')
    names = {name for name, _, _ in suites}
    if args.suite and not args.release:
        unknown = set(args.suite) - names
        if unknown:
            parser.error('unknown suites: ' + ', '.join(sorted(unknown)))
        skipped.update({name: 'excluded by --suite' for name, _, _ in suites if name not in args.suite})
        suites = [suite for suite in suites if suite[0] in args.suite]
        full_matrix = False
    if args.list and not args.changed and not args.release:
        print('\n'.join(name for name, _, _ in suites))
        return 0
    print('Selection: ' + selection_reason)
    for name, _, _ in suites:
        print('RUN  ' + name)
    for name, reason in skipped.items():
        print(f'SKIP {name}: {reason}')
    if args.list:
        return 0
    # daemon-race and runtime-race run these same packages with -race, a strictly
    # stronger check. Only drop them from the plain run when both race suites are
    # actually about to run alongside it (always true for the full matrix and for
    # --changed region selection, since they share runner-go's region); a focused
    # --suite or --changed-since selection that excludes the race suites keeps full
    # ./... coverage here instead.
    present = {name for name, _, _ in suites}
    if {'runner-go', 'daemon-race', 'runtime-race'} <= present:
        from touched_tests import strip_trailing_packages
        excluded = runner_plain_packages(go)
        suites = [(name, cwd, strip_trailing_packages(command) + excluded)
                  if name == 'runner-go' else (name, cwd, command) for name, cwd, command in suites]
    # Missing DB settings must never produce a seemingly green integrated gate.
    if full_matrix and any(not os.environ.get(key) for key in ('DATABASE_URL', 'TEST_DATABASE_URL')):
        parser.error('full verification requires DATABASE_URL and TEST_DATABASE_URL')
    logs = args.logs or Path(tempfile.mkdtemp(prefix='blerg-pre-merge-'))
    if args.logs:
        logs.mkdir(parents=True, exist_ok=False)
    logs = logs.resolve()
    env = dict(os.environ, GOWORK='off', GOMAXPROCS=str(workers),
               GOFLAGS=f'-p={workers}', BLERG_TOOL_WORKERS=str(workers),
               VITEST_MAX_WORKERS=str(workers), PLAYWRIGHT_WORKERS=str(workers),
               BLERG_SKIP_KUBECTL='1', PYTHONDONTWRITEBYTECODE='1')
    env['PATH'] = str(Path(go).resolve().parent) + os.pathsep + env.get('PATH', '')
    print(f'Logs: {logs}', flush=True)
    telemetry = start_record(workers, full_matrix=full_matrix, changed_since=args.changed_since, changed=args.changed)
    started = time.monotonic()
    results = []
    stopped_reason = None
    outer_lock = env.get('BLERG_UI_SUITE_LOCK')
    outer_fd = inherited_lock_fd(outer_lock) if outer_lock else None
    memory = memory_budget()
    slots = parallel_slots(requested, workers, memory)
    print(f'Concurrent suites: {slots}; workers per suite: {workers}; memory headroom: {memory // 1024**2} MiB', flush=True)

    cancelled = threading.Event()

    def execute(suite):
        name, cwd, command = suite
        command = prepare_go_command(suite, present, workers)
        print(f'Running {name}…', flush=True)
        suite_started = stamp()
        suite_load = load_average()
        before = time.monotonic()
        path = logs / f'{name}.log'
        vitest = logs / f'{name}.vitest.json'
        image_builder = 'blerg-gate-' + uuid.uuid4().hex if name in IMAGE_REGIONS else None
        if image_builder:
            command = [*command, image_builder]
        if name == 'session-regressions':
            command = [*command, '-logs', str(logs / 'session-regressions-evidence')]
        if name == 'runtime-smoke-local':
            command = [*command, '-logs', str(logs / 'runtime-smoke-local-evidence')]
        command, is_vitest = vitest_command(command, vitest)
        suite_env = dict(env, BLERG_GATE_VITEST_RERUNS=str(vitest) + '.reruns.json',
                         BLERG_KEEPER_UID_REQUIRED='1')
        # Performance must retain every viewport even in focused development runs.
        suite_env['VITE_BLERG_TEST_PHASE'] = 'touched' if args.changed_since and not args.release and name != 'runner-performance' else 'final'
        try:
            lock_ui = is_vitest and needs_ui_lock(command, cwd)
            with ui_suite_lock(lock_ui) as lock_fd, path.open('w') as output:
                if is_vitest:
                    vitest_results_cache.best_effort(ROOT, cwd, restore=True)
                result = run_suite(command, timeout=suite_timeout(name), cancel_event=cancelled, cwd=ROOT / cwd, env=suite_env,
                                        stdout=output, stderr=subprocess.STDOUT,
                                        pass_fds=command_lock_fds(command, outer_fd, lock_fd, go_binary=go))
            code = result.returncode
            if is_vitest:
                vitest_results_cache.best_effort(ROOT, cwd, restore=False)
        except subprocess.TimeoutExpired:
            code = 124
            with path.open('a') as output:
                output.write(f'\nSuite {name} timed out after {suite_timeout(name)} seconds; process group killed.\n')
            print(f'{name} timed out after {suite_timeout(name)} seconds.', flush=True)
        except KeyboardInterrupt:
            cancelled.set()
            code = 130
        except OSError as error:
            path.write_text(f'Could not start suite: {error}\n')
            code = 127
        if image_builder and code in (124, 130):
            try:
                with path.open('a') as output:
                    subprocess.run([go, 'run', str(ROOT / 'contracts/cmd/image-build/main.go'),
                                    '-cleanup', image_builder], cwd=ROOT, env=env,
                                   stdout=output, stderr=subprocess.STDOUT,
                                   timeout=180, check=True)
            except (OSError, RuntimeError, subprocess.SubprocessError) as error:
                with path.open('a') as output:
                    output.write(f'Image cleanup failed: {error}\n')
        # go run maps the command's exit 77 to 1; preserve its named skip receipt.
        if (name in IMAGE_REGIONS and code in (0, 1, 77)
                and f'SKIP {name}: Docker is unavailable' in path.read_text().splitlines()):
            code = 77
        duration = round(time.monotonic() - before, 3)
        row = dict(suite=name, started_at=suite_started, load_start=suite_load, load_end=load_average(), workers=workers, exit_code=code, seconds=duration, log=path.name,
                   finished_at=datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z'))
        if name.endswith('-go') or name.endswith('-race'):
            required = ('TestPersistentShellRealWireReconnectAndWithdrawal'
                        if name.startswith('persistent-shell-wire-') else None)
            row['tests'] = go_results(path, required)
            if required and not row['tests']['required_test_passed']:
                code = code or 1
                row['exit_code'] = code
                row['verification_error'] = 'Required wire test did not pass exactly once (missing, skipped, or failed).'
                print(row['verification_error'], flush=True)
        row['outcome'] = 'skip' if name in IMAGE_REGIONS and code == 77 else ('pass' if code == 0 else 'fail')
        if row['outcome'] == 'skip':
            row['reason'] = path.read_text().strip()
        try:
            measured = (runtime_smoke_results(logs / 'runtime-smoke-local-evidence')
                        if name == 'runtime-smoke-local' else test_results(path, vitest))
        except Exception as error:
            print(f'Gate telemetry unavailable: {type(error).__name__}', file=sys.stderr)
            measured = {}
        if 'tests' in row:
            row['tests'].update(measured)
        else:
            row['tests'] = measured
        return row

    previous_interrupt = signal.signal(signal.SIGINT, lambda *_: cancelled.set())
    try:
        for row in scheduled_results(suites, slots, execute, lambda: stopped_reason is not None or cancelled.is_set()):
            results.append(row)
            name, code = row['suite'], row['exit_code']
            if code == 130:
                stopped_reason = 'interrupted'
            print(f'{name}: {row["outcome"].upper()} ({row["seconds"]:.1f}s)', flush=True)
            output = (logs / row['log']).read_text(errors='replace') if code != 0 else ''
            if code != 0 and row['outcome'] != 'skip':
                row['failure_detail'] = suite_failure_detail(output, code)
                print(row['failure_detail'], flush=True)
            if name == 'host-tracing-preflight' and code != 0:
                stopped_reason = 'host-tracing-unavailable'
                print('Host shell verification is blocked; stopping the matrix.', flush=True)
            resource = resource_failure(output) if code != 0 else None
            if code in (-9, 137):
                resource = f'suite process was killed (exit {code})'
            if resource:
                row['failure_detail'] = resource
                stopped_reason = 'resource-or-containment-failure'
                print(f'Resource or containment failure at {row["finished_at"]}; stopping without retry: {resource}', flush=True)
    finally:
        signal.signal(signal.SIGINT, previous_interrupt)
    if cancelled.is_set():
        stopped_reason = 'interrupted'
    order = {name: index for index, (name, _, _) in enumerate(suites)}
    results.sort(key=lambda row: order[row['suite']])
    report = dict(full_matrix=full_matrix, complete=stopped_reason is None and len(results) == len(suites),
                  skipped_suites=[dict(suite=name, outcome='skip', reason=reason) for name, reason in skipped.items()],
                  selection_reason=selection_reason, changed=args.changed,
                  stopped_reason=stopped_reason, release=args.release, concurrent_suites=slots, memory_bytes=memory,
                  seconds=round(time.monotonic() - started, 3), workers=workers, suites=results)
    telemetry.update(report, finished_at=stamp(), load_end=load_average())
    try:
        history = load_history(host=telemetry.get('host'))
        report['growth_warnings'] = growth_warnings(history, report)
    except Exception as error:
        report['growth_warnings'] = []
        print(f'Gate telemetry unavailable: {type(error).__name__}', file=sys.stderr)
    (logs / 'summary.json').write_text(json.dumps(report, indent=2) + '\n')
    try:
        append_record(telemetry)
    except Exception as error:
        print(f'Gate telemetry unavailable: {type(error).__name__}', file=sys.stderr)
    publish_gate_report(telemetry, logs)
    for warning in report['growth_warnings']:
        print(f'WARN {warning}', flush=True)
    print('\nSuite                       Result     Seconds')
    for row in results:
        print(f'{row["suite"]:28} {row["outcome"].upper():6} {row["seconds"]:9.1f}')
    print(f'Total: {report["seconds"]:.1f}s; full logs: {logs}')
    return int(not report['complete'] or any(row['exit_code'] for row in results))


if __name__ == '__main__':
    sys.exit(main())
