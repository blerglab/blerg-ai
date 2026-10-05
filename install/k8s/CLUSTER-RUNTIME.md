# Cluster runtime (ephemeral per-session agent Jobs)

Written so an operator — or an LLM configuring this on someone's behalf — can
follow it start to finish without reading any Go source.

## What this is

Agent-kind sessions have to run *somewhere*. There are two places:

- **A workstation daemon** — the `blerg-runner` daemon running on a human's
  machine, driving their own `claude`/`codex`/`hermes` CLI in tmux. Optional;
  see the main [`README.md`](README.md)'s "Connecting the workstation daemon".
- **Cluster runtime** — the in-cluster runner server creates a throwaway
  Kubernetes `Job` per session, in its own namespace, running the
  `blerg-runner-agent` image with `blerg-runner` as PID 1. No human machine
  needs to stay online.

**Cluster runtime is on by default.** `deploy.sh` sets it up on every run
unless you turn it off, and it needs **no operator-supplied credential at all**
— each user adds their own in Settings (see "Personal credentials" below). The
two runtimes coexist; a session picks one in the launch sheet, which defaults
to the cluster runtime whenever no workstation daemon is connected.

### What `deploy.sh` already did for you

On every run, with `CLUSTER_RUNTIME` unset or `on`:

| Step | What it creates |
|---|---|
| Build + push the agent image | `${REGISTRY}/blerg-runner-agent:${TAG}`, built from `runner/Dockerfile.devcontainer` with the repo root as context — a *different* image from `blerg-runner:${TAG}` (the server) |
| Namespace + RBAC | Applies `cluster-runtime.yaml`: the `blerg-runner-sessions` namespace (every session Job runs there, not in `blerg`) and a Role/RoleBinding granting the existing `blerg-runner-agent-launcher` ServiceAccount exactly two things in it — manage `batch/jobs`, and `get, create, patch, delete` on `secrets` (the runner writes a per-session Secret for each session's credentials and prompt, owns it to the Job, and deletes it) |
| The agent Secret | `blerg-runner-agent` in `blerg-runner-sessions` — see "Operator secrets contract" |
| ConfigMap keys | In `blerg-config`: `BLERG_RUNNER_AGENT_IMAGE` (the agent image just built), `BLERG_RUNNER_AGENT_NAMESPACE: blerg-runner-sessions`, `BLERG_RUNNER_INTERNAL_URL: http://blerg-runner.blerg.svc.cluster.local:8080`, `BLERG_RUNNER_MAX_SESSIONS: "4"` |
| A runner restart | `kubectl -n blerg rollout restart deploy/blerg-runner`, unconditionally, after the apply — see below |

Two details worth knowing, because they are easy to get wrong by hand:

- **`cluster-runtime.yaml` is applied directly** (`kubectl apply -f`), *not*
  through the kustomization. The kustomization sets `namespace: blerg`, and
  kustomize's namespace transformer would rewrite that file's Role/RoleBinding
  — and the RoleBinding's ServiceAccount subject namespace — into `blerg`,
  breaking RBAC that only works scoped to the sessions namespace. The file
  contains no `${...}` placeholders, so it needs no rendering either.
- **`BLERG_RUNNER_INTERNAL_URL` must be set explicitly here.** Its built-in
  default names a different, older manifest set's Service
  (`blerg-runner-server` in `default`), not this install's Service
  (`blerg-runner` in `blerg`). With the wrong value, session pods spawn but
  their callback to the server resolves nowhere and the Job never reaches
  `running` — silently, with no spawn-time error. `configmap.yaml` pins the
  right value; leave it alone unless you renamed the Service.

- **The runner is restarted on every run.** Env-from-ConfigMap values are read
  once, at process start. A re-run with an unchanged `TAG` leaves the
  Deployment's pod spec byte-identical, so `kubectl apply -k` updates only the
  ConfigMap and nothing restarts — the runner would keep running with the old
  (or absent) cluster-runtime keys, and turning the runtime on or off would
  appear to do nothing. `deploy.sh` therefore always runs
  `kubectl -n blerg rollout restart deploy/blerg-runner` after the apply.

Re-running `deploy.sh` is idempotent: same namespace, same RBAC, same Secret
keys, a rebuilt image at whatever `TAG` you set, and a runner restarted so the
ConfigMap it just wrote actually takes effect.

## Turning it off

Set `CLUSTER_RUNTIME=off` in `install/k8s/.env` (or in the environment) and
re-run `deploy.sh`. That run skips the agent image build and push, skips
`cluster-runtime.yaml`, skips the agent Secret, and renders
`BLERG_RUNNER_AGENT_IMAGE` **empty** in the ConfigMap. An empty agent image is
the runner's off switch: `GET /api/cluster/status` reports
`"configured": false`, the launch sheet stops offering the cluster runtime, and
every other cluster-runtime ConfigMap key is inert. Sessions then only run on a
workstation daemon.

Turning it off does not delete anything an earlier run created — an existing
`blerg-runner-sessions` namespace, its RBAC and its Secret stay where they are,
unused. Remove them yourself if you want them gone.

`deploy.sh` prints which way it went on every run:

```
==> Cluster runtime: on (users add their own credentials at https://<DOMAIN>/settings)
```

## Personal credentials

Cluster sessions **run as the user who launched them**. When a session starts,
the runner fetches that account's credentials out of core's encrypted per-user
vault and writes them into the session's own Secret — so each person uses their
own subscription, their own billing, and their own git identity.

Users add credentials themselves at `<SCHEME>://<DOMAIN>/settings`. Each block
on that page explains how to produce the value:

| Kind | What to paste |
|---|---|
| `claude` | The token printed by `claude setup-token` on a machine where they're logged in (starts with `sk-ant-oat`), or an Anthropic console API key to bill an organisation instead |
| `codex` | The contents of `~/.codex/auth.json` from a machine where `codex login` has succeeded |
| `hermes` | The contents of `~/.hermes/.env` from a machine where Hermes is set up |
| `github` | A fine-grained personal access token with Contents read+write and Metadata read on the repos sessions may touch — sessions clone and push as that user |

A Claude value starting `sk-ant-oat` is treated as an OAuth (subscription)
token and injected as `CLAUDE_CODE_OAUTH_TOKEN`; anything else is treated as an
API key and injected as `ANTHROPIC_API_KEY`. The pod picks subscription vs.
metered mode from which of the two is set.

**Personal wins over the operator Secret.** For every key a user has in their
vault, the session pod gets the personal value; for the rest it falls back to
whatever the operator put in `blerg-runner-agent`. If neither exists for the
chosen engine, the launch sheet disables the launch button and says to add a
credential in Settings.

**Without a GitHub credential** — neither a personal one nor an operator
`BLERG_RUNNER_GIT_TOKEN` — the launch sheet shows a *warning*, not a block: the
session can clone public repositories and cannot push. `GET
/api/cluster/status` exposes this as `git_configured` (operator token present
or not); the launch sheet combines it with the caller's own credentials.

**GitLab.** A user who stores a GitLab token in Settings sees their own GitLab
projects in the launch sheet, and a session on one clones from gitlab.com with
that token. The operator's `BLERG_RUNNER_GIT_TOKEN` is never offered to a GitLab
clone — it belongs to the operator's git base (github.com by default) — so a
private GitLab repository needs the launcher's own GitLab token.

**Repository names.** `BLERG_RUNNER_AGENT_GIT_BASE` is deliberately left unset
by this install: with neither it nor `BLERG_RUNNER_GITHUB_ORG` configured, the
git base is the runner's own default of bare `https://github.com`, and a
cluster session's repo must be given as `org/name`. A bare `name` is rejected
at spawn time with a 422 naming the `org/name` form. Two ways to change that,
both set on the `blerg-config` ConfigMap:

- `BLERG_RUNNER_GITHUB_ORG=<org>` — bare names resolve against that one org
  (`https://github.com/<org>/<name>.git`); `org/name` still works too.
- `BLERG_RUNNER_AGENT_GIT_BASE=https://git.example` — a self-hosted git host.
  Setting it explicitly wins over `BLERG_RUNNER_GITHUB_ORG` and also accepts
  bare names, so set it only when you really mean it.

ConfigMap values are read at process start, so after editing `blerg-config` by
hand, restart the server: `kubectl -n blerg rollout restart
deployment/blerg-runner`.

The per-account credential path also depends on `BLERG_CORE_INTERNAL_KEY`
matching between core and runner (`BLERG_RUNNER_CORE_INTERNAL_KEY`).
`deploy.sh` provisions and wires both, so there is nothing to do — but if a
cluster session can't resolve its launcher's credential, confirm the pair is
actually live before looking anywhere else:

```sh
kubectl -n blerg exec deploy/blerg-runner -- env | grep BLERG_RUNNER_CORE_INTERNAL_KEY
```

## New repository

With **Cluster pod** selected, the launch sheet's **New repository** item creates a repository on
the person's git host and starts the session in it: type `owner/name`, pick GitHub or GitLab and
Private or Public, and launch (**Create & Launch →**). The runner uses the person's own `github` /
`gitlab` token from Settings to create it — under their account when `owner` is their login,
otherwise under that organisation or group — with an initial commit, and the pod clones it like
any other repository. The default branch is the provider's; the agent works on `wip/<session>`.

An existing repository is refused (409): launch it as an existing one instead. When the
repository cannot be created — no token in Settings, a token without permission to create
repositories under that owner (a fine-grained GitHub token needs **Administration: write**, a
GitLab token `api`), a provider error — the session still starts: the pod initialises an empty
repository whose `origin` is where the repository would be, the start panel's first stage says
why creation did not happen, and the agent's first prompt tells it the remote does not exist yet.
Work is committed locally; the first push fails visibly in the transcript, and pushes succeed as
soon as the repository is created (by the agent with a credential that may, or by the person).
The pod of a new-repository session never gets the operator's `BLERG_RUNNER_GIT_TOKEN`.

API: `POST /api/sessions` with `new_repo: true`, `repo: "owner/name"`, optional `provider` and
`visibility` — see `runner/README.md`, "New repository (cluster)".

## Operator secrets contract

Two Kubernetes Secrets, with **stable names and keys**. That contract is the
whole integration surface: Blerg grows no clients for any particular secret
manager.

### `blerg-secrets` (namespace `blerg`)

The base install's Secret, created by `deploy.sh` — see
[`README.md`](README.md) and `secret.example.yaml`. Only one key matters for
cluster runtime:

| Key | Required | Purpose |
|---|---|---|
| `BLERG_RUNNER_DAEMON_TOKEN` | yes | The token daemons authenticate to `blerg-runner` with. Session pods use the **same value**, from the Secret below. |

### `blerg-runner-agent` (namespace `blerg-runner-sessions`)

The Secret every cluster session pod reads. A different namespace than
`blerg-secrets` on purpose: a pod's `secretKeyRef` only ever resolves a Secret
in its own namespace, and session pods live in `blerg-runner-sessions`.

| Key | Required | Purpose |
|---|---|---|
| `BLERG_RUNNER_DAEMON_TOKEN` | **yes** | How the session pod authenticates back to `blerg-runner`'s `/ws/daemon`. Must equal `blerg-secrets`' value — the server validates one daemon token, not a per-namespace one. `deploy.sh` copies it across. |
| `ANTHROPIC_API_KEY` | no | Shared fallback Claude API key (metered billing). Makes `claude` show up in `available_engines`. |
| `CLAUDE_CODE_OAUTH_TOKEN` | no | Shared fallback Claude OAuth/subscription token. Also makes `claude` available. Set one of these two, not both. |
| `BLERG_RUNNER_GIT_TOKEN` | no | Shared fallback git token for clone/push. Drives `git_configured` in the status endpoint. |
| `CODEX_AUTH_JSON` | no | Shared fallback Codex auth — contents of a working `~/.codex/auth.json` (JSON, not a bare token). Makes `codex` available. |
| `HERMES_ENV_CONTENTS` | no | Shared fallback Hermes env — contents of a working `~/.hermes/.env`. Makes `hermes` available. |

Every optional key is exactly that: **a default install sets none of them** and
still works, because each user's own credential from Settings takes precedence
anyway. Set one only when you want every cluster session to be able to fall
back to a shared, operator-funded credential.

`deploy.sh` writes these on the same ladder as `blerg-secrets`: an explicit
`SECRET_*` value from `.env` wins, else whatever is already live in the cluster
is reused, else the key is **omitted entirely** rather than written as `""` (an
absent key must be absent, so the pod's `optional: true` references mean
something). None of them is ever randomized. The `.env` names are
`SECRET_ANTHROPIC_API_KEY`, `SECRET_CLAUDE_CODE_OAUTH_TOKEN`,
`SECRET_BLERG_RUNNER_GIT_TOKEN`, `SECRET_CODEX_AUTH_JSON`,
`SECRET_HERMES_ENV_CONTENTS`.

`cluster-agent-secret.example.yaml` documents the same shape as a YAML
template, with a `kubectl create secret generic` one-liner — useful if you
apply it by hand instead of running `deploy.sh`.

## Bring your own secret manager

To source the agent Secret from Infisical, HashiCorp Vault, Doppler,
1Password, AWS/GCP secret managers, sealed-secrets, or anything else: set

```sh
AGENT_SECRET_EXTERNAL=1
```

in `install/k8s/.env`. `deploy.sh` then leaves
`blerg-runner-sessions/blerg-runner-agent` **completely alone** — it never
creates, updates or reads it — and everything else (image, namespace, RBAC,
ConfigMap) proceeds as normal. You own the Secret; the contract above is all
Blerg requires of it.

The usual way to fill it is [External Secrets
Operator](https://external-secrets.io/), which turns a backend's entries into a
normal Kubernetes Secret. A complete example — replace the store name/kind and
every `remoteRef.key` with your backend's paths:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: blerg-runner-agent
  namespace: blerg-runner-sessions
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: my-secret-store          # your SecretStore/ClusterSecretStore
    kind: ClusterSecretStore       # or SecretStore (namespaced)
  target:
    name: blerg-runner-agent       # must be this name
    creationPolicy: Owner
  data:
    # Required — must equal blerg-secrets' BLERG_RUNNER_DAEMON_TOKEN.
    - secretKey: BLERG_RUNNER_DAEMON_TOKEN
      remoteRef:
        key: blerg/runner-daemon-token
    # Optional shared fallbacks — drop any entry you don't want.
    - secretKey: ANTHROPIC_API_KEY
      remoteRef:
        key: blerg/anthropic-api-key
    - secretKey: CLAUDE_CODE_OAUTH_TOKEN
      remoteRef:
        key: blerg/claude-code-oauth-token
    - secretKey: BLERG_RUNNER_GIT_TOKEN
      remoteRef:
        key: blerg/runner-git-token
    - secretKey: CODEX_AUTH_JSON
      remoteRef:
        key: blerg/codex-auth-json
    - secretKey: HERMES_ENV_CONTENTS
      remoteRef:
        key: blerg/hermes-env-contents
```

**The daemon token must match.** `blerg-runner` validates a single daemon
token, so `blerg-runner-sessions/blerg-runner-agent`'s
`BLERG_RUNNER_DAEMON_TOKEN` has to be byte-identical to
`blerg/blerg-secrets`'. Two ways to guarantee that:

1. **Sync both from the manager** — store the token in your backend and point a
   second `ExternalSecret` (in namespace `blerg`, targeting `blerg-secrets`) at
   the same `remoteRef.key`. Rotating it in one place then updates both.
2. **Copy it once** — let `deploy.sh` generate it, then read it out and put
   that value in your backend under the key the `ExternalSecret` above
   references:

   ```sh
   kubectl -n blerg get secret blerg-secrets \
     -o jsonpath='{.data.BLERG_RUNNER_DAEMON_TOKEN}' | base64 -d
   ```

   If the two ever diverge, every session pod fails to connect back to the
   server: the Job starts, authenticates, is rejected, and the session never
   reaches `running`.

## Verify

```sh
curl -s http://runner.<DOMAIN>/api/cluster/status | jq
```

Expect:

```json
{
  "configured": true,
  "namespace": "blerg-runner-sessions",
  "image": "reg.example/blerg-runner-agent:latest",
  "daemon_id": "00000000-0000-4000-8000-00000000f00d",
  "max_sessions": 4,
  "active_sessions": 0,
  "available_engines": [],
  "git_configured": false,
  ...
}
```

- `"configured": false` (with every other field empty) means
  `BLERG_RUNNER_AGENT_IMAGE` is empty — you deployed with `CLUSTER_RUNTIME=off`
  — or the server isn't actually running inside the cluster
  (`KUBERNETES_SERVICE_HOST` unset). This endpoint can't distinguish "turned it
  off" from "not in k8s at all," which is fine: both mean the same thing to a
  launch UI.
- `"active_sessions": -1` means the Jobs list call itself failed — almost
  always the Role/RoleBinding from `cluster-runtime.yaml` not applied, or
  applied to the wrong namespace. Check
  `kubectl -n blerg logs deployment/blerg-runner` for the exact k8s API error.
- `"available_engines": []` and `"git_configured": false` are **normal** on a
  default install: they describe the *operator* Secret only. Users' own
  credentials are per-account and never appear here — the launch sheet unions
  this list with the caller's own (`GET /api/me/credentials`). These fields
  only fill in if you set the optional keys above.
- `"available_engines"` empty when you *did* set an optional key means the
  Secret is missing, in the wrong namespace, or that key is empty. Reading it
  needs the same RBAC as the Jobs calls, so an empty list alongside a working
  `active_sessions` count points at the Secret rather than the RBAC.

Also check the deploy actually landed:

```sh
kubectl -n blerg-runner-sessions get secret blerg-runner-agent
kubectl -n blerg-runner-sessions get rolebinding
```

Then pick **Agent** + **Cluster pod** in the launch sheet (it is already the
default when no workstation daemon is connected), give a repository as
`org/name`, and launch — a Job named `blerg-runner-agent-<session-id>` should
appear:

```sh
kubectl -n blerg-runner-sessions get jobs
kubectl -n blerg-runner-sessions logs job/blerg-runner-agent-<session-id>
```

### Environment preflight

Run [`README.md`](README.md)'s "Environment preflight" checks first —
everything here is additional and assumes the base stack already passes those.

| # | Check | Command | Pass looks like | If it fails |
|---|---|---|---|---|
| 1 | Base stack is deployed and healthy | `kubectl -n blerg get pods` | `blerg-runner`, `blerg-core`, `blerg-board`, `postgres-0` all `Running`/`1/1` | Deploy the base stack first (`README.md`) |
| 2 | Service account tokens auto-mount in this cluster (the runner reads `/var/run/secrets/kubernetes.io/serviceaccount/token` from inside its own pod) | `kubectl -n blerg exec deploy/blerg-runner -- cat /var/run/secrets/kubernetes.io/serviceaccount/token \| head -c 20` | Prints the start of a JWT, not an error | Some hardened clusters (Pod Security Admission, OPA/Kyverno) disable `automountServiceAccountToken` cluster-wide or block default SA tokens — then `/api/cluster/status` always reports `"configured": false` even with the agent image set, because a missing token file reads as "not in a cluster" |
| 3 | The agent image's architecture matches your nodes | `kubectl get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}'` vs. `docker version --format '{{.Server.Arch}}'` on the build machine | Match (both `amd64`, or both `arm64`) | A mismatch (common on ARM nodes built from an x86 workstation) produces `exec format error` crash-looping pods, not a clear image-pull error — cross-build with `docker buildx build --platform linux/arm64 ...` |
| 4 | Cluster nodes can pull the agent image | see `README.md`'s "Image registry" check | — | Same registry as the three server images; nothing extra to configure |

## Board-started sessions

Sessions the Board starts (Run board, respawns, reviewers, standing agents,
card and board-chat buttons) are started with that board's **automation
token** — a `run-sessions` agent token a board admin minted for themselves and
saved in the board's settings — not with `BLERG_RUNNER_KEY`. The launching
account is therefore the token's owner, and the session gets *their*
engine credential below, for the board's chosen engine. A board with no token
starts nothing (it never falls back to the shared key). See
[`board/docs/CONFIG.md`](../../board/docs/CONFIG.md#board-automation-identity-who-a-boards-sessions-run-as).

## Session pod hardening and network

Every session pod is created with: no Kubernetes service-account token mounted
(`automountServiceAccountToken: false` — the pod never calls the Kubernetes
API), and a container security context of `runAsNonRoot` (uid 1000, the image's
`agent` user), `allowPrivilegeEscalation: false`, all Linux capabilities
dropped and the container runtime's default seccomp profile
(`RuntimeDefault`). That is the Kubernetes "restricted" Pod Security profile,
so the namespace can also enforce it (`pod-security.kubernetes.io/enforce:
restricted`). The root filesystem is left writable on purpose: the agent works
in `/workspace` (its `$HOME`) and uses `/tmp` and the npm, go and pip caches.

What the sessions need, and why none of it needs a privilege: cloning and
pushing over https, npm and go builds, `claude plugin install`, and the
runner's own child processes run as an ordinary user; nothing binds a port
below 1024, mounts a filesystem or runs Docker. Headless Chromium is the one
tool that cares about the seccomp profile: its own sandbox needs unprivileged
user namespaces, which the default profile blocks (checked: with
`RuntimeDefault` and all capabilities dropped `chromium --headless
--screenshot=...` fails with "No usable sandbox"; unconfined it works). The
agent image therefore sets `--no-sandbox` for `/usr/bin/chromium` through
`/etc/chromium.d`; the pod itself is the sandbox. Rebuild and redeploy the
agent image together with the runner server that adds these settings, or
screenshots with the plain `chromium` command fail in pods running an older
image. The `--no-sandbox` setting applies to that wrapper only: browsers that
Playwright downloads for itself ignore `/etc/chromium.d`. A pod's `/dev/shm` is
64 MB by default, which large pages can exhaust and crash Chromium; mount an
`emptyDir` with `medium: Memory` there if your sessions render heavy pages.

**Network.** By default a session pod can reach anything the cluster network
routes to, including in-cluster services such as Postgres, core and the board.
[`networkpolicy-sessions.example.yaml`](networkpolicy-sessions.example.yaml)
is an optional policy that blocks pod-to-cluster traffic from the sessions
namespace except DNS and the runner (port 8080 and the MCP gateway's port 8090),
while keeping general internet egress to public
addresses (git hosts, model APIs, the plugin marketplace). Core is not
reachable from a session pod, and the board only if you uncomment the clearly
marked opt-in rule (crons do not need it: their board access goes through the
gateway). Verify with a throwaway session: the gateway answers (401 without a
token) and `curl` to core, the board (unless opted in) and Postgres times out.
It is not applied by
`deploy.sh`; it needs a CNI that enforces NetworkPolicy (MicroK8s' default,
Calico, does) and CIDRs that match your cluster — read its header first.

### Crons, MCP connections and the gateway

Sessions with MCP connections, and every cron run
([`docs/crons.md`](../../docs/crons.md), [`docs/mcp-connections.md`](../../docs/mcp-connections.md)),
reach their tools through the runner's **MCP gateway**, not directly:

- The runner serves the gateway on its own listener, port 8090
  (`BLERG_RUNNER_MCP_GW_ADDR`), and exposes it **only** through the
  `blerg-runner-mcp-gateway` Service, a `ClusterIP` Service separate from
  `blerg-runner`. `ingress.yaml` routes only the web port 8080; nothing routes
  the gateway, and `deploy_test.sh` fails if an Ingress ever references its
  Service or port. Do not add one, and do not change the Service type.
- Session pods dial it at `BLERG_RUNNER_MCP_GW_URL`
  (`http://blerg-runner-mcp-gateway.blerg.svc.cluster.local:8090`, set in
  `configmap.yaml`). Each pod holds only a per-session, per-connection token,
  delivered through its per-session Secret and never in the Job spec.
- **Apply the egress policy if you use crons or connections.** A cron run
  reads untrusted text unattended. The runner already restricts it to file tools
  plus the MCP tools you allowed, and the policy is the layer that keeps a fooled
  agent from reaching Postgres or your private network. The example already
  allows the gateway port; if you write your own policy, allow TCP 8090 to the
  runner pods (a NetworkPolicy sees the pod port) or every MCP tool call will
  fail. Cron sessions are ordinary session Jobs in the `blerg-runner-sessions`
  namespace, so the policy covers them with no extra selector.
- A cluster cron uses the owner's stored credentials and **never** falls back
  to the shared operator Secret: a missing personal credential fails the run
  before a pod exists.
- `BLERG_*_MCP_ALLOW_HTTP_HOSTS` and `BLERG_*_MCP_ALLOW_PRIVATE_HOSTS` (see
  `configmap.yaml`) are empty by default. The runner, not the session pod,
  fetches a connection's URL, so listing a private MCP host needs no policy rule.

## Per-session Secret

Each session Job gets its own `blerg-runner-session-<id>` Secret, owned by the
Job via `ownerReferences` so Kubernetes' own GC cascade cleans it up whenever
the Job goes away — including `ttlSecondsAfterFinished`/`activeDeadlineSeconds`
expiry, which the runner never hears about.

It carries everything personal or sensitive to that one session: the launching
account's engine credential (`ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN`,
`CODEX_AUTH_JSON`, `HERMES_ENV_CONTENTS`), their personal GitHub or GitLab token
(whichever matches the clone's host) as `BLERG_RUNNER_GIT_TOKEN`, the session's initial prompt
(`BLERG_RUNNER_INITIAL_PROMPT`) and every caller-supplied extra env entry
(runner-brokered session tokens such as `BLERG_BOARD_TOKEN`). The Job spec
references all of them via `secretKeyRef` rather than as literal env values — a
literal is visible to anyone who can `kubectl describe job` / `get job -o
yaml`, which needs only Job read access, not Secret read access. "Personal" is
a *set of keys*, not a single key: whichever ones the user has come from this
Secret, and the rest come from the shared `blerg-runner-agent` Secret.

The runner cleans this Secret up on every path where it was created but won't
end up owned by a Job: a failed Job-create call, a non-2xx response, and a
`409` against an already-live Job (that live Job is not this call's Job and
will never adopt the Secret). Resuming a disconnected cluster session reads the
launching account back from the session's stored spawning account, so a resumed
session still gets its personal credentials instead of silently falling back to
the operator Secret.

## Status dashboard

The runner frontend's Cluster tab (same server, `/cluster` route) shows the
same `/api/cluster/status` data plus the live session list
(`GET /api/sessions?runtime=cluster`) without needing `kubectl` — useful for
checking cluster runtime's state from a browser, or for an agent to `curl` both
endpoints as a health check before relying on cluster runtime for a task. Use
`runtime=cluster`, not `daemon_id=<the fixed ID above>`: each session pod
connects as its own ephemeral per-session daemon, so a fixed daemon_id only
matches a session in the brief window before its pod connects, while
`runtime=cluster` lists every cluster-runtime session regardless of which
per-pod daemon currently owns it.

## OpenClaw is not supported here

Cluster-runtime sessions can use Claude, Codex, or Hermes — not OpenClaw. The
agent image doesn't install the `openclaw` CLI, and cluster session Jobs have
no `OPENCLAW_*` credential wiring. The workstation daemon path supports all
four engines; this is a real capability gap of cluster runtime specifically,
not a bug. Extending cluster runtime to OpenClaw is future work.
