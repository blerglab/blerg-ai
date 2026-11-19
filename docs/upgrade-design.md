# One-step upgrade proposal (BLG-171)

Status: partially implemented for 0.6.0. The desktop command
`install/desktop/blerg-up.sh --upgrade [vX.Y.Z]` now implements stable selection,
private backups, checkout validation and persisted recovery. It runs in the
operator's terminal; the independent host-service worker, administrator UI and
reconnect flow below are still proposed. Real upgrade and restore drills remain
required. Cluster web authority remains pending the owner decision below.

## Owner decision required

May an administrator initiate a cluster upgrade from the web application, using
an opt-in installer-created upgrade controller with permission to change the
three Blerg Deployments and the runner agent-image setting?

Recommendation: opt in during cluster installation; keep the runner server's
service account unable to create upgrade Jobs or patch Deployments. A dedicated
controller accepts only a fixed upgrade request schema and creates Jobs from a
fixed template. Do not give the web process arbitrary Job creation: that permits
arbitrary pod specifications and can become access to secrets or service accounts.
The controller, its image allowlist, and its request authentication need a security
review before this authority is installed. Existing clusters retain the command
fallback until an operator explicitly installs it.

The owner must approve or reject this additional cluster authority before
implementation. Narrow Deployment patch permission alone is insufficient: the
current cluster installer also updates configuration and other resources, and a
patched Deployment can run arbitrary code under its existing service account.

## Shared contract

Implement new orchestration and validation tooling in Go. Desktop shell remains
a small entry point to that tool. Backend adapters register desktop and Kubernetes
operations behind the same durable operation contract; unsupported installs show
an actionable command fallback rather than pretending to support a web upgrade.

Each operation records an ID, requesting administrator, install identity, old and
new commit/image identifiers, release version, backup reference, timestamps, and
phase: validating, quiescing, backing up, installing, checking, succeeded, failed,
or recovery required. Persist before every destructive step outside replaceable
containers. Only one operation per installation may run; retries attach to its ID.
No arbitrary command, filesystem path, repository URL, or image name comes from
the browser. Status responses omit process output and secrets.

Resolve a release through the configured trusted upstream's published release
metadata. Accept exact vX.Y.Z names only, excluding drafts and prereleases, then
fetch the exact tag into an isolated ref and record its commit. Refuse a mismatch
with previously recorded release identity. A Git tag alone does not establish
that a stable release has been published. Do not trust locally shadowed tags.
Revalidate the selected identity immediately before installation.

## Desktop command and daemon

Proposed fallback: `./install/desktop/blerg-up.sh --upgrade [vX.Y.Z]`.
Without a version choose the newest published stable release by semantic version.
Refuse tracked, staged, and untracked checkout changes, active Git operations,
missing upstream identity, concurrent upgrades, and incompatible option mixtures
such as --upgrade with --no-build, --no-daemon, or --reset-admin. Fetching must not
move local branches or overwrite local tags. Preserve the original checkout's
branch and commit in the operation record and install at the resolved detached
commit. Re-execute the selected release's installer only after validation and
backup. The current installer already calls daemon/install.sh install.

Do not force a restart through active agent work. The UI lists the interruption
and asks the administrator to finish or stop that work first. The upgrade worker
must outlive both the daemon and the Compose services it replaces: launch through
a separate host service, with a fixed executable and argument schema. Persist its
status in a private host directory. A daemon WebSocket handler that directly runs
its own installer cannot reliably report completion after replacing itself.

Before migrations, stop writers and new session admission, drain work, then dump
all three databases and preserve the existing encryption keys, installer settings,
and daemon settings using the existing backup format. Use private permissions,
exclusive filenames, checked exit statuses, and a completion manifest written last.
Never put backups inside the checkout. On backup failure, restart old services and
refuse checkout or migration. Keep runner artifacts and retained workspace data;
a database backup does not replace the runner data-volume backup.

After install, verify all services, migrations, and daemon reconnect/version before
marking success. A lost browser connection is not evidence of either failure or
success. On reconnect, resume polling the persisted operation ID. Do not blindly
retry an install whose result is unknown.

## Administrator UI

Extend the existing upgrade notice and honor its administrator off switch. The
server rechecks live administrator authorization on every mutation and status
read; hiding a button is insufficient. Require same-origin request protection.
The confirmation shows the chosen stable release, plain release notes, backup
location policy, expected interruption, and recovery instructions. Confirmation
binds to a short-lived server-side selection of the exact target identity.

The page shows persisted phases and retries status reads through restarts. Keep
the operation ID across reloads. A failed install offers its recovery record and
exact command fallback; do not display raw installer logs containing credentials.
Test keyboard focus, confirmation cancellation, expired authorization, non-admin
requests, disabled notices, reconnects, and layouts at 390 and 3840 pixels.

## Cluster execution and recovery

A fixed Job validates the published tag and pinned image identities, records the
current Deployments and runner agent image, blocks new work, drains existing work,
and backs up all three databases and encryption settings to operator-controlled
private persistent storage. Only then change the registered component images and
agent-image configuration together. Check rollout health and stamped versions;
record partial rollout as recovery required. Include workstation daemon version
compatibility: a cluster image rollout cannot silently upgrade a remote laptop.

The approved RBAC design must enumerate resourceNames, verbs, namespace, backup
access, Job identity, admission constraints, and controller/request credentials.
Avoid broad Secret listing, cluster-wide permissions, or permissions inherited by
ordinary session Jobs. Deployment resourceNames restrict targets, not patch fields;
controller validation must restrict the actual changes too.

Rollback never automatically runs old binaries on migrated databases. Retain exact
old images/commit and backup manifests. If forward recovery fails, an operator
stops writers, restores databases plus matching keys and data, and reinstalls the
old version using the existing restore runbook. State plainly that restores discard
writes made after the backup. Do not delete backup files automatically on success.

## Implementation and evidence plan

1. Failing Go tests with local bare Git repositories and a fake release server:
   dirty refusal, missing/draft/prerelease rejection, semantic ordering, shadowed
   and moved tags, backup failure, locking, restart recovery, rollback metadata.
2. Implement command fallback, operation persistence, and desktop worker; exercise
   actual previous-release to candidate migration and daemon reinstall on scratch
   Linux and macOS installs. Unit tests alone do not establish restore safety.
3. Implement authorization and UI with focused frontend and browser tests.
4. After owner security approval, implement the cluster adapter and exercise
   negative RBAC tests plus actual upgrade and restore on a disposable cluster.
5. Replace the desktop README's stale workspace status with verified one-command
   instructions when that command exists. Do not document the proposal as shipped.

The previous-release/candidate drill must retain exact versions, commands,
migration outcomes, daemon reconnect, artifact/workspace checks, and restore
results. No such drill has been performed by this design change.
