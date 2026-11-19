# Local failure campaign (BLG-180)

This campaign runs existing production-path fixtures without Kubernetes, a
kubeconfig, a database, external repositories, provider accounts or model access.
It creates temporary local repositories and loopback TLS servers and exercises
real Git clone, snapshot, push and resume. It also exercises runtime request and
shell boundaries with fixture transports and containment evidence. It is not
cluster end-to-end acceptance and does not clear any deferred `cases.json` case.

From the `runner` module directory, on Linux with Go and Git installed:

```sh
GOWORK=off go run ./cmd/cluster-failure-test -race > failure-fixtures.jsonl
```

Use `-go /path/to/go` when the Go executable is not on PATH. The command runs
three selected suites sequentially with ten-minute package timeouts. It honors
the environment's Go worker limits. A missing required test, any skipped test or
subtest, a malformed test stream, or a failed package exits nonzero. Receipts
contain package names, pass/fail/skip event counts (including subtests), duration
and status, never raw test output. For diagnostics, rerun the selected package
with `go test -v` locally. Race mode deliberately reruns tests as needed; no
cache-discarding flag is added. The optional privileged `P3_TEST_QUOTA` fixture
mode is explicitly disabled: this command never mounts or fills a filesystem.

## Coverage and limits

| Campaign case | Local evidence selected by the command | Still requires a disposable cluster or provider setup |
| --- | --- | --- |
| mixed-providers, credential-boundary, home-private | DiskGit TLS fixture uses two provider identities, scoped credentials, anonymous read without push authority, hostile Git metadata and HOME exclusion; plan credential refusals | Actual two-provider private accounts, redirects, duplicate remote names and protected allocation against a hostile agent |
| partial-prepare | Second clone failure rolls back proven unused first clone; lifecycle readiness refuses partial preparation | PVC allocation failure and retained prior-work rollback |
| partial-publication-resume | Real second-remote push refusal, preserved successful publication, dirty-file/index retention, exact remote SHAs and reopened DiskGit identity resume | Exact retained claim across pod replacement, dirty-work recovery |
| stop-fencing, replacement-fencing, natural-exit | Supervisor uncertainty and freeze/thaw/stop failure fixtures fence admission and retain recovery state | Real Job/Pod UIDs, physical writer death, natural exits and durable cleanup |
| containment-reader-joins, lease-freeze | Outbound request disconnect joins/fences replies; shell atomic admission, lost helper and expired PTY cases | Real cgroups, detached descendants, memory accounting and leases across process freeze |
| outbound-transport | Bound outbound requests, isolation, budgets and disconnect handling | Real hello/control/explorer/viewer/PTY path, flooded transport fairness and effective no-exec RBAC |
| csi-bytes, csi-inodes-metadata | Unbounded storage fails closed; real Git fixture exercises aggregate accounting without physical exhaustion | Quota-enforcing CSI, byte/inode ENOSPC, metadata growth and node headroom |
| placement-rbac, writer-fence, job-secret-token-cleanup | Not covered by this command | Scheduler exclusion, RBAC denial, exact Job/Secret cleanup and durable token revocation |
| restart-orphan-recovery, storage-remount, partition-fencing | Not covered by this command | Isolated restart/partition injection, physical fencing proof and retained claim identity |
| measure-{1,5,20}-{ordinary,metadata-heavy} | Not measured | Target image/CSI/providers, cold/warm samples, percentiles, bytes/inodes, CPU/memory, control and cleanup latency |

The 25 catalog entries (two builtins and 23 drivers) remain deferred. Their old
0.5.1 release labels describe the original deferral; BLG-180 now targets 0.6.0.
Do not interpret a fixture pass as a lab pass. A kind cluster alone cannot prove
CSI quotas, provider credentials or physical fencing under partition. None was
created or contacted for this change.

Estimate for the complete campaign: size XL, risk high, complexity high. The
local runner is a bounded first step. Board estimates could not be updated from
the builder session because board read/write access was unavailable.

## Builder verification limitation

On 2026-11-01 at 07:04 UTC the race campaign passed clusterworkspace (17 test
pass events) and runner (9), then failed shellruntime (three failed tests and a
failed package event, no skips). A direct verbose rerun at 07:04:53 UTC reported:

```text
exec_shell: The account shell could not be executed. Check its permissions and service restrictions. permission denied starting shell (paths and details redacted)
```

The failing assertions were `cluster_test.go:47` (admitted PTY failed), `:104`
and `:143` (open). No containment bypass or outside-session rerun was attempted.
This is limited evidence: the campaign is not green in the builder environment.
Rerun on a disposable Linux environment that permits the shell fixtures before
claiming complete local coverage. Failure recording on the board was also
unavailable because the session had no board context/access.
