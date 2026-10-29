#!/usr/bin/env python3
"""Require native host evidence; explicit Docker pending is not full acceptance."""
import argparse
import json
from pathlib import Path


# Exact package/test pairs only. No prefix, subtest or general Docker waiver.
docker_package = "github.com/blerglab/blerg-ai/runner/internal/daemon"
docker_cases = {
    "TestWorkspaceExplorerProductionDockerImage":
        "requires Docker Desktop on macOS and the locally built production sandbox image",
}

# Exact package/test pairs a macOS race run may skip for a known, carded performance limit.
# Never a security requirement (none of these may be in `required`); each names the card that removes it.
timing_waivers = {
    "TestWorkspaceSearchLargeIndexUnderContention":
        "BLG-319: Darwin samples Git's memory by spawning /bin/ps, too slow for the 1 s ignore budget under -race",
}

required = {
    "TestDarwinResolvedGitBypassesAppleShim",
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
    # Never preserve a previous acceptance if revalidation fails.
    metadata.update(accepted=False, native_host_accepted=False, pending=[])
    architecture = {"arm64": "arm64", "x86_64": "amd64"}.get(metadata.get("machine"))
    if (architecture is None or metadata.get("system") != "Darwin" or metadata.get("goos") != "darwin"
            or metadata.get("goarch") != architecture
            or metadata.get("commit") != commit or metadata.get("commit_after") != commit
            or not all(metadata.get(key) is True for key in ("race", "clean_before", "clean_after", "complete"))):
        raise SystemExit("Missing native, clean, completed evidence for the requested commit")
    if type(metadata.get("docker_present")) is not bool:
        raise SystemExit("Missing Docker presence evidence: docker_present must be a native harness boolean")
    passed = set()
    packages = set()
    docker_results = set()
    waived = []
    for filename in filenames:
        with open(filename, encoding="utf-8") as source:
            for line in source:
                event = json.loads(line)
                name = event.get("Test", "")
                listed = event.get("Package") == docker_package and name in docker_cases
                if event["Action"] == "fail":
                    raise SystemExit("macOS workspace gate failed or skipped: " + name)
                if event["Action"] == "skip" and event.get("Package") == docker_package and name in timing_waivers:
                    waived.append(name)
                    continue
                if event["Action"] == "skip":
                    if not listed:
                        raise SystemExit("macOS workspace gate has an unlisted skip: " + event.get("Package", "") + "/" + name)
                    if metadata["docker_present"]:
                        raise SystemExit("Docker is present; test must run and pass: " + name)
                    if not any(case["test"] == name for case in metadata["pending"]):
                        metadata["pending"].append(dict(test=name, package=docker_package,
                                                        reason=docker_cases[name], accepted=False))
                    docker_results.add(name)
                if event["Action"] == "pass":
                    passed.add(name)
                    if listed:
                        docker_results.add(name)
                    if not name:
                        packages.add(event.get("Package"))
    expected_packages = {"github.com/blerglab/blerg-ai/runner/internal/" + name for name in ("daemon", "hostgit", "workspacepolicy")}
    if not expected_packages <= packages:
        raise SystemExit("missing completed native package evidence")
    missing = required - passed
    if missing:
        raise SystemExit("missing native macOS evidence: " + ", ".join(sorted(missing)))
    missing_docker = docker_cases.keys() - docker_results
    if missing_docker:
        raise SystemExit("missing native Docker test evidence: " + ", ".join(sorted(missing_docker)))
    metadata["native_host_accepted"] = True
    metadata["accepted"] = not metadata["pending"]
    print("PASS: native macOS host guarantees; all non-Docker cases passed")
    for name in sorted(set(waived)):
        print("waived: " + timing_waivers[name] + ": " + docker_package + "/" + name)
    for case in metadata["pending"]:
        print("pending: " + case["reason"] + ": " + case["package"] + "/" + case["test"] + " [NOT accepted]")
    if metadata["pending"]:
        print("Native macOS workspace gate: NOT accepted; Docker cases pending")
    elif waived:
        print("Native macOS workspace gate: all required tests passed; carded timing waivers listed above")
    else:
        print("Native macOS workspace gate: all required tests passed, none skipped")


def check(metadata_path, commit, filenames):
    metadata_path = Path(metadata_path)
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    try:
        validate(metadata, commit, filenames)
    finally:
        metadata_path.write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--metadata", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("logs", nargs="+")
    args = parser.parse_args()
    check(args.metadata, args.commit, args.logs)
