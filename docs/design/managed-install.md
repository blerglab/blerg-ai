# Managed Kubernetes install (0.7.0)

Status: draft for owner review, 2026-11-09, revised after adversarial review round 1 (see the end). Card: BLG-612. Related: BLG-560 (prove it on a real
cloud cluster), BLG-563 (email), BLG-611 (credentials), BLG-403 (pinned image
digests), BLG-448 (least-privilege database roles), BLG-182 (RBAC), BLG-117 (rolling update),
BLG-597 (disk guard), BLG-171 (one-click upgrade, moved to 0.8.0). Builds on
`docs/design/team-scope.md` and `docs/design/accounts-and-placement.md`.

## Why

The startup's first week starts with "sign in to a cloud Kubernetes Blerg" (owner). Today the
Kubernetes install is `install/k8s/deploy.sh`: it builds every image from source, pushes them to a
registry the installer must provide, and applies Blerg's own manifests with kustomize. That suits
the lab and people who build Blerg. It doesn't suit a team that wants to run Blerg, and it isn't
what teams or enterprises expect on Kubernetes.

## Who installs

**An agent following a runbook** (owner). `install/k8s/AGENT-INSTALL.md` already is a runbook
written for an agent, with ground rules, decision points for the human, preflight, checkpoints
after each step and secret-handling rules. The managed install extends that runbook; it doesn't
replace its approach.

## Goals

1. **A Helm chart plus prebuilt images** published with every release (owner). The chart works on
   any conformant cluster; the images come from a public registry, so installers need no registry
   of their own and never build the 5 GB worker image.
2. **A short runbook for the "we have nothing yet" path:** create a small cluster (DigitalOcean
   recommended), add the standard ingress and certificate add-ons, choose the database, then
   `helm install` with a few values. It points to the cloud provider's own docs for cloud steps;
   Blerg doesn't automate cloud provisioning.
3. **The agent alerts the person about anything missing** (owner): before changing anything, it
   runs a preflight; for each missing prerequisite it stops and tells the person what is needed,
   why, the recommended choice and its monthly cost, and continues only after they confirm.
4. **Upgrades through the agent and Helm** (owner), with a backup first, migrations on startup, a
   health check and a documented rollback. Blerg's UI keeps showing "update available" with the
   release notes.
5. **The lab keeps building from source** (owner).

## Non-goals for 0.7.0

- Automating cloud provisioning (creating clusters, DNS zones or databases through cloud APIs).
- One-click upgrades from the UI (BLG-171, 0.8.0): that needs Blerg to hold rights over its own
  cluster.
- A Kubernetes operator.
- Multiple clusters as runtimes (BLG-272, 0.8.0).

## Anti-goals

- A second, drifting set of manifests. There is one source of Kubernetes manifests.
- Printing or committing secrets; personal setup in tracked files.
- An install that silently loses data: no default that leaves a team's only copy of its work in
  an unbacked-up volume without saying so.
- Images that change under a tag: installs pin digests (BLG-403).

## What the person provides, and what the agent does

Prerequisites the person owns (they need payment or ownership), each with a recommendation the
runbook gives when it is missing:

Prices below are list prices to **verify in the spike**; the runbook shows current ones.

- A Kubernetes cluster, or a cloud account to create one. Recommended: DigitalOcean Kubernetes,
  Basic nodes, **two 4 vCPU / 8 GB nodes** (about $96 a month), no paid control-plane HA, until
  the load run (call 11) supports another shape. The cloud load balancer the ingress creates
  adds about $12 a month.
- **Managed Postgres** (recommended, below), sized so its connection limit covers the three
  services' pools plus headroom.
- **Object storage** for whole-install backups, outside the cluster. Recommended: DigitalOcean
  Spaces (about $5 a month for the base allowance). The person creates the bucket and an access
  key; the chart references the key by Secret name.
- A domain name they control, and the ability to add **three DNS records**: `<DOMAIN>`,
  `board.<DOMAIN>` and `runner.<DOMAIN>`, all pointing at the ingress load balancer (or a DNS API
  token if the certificate add-on validates through DNS).
- An email sending account (BLG-563) that works from the cluster. DigitalOcean is reported to
  block outbound SMTP on ports 25, 465 and 587 (**verify in the spike**), so the runbook
  recommends a transactional service reached over its **HTTP API** or SMTP on **port 2525**.
- **`kubectl` access for the first admin**, because the first-run setup link comes from a
  `kubectl exec` command the person runs themselves (below).
- Engine logins are connected after the install, from the [+] credential screen. OIDC is 0.8.0
  (BLG-610) and not part of this install.

The agent does everything else with the chart: renders values, creates or references secrets,
installs, waits for health, and verifies. The person, not the agent, completes first-run setup
(call 5).

## The chart

- **One chart, one source of manifests.** The kustomize manifests become the chart. `deploy.sh`
  stays for the lab and source builds, as a wrapper that builds and pushes images, then runs the
  same chart with local values. *(Main's call; it keeps one source of truth.)*
- **Components:** Core, Runner and Board; the live-data components; the cluster runtime (agent
  jobs and their RBAC); session network policies (on by default, explicit opt-out, call 8);
  optional in-cluster Postgres.
- **Images:** prebuilt per release for each service and the worker image, published to a public
  registry, referenced by digest. Air-gapped or enterprise installs can mirror them; the registry
  is a value.
- **Values, kept small:** the domain, the ingress class, TLS issuer, database (in-cluster or an
  external URL from a secret), email settings, the backup bucket, storage
  classes and sizes, resource requests, cluster runtime on or off, network policy on or off.
  Everything else has sensible defaults.
- **Secrets:** by default they are generated once, on first install, as Kubernetes Secrets
  created outside the rendered release, so Helm's history never stores them (call 4); they are
  never regenerated on upgrade. Each can instead reference an existing Secret, so External
  Secrets, Vault and similar work without the chart knowing about them. Rotation is BLG-611.
- **RBAC on** (BLG-182), with least-privilege service accounts, and least-privilege database roles
  per service (BLG-448).

## Database

- **The chart supports both** (owner): in-cluster Postgres, or an external managed Postgres.
- **The startup runbook recommends managed Postgres** (owner) because it gives point-in-time
  restore, which the whole-install backup builds on (below); on DigitalOcean it starts around $15
  a month.
- **Connection limits.** Small managed plans allow few connections (the smallest DigitalOcean plan
  is reported at about 22; **verify in the spike**). The chart sets each service's pool size;
  preflight adds up Core, Runner and Board pools plus migrations and the setup identity, compares
  the sum with the plan's limit, and stops with a recommended plan size if it doesn't fit. The
  spike records the plan the three-person load run needs.
- All three databases live on **one managed Postgres cluster**, so one point-in-time restore
  covers them at the same instant (Main's call).
- In-cluster Postgres stays available for trials and homelabs; the chart then installs a
  scheduled backup job to object storage, and the runbook says plainly that backups are the
  installer's responsibility.

## TLS, DNS and ingress

- The standard ingress controller and certificate add-on are prerequisites the runbook installs
  from their own charts if missing; Blerg's chart only references them.
- **Which ingress controller.** Today's manifests assume ingress-nginx (`install/k8s/README.md`),
  which is reported to be retiring (**verify**). The spike picks a maintained controller and
  proves WebSockets with long timeouts and 128 MB uploads on it; **Traefik** is the first
  candidate to verify, Envoy Gateway the second. The chart expresses timeouts and body size for
  the chosen controller, not only as nginx annotations.
- The agent tells the person exactly which DNS records to create, or creates them with a DNS
  token if provided, and waits for certificates before continuing.

## Backups and restore

- **A backup is the whole install** (call 2), stored encrypted in object storage outside the
  cluster.
- **No admission freeze for routine backups** (Main's call). Work keeps running. Upgrades still
  freeze admission (call 1). Key rotation never runs during a backup.
- **One recovery point across the database and files** (Main's call). With managed Postgres, the
  database side is the provider's point-in-time restore. Each backup:
  1. opens a **file journal**: every write to the backed-up files gets a sequence number, and the
     journal keeps the file's content before and after;
  2. snapshots keys, live-data state, runner files and volumes;
  3. inside that snapshot window, writes a **recovery checkpoint record** into the database,
     carrying the journal's current sequence number, and records that moment as the database
     **restore point**;
  4. closes the journal when the snapshot finishes and stores it with the backup.
- **Restore** brings the databases back to that restore point, so the checkpoint record is the
  last thing they contain. It then lays down the file snapshot, replays journal entries at or
  before the checkpoint's sequence number and discards later ones (restoring the earlier content,
  or removing a new file), so files and database match one point. This all happens while the
  install is still inert (`credential-handling.md`, call 11).
  A restore that can't find the checkpoint record, or finds a gap in the journal, stops and says
  so; it never guesses.
- **A drill proves it:** back up under write load, restore into an isolated namespace, and check
  that every file the database references exists, that no file newer than the checkpoint
  survives, and that sessions resume. It is part of the release-gate restore drill.
- In-cluster Postgres: the backup job takes one physical backup of the server, so the three
  databases share one point; the same checkpoint record, file snapshot and journal apply.
- **Restore is a written procedure** every install keeps. The populated, isolated restore drill is
  a release-gate check we run ourselves (moved, see the end), not a step of every first install.

## Upgrades

- `helm upgrade` to a new release, run by the agent from an upgrade runbook: back up, check the
  release notes' upgrade section, upgrade, wait for migrations and health, verify, and roll back
  if verification fails. The order (freeze admission, fence writers, a coordinated recovery point,
  then upgrade) and the rollback rules are in call 1: no plain `helm rollback` across an
  incompatible migration, and a destructive restore needs the person's approval.
- The existing upgrade gate (checking the release notes' "How to upgrade" before changing
  anything) carries over.
- Blerg's UI keeps showing "update available" with the release notes.

## First admin and setup

- The first admin keeps **today's first-run setup**: while no account exists, the person (not the
  agent) obtains a single-use setup link valid for 24 hours and chooses their own password. The
  agent never mints or prints it (round 1, call 5).
- The person gets the link by running `kubectl -n <namespace> exec deploy/blerg-core --
  /blerg-core setup-link` themselves, so they need `kubectl` access to the cluster (on
  DigitalOcean, `doctl kubernetes cluster kubeconfig save`). The runbook checks this in preflight.
- Email is configured through values. OIDC arrives in 0.8.0.
- **Preflight sends a real test email** to the address the person names, and the person confirms
  it arrived, before setup continues.
- After the first admin signs in, the team model applies: invites, the default org, projects and
  teamspaces (team scope design).

## Machines and desktops

- Desktop daemons and machines such as a Mac Studio connect to the cloud install exactly as they
  connect to the lab today: an outbound secure websocket, enrolled from Settings.
- The cloud install's runner must be reachable from those machines (the ingress already provides
  it).

## The runbook's shape

Kept close to today's AGENT-INSTALL.md:

1. Ground rules (secrets, asking the human, no personal setup).
2. Decision points for the human: cluster or cloud account, domain, database (managed
   recommended), email, cluster runtime on or off.
3. Preflight: tools present (kubectl, helm), the person's own `kubectl` access, cluster
   reachable, add-ons present (a maintained ingress controller), the three DNS names, storage
   classes, capacity, the database connection limit, the backup bucket, and a real test email;
   for anything missing, stop and alert the person with the recommendation and its cost.
4. Install: values, secrets, `helm install`, checkpoints after each step.
5. Verify: pods ready, health through the ingress, the agent manifest, first-run setup, a first
   backup landing in object storage, and a sign-in by the first admin.
6. Upgrade and rollback sections.

## Decided by the owner (2026-11-09)

- An agent following a runbook does the install; the person gets recommendations for anything
  missing, and the agent alerts them.
- A Helm chart with prebuilt images is the managed install; the lab keeps building from source.
- The chart supports in-cluster and managed Postgres; the startup runbook recommends managed.
- Upgrades through the agent and Helm in 0.7.0; one-click UI upgrades move to 0.8.0.
- The remaining calls (secrets, TLS, backups, first admin, machines) were accepted as proposed.

## Left to the builder

- Chart structure, value names and defaults; how the kustomize manifests are converted.
- The release pipeline that builds, signs or pins, and publishes images and the chart.
- The backup job's mechanics and object storage choice.
- Runbook wording, within the existing runbook's conventions.

## Review round 1 (2026-11-09): contracts added

An adversarial review (Codex, 16 findings, 10 high) found the draft named the right parts but left
their behavior undefined. These are **Main's calls** (the owner may overrule). They define behavior
the chart, runbook and release pipeline must meet; mechanics stay the builder's.

1. **Upgrades and rollbacks are an ordered operation.** Upgrade: freeze admission (no new
   sessions or workers), stop or fence writers including running Jobs and connected daemons, take a
   coordinated recovery point of all three databases plus keys and files, record the old release
   and image digests, upgrade, wait for migrations and a cross-service compatibility check, verify,
   then reopen. Rollback: if the schema changed incompatibly, services stay stopped, the matching
   data, keys and files are restored, then the matching release, then verify and reopen. A
   destructive restore needs the person's approval and states what is lost. No automatic rollback
   across incompatible migrations.
2. **A backup is the whole install,** in both database modes: all three databases at one
   consistent point; the vault and Board keys; the live-data authority and issuer state; runner
   files; and workspace and native-history volumes, or an explicit statement that they are lost.
   Backups are encrypted and stored outside the cluster, with retention, alerting and a stated
   recovery-point and recovery-time objective. Restored volume claims are rebound or refused
   explicitly, never silently mismatched. The populated restore into an isolated namespace is a
   release-gate drill (moved, see the end).
3. **Secrets fail closed.** Generate once. If data exists but key material is missing or
   malformed, the install stops and asks for recovery; it never generates a replacement. A denied
   read is not treated as absence. External Secrets are validated before rollout. Uninstall keeps
   Secrets and volumes by default. Rotation (BLG-611) covers database passwords, encryption keys and
   service keys, with consumer reloads and backup compatibility.
4. **Helm never holds secrets.** Values carry only non-secret settings and the names of Secrets and
   keys. Credentials are never passed with `--set`, and nothing secret appears in notes, dry-run or
   debug output. Generated secrets are created outside the rendered release, so Helm's release
   history doesn't store them. The runbook gives the agent name-and-status-only commands, and never
   prints full values or manifests.
5. **Bootstrap keeps today's protocol.** The first-run setup link stays human-held, single use,
   valid 24 hours while no account exists, and never printed by the agent or logged. Changing chart
   values never resets or promotes an account. On a fresh install the first person becomes the
   install admin and the default org's admin, per the team scope compatibility rule. A recovery
   route exists when email is unavailable.
6. **Managed Postgres has a provisioning contract.** Three named connection references (Core,
   Runner, Board), supported Postgres versions, CA-verified TLS with hostname checks, direct
   endpoints (not transaction poolers) for migrations, and an idempotent setup step using a
   separate setup identity that creates the three databases and least-privilege roles (BLG-448),
   with a published privilege matrix. External mode renders no bundled database resources and never
   rewrites the connection strings on upgrade. Preflight proves migrations run with those roles.
7. **The cluster runtime keeps its explicit Role,** bound only in the sessions namespace: Jobs
   (get, list, watch, create, delete), volume claims (get, list, create, delete), Secrets (get,
   create, patch, delete), pods (get, list, patch) and pod logs (get). No exec, no RBAC writes, no
   cluster-wide rights. The sessions namespace enforces pod security: no privileged pods, no host
   mounts, no service-account tokens in worker pods.
8. **Session network isolation is on by default** for managed installs, with an explicit opt-out
   that states the consequence. It allows DNS, the runner (8080), the internal MCP gateway (8090),
   and public model and Git endpoints, plus intentional private mirrors; it denies cloud metadata
   and other private ranges, including IPv6. The verify step proves allowed and denied connections
   from a throwaway session.
9. **Rollout is version-gated.** The chart pins Core, Board, Runner and the worker image as one
   supported bundle. Team operations stay refused until every service reports the compatible
   version and its migrations are done (accounts design, call 10). Running Jobs and daemons on an
   older version are fenced. Partial rollout, rollback and daemon mismatch are tested.
10. **The install's acceptance is the first-week gate.** Infrastructure health is a narrower
    status; release acceptance on a cloud cluster requires the team scope and accounts gates: email
    delivery and invite redemption, three people in one org and project, the revision-bound
    blackboard approval, workspaces and claims, each person's own Claude and Codex credentials
    with simultaneous refresh, reconnect and revocation, and results flowing back. It records the
    released digests and drill results.
11. **Sizing comes from a load run.** The recommended node shape is published only after a
    three-person load run with the real requests, limits, session caps and storage. Until then the
    runbook says **two 8 GB nodes** (about $96 a month) rather than one, and shows storage, load
    balancer and backup costs. Preflight checks allocatable memory, disk for the worker image and
    volume limits, and Blerg refuses work plainly at capacity.
12. **Live data gets a lifecycle.** An in-cluster initializer creates the live-data authority and
    issuer state (no Docker on the installer's machine), reuses them on upgrade, renews leaves and
    backs them up. A degraded state is visible. The accounts usage tab depends on it, so it is part
    of the acceptance gate.
13. *(Signing, the bill of materials and multiple architectures moved to 0.8.0; digest pinning
    stays.)* **Releases are signed and fully pinned.** Each release publishes a signed bill of materials
    binding the chart version, source commit and every image digest (servers, worker, Postgres,
    helpers) for each supported architecture. The runbook verifies the signature before installing.
14. *(Mirror coverage moved to 0.8.0; the published sizes and endpoint list stay.)* **Mirrors
    cover dynamic Jobs.** Registry overrides, pull credentials and registry CA trust
    apply to the sessions namespace and every dynamically created Job too. The docs publish the
    worker image's compressed and unpacked sizes, cold-start times and minimum disk, and list the
    outbound endpoints a runtime needs. An image mirror is not an offline runtime.
15. **Desktops are verified, not assumed.** Verify WebSocket and upload limits on the chosen
    ingress controller, person-bound enrollment, revoke and reconnect, and the gateway route.
    Publish the minimum daemon version and capabilities; refuse team work from older daemons. Remove
    the obsolete shared-token instructions from the README.
16. **The lab adopts the chart once, carefully.** A one-time adoption runbook preserves resource
    names, volume bindings, keys and live settings, with a backup first. Afterwards `deploy.sh` only
    builds, pushes and resolves digests, then runs the same chart and lifecycle helpers; the
    kustomize renderer is retired so there is no second production source.

This round also corrected a factual error: the draft said first-run setup used a forced password
change. Today it is a human-held single-use link, which call 5 keeps.

## Scope moved to 0.8.0 (owner, 2026-11-09)

After a readiness review by two models (Codex and Claude), the owner moved these out of 0.7.0;
they can be pulled forward if 0.7.0 goes well. Where this list conflicts with earlier sections,
this list wins.

1. **Shared personal machines and local models** (BLG-607, BLG-608): sharing a person's machine
   with a team, sandbox-only use by others, the local-model card, the daemon relay and direct
   URLs. The startup works on the cloud in 0.7.0.
2. **Nested orgs and projects in the UI, and blackboard rollup.** The data model stays nested so
   no door closes; the 0.7.0 screens show one org, flat projects and one level of teams.
3. **Release extras:** the signed bill of materials, multi-architecture images and mirror coverage
   for dynamic worker Jobs. Digest pinning stays.
4. **Full key rotation:** versioned-envelope re-encryption and a planned no-downtime rotation
   procedure for every secret class. 0.7.0 keeps the **emergency rotation** procedure (which may
   need a brief planned outage, documented) and the **mandatory offboarding rotation**. *(Since
   2026-11-09 there are no team credentials to rotate in 0.7.0; see Simplification.)*
5. **The restore drill on first install** becomes a **release-gate check we run ourselves**;
   installs keep whole-install backups and a written restore procedure. Customers can add their
   own verification pipelines.
6. **Magic links.** Sign-in in 0.7.0 is **username + password + an authenticator-app code**.
   Email is still used for invites, password resets and credential alerts.

## Final round (2026-11-09)

- Sizing in the body now says two 8 GB nodes, matching call 11; load balancer cost added.
- Object storage (DigitalOcean Spaces, about $5 a month) is a prerequisite for backups.
- Email uses an HTTP API or SMTP on port 2525 (DigitalOcean port blocking: verify in the spike);
  preflight sends a real test email.
- A maintained ingress controller replaces the ingress-nginx assumption; Traefik first, then
  Envoy Gateway (verify in the spike).
- Managed Postgres: a connection-limit preflight and plan sizing; all three databases on one
  cluster (Main's call).
- Routine whole-install backups use provider point-in-time restore plus a short coordinated
  snapshot of keys and files, recording the restore point, without freezing admission (Main's
  call).
- Backups write a recovery checkpoint record inside the snapshot window and journal file writes
  by sequence number; restore replays or discards files by checkpoint, proven by a drill under
  write load (Main's call; replaces "reconciles the few seconds").
- DNS for the three hostnames is listed; the first admin needs `kubectl` for the setup link.
- The body follows calls 1, 2, 4, 5 and 8: no "first admin" value, secrets outside Helm history,
  network isolation on by default, ordered rollback, whole-install backups.
- The first-install restore drill is a release-gate drill; calls 13 and 14 are marked 0.8.0 where
  moved.

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- The acceptance gate (call 10) uses each person's own Claude and Codex credentials, not
  team-only ones. AI credentials are personal only (`accounts-and-placement.md`).
- The mandatory offboarding rotation has no team credentials to rotate in 0.7.0.
