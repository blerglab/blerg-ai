#!/usr/bin/env python3
"""Print suite and retained test costs; compare two JSONL runs by index."""
import argparse
import json
from pathlib import Path
import os
from gate_telemetry import DEFAULT_PATH, compare


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('path', nargs='?', type=Path, default=Path(os.environ.get('BLERG_GATE_TELEMETRY', DEFAULT_PATH)))
    parser.add_argument('--run', type=int, default=-1, help='zero-based run index; default latest')
    parser.add_argument('--compare', type=int, help='compare selected run against this earlier index')
    args = parser.parse_args()
    try:
        runs = [json.loads(line) for line in args.path.read_text().splitlines() if line.strip()]
        selected = runs[args.run]
        if args.compare is not None:
            for label, rows in compare(runs[args.compare], selected).items():
                print(label + ' (seconds, after - before):')
                for row in rows:
                    print(f"{row['delta_seconds']:+10.3f}  {row['name']}")
        else:
            print(f"Run {args.run}: {selected.get('id', '')} {selected.get('started_at')} {selected.get('commit')}")
            for row in sorted(selected['suites'], key=lambda r: r['seconds'], reverse=True):
                print(f"{row['seconds']:10.3f}s  {row['suite']}  {row.get('outcome', row.get('exit_code'))}")
            tests = [(row['suite'], test) for row in selected['suites'] for test in row.get('tests', {}).get('slowest', [])]
            print('Tests (retained slowest 30 per suite):')
            for suite, test in sorted(tests, key=lambda pair: pair[1]['seconds'], reverse=True):
                print(f"{test['seconds']:10.3f}s  {suite} {test.get('package', '')} {test['test']}")
    except (OSError, ValueError, IndexError) as error:
        parser.error(str(error))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
