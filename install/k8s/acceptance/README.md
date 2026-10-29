# Isolated 0.5.0 cluster smoke

Prepared for controller review/run only; **no lab contact or execution**.
[RUNBOOK.md](RUNBOOK.md) contains exact commands. The owner deferred the full
failure/measurement campaign to **#180 / 0.5.1**. Every entry in `cases.json`
is retained and marked deferred. None is executed or counted as passing here.

The one active smoke takes 2–3 credential-free public HTTPS repository URLs.
It anonymously clones them into namespace-local bare mirrors, preserving their
HEADs. Through the production P1 selection consumer and P3 Job manager it launches
one multi-repo pod, checks distinct member paths and initial HEADs, queries
protected explorer list/search/status over the outbound socket, opens/closes a
P5 shell, sends a scripted turn, and verifies each committed marker arrives on
`wip/<session>` in its bare mirror. Stop must complete real Job/Pod/Secret removal
and durable token revocation. The controller checks effective runner SA denial
of `pods/exec` and required/actual node exclusion before and after the run.
If exec is permitted, impersonated `auth can-i` probes for unrelated Secret reads
in `kube-system` and ClusterRole creation must both positively identify the
AlwaysAllow/RBAC-off limitation. The probes never perform those actions. The
full smoke continues, recording `exec_denied` as `pass-with-limitation` with
`not-enforced: cluster authorization is AlwaysAllow (RBAC off)`. Its successful
overall status is `pass-rbac-not-enforced` (exit zero), never plain `pass`.
An exec grant without both positive probes, a probe error, or any smoke failure
still fails. See [runbook section 5](RUNBOOK.md#5-run-the-smoke-collect-and-inspect)
for result fields and operator checks. Additional cluster-wide grants to the
runner SA can mimic AlwaysAllow; the probes assume unchanged production RBAC.

No provider/model credential is needed: Core identity/liveness/shell policy and
the engine are deterministic fixtures inside the smoke test process. That
process uses the production server handlers with its own isolated test database
on this installation's Postgres, exposed only through a namespace-local Service.
It does **not** exercise the deployed Core login/UI or the deployed runner
process. Its runner code is compiled from the same source. This is a happy-path
runtime smoke, not full production negotiation/provider acceptance.

Public read permission never becomes public push authority. The custom provider
exists only in the tagged test process; its ephemeral token authorizes only the
namespace-local mirror host. Optional disposable HTTPS push targets, one per
member, receive the exact same branch through an anonymous fixture relay; their
remote SHAs must match. Targets must differ from every public source and one
another. They must accept credential-free pushes; use the fixture-only mode
otherwise. No source repository receives a push. Results distinguish both modes.

The observed **default** StorageClass is recorded by name, UID and provisioner;
it must have Delete reclaim. The lab's `microk8s-hostpath` and `local-path` classes
**do not enforce byte/inode quotas**. PVC sizes/ResourceQuota are allocation
requests, not physical limits. No exhaustion runs. Production correctly rejects
such volumes. Only the disposable pod image uses a narrowly checked Go build
overlay omitting `NewDiskGit`'s capacity admission, retaining its other checks.
The overlay never edits checkout source; it must match exactly one constructor.
It cannot establish capacity safety. Cluster workspace and shell policy is now
ON in ordinary builds after the controller's real-pod happy-path pass; macOS host
remains OFF (#181). The existing `blerg_workspace_integration` build tag retains
the opt-in harness, rather than providing production enablement.
Do not promote these images. The private TLS fixture key ships only in the tests
image, and its public CA only in the disposable images. Rebuild after 7 days.

Safety retained:

- Explicit context and fresh `acceptance-` namespace; no adoption or live Secret
  lookup. Resume checks saved namespace UID/label, default StorageClass identity,
  values and executable campaign digest. Failed resources remain for inspection.
- `--dry-run=server-free` renders only: no tools, kubeconfig, cluster discovery,
  state writes or usable credentials. This is a script flag, not a kubectl flag.
- Own Postgres, ClusterIP Services, no ingress/public exposure. Required affinity
  excludes every supplied hostname label, including generated session/catalog
  Jobs. No node/admission mutation. Runner RBAC rules equal production.
- Modest CPU/memory, one session, 20 GiB requested storage quota, five claims.
  The actual small repositories and disk reserve must be checked by the operator;
  hostpath and ephemeral-storage size limits are not disk exhaustion protection.
- UID-preconditioned teardown deletes only the owned acceptance namespace and
  exact bound PV UIDs after checking their claim namespace/UID. Never strip
  finalizers, force delete, prune, delete storage classes or guess PV ownership.
- Results contain assertion receipts, image IDs, allowlisted inventory and safe
  exception diagnostics (stage, type and message); no raw test output, Secret
  data, credentials or URLs. Collection failures retain earlier smoke errors.
  Untrusted exception messages are redacted. Keep controller state private.

Offline validation: `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s
install/k8s/acceptance -p 'test_*.py' -v`. Local kind/k3d validation is optional
only when a disposable local cluster is available; never substitute the lab.

### Launch diagnostics

The smoke polls both successful and failed Job outcomes, then collects a bounded,
allowlisted launch receipt into `cases[].evidence.launch_stages` in `results.json`.
Each stage includes its start/end timestamps, outcome and a fixed reason code.
Raw logs, credentials, repository URLs and Kubernetes rejection bodies are excluded.
The smoke checks persisted launch failures immediately and fails a stage that makes
no progress for 60 seconds, naming that stage. This is a smoke diagnostic deadline;
it does not change production scheduling or storage timeouts.

Session `start_stage` events and server logs cover selection, manifest persistence,
plugin resolution, authorization, repository access, retained PVC allocation, old
writer fencing, runtime generation/token binding, engine credentials, pod validation,
Secret creation and Job submission. Pod status covers scheduling, image pull and
protected storage init containers; hello/welcome, clone, contained readers and engine
startup complete the path. Pre-hello protected-storage/bootstrap refusals use a fixed
Kubernetes termination message. No `pods/exec` permission is required.

A retained claim may be Pending with `WaitForFirstConsumer` when its Job is submitted.
The launcher validates claim identity and ownership before submission, without waiting
for binding. The consumer pod lets the scheduler select the volume topology. No live
cluster smoke has been run by this change; rebuild all disposable images and rerun
in the controller's owned namespace to establish the actual pod/storage lifecycle.

The smoke waits for a live session and its persisted `ready` stage after workspace
preparation before opening the owner shell. Workspace receipts alone precede engine
startup. Shell assertions have a separate `shell_open` stage; refusals retain only
fixed codes such as `session_not_ready`, `policy_unavailable`, `policy_disabled`,
`binding_refused`, `authority_refused` or `runtime_refused` in the receipt. HTTP
shell denials retain the generic message and add a safe `code` field. Raw response
bodies, policy credentials and runtime error text are excluded.
