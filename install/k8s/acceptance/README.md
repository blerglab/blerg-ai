# Disposable workspace pod acceptance

This directory **prepares** an isolated install and a fail-closed campaign. It
has not been deployed or run against a lab. Both release constants stay false;
only the explicitly test-built runner/pod images use the existing
`blerg_workspace_integration` build tag. Never promote these images to releases.

**Coverage gap:** this repository currently has one actual Kubernetes opt-in,
`TestClusterWorkspaceRealPodWriterFence`. That sleep-Job test proves only its
writer deletion/replacement assertions. The other P3/P6 deployment requirements
are not existing executable pod tests. `cases.json` names all of them, including
six target-runtime measurement profiles. The runner requires reviewed,
lab-specific executable adapters for its **23 driver cases**. None is silently
mapped to a host/fake/Docker test. The supplied example deliberately blocks before
cluster access until these adapters and private fixtures exist. Preparing this
orchestration does not implement or pass those missing acceptance tests.

[RUNBOOK.md](RUNBOOK.md) gives the controller's exact build, render, install,
bootstrap, run, collect and teardown commands. A copy is written at the requested
`.superpowers/sdd/0.5.0/p6-lab-runbook.md` working-note path.

## Safety contract

- Every operation requires an explicit `--namespace acceptance-<unique-suffix>`.
  There is no default context and no use of the ordinary installer. It never
  loads `install/k8s/.env` or looks up production Secrets. A fresh namespace is
  created, not adopted; its random ownership label and UID are journaled before
  application. Resume verifies that UID and the deployment values.
- `--dry-run=server-free` means **render only**, with unusable Secret placeholders.
  It invokes no kubectl, Docker, cluster discovery or kubeconfig reading and
  creates no state directory. This spelling is a script option, not a kubectl
  option; kubectl itself has no `server-free` dry-run mode.
- The renderer derives Deployments, services, database initialization and Role
  directly from the production files. Core, board, runner and their own Postgres
  live in the single new namespace; session Jobs use that namespace too. Services
  are ClusterIP; ingress and cross-namespace ingress are absent. Egress is not
  restricted: actual Git/engine providers and the Kubernetes API must be reachable.
- Bootstrap uses the existing loopback-only Core setup mode behind the
  controller's localhost port-forward, with proxy trust off. The handler still
  checks TCP peer, Host and forwarding headers; no setup-link secret or
  pods/exec is needed. A platform that does not preserve loopback blocks setup.
- Required node affinity excludes the supplied hostname label values, including
  dynamic session/catalog Jobs through `BLERG_RUNNER_POD_EXCLUDE_NODES`. The
  shared Job submission guard ANDs exclusion into **every** existing required OR
  term and refuses direct `nodeName` binding. It does not mutate nodes or cluster
  admission. The campaign also checks actual assigned nodes and refuses excluded values absent from the cluster. This env setting is
  scheduling policy, never a release-gate override.
- Runtime Role rules are exactly production, with no pods/exec permission. The
  built-in fixture uses the production launcher SA, not an admin certificate.
  The operator running the scripts needs namespace/PV create/read/delete and
  read-only node/StorageClass inspection; effective `can-i --as` also requires
  impersonation authorization for the SA and its authenticated/serviceaccount groups. These operator powers are not granted to pods.
- CPU/memory requests/limits, namespace quotas and a LimitRange bound concurrency.
  StorageClass selection is explicit; Delete reclaim is required. Kubernetes PVC
  requested size is **not** proof of physical byte/inode enforcement. Never run
  exhaustion on an unverified class or fill a worker's node filesystem.
- Teardown requires the saved namespace UID/label, snapshots the exact bound
  claim/PV UIDs, uses UID-preconditioned foreground deletion, waits for namespace
  removal, then rechecks claim identities before deleting any surviving owned PV.
  CSI PV names need not have an acceptance prefix: ownership is proved by the
  acceptance namespace and exact claim/PV UIDs. It never deletes a PV merely by
  name/prefix, strips finalizers, force-deletes, deletes a StorageClass, or prunes
  cluster resources. A rebound/replaced PV or namespace refuses. Missing state
  never falls back to a guess. CSI backend deletion still needs confirmation.

## Case adapter interface

Keep credentials, real registry/node/provider coordinates and account data in
private files outside tracked source. `values.example.json` is an intentionally
unusable generic template. Add each `kind: driver` case from `cases.json` to
`case_drivers`, with an **absolute executable** and a timeout:

```json
{"case_drivers":{"mixed-providers":{"argv":["/private/reviewed-tests/mixed-providers"],"timeout_seconds":1800}}}
```

The adapter is test code the controller reviews before execution. The campaign
cannot sandbox arbitrary adapter code. It starts it without a shell, with closed
stdin, a private working directory/HOME and no inherited session, provider or
kubeconfig credentials. It passes only PATH/LANG and:

- `ACCEPTANCE_NAMESPACE`, `ACCEPTANCE_NAMESPACE_UID`, `ACCEPTANCE_CONTEXT`,
  `ACCEPTANCE_RUN_ID`, `ACCEPTANCE_CASE`, `ACCEPTANCE_CASES_FILE`;
- `ACCEPTANCE_FIXTURES_FILE`: mode-0600 input for **this install's** auth, disposable
  provider repositories and any reviewed namespace-scoped fault-injection client.
  Its `install` object must match namespace/UID/run ID and use only the three
  fixed localhost endpoints or exact same-namespace Service URLs; a live/foreign
  install URL refuses before any driver starts;
- `ACCEPTANCE_REPORT`: the receipt filename to create. An old receipt is removed
  before invocation. Raw stdout/stderr are discarded to prevent credential dumps.

An adapter must fail on an unmet assertion and write a bounded JSON receipt.
Zero exit status by itself never passes. All required `observations` in the case
registry must pass and cite nonempty, sanitized evidence files beneath the state
folder, each with its SHA-256 digest. Absolute paths, traversal, symlinks, missing
artifacts, stale install/run IDs and failed observations refuse. Example shape:

```json
{
  "case":"mixed-providers", "namespace_uid":"CURRENT_UID",
  "run_id":"CURRENT_RUN_ID", "status":"pass",
  "observations":{
    "two_providers":{"passed":true,"evidence":[{"path":"mixed-providers/observations.json","sha256":"ACTUAL_SHA256"}]}
  }
}
```

That abbreviated example is not a passing receipt: include **every** observation
for the case. Evidence must include actual image IDs, Job/Pod/claim/launch UIDs,
provider destination/ref assertions, timing/ordering and the appropriate negative
control. Do not include token contents, private file contents, raw environment,
private URLs, prompts or raw PTY captures. Provider refs should be disposable and
independently verified. Never push to a public or real working repository.

For the six `measure-N-PROFILE` cases, also include `measurement` with `members`,
`profile`, `image_id`, `commit`, `storage_class`, `csi_driver`, `node` and at least
three `samples` spanning cold and warm runs. Each sample has boolean `cold` and
nonnegative finite `prepare_ms`, `snapshot_ms`, `sync_ms`, `stop_ms`, `control_ms`,
`bytes`, `inodes`, `cpu_seconds`, `peak_rss_bytes`. The runner independently derives
nearest-rank p50/p95/p99 and includes the measurements in `results.json`. Agree on
latency/fairness thresholds before running; recorded measurements alone are not
an SLO pass. The required observation artifacts retain those threshold assertions.

Adapters must remain namespace-scoped, apply the required scheduling policy to
any fixtures they create, join their readers/processes, cap byte/inode generation,
and use exact resource UID cleanup. Physical node-loss/CSI faults need the
controller's storage-specific safe mechanism; namespace-level Pod disappearance
cannot substitute for proof that old physical writers stopped. Do not cordon,
drain, reboot, partition nodes or modify cluster-wide networking/admission as an
automatic part of this shared-lab campaign. If safe physical fencing cannot be
established, report the case failed/blocked and retain recovery data.

Results are mode-0600, updated after every case, and all 25 cases must pass for
campaign exit 0. Missing adapters block before deployment. `--deploy-only` returns
success for deployment only and records `deployed-not-accepted`.
`--diagnostics-only --resume` runs just placement/RBAC and the existing real-pod
opt-in; the other cases remain blocked and overall exit is nonzero. Collection
writes an allowlisted resource inventory and image IDs, never Secret payloads,
raw pod specs/env or raw logs. Failed campaigns retain the namespace until the
controller collects/reviews the evidence and explicitly tears it down.

The effective exec check uses `can-i create pods --subresource=exec` with the
SA's three authentication groups, following the [Kubernetes authorization
reference](https://kubernetes.io/docs/reference/access-authn-authz/authorization/)
and [can-i command reference](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_auth/kubectl_auth_can-i/).
`pods/exec` alone can denote a Pod named exec instead of the exec subresource;
it must not stand in for this check.
