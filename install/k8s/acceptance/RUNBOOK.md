# 0.5.0 isolated lab smoke — controller runbook

Prepared only: **the preparer has no lab access and has not run this smoke**.
The owner moved all failure/exhaustion/measurement cases to **#180 / 0.5.1**;
`cases.json` retains that deferred matrix. This run has one happy-path smoke.
Never use the live `blerg` namespace, the normal deploy script, production
credentials, or `pods/exec`. Cluster workspace and shell policy is ON following
the controller's real-pod happy-path pass; macOS host remains OFF (#181).
The disposable capacity overlay is still test-only and must never be promoted.

## 1. Check disk before building or copying images

Run on the builder and locally on each eligible worker, on the actual containerd
and storage backing filesystems:

```sh
df -h / "$PWD"
df -ih / "$PWD"
```

Keep at least 20 GiB free **after** the required images/archive/repository data;
stop if a filesystem cannot maintain that reserve. Use small public repositories
(ideally each checkout/history below 10 MiB), one session, no stress or exhaustion.
The namespace requests about 14.3 GiB of storage (workspace 11520 MiB + Postgres
2 GiB + runner data 1 GiB); these hostpath allocations are sparse and **not
physical quotas**. Its ResourceQuota caps requested storage at 20 GiB, not disk
use. Build-cache and the devcontainer image/archive may be large: reuse reviewed
base images, stream imports, and avoid keeping multiple archives. No Docker or
containerd prune; delete only artifacts created for this run, after teardown.

Requirements: Python 3.9+, distribution `python3-yaml`, Docker builder, controller's
explicit kubectl context and MicroK8s node-local import access. No registry push
is needed. Never enable/reconfigure addons or the storage default for this test.

## 2. Private parameters

From this worktree's repository root, use a fresh shell/state directory:

```sh
umask 077
export LAB_STATE="$(mktemp -d "${TMPDIR:-/tmp}/acceptance-smoke.XXXXXXXX")"
export NS="acceptance-p6-$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 4)"
export KUBE_CONTEXT='SET_EXPLICIT_LAB_CONTEXT'
export EXCLUDE_NODES='SET_CONTROL_NODE_HOSTNAME_LABEL'
export REGISTRY='localhost:32000'
export TAG="acceptance-p6-$(git rev-parse --short HEAD)-$(openssl rand -hex 4)"
export PUBLIC_REPOS='["https://SET_PUBLIC_HOST/OWNER/REPO_ONE.git","https://SET_PUBLIC_HOST/OWNER/REPO_TWO.git"]'
export PUSH_TARGETS='[]'
# Optional: exactly one unique disposable, anonymous-writable HTTPS target per
# member. Each must differ from EVERY source and every other target. Use [] if
# unavailable; no external push credential is required or accepted by this smoke.
# export PUSH_TARGETS='["https://SET_TARGET_HOST/OWNER/DISPOSABLE_ONE.git","https://SET_TARGET_HOST/OWNER/DISPOSABLE_TWO.git"]'
python3 - <<'PY'
import json, os
from pathlib import Path
v=json.loads(Path('install/k8s/acceptance/values.example.json').read_text())
v.update(registry=os.environ['REGISTRY'],tag=os.environ['TAG'],
         excluded_nodes=os.environ['EXCLUDE_NODES'].split(','),storage_class='default',
         public_repos=json.loads(os.environ['PUBLIC_REPOS']),push_targets=json.loads(os.environ['PUSH_TARGETS']))
p=Path(os.environ['LAB_STATE'])/'values.json'
p.write_text(json.dumps(v,indent=2)+'\n');p.chmod(0o600)
PY
```

The registry prefix is an **exact image name** for import, not a requirement to
contact/push to a registry. Substitute the lab's usual tagging prefix if needed.
Keep actual node names, URLs, addresses and results out of tracked files.
Two or three PUBLIC HTTPS sources, without userinfo/query/fragment, are required.
Confirm they anonymously clone and are small. A namespace-local bare mirror of
each source preserves its HEAD and receives the runtime's sync push. Optional
disposable targets are verified by a relay after that push, not by granting push
authority to public source hosts. [README.md](README.md) describes the boundaries.

Controller-only, explicit-context read checks:

```sh
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" get nodes \
  -o 'custom-columns=NAME:.metadata.name,HOSTNAME:.metadata.labels.kubernetes\.io/hostname'
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" get storageclass -o yaml
```

Set exclusions to the actual control-node hostname **label**, not a guessed node
name. At least one eligible worker must remain. Exactly one default StorageClass
with Delete reclaim is required. The script discovers and journals its UID/name/
provisioner and uses it for all claims. The lab's `microk8s-hostpath` and
`local-path` classes **do not enforce quotas**. Results say `quota_enforced:false`;
there are no byte/inode exhaustion cases. No claim of storage-limit acceptance.

## 3. Render without any cluster contact

```sh
install/k8s/acceptance/run.sh --namespace "$NS" \
  --values "$LAB_STATE/values.json" --dry-run=server-free > "$LAB_STATE/render.json"
python3 -m json.tool "$LAB_STATE/render.json" >/dev/null
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s install/k8s/acceptance -p 'test_*.py' -v
bash -n install/k8s/acceptance/run.sh install/k8s/acceptance/collect.sh \
  install/k8s/acceptance/teardown.sh
```

Inspect one namespace, own Postgres, no ingress, only ClusterIP Services, required
node exclusion, modest requests/limits and unchanged production Role rules. The
render contains unusable Secret placeholders and an unresolved ConfigMap default
class marker: **do not apply it**. The actual deploy discovers the default class.
This is our script's render-only mode, not a native kubectl dry-run option.

## 4. Build test images, then import on each eligible node

Use the same reviewed base images and build arguments as `install/k8s/deploy.sh`;
do **not** run that script (it deploys the live namespace). Reuse already built
base tags from this source commit when available. Otherwise:

```sh
export COMMIT="$(git rev-parse HEAD)"
export BUILD_LABEL="blerg.acceptance/run=$NS"
for component in core board; do
  docker build --label "$BUILD_LABEL" -f "$component/Dockerfile" \
    --build-arg VERSION="$TAG" --build-arg COMMIT="$COMMIT" \
    -t "$REGISTRY/blerg-$component:$TAG" .
done
docker build --label "$BUILD_LABEL" -f runner/Dockerfile.server \
  --build-arg VERSION="$TAG" --build-arg COMMIT="$COMMIT" \
  -t "$REGISTRY/blerg-runner:$TAG" .
docker build --label "$BUILD_LABEL" -f runner/Dockerfile.devcontainer \
  -t "$REGISTRY/blerg-runner-agent:$TAG" .
for pair in server:blerg-runner-enabled agent:blerg-runner-agent-enabled tests:blerg-acceptance-tests; do
  target=${pair%%:*}; image=${pair#*:}
  docker build --label "$BUILD_LABEL" -f install/k8s/acceptance/Dockerfile \
    --target "$target" --build-arg COMMIT="$COMMIT" --build-arg VERSION="$TAG" \
    --build-arg BASE_SERVER="$REGISTRY/blerg-runner:$TAG" \
    --build-arg BASE_AGENT="$REGISTRY/blerg-runner-agent:$TAG" \
    -t "$REGISTRY/$image:$TAG" .
done
docker pull postgres:16
```

All three overlay targets must share the **same build cache** so the ephemeral
fixture certificate/key match. Do not rebuild independently per node. The agent
image uses the existing test build tag, a scripted engine and a Go source overlay
which omits **only capacity admission** for non-enforcing default storage. Checkout
and production builds remain unchanged; do not promote these images. The tests
image has the private fixture key; neither image is a release artifact. Rebuild
and rerun imports within seven days (certificate lifetime).

Record image IDs and make ONE archive, checking disk again before saving:

```sh
for image in blerg-core blerg-board blerg-runner-enabled blerg-runner-agent-enabled blerg-acceptance-tests; do
  docker image inspect --format '{{.Id}}' "$REGISTRY/$image:$TAG"
done > "$LAB_STATE/image-ids.txt"
df -h / "$LAB_STATE"
docker save -o "$LAB_STATE/images.tar" \
  "$REGISTRY/blerg-core:$TAG" "$REGISTRY/blerg-board:$TAG" \
  "$REGISTRY/blerg-runner-enabled:$TAG" "$REGISTRY/blerg-runner-agent-enabled:$TAG" \
  "$REGISTRY/blerg-acceptance-tests:$TAG" postgres:16
sha256sum "$LAB_STATE/images.tar" > "$LAB_STATE/images.tar.sha256"
```

The controller transfers/streams this archive using its approved mechanism and
runs **locally on each eligible worker** (not the excluded control node):

```sh
# First compare the transferred archive's digest to the controller's saved digest.
sha256sum images.tar
microk8s ctr image import - < images.tar
microk8s ctr images ls
```

Check exact tags and digests on **both** eligible workers. Use per-node
`microk8s ctr image import`, not a cluster-wide import and not registry push.
Manifests use IfNotPresent. Delete only transferred archives created for this
run after import verification; retain source IDs/digests privately. Never remove
pre-existing containerd images, containers or caches. The preparer must not
perform transfers or any lab commands.

## 5. Run the smoke, collect, and inspect

```sh
install/k8s/acceptance/run.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --values "$LAB_STATE/values.json" --state "$LAB_STATE"
cat "$LAB_STATE/results.json"
install/k8s/acceptance/collect.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE"
```

This creates a fresh namespace (no adoption), journals its UID before apply,
waits for Postgres/Core/Board/Runner readiness, then runs one bounded smoke Job
under the unchanged production runner SA. Its server handlers use an isolated
test database in **this namespace's Postgres** and a private Service for real
pod outbound transport. Core liveness/policy and the engine are fixtures; no
manual account bootstrap, API token or model credential is needed. This does not
exercise the deployed Core UI/login or the deployed runner process. The test
checks actual pod hello, selection/launch, distinct cloned members, explorer,
shell, scripted commits, real fixture sync receipts and terminal cleanup.

`results.json` reports plain `pass` only after the complete assertion marker,
Go PASS (no skip), Job UID/ownership, actual scheduling, and effective
`pods/exec` denial before and after the smoke. A completed Job/zero exit alone
cannot pass. If exec is permitted, the controller positively probes the same
runner SA/groups for BOTH `get secrets` in `kube-system` and `create
clusterroles.rbac.authorization.k8s.io`. Neither permission is granted by the
unchanged production Role. These are only impersonated `auth can-i` questions:
the controller never reads those Secrets, creates a ClusterRole or performs exec.

If both probes permit the unrelated actions (the AlwaysAllow/RBAC-off case,
#182), the smoke continues in full. The `cases[0].checks.exec_denied` check has
`status: "pass-with-limitation"` and `message: "not-enforced: cluster authorization
is AlwaysAllow (RBAC off)"`. The same message appears in
`cases[0].evidence.exec_denied` and `limitations`. Successful completion then has
overall and case status **`pass-rbac-not-enforced`**, never plain `pass`, even if
a later authorization check denies exec. Both successful statuses exit zero;
controllers must inspect the status and retain the RBAC limitation. An exec
grant without both positive probes is a hard failure. Probe errors, missing
impersonation authority, malformed answers, and any smoke/assertion failure
remain failures; the limitation cannot turn them into success.

Results also record storage/deferred #180 cases and distinguish optional relay
pushes from fixture-only mode. Every caught exception has a stage, type and safe
message in `errors`, including setup and collection failures; a collection error
preserves the original smoke error. Internal refusal messages are retained.
Parser/system/subprocess messages redact private payloads, commands, paths and
URLs, retaining safe diagnostics such as JSON position or subprocess exit code.
If the state directory is invalid or results cannot be written, safe diagnostics
appear on stderr instead. Render-only failures never write state. Raw logs,
Secret contents and URLs are not persisted. No failure cases or measurements
run. Neither success status changes either release constant.

Failures remain for diagnosis and teardown. Do not retry a smoke in the same
namespace; preserve state, collect, teardown, choose a new namespace/tag.
`--deploy-only` is available for readiness inspection; its result is explicitly
`deployed-not-accepted`. Only that staged flow may use the identical command with
`--resume` to run the smoke later; executable files/values/default class UID and
namespace ownership must match. No automatic namespace or PV removal on failure.

The controller needs namespace/resource create/read/delete, node/StorageClass/PV
reads and exact owned PV deletion. Effective RBAC checks require SA/group
impersonation; inability to check is a failure, not a permission grant to pods.
The probes assume the runner has the unchanged production Role without added
cluster-wide grants; additional grants can mimic AlwaysAllow and require operator
investigation. RBAC enforcement is not accepted by a limited-success result.
For diagnosis without exec or Secret contents:

```sh
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" get pods,jobs,pvc
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" auth can-i create pods \
  --subresource=exec --as="system:serviceaccount:$NS:blerg-runner-agent-launcher" \
  --as-group=system:serviceaccounts --as-group="system:serviceaccounts:$NS" \
  --as-group=system:authenticated
# RBAC: must print no (exit 1). If yes, BOTH probes below must print yes
# (exit 0) for the AlwaysAllow limitation; otherwise the smoke must fail.
kubectl --context "$KUBE_CONTEXT" --namespace kube-system auth can-i get secrets \
  --as="system:serviceaccount:$NS:blerg-runner-agent-launcher" \
  --as-group=system:serviceaccounts --as-group="system:serviceaccounts:$NS" \
  --as-group=system:authenticated
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" auth can-i create \
  clusterroles.rbac.authorization.k8s.io \
  --as="system:serviceaccount:$NS:blerg-runner-agent-launcher" \
  --as-group=system:serviceaccounts --as-group="system:serviceaccounts:$NS" \
  --as-group=system:authenticated
```

## 6. UID-checked teardown, including failure/partial deploy

Preserve `namespace.json` and results first. Dry-run the deletion plan:

```sh
install/k8s/acceptance/teardown.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE" --dry-run=server-free
install/k8s/acceptance/teardown.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE"
cat "$LAB_STATE/teardown.json"
```

The script refuses names outside `acceptance-`, mismatched/recreated namespaces
or rebound PVs. It journals bound PVC/PV UIDs, foreground-deletes the exact
namespace with a UID precondition, waits, then deletes only surviving PVs whose
UID and claim namespace/UID still match. It never strips finalizers, force-deletes,
prunes, deletes storage classes or guesses ownership. Retained volumes may have
CSI-generated names; only proven claim/UID ownership authorizes deletion. Retry
teardown with the same journal if asynchronous deletion takes longer. Confirm
backend reclamation and disk reserve; delete only this run's local archives and
unneeded tagged images by recorded IDs after all tests and teardown complete.
Retain private results, image IDs, inventories and teardown receipt for review.

## Preparation validation (not lab evidence)

The following is the original preparation record, before the controller's
subsequent passing smoke and cluster release activation. Its gate state is historical.

Locally checked on 2026-10-24, without any lab/cluster contact:

- 21 offline Python tests pass, including rendered isolation/RBAC, UID teardown,
  default-class/no-quota reporting, one active smoke, receipt rejection and real
  local Git commits in all three scripted members. Twelve Python mutations and
  one Go overlay removing URL guards fail their regressions as required.
- Focused default-build server suite: five tests pass (P1/P3 consumer,
  scheduling guards, generated Job exclusion, target validation). Real-pod writer
  opt-in skips. Tagged race run: smoke parameter test passes; actual pod smoke
  skips explicitly. The default protocol fail-closed gate test also passes. No
  real-pod pass is claimed.
- Tagged pod build with the exact capacity overlay, tagged server build and
  CGO-disabled tagged server test binary compile. No Docker images were built,
  imported or run here. Both gates remain literal false.
- Pinned Go lint reports zero issues. Shell wrappers and all runbook shell blocks
  parse; render-only produces 21 objects; whitespace and privacy scrub pass.
  Ordinary installer regression passes its available checks; kubectl-dependent
  checks skip because kubectl is absent. kind/k3d are also absent, so cluster
  schema/admission validation remains for the controller.
- An initial tagged test selection included an existing gate-off assertion and
  failed it, as expected for a test-enabled build. That consumer test was rerun
  with the default build and passed; it was not weakened or changed.

The private preparation logs are retained in the worktree's ignored
`.p6/lab-smoke/evidence/`. Build caches and binaries created for these checks are
removed. Runtime smoke, optional disposable forwarding and backend reclamation
remain **unrun**, for controller review and execution only.
