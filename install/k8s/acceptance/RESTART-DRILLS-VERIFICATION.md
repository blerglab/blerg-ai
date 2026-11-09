# BLG-433 offline verification

This continuation completes the lifecycle scaffold from
`9e4e1f91b8ef9e7be64ff90ba7c33c12118435de`; the original worktree base remains
`15afe83156b47ac8ff545248d6185609c660b415`. It implements the full private install,
ingress rendering, session-suite prerequisite, three live fault paths and
ownership-checked teardown. It supersedes the earlier incomplete-scaffold report.

No cluster, real model, existing session or external database was contacted.
All Kubernetes calls in tests use a fake executor. The fixture implementation
proof uses a loopback HTTP fixture and a private temporary Git repository.

## Final commands and results

From `runner/`, using the requested Go PATH, `GOWORK=off`, database environment
and pinned `GOLANGCI_LINT`. Final checks used `GOMAXPROCS=1 GOFLAGS=-p=1` to keep
builds small. The supplied database environment was not consumed by these
command-package tests; the URL policy tests replace it with fixture data.

```sh
export PATH=$HOME/go/bin:$HOME/.local/go/bin:$PATH
export GOWORK=off GOMAXPROCS=1 GOFLAGS=-p=1
# TEST_DATABASE_URL, DATABASE_URL and GOLANGCI_LINT as supplied for this task.
gofmt -w cmd/restart-drills cmd/session-suite/main.go
"$GOLANGCI_LINT" run --concurrency 1 --allow-serial-runners --config ../.golangci.yml ./cmd/restart-drills/... ./cmd/session-suite/...
go vet ./cmd/restart-drills ./cmd/session-suite
go build -o /tmp/blg433-restart-drills ./cmd/restart-drills
go build -o /tmp/blg433-session-suite ./cmd/session-suite
go test ./cmd/restart-drills ./cmd/session-suite -json
```

| Whole touched package | Top-level passes | Subtest passes | Failures / skips | Package seconds |
| --- | ---: | ---: | ---: | ---: |
| `cmd/restart-drills` | 24 | 29 | 0 / 0 | 0.711 |
| `cmd/session-suite` | 1 | 6 | 0 / 0 | cached |

The session-suite parser tests were cached in the final normal invocation; they
also ran uncached successfully with the race detector. No `-count=1` was used.
Vet and both Go binary builds passed. Regional lint reported **0 issues**.

Complete race checks ran after restoring the ownership mutation and again after
strengthening old-server-pod absence proof:

```sh
go test ./cmd/restart-drills ./cmd/session-suite -race -json
```

Final race results: restart-drills **24 + 29 passes**, 2.020 s; session-suite
**1 + 6 passes**, cached. An earlier uncached race run of session-suite passed in
1.038 s. No failures, skips or race reports. These are package event
elapsed times, excluding compilation. No repeated timing campaign is claimed.

Repository checks, after staging all new files:

```sh
bash scripts/scrub.sh
git diff --cached --check
bash scripts/check_links.sh
```

## Proof and development failures

Offline coverage includes full install/reference rendering; no-contact plan;
namespace create/journal sequencing; UID-preconditioned deletion and completion;
changed configuration and foreign ownership refusal; storage/ingress collision
checks; owned resourceVersion-guarded mutations; fixture exec confinement; all
three controller case paths; failed prerequisite/fault/assertion recording;
immutable image and server replacement proof; event prefix/duplicate refusal;
authentication and diagnostic redaction; regression receipt completeness; the
HTTP mid-turn barrier with real Git; and refusal to recreate a lost recovery marker.

Development caught a composite-literal compile error, three stale lifecycle tests
as the scaffold grew, and lint findings; all were corrected. One deliberate
mutation removed the namespace label comparison from `owned`, then ran:

```sh
go test ./cmd/restart-drills -run '^TestOwnership$'
```

It failed exactly the same-UID/different-label assertion (one top-level failure),
proving UID alone cannot authorize teardown. The original file was restored
byte-for-byte before the complete race and final normal runs.

No frontend region changed, so no frontend/browser suite or screenshots were
needed. No daemon/server production package changed, so their whole suites were
not run here. Their nine existing regression scenarios are invoked by the live
controller inside its private image/database, not on the operator's host. The
race run here covers only the two touched command packages. No full-repository,
release, Docker-image or live-cluster gate is claimed.

No `/tmp/cx/outside.sh` or ptrace tracer was used during this work. The fixture
image needs pod-local ptrace for its later operator-run regression prerequisite.
No images or containers were created. Temporary Go binaries, JSON test logs,
mutation backup and summary helper were removed after recording results. Shared
Go caches remain. No push or board operations were performed. The failure-mark
CLI requires a board card; board access was unavailable as specified.

## Operator handoff

Use the exact environment and `plan`/`all` commands in
[the restart drill runbook](RESTART-DRILLS.md). The Go binaries compile and the
controller/fixture contracts pass offline; the container image has not been built
here, and the full Core/PostgreSQL bootstrap, Kubernetes rollout, ingress routing,
CNI isolation and storage reclamation still require the first owner-run drill.
A failed live assertion retains the namespace and records failure; it cannot turn
into a skipped or local-only pass. No product decision is waiting: the operator
supplies the disposable context, private DNS names, storage/ingress classes and
previous/candidate images. Real-model acceptance remains a separate specification.
