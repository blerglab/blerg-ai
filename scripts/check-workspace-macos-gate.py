#!/usr/bin/env python3
"""Require native evidence; a skipped test is not platform acceptance."""
import argparse
import json

required = {
    "TestWorkspaceWireCancelBeforeWriterPreservesConnection",
    "TestWorkspaceWireCancelActiveWritePoisonsConnection",
    "TestWorkspaceDescriptorWalkMovedAncestors",
    "TestWorkspaceExplorerConcurrentRename",
    "TestWorkspaceExplorerIntermediateSymlink",
    "TestWorkspaceExplorerListSearchPathMatrix",
    "TestWorkspaceExplorerProtectedChildrenAndLinks",
    "TestWorkspaceExplorerRootAndMemberVisibility",
    "TestWorkspaceExplorerCursorReplayMatrix",
    "TestWorkspaceExplorerWorktreeSpecialPointers",
    "TestWorkspaceExplorerOtherDeviceMount",
    "TestWorkspaceExplorerFIFOAndSocket",
    "TestWorkspaceExplorerDevice",
    "TestWorkspaceExplorerGitStatusSafety",
    "TestWorkspaceExplorerHostWorktreePins",
    "TestWorkspaceExplorerStatusNeverRunsCleanFilters",
    "TestWorkspaceStatusExactMemoryLimit",
    "TestWorkspaceStatusMemoryBoundary",
    "TestWorkspaceStatusCancellationJoinsBlockedGit",
    "TestWorkspaceExplorerExecutablePipesAndViewerProgress",
    "TestWorkspaceExplorerVisitedExhaustion",
    "TestWorkspaceExplorerAggregateVisitedExhaustion",
}


def validate(metadata, commit, filenames):
    architecture = {"arm64": "arm64", "x86_64": "amd64"}.get(metadata.get("machine"))
    if (architecture is None or metadata.get("system") != "Darwin" or metadata.get("goos") != "darwin"
            or metadata.get("goarch") != architecture
            or metadata.get("commit") != commit or metadata.get("commit_after") != commit
            or not all(metadata.get(key) is True for key in ("race", "clean_before", "clean_after", "complete"))):
        raise SystemExit("Missing native, clean, completed evidence for the requested commit")
    passed = set()
    packages = set()
    for filename in filenames:
        with open(filename, encoding="utf-8") as source:
            for line in source:
                event = json.loads(line)
                name = event.get("Test", "")
                if event["Action"] == "fail" or (event["Action"] == "skip" and name.startswith("TestWorkspace")):
                    raise SystemExit("macOS workspace gate failed or skipped: " + name)
                if event["Action"] == "pass":
                    passed.add(name)
                    if not name:
                        packages.add(event.get("Package"))
    expected_packages = {"github.com/blerglab/blerg-ai/runner/internal/" + name for name in ("daemon", "hostgit", "workspacepolicy")}
    if not expected_packages <= packages:
        raise SystemExit("missing completed native package evidence")
    missing = required - passed
    if missing:
        raise SystemExit("missing native macOS evidence: " + ", ".join(sorted(missing)))
    print("Native macOS workspace gate: all required tests passed, none skipped")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("logs", nargs="+")
    args = parser.parse_args()
    with open(args.metadata, encoding="utf-8") as source:
        validate(json.load(source), args.commit, args.logs)
