#!/usr/bin/env python3
"""One-time generator that produced this directory's summary-*.json fixtures.

A real scripts/pre_merge.py summary.json runs to hundreds of suites and tens
of thousands of lines once every suite's slowest tests are included (the
originals here were ~125 KB each, from a real gate's
~/.cache/gate-*-logs/changed/summary.json). These fixtures keep only the
fields testtiming.Record and testtiming.Suite actually read (see
../types.go): top-level seconds, workers, concurrent_suites, changed,
complete, suites[].{suite, started_at, seconds, workers, exit_code, outcome,
tests.counts, tests.slowest[]}; and only three representative suites per
gate (runner-go, core-go, board-go), each with its three slowest tests
instead of thirty.

Already run once against the three original full summaries, which were then
deleted — this file now documents how, not a script to re-run as-is. To
regenerate from fresh real summaries, point SOURCES at their paths (they do
not need to live in this directory) and run:

    python3 derive_fixtures.py
"""
import json
from pathlib import Path

# dest filename (written here) -> source summary.json path to trim it from.
SOURCES = {
    "summary-c4489560.json": "summary-c4489560.json",
    "summary-d42b8f44.json": "summary-d42b8f44.json",
    "summary-ef53c85d.json": "summary-ef53c85d.json",
}
KEEP_SUITES = ("runner-go", "core-go", "board-go")
KEEP_SLOWEST = 3

HERE = Path(__file__).parent


def trim_suite(suite):
    tests = suite.get("tests", {})
    return {
        "suite": suite["suite"],
        "started_at": suite["started_at"],
        "seconds": suite["seconds"],
        "workers": suite["workers"],
        "exit_code": suite["exit_code"],
        "outcome": suite["outcome"],
        "tests": {
            "counts": tests.get("counts", {"pass": 0, "fail": 0, "skip": 0}),
            "slowest": tests.get("slowest", [])[:KEEP_SLOWEST],
        },
    }


def trim_summary(full):
    suites = [trim_suite(s) for s in full["suites"] if s["suite"] in KEEP_SUITES]
    return {
        "seconds": full["seconds"],
        "workers": full["workers"],
        "concurrent_suites": full["concurrent_suites"],
        "changed": full.get("changed"),
        "complete": full.get("complete", True),
        "suites": suites,
    }


def main():
    for dest, source in SOURCES.items():
        full = json.loads((HERE / source).read_text())
        trimmed = trim_summary(full)
        (HERE / dest).write_text(json.dumps(trimmed, indent=2) + "\n")
        print(f"{dest}: {len(json.dumps(trimmed))} bytes, {len(trimmed['suites'])} suites")


if __name__ == "__main__":
    main()
