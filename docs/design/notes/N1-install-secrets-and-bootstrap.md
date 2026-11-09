# N1 — install secrets and bootstrap

Blocks: BLG-641 (1.1 Helm chart), BLG-642 (1.2 generated Secrets / existing-Secret refs),
BLG-644 (1.4 external managed Postgres). Source: `docs/design/managed-install.md` ("The chart",
"Database", calls 3, 4, 6, 16); `docs/design/0.7.0-build-plan.md` ("Hard-rules notes" N1).

## Failure rule

Anything in this area that fails, or whose state cannot be proven (a denied read, a missing key,
an unverified certificate, an unresolved role), stops the install or upgrade before it changes
anything. Nothing is generated, rendered, or connected to patch over the unknown. The person sees
the exact step that stopped and what is needed to continue; the agent logs the cause (which key,
which read, which check) without the secret value itself.

## Rules

1. The **data-presence probe** is mode-specific, because before first boot there is no database to
   query at all:
   - **Bundled Postgres, never started.** Proven entirely from the Kubernetes side, before any
     Postgres process exists: no bundled Postgres PVC/PV (bound, retained, or rebound), no
     `blerg-secrets`, no install-identity marker, no runner/workspace files or PVCs, and no
     live-data authority or issuer state, in the release namespace and the configured sessions
     namespace. (Main's call, 2026-11-09.) This path exists because today's Postgres pod depends
     on `blerg-secrets/POSTGRES_PASSWORD` to even start (`install/k8s/postgres-statefulset.yaml:
     47-61`) — there is no password to check against a database that has never run, and starting
     one just to check would itself be a change made before the probe.
   - **Bundled Postgres, previously started, or external Postgres.** A read-only maintenance/
     catalog connection checks whether each of the three target databases exists and holds
     **application** schema — not merely the stock system/public schema an empty, newly
     initialized Postgres database always has. A target database that does not exist yet, versus
     one that exists but a read is denied or fails, are different outcomes: the former can still
     feed rule 1's Kubernetes-side checks; the latter is covered by rule 3. (Finding 1, round 2.)
   - For either mode, the chart's probe replaces today's `deploy.sh`, which checks only Secret
     existence (`install/k8s/deploy.sh:185-197`) and documents the resulting
     `password authentication failed` crash loop when that assumption is wrong
     (`install/k8s/deploy.sh:178-184`, `install/k8s/README.md:526`). (Finding 1, round 1.)
2. The probe's inventory is **bounded and published**, not "no files anywhere": the release
   namespace and the one configured sessions namespace; retained or rebound PersistentVolumes
   claimed by either; and, for the catalog check, only the three target databases, read through
   the setup identity's own catalog access to those three databases — not broad Secret, volume, or
   database administration rights, which would violate the least-privilege roles rule 19 requires.
   The setup identity's probe rights are separate from, and wider than, the services' own
   least-privilege runtime roles. A probe that cannot see everything in its bounded inventory
   (a denied namespace list, an unreadable PV, a privilege-filtered catalog view that hides another
   role's tables) is **insufficient visibility**, which is unknown, not empty. The catalog check is
   read-only and never triggers a migration or any DDL — `core/internal/db/postgres.go:11-18`
   calls `Migrate` from `Open`, so that path is never reused as the probe connection. (Finding 2,
   round 2.)
3. If any part of the data-presence probe is denied, errors, or is otherwise inconclusive, the
   install treats that as **data present** — never as absent — and stops. Unknown is never grounds
   to generate. (Finding 1, round 1.)
4. On an install where the probe (or `blerg-secrets`'s existence) finds data, an explicit
   `SECRET_*`-style override or an existing-Secret reference for **any** persistent, generated
   install secret — not only the encryption/signing keys (`BLERG_CORE_LOCAL_KEY`,
   `BLERG_BOARD_SECRET_KEY`), but also `POSTGRES_PASSWORD`, `BLERG_CORE_REGISTER_KEY`,
   `BLERG_BOARD_SERVICE_KEY`, `BLERG_RUNNER_KEY`, `BLERG_RUNNER_DAEMON_TOKEN`,
   `BLERG_CORE_INTERNAL_KEY`, and `BLERG_CORE_BOARD_MINT_KEY` — that **differs** from the value
   already live is rejected: the install stops and names the key, rather than applying the new
   value. Today's `pick`/`pick_b64` silently take the override over the live value
   (`install/k8s/deploy.sh:193-205,207,219-223`); for `POSTGRES_PASSWORD` specifically this is the
   exact outage `deploy.sh`'s own comment already warns about
   (`install/k8s/deploy.sh:178-184,193-197,207`) — a redeploy writes a new password into
   application config while the running Postgres data directory keeps the old one. Changing any of
   these keys on a live install needs an explicit, separately invoked recovery or
   emergency-rotation operation (BLG-611), never a plain override on an ordinary
   `helm upgrade`/`deploy.sh` run. An override or reference that matches the live value is a no-op,
   not a stop. (Finding 2, round 1; Finding 4, round 2.) DSNs are exempt from this rule — rule 20
   already requires their connection string never be rewritten by the chart itself, which is a
   stronger guarantee.
5. Where rule 4 does not forbid the override outright, generation and override precedence follow
   today's lab-Secret order (`install/k8s/deploy.sh:177-231`: explicit override, else the live
   value, else random).
6. A Secret that exists but is missing an expected key, holds a value that fails validation
   (wrong length, not base64, unparsable DSN), or has a **required** key set to the empty string,
   is **malformed**, not absent. The install stops and asks for recovery; it never invents a
   replacement for that one key. This is a change from `deploy.sh`'s current `existing()`
   (`install/k8s/deploy.sh:188-191`), which silently falls through to `rand` when a specific key is
   missing from an otherwise-existing Secret — the chart must not repeat that fallback. Optional,
   omit-if-absent keys (`ANTHROPIC_API_KEY`, `BLERG_RUNNER_METRICS_TOKEN`,
   `install/k8s/deploy.sh:224-231`) keep today's behavior: empty or absent both mean "feature off,"
   not malformed. (Main's call, 2026-11-09.)
7. When the install stops for a malformed key, a denied read, a rejected differing override, or an
   inconclusive data-presence probe, the message names the Secret and key (or probe step) and
   points at the runbook's recovery section (restore from the person's own backup, or supply an
   existing-Secret reference). Recovery is manual in 0.7.0 — the chart offers no guided recovery
   command. (Main's call, 2026-11-09.)
8. A denied read of a Secret (RBAC refusal, API error) is treated as unknown, never as "no data
   exists." The install stops with the same message a missing Secret would give, naming the read
   that was denied.
9. Any key may instead reference an existing Secret and key name (for External Secrets, Vault,
   or a manual `kubectl apply`). A referenced Secret is validated — exists, has the named key, the
   value parses — before rollout proceeds; an invalid reference stops the install the same as a
   malformed generated key. Changing a reference to point at a different Secret/key for an
   already-provisioned key, while consumers are running, falls under rule 4.
10. Bootstrap uses a narrow **initializing** state, not open-ended resume of an arbitrary partial
    Secret (which would conflict with rule 3's "unknown is never grounds to generate" and rule 6's
    rejection of an incomplete Secret as malformed). Before any Secret write, the installer — under
    one install identity or lock, generation-fenced — writes a lightweight progress record naming
    its generation and that no data consumer (migration, service) has started yet. Each Secret is
    then written **atomically and complete** (every required key in one create call), never
    key-by-key, replacing today's upsert pattern (`kubectl create secret ... --dry-run=client -o
    yaml | kubectl apply -f -`, `install/k8s/deploy.sh:256-258`) which two simultaneous installers
    could both reach and each overwrite. A create conflict (another installer's generation already
    created the Secret) means re-reading and validating the winner, never overwriting it; a stale or
    competing installer holding an older generation is fenced and cannot finish. Resume is allowed
    **only** while the progress record still says "initializing" and no data consumer has started —
    a crash in that window re-runs safely from the record and never rotates an already-committed
    Secret. Once the record is finalized (rollout started, or any data consumer ran), a later crash
    stops for manual recovery; it is never auto-resumed. (Finding 3, round 1; Finding 5, round 2.)
11. No secret value appears in rendered values, `helm get values`, `helm history`, `--dry-run`
    output, or any log line. Credentials are never passed with `--set`. Generated Secrets are
    created outside the rendered release, so Helm's own release history never stores them.
12. Changing any chart value never creates or promotes an account. First-run setup stays the
    human-held, single-use link (`install/k8s/AGENT-INSTALL.md` Step 5 area,
    `install/k8s/deploy.sh:405` `first_run_setup_notice`); the agent never mints or prints it.
13. `helm uninstall` keeps Secrets, the runner data claim, workspace volumes, **and** — when the
    install uses bundled in-cluster Postgres — the database PersistentVolumeClaim/PersistentVolume
    and its backing storage. Nothing in the uninstall path deletes any of these. A chart that
    explicitly manages the bundled Postgres PVC must still default its reclaim/deletion policy to
    retain; "external mode has no bundled database" does not excuse the bundled-mode path from this
    rule. (Finding 4, round 1.)
14. External database mode renders no in-cluster Postgres resources (no StatefulSet, no init
    ConfigMap, no bundled Postgres Secret keys) — today's bundled path is
    `install/k8s/postgres-statefulset.yaml` plus `install/k8s/postgres-init-configmap.yaml`, which
    creates `blerg_board`/`blerg_runner` owned by the same `POSTGRES_USER`
    (`install/k8s/postgres-init-configmap.yaml:11-19`); external mode must not touch that path at
    all.
15. External Postgres is described by three named connection references (Core, Runner, Board),
    each a full DSN naming its own database — mirroring the three keys `deploy.sh` already
    generates (`install/k8s/deploy.sh:240-242`: `DATABASE_URL_CORE`, `DATABASE_URL_BOARD`,
    `BLERG_RUNNER_DATABASE_URL`), but pointing at the managed cluster instead of the in-cluster
    service. Before any write, preflight proves the three DSNs are **one Postgres cluster** by
    reading the Postgres system identifier (`pg_control_system()` or the provider's equivalent)
    through each DSN's actual connection path, including the migration endpoint — matching host and
    port alone is never accepted as proof, since a gateway or pooler can route the same endpoint to
    different backends. Where the provider does not expose the system identifier to the setup
    identity, the install stops and the runbook asks the person to confirm the three databases are
    on one cluster, recording that confirmation as the proof instead. A proven or confirmed mismatch
    stops the install with no provisioning side effects — the single coherent restore point
    `docs/design/managed-install.md` ("Database") requires depends on it. (Finding 6, round 1 and
    Finding 3, round 2; proof method: Main's call, 2026-11-09.)
16. Preflight also proves the server reports a supported Postgres version before any write; an
    unsupported version stops the install with no provisioning side effects.
17. External Postgres connections are CA-verified TLS with hostname checking (`sslmode=verify-full`
    or equivalent), not `sslmode=disable` as the bundled lab path uses today
    (`install/k8s/secret.example.yaml:22-24`). A certificate or hostname mismatch stops the
    connection; it never falls back to an unverified mode.
18. Migrations and the one-time database/role setup use a direct Postgres endpoint, never a
    transaction pooler. `core/internal/db/postgres.go:9-18` and
    `core/cmd/blerg-core/startup.go:16-33` show today's pool is opened straight from the DSN with
    no pooler awareness; the chart's external-mode values must say which endpoint is which.
19. A separate setup identity — not any of Core's, Runner's, or Board's own roles — creates the
    three databases and three least-privilege service roles, idempotently (safe to re-run). Each
    service role gets only the grants it needs on its own database; a published privilege matrix
    documents them. Preflight proves migrations actually run under the least-privilege role, not
    the setup identity. The setup identity's own rights (rule 2) are never handed to a running
    service.
20. Upgrading an external-database install never rewrites the stored connection strings.
21. Each service's pool size is set explicitly in its DSN or chart value — today none of the three
    services sets one (`core/internal/db/postgres.go:9-14` calls `pgxpool.New`/`NewWithConfig`
    with no `pool_max_conns`, so pgx's unconfigured default applies). Preflight sums the
    **worst-case concurrent** connection count, not steady state: each service's configured pool
    size multiplied by its rolling-upgrade surge (old and new replicas overlapping, not just
    replica count — `install/k8s/core-deployment.yaml:9` and `install/k8s/board-deployment.yaml:9`
    are one-replica Deployments today, but one replica is not a bound on simultaneous pods during a
    rolling update), plus the setup identity's and migration connections (serialized with each
    other, or counted concurrently if the chart allows them to overlap). It reserves headroom of
    `max(5 connections, 10% of the plan's advertised limit)` beyond that worst-case sum, and stops
    with a recommended plan size if the total doesn't fit. (Finding 5, round 1; headroom formula:
    Main's call, 2026-11-09.)
22. The lab's adoption of the chart (`deploy.sh` → chart) is a one-time, backed-up runbook step
    that preserves existing resource names, volume bindings, live keys, **and the bundled Postgres
    PVC** (rule 13); it does not regenerate `blerg-secrets` or any existing PVC. After adoption,
    `deploy.sh` only builds, pushes, and resolves digests, then runs the same chart; the kustomize
    renderer (`install/k8s/kustomization.yaml`) is retired as a second production source.

## Test matrix

| Situation | Expected result | Layer | Slice |
|---|---|---|---|
| Fresh bundled install: truly empty namespace/cluster, no Postgres process has ever run | Kubernetes-side-only probe passes; keys generated, created outside the release | 2 (impl) | 1.2 |
| Fresh external install: target databases not yet created on the external server | Catalog check reports "not yet created" (distinct from denied/failed); keys generated | 3b (integration, real Postgres) | 1.4 |
| External target database exists but is newly initialized (only stock system/public schema) | Treated as no application data; keys generated, not blocked by the stock schema | 3b (integration, real Postgres) | 1.4 |
| `blerg-secrets` deleted, but Postgres and runner PVCs still hold data (uninstall/reinstall) | Data-presence probe finds data; install refuses to generate replacement keys and stops | 3b (integration, real Postgres + PVCs) | 1.2 |
| External mode, no local PVCs, but the external DB already has application schema from a prior install | Catalog check finds existing application data; install stops, no key generation | 3b (integration, real Postgres) | 1.2 |
| Data-presence probe read denied or errors (can't reach DB, can't list PVCs, denied namespace list) | Treated as insufficient visibility / present / unknown; install stops, no key generation | 2 (impl) | 1.2 |
| Setup identity's catalog view hides another role's existing table (privilege-filtered) | Treated as insufficient visibility, not absence; install stops | 3b (integration, real Postgres with a restricted role) | 1.2 |
| Probe connection used for the catalog check | Never runs `Migrate` or any DDL; read-only assertions only | 2 (impl, unit against the probe's connection setup) | 1.2 |
| Retained PV whose PVC and namespace are gone; a rebound PV; workspace data only in the sessions namespace; live-data-authority-only state with no other data | Each is found as data by the bounded inventory; install refuses to generate keys and stops | 3b (integration, real cluster with the published installer permissions) | 1.2 |
| Probe run under the published installer permissions; a runtime service account attempts the same reads | Installer permissions see the full bounded inventory; the runtime service account cannot (rights are not inherited) | 3b (integration, real cluster, both identities) | 1.2 |
| Unreadable ownership/claim history for a PV or PVC (can't tell who owns it or whether it's bound) | Insufficient visibility; treated as unknown, install stops | 3b (integration, real cluster) | 1.2 |
| Secret exists, all keys present, redeploy with no overrides | No key changes value | 2 (impl) | 1.2 |
| Existing install, `SECRET_BLERG_CORE_LOCAL_KEY` (or similar encryption key) override differs from the live value | Install stops, names the key, value not replaced | 2 (impl) | 1.2 |
| Existing install, `SECRET_POSTGRES_PASSWORD` override differs from the live value | Install stops, names the key, does not write a mismatched password into application config | 3b (integration, real Postgres) | 1.2 |
| Existing install, a service key override (e.g. `BLERG_RUNNER_KEY`) differs from the live value | Install stops, names the key, old consumers keep working with the unchanged live value | 3b (integration, real Postgres) | 1.2 |
| Existing install, override for any persistent key matches the live value | No-op, install proceeds | 2 (impl) | 1.2 |
| Existing-Secret reference for an already-provisioned key changed to a different Secret/key while consumers run | Install stops before rollout | 3a (contract, fake K8s API) | 1.2 |
| Two installers run `helm install`/`deploy.sh` against the same fresh cluster simultaneously | Exactly one generation's Secret creation wins; the loser's progress record is fenced and it re-reads and validates the winner, never overwrites | 4 (e2e, test cluster, two concurrent runs) | 1.2 |
| Install crashes after writing the progress record but before any Secret is created | Re-run resumes from the record; proceeds to create Secrets once | 4 (e2e, test cluster, simulated crash) | 1.2 |
| Install crashes after creating one of several Secrets, still "initializing," no data consumer started | Re-run resumes safely; the already-created Secret's keys are not rotated | 4 (e2e, test cluster, simulated crash) | 1.2 |
| Install crashes after the progress record is finalized (rollout started or a migration ran) | Re-run stops for manual recovery; never auto-resumes | 4 (e2e, test cluster, simulated crash) | 1.2 |
| A stale installer holding an older generation tries to finish after a newer generation won | Fenced; cannot create or finalize anything | 4 (e2e, test cluster) | 1.2 |
| Secret exists, one expected key missing (partial/malformed) | Install stops, asks for recovery, generates nothing for that key | 2 (impl) | 1.2 |
| Secret read denied (RBAC refusal) | Treated as unknown, install stops, not "no data" | 2 (impl) | 1.2 |
| A key references an existing Secret missing the named field | Install stops before rollout | 3a (contract, fake K8s API) | 1.2 |
| A key references a valid existing Secret | Chart uses it, never regenerates | 3a (contract) | 1.2 |
| `helm template` / `--dry-run` / `helm get values` / `helm history` on a populated release | No secret value in any output | 2 (impl, string-scan assertion) | 1.1 |
| Re-run with only a non-secret value changed (e.g. domain) | No account created or promoted; existing admin untouched | 3b (integration, real Postgres) | 1.2 |
| External database mode selected | No Postgres StatefulSet/init ConfigMap/PVC rendered | 2 (impl, template rendering) | 1.4 |
| `helm uninstall` with bundled in-cluster Postgres populated | Database PVC/PV and backing storage remain, along with `blerg-secrets`, runner data, and workspace PVCs; a later reinstall reads back the same records and keys stay decryptable | 4 (e2e, test cluster) | 1.1 |
| External Postgres: three DSNs, setup identity runs setup step twice | Databases and roles exist once, second run is a no-op | 3b (integration, real Postgres) | 1.4 |
| External Postgres: three DSNs share one host:port but route (gateway/pooler) to different backends | System-identifier check catches the mismatch; preflight stops before any write | 3b (integration, real Postgres behind a test gateway) | 1.4 |
| External Postgres: three DSNs are distinct aliases of one supported cluster (matching system identifier) | Preflight passes; no false mismatch from differing hostnames | 3b (integration, real Postgres) | 1.4 |
| External Postgres: provider does not expose the system identifier to the setup identity | Install stops; runbook asks the person to confirm one cluster; confirmation is recorded and accepted as proof | 4 (e2e, runbook walkthrough) | 1.4 |
| External Postgres: provider identity query unavailable, and the person has not yet answered | No writes occur while the confirmation is outstanding | 3b (integration, real Postgres) | 1.4 |
| External Postgres: a recorded person confirmation exists, then one endpoint/database/reference changes before provisioning runs | Stale confirmation does not authorize the changed configuration; install stops and asks for renewed confirmation | 3b (integration, real Postgres) | 1.4 |
| A recorded person confirmation | Names safe connection identities (host, port, database); never a full credential-bearing DSN | 2 (impl, unit on the recorded-confirmation shape) | 1.4 |
| Install crashes immediately before the transition that would permit the first data consumer to start | Re-run finds the "initializing" marker alone; no consumer may have started; resumes safely | 4 (e2e, test cluster, simulated crash) | 1.2 |
| Install crashes immediately after that transition (a consumer may have started) | Re-run treats this as finalized; stops for manual recovery, never auto-resumes | 4 (e2e, test cluster, simulated crash) | 1.2 |
| A stale generation's recorded confirmation or progress record is presented after a newer generation has changed configuration | Rejected; cannot authorize provisioning or resume under the old generation | 4 (e2e, test cluster) | 1.2 |
| External Postgres: direct migration endpoint and the services' DSNs resolve to different backends | System-identifier check (read via each actual connection path) catches the mismatch | 3b (integration, real Postgres) | 1.4 |
| External Postgres: server reports an unsupported Postgres version | Preflight stops before any write; no provisioning side effects | 3b (integration, real Postgres) | 1.4 |
| External Postgres: TLS cert hostname mismatch | Connection refused, named cause logged, no silent fallback | 3b (integration, real Postgres with bad cert) | 1.4 |
| External Postgres: migrations run under the least-privilege role | Migrations succeed; role cannot touch another service's database | 3b (integration, real Postgres) | 1.4 |
| Pool-sum preflight: steady-state configured pools fit, but rolling-upgrade surge (old+new replicas overlapping) would exceed the limit | Preflight accounts for surge and stops with a recommended plan size | 2 (impl, unit on the sum/compare logic) | 1.4 |
| Pool-sum preflight: rollout capped at no surge (old stopped before new starts) | Preflight does not double-count replicas; passes if the non-surge sum plus headroom fits | 2 (impl) | 1.4 |
| Pool-sum preflight: worst-case sum plus headroom fits the plan limit | Preflight passes, install proceeds | 2 (impl) | 1.4 |
| Required key present but set to the empty string | Treated as malformed, install stops | 2 (impl) | 1.2 |
| Optional key (e.g. `ANTHROPIC_API_KEY`) absent or empty | No stop; feature stays off | 2 (impl) | 1.2 |
| Upgrade of an external-database install | Stored connection strings unchanged after upgrade | 3b (integration) | 1.4 |
| Lab adoption: `deploy.sh` → chart on a live install with a populated bundled Postgres | Existing Secret names, volume bindings, bundled Postgres PVC, and keys survive; backup exists first | 4 (e2e, on a disposable adopted copy) | 1.1 |
| `kubectl apply` of `install/k8s/postgres-init-configmap.yaml`-equivalent against external mode | Never applied/rendered in that mode | 2 (impl) | 1.4 |

## Decisions from Main (2026-11-09)

No open questions remain; Main ruled on all three:

1. A required key set to the empty string is malformed (stop), same as missing-with-data.
   Optional, omit-if-absent keys keep today's behavior.
2. Recovery is manual in 0.7.0: the stop message names the Secret and key and points at the
   runbook's recovery section. No guided recovery command.
3. The pool-sum preflight reserves `max(5 connections, 10% of the plan's limit)` beyond the
   computed sum.

Round 2 rulings:

4. One-cluster proof (rule 15) is the Postgres system identifier read through each DSN's actual
   connection path. Where the provider doesn't expose it to the setup identity, the install stops
   and the runbook asks the person to confirm the three databases are on one cluster, recording the
   answer. Equal host and port alone never counts.
5. The fresh-bundled-Postgres probe (rule 1) is a separate, explicit path: no data volume and no
   release-owned state at all, proven entirely from the Kubernetes side, before any database
   exists.

## Review round 1 (Codex, 2026-11-09)

An adversarial review found 4 high and 2 medium findings, all folded into the rules and test
matrix above (cited inline as "Finding N"): the data-presence probe must look past
`blerg-secrets`'s mere existence (1); an override or reference that silently changes a live
encryption key breaks ciphertext (2); concurrent installers and crash recovery need create-only
writes and a resumable record (3); uninstall must retain the bundled Postgres PVC too, not just
`blerg-secrets` (4); the pool-sum preflight must use worst-case rollout surge, not steady state
(5); the three external DSNs must be proven to share one cluster and a supported version before
any write (6). No disagreements — all six held up on inspection.

## Review round 2 (Codex, 2026-11-09)

A second adversarial pass found 4 high and 1 medium finding against the round-1 fix, all folded
in: a fresh bundled install has no database to probe yet, so the probe is now a mode-specific,
Kubernetes-side-only path before any Postgres process exists (1); naming the setup identity didn't
bound what it can see or stop broad administrative rights, so the probe now has a published,
bounded inventory and treats insufficient visibility as unknown, with a read-only, non-migrating
connection (2); equal host/port was accepted as cluster proof but a gateway or pooler can defeat
it, so proof is now the Postgres system identifier read through each real connection path, or a
recorded person confirmation when the provider can't expose it (3); the override-rejection rule
covered only encryption/signing keys, leaving `POSTGRES_PASSWORD` and the service keys open to the
exact outage `deploy.sh` already warns about, so it now covers every persistent, generated install
secret (4); and the bootstrap-resume rule conflicted with the unknown-is-never-absent and
malformed-Secret rules, so bootstrap now uses a narrow, generation-fenced "initializing" state with
atomic whole-Secret writes and a bounded resume window (5). The review also caught a stale
cross-reference (the DSN-immutability rule had moved numbers); fixed. No disagreements.

## Review round 3 (Codex, 2026-11-09)

**N1 passes.** A narrow review of round-2 closure found 0 high and 2 medium findings, both build
test rows rather than rule changes, added to the matrix above: the full bounded-inventory
coverage for rules 1–2 (a retained PV with no PVC left, a rebound PV, sessions-namespace-only
workspace data, live-data-authority-only state, installer-vs-runtime permission separation,
unreadable ownership/claim history) and binding manual confirmation and resume to the current
configuration and generation for rules 10/15 (no writes while a confirmation is outstanding, a
stale confirmation after a changed endpoint/database/reference, confirmations naming only safe
connection identities never full DSNs, and the crash-before/after boundary that lets the first
data consumer start). No rule changes; no disagreements.
