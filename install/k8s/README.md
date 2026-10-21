# Blerg on a self-hosted Kubernetes cluster

Deploys the full stack — `blerg-core` (control plane), `blerg-board` (kanban
agent board), `blerg-runner` (agent session server), and a single Postgres
instance with three logical databases — into a `blerg` namespace, mirroring
[`install/desktop/docker-compose.yml`](../desktop/docker-compose.yml) but for
a real cluster instead of a single Docker host. Agent sessions run as
throwaway Kubernetes Jobs in a second namespace, `blerg-runner-sessions`.

Everything under `install/k8s/` is plain YAML plus a `kustomization.yaml` and
one script, `deploy.sh`. No Helm, no cluster-specific hostnames or IPs baked in.

**Installing with an AI agent?** Hand it
[`AGENT-INSTALL.md`](AGENT-INSTALL.md) — the same install as a step-by-step
runbook with checkpoints, the decisions it must ask you about, and rules that
keep secrets out of its transcript.

## Quick start

### 1. Check you have

- [ ] `kubectl` pointed at the target cluster (`kubectl get nodes` lists a `Ready` node).
- [ ] `docker`, `envsubst` (from gettext) and `openssl` on the machine you deploy from.
- [ ] A **default StorageClass** (`kubectl get storageclass` shows `(default)`) — Postgres asks for a `5Gi` PVC and the runner for a `10Gi` one (its files) without naming a class. Without a default, set `RUNNER_DATA_STORAGE_CLASS` for the runner's ([Runner data volume](#runner-data-volume)).
- [ ] An **image registry your cluster's nodes can pull from** (not just your workstation). MicroK8s: `microk8s enable registry` gives `localhost:32000`, the default.
- [ ] **ingress-nginx** with the ingress class `nginx` (`kubectl get ingressclass`). Another controller needs edits — see [Ingress controller](#ingress-controller-check-6-expanded).
- [ ] **DNS** for three names pointing at the ingress controller: `<DOMAIN>`, `board.<DOMAIN>` and `runner.<DOMAIN>`.
- [ ] **TLS** for those three names — a cert-manager `ClusterIssuer` (set `CLUSTER_ISSUER`), or a proxy in front that terminates TLS (set `SCHEME=https`). Without TLS, browser login only works at `localhost` ([why](#tls)).

The [Environment preflight](#environment-preflight) turns each of these into a
command with a pass/fail.

### 2. Configure

```sh
cd install/k8s
cp -n .env.example .env
```

and set, at minimum:

```sh
REGISTRY=reg.example:5000
TAG=v1
DOMAIN=blerg.example.com
CLUSTER_ISSUER=letsencrypt-prod
# WEBHOOK_ALLOW_PRIVATE=1   # only if your hostnames resolve to a private network
```

Every setting is in the [configuration reference](#configuration-reference).
Never commit `install/k8s/.env`.

### 3. Deploy

```sh
./deploy.sh
```

Before it builds anything it prints the kubectl context and cluster server it
will act on and asks you to confirm (pass `--yes`, or set `BLERG_ASSUME_YES=1`,
to skip the question; a run with no terminal on stdin is refused unless it passes one
of them, so it can never deploy to a cluster by accident). It
then builds and pushes four images, generates every secret, applies the
manifests, and waits up to 180 s for the three Deployments. The last lines it
prints are:

```
==> Done. See README.md for how to reach the landing page and connect the runner daemon.
==> Cluster runtime: on (users add their own credentials at https://blerg.example.com/settings)

==> FINISH SETUP: create the first (admin) account by opening this link and
    choosing your username and password:

    https://blerg.example.com/setup#code=…
    ...
```

On a fresh install only: once an account exists, a re-run prints nothing about
setup.

### 4. Create your account

Open the setup link `deploy.sh` printed. On the **Create your account** page,
choose your username and a password (12–72 characters). That account is the
administrator, and you are signed in. Details, a lost link and recovery:
[First login](#first-login).

### 5. Add credentials and run a session

Sessions run as the person who launched them, with their own engine and
GitHub credentials. Each user opens `https://<DOMAIN>/settings` and adds a
Claude, Codex or Hermes credential and a GitHub and/or GitLab token (the page
explains how to produce each). Then open `https://runner.<DOMAIN>/`, **+ New**,
pick one of your repositories (the list is what your tokens can see) or give one
as `org/name`, and launch — it runs as a Job in
`blerg-runner-sessions`. See [`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md).

Boards start sessions the same way, as a person. **Run this card works out of
the box on a single-user install:** a new board has **Run this board's cards as
me** checked, and a board without a token offers **Run as me** in its settings
and beside **Run this card**; one click mints your `run-sessions` token in core
and saves it on the board (`SECRET_BLERG_CORE_BOARD_MINT_KEY` below). On a
multi-tenant install set `BLERG_BOARD_DEFAULT_RUN_AS_CREATOR: "false"` in
`configmap.yaml` to start that box unchecked. See
[`board/docs/CONFIG.md`](../../board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

Then run the [verification](#verify) checks.

To disable new **Run as me** tokens entirely, set `BLERG_CORE_BOARD_MINT_DISABLED: "true"` in `blerg-config` and restart the core deployment. Existing tokens can still be revoked; this setting does not revoke them.

## Configuration reference

`deploy.sh` reads `install/k8s/.env` if it exists (otherwise the same names
from the environment). `.env.example` lists every one.

### Install settings

| Variable | Default | Meaning |
|---|---|---|
| `REGISTRY` | `localhost:32000` | Where images are pushed and pulled: `${REGISTRY}/blerg-core:${TAG}`, `blerg-board`, `blerg-runner`, `blerg-runner-agent`. The default is the MicroK8s built-in registry convention; nodes must be able to pull from it. |
| `TAG` | `latest` | Image tag, also baked in as the version string. **Bump it on every upgrade** — see [Re-running deploy.sh](#re-running-deploysh). |
| `DOMAIN` | `blerg.local` | Ingress hosts: core at `<DOMAIN>`, board at `board.<DOMAIN>`, runner at `runner.<DOMAIN>`. |
| `CLUSTER_ISSUER` | unset | Name of an existing cert-manager `ClusterIssuer`. When set, the ingress gets a certificate for all three hosts (Secret `blerg-tls`) and `SCHEME` defaults to `https`. |
| `SCHEME` | `https` if `CLUSTER_ISSUER` is set, else `http` | The scheme browsers use. Templated into every public URL, origin allowlist and audience map. Set `https` yourself when a proxy in front terminates TLS. |
| `CLUSTER_RUNTIME` | `on` | Per-session agent Jobs. `off` (or `false`/`0`/`no`) skips the agent image, the `blerg-runner-sessions` namespace, its RBAC and the agent Secret, and renders the agent image empty (the runner's off switch). |
| `AGENT_SECRET_EXTERNAL` | unset | `1` (or `true`/`yes`): you manage `blerg-runner-sessions/blerg-runner-agent` yourself; `deploy.sh` never touches it. |
| `BLERG_ASSUME_YES` | unset | `1` (or `true`/`yes`), or the `--yes`/`-y` flag: do not ask "Deploy to this cluster?" before building and applying. Without a terminal on stdin (an agent, CI, ssh without a tty) the run is refused unless one of these is given, so a non-interactive deploy is always a deliberate one. |
| `RUNNER_DATA_SIZE` | `10Gi` | Size of the runner's data claim `blerg-runner-data` (published and attached files, agent-config bundles, mockups). Applies only when `deploy.sh` creates the claim — see [Runner data volume](#runner-data-volume). |
| `RUNNER_DATA_STORAGE_CLASS` | unset (the cluster's default StorageClass) | StorageClass for that claim, e.g. when the cluster has no default or you want another. Also applies only at creation. |
| `HERMES_PROVIDERS_FILE` | unset | Path (absolute, or relative to `install/k8s`) of a Hermes `config.yaml` fragment (at most 64 KiB) naming your model servers, e.g. local keyless OpenAI-compatible endpoints. Every cluster Hermes user, and their agent, can read it whole: keep paid provider keys out. When set, `deploy.sh` stores it in Secret `blerg/blerg-hermes-providers`; every Hermes session pod gets it merged into `~/.hermes/config.yaml` and the cluster launch sheet lists its models. Unset: nothing is created. Template: `hermes-providers.example.yaml`; details in [CLUSTER-RUNTIME.md](CLUSTER-RUNTIME.md#shared-hermes-providers). |
| `WEBHOOK_ALLOW_PRIVATE` | unset (`false`) | `1` (or `true`/`yes`) renders `BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE: "true"`, allowing completion webhooks to private, loopback and link-local addresses. |

### Secret pins (`SECRET_*`)

Each value `deploy.sh` writes is chosen on a fixed ladder: the `SECRET_*`
value if set, else the value already live in the cluster, else (required keys
only, on a first deploy) random. A re-run therefore never rotates anything
unless you ask it to. Values land in `blerg-secrets` (namespace `blerg`) or
`blerg-runner-agent` (namespace `blerg-runner-sessions`).

| Variable | Written to | Default when unset | Meaning |
|---|---|---|---|
| `SECRET_POSTGRES_PASSWORD` | `blerg-secrets`: `POSTGRES_PASSWORD` and the three DSNs `DATABASE_URL_CORE`, `DATABASE_URL_BOARD`, `BLERG_RUNNER_DATABASE_URL` | live value, else random | Postgres password. It is only applied when the data directory is first initialized — **never set a new one on an existing PVC**. |
| `SECRET_BLERG_CORE_REGISTER_KEY` | `blerg-secrets` | live, else random | Key board and runner present to register with core. |
| `SECRET_BLERG_BOARD_SERVICE_KEY` | `blerg-secrets` | live, else random | Board's service key. |
| `SECRET_BLERG_BOARD_SECRET_KEY` | `blerg-secrets` | live, else `openssl rand -base64 32` | AES-256 key (base64 of 32 bytes) that encrypts each board's automation token at rest. **Losing it makes stored automation tokens unreadable** (re-enter them in the board's settings); changing it does the same. |
| `SECRET_BLERG_RUNNER_KEY` | `blerg-secrets` | live, else random | The board↔runner contract key. |
| `SECRET_BLERG_RUNNER_DAEMON_TOKEN` | `blerg-secrets` and `blerg-runner-agent` | live, else random | Token daemons and session pods present on `/ws/daemon`. Both Secrets must hold the same value. |
| `SECRET_BLERG_CORE_INTERNAL_KEY` | `blerg-secrets` | live, else random | Shared by core and runner (`BLERG_RUNNER_CORE_INTERNAL_KEY`) for the personal-credential fetch. |
| `SECRET_BLERG_CORE_BOARD_MINT_KEY` | `blerg-secrets` | live, else random | Shared by core and board for "Run as me": the board mints a person's run-sessions token through core in one click, so Run this card works without pasting a token. Opens nothing else on core. |
| `SECRET_BLERG_CORE_LOCAL_KEY` | `blerg-secrets` | live, else `openssl rand -base64 32` | AES-256 key for the credential vault (base64 of 32 bytes). **Losing it loses every stored credential**; changing it makes them unreadable. |
| `SECRET_ANTHROPIC_API_KEY` | `blerg-secrets` (read by board) and `blerg-runner-agent` | live, else omitted | Optional. In the agent Secret it is a shared fallback Claude key for every cluster session; board uses it for the admission gate unless the gate runs on a person's own credential (`BLERG_BOARD_GATE_ACCOUNT_TOKEN`, see `board/docs/CONFIG.md`). |
| `SECRET_BLERG_CORE_GITHUB_TOKEN` | `blerg-secrets` | live, else omitted | Optional. Core's own GitHub reconciliation. |
| `SECRET_BLERG_RUNNER_GITHUB_TOKEN` | `blerg-secrets` | live, else omitted | Optional. Runner's repo picker org listing (pair with `BLERG_RUNNER_GITHUB_ORG` in `blerg-config`). |
| `SECRET_CLAUDE_CODE_OAUTH_TOKEN` | `blerg-runner-agent` | live, else omitted | Optional shared fallback Claude subscription token. |
| `SECRET_BLERG_RUNNER_GIT_TOKEN` | `blerg-runner-agent` | live, else omitted | Optional shared fallback git token for clone/push. |
| `SECRET_CODEX_AUTH_JSON` | `blerg-runner-agent` | live, else omitted | Optional shared fallback Codex `auth.json` contents. |
| `SECRET_HERMES_ENV_CONTENTS` | `blerg-runner-agent` | live, else omitted | Optional shared fallback Hermes `.env` contents. |

Two consequences of the ladder: a pinned value, once applied, *is* the live
value, so removing the pin from `.env` keeps it; and an optional key, once
written, stays until you remove it from the Secret by hand. Users' own
credentials from Settings take precedence over every shared fallback above.

## Environment preflight

Written so an agent (or a human) can verify every precondition with a
runnable command and an unambiguous pass/fail, before touching `deploy.sh`.
Run every check in this section before deploying — a partial preflight is
what makes a stuck PVC or a 404 ingress look like a `deploy.sh` bug instead
of an environment gap.

| # | Check | Command | Pass looks like | If it fails |
|---|---|---|---|---|
| 1 | `kubectl` is installed and points at the target cluster | `kubectl config current-context` | Prints a context name, not an error | Install `kubectl`; `kubectl config use-context <name>` |
| 2 | The cluster is actually reachable | `kubectl get nodes` | Lists at least one `Ready` node | Fix networking/kubeconfig before anything else |
| 3 | Docker is available to build images | `docker version` | Prints client + server info | Install/start Docker |
| 4 | **Default StorageClass exists** (Postgres's PVC has no `storageClassName` set — it relies on the cluster default) | `kubectl get storageclass` | At least one `NAME` reads `<name> (default)` | Enable/create one for your distro (MicroK8s: `microk8s enable hostpath-storage`; k3s ships `local-path` by default; kubeadm needs a CSI driver installed explicitly). Without this, the Postgres pod's PVC stays `Pending` forever — a common, non-obvious first failure regardless of distro |
| 5 | An image registry the cluster's **nodes** (not just your workstation) can pull from | see below | — | — |
| 6 | An ingress controller is running, with the class `ingress.yaml` expects | `kubectl get pods -A \| grep -i ingress` and `kubectl get ingressclass` | A running ingress pod; `ingressclass` list includes `nginx` (or whatever you set `ingressClassName` to) | See "Ingress controller" below |
| 7 | DNS/hosts resolve `${DOMAIN}` and its subdomains to the ingress controller | `getent hosts <DOMAIN> board.<DOMAIN> runner.<DOMAIN>` from any machine you'll browse from | All three resolve to the address that reaches the ingress controller: its Service's `EXTERNAL-IP` (`kubectl get svc -A \| grep -i ingress`), or a node's address for hostport-style controllers such as the MicroK8s add-on (after a deploy, `kubectl -n blerg get ingress blerg` shows it as `ADDRESS` when the controller reports one) | Add DNS records or `/etc/hosts` entries — see "Reaching the stack" below |
| 8 | Enough headroom for 4 pods (`core`/`board`/`runner`/`postgres`) at their default requests | `kubectl top nodes` (if metrics-server is installed) or just eyeball available CPU/mem | ≥ ~550m CPU / ~640Mi free across the cluster: core/board/runner each request `100m`/`128Mi` (limit `1`/`1Gi`) and postgres requests `250m`/`256Mi` (limit `2`/`2Gi`) | Reduce replica count (already 1 each) or add a node; this is a small test-deploy stack, not sized for production load |
| 9 | `envsubst` and `openssl` are installed | `command -v envsubst openssl` | Prints two paths | Install gettext (`envsubst`) and openssl. `deploy.sh` checks for `docker`, `kubectl`, `envsubst` and `openssl` before it builds anything and stops with a message naming the missing one |
| 10 | The build machine's CPU architecture matches the nodes' (`deploy.sh` builds with plain `docker build`, so images are the builder's architecture) | `kubectl get nodes -o jsonpath='{.items[*].status.nodeInfo.architecture}'` vs. `docker version --format '{{.Server.Arch}}'` | Same value (`amd64` or `arm64`) everywhere | Build on a matching machine; a mismatch shows up as `exec format error` crash loops |
| 11 | (TLS via cert-manager only) The issuer exists and is ready | `kubectl get clusterissuer` | Your issuer's row shows `READY True` | Install cert-manager and create the issuer first, or leave `CLUSTER_ISSUER` unset |

### Image registry (check 5, expanded)

The cluster's **nodes** must be able to pull `${REGISTRY}/blerg-*:${TAG}` —
not just your workstation being able to push to it. `deploy.sh`'s
`REGISTRY=localhost:32000` default is only meaningful for MicroK8s's
built-in registry add-on — set `REGISTRY` in `.env` to whatever's actually
right for your cluster. Two common setups:

- **A distro's built-in registry add-on** (e.g. MicroK8s: `microk8s enable
  registry` — exposes `localhost:32000` on every node automatically,
  pre-trusted, zero extra config; since `localhost:32000` is only that
  registry *on a node*, run `deploy.sh` on a cluster node; verify with `microk8s ctr images ls | grep
  blerg` after a build). Other distros may offer something similar or
  nothing at all — check your distro's docs.
- **A LAN registry you already run**: if it's plain HTTP (no TLS) or has a
  self-signed cert, every node needs it added to its container runtime's
  insecure-registry / trusted-CA config (containerd: `/etc/containerd/
  config.toml`'s `[plugins."io.containerd.grpc.v1.cri".registry.mirrors]` and
  a runtime restart) — this is a node-level, cluster-tool-specific step
  outside this directory's scope; consult your registry's and your cluster
  distro's docs. Skipping it produces `ImagePullBackOff` on every pod, not
  an error from `deploy.sh` itself (which only builds/pushes from your
  workstation and never touches node pull config). If the registry needs
  authentication, `docker login` on your workstation covers the push; the
  nodes need their own pull credentials.

### Ingress controller (check 6, expanded)

`ingress.yaml` hardcodes `ingressClassName: nginx` and three
`nginx.ingress.kubernetes.io/*` annotations: `proxy-read-timeout` and
`proxy-send-timeout` of `3600` (needed because the runner's
`/ws/daemon`/`/ws/browser` are long-lived WebSocket connections, not short
HTTP requests), and `proxy-body-size: 128m` (nginx's default 1m cap breaks file
uploads, which the runner takes up to 100 MiB, and the daemon's agent-config
bundle upload). If your cluster's controller is Traefik
(k3s's default) or anything else:

- Change `ingressClassName: nginx` to match (`kubectl get ingressclass`
  shows the installed name(s)).
- Translate the `nginx.ingress.kubernetes.io/*` annotations to your
  controller's equivalent — without a long timeout, a WebSocket connection
  idle for the controller's default (often 60s) gets silently killed, which
  shows up as the runner daemon or browser disconnecting/reconnecting in a
  loop, not a clean error; without a larger body limit, agent-config uploads
  fail with `413`.
- Confirm your controller actually proxies WebSocket upgrades — ingress-nginx
  does this by default with no extra annotation; verify anything else does
  too before assuming a disconnect is a Blerg bug.

If your distro ships ingress-nginx directly or via an add-on (e.g. MicroK8s:
`microk8s enable ingress`), the manifest's `ingressClassName: nginx` default
needs no edits. k3s ships Traefik by default instead — edit as above.
kubeadm/vanilla clusters usually have no ingress controller at all until you
install one.

## What `deploy.sh` does

```sh
cd install/k8s
cp -n .env.example .env
$EDITOR .env          # set REGISTRY to your own registry, and DOMAIN if you want something other than blerg.local
./deploy.sh
```

1. Builds `blerg-core`, `blerg-board`, `blerg-runner` from repo-root Docker
   contexts (same Dockerfiles as the desktop stack: `core/Dockerfile`,
   `board/Dockerfile`, `runner/Dockerfile.server`) — plus
   `blerg-runner-agent` (`runner/Dockerfile.devcontainer`) unless
   `CLUSTER_RUNTIME=off` — and pushes them to `${REGISTRY}`.
2. Creates the `blerg` namespace if missing.
3. Generates the `blerg-secrets` Secret (`kubectl create secret ... --dry-run=client -o yaml | kubectl apply -f -`)
   — every credential/token is: an explicit `SECRET_*` override, else
   whatever value is already live in the cluster's `blerg-secrets` Secret,
   else (only on a genuinely first deploy) random. Optional tokens
   (`ANTHROPIC_API_KEY`, `BLERG_CORE_GITHUB_TOKEN`,
   `BLERG_RUNNER_GITHUB_TOKEN`) are omitted from the Secret entirely when
   empty, rather than set to `""`, so the deployments' `optional: true`
   `secretKeyRef`s actually mean something. Values reach `kubectl` through
   private (mode 0600) temporary files that are removed on exit, never as
   command-line arguments that other local users could read from the process
   list. Nothing secret is ever
   committed to git; `secret.example.yaml` is a template, not applied.
4. Sets up **cluster runtime** (on by default): creates the
   `blerg-runner-sessions` namespace and the `blerg-runner-agent` Secret in it
   (the daemon token plus any optional shared credential you pinned; skipped
   when `AGENT_SECRET_EXTERNAL=1`), and later applies `cluster-runtime.yaml`
   (the namespace + its RBAC) directly rather than through the kustomization.
   `CLUSTER_RUNTIME=off` skips all of it. No human-supplied secret is needed
   either way — see [`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md).
5. Creates the runner's data claim `blerg-runner-data` (`runner-data-pvc.yaml`,
   `RUNNER_DATA_SIZE`, optional `RUNNER_DATA_STORAGE_CLASS`) if it does not
   exist yet — before the Deployment that mounts it. An existing claim is left
   exactly as it is, and `deploy.sh` never deletes it; it is not in the
   kustomization. See [Runner data volume](#runner-data-volume).
6. Renders only the files `kustomization.yaml` lists — substituting
   `${REGISTRY}` / `${TAG}` / `${DOMAIN}` / `${SCHEME}` / `${AGENT_IMAGE}` /
   `${WEBHOOK_ALLOW_PRIVATE}` in a temp copy with `envsubst` — then
   `kubectl apply -k <rendered temp dir>`. `AGENT_IMAGE` is
   `${REGISTRY}/blerg-runner-agent:${TAG}`, or empty with `CLUSTER_RUNTIME=off`.
7. Restarts the runner (`kubectl -n blerg rollout restart
   deploy/blerg-runner`) unconditionally, then waits for `kubectl -n blerg
   rollout status` on all three Deployments (timeout 180 s). The restart
   matters on re-runs: the runner reads its ConfigMap only at process start,
   and a re-run with an unchanged `TAG` leaves the pod spec byte-identical, so
   `apply -k` would update `blerg-config` without anything picking the new
   values up.
8. With `CLUSTER_ISSUER` set, annotates `ingress/blerg` with
   `cert-manager.io/cluster-issuer` and sets its `spec.tls` to the three hosts
   and Secret `blerg-tls`; without it, removes both (see [TLS](#tls)).
9. Prints the cluster-runtime state and the first-login commands.

`core-deployment.yaml` sets `BLERG_CORE_BEHIND_PROXY=true` because core is
only ever reached through the ingress here, and ingress-nginx *overwrites*
`X-Forwarded-For` with the real client address (it does not append to a
caller-supplied value), so trusting the header cannot be spoofed. It affects
only session attribution (`human_sessions.ip`, shown as "where you're logged
in"), never authorization — without it every login would be recorded as the
ingress pod's IP. If you front the ingress with another proxy or a
controller that appends rather than replaces `X-Forwarded-For`, verify its
behavior before keeping this on; see `core/docs/CONFIG.md`.

The runner's public MCP gateway route (`/mcp-gw/`, for sessions on workstation
daemons) limits failed attempts per client. `deploy.sh` renders
`BLERG_RUNNER_TRUSTED_PROXIES` from `TRUSTED_PROXIES` in `.env` (default: the
private ranges where pod networks live, RFC 1918 and IPv6 `fc00::/7`), and the
runner believes `X-Forwarded-For` only from those peers, taking the right-most
hop that is not one of them. Any pod in a trusted range, session pods included
(they must reach the runner's port), can claim another client's address. That
evades the failure limit and costs database lookups; it grants no access. To
exclude session pods, list only the ingress controller pods' own addresses (or
the node addresses, if it uses host networking); a range that also holds session
pods, such as the cluster's pod CIDR on most network plugins, does not. Set
`TRUSTED_PROXIES=` (empty) to trust none; every client then shares the
ingress's allowance, and a flood of failures from anywhere can make valid tokens
wait.

### Re-running deploy.sh

Re-running `deploy.sh` (e.g. to roll out a new `TAG`) is safe and reuses
every value already live in `blerg-secrets` — it never rotates a value out
from under Postgres or a component that already has it on disk. A
`SECRET_*` override in `.env` replaces the corresponding value on that run;
leaving it unset just re-applies what's already there. Random values are
only ever generated on a genuinely first deploy (no existing Secret to read
from).

**Always bump `TAG`** when you want a new build to actually roll out. The
Deployments' pod specs only change when the image reference does, so with an
unchanged `TAG` nothing but the runner restarts — `deploy.sh` will build,
push, and apply, but core and board keep running the old build. With a fixed
tag other than `latest`, the default `imagePullPolicy` (`IfNotPresent`) also
means a node that already has that tag never re-pulls it, so even the
restarted runner keeps the old image.

### Runner data volume

The runner keeps the bytes of every file a session published (`blerg-runner
publish`) or a person attached, uploaded agent-config bundles and mockups under
`/data` (`BLERG_RUNNER_DATA_DIR`); its database only indexes them. `/data` is the
PersistentVolumeClaim `blerg-runner-data` (`ReadWriteOnce`, `10Gi` by default),
so those files survive restarts and deploys.

- **Size and class.** Set `RUNNER_DATA_SIZE` (a quantity such as `20Gi`) and,
  when the cluster has no default StorageClass or you want another,
  `RUNNER_DATA_STORAGE_CLASS=<name>` (`kubectl get storageclass` lists them) in
  `.env` **before the first deploy that creates the claim**. On an existing
  claim both are ignored: a bound claim's class cannot change, and it can only
  grow if its StorageClass has `allowVolumeExpansion: true` — then
  `kubectl -n blerg patch pvc blerg-runner-data -p '{"spec":{"resources":{"requests":{"storage":"20Gi"}}}}'`.
  A size request is not a hard quota on host-path provisioners.
- **How much a session holds.** By default one session's agent may publish
  1 GiB of files and be sent 500 MiB of attachments, at most 100 MiB a file
  (`BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB`, `BLERG_RUNNER_MAX_SESSION_UPLOADS_MB`,
  `BLERG_RUNNER_MAX_FILE_MB` in `configmap.yaml`). Size the claim for the
  sessions you keep, or lower the limits.
- **Recreate, not rolling.** A `ReadWriteOnce` volume is mounted by one node at
  a time, so the runner Deployment uses the `Recreate` strategy: a rolling
  update would start the new pod before stopping the old one, and on another
  node it would wait forever for the volume. A deploy therefore stops the
  runner for a few seconds; browsers and daemons reconnect by themselves.
- **Ownership.** The pod sets `fsGroup: 65532` (the runner's user), so a
  freshly provisioned, root-owned volume is writable — on volume types with
  ownership management (most CSI and block volumes). In-tree hostPath volumes,
  such as MicroK8s' `microk8s-hostpath`, ignore `fsGroup`. If the runner's log
  shows `permission denied` under `/data`, fix the claim once from a helper pod
  while the runner is stopped (look first: `ls -lan` shows the owner; chown
  only if it is not `65532`):

  ```sh
  kubectl -n blerg scale deploy/blerg-runner --replicas=0
  kubectl -n blerg wait --for=delete pod -l app=blerg-runner --timeout=120s
  kubectl -n blerg run runner-data-fix --rm -i --restart=Never --image=busybox:1.36 --overrides='{"spec":{"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"blerg-runner-data"}}],"containers":[{"name":"runner-data-fix","image":"busybox:1.36","command":["ls","-lan","/data"],"volumeMounts":[{"name":"d","mountPath":"/data"}]}]}}'
  # only if that shows another owner — the same command with:  "command":["chown","-R","65532:65532","/data"]
  kubectl -n blerg scale deploy/blerg-runner --replicas=1
  ```

**Upgrading an existing install.** Before this change `/data` was an
`emptyDir`, emptied on every runner restart. The first `deploy.sh` run after
the upgrade creates the claim **empty** — there is nothing to migrate: the old
pod's files are gone already (or go with it). Files published or attached
before the upgrade stay listed in each session's Files tab, marked "no longer
available", and cannot be opened or downloaded; delete them there if you like.
Everything published from then on is kept.

### Rolling back an upgrade

Before an upgrade, record what is running (`kubectl -n blerg get deploy -o
wide`, and `kubectl -n blerg get configmap blerg-config -o
jsonpath='{.data.BLERG_RUNNER_AGENT_IMAGE}'`) and take a [backup](#back-up).
Database migrations only run forward, so an older build may not start
against a database a newer one has migrated; then the backup is the way back.
Migrations 041, 042 and 049 each build an index on `agent_events` at the runner's
first start, which holds writes to that table (daemon ingest waits) for as
long as the build takes; a slow first start is not a hang.

`kubectl rollout undo` does **not** roll back a `deploy.sh` upgrade: the
runner is restarted after the apply, so its previous revision already runs
the new image, and the agent image in `blerg-config` stays on the new tag.
Instead, preferably check out the commit the old tag was built from, set
`TAG=<OLD_TAG>` in `.env` and re-run `deploy.sh` — that restores images and
ConfigMap together. Or, without rebuilding:

```sh
kubectl -n blerg patch configmap blerg-config --type merge \
  -p '{"data":{"BLERG_RUNNER_AGENT_IMAGE":"<REGISTRY>/blerg-runner-agent:<OLD_TAG>"}}'   # cluster runtime only
for c in blerg-core blerg-board blerg-runner; do
  kubectl -n blerg set image deploy/$c $c=<REGISTRY>/$c:<OLD_TAG>
done
kubectl -n blerg rollout restart deploy/blerg-runner
kubectl -n blerg rollout status deploy/blerg-core deploy/blerg-board deploy/blerg-runner --timeout=180s
```

and set `TAG=<OLD_TAG>` in `.env` so the next run does not re-apply the newer
tag.

### Deploying without deploy.sh

You can apply the pieces by hand too:

```sh
kubectl create namespace blerg
kubectl apply -f secret.example.yaml   # after editing it with real values — do not commit your copy
RUNNER_DATA_SIZE=10Gi envsubst '${RUNNER_DATA_SIZE}' < runner-data-pvc.yaml | kubectl apply -f -   # once, before the runner; add storageClassName under spec if needed
kubectl apply -k .                      # applies with the ${...} placeholders literal, unless you envsubst first
kubectl apply -f cluster-runtime.yaml   # cluster runtime only; never through the kustomization
```

If you skip `envsubst`, the images/ingress host fields will literally read
`${REGISTRY}/blerg-core:${TAG}` / `${DOMAIN}` — harmless for schema
validation (`kubectl apply -k . --dry-run=client` still succeeds) but wrong
for a real deploy, so render them first, with the same six variables
`deploy.sh` substitutes:

```sh
REGISTRY=localhost:32000 TAG=latest DOMAIN=blerg.local SCHEME=http \
AGENT_IMAGE=localhost:32000/blerg-runner-agent:latest WEBHOOK_ALLOW_PRIVATE=false \
  bash -c 'for f in *.yaml; do envsubst "\${REGISTRY} \${TAG} \${DOMAIN} \${SCHEME} \${AGENT_IMAGE} \${WEBHOOK_ALLOW_PRIVATE}" < "$f" > "$f.out"; mv "$f.out" "$f"; done'
```

(`SCHEME=http` or `https` — see "TLS" below; `AGENT_IMAGE=` empty turns
cluster runtime off; do this on a scratch copy of the directory, not your git
checkout.) With cluster runtime on you also need the `blerg-runner-agent`
Secret — see `cluster-agent-secret.example.yaml`.

## TLS

Setting `CLUSTER_ISSUER=<cert-manager ClusterIssuer>` in `.env` does two
things on the next `deploy.sh` run: it requests a real TLS certificate for
`${DOMAIN}`/`board.${DOMAIN}`/`runner.${DOMAIN}` via cert-manager's
ingress-shim (annotating `ingress/blerg` and setting `spec.tls` with Secret
`blerg-tls`), and it flips `SCHEME` to `https`, which is templated into every
BLERG_CORE origin/audience/public-URL value across `configmap.yaml` and
`core-deployment.yaml` so they match what browsers actually see.

The certificate is issued after `deploy.sh` finishes; until it is, the
ingress serves the controller's default certificate. Watch it with
`kubectl -n blerg get certificate,order,challenge` — `blerg-tls` should reach
`READY True` (DNS-01 challenges can take minutes to propagate).

Unsetting `CLUSTER_ISSUER` and re-running `deploy.sh` removes the
`cert-manager.io/cluster-issuer` annotation and `spec.tls` again — the TLS
branch is idempotent in both directions, so toggling it back and forth is
safe.

If TLS is terminated by something other than cert-manager (a fronting
proxy, an external load balancer), leave `CLUSTER_ISSUER` unset and set
`SCHEME=https` directly in `.env` instead. Its certificate must cover all
three hosts.

**Nested domains.** With `DOMAIN=blerg.example.com` the other two hosts are
`board.blerg.example.com` and `runner.blerg.example.com`: you need a DNS
record for each (or `blerg.example.com` plus a wildcard
`*.blerg.example.com`), and a certificate from an external terminator must
cover them — a `*.example.com` wildcard does not. The cert-manager path
requests all three names explicitly.

**The refresh cookie is `Secure`.** Browsers only send a `Secure` cookie
over HTTPS (with a narrow exception for `http://localhost` that Safari does
not honor), so plain `SCHEME=http` only actually works when you're browsing
at `localhost` — anywhere else, set up TLS (`CLUSTER_ISSUER` or an external
terminator) or login/refresh will silently fail. See
[`core/docs/CONFIG.md`](../../core/docs/CONFIG.md).

## First login

A fresh install has no account. Core is in first-run setup mode until the
first account exists: the person who will own the install opens the setup
link and creates the admin by choosing a username and password at
`https://<DOMAIN>/setup`. Nothing to find in a log, and no forced password
change afterwards.

The link carries a single-use setup code (in the `#` fragment, so it never
reaches an access log): it works once, for 24 hours, and only while no
account exists. `deploy.sh` prints it at the end of a fresh install when its
output is a terminal; otherwise it prints only the command below and mints
nothing. Core never logs a link. For a fresh one at any time (the previous
link stops working):

```sh
kubectl -n blerg exec deploy/blerg-core -- /blerg-core setup-link
```

It prints the link alone, or, once an account exists, says setup is complete
and exits 1. **Until the first account exists, the setup link is the admin
account:** anyone holding it can create it. Treat it like a password, and treat
`kubectl exec` access to the `blerg` namespace accordingly. If a link stops
working (it expired, or someone ran the command again), get a fresh one with
the command above. Opened without a code, the sign-in page says the install
isn't set up yet.

Once the first account exists, `/setup` is closed for good. A forgotten
password: mint a one-time one (this also forces a change at the next sign-in
and signs the account out everywhere):

```sh
kubectl -n blerg exec deploy/blerg-core -- /blerg-core users set-password --subject <username>
```

It prints one line, `username=<username> password=<one-time>`. If you
no longer know the admin username either, look it up:

```sh
kubectl -n blerg exec postgres-0 -- psql -U blerg -d blerg_core -tAc \
  "select provider_subject from accounts where provider='local' and role='admin' order by created_at limit 1"
```

**Adding people.** Create an account with a one-time password and hand it
over; they are asked to change it at first login:

```sh
kubectl -n blerg exec deploy/blerg-core -- /blerg-core users create --subject alice --role member
```

(`--role admin` for another admin.) The full operator CLI is in
[`core/docs/CONFIG.md`](../../core/docs/CONFIG.md) ("Operator CLI").

## Reaching the stack

Point `${DOMAIN}` (default `blerg.local`) and `board.${DOMAIN}` /
`runner.${DOMAIN}` at your ingress controller's address — either via
your own DNS, or per-host `/etc/hosts` entries on any machine you'll browse
from:

```
<ingress-controller-ip>  blerg.local board.blerg.local runner.blerg.local
```

Then (`https://` once `CLUSTER_ISSUER` is set, `http://` otherwise):
- **`http(s)://blerg.local/`** — blerg-core's landing page (shows the
  registered tooling, same as `:8081` on desktop). Settings, including
  personal credentials and agent tokens, is at `/settings`.
- **`http(s)://board.blerg.local/`** — the BOARD UI (same as `:8082` on
  desktop).
- **`http(s)://runner.blerg.local/`** — the RUNNER UI and server (same as
  `:8083` on desktop): launch and watch sessions here; it is also what the
  workstation daemon talks to.

If your cluster's ingress controller isn't ingress-nginx (e.g. k3s's
default Traefik), edit `ingressClassName: nginx` in `ingress.yaml` to match
before deploying (or install ingress-nginx alongside/instead).

### For agents and tools

An automated tool should start at `http(s)://${DOMAIN}/agents` — core's manifest of the whole
install. With `Accept: text/markdown` it reads as a guide for an LLM; as JSON it lists every
component with its base URL, auth and operations, and `http(s)://${DOMAIN}/openapi.json` sits
beside it. Both are public, so a tool can read them before it holds any credential. To get one,
sign in and use **Settings → Agent tokens** with the `run-sessions` preset, then send it as
`Authorization: Bearer <token>` to the runner — REST or MCP, documented in
[`runner/README.md`](../../runner/README.md#api-contract-v1). The runner publishes its own
`/agents` and `/openapi.json` at `http(s)://runner.${DOMAIN}/` too.

## Verify

Run from any machine that resolves the three hosts (`https` shown; use
`http` without TLS).

```sh
kubectl -n blerg get pods
```

```
NAME                            READY   STATUS    RESTARTS   AGE
blerg-board-6c9f7d8b5-x2k4p     1/1     Running   0          3m
blerg-core-7b5d9c6f4-q8m2n      1/1     Running   0          3m
blerg-runner-5f8b7c9d6-z7w3r    1/1     Running   0          2m
postgres-0                      1/1     Running   0          3m
```

(A restart or two on board/runner in the first seconds is normal — see
[Troubleshooting](#troubleshooting).)

```sh
curl -s https://<DOMAIN>/healthz          # {"status":"ok"}
curl -s https://board.<DOMAIN>/healthz    # ok
curl -s https://runner.<DOMAIN>/healthz   # ok
curl -s https://<DOMAIN>/agents | jq -r '.contract_version, .components[].name'
```

The last command prints `v1`, then `blerg-core`, then `blerg-board` and
`blerg-runner` once they have registered with core (seconds after start).

Cluster runtime, without credentials:

```sh
kubectl -n blerg logs deploy/blerg-runner | grep 'cluster agent runtime enabled'
kubectl -n blerg-runner-sessions get secret blerg-runner-agent
kubectl -n blerg-runner-sessions get rolebinding blerg-runner-agent-launcher
```

The runner's `GET /api/cluster/status` needs a login: open the runner's
**Cluster** tab in a browser, or call it with a `run-sessions` agent token —
expected output and what each field means are in
[`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md#verify):

```sh
curl -s -H "Authorization: Bearer <token>" https://runner.<DOMAIN>/api/cluster/status | jq
```

Finally, log in through a browser ([First login](#first-login)) and launch a
session ([step 5](#5-add-credentials-and-run-a-session)).

## Where sessions run

Sessions execute in one of two places, and both can be live at once:

- **Cluster pods — the default.** The in-cluster runner server creates a
  throwaway Kubernetes Job per session in the `blerg-runner-sessions`
  namespace. `deploy.sh` sets this up on every run (`CLUSTER_RUNTIME=off` to
  skip it), and it needs no operator-supplied credential: each user adds their
  own Claude/Codex/Hermes and GitHub credentials in Settings (for Codex, also
  a managed connection: a device login under Settings → Managed connections),
  and sessions run as the user who launched them. A new account-owned Claude or
  Codex session keeps its native conversation on its own PVC, so a replacement
  pod resumes it. Details, the secrets contract and verification:
  [`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md).
- **A workstation daemon — optional.** A human's own machine drives their
  `claude`/`codex`/`hermes` CLI in tmux — the only way to run a Terminal
  session, which a Job pod cannot host. Set-up below.

On a cluster install the runner's launch sheet defaults to **Cluster pod** +
**Agent** even when a workstation daemon is connected; the daemon's Local
sandbox and unsandboxed host runtimes stay one click away in the same Run
column. See [`runner/README.md`](../../runner/README.md#runtimes) for the full
runtime × kind table.

### Connecting the workstation daemon

Exactly as on desktop: **the in-cluster `blerg-runner` is the server only.**
The actual agent-session daemon — the process that drives your `claude` CLI
in tmux — is not containerized and does not run in the cluster. Run it on
your workstation, pointed at the in-cluster runner's `/ws/daemon` endpoint
through the Ingress. In the foreground, from a checkout of this repository
(Go ≥ 1.26):

```sh
export BLERG_RUNNER_SERVER_URL=wss://runner.<DOMAIN>/ws/daemon
export BLERG_RUNNER_DAEMON_TOKEN="$(kubectl -n blerg get secret blerg-secrets -o jsonpath='{.data.BLERG_RUNNER_DAEMON_TOKEN}' | base64 -d)"
export BLERG_RUNNER_REPOS_ROOT="$HOME/repositories"
( cd runner && GOWORK=off go run ./cmd/daemon )
```

(`ws://` instead of `wss://` if you're running without TLS.) The daemon
reads `BLERG_RUNNER_SERVER_URL` directly; `BLERG_RUNNER_REPOS_ROOT` is where
its session checkouts live. Every daemon variable is listed in
[`runner/README.md`](../../runner/README.md) ("Desktop daemon").

To run it as a persistent service instead (systemd `--user` on Linux,
launchd on macOS), use the desktop installer with those same values — no
desktop compose stack is needed on the workstation, only these three lines
in `install/desktop/.env` (git-ignored; keep it `0600`):

```sh
cat > install/desktop/.env <<EOF
BLERG_RUNNER_SERVER_URL=wss://runner.<DOMAIN>/ws/daemon
BLERG_RUNNER_DAEMON_TOKEN=<the value in blerg-secrets>
BLERG_RUNNER_REPOS_ROOT=$HOME/repositories
EOF
chmod 600 install/desktop/.env
(cd install/desktop && ./daemon/install.sh install)
```

See [`install/desktop/DAEMON.md`](../desktop/DAEMON.md) for what the
service needs (engine login, tmux) and how to check its logs.

When you upgrade the cluster, update the daemon too: `git pull` the same
commit on the workstation and run `(cd install/desktop && ./daemon/install.sh install)`
again. Much
of what a session shows comes from the daemon, and a session left with a
revoked messaging token is given a new one only when both are updated.

Fetch the daemon token with (it prints the secret — agents: don't run this):

```sh
kubectl -n blerg get secret blerg-secrets -o jsonpath='{.data.BLERG_RUNNER_DAEMON_TOKEN}' | base64 -d
```

Until a daemon is connected the runner's launch sheet lists no repositories
from your workstation — with cluster runtime on, it says sessions will run as
cluster pods instead. That message is about your workstation, not the cluster.

## Completion webhooks on a private network

Session completion webhooks refuse callback targets that resolve to private,
link-local or loopback addresses (SSRF guard). On an install whose public
hostnames resolve to a private network — a homelab behind NAT — that refuses
every callback into that network. Set `WEBHOOK_ALLOW_PRIVATE=1` in `.env`
(rendered into the runner's `BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE`) and re-run
`deploy.sh` to allow them.

## Secrets

Two Secrets, with stable names and keys — that contract is the whole
integration surface, so any external secret manager can fill them:

| Secret | Namespace | What it holds |
|---|---|---|
| `blerg-secrets` | `blerg` | The base stack's database URLs, signing/internal keys and tokens. Generated by `deploy.sh`; `secret.example.yaml` documents the shape. |
| `blerg-runner-agent` | `blerg-runner-sessions` | What cluster session pods read: `BLERG_RUNNER_DAEMON_TOKEN` (required, same value as `blerg-secrets`') plus five optional shared engine/git credentials. |

Neither needs a human to type a credential into a file. Personal credentials
live in core's encrypted per-user vault (Settings), not in either Secret —
encrypted with `BLERG_CORE_LOCAL_KEY` from `blerg-secrets`.

**Do not delete `blerg-secrets`** to "reset" it: the next `deploy.sh` run
then generates every value afresh, which breaks Postgres authentication
against the existing data directory and makes every stored credential
unreadable.

Set `AGENT_SECRET_EXTERNAL=1` in `.env` to own `blerg-runner-agent` yourself —
`deploy.sh` then never touches it. The full key-by-key contract, and a complete
External Secrets Operator example, are in
[`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md)'s "Operator secrets contract" and
"Bring your own secret manager".

## Backup, restore and uninstall

The same procedure for both installs, with a restore drill checklist, is in
[`docs/backup-and-restore.md`](../../docs/backup-and-restore.md).

### What is stateful

| What | Where | If you lose it |
|---|---|---|
| Postgres data: accounts, boards, sessions, encrypted credentials | PVC `postgres-data-postgres-0` in `blerg` (`5Gi`) | Everything. |
| `blerg-secrets`, above all `BLERG_CORE_LOCAL_KEY` | Secret in `blerg` | **Losing `BLERG_CORE_LOCAL_KEY` loses every stored credential** — there is no recovery; users must re-enter them. Losing `POSTGRES_PASSWORD` locks the components out of the database. |
| Runner data (uploaded agent-config bundles, published mockups, files agents published with `blerg-runner publish`, files people attached) | PVC `blerg-runner-data` in `blerg` (`10Gi`), at `/data` in the runner pod | Those files: they stay listed in the Files tab, marked "no longer available", but cannot be opened or downloaded. Back it up with a volume snapshot, or copy `/data` from a helper pod that mounts the claim while the runner is scaled to 0 (the runner image has no shell or `tar`). |
| Retained native conversations (account-owned Claude and Codex Cluster sessions) | one PVC per session in `blerg-runner-sessions` (`1Gi` each, `BLERG_RUNNER_NATIVE_HISTORY_*` in `blerg-config`), reclaimed seven days after the session ends | That session's native conversation: replacing its pod then fails explicitly instead of resuming. See [`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md#retained-native-conversations). |
| `blerg-runner-agent` | Secret in `blerg-runner-sessions` | Recreated by the next `deploy.sh` run (daemon token copied from `blerg-secrets`; optional keys only if pinned in `.env`). |

Session Jobs, their per-session Secrets, and the images are disposable.

### Back up

```sh
umask 077
for db in blerg_core blerg_board blerg_runner; do
  kubectl -n blerg exec postgres-0 -- pg_dump -U blerg -d "$db" > "$db.sql"
done
kubectl -n blerg get secret blerg-secrets -o json \
  | jq 'del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.managedFields, .metadata.annotations)' \
  > blerg-secrets.json
```

`blerg-secrets.json` holds every secret of the install in base64 — store it as
carefully as a password vault, never in git.

### Restore

On a fresh install, restore the Secret **before** the first `deploy.sh` run
(which then reuses it), and the databases before anyone uses the stack:

```sh
kubectl create namespace blerg
kubectl apply -f blerg-secrets.json
./deploy.sh
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner --replicas=0
kubectl -n blerg wait --for=delete pod -l 'app in (blerg-core,blerg-board,blerg-runner)' --timeout=120s
for db in blerg_core blerg_board blerg_runner; do
  kubectl -n blerg exec postgres-0 -- psql -U blerg -d postgres -c "DROP DATABASE IF EXISTS $db" -c "CREATE DATABASE $db"
  kubectl -n blerg exec -i postgres-0 -- psql -U blerg -d "$db" < "$db.sql"
done
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner --replicas=1
```

Rehearse this before you need it.

### Uninstall

Stop everything but keep the data:

```sh
kubectl -n blerg scale deploy/blerg-core deploy/blerg-board deploy/blerg-runner statefulset/postgres --replicas=0
```

(`./deploy.sh` brings it back.) Remove it completely — **this deletes the
Postgres PVC, the runner data PVC and `blerg-secrets`, including `BLERG_CORE_LOCAL_KEY`; back up
first, and there is no undo**:

```sh
kubectl delete namespace blerg-runner-sessions
kubectl delete namespace blerg
```

Whether the underlying volume is erased depends on your StorageClass's
reclaim policy (`Delete` is the usual default). The images stay in your
registry until you remove them there.

## Postgres notes

This ships a single `postgres:16` StatefulSet with one PVC (`5Gi` default —
adjust `postgres-statefulset.yaml`'s `volumeClaimTemplates` request for your
workload), creating three logical databases (`blerg_core`, `blerg_board`,
`blerg_runner`) all owned by the same user, via the init script in
`postgres-init-configmap.yaml` — this is the in-cluster equivalent of
`install/desktop/init-multi-db.sh`. It is not highly-available and has no
automatic backup; see [Back up](#back-up) — fine for a small self-hosted
deploy.

**External Postgres option:** if you'd rather point at a Postgres you
already run in your cluster, skip `postgres-statefulset.yaml` and
`postgres-init-configmap.yaml` entirely (remove them from
`kustomization.yaml`'s `resources`), create the three databases yourself,
and set `DATABASE_URL_CORE` / `DATABASE_URL_BOARD` /
`BLERG_RUNNER_DATABASE_URL` in your `blerg-secrets` Secret to point at your
existing instance instead of the in-cluster `postgres` Service. **Note:**
`deploy.sh` rewrites those three keys to the in-cluster `postgres` Service on
every run, so with an external database either apply the manifests yourself
([Deploying without deploy.sh](#deploying-without-deploysh)) or re-patch the
Secret and restart the three Deployments after each `deploy.sh` run.

## Troubleshooting

| Symptom | Check | Fix |
|---|---|---|
| `jwks fetch failed` / `register ... 401` in board's or runner's logs for about the first 15 seconds after a fresh deploy or a core rollout | `kubectl -n blerg logs deploy/blerg-core` | Expected, not a bug: core runs its database migrations on boot before it can serve `/.well-known/jwks` or accept registration, and board/runner race to reach it. Both retry and settle; if the errors persist past ~15–20 s, look for an actual migration failure in core's log. |
| Pods stuck in `ImagePullBackOff` / `ErrImagePull` | `kubectl -n blerg describe pod <pod>` (Events) | Nodes cannot pull from `REGISTRY`: fix the registry address, or the nodes' containerd trust/insecure-registry config, or their pull credentials ([Image registry](#image-registry-check-5-expanded)). |
| `postgres-0` `Pending`; core never becomes ready | `kubectl -n blerg get pvc` shows `Pending`; `kubectl get storageclass` has no `(default)` | Install or mark a default StorageClass. On Kubernetes ≥ 1.28 the class-less `Pending` claim picks up the new default by itself — wait a minute first. If it stays `Pending`, delete both the claim (it holds no data yet) and the pod so the StatefulSet recreates them: `kubectl -n blerg delete pvc/postgres-data-postgres-0 pod/postgres-0` (repeat the pod delete if the new pod caught the terminating claim). |
| `blerg-runner-…` `Pending`; `deploy.sh` times out on the runner | `kubectl -n blerg get pvc blerg-runner-data` shows `Pending`; `kubectl -n blerg describe pvc blerg-runner-data` (Events) | No default StorageClass, or the named one does not exist. Set `RUNNER_DATA_STORAGE_CLASS`; while the claim is still `Pending` (it holds no data yet), stop the runner and delete it, waiting until it is gone: `kubectl -n blerg scale deploy/blerg-runner --replicas=0`, `kubectl -n blerg delete pvc blerg-runner-data --wait=false`, `kubectl -n blerg wait --for=delete pvc/blerg-runner-data --timeout=120s`. Then re-run `deploy.sh`, which recreates the claim and scales the runner back to 1 ([Runner data volume](#runner-data-volume)). |
| The Files tab marks files "no longer available"; opening one answers `410` | Were they published before the runner data volume existed, or was the claim deleted? | Their bytes are gone; nothing brings them back. Delete them from the Files tab. New files are kept ([Runner data volume](#runner-data-volume)). |
| Pods `CrashLoopBackOff` with `exec format error` | `kubectl get nodes -o jsonpath='{.items[*].status.nodeInfo.architecture}'` vs. `docker version --format '{{.Server.Arch}}'` | Images were built for the wrong CPU architecture. Build on a matching machine and deploy with a new `TAG`. |
| `deploy.sh` times out at `rollout status` | `kubectl -n blerg get pods`, then `describe`/`logs` of the failing pod | Usually one of the rows above; the Postgres pod is not in that wait list, so check it too. |
| Every host answers `404` from the ingress | `kubectl get ingressclass`; `kubectl -n blerg get ingress blerg` shows no `ADDRESS` | The controller is not ingress-nginx or uses another class: edit `ingressClassName` and the annotations in `ingress.yaml` ([Ingress controller](#ingress-controller-check-6-expanded)). |
| Browser or daemon disconnects and reconnects in a loop; daemon uploads fail with `413` | `kubectl -n blerg get ingress blerg -o yaml` shows the `proxy-*-timeout` and `proxy-body-size` annotations | Your controller ignores the nginx annotations: set its own long WebSocket timeout (3600 s) and a 128m body limit (above the runner's per-file limit, `BLERG_RUNNER_MAX_FILE_MB`). |
| Login loops back to "Sign in required" | Is the page `http://` on a host other than `localhost`? | The refresh cookie is `Secure`: use TLS (`CLUSTER_ISSUER` or a terminator plus `SCHEME=https`) and re-run `deploy.sh` ([TLS](#tls)). |
| Endless redirect between board/runner and `/auth/refresh`, or refresh answers `400 invalid return_to` | Does the browser's scheme match `SCHEME`? `kubectl -n blerg get configmap blerg-config -o jsonpath='{.data.BLERG_CORE_PUBLIC_URL}'` | Set `SCHEME` to what browsers actually use and re-run `deploy.sh`. |
| Browser warns about the certificate; `blerg-tls` not ready | `kubectl -n blerg get certificate,order,challenge` | Wait for DNS-01 propagation (minutes); a failing `challenge` says why (DNS, issuer credentials, HTTP-01 not reachable). Check `kubectl get clusterissuer`. |
| One of `board.<DOMAIN>`/`runner.<DOMAIN>` does not resolve or fails TLS | `getent hosts board.<DOMAIN> runner.<DOMAIN>`; the terminator's certificate names | Add the missing DNS records (or `*.<DOMAIN>`) and a certificate covering all three hosts. |
| Components crash with Postgres `password authentication failed` | Was `SECRET_POSTGRES_PASSWORD` changed, or `blerg-secrets` deleted, after the first deploy? | The data directory keeps its first password. Put the original back (`SECRET_POSTGRES_PASSWORD=<old>` from your backup) and re-run `deploy.sh`. Never rotate it on a live PVC. |
| Stored credentials in Settings no longer work; sessions say to add a credential | Was `BLERG_CORE_LOCAL_KEY` changed or `blerg-secrets` deleted? | Restore the old key (`SECRET_BLERG_CORE_LOCAL_KEY` from your backup) and re-run `deploy.sh`; without it, users must re-enter their credentials. |
| A new build does not show up after `deploy.sh` | `kubectl -n blerg get deploy -o wide` shows the old image tag | Use a new `TAG` ([Re-running deploy.sh](#re-running-deploysh)). |
| `GET /api/cluster/status` reports `"configured": false` | `kubectl -n blerg get configmap blerg-config -o jsonpath='{.data.BLERG_RUNNER_AGENT_IMAGE}'` | Empty means `CLUSTER_RUNTIME=off`; if it is set, the runner cannot read its service-account token ([`CLUSTER-RUNTIME.md`](CLUSTER-RUNTIME.md) preflight). |
| `GET /api/cluster/status` reports `"active_sessions": -1` | `kubectl -n blerg logs deploy/blerg-runner`; `kubectl -n blerg-runner-sessions get rolebinding` | The RBAC in `cluster-runtime.yaml` is missing or in the wrong namespace: `kubectl apply -f cluster-runtime.yaml` (never through the kustomization). |
| Completion webhooks never arrive; runner log shows `callback address refused (private or loopback)` | Does the callback host resolve to a private address? | Set `WEBHOOK_ALLOW_PRIVATE=1` and re-run `deploy.sh`. |

## What's here

| File | Purpose |
|---|---|
| `namespace.yaml` | the `blerg` namespace |
| `configmap.yaml` | non-secret config (service URLs, postgres user/db names, cluster-runtime keys) |
| `secret.example.yaml` | **template only** — documents the `blerg-secrets` Secret shape; never applied directly |
| `postgres-init-configmap.yaml` | the multi-DB init script (same as desktop's `init-multi-db.sh`) |
| `postgres-statefulset.yaml` | Postgres 16 StatefulSet + headless Service + PVC |
| `core-deployment.yaml` | `blerg-core` Deployment + Service (`:8080`) |
| `board-deployment.yaml` | `blerg-board` Deployment + Service (`:8080`) |
| `runner-deployment.yaml` | `blerg-runner` Deployment (strategy `Recreate`) + Service (`:8080`) — server only, see "Connecting the workstation daemon" |
| `runner-data-pvc.yaml` | the runner's data claim `blerg-runner-data`, mounted at `/data`; created once by `deploy.sh`, not in the kustomization ([Runner data volume](#runner-data-volume)) |
| `ingress.yaml` | host-based routing under `${DOMAIN}` |
| `kustomization.yaml` | ties the above together (everything except the Secret, the runner data claim and `cluster-runtime.yaml`) |
| `deploy.sh` | build, push, generate secrets, apply |
| `deploy_test.sh` | renders the manifests the way `deploy.sh` does and checks the wiring; no cluster needed |
| `.env.example` | every setting `deploy.sh` reads |
| `AGENT-INSTALL.md` | the install as a runbook for an AI agent |
| `CLUSTER-RUNTIME.md` | cluster runtime (on by default) — ephemeral per-session agent Jobs, the operator secrets contract, and how to turn it off |
| `cluster-runtime.yaml` | namespace + RBAC for cluster runtime; applied by `deploy.sh` directly, not via the kustomization (see CLUSTER-RUNTIME.md) |
| `cluster-agent-secret.example.yaml` | **template only** — the shape of the `blerg-runner-agent` Secret `deploy.sh` creates |
| `hermes-providers.example.yaml` | **template only** — a shared Hermes provider fragment for `HERMES_PROVIDERS_FILE`; not a manifest |
| `networkpolicy-sessions.example.yaml` | **optional, not applied** — session-pod egress policy, with an opt-in rule for local model servers |

Deployment images are referenced as `${REGISTRY}/blerg-core:${TAG}` etc.
These are placeholders substituted by `deploy.sh` at apply time. Nothing in
this directory hardcodes a real private registry, hostname, or IP.

## What's actually verified here

- These manifests have been applied to a live, self-hosted Kubernetes
  cluster running ingress-nginx and cert-manager, and the login flow has
  been exercised end to end through the Ingress: registering/logging in at
  core, being redirected into board and runner with a working
  `/auth/refresh`, and the first-login path above (at the time, a bootstrap admin password; first-run setup replaced it).
- `install/k8s/deploy_test.sh` renders the exact set of files
  `kustomization.yaml` lists the way `deploy.sh` does (`envsubst` with
  `REGISTRY`/`TAG`/`DOMAIN`/`SCHEME`/`AGENT_IMAGE`/`WEBHOOK_ALLOW_PRIVATE`),
  asserts every var this deploy path wires up is actually present in the
  rendered output — including the cluster-runtime ConfigMap keys, and that an
  empty `AGENT_IMAGE` (`CLUSTER_RUNTIME=off`) renders
  `BLERG_RUNNER_AGENT_IMAGE: ""` rather than a leftover placeholder — and,
  when `kubectl` is on `PATH` and pointed at a cluster, dry-runs the
  rendered manifests with `kubectl apply -k --dry-run=client` plus
  `cluster-runtime.yaml` on its own. It never touches a live cluster itself.
- Every manifest in this directory parses as valid YAML and the container
  images build from these exact Dockerfiles with repo-root contexts.
