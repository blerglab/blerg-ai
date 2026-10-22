# Installing Blerg on Kubernetes — runbook for an AI agent

You are an agent installing Blerg on a Kubernetes cluster for a human. Follow
this document top to bottom. Each step says what to run and what a good
result looks like; do not move on from a checkpoint that failed. The human
version of the same install, with the background for every step, is
[`README.md`](README.md) in this directory.

Run every command from the repository root unless a step says otherwise.
Placeholders: `<DOMAIN>` is the domain the human chose, `<SCHEME>` is `https`
or `http`, `<REGISTRY>` the registry, `<TAG>` the image tag. Examples use
`blerg.example.com` and `reg.example:5000`.

## Ground rules

1. **Never invent a credential, hostname, registry, issuer or password.** If a
   value is not in this document or given by the human, ask.
2. **Ask the human at every decision point below** before running `deploy.sh`,
   and read the answers back in one summary for them to confirm.
3. **Never print a secret value into the transcript.** That means: no
   `kubectl get secret ... -o yaml` or `-o json` to the terminal, no
   `base64 -d` to the terminal, no `echo` of a variable that holds a secret,
   no `cat` of `install/k8s/.env` once it contains `SECRET_*` lines, and no
   setup link: never run `blerg-core setup-link` (without `--check`) and never
   run `deploy.sh` on a terminal; the human runs `setup-link` in their own
   terminal. Core's log never contains the link. Until the
   first account exists, the setup link is the admin account — whoever opens
   it first creates it. It is for the human only. The commands in this runbook
   are written to comply; keep them that way.
4. **Read secrets from the cluster only when a step needs them**, into a shell
   variable or a pipe, inside the same command that uses them, and `unset`
   them afterwards. Compare secrets by comparing variables, never by showing
   them.
5. **Never ask the human to paste a secret into the conversation.** If they
   want to pin a `SECRET_*` value, they add it to `install/k8s/.env`
   themselves.
6. **Never delete data or rotate a secret without an explicit confirmation
   line from the human** (see [Rollback and uninstall](#rollback-and-uninstall)).
   That covers deleting the `blerg` or `blerg-runner-sessions` namespace, any
   PVC, the `blerg-secrets` Secret, and setting `SECRET_POSTGRES_PASSWORD` or
   `SECRET_BLERG_CORE_LOCAL_KEY` on an install that already exists.
7. **Do not edit tracked files** except `install/k8s/ingress.yaml` when
   decision 4 requires it, and show the human the diff. Never commit
   `install/k8s/.env` (it is git-ignored; keep it that way).
8. **On a failed checkpoint, stop**, find the symptom in
   [Troubleshooting](#troubleshooting), and tell the human what you saw and
   what you propose before changing anything in the cluster.

## Decision points — ask the human

Ask all of these before the install; skip none. The right-hand column is
what the answer becomes.

| # | Ask | Why it matters | Becomes |
|---|---|---|---|
| 0 | "Is `kubectl config current-context` (show them the name) the cluster to install into?" | Everything below acts on that cluster. | Confirmation only. |
| 1 | **Domain and DNS.** "Which domain should Blerg use? I need DNS for `<DOMAIN>`, `board.<DOMAIN>` and `runner.<DOMAIN>` pointing at the ingress controller — do those records (or a `*.<DOMAIN>` wildcard) exist, or will you create them?" | Three hostnames are fixed by `ingress.yaml`. A nested domain (`blerg.example.com`) needs records for the sub-hosts too; a `*.example.com` wildcard does not cover `board.blerg.example.com`. | `DOMAIN=` |
| 2 | **Registry.** "Which image registry can every node pull from, and can this machine push to it (is `docker login` done)?" | The nodes pull the images, not this machine. MicroK8s' built-in registry is `localhost:32000` — that address only means the registry from a cluster node, so with it `deploy.sh` must run on a node. | `REGISTRY=` |
| 3 | **Storage class.** Show them `kubectl get storageclass`. "Postgres needs a 5Gi volume and the runner a 10Gi one (the files sessions publish and people attach) from the default StorageClass — is this one right, and is 10Gi enough?" | Without a default class the PVCs stay `Pending` forever. The runner's size and class apply only when its claim is created. | Confirmation; the human fixes it if absent. `RUNNER_DATA_SIZE=` if they want another size; `RUNNER_DATA_STORAGE_CLASS=<name>` if the runner's volume should come from another class (or there is no default). |
| 4 | **Ingress class.** Show them `kubectl get ingressclass`. "Is this ingress-nginx with class `nginx`?" | `ingress.yaml` sets `ingressClassName: nginx` and nginx-only annotations (WebSocket timeouts `3600`, body size `100m`). | If not nginx: approval to edit `ingress.yaml` (Install step 2). |
| 5 | **TLS.** "How is TLS provided: a cert-manager ClusterIssuer (which name?), a proxy or load balancer in front that terminates TLS, or none?" | Login cookies are `Secure`: without TLS, browser login only works at `localhost`. | Issuer: `CLUSTER_ISSUER=<name>`. Terminator: `SCHEME=https` (its certificate must cover all three hosts). None: tell them login will fail from any other host and get an explicit "proceed anyway". |
| 6 | **Cluster runtime.** "Should agent sessions run as pods in this cluster (the default), or only on workstation daemons? Do you manage the `blerg-runner-agent` Secret with your own secret manager?" | On: builds the agent image and creates the `blerg-runner-sessions` namespace, RBAC and Secret. | `CLUSTER_RUNTIME=off` only if they say no; `AGENT_SECRET_EXTERNAL=1` if they manage the Secret. |
| 7 | **Private-network webhooks.** "Do `<DOMAIN>` or the systems that should receive session-completion webhooks resolve to private addresses (e.g. a network behind NAT)?" | The runner refuses webhooks to private, loopback and link-local addresses unless allowed. | `WEBHOOK_ALLOW_PRIVATE=1` if yes. |
| 8 | **Postgres.** "Use the bundled single Postgres (default), or an existing external one?" | `deploy.sh` always points the components at the bundled Postgres. | External: stop — this runbook does not cover it; refer the human to README's "Postgres notes". |

Also decide, without asking, `TAG`: a fresh value for this build, e.g.
`git rev-parse --short HEAD`. Tell the human which one you used.

## Preflight

Run each command; compare with the expected output. Any mismatch is a stop.

| # | Command | Expected |
|---|---|---|
| P1 | `kubectl config current-context` | A context name the human confirmed (decision 0). |
| P2 | `kubectl get nodes` | At least one node with `STATUS` `Ready`. |
| P3 | `docker version --format '{{.Server.Version}}'` | A version number, not an error. |
| P4 | `command -v envsubst openssl jq curl` | Four paths. (`deploy.sh` needs `envsubst` and `openssl`; this runbook uses `jq` and `curl`.) |
| P5 | `kubectl get storageclass` | One line whose `NAME` column reads `<name> (default)`. |
| P6 | `kubectl get ingressclass` | A line whose `NAME` is `nginx` (or the class agreed in decision 4). |
| P7 | `kubectl get clusterissuer <issuer>` (only if decision 5 named one) | `READY` is `True`. |
| P8 | `kubectl get nodes -o jsonpath='{.items[*].status.nodeInfo.architecture}'; echo; docker version --format '{{.Server.Arch}}'` | Every value the same (`amd64` or `arm64`). `deploy.sh` builds for this machine's architecture. |
| P9 | `getent hosts <DOMAIN> board.<DOMAIN> runner.<DOMAIN>` | Three lines, all the address that reaches the ingress controller: the `EXTERNAL-IP` of the controller's Service (`kubectl get svc -A \| grep -i ingress`), or, on an upgrade, the `ADDRESS` of `kubectl -n blerg get ingress blerg`. Hostport-style controllers (e.g. the MicroK8s add-on) have neither: then it is a node's address — ask the human. |
| P10 | `curl -s -o /dev/null -w '%{http_code}\n' https://<REGISTRY>/v2/` (use `http://` for a plain-HTTP registry) | `200` or `401`. This only proves this machine reaches it; node pulls are checked in Install step 4. With `REGISTRY=localhost:32000`, run this — and `deploy.sh` — on a cluster node. |
| P11 | `kubectl get namespace blerg` | `Error from server (NotFound)` for a new install. If it exists, this is an **upgrade**: tell the human, set no `SECRET_*` value, only change `TAG`, and pass the [upgrade gate](#upgrade-gate-upgrades-only) before Step 3. |
| P12 | `git check-ignore install/k8s/.env` | Prints `install/k8s/.env`. |

## Install

### Step 1 — write `install/k8s/.env`

Only non-secret settings from the decisions; omit any line whose decision did
not apply. **Never overwrite an existing `install/k8s/.env`** — it may hold
the human's `SECRET_*` pins.

Check first:

```sh
test -e install/k8s/.env && echo "exists" || echo "absent"
```

**If `absent`**, create it readable only by its owner. Example for decisions
1, 2, 5 (issuer) and 7:

```sh
( umask 077
  cat > install/k8s/.env <<'EOF'
REGISTRY=reg.example:5000
TAG=<commit>
DOMAIN=blerg.example.com
CLUSTER_ISSUER=letsencrypt-prod
WEBHOOK_ALLOW_PRIVATE=1
EOF
)
```

**If `exists`** (always the case on an upgrade), show its non-secret settings
only:

```sh
grep -v '^#' install/k8s/.env | grep -v '^SECRET_' | grep .
```

Tell the human which lines you would change or add, and wait for their
approval. Then edit only those lines — never the file as a whole, never a
`SECRET_*` line. With GNU sed (macOS: `sed -i ''`), one command per setting:

```sh
sed -i 's|^TAG=.*|TAG=<commit>|' install/k8s/.env                 # change an existing line
grep -q '^WEBHOOK_ALLOW_PRIVATE=' install/k8s/.env || echo 'WEBHOOK_ALLOW_PRIVATE=1' >> install/k8s/.env   # add a missing one
```

On an upgrade the only line to change is `TAG` (plus `RUNNER_DATA_SIZE` /
`RUNNER_DATA_STORAGE_CLASS` if decision 3 set them and the install has no
`blerg-runner-data` claim yet — see the upgrade gate).

**Checkpoint:** `grep -v '^#' install/k8s/.env | grep -v '^SECRET_' | grep .`
prints exactly the settings you agreed with the human, and
`ls -l install/k8s/.env` shows `-rw-------` for a file you created.

### Step 2 — ingress class (only if decision 4 said not nginx)

Change `ingressClassName: nginx` in `install/k8s/ingress.yaml` to the agreed
class and replace the three `nginx.ingress.kubernetes.io/*` annotations with
that controller's equivalents (read/send timeout 3600 s, body size 100m).

**Checkpoint:** `git diff install/k8s/ingress.yaml` shown to the human and
approved; `bash install/k8s/deploy_test.sh` ends with `ok` (it prints
`skip: kubectl not on PATH ...` first when `kubectl` is absent).

### Upgrade gate (upgrades only)

Skip this on a new install. On an upgrade (P11 found `namespace/blerg`), do
all four before Step 3:

1. **Record what is running now** — you need it to roll back:

   ```sh
   kubectl -n blerg get deploy -o wide
   kubectl -n blerg get configmap blerg-config -o jsonpath='{.data.BLERG_RUNNER_AGENT_IMAGE}'; echo
   ```

   Note the old image tag (the `IMAGES` column), the old agent image, and the
   commit the old tag was built from (ask the human if the tag is not a
   commit id). Do not use `git rev-parse HEAD` for this: HEAD is the new
   build you are about to deploy.

2. **Offer a backup.** Database migrations run forward on boot, so an
   upgrade cannot always be undone without one. If the human accepts, ask
   where to keep it (outside the repository), then write it without printing
   anything:

   ```sh
   ( umask 077
     dir=<backup-dir>; mkdir -p "$dir"
     for db in blerg_core blerg_board blerg_runner; do
       kubectl -n blerg exec postgres-0 -- pg_dump -U blerg -d "$db" > "$dir/$db.sql"
     done
     kubectl -n blerg get secret blerg-secrets -o json \
       | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields, .metadata.annotations)' \
       > "$dir/blerg-secrets.json"
   )
   ls -l <backup-dir>
   ```

   **Checkpoint:** four files, none of size `0`, all `-rw-------`. Tell the
   human that `blerg-secrets.json` holds every secret of the install.

3. **Runner data volume.** Run `kubectl -n blerg get pvc blerg-runner-data`.
   `NotFound` means this install predates the runner's data volume: its
   `/data` was an `emptyDir`. Tell the human: this `deploy.sh` run creates the
   claim **empty**, so there is nothing to migrate or back up from the old pod;
   files published or attached before it are already lost (or go with the old
   pod), stay listed in each session's Files tab marked "no longer available",
   and cannot be opened. Everything published afterwards is kept. The runner
   Deployment uses rolling updates: the old pod serves until the new pod's
   database and migrations pass readiness. The surge stays on the old pod's
   node so both can mount the `ReadWriteOnce` claim. Liveness remains healthy
   during database startup retries. If the claim exists, it is retained.

4. **Get the human's go-ahead** to upgrade from the old tag to `<TAG>`, with
   or without the backup, before running Step 3.

### Step 3 — run `deploy.sh`

From the repository root:

```sh
BLERG_ASSUME_YES=1 install/k8s/deploy.sh
```

Before building, `deploy.sh` prints the kubectl context and cluster server it
will change and, on a terminal, asks for confirmation. Run it with
`BLERG_ASSUME_YES=1` (or `--yes`) only after the human has confirmed that
context and server in the pre-flight (P1). Without a terminal it does not ask and it
refuses to run unless you pass one of those, so this step is always deliberate.

It builds four images (three with `CLUSTER_RUNTIME=off`), pushes them, creates
the Secrets, applies the manifests and waits up to 180 s for the Deployments.
Image builds take several minutes; run it in the background if your shell
tool has a short timeout, and wait for it to exit. Its output contains no
secret values as long as its stdout is not a terminal: on a terminal, a fresh
install ends with the **setup link**, which is a credential (whoever opens it
first creates the admin account). Your shell tool's output is normally not a
terminal; if yours gives commands one, send the output to a file
(`... > /tmp/blerg-deploy.log 2>&1; echo "exit=$?"`) and read that.

**Checkpoint:** exit status `0`, and the output ends with:

```
==> Done. See README.md for how to reach the landing page and connect the runner daemon.
==> Cluster runtime: on (users add their own credentials at <SCHEME>://<DOMAIN>/settings)

==> FINISH SETUP: this install has no account yet. The person who will own it runs this in their
    own terminal to get the setup link (a credential, so it is not printed here):
    kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
    then opens it and chooses their username and password.
```

(`Cluster runtime: off (CLUSTER_RUNTIME=off)` if decision 6 turned it off.) The
`FINISH SETUP` block appears only on a fresh install; an upgrade of an install
that already has an account ends after the `Cluster runtime` line. Without a
terminal, `deploy.sh` mints nothing (it only checks with `setup-link --check`),
so a re-run never invalidates a link the human already has. Do not run the
printed `setup-link` command yourself: it prints the setup link, which is for
the human only. Hand the command to the human (handoff message, step 1).

### Step 4 — pods are running

```sh
kubectl -n blerg get pods
```

**Checkpoint:** four pods, all `READY 1/1` and `STATUS Running`:
`blerg-core-…`, `blerg-board-…`, `blerg-runner-…` and `postgres-0`; and
`kubectl -n blerg get pvc blerg-runner-data` shows `STATUS Bound`. A restart
or two on board or runner in the first seconds is normal (they race core's
migrations). `ImagePullBackOff`, `Pending` or `CrashLoopBackOff`: stop, see
[Troubleshooting](#troubleshooting).

### Step 5 — Secrets exist (names only)

```sh
kubectl -n blerg get secret blerg-secrets -o jsonpath='{.data}' | jq -r 'keys[]'
```

**Checkpoint:** at least these twelve keys — `BLERG_BOARD_SECRET_KEY`,
`BLERG_BOARD_SERVICE_KEY`, `BLERG_CORE_BOARD_MINT_KEY`, `BLERG_CORE_INTERNAL_KEY`, `BLERG_CORE_LOCAL_KEY`, `BLERG_CORE_REGISTER_KEY`,
`BLERG_RUNNER_DAEMON_TOKEN`, `BLERG_RUNNER_DATABASE_URL`, `BLERG_RUNNER_KEY`,
`DATABASE_URL_BOARD`, `DATABASE_URL_CORE`, `POSTGRES_PASSWORD` (plus
`ANTHROPIC_API_KEY`, `BLERG_CORE_GITHUB_TOKEN`, `BLERG_RUNNER_GITHUB_TOKEN`
only if pinned).

With cluster runtime on, also check that both Secrets hold the same daemon
token, without showing it:

```sh
a="$(kubectl -n blerg get secret blerg-secrets -o jsonpath='{.data.BLERG_RUNNER_DAEMON_TOKEN}')"
b="$(kubectl -n blerg-runner-sessions get secret blerg-runner-agent -o jsonpath='{.data.BLERG_RUNNER_DAEMON_TOKEN}')"
[ -n "$a" ] && [ "$a" = "$b" ] && echo "daemon token: match" || echo "daemon token: MISMATCH"
unset a b
```

**Checkpoint:** `daemon token: match`.

### Step 6 — ingress and TLS

```sh
kubectl -n blerg get ingress blerg
```

**Checkpoint:** `CLASS` is the agreed class, `HOSTS` lists the three hosts,
and `ADDRESS` is filled in (some controllers leave it empty; then rely on P9
and Step 7).

If `CLUSTER_ISSUER` is set, cert-manager creates the Certificate
asynchronously after `deploy.sh` annotates the ingress, so wait for it to
exist before waiting for it to be ready:

```sh
for i in $(seq 1 30); do kubectl -n blerg get certificate blerg-tls >/dev/null 2>&1 && break; sleep 5; done
kubectl -n blerg wait --for=condition=Ready certificate/blerg-tls --timeout=600s
```

**Checkpoint:** `certificate.cert-manager.io/blerg-tls condition met`. On a
timeout, run `kubectl -n blerg get certificate,order,challenge` and see
[Troubleshooting](#troubleshooting).

### Step 7 — cluster runtime (skip if decision 6 turned it off)

```sh
kubectl -n blerg logs deploy/blerg-runner | grep -c 'cluster agent runtime enabled'
kubectl -n blerg-runner-sessions get rolebinding blerg-runner-agent-launcher
kubectl -n blerg get configmap blerg-config -o jsonpath='{.data.BLERG_RUNNER_AGENT_IMAGE}'; echo
```

**Checkpoint:** a count of `1` or more; the RoleBinding is listed; the image
is `<REGISTRY>/blerg-runner-agent:<TAG>`.

## Verify

Set the first two lines, then run the block as one script. It prints one
`ok`, `skip` or `FAIL` line per check and never prints a secret.

```bash
DOMAIN=blerg.example.com   # <DOMAIN>
SCHEME=https               # <SCHEME>
CLUSTER_RUNTIME=on         # off if decision 6 turned it off

core="$SCHEME://$DOMAIN"; board="$SCHEME://board.$DOMAIN"; runner="$SCHEME://runner.$DOMAIN"
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fail=1; fi; }

# 1. Every pod in the blerg namespace is Ready.
if kubectl -n blerg wait --for=condition=Ready pod --all --timeout=180s >/dev/null; then
  echo "ok   pods Ready"
else
  echo "FAIL pods not Ready"; fail=1; kubectl -n blerg get pods
fi

# 2. Health through the ingress.
check "core /healthz"   "$(curl -sS "$core/healthz" | jq -r .status)" ok
check "board /healthz"  "$(curl -sS "$board/healthz")" ok
check "runner /healthz" "$(curl -sS "$runner/healthz")" ok

# 3. The public agent manifest.
check "GET /agents status" "$(curl -s -o /dev/null -w '%{http_code}' "$core/agents")" 200
check "contract_version"   "$(curl -sS "$core/agents" | jq -r .contract_version)" v1
check "registered components" \
  "$(curl -sS "$core/agents" | jq -r '[.components[].name] | sort | join(",")')" \
  "blerg-board,blerg-core,blerg-runner"

# 4. Cluster status is authenticated; without a token it must answer 401.
check "GET /api/cluster/status needs auth" \
  "$(curl -s -o /dev/null -w '%{http_code}' "$runner/api/cluster/status")" 401
if [ "$CLUSTER_RUNTIME" = on ]; then
  n="$(kubectl -n blerg logs deploy/blerg-runner | grep -c 'cluster agent runtime enabled')"
  [ "$n" -ge 1 ] && echo "ok   cluster runtime enabled in runner" || { echo "FAIL cluster runtime not enabled"; fail=1; }
fi

# 5. First-run setup: open and refusing made-up codes on a fresh install, closed on
#    an upgrade. Uses no credential and creates nothing.
setup="$(curl -sS "$core/auth/provider" | jq -r '.setup // "done"')"
case "$setup" in
  code)
    check "setup refuses a made-up code" \
      "$(curl -s -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
         --data '{"code":"not-a-real-code","provider_subject":"probe","password":"not-a-real-password"}' \
         "$core/auth/setup")" 403 ;;
  done) echo "skip setup check: this install already has an account" ;;
  *) echo "FAIL setup mode: got '$setup', want code (or done on an upgrade)"; fail=1 ;;
esac

[ "$fail" -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
```

**Expected:** every line `ok` (a `skip` line explains itself), then
`ALL CHECKS PASSED`. The components check can fail for ~20 s after a fresh
deploy while board and runner register; run the block again before
troubleshooting.

`GET <runner>/api/cluster/status` itself needs a login, so this script only
checks that it refuses anonymous callers. For its content, either ask the
human to open the runner's **Cluster** tab (`<SCHEME>://runner.<DOMAIN>/cluster`),
or — once the human has minted a `run-sessions` agent token and put it in
your environment as `BLERG_TOKEN` without showing it to you — run:

```sh
curl -s -H "Authorization: Bearer $BLERG_TOKEN" https://runner.<DOMAIN>/api/cluster/status | jq '{configured, namespace, image, max_sessions, active_sessions}'
```

Expected: `"configured": true`, `"namespace": "blerg-runner-sessions"`, the
agent image, `"max_sessions": 4`, `"active_sessions": 0`. Empty
`available_engines` and `"git_configured": false` are normal — they describe
only the operator Secret.

## Handoff message

Send the human this, filled in. Do not include any secret value.

```text
Blerg is installed on <cluster context> and all checks passed.

  Landing page  <SCHEME>://<DOMAIN>/
  Board         <SCHEME>://board.<DOMAIN>/
  Runner        <SCHEME>://runner.<DOMAIN>/
  Image tag     <TAG>   (settings: <summary of the decisions>)

1. Create your account (run this yourself — the output is a one-time setup
   link, a credential: whoever opens it first becomes the administrator):
     kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
   Open the link and choose your username and a password of 12–72
   characters; you are signed in as the administrator. The link works once,
   for 24 hours, and running the command again replaces it.

2. Add your credentials at <SCHEME>://<DOMAIN>/settings: a Claude, Codex or
   Hermes credential and a GitHub token. Sessions run as you, with these.

3. Start a session: Runner → + New → repository as org/name → Launch.

4. For tools and agents: Settings → Agent tokens → preset "run-sessions".
   The token is shown once; tools send it as "Authorization: Bearer <token>"
   and start from <SCHEME>://<DOMAIN>/agents.

5. Add teammates:
     kubectl -n blerg exec deploy/blerg-core -- /blerg-core users create --subject <name> --role member
   and give them the printed one-time password.

6. Back up the Postgres volume and the blerg-secrets Secret (install/k8s/README.md,
   "Backup, restore and uninstall"). Losing BLERG_CORE_LOCAL_KEY in blerg-secrets
   loses every stored credential.
```

## Rollback and uninstall

**A failed step during a first install.** Nothing needs undoing to retry:
fix the cause and re-run `./deploy.sh`; it reuses the Secrets it already
created. Do not delete `blerg-secrets` to "start clean" — Postgres keeps the
password from its first initialization.

**Rolling back an upgrade.** Use what you recorded at the
[upgrade gate](#upgrade-gate-upgrades-only). Database migrations only run
forward: an older build may refuse to start against a database a newer build
has migrated. If it does, the way back is the backup (README "Restore") —
tell the human before trying. Do not use `kubectl rollout undo`: `deploy.sh`
restarts the runner after applying, so the previous revision already has the
new image, and it would not restore `BLERG_RUNNER_AGENT_IMAGE` in
`blerg-config` either.

Preferred, with the human's approval — redeploy the old build, which restores
images and ConfigMap consistently:

```sh
git checkout <old-commit>
sed -i 's|^TAG=.*|TAG=<OLD_TAG>|' install/k8s/.env
install/k8s/deploy.sh
```

(`deploy.sh` builds from the checked-out code and pushes it as `<OLD_TAG>`:
first confirm `git rev-parse --short <old-commit>` equals `<OLD_TAG>`, or you
would overwrite the good old images in the registry with different code. If
the old images are still in the registry, prefer the no-rebuild route below. Afterwards, return the checkout to where the human wants it.)

Without rebuilding — point everything back at the old images still in the
registry:

```sh
kubectl -n blerg patch configmap blerg-config --type merge \
  -p '{"data":{"BLERG_RUNNER_AGENT_IMAGE":"<REGISTRY>/blerg-runner-agent:<OLD_TAG>"}}'   # cluster runtime only
for c in blerg-core blerg-board blerg-runner; do
  kubectl -n blerg set image deploy/$c $c=<REGISTRY>/$c:<OLD_TAG>
done
kubectl -n blerg rollout status deploy/blerg-core deploy/blerg-board deploy/blerg-runner --timeout=180s
```

Then set `TAG=<OLD_TAG>` in `install/k8s/.env` (as in Step 1) so the next
`deploy.sh` run does not silently re-apply the newer tag.

**Stopping without deleting anything:**

```sh
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner statefulset/postgres --replicas=0
```

**Uninstalling** deletes the Postgres volume, the runner's data volume (every
published and attached file) and `blerg-secrets`, including
`BLERG_CORE_LOCAL_KEY`: every account, board, session record and stored
credential is gone for good. Before running it:

1. Tell the human exactly that, and offer the backup from README's
   "Backup, restore and uninstall" first.
2. Ask them to reply with this exact line, and do not proceed on anything
   else (not "yes", not "go ahead"):

   `I confirm: delete the Blerg namespaces blerg and blerg-runner-sessions and all their data`

Only then:

```sh
kubectl delete namespace blerg-runner-sessions
kubectl delete namespace blerg
```

Whether the Postgres volume itself is erased depends on the StorageClass's
reclaim policy (`Delete` is the usual default; `Retain` keeps the volume for
the human to remove). The images stay in the registry, and `install/k8s/.env`
stays on disk; tell the human all three.

## Troubleshooting

Find the symptom you observed; run the check; propose the fix to the human.
More rows, with background, are in README's "Troubleshooting".

| Symptom | Check | Fix |
|---|---|---|
| `deploy.sh` stops with `docker is required` / `kubectl is required` | P3 / P1 | Install the tool on this machine. |
| `deploy.sh` stops with `envsubst is required` or `openssl is required` | P4 | Install gettext / openssl; re-run `deploy.sh`. |
| `docker push` fails: `http: server gave HTTP response to HTTPS client`, `denied`, or `unauthorized` | P10 | Plain-HTTP registry: the human adds it to this machine's Docker `insecure-registries`. Auth: the human runs `docker login <REGISTRY>`. |
| Pods `ImagePullBackOff` / `ErrImagePull` | `kubectl -n blerg describe pod <pod>` Events | Nodes cannot pull from `REGISTRY` (address, containerd trust or insecure-registry config, pull credentials). Node configuration is the human's. |
| `postgres-0` `Pending`; `rollout status` times out | `kubectl -n blerg get pvc` shows `Pending`; P5 | No default StorageClass. The human installs or marks one. On Kubernetes ≥ 1.28 a class-less `Pending` claim then picks up the new default by itself — wait a minute and check again. If it is still `Pending`, with the human's approval (it holds no data yet) delete both so the StatefulSet recreates the claim: `kubectl -n blerg delete pvc/postgres-data-postgres-0 pod/postgres-0`. If the new `postgres-0` stays `Pending` because it caught the old claim while it was terminating, delete `pod/postgres-0` once more. |
| `blerg-runner-…` `Pending`; `rollout status` times out on the runner | `kubectl -n blerg get pvc blerg-runner-data` shows `Pending`; `kubectl -n blerg describe pvc blerg-runner-data` | No default StorageClass, or `RUNNER_DATA_STORAGE_CLASS` names one that does not exist. With the human's approval: set `RUNNER_DATA_STORAGE_CLASS` (Step 1); stop the runner and delete the still-`Pending` claim (it holds no data yet), waiting until it is gone — `kubectl -n blerg scale deploy/blerg-runner --replicas=0`, `kubectl -n blerg delete pvc blerg-runner-data --wait=false`, `kubectl -n blerg wait --for=delete pvc/blerg-runner-data --timeout=120s` — then re-run `deploy.sh` (it recreates the claim and the runner). Never delete a `Bound` one: it holds the published files. |
| Pods `CrashLoopBackOff`, log says `exec format error` | P8 | Architecture mismatch. Build on a machine matching the nodes; deploy with a new `TAG`. |
| `rollout status` times out, board or runner restarting | `kubectl -n blerg logs deploy/blerg-core` (core never logs a setup code or link; on a fresh install it logs only that setup is pending) | `jwks fetch failed` / `register ... 401` for ~15 s is normal. Longer: look for a migration or database error in core's log. |
| Components log Postgres `password authentication failed` | Was `SECRET_POSTGRES_PASSWORD` set on an existing install, or `blerg-secrets` deleted? | The data directory keeps its first password. The human restores the original value (`SECRET_POSTGRES_PASSWORD` from their backup) and you re-run `deploy.sh`. |
| Every host answers `404` from the ingress | P6; `kubectl -n blerg get ingress blerg` | Wrong ingress class. Install step 2. |
| Browser or daemon reconnects in a loop; uploads fail `413` | `kubectl -n blerg get ingress blerg -o yaml` annotations | Controller ignores nginx annotations; set its own 3600 s timeouts and 100m body limit. |
| Human reports login looping at "Sign in required" | Are they on `http://` at a host other than `localhost`? | Decision 5: TLS is required. Set `CLUSTER_ISSUER` or `SCHEME=https` and re-run `deploy.sh`. |
| Redirect loop between board/runner and `/auth/refresh`, or `400 invalid return_to` | `kubectl -n blerg get configmap blerg-config -o jsonpath='{.data.BLERG_CORE_PUBLIC_URL}'` | `SCHEME` does not match what the browser uses; fix it in `.env`, re-run `deploy.sh`. |
| `certificate/blerg-tls` never Ready | `kubectl -n blerg get certificate,order,challenge`; `kubectl -n blerg describe challenge` | DNS-01 propagation can take minutes; otherwise the challenge's reason (DNS record, issuer credentials, HTTP-01 unreachable). |
| `getent hosts` misses `board.` or `runner.` | P9 | The human adds the records (or `*.<DOMAIN>`). |
| Verify: components check lists only `blerg-core` | wait 30 s, run again; `kubectl -n blerg logs deploy/blerg-board` | Registration retries until core is up; persisting means `BLERG_CORE_REGISTER_KEY` or `BLERG_CORE_URL` is wrong. |
| `/api/cluster/status` shows `"configured": false` | Step 7 | Empty agent image: `CLUSTER_RUNTIME=off`. Image set: the runner cannot read its service-account token (see `CLUSTER-RUNTIME.md` preflight). |
| `/api/cluster/status` shows `"active_sessions": -1` | `kubectl -n blerg-runner-sessions get rolebinding`; runner logs | RBAC missing: `kubectl apply -f install/k8s/cluster-runtime.yaml` (never through the kustomization). |
| Session pods start but sessions never reach `running` | Step 5 daemon-token comparison | Mismatch: with `AGENT_SECRET_EXTERNAL` unset, re-run `deploy.sh`; otherwise the human fixes their external Secret. |
| Completion webhooks never arrive; runner logs `callback address refused (private or loopback)` | decision 7 | Set `WEBHOOK_ALLOW_PRIVATE=1`, re-run `deploy.sh`. |
| A re-deploy changed nothing | `kubectl -n blerg get deploy -o wide` shows the old tag | `TAG` was not changed. Use a new `TAG`. |
| Human lost the setup link, or it says "not valid" | — | Give them the command from the handoff message, step 1, to run in their own terminal (each run replaces the code, so an older link stops working). Do not run `setup-link` yourself: it prints the link. To see whether setup is still open without minting anything: `kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link --check`. |
| Human lost the admin password | — | Give them `kubectl -n blerg exec deploy/blerg-core -- /blerg-core users set-password --subject <username>` (the username: `kubectl -n blerg exec postgres-0 -- psql -U blerg -d blerg_core -tAc "select provider_subject from accounts where provider='local' and role='admin' order by created_at limit 1"`). Do not run `users set-password` yourself: it prints the new password. |
