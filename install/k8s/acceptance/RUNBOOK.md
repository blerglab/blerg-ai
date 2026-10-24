# P6 lab pod acceptance — controller runbook

Prepared only. **No lab contact or campaign execution was performed.** Use a
separate throwaway Blerg installation on the approved shared three-node lab;
exclude its control node using required affinity. All application, database and
session resources belong to one fresh `acceptance-` namespace. Never use or
modify the live `blerg` namespace or run the ordinary `deploy.sh`.

Both workspace/shell release constants stay false. Only test-built enabled images
use the existing single build tag; there is no production environment override.
Do not promote these images. Review the committed source and image provenance
before executing any commands below. The [coverage gap and adapter contract](README.md)
are binding: **23 lab-specific case drivers remain to be supplied/reviewed**.
The repository supplies two built-in checks, not the full acceptance matrix.
No campaign success can be reported until every required case has real evidence.

## 1. Disk and prerequisites first

On the builder, **before building/saving/importing images**:

```sh
df -h / "$PWD"
df -ih / "$PWD"
```

On each eligible storage/worker node, locally inspect the actual CSI backing
mount and the containerd/registry filesystem with `df -h` and `df -ih` as well.
Do not infer available space or inode enforcement from a PVC request or node
allocatable capacity. Budget at least 30 GiB free on the builder and 80 GiB plus
ample free inodes on the relevant storage filesystems before the bounded
exhaustion campaign; increase these for the chosen driver/images/retained cases.
Stop if there is insufficient reserve. Exhaustion must hit the **verified
per-volume** ceiling, never the worker/root filesystem. Hostpath requested size
alone is insufficient. If this lab lacks real enforced byte/inode limits, keep
those cases blocked and both release gates false.

Required locally: Python 3.9+ and distribution `python3-yaml`, Docker for builds,
kubectl configured by the controller, and an approved registry reachable by all
eligible nodes (or an explicitly verified node-local image import path). No
cluster tools are needed for offline render/tests. There is no need for SSH,
pods/exec, node mutations, cluster admission changes or cluster-wide cleanup.
The preparer must not run this campaign; only the reviewing controller does.

## 2. Private controller parameters and complete case plan

Use the intended worktree/commit. Do not pull or switch a running checkout.
From repository root, create a private state directory outside tracked source:

```sh
umask 077
export LAB_STATE="$(mktemp -d "${TMPDIR:-/tmp}/acceptance-p6.XXXXXXXX")"
export NS="acceptance-p6-$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 4)"
export KUBE_CONTEXT='SET_EXPLICIT_LAB_CONTEXT'
export REGISTRY='SET_APPROVED_REGISTRY'
export TAG="acceptance-p6-$(git rev-parse --short HEAD)-$(openssl rand -hex 4)"
export EXCLUDE_NODES='SET_CONTROL_NODE_HOSTNAME_LABEL'
export WORKSPACE_STORAGE_CLASS='SET_VERIFIED_QUOTA_CSI_CLASS'
export DATA_STORAGE_CLASS='SET_DISPOSABLE_DELETE_RECLAIM_CLASS'
```

Replace those values in the private shell; never commit actual names/addresses,
credentials, kubeconfigs, provider repositories or results. Check the explicit
context and nodes yourself (read-only commands; never change the current context):

```sh
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" config get-contexts "$KUBE_CONTEXT"
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" get nodes \
  -o 'custom-columns=NAME:.metadata.name,HOSTNAME:.metadata.labels.kubernetes\.io/hostname,READY:.status.conditions[-1].status'
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" get storageclass \
  "$WORKSPACE_STORAGE_CLASS" "$DATA_STORAGE_CLASS"
```

Keep the context/node output private. Ensure the excluded
list is the Kubernetes **hostname label**, not an assumed local machine name;
use comma-separated values for multiple exclusions. Required affinity excludes
those values and requires the label to exist. Inspect actual Ready conditions
rather than relying only on the abbreviated table. Only eligible workers may
run application, Postgres, session, catalog and fixture pods. Node loss/partition
experiments must be scoped safely to this install; never reboot/drain/cordon a
shared node or change shared storage/networking as part of this script.

Create values without sourcing the production install's `.env`:

```sh
python3 - <<'PY'
import json, os
from pathlib import Path
v=json.loads(Path('install/k8s/acceptance/values.example.json').read_text())
v.update(registry=os.environ['REGISTRY'], tag=os.environ['TAG'],
         excluded_nodes=os.environ['EXCLUDE_NODES'].split(','),
         workspace_storage_class=os.environ['WORKSPACE_STORAGE_CLASS'],
         data_storage_class=os.environ['DATA_STORAGE_CLASS'],
         fixtures_file=str(Path(os.environ['LAB_STATE'])/'fixtures.json'))
p=Path(os.environ['LAB_STATE'])/'values.json'
p.write_text(json.dumps(v,indent=2)+'\n');p.chmod(0o600)
PY
```

Before full execution, install/review every `kind: driver` adapter listed in
`cases.json` and populate `case_drivers` with its absolute executable argv and
bounded timeout. Review credentials/fault scope, physical proof, cleanup and
negative controls. See README for receipt/evidence/measurement schemas. These
adapters are missing implementation work, not optional manual checkboxes.
An adapter must not use an inherited live Blerg token or operate outside this
namespace. Supply only disposable provider repositories, temporary test accounts
and fixture-specific credentials. Use at least two providers with private repos,
a public anonymous/no-push member, and duplicate leaf names; verify permissions
before allowing any publication. Never use real working repositories.

## 3. Server-free dry run, then local checks

```sh
install/k8s/acceptance/run.sh --namespace "$NS" \
  --values "$LAB_STATE/values.json" --dry-run=server-free \
  > "$LAB_STATE/render.json"
python3 -m json.tool "$LAB_STATE/render.json" >/dev/null
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s install/k8s/acceptance -p 'test_*.py' -v
bash -n install/k8s/acceptance/run.sh install/k8s/acceptance/collect.sh \
  install/k8s/acceptance/teardown.sh
```

This does not contact any API, including with client-side kubectl. The rendered
Secret placeholders are unusable. Inspect: only one acceptance namespace, no
Ingress/public Services, own database/PVCs, explicit test image tags, required
exclusions, modest resources, same Role rules as production, and correct
same-namespace ServiceAccount references. Do not apply the dry-run artifact.
There is no `kubectl --dry-run=server-free`; the script implements render-only.
Optional schema validation is against a controller-created **disposable local**
kind/k3d cluster only. Do not substitute a real cluster for local validation.

## 4. Build and push tagged test images

Do not run the normal deploy script: it targets the production namespaces and
would read/reuse their Secrets. The following reproduces its build/push flow
without deployment, then builds explicit test-only binary overlays:

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
for image in blerg-core blerg-board blerg-runner blerg-runner-agent \
             blerg-runner-enabled blerg-runner-agent-enabled blerg-acceptance-tests; do
  docker image inspect --format '{{.Id}}' "$REGISTRY/$image:$TAG"
  docker push "$REGISTRY/$image:$TAG"
done
```

Save the source commit, tag, local image IDs and registry digests privately.
The manifest deploys enabled runner/agent and ordinary core/board, not release
gate mutations. The test image entrypoint selects the currently existing Kubernetes
opt-in from the compiled server tests and uses the namespace's production launcher SA token. Test artifacts
use TAG references; collect resolved runtime image IDs for evidence.

MicroK8s registry handling follows `install/k8s/README.md`: its built-in registry
convention is meaningful from cluster nodes, and all eligible nodes must actually
be able to pull the same tags. Use the lab's approved endpoint/trust config;
**do not enable/reconfigure a registry addon** in this shared cluster as part of
the campaign. If using import instead of push, check disk before saving, then:

```sh
docker save -o "$LAB_STATE/images.tar" \
  "$REGISTRY/blerg-core:$TAG" "$REGISTRY/blerg-board:$TAG" \
  "$REGISTRY/blerg-runner-enabled:$TAG" "$REGISTRY/blerg-runner-agent-enabled:$TAG" \
  "$REGISTRY/blerg-acceptance-tests:$TAG" postgres:16
# Run locally on EACH eligible node, using its approved transfer mechanism:
# microk8s ctr image import - < images.tar
# microk8s ctr images ls
```

The per-node import spelling follows [Canonical's image side-loading guide](https://canonical.com/microk8s/docs/sideload).
It avoids the cluster-wide import command, so the excluded node need not be loaded.
Pull the explicitly tagged Postgres image first if it is not already local.
For an air-gapped import, include all required base/runtime images, verify exact
reference names on every eligible node and that IfNotPresent resolves them
without a registry pull. Delete only the archive you created after verified
transfer. Do not remove, stop or prune any pre-existing images/containers/cache.

## 5. Deploy the isolated install and bootstrap only it

The state directory must remain 0700. The run creates a fresh namespace, refuses
adoption, generates new install secrets in memory, journals its UID and applies
only that namespace's resources. It checks explicit Delete-reclaim classes and
waits for Postgres plus core/board/runner readiness:

```sh
install/k8s/acceptance/run.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --values "$LAB_STATE/values.json" --state "$LAB_STATE" --deploy-only
```

This result is **deployed-not-accepted**, never an acceptance pass. A failure
keeps the ownership journal for collection/teardown; do not delete it. Do not
repeat a fresh deployment using an existing namespace. Use `--resume` only with
matching journal UID and immutable deployment values. Driver entries can be
completed after deploy without changing the images/placement/storage.

In separate controller-owned terminals, bind only to loopback:

```sh
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" port-forward \
  --address localhost service/blerg-core 18081:8080
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" port-forward \
  --address localhost service/blerg-board 18082:8080
kubectl --context "$KUBE_CONTEXT" --namespace "$NS" port-forward \
  --address localhost service/blerg-runner 18083:8080
```

Open the controller-forwarded core at `http://localhost:18081/setup` and create
a disposable owner. This overlay sets the existing `BLERG_SETUP_ALLOW_LOCAL=true`
and `BLERG_SETUP_PUBLISHED_ADDR=localhost` only in its own Core deployment.
Core still requires a genuine loopback TCP peer, localhost Host and no proxy
forwarding headers; `BLERG_CORE_BEHIND_PROXY=false`. Other pods reaching the
ClusterIP cannot satisfy that peer check. Confirm the setup page permits this
local path; if the port-forward does not preserve loopback, stop bootstrap rather
than weakening checks or using pods/exec. No setup-link credential is retrieved,
printed or placed in a Job log. Attach only the test provider
and engine credentials needed by the reviewed cases, allow shell policy for this
test owner, and create this install's test API credentials. Record them and the
fixture repository/ref matrix in `$LAB_STATE/fixtures.json` (0600). Start with:

```sh
python3 - <<'PY'
import json,os
from pathlib import Path
root=Path(os.environ['LAB_STATE']);s=json.loads((root/'namespace.json').read_text())
f={'install':{k:s[k] for k in ('namespace','namespace_uid','run_id')}}
f['install']['urls']={'core':'http://localhost:18081','board':'http://localhost:18082','runner':'http://localhost:18083'}
f.update(accounts={},repositories=[],credentials={})
p=root/'fixtures.json';p.write_text(json.dumps(f,indent=2)+'\n');p.chmod(0o600)
PY
```

Fill only this install's disposable inputs according to the reviewed adapters.
The runner checks fixture install identity and endpoint scope before any driver.
Do not dump Kubernetes Secrets or reuse a production account's runner token. These are
lab-specific test inputs; the preparer cannot invent real provider identities.
The UI origins/audiences are the three localhost ports above; do not use public
exposure or a live install to bootstrap. Keep those port-forward process IDs and
stop only those processes when finished.

## 6. Run and collect

Until all 23 drivers are implemented/reviewed, only diagnostics are runnable:

```sh
install/k8s/acceptance/run.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --values "$LAB_STATE/values.json" --state "$LAB_STATE" --resume --diagnostics-only
```

Expected overall exit is nonzero, because the missing acceptance cases stay
blocked even when placement/RBAC and the real writer-fence opt-in pass. Do not
translate that diagnostic result into a release gate. To run the complete matrix
once all drivers and private fixtures are ready:

```sh
install/k8s/acceptance/run.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --values "$LAB_STATE/values.json" --state "$LAB_STATE" --resume
install/k8s/acceptance/collect.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE"
```

Without drivers, full run refuses **before cluster access** and writes the full
case registry with missing cases blocked. With drivers, it runs each case,
updates a private results file after each one, retains every failure, verifies
receipt/artifact identity/hashes, checks placement after cases and requires all
25 cases to pass. Timeout terminates only the executable process group it
started, never a cluster node or foreign process. A failed driver must leave
uncertain data/resources retained and report where they are.

Review `results.json`, previous `results-*.json` attempts, each case receipt and
sanitized hashed artifacts, `inventory.json` and `namespace.json`. Check evidence
for all categories in `cases.json`, including exact provider credentials/public
no-push, actual CSI ENOSPC/inodes/metadata, rollback/retained resume, physical
fencing, cleanup token revocation, containment/readers, recovery, frozen leases,
outbound transport and six repeated 1/5/20-member target measurements. No host
benchmark may satisfy CSI/provider evidence. Agree fairness/SLO thresholds in
advance. Collection deliberately omits raw pod logs/specs/Secret data; a driver
must emit purpose-built sanitized observations. Keep this bundle private and
scrub before publishing. The runbook's local validation is not lab acceptance.

## 7. Teardown — only this namespace and its PVs

Collect/review recovery data before approving its disposal. Keep the state file:

```sh
install/k8s/acceptance/teardown.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE" --dry-run=server-free
install/k8s/acceptance/teardown.sh --namespace "$NS" --context "$KUBE_CONTEXT" \
  --state "$LAB_STATE"
```

The dry run reads only the local journal and prints the planned identity checks;
it does not query PVs. The raw DELETE/file-body transport follows [kubectl's implementation](https://github.com/kubernetes/kubectl/blob/master/pkg/cmd/delete/delete.go).
Actual teardown snapshots bound claim/PV UIDs before
foreground namespace deletion and rechecks namespace absence plus PV identity
before deleting any surviving associated PV with UID preconditions. Namespace
and PV API-object deletion is not proof of backend capacity reclamation: verify
chosen CSI deletion completes and physical storage is reclaimed. A finalizer or
identity mismatch must stop cleanup; never force it. Never substitute a broad
`kubectl delete all`, label deletion, finalizer patch, cluster prune or a guessed
PV name. Re-running teardown uses the saved exact-UID PV journal.

Retain `teardown.json` and the scrubbed evidence bundle. Stop only the controller's
own port-forwards. Delete your private transfer archive and local build caches
through their exact owned paths; remove imported test image refs only after
verifying their recorded IDs/tags are no longer used. Registry cleanup follows
its owner-approved retention policy; do not garbage-collect shared manifests.
Never rotate/delete live install secrets or touch the live namespace.

## Local preparation evidence

The preparer found Docker but no kind, k3d or kubectl executable, so used static
render checks and mocked API/UID safety regressions instead of creating a local
cluster. No real-cluster command, SSH, image build/push/import or pod campaign was
run. Offline tests and focused scheduling/Job tests, production deploy-script
checks, lint, scrub and negative controls are recorded in the worktree's ignored
`.p6/lab-prep/evidence/`. Release gates and lab case results remain unverified.
