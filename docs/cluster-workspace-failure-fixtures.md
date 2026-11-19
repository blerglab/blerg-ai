# Cluster workspace failure fixtures (BLG-180)

These local fixtures exercise production workspace preparation, Git transport,
retained on-disk identities, launch diagnostics and Kubernetes Job retry policy.
They do not need provider accounts, credentials or an existing cluster.

| Failure | Proof |
| --- | --- |
| Clone failure | An owned Git HTTP endpoint returns 404; preparation reports `repository_clone_failed`. Publication requests after failure make no further transport calls. |
| Credential refusal | The endpoint returns 401 and 403 without any external fallback. A mismatched credential host is refused before transport with `credential_scope_refused`; public read access supplies no push authority. |
| Pod OOM | A disposable Docker container touches memory until its cgroup kills it. Inspection requires `OOMKilled`, exit 137 and zero restarts. Server tests preserve the OOM cause even with a generic pod failure reason. |
| Restart and resume | A fresh runtime and disk handle recover an unpublished commit and uncommitted file from retained identities without network traffic. Existing reconciler tests prevent old pod failures from overwriting an active resume. |
| Storage loss | Removing the owned retained projects tree refuses resume with `retained_storage_missing`, without cloning a replacement or issuing publication retries. |

The generated Job must have `backoffLimit: 0` and `restartPolicy: Never`.
Repeated reconciliation already tests one persisted failure event per observation;
resumption stays an explicit action. Typed preparation causes survive through the
runner's user-facing launch stage and the server's diagnostic receipt. Upstream
error text is excluded from logs, errors and member results; a canary test checks
that exclusion. Diagnostic logging uses the shared rate-limited recorder.

From `runner`, use the prescribed Go toolchain and dedicated test database:

```sh
export GOWORK=off
export TEST_DATABASE_URL=postgres://postgres:pw@127.0.0.1:55440/postgres
export DATABASE_URL=$TEST_DATABASE_URL
"$HOME/go/bin/go" test ./internal/clusterworkspace -timeout 3m
"$HOME/go/bin/go" test ./internal/runner -run '^(TestClusterWorkspaceFailureRecovery|TestClassifyCloneError|TestClassifyWorkspaceErrorOnlyBlamesCloneForCloneFailures|TestClusterWorkspaceStoragePlanEnforcement|TestClusterRunnerRefusesMissingContainmentBeforeAllocation|TestClusterRunnerRefusesAgentWritableSnapshotLocation)$' -timeout 3m
P6_TEST_DOCKER=1 "$HOME/go/bin/go" test ./internal/runner -run '^TestPodUIDStorageBoundaryDocker$' -count=1 -timeout 5m -v
"$HOME/go/bin/go" test ./internal/server -run '^(TestClusterFailureJobNeverRetries|TestClusterWorkspaceFailureLaunchCauses|TestClusterFailurePodCauseSurvivesMissingStatus|TestDisconnectedPodTerminationExplainedAndLabeled|TestPodTerminationDoesNotRewriteResumedSession|TestRunningPodTerminationRemainsResumable|TestPodFailureClearsWhenResumeBegins|TestStaleFailedJobDefersToOpenResume)$' -timeout 3m
```

The Docker test extends `cluster_pod_docker_test.go` and runs the Git/recovery
cases inside the production supervisor UID/PID boundary before its existing shell
proof. Its disposable image installs Git. It also runs the real cgroup OOM check.
The Job-spec retry proof reuses `worker_tools_pod_test.go`'s generated-Job fixture.
The existing worker-tools Docker test can additionally run with
`BLERG_TEST_WORKER_POD_IMAGE` naming a local image containing Python and setpriv.
Docker tests require uncached runs because their external images are not tracked
by Go's test cache. Containers, temporary repositories and the built image are
fixture-owned and cleaned up.

Scope limits: this is local cross-service/runtime evidence, not a full deployed
installation campaign. Restart reconstructs runtime state over retained files;
it does not restart Kubernetes or the runner server. Storage loss removes a
fixture-owned tree; it does not destroy a real PVC. CSI byte/inode exhaustion,
multiple private providers, Job/Secret/token garbage collection, orphan recovery
and 1/5/20 measurements still require the separately configured lab campaign on
BLG-180. No live cluster or quota-enforcing storage class is claimed here.


BLG-570 extends the generated-Job worker-tools fixture with a real Git commit
using the Job's default author/committer identity, the production AgentHost
turn-end bundle hook, a captured artifact upload through the shipped CLI, and
the shipped session import command against Main's separate repository.
It requires the dedicated test database as well as the local pod image.
The HTTP artifact store is fixture-owned; this is not a deployed-cluster test.
Workspace bundles use the prepared projects root and manifest member paths.
Fresh clones record refs/blerg/result-base at their launch commit, including
nondefault bases. Bundle skips and failures emit fixed diagnostic causes.
The default pod Git identity is Blerg Agent <agent@blerg.invalid>.
Launch pins the remote branch's advertised SHA; a stale local branch is checked
for unpushed commits but does not select the worker's base.
