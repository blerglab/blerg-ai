#!/usr/bin/env python3
"""Record one focused worker suite: gate_record.py --suite name -- go test -json ..."""
from ui_suite_lock import command_lock_fds, inherited_lock_fd, ui_suite_lock
import argparse
import os
from pathlib import Path
import subprocess
import tempfile
import time
from gate_telemetry import append_record, go_json_command, load_average, needs_ui_lock, stamp, start_record, test_results, vitest_command
import vitest_results_cache


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--suite', required=True)
    parser.add_argument('--rerun-of', help='prior run identifier; no automatic retries')
    parser.add_argument('command', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ['--'] else args.command
    if not command:
        parser.error('a command is required')
    try:
        workers = int(os.environ.get('BLERG_TOOL_WORKERS', '1'))
        if workers < 1:
            raise ValueError
    except ValueError:
        parser.error('BLERG_TOOL_WORKERS must be positive')
    record = start_record(workers, full_matrix=False, rerun_of=args.rerun_of)
    root = Path(__file__).resolve().parent.parent
    try:
        cwd = Path.cwd().relative_to(root).as_posix()
    except ValueError:
        cwd = ''
    started = time.monotonic()
    outer_lock = os.environ.get('BLERG_UI_SUITE_LOCK')
    outer_fd = inherited_lock_fd(outer_lock) if outer_lock else None
    code = 130
    with tempfile.TemporaryDirectory(prefix='blerg-gate-') as tmp:
        log, vitest = Path(tmp) / 'suite.log', Path(tmp) / 'vitest.json'
        command, is_vitest = vitest_command(go_json_command(command), vitest)
        try:
            with ui_suite_lock(is_vitest and needs_ui_lock(command, cwd)) as lock_fd, log.open('w') as output:
                if is_vitest:
                    vitest_results_cache.best_effort(root, cwd, restore=True)
                proc = subprocess.Popen(command, env=dict(os.environ, BLERG_GATE_VITEST_RERUNS=str(vitest) + '.reruns.json'), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, pass_fds=command_lock_fds(command, outer_fd, lock_fd, go_binary=os.environ.get('GO')))
                try:
                    for chunk in iter(lambda: proc.stdout.read1(65536), b''):
                        output.write(chunk.decode(errors='replace'))
                        print(chunk.decode(errors='replace'), end='', flush=True)
                    code = proc.wait()
                finally:
                    if proc.poll() is None:
                        proc.terminate()
                        proc.wait()
        except OSError as error:
            print(str(error))
            code = 127
        finally:
            if is_vitest:
                vitest_results_cache.best_effort(root, cwd, restore=False)
            try:
                record.update(finished_at=stamp(), load_end=load_average(), seconds=round(time.monotonic()-started, 3), complete=code != 130)
                record['suites'] = [dict(suite=args.suite, seconds=record['seconds'], exit_code=code,
                                         outcome='pass' if code == 0 else 'fail', started_at=record['started_at'], load_start=record['load_start'], load_end=record['load_end'], workers=workers, tests=test_results(log, vitest))]
                append_record(record)
            except Exception as error:
                print(f"Gate telemetry unavailable: {type(error).__name__}")

    return code


if __name__ == '__main__':
    raise SystemExit(main())
