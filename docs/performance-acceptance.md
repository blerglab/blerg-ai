# Foreground performance acceptance

Run `scripts/verification/cmd/performance-acceptance` outside the agent session, in a normal-priority
systemd **user service** on a quiet Linux machine. This follows Main's BLG-431
benchmark-environment decision. The branch includes main through `135a59cdc`; the reverted
SessionGroups initial-geometry change is not restored.

The default run takes about **40–60 minutes**, including exactly one
30-minute sustained soak. The November 7 production resume took 35 min 21.305 s,
plus the separate browser/route stages; see the [measured report](performance-acceptance-2026-11-07.md).
The short browser-driver fixture takes about 3 seconds; it is a correctness
check, not acceptance evidence.

Prepare the repository dependencies, Playwright Chromium, Linux `taskset` and `nice`, the pinned Go toolchain,
and a disposable-test PostgreSQL administration connection in `TEST_DATABASE_URL`.
Keep the connection in the environment; never put its value in this document or
command arguments. Supply the PID of the existing daemon to sample. The script
uses the existing release-performance orchestrator's disposable databases,
ports, binaries, production builds and cleanup. It does not change the running
installation or start agent engines.

Build from the checkout root, then run with `ACCEPTANCE_DAEMON_PID` set to the intended daemon PID:

```sh
mkdir -p "$PWD/.cache"
GOWORK=off "$HOME/go/bin/go" -C scripts/verification build \
  -o "$PWD/.cache/performance-acceptance" ./cmd/performance-acceptance
systemd-run --user --collect --wait --pipe \
  --property=Nice=0 --property=CPUWeight=100 \
  --working-directory="$PWD" --setenv=PATH --setenv=TEST_DATABASE_URL \
  "$PWD/.cache/performance-acceptance" \
  --daemon-pid "$ACCEPTANCE_DAEMON_PID" \
  --output "$PWD/.cache/foreground-acceptance/results.json"
```

Choose a fresh output directory for each run. `--plan --output <new-path>` writes
the command plan without executing benchmarks. `--help` lists the options.
Normal execution refuses nonzero niceness, initial one-minute host load of 4 or
more, a missing database environment, or a missing explicit daemon PID.
Close competing test/build workloads before starting. The initial load check
cannot guarantee that the machine stays quiet throughout the run.

## Measurements and JSON

`results.json` records the revision, CPU affinity, priority, initial load,
configuration, per-step exit status, fixture timings, production scenario
samples, boot/navigation timings, bundle assets and soak samples. Private
`evidence/` files retain raw test output and the existing release-performance
report. Do not publish raw evidence without inspecting it for private values.
The selected JSON timing fields omit credentials, query strings and response
bodies. Results are checkpointed; existing outputs are never overwritten.

The four stages are:

1. Quiet browser fixtures: native composer input with a long Main and multiple
   panels, session switch and warm Markdown reuse, tab switching, 400-card board,
   graph, Insights, first opening and open-tab polling demand.
2. The same fixtures under CPU load: one lower-priority busy worker per selected
   CPU, sharing the browser's affinity. By default the first four allowed CPUs
   are selected, bounded by `BLERG_TOOL_WORKERS`; `--cpus` overrides the selection.
   Only the script's own load processes are stopped afterward.
3. Focused route, initial-state and retained-memory tests, with Go JSON output.
   Any missing or skipped named test prevents acceptance.
4. Production release-performance scenarios (11 repetitions plus warm-up, three
   runs), three cache-disabled boot trials, and a single sustained soak in the
   primary run. The soak repeatedly switches workspaces and enters native keys
   without periodically reloading away caches. It records browser GC-normalized
   heap/DOM counters, RSS/threads/file descriptors for the owned services and the
   explicitly supplied daemon, API request-family demand, long tasks and the
   first cycle's sampled CPU frames. Long-task entries are capped at 2,000 with
   total count and an explicit truncation flag.

Exit codes are **0** for passed, **1** for failed budgets/tests, and **2** for an
incomplete run. The final JSON includes failure codes or a sanitized cause.
The 70 ms native keystroke-to-paint median must pass at both viewport widths,
quiet and loaded. Existing fixture assertions remain active. Explicit additional
defaults are a 3,000 ms informational boot target and 1,100,000 bytes of
entry JavaScript gzip (entry plus static preloads; lazy chunks excluded; Go gzip level 6 with zero mtime), 64 MiB
process RSS growth and 16 MiB GC-normalized browser heap growth. Memory growth
compares the first and last three sample medians; maxima are retained separately.
These additional thresholds are script defaults, not newly measured findings.
The boot target and bundle/typing budgets can be supplied explicitly on the command line.
A shortened soak can exercise the driver but cannot pass the 1,800-second gate.

## Interpretation limits

The 400-card board and graph/Insights timing coverage comes from the existing
browser fixtures. The real production release-performance dataset and scenarios
are unchanged (including its 150-card legacy board). Browser fixtures are run
quiet and loaded; production scenarios and the sustained soak run after the
synthetic CPU workers stop. The bundle is measured once from the production
build because CPU load does not change its bytes.

Daemon sampling observes the supplied daemon's existing workload; it does not
create a synthetic engine lifecycle. RSS is process residency, not a Go heap
profile or proof of a leak. Repeated browser GC normalizes retained-heap samples
but changes the workload. API family timing reports completed browser requests,
not a global server-route ranking. The focused route tests and existing
production scenarios supply complementary route timings. Raw fixture long-task
traces remain in their existing diagnostics; the soak's CPU summary covers only
its first navigation/input cycle. Read these qualifications with the results;
this script does not by itself certify every BLG-431 finding or close a card.

The initial November 7 foreground measurements and failures are preserved in
[the earlier report](performance-acceptance-2026-11-07.md). The
[single final run](performance-acceptance-final-2026-11-07.md) includes main's
render fixes and the historical boot comparison. Its aggregate passes; the
production typing-mark budget exception and remaining switch costs are explicit.

The aggregate now also checks the raw production typing samples in **all three**
calibrations, using release-perf's nearest-rank p95 and strict 16 ms ceiling.
It fails with `production:typing_mark_budget` even on a first run without an
official baseline; missing, nonnumeric, nonfinite or negative samples fail
closed. The recorded 49.3 ms final-run result therefore fails this corrected
policy; its historical JSON is preserved rather than relabeled. The separate
70 ms native key-to-paint median checks remain unchanged.

The subsequent [typing measurement correction](performance-follow-up-2026-11-07.md)
anchors production `clientSamplesMs` to the actual keydown. The former
command-clock delay is retained in separate numeric diagnostic arrays, rather
than charged to application commit time. The 16 ms gate is unchanged; historical
command-clock and corrected keydown-clock samples are not comparable baselines.

Gate regression proof: `go -C scripts/verification test ./cmd/performance-acceptance`
passes all 15 top-level tests, no skips. The new test first failed for the
observed regression, boundary and malformed samples; raising the ceiling to
160 ms reproduced the boundary/regression failures, then restoration passed.
Focused verification-module lint (zero issues), vet, build, scrub and diff checks
pass. A mistaken working-directory/PATH check invocation failed before execution
and was corrected; no benchmark was rerun for this policy change.

## Go port validation

The Python driver and its tests have been removed. The verification module is
already included in the workspace, lint, build, test and CI targets. The Node
production driver and Chromium pieces are unchanged by this port.

Ported coverage includes the four-stage plan, complete/missing/failed results,
70 ms typing assessment, missing/skipped routes, short soaks, bundle selection
and traversal rejection, credential canaries, production timing selection and
JSON permissions. Additional coverage exercises a complete CLI run with fake
benchmark commands, persisted interrupted results, CPU selection, real child
exit status, and load-worker readiness and cleanup. Tests never launch the real
benchmark services. Removing the foreground-priority guard makes its test fail;
restoring it passes.

Focused final commands (from the checkout root):

```sh
GOWORK=off "$HOME/go/bin/go" -C scripts/verification test -v ./cmd/performance-acceptance
GOWORK=off "$HOME/go/bin/go" -C scripts/verification vet ./cmd/performance-acceptance
GOWORK=off "$HOME/go/bin/go" -C scripts/verification build -o /tmp/blg431-performance-acceptance ./cmd/performance-acceptance
(cd scripts/verification && PATH="$HOME/go/bin:$PATH" GOWORK=off golangci-lint run --allow-serial-runners ./cmd/performance-acceptance/...)
scripts/scrub.sh
git diff --check
```

The initial compiling plan stub failed its behavioral test. The first lint run
identified issues subsequently fixed; a second invocation from the repository
root used the wrong package path and did not check the package. Final verification passed: 12 focused Go tests in 0.085 seconds, vet, build,
focused lint (zero issues), scrub and diff checks. No tests were skipped. The
plan-only CLI produced all four stages, and the real lower-priority invocation
persisted `incomplete: foreground_priority_required` without starting benchmarks.
Gzip uses Go's level-6 implementation with zero mtime; the JSON schema is retained,
but compressed byte counts may differ slightly from the former Python encoder. No frontend code changed in this port, so
frontend tests/builds are not repeated. The original Node/Chromium checks and
production-tool checks are described in the preceding commit; the full
foreground measurement later completed as documented in the November 7 report,
with acceptance failures retained.

## Historical boot comparison

The verified v0.5.4 boot-only run is recorded in
[boot-v0.5.4-2026-11-07.json](boot-v0.5.4-2026-11-07.json): cache-disabled
trials 3,487 / 8,237 / 8,137 ms, median 8,137 ms. All three services and
frontends are built from the unmodified tag. A private legacy working-directory
layout is supplied because that runner predates the web-dist override; the
served runner index must match the selected build byte-for-byte before seeding.
The common-schema boot seed preserves 446 sessions and 176,000 events, including
the 8,000-event Main; it excludes newer-only card links and worker-kind fields.
The ordinary full scenario seed is unchanged.

Both versions now use the same injected readiness: a real transcript event row
or folded tool-group DOM node, followed by two animation frames. Historical
folded groups have no `data-event-id`, so waiting only for that newer attribute
would not measure the old release. Neither the tag nor product rendering is
modified. Untimed runner login and transcript-tab selection happen first.

The default 3,000 ms boot value is an informational target. Pass the measured
same-machine JSON with `--boot-baseline docs/boot-v0.5.4-2026-11-07.json` to compare
medians. Only a slower measured median fails that regression gate; missing or
invalid trials still fail. The JSON retains the target result, both medians and
the measured delta. Three samples are not a confidence interval; inspect the
individual trials rather than interpreting a tiny delta as certain causation.

To repeat just the historical comparison, build `runner/cmd/release-perf` and
run it from this checkout with `-source-root <v0.5.4-worktree> -boot-only` and
`-logs <new-private-evidence-directory>`, using the same private database and
normal-priority service environment as the full run. It creates and drops only
its own disposable databases. No sustained soak or other scenarios run in this
mode. Explicit `GOCACHE`, `GOTMPDIR` and worker controls are now forwarded to all
server builds; earlier launcher code discarded those controls.
