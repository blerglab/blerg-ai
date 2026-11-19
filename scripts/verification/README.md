# Verification tools

These standard-library-only Go commands replace the BLG-298 Python tools.
They are separate main files so the existing gate can run the sharder from a
module's working directory without changing that directory or module selection.

From the repository root, with Go 1.26+:

```sh
(cd runner && GOWORK=off go run ../scripts/verification/cmd/go-test-shards/main.go \
  "$HOME/go/bin/go" test -json -p=2 -parallel=4 -timeout=45m ./...)
GOWORK=off go run scripts/verification/cmd/gate-timing/main.go \
  BASE HEAD --output /tmp/blerg-gate-comparison
```

Run the sharder from the Go module being tested (use an absolute source path
when outside the repository root). It accepts the original Go command unchanged
as positional arguments. `BLERG_TOOL_WORKERS` defaults to two and caps each
suite at six; other packages finish before up to four server shards start.
Every selected root, subtest, example and fuzz seed remains included. Explicit
focused selectors bypass discovery. The parent gate owns process-group deadlines;
children retain that process group. JSON lines are serialized across shards.

The benchmark retains the original `BASE HEAD --output DIR [--mode MODE]`
interface and `results.json`, summary, log and receipt schemas. Configure database
variables privately. It still invokes the existing Python pre-merge gate in each
snapshot; the benchmark and its legacy-make JSON reporter proxy are Go binaries.
The output directory is newly created, owner-only, and retained for inspection.
No worktrees or evidence are automatically deleted. Normal caches remain shared.

The Go tests cover disjoint selection, budgets, focused commands, discovery
failures, failed-shard draining, exact real-Go subtest/example/fuzz receipts,
comparison failures, Vitest normalization, both benchmark modes, persisted failed
runs, and the Go reporter proxy. Fake Git/npm/gate/make tools never fall through
to external services. The only real-Go fixture is a tiny dependency-free module.

```sh
cd scripts/verification
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go build ./...
```

The module is listed in `go.work`. `make fmt` and `make check-format` include it;
`make lint` checks it with the shared pinned configuration. `make test-verification`
runs full vet, tests (without duplicate implicit vet), and builds with a worker
budget from `BLERG_TOOL_WORKERS` (default two). Both `make test` and
`make check-scripts` depend on that target. CI has a verification lint matrix
entry and a Go setup/test/build job using the same target. The full pre-merge
matrix also runs verification lint and the shared target.

## Foreground performance acceptance

`cmd/performance-acceptance` replaces the BLG-431 Python wrapper. Build it from
this module and run the binary from the repository root. It retains the same
four benchmark stages, flags and JSON schema, driving the existing Node and
Chromium tools as subprocesses. Linux `taskset` and `nice` are required.
See [the foreground runbook](../../docs/performance-acceptance.md) for the
normal-priority systemd command, private environment setup, estimated 40–60
minute duration, output interpretation and measurement limits.
