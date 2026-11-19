# Disposable restart drills (BLG-433)

The Go controller installs a private Core, Board, Runner, PostgreSQL and scripted
workstation daemon, runs the nine existing `runner/cmd/session-suite` regressions,
then injects mid-turn daemon restart, server restart and upgrade faults. It uses
production service images and the production daemon binary; the only engine is a
Go Claude-wire fixture. No real model, repository, user credential or session is
used. The controller has been tested offline; **the first live run is owner-run**.
A live failure is a failed drill, never a local-test pass.

## Prepare images (operator)

Provide previous and candidate production Core, Board and Runner images, built
using their existing Dockerfiles. The previous version must support personal
desktop credentials, the runner contract and `no_repo` scratch sessions. This is
a forward upgrade check, not an arbitrary historical-version compatibility test.

The current source builds the fixture controller/engine and regression tests.
The daemon source is independently selected using Docker's named build context,
so the previous daemon is really compiled from the previous source. From the
current repository root, with `PREVIOUS_SOURCE` pointing at a clean, complete
previous repository snapshot, `PREVIOUS_SHA` naming that source revision, and
the image names below already exported:

```sh
docker buildx build --load \
  -f install/k8s/acceptance/RestartDrills.Dockerfile \
  --build-context daemon-source="$PREVIOUS_SOURCE" \
  --build-arg DAEMON_COMMIT="$PREVIOUS_SHA" \
  -t "$BLERG_DRILL_PREVIOUS_DAEMON_IMAGE" .
docker buildx build --load \
  -f install/k8s/acceptance/RestartDrills.Dockerfile \
  --build-context daemon-source=. \
  --build-arg DAEMON_COMMIT="$(git rev-parse HEAD)" \
  -t "$BLERG_DRILL_IMAGE" .
export BLERG_DRILL_DAEMON_IMAGE="$BLERG_DRILL_IMAGE"
```

Use clean source snapshots, without secrets. The fixture Dockerfile excludes
Git metadata, environment files, dependencies and logs from its default context.
The named daemon source must also be clean. Make all images available to the
cluster using your usual operator-controlled import/registry process. The tool
never builds, pushes or promotes images. These large test images include Go, Git,
C and tmux; they are disposable fixture images, not release artifacts. Build once
and retain their immutable digests with the source revisions.

## Environment

Set every variable below explicitly. All names and endpoints come from environment;
there is no default cluster context, namespace, ingress name or external database.
The fixture images must come from the Dockerfile above. Prefer immutable image
references; the campaign also records actual Kubernetes image IDs.

From the current repository root (replace the image references and `LAB_DOMAIN`
with your disposable lab values):

```sh
export BLERG_DRILL_CONTEXT='YOUR_DISPOSABLE_CONTEXT'
export BLERG_DRILL_NAMESPACE="acceptance-restart-$(date +%Y%m%d%H%M%S)"
export LAB_DOMAIN='example.invalid'
export BLERG_DRILL_CORE_HOST="$BLERG_DRILL_NAMESPACE.$LAB_DOMAIN"
export BLERG_DRILL_BOARD_HOST="$BLERG_DRILL_NAMESPACE-board.$LAB_DOMAIN"
export BLERG_DRILL_RUNNER_HOST="$BLERG_DRILL_NAMESPACE-runner.$LAB_DOMAIN"
export BLERG_DRILL_INGRESS_CLASS='YOUR_INGRESS_CLASS'
export BLERG_DRILL_STORAGE_CLASS='YOUR_DISPOSABLE_STORAGE_CLASS'
export BLERG_DRILL_POSTGRES_IMAGE='postgres:16'
export BLERG_DRILL_PREVIOUS_CORE_IMAGE='registry.example.invalid/blerg-core:previous'
export BLERG_DRILL_PREVIOUS_BOARD_IMAGE='registry.example.invalid/blerg-board:previous'
export BLERG_DRILL_PREVIOUS_RUNNER_IMAGE='registry.example.invalid/blerg-runner:previous'
export BLERG_DRILL_PREVIOUS_DAEMON_IMAGE='registry.example.invalid/restart-fixture:previous'
export BLERG_DRILL_CORE_IMAGE='registry.example.invalid/blerg-core:candidate'
export BLERG_DRILL_BOARD_IMAGE='registry.example.invalid/blerg-board:candidate'
export BLERG_DRILL_RUNNER_IMAGE='registry.example.invalid/blerg-runner:candidate'
export BLERG_DRILL_IMAGE='registry.example.invalid/restart-fixture:candidate'
export BLERG_DRILL_DAEMON_IMAGE="$BLERG_DRILL_IMAGE"
export BLERG_DRILL_TEMPLATES="$PWD/install/k8s"
export BLERG_DRILL_STATE="${TMPDIR:-/tmp}/$BLERG_DRILL_NAMESPACE-evidence"
```

`BLERG_DRILL_STATE` must not exist; its parent must exist. It is a private evidence
and ownership journal directory outside the checkout. Keep the same environment
for `up` and `run`; configuration changes are refused. `down` only needs the five
base settings (context, namespace, fixture image, PostgreSQL image and state path).
Previous and candidate Runner/daemon image references must differ. The image IDs
must actually change during upgrade, or that case fails.

Choose three unused DNS names whose first label starts with the namespace; point
them at the lab ingress controller. An HTTP-only Ingress named `restart-drills` is
created in the new namespace, with routes to the three private Services. No TLS
issuer, DNS zone, load balancer or ingress controller is installed or changed.
Automatic HTTP-to-HTTPS redirects must be disabled for these disposable routes;
no real credentials are used. Drills drive the private Services, not the public
Ingress, so they do not establish browser/TLS/ingress reachability acceptance.

The explicit StorageClass must use `Delete` reclaim and support filesystem group
ownership for the Runner and daemon claims. No default-class discovery, hostPath,
capacity-admission bypass, storage-class mutation or PV deletion is performed.
Byte/inode quota enforcement is needed for capacity-safety testing; these small
restart drills perform no exhaustion testing and establish no capacity guarantee.

The cluster must support StatefulSets, dynamic ReadWriteOnce claims, `fsGroup`,
NetworkPolicy egress isolation and pod-local shared process namespaces. Only the
fixture pod needs `SYS_PTRACE` and an unconfined seccomp profile for the existing
host regression/daemon containment proofs; it has no service-account token,
host mount, host namespace or privileged flag. Admission must permit this in the
lab namespace. Egress permits only same-namespace pods and DNS; model endpoints
and other installations are unreachable when the CNI enforces NetworkPolicy.
The scripted image contains no real model implementation regardless.

The invoking kubeconfig identity needs read access to StorageClasses and all
Ingress rules (collision check), namespace create/get/delete/watch, and resource
create/get/watch/patch plus pod exec in its disposable namespace. It does not need
cluster-role creation, PV deletion, node mutation or access to another namespace's
Secrets. Ingress collision checking includes wildcard rules and fails on read
errors. Namespace-prefixed names avoid sharing routes; cluster-wide admission
should enforce route uniqueness if concurrent operators can claim the same host.

## Exact commands

With the environment set above and Go 1.26.9+ and kubectl on PATH:

```sh
cd runner
GOWORK=off go run ./cmd/restart-drills plan
GOWORK=off go run ./cmd/restart-drills all
```

`plan` only reads local templates and validates configuration: no kubeconfig,
cluster contact, tools, state writes or usable credentials. `all` is the same
on-demand and pre-release command: `up`, `run`, then `down`. Exit zero requires
the regression prerequisite, all three live cases, and completed namespace
teardown. A failure retains the namespace and private evidence for inspection;
there is no automatic destructive cleanup after failure. Do not rerun a partial
campaign in place: it reserves its evidence before running and refuses reuse.

For deliberate inspection between phases, use these instead of `all`:

```sh
GOWORK=off go run ./cmd/restart-drills up
GOWORK=off go run ./cmd/restart-drills run
# Inspect private results, then explicitly remove the installation:
GOWORK=off go run ./cmd/restart-drills down
```

Teardown first verifies both the namespace ownership label and recorded UID. A
raw Kubernetes Namespace deletion carries a server-enforced UID precondition,
closing the check/delete race. It waits for deletion before writing
`teardown.json`. It never adopts an existing namespace, strips finalizers,
force-deletes, prunes resources or guesses PV ownership. Dynamic storage cleanup
belongs to the explicit Delete-reclaim class. If namespace creation succeeds but
its response/journal write is lost, the controller refuses recovery by guessing;
operator recovery is required.

## Case list and proof

All three cases start a fresh owned `no_repo` scratch session and a scripted turn.
The engine creates a private Git baseline and an **uncommitted** marker, takes a
kernel writer lock, and signals a durable HTTP barrier. The controller captures
the session's event prefix and verifies its durable active-turn daemon record before injecting a fault. Polling waits on observed
state with deadlines, never an elapsed sleep assumed to mean mid-turn.

| Case | Injected fault | Additional recovery proof |
| --- | --- | --- |
| `daemon` | SIGKILL the exact recorded production daemon process, after checking its executable in the pod-local process namespace; supervisor restarts it | New daemon boot identity; automatic recovery of the same native fixture conversation; the engine must find the original path, Git baseline and uncommitted marker |
| `server` | Change the owned Runner Deployment template; Recreate rollout replaces the serving server | New server pod UID; unchanged daemon boot and engine identity; the original blocked turn completes after reconnect |
| `upgrade` | Replace Core, Board, Runner and daemon images with the explicit candidates; wait for migrations/readiness | New server pod and daemon boot; changed immutable Runner/daemon image IDs; same automatically recovered session/native fixture identity, workspace and event prefix |

Every case requires the identical saved event prefix, exactly one successful
fixture reply/turn, exactly one marker commit, a clean working tree, exclusive
writer lock and confirmed session stop. Missing receipts, skipped regression
cases, unchanged image IDs, ownership/version conflicts or incomplete recovery
fail the campaign. It does not test child questions, external Git delivery,
cluster session Jobs, real model history, arbitrary schema rollback, quota
exhaustion or UI behavior. Those require separate campaigns; a real-model run
remains excluded until its own specification exists.

## Results and release use

`results.json` records overall status, the regression prerequisite, each case's
status/reason/timestamps, assertion receipts, workload and pod UIDs and immutable
container image IDs. Unattempted cases remain `not-run`. `regression.json` exports
only the nine validated scenario pass/elapsed receipts, excluding raw errors.
`teardown.json` records successful ownership-checked namespace deletion. None
contains passwords, bearer tokens, connection strings, raw Kubernetes diagnostics,
repository URLs or event payloads. Keep these files with the exact candidate and
previous source revisions as private release evidence.

The fixture volume holds private bootstrap credentials, turn state and raw local
session-suite evidence until teardown. Collect only allowlisted summaries for
sharing. The controller does not print raw service/daemon logs. Each kubectl
operation has a bounded timeout; individual rollout/fixture phases also have
explicit deadlines. Timeouts fail and retain evidence rather than counting a skip.

Before cutting a release, the operator runs the same `all` command against the
candidate images and requires exit zero plus all three JSON receipts. There is no
implicit cluster access from ordinary unit tests, image builds, or release scripts.
The first operator-run cluster campaign is still outstanding; offline tests are
not a claim of live acceptance.
