"""Shared local gate recorder. JSONL contains metadata and test names, never output."""
from collections import defaultdict
from datetime import datetime, timezone
import fcntl
import json
import os
import re
from pathlib import Path
import socket
import subprocess
import sys
import uuid

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_PATH = ROOT / '.superpowers/gate-runs.jsonl'


def stamp():
    return datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')


def load_average():
    try:
        return list(os.getloadavg())
    except OSError:
        return None


def start_record(workers, **extra):
    commit = ''
    try:
        with subprocess.Popen(['git', 'rev-parse', 'HEAD'], cwd=ROOT,
                              stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True) as proc:
            commit, _ = proc.communicate()
    except OSError:
        pass
    try:
        host = socket.gethostname()
    except OSError:
        host = None
    return dict(id=str(uuid.uuid4()), started_at=stamp(), commit=commit.strip() or None,
                host=host, workers=workers, load_start=load_average(), suites=[], **extra)


def load_history(path=None, host=None, limit=200):
    """The most recent records from the same host, oldest first. A line that cannot
    be parsed as a JSON object is skipped rather than aborting the read: a partial
    write from a killed process must not hide every earlier run's history."""
    path = Path(path or os.environ.get('BLERG_GATE_TELEMETRY', DEFAULT_PATH))
    if not path.exists():
        return []
    records = []
    with path.open(encoding='utf-8', errors='replace') as stream:
        for line in stream:
            try:
                record = json.loads(line)
            except ValueError:
                continue
            if isinstance(record, dict) and (host is None or record.get('host') == host):
                records.append(record)
    return records[-limit:]


def rolling_median(values):
    values = sorted(values)
    count = len(values)
    if count == 0:
        return None
    middle = count // 2
    return values[middle] if count % 2 else (values[middle - 1] + values[middle]) / 2


def growth_warnings(history, report, window=10, threshold=1.25, minimum_samples=3):
    """Compare this run's suite and per-test seconds against a same-host rolling
    median. A suite growing because it gained tests is told apart from one that
    is actually slower: the per-test median is checked independently of the
    suite total, and a slow individual test is flagged even when its suite's
    total time did not grow (other tests may have gotten faster)."""
    warnings = []
    suite_history = defaultdict(list)
    test_history = defaultdict(list)
    for record in history:
        for row in record.get('suites', []):
            suite_history[row['suite']].append(row)
            for test in row.get('tests', {}).get('slowest', []):
                key = (row['suite'], test.get('package', ''), test.get('test', ''))
                test_history[key].append(test.get('seconds', 0))
    for row in report.get('suites', []):
        name = row['suite']
        past_rows = suite_history.get(name, [])[-window:]
        if len(past_rows) >= minimum_samples:
            seconds_median = rolling_median([r['seconds'] for r in past_rows])
            if seconds_median and row['seconds'] > seconds_median * threshold:
                pct = (row['seconds'] / seconds_median - 1) * 100
                count = sum(row.get('tests', {}).get('counts', {}).values())
                past_counts = [sum(r.get('tests', {}).get('counts', {}).values()) for r in past_rows]
                count_median = rolling_median(past_counts) or 0
                per_test_now = row['seconds'] / count if count else row['seconds']
                per_test_history = [r['seconds'] / c for r, c in zip(past_rows, past_counts) if c]
                per_test_median = rolling_median(per_test_history)
                if per_test_median and per_test_now > per_test_median * threshold:
                    warnings.append(
                        f"{name}: time grew {pct:.0f}% over its rolling median ({seconds_median:.1f}s -> "
                        f"{row['seconds']:.1f}s) and per-test time grew too ({per_test_median:.3f}s -> "
                        f"{per_test_now:.3f}s/test) -- looks like a slowdown, not just more tests")
                elif count > count_median * threshold or (count_median == 0 and count):
                    warnings.append(
                        f"{name}: time grew {pct:.0f}% over its rolling median ({seconds_median:.1f}s -> "
                        f"{row['seconds']:.1f}s), but test count grew too ({count_median:.0f} -> {count}) "
                        f"-- looks like added coverage, not a slowdown")
                else:
                    warnings.append(
                        f"{name}: time grew {pct:.0f}% over its rolling median ({seconds_median:.1f}s -> "
                        f"{row['seconds']:.1f}s)")
        for test in row.get('tests', {}).get('slowest', []):
            key = (name, test.get('package', ''), test.get('test', ''))
            past_times = test_history.get(key, [])[-window:]
            if len(past_times) < minimum_samples:
                continue
            median = rolling_median(past_times)
            if median and test.get('seconds', 0) > median * threshold:
                pct = (test['seconds'] / median - 1) * 100
                warnings.append(
                    f"{name}: {test['test']} grew {pct:.0f}% over its rolling median "
                    f"({median:.2f}s -> {test['seconds']:.2f}s)")
    return warnings


def append_record(record, path=None):
    try:
        path = Path(path or os.environ.get('BLERG_GATE_TELEMETRY', DEFAULT_PATH))
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open('a', encoding='utf-8') as stream:
            fcntl.flock(stream, fcntl.LOCK_EX)
            stream.write(json.dumps(record, separators=(',', ':')) + '\n')
            stream.flush()
            os.fsync(stream.fileno())
        return True
    except (OSError, ValueError, TypeError) as error:
        print(f'Gate telemetry unavailable: {type(error).__name__}', file=sys.stderr)
        return False


def relative_package(value):
    path = Path(value)
    if not path.is_absolute():
        return value
    try:
        return path.relative_to(ROOT).as_posix()
    except ValueError:
        return '<external>/' + path.name


def test_results(log, vitest=None):
    rows = []
    if Path(log).exists():
        with Path(log).open(errors="replace") as stream:
            for line in stream:
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if isinstance(event, dict) and event.get('Test') and event.get('Action') in ('pass', 'fail', 'skip'):
                    rows.append(dict(package=event.get('Package', ''), test=event['Test'],
                                     seconds=event.get('Elapsed', 0), result=event['Action']))
    if vitest and Path(vitest).exists():
        try:
            data = json.loads(Path(vitest).read_text())
        except (ValueError, OSError):
            data = {}
        for suite in data.get('testResults', []):
            for test in suite.get('assertionResults', []):
                status = {'passed': 'pass', 'failed': 'fail', 'pending': 'skip', 'skipped': 'skip', 'todo': 'skip'}.get(test.get('status'))
                if status:
                    rows.append(dict(package=relative_package(suite.get('name', '')), test=test.get('fullName', test.get('title', '')),
                                     seconds=(test.get('duration') or 0) / 1000, result=status))
    counts = {result: sum(row['result'] == result for row in rows) for result in ('pass', 'fail', 'skip')}
    attempts = defaultdict(list)
    for row in rows:
        attempts[(row['package'], row['test'])].append(row['result'])
    reruns = [dict(package=p, test=t, outcomes=outcomes) for (p, t), outcomes in attempts.items()
              if 'fail' in outcomes and 'pass' in outcomes[outcomes.index('fail') + 1:]]
    retry_file = Path(str(vitest) + '.reruns.json') if vitest else None
    if retry_file and retry_file.exists():
        try:
            reruns.extend(json.loads(retry_file.read_text()))
        except (ValueError, OSError):
            pass
    for row in reruns:
        if 'package' in row:
            row['package'] = relative_package(row['package'])
    return dict(counts=counts, slowest=sorted(rows, key=lambda r: r['seconds'], reverse=True)[:30], flake_reruns=reruns)


def vitest_command(command, output):
    """Keep normal output and write separate machine-readable test durations."""
    command = list(command)
    npm_test = command[:2] == ['npm', 'test'] or command[:3] == ['npm', 'run', 'test:performance']
    is_vitest = 'vitest' in command or npm_test
    if is_vitest:
        if npm_test and '--' not in command:
            command.append('--')
        command += ['--reporter=default', '--reporter=json', '--reporter=' + str(ROOT / 'scripts/gate_vitest_reporter.mjs'), '--outputFile=' + str(output)]
    return command, is_vitest


def needs_ui_lock(command, cwd=None):
    """Selected files/related tests and unit projects never block the full UI gate."""
    if cwd in ('core/web', 'packages/chat'):
        return False
    selected_files = any(
        re.search(r'\.(?:test|spec)\.[cm]?[jt]sx?(?::\d+)?$', arg) and not arg.startswith('-')
        and (i == 0 or command[i - 1] not in ('--exclude', '--reporter', '--outputFile', '--testNamePattern', '-t'))
        for i, arg in enumerate(command))
    if 'related' in command or selected_files:
        return False
    projects = [arg.split('=', 1)[1] for arg in command if arg.startswith('--project=')]
    projects += [command[i + 1] for i, arg in enumerate(command[:-1]) if arg == '--project']
    if projects:
        return any('browser' in project for project in projects)
    return ('vitest' in command or command[:2] == ['npm', 'test'] or
            command[:3] in (['npm', 'run', 'test:performance'], ['npm', 'run', 'test:browser']))


def compare(before, after):
    def costs(record):
        suites, tests = {}, {}
        for row in record.get('suites', []):
            suite = row['suite']
            suites[suite] = row['seconds']
            for test in row.get('tests', {}).get('slowest', []):
                key = suite + ':' + test.get('package', '') + ':' + test['test']
                tests[key] = tests.get(key, 0) + test['seconds']
        return suites, tests
    a, b = costs(before), costs(after)
    result = {}
    for index, label in enumerate(('suites', 'tests')):
        result[label] = sorted([dict(name=key, before_seconds=a[index].get(key), after_seconds=b[index].get(key),
                                    delta_seconds=b[index].get(key, 0) - a[index].get(key, 0))
                                for key in a[index].keys() | b[index].keys()],
                               key=lambda row: abs(row['delta_seconds']), reverse=True)
    return result


def go_json_command(command):
    command = list(command)
    if not command or Path(command[0]).name != 'go' or '-json' in command:
        return command
    index = 1
    while index < len(command):
        if command[index] == '-C':
            index += 2
        elif command[index].startswith('-'):
            index += 1
        else:
            if command[index] == 'test':
                command.insert(index + 1, '-json')
            break
    return command
