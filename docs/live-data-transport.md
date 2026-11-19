# Live-data transport

Polling remains disabled. These listeners supply only the dedicated approval and
metadata transport; they do not provision an authority epoch or start a poller.

Core and Runner each bind an internal TLS 1.3 listener on port 8443 when
`BLERG_LIVE_DATA_TLS_DIR` names a valid bundle. Missing, invalid, expired or soon-to-expire
bundles disable only live-data. The application still starts and serves sign-in
and ordinary routes. Readiness stays successful, with the fixed
`X-Blerg-Readiness-Warning: live_data_tls_unavailable` header. Runtime probes
report `live_data_tls_expiring` starting 30 days before expiry, and live-data
requests are refused in that window: the 90-day leaves effectively serve for 60 days. The cluster renewal Job refreshes leaves daily, and running listeners follow their own projected leaf Secret. Desktop renewal requires rerunning the initializer and restarting both services. Failures emit rate-limited cause labels.
An absent setting leaves this listener off. There is no HTTP
fallback. The public service clients and shared internal key do not supply this
identity. Certificates must chain to this installation's dedicated Ed25519 CA and
carry the exact peer URI. Server DNS verification remains enabled.

The Core URI is `spiffe://blerg/installation/<installation UUID>/core`; Runner uses
`spiffe://blerg/installation/<installation UUID>/runner/<runner UUID>`. The
installation and Runner UUIDs survive certificate renewal. The listener exposes
only existing live-data approval/reconciliation routes on Core and the metadata
callback on Runner. Forwarded headers are refused. The verifier and MCP invocation
paths are not exposed by either listener.

## Provisioning and renewal

`blerg-core live-data-pki --root <private directory>` creates an `authority/`
directory and separate `core/` and `runner/` runtime bundles. It preserves an
existing authority and fails on an incomplete authority. Only offline provisioners and the dedicated cluster renewal Job
receive the CA private key; runtime containers never mount it. Each runtime bundle contains its own
private key, leaf certificate, public CA and identity manifest. Keys are mode 0600;
Kubernetes projects them as group-readable 0440 for the service's group 65532.

The CA lasts ten years. Leaves last 90 days and are renewed on every provisioning
run; services load them at startup. Renew and restart both services before their
leaves expire. A CA that cannot cover another 90-day leaf is refused and needs an
operator-managed CA replacement. Preserve the authority independently of database
backups. This is certificate state; the separate monotonic authority epoch is
still an enabling prerequisite.

Desktop Compose runs the network-isolated `blerg-live-data-pki` initializer before
Core and Runner. Three named volumes separate the authority and the two runtime
bundles. Runtime mounts are read-only; nothing is written to the host `.env` or
agent mounts. To renew on an unchanged installation, run the initializer again
with `docker compose run --rm blerg-live-data-pki`, then restart Core and Runner.
Container-runtime administrators can access these volumes: the desktop posture
restriction applies: when `BLERG_LIVE_DATA_DESKTOP=true`, polling refuses
even an otherwise-safe posture unless the owner explicitly consents in Settings.
Agents in the Docker group can read the desktop CA and polling credentials.
The per-binding Core and Runner gates require the epoch-bound Settings consent;
a service key, safe-runtime assertion or configuration flag cannot substitute.

Cluster `deploy.sh` calls `provision_live_data.sh` after namespace creation. It
reads the dedicated authority Secret, runs the Core provisioner without network
access and applies three separate Secrets with server-side apply. A failed authority
read aborts provisioning; it never silently creates a replacement CA. Any
provisioning failure warns with a Docker ownership/user-namespace/SELinux fix hint
and continues application deployment with live-data explicitly disabled. Bind
mounts use `:Z` for SELinux.

Service leaf Secret mounts are optional: acceptance and ordinary installs without
these Secrets still start. No CA or renewal Job is created in the shared
acceptance namespace. Core and Runner mount only their own leaf Secret, never the
CA, including during initialization. The dedicated `blerg-live-data-renew` CronJob
runs daily using its own ServiceAccount and the Core image. Its only API rights
are get/update on the two existing leaf Secrets: no issuer-key, deployment, pod,
exec, create or list access. Only that Job mounts the CA private key. It validates
both existing installation identities and roots before updating either Secret,
and preserves resourceVersion for compare-and-swap. A failed or partial rotation
can be retried safely under the same CA and identity; it never creates an authority.

Running listeners load their own projected leaf on each TLS handshake and verify
it against the original identity and root. A bad or partial projection refuses
live data without falling back to an old private key. Dedicated clients establish
a new connection per request so renewed client certificates take effect too.
The Job needs no deployment patch permissions or service restart. The unchanged
CA, installation UUIDs and issuer keyring preserve approvals. Missing startup
bundles still leave the optional listener off; after repairing initial provisioning,
restart that service to open it. Deploy-time public certificate hashes still
restart services on explicit reprovisioning. Monitor failed Jobs: a sustained
renewal outage reaches the documented 60-day serving cutoff and refuses live data;
sign-in and ordinary API service continue. An operator can run renewal immediately
with `kubectl create job --from=cronjob/blerg-live-data-renew <job-name> -n blerg`.
Separate ClusterIP services expose 8443, with a NetworkPolicy
limiting only port 8443 to the opposite service in the control-plane namespace.
The policy deliberately preserves existing 8080 web and 8090 gateway ingress;
it is not an installation-wide default-deny policy. There
is no ingress route or host-published port. The agent namespace receives none of
these Secrets or access to them.

On a suspected CA or leaf-key compromise, keep polling off, replace the authority
and both leaf bundles through the operator's secret-management process, and
restart both services. Do not restore an old authority as a compromise response.
TLS and refusal logs contain fixed, rate-limited cause labels; they exclude
headers, certificate subjects, request content and raw handshake errors.

## CA backup and restore

Back up the dedicated authority Secret (or desktop authority volume) with the
installation and Runner identity manifest in the operator's encrypted secret
backup system, independently of database snapshots. Limit recovery access to
operators; never publish these files or put them in an agent namespace. Missing
CA material disables live-data and never justifies generating a replacement from
a partial authority. Restore the matching CA and identity set before renewing
leaves. A suspected compromise requires a replacement CA, both leaf bundles and
service restarts, rather than restoration of the compromised CA. Certificate
restore does not restore polling authority: the separate external epoch must
advance and restored grants require fresh approval under the recovery rules.

## Issuer and independent authority state

The issuer transport is mounted only on Core's dedicated certificate listener,
under `/internal/live-data/issuer/`. Runner uses a separate in-process service
client for mint, renewal, public keys, fresh authority checks and invalidation.
It supplies no provider URL, arguments or mapping. Every returned snapshot contains
the full approved configuration, and the existing verifier rechecks its digest.
No credential-cache hit substitutes for the fresh durable check or audit.

The production issuer gate is a constant false in this checkpoint. Provisioning
state or granting Settings consent cannot activate it or the Runner poller. Host
posture remains conservatively unsafe until the detector is implemented.

Desktop uses the Core-only `blerg-live-data-issuer-state` volume, independent of
Postgres and separate from CA/leaf volumes. Cluster uses the dedicated
`blerg-live-data-issuer` Secret, outside the database snapshot. Only Core's
existing `default` service account receives a narrowly scoped Role to get/update that exact Secret; the Role grants no list/create rights or rights to other Secrets. Core keeps its existing service account for Vault transit compatibility. Signing state is not mounted into the pod. Fresh authority checks read the current Secret through
the Kubernetes API, rather than trusting the projected Secret's refresh cache.
An API outage refuses authority; it does not stop sign-in or use cached approval.

Ordinary Core starts reload the same persisted signing key and authority epoch;
current approvals survive restarts. Each operation reads external state afresh.
An already-running Core observes an explicit fence on its next operation and
remains fenced once superseded. Transient external-store outages refuse calls
without permanently revoking an unchanged epoch. Filesystem updates use a private,
fsynced atomic replacement and interprocess lock; cluster updates use
resource-version compare-and-swap.

Database restore is an explicit fenced operator procedure: stop Core and dispatch,
restore only the database, preserve independent issuer state, then run
`blerg-core live-data-fence --reason restore` with the same authority backend and
mounts before restarting Core. This command rotates the persisted key and advances
the epoch; startup revokes only grants whose epoch differs from external authority.
Use `--reason compromise` for compromise response or `--reason operator` for an
explicit administrative fence. No HTTP/agent route exposes this command.

Core also detects a database-only restore: a monotonic revocation high-water mark
is persisted alongside the external keyring and compared with the database at boot.
A lower database mark disables live data, with a readiness warning and a bounded
`restore_detected` failure event. Sign-in stays running. Run the operator fence
before retrying; restarting or toggling an environment variable cannot clear it.
Backups must never include external issuer state.

Missing/corrupt state, compare-and-swap failure or failed grant invalidation leave
the issuer unavailable and the application running. Never restore older external
state alongside a database snapshot or reset initialized state to epoch one. Lost
state requires an operator-established epoch above every previously observed Runner
high-water mark; a database snapshot is not evidence of that mark.

An empty external state is initialized only when the database has no live-data
grant, proposal or credential-audit history. If history exists, even a newly
created empty volume or Secret is treated as lost authority and stays off. This
check uses database history only as a refusal signal, never as an epoch source.

Explicit durable authority refusal uses a distinct transport error from a
transient Core/control-plane outage. Runner retains the safe typed failure cause
for the scheduler, so revocation can purge payloads while a temporary outage
backs off without being mistaken for renewed authorization. Revocation requires
an explicit acknowledgment; a generic successful HTTP page is insufficient.

## Deployment recovery and remaining isolation work

The desktop initializer logs provisioning failures and exits successfully so sign-in
and ordinary application routes can start with live-data off. New authorities are
written into a temporary directory, then renamed to `authority/bundle` only when
all three files are complete. Interrupted staging directories are ignored; an
incomplete committed or legacy flat authority is refused, never silently replaced.
Restore its matching CA, key and identity from the operator backup before retrying.

Cluster renewal is isolated in the BLG-520 dedicated daily Job. Its own service
account updates only the two existing leaf Secrets and has no issuer-secret or
workload-control permission. Core and Runner service pods receive their own leaf
bundles and never mount the CA in any container. Services follow valid
same-identity, same-root renewal without stale-key fallback; leaf renewal does
not change the authority epoch. Polling remains disabled.

If `authority.lock` exists without `authority.json` in the desktop issuer-state
volume, startup refuses initialization and leaves live-data off. Keep polling off;
stop all Core instances and preserve the volume for diagnosis. Restore the matching
issuer state from a trusted backup only with an epoch above every Runner high-water
mark. Do not delete the lock to force epoch-one initialization. If no trustworthy
state remains, operator recovery must establish a fresh keyring and an explicit
higher fence before approvals resume.

Restart persistence and explicit fenced restore have separate-process proofs.
See the [rule-by-rule proof index](design/charts-mcp-proof-index.md) for coverage
and remaining enablement limits; proof of restore does not enable polling.

Board and Postgres use separate `blerg-board` and `blerg-postgres` service
accounts with token automount disabled on both the account and pod. Neither
requires the Kubernetes API: Board uses SQL and Core/Runner HTTP clients;
Postgres uses its database protocol and local initialization SQL. Secret and
ConfigMap volumes/environment are supplied by the kubelet, without pod API
credentials. Only Core retains the default account bound to the issuer Role;
Runner uses its existing separate launcher account.

## Disabled scheduler and card delivery checkpoint

The Runner now has a durable due-time/lease loop with up to four workers, fresh
minting for every attempt, shared quota consumption, bounded backoff and jitter,
and no catch-up burst. Its existing maintenance loop retains attempt metadata for
180 days, removes payloads after 24 hours and auto-pauses after seven days without
views. The production polling gate is still false; configuration cannot enable it.

The in-process provider adapter uses the existing gateway network policy and MCP
transport, always lists tools afresh, and performs one tools/call POST without
retrying an ambiguous result. It bypasses the legacy credential cache and session
proof path. Dedicated Core mTLS releases a connection credential only after a fresh
capability/grant check and a committed single-use Runner receipt: one for listing,
and another after attempt consumption for invocation. Expiry is bounded by the
capability. Core rechecks durable authority after credential resolution. Correlation
IDs connect Core credential audits with Runner attempts; no arguments or results
enter either audit. No provider credential is serialized to a card or agent.

Cards reference only `{kind: "dataset", name: "blerg.mcp", parameters: {binding:
"<binding UUID>"}}`. The existing socket and keyed card updates carry owner-only
normalized rows. Collection and queued delivery independently recheck owner,
workspace, current Core authority, epoch and approved configuration; a changed
revision fences an already queued sample. Visible subscription/heartbeat demand
expires after 60 seconds. Source/authority checks run on the subscription's
one-second local sequence timer. A new sample or local fence triggers a refresh;
unchanged samples recheck Core every five seconds. Provider calls follow the
approved interval. Transient
outages retain last good rows with their true observation timestamp. Explicit
authority loss purges rows and pauses the binding. Mapping failures pause while
retaining the previous authorized sample. No background agent is involved.

This checkpoint uses Core migration 160 and Runner migration 161. Real-process
market and real-app scalar tests use visibly simulated provider data. Host posture and BLG-520 renewal Job isolation are implemented. The
[proof index](design/charts-mcp-proof-index.md) separates inherited process proofs
from current browser evidence and remaining enablement limits. Polling stays off.

## Revocation durability checkpoint

Core migrations 160–161 retain a transactional high-water row and nontransactional
reservation/acknowledgment sequences. A first grant revocation or deletion commits
a local deny and retry ticket in the human action's transaction. Password reset,
sign-out everywhere, account disable and policy reductions do not wait for the
optional external store. Already revoked rows allocate no new ticket.

The checkpoint worker retries every 100 milliseconds, advancing the independent
filesystem or Kubernetes authority mark before acknowledging a ticket. Pending
work and external-store outages refuse live-data authority; they do not roll back
the human action. Recovery reauthorizes only after acknowledgment, and never
resurrects the revoked grant. An idle worker does no external writes or audits.
A rollback after an externally checkpointed reservation remains conservatively
ambiguous and requires an operator fence.

There is deliberately no synchronous external durability claim for an action
committed during an authority-store outage. A crash can leave a SQL-only retry
ticket. Restoring a snapshot from before that ticket cannot be detected from the
older database alone: every database restore must stop dispatch and advance the
independent epoch with the operator fence before live-data routes resume. The
external checkpoint can additionally detect rollback of acknowledged history;
it is not a replacement for that fenced restore procedure. Polling stays disabled.

The operator fence retains the external high-water mark while rotating the key
and epoch. A newer fenced epoch can rebase restored counters only when all stored
grant epochs are older; startup then revokes those grants before serving authority.
Old processes cannot acknowledge reservations across the fence. Database roles and
sequence access remain implementation conventions, not an isolation boundary
against a database administrator. Restore only while Core and dispatch are stopped.

## Subscriber authority and read audits

Subscriber collection, demand and queued delivery share at most one second of a
binding's Core authority result, keyed by the exact approved scope and sample
sequence. New samples invalidate that result immediately. Owner/workspace filters,
local authority-loss purges, local expiry and the Runner's durable highest epoch
are checked before serving every frame, including cache hits. Other accounts never
reach the shared result. The cache contains bounded metadata only (4,096 bindings),
no rows or credentials, and coalesces concurrent readers without holding a lock
across the network request. A canceled reader cannot reuse a warm result.

An unchanged visible sample triggers a periodic Core check every five seconds,
instead of one request per subscriber every second; changed samples and local
fences are noticed by the one-second sequence timer. Remote revocation therefore
purges previously displayed browser values within five seconds plus the bounded
one-second read reuse and transport time. Known local revocation refuses queued
frames immediately. Dispatch, credential release, fresh listing, post-call authority
and result-storage checks never use this subscriber cache.

Core still reads durable authority on every invocation. Successful unchanged read
checks do not append audit rows; first authorization, changed approved scope,
refusals and recovery do. This uses a bounded process-local metadata map, so restart
or eviction may add a state audit. Mint, renewal and each provider credential
exchange still write their required durable audits, including the exact attempt ID.
Unchanged Runner epoch observations use a read rather than rewriting the epoch row.

## Runtime posture before approval and dispatch

Core asks only the configured, certificate-authenticated Runner for a fresh
posture report. Agent hello fields and request bodies cannot declare a machine
safe. Managed desktop installs always require the owner's existing Settings
consent: agents in the Docker group can reach container credentials. Other
host-connected or unverifiable runtimes also require consent. An unavailable
assessment refuses approval and dispatch; it never becomes an isolated result.

For cluster isolation, Runner checks its authenticated daemon inventory, enrolled
hosts and active host sessions, then lists every live pod in the agent namespace
through its existing Kubernetes identity. Each connected cluster runtime must
match a pod. Host namespaces, service-account tokens, privileged containers,
unsafe capabilities, host or secret volumes, runtime sockets and credential or
runtime-control environment references refuse isolation. Pagination and response
size are bounded; incomplete inventory fails closed. No positive posture cache is
used. Core bounds the callback to three seconds and rejects timestamps outside
30 seconds including transport uncertainty. Approval checks the report is still
at most five seconds old after acquiring its transaction locks; upstream work
happens before those locks.

The existing UID-separated controller has a container-root entry point that drops
to a separate controller UID before running the agent. It is accepted only with
the current configured agent image and the exact managed command, capability and
mount restrictions. Container root alone does not imply host administration.
Custom or older root entry points are conservatively unsafe; update those pods
or explicitly consent in Settings. Reports contain only a Boolean, a fixed cause
and a timestamp, never host names, manifests or secrets. Polling remains disabled.
