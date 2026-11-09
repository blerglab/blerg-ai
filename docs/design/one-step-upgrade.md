# One-step upgrade for every install type (BLG-171)

Status: design draft, 2026-11-02. No implementation or security approval is implied.
Baseline: local main `9644e115`. The supplied card is the requirements source;
board access is unavailable. Proposed remaining delivery estimate: L, high risk,
high complexity, because this grants deployment authority and crosses database
migration boundaries. These are draft estimates, not board updates.

## Existing behavior and scope

The 0.6.0 requirement is delivery, not an evaluation. The fallback already exists:
`install/desktop/blerg-up.sh --upgrade [vX.Y.Z]` selects a published stable release,
refuses dirty checkouts and live sessions, backs up the installation, rebuilds,
and reinstalls the daemon. The installer already invokes daemon installation;
there is no second reinstall step. The stale workspace upgrade section has already
been replaced in [the desktop README](../../install/desktop/README.md).
Do not repeat that edit or promise new command behavior in this draft.

[The earlier proposal](../upgrade-design.md),
[validation record](../upgrade-validation.md), and
[backup and restore guide](../backup-and-restore.md) remain relevant. The simulated
restore drill does not establish real Linux/macOS or Kubernetes restore acceptance.
This draft specifies the remaining UI and executor choices, and distinguishes
capability from install type: a laptop running Compose is a desktop installation;
a source-only or externally managed install may lack a supported web executor.

## Options and recommendation

| Decision area | Options and consequences | Recommendation |
|---|---|---|
| Desktop execution | Run inside the request or daemon: restart kills supervision. Detached process: survives some restarts but has weak lifecycle recovery. Independent host service: durable supervision, additional installed capability. | Independent fixed-purpose host service, invoking the existing Go upgrade orchestrator; daemon forwards authenticated requests. |
| UI availability | Command only: safe fallback but misses UI delivery. Unconditional button: falsely promises unsupported installs. Capability-driven button: accurately represents installed executors. | Extend the existing upgrade notice when an executor is enrolled; otherwise show the exact documented fallback and reason. |
| Release choice | Latest stable at execution: target can change after confirmation. Exact stable target resolved before confirmation: reproducible consent. Arbitrary refs/URLs: code execution authority beyond scope. | Resolve latest stable, bind confirmation to version and commit/image digests, and revalidate before mutation. Allow explicit published stable version selection; reject arbitrary refs, RCs and downgrades. |
| Kubernetes execution | Existing deploy command: no new web authority. Runner patches resources or creates Jobs: broad compromise impact. Opt-in dedicated controller creates fixed Jobs: narrower API, still privileged. External GitOps reconciler: preserves external ownership but requires integration. | Opt-in controller for installer-managed clusters, pending owner approval; command fallback by default. GitOps installations retain their operator workflow until a dedicated adapter exists. |
| Quiescence | Kill work automatically; wait indefinitely; refuse with actionable work list. | Refuse while any live sessions remain, including idle sessions; administrator finishes/stops them explicitly. Recheck after acquiring an admission fence. |
| Recovery | Automatic binary rollback; automatic full restore; explicit operator recovery. | Explicit recovery after migrations; old binaries must never run on migrated databases. Automatic old-service restart is allowed only on proven pre-migration backup failure. |
| Unsupported installs | Execute arbitrary configured scripts; build a fixed adapter; display manual instructions. | Fixed adapters only. Show existing install-specific instructions until one is supported; never claim a universal command works on unrecognized packaging. |
| Notification off switch | Hide notice only; disable all web initiation; disable even terminal administration. | Honor the existing notice off switch and additionally refuse web initiation while disabled; leave command recovery available. Owner must confirm this stronger policy. |

New orchestration tooling is Go; existing shell entry points stay wrappers. Do not
create a general remote command facility to implement an upgrade.

## Proposed operation and user flow

The existing notice owns a natural-width Upgrade action, admin-only. Its existing
release-information link remains useful when execution is unavailable. Reuse the
app's confirmation dialog and status patterns rather than adding a settings panel.
Confirmation shows old/target releases, sanitized release notes, downtime, backup
policy and the recovery procedure. A confirm action is intentional consent; “one
click” means one initiated workflow, not removal of the required confirmation.

Proposed API semantics (route names remain an implementation decision): an admin
prepares a target; receives a short-lived target-bound confirmation handle; submits
it with an idempotency key; then reads the resulting operation. The server checks
live human administrator status, same-origin/CSRF protection, install ownership,
executor enrollment and the off switch. Agent/session tokens cannot initiate,
confirm, cancel, or read privileged operation details. Repeat submissions attach
to the same operation, never start another installer.

Durable phases: validating → quiescing → backing up → installing → checking →
succeeded; failures become failed-before-mutation or recovery-required. Store ID,
requesting administrator, installation ID, old/target immutable identities, backup
manifest reference, phase, timestamps and safe failure reason outside replaceable
services. One exclusive operation per installation. Admission fencing prevents
new sessions and other application writers from racing backup; the fence and
operation survive restarts. A crashed worker requires reconciliation, not blind retry.

Desktop service checks the registered checkout identity, official upstream,
clean staged/tracked/untracked state, disk capacity and existing operation lock.
It uses a fixed executable and typed arguments, not browser-supplied paths or
shell text. Stop application writers, keep the database available for all three
dumps, preserve keys/settings, and write backup completion last. Fail before
checkout change if backup is incomplete. Preserve artifacts and retained workspace
volumes; communicate that database dumps alone are not a complete file backup.

The independent worker survives daemon/Compose replacement. It verifies service
release/commit, migration health and a newly accepted daemon connection before
success. The browser retains the operation ID, tolerates disconnects and resumes
status reads on reconnect/reload using existing recovery behavior. A missing
connection is neither success nor failure. Reauthentication may be needed; do not
restart the upgrade when status is temporarily unavailable. Expose only redacted
phases and recovery references, never raw installer output or backup secrets.

## Security boundary: installation authority

A human web administrator is not automatically an operating-system administrator
or cluster administrator. Host executor enrollment explicitly delegates the ability
to run trusted release installers with the install owner's existing privileges.
The host service trusts only the registered runner/install identity and authenticated,
replay-resistant target-bound requests. It independently validates release identity;
a compromised session cannot send it commands or choose another checkout. This
narrow schema reduces exposure but running a published installer is still code
execution as the install owner. Stable publication relies on BLG-322's release
order; it does not replace release-account and transport trust.

Kubernetes delegation is a separate owner decision, not implied by host enrollment.
Proposed installer-created controller uses a dedicated ServiceAccount and fixed,
pinned Job template. Runner may submit only an authenticated typed request, not
create arbitrary Jobs. Ordinary session pods have neither controller credentials
nor upgrade RBAC. Controller request authentication, nonce/expiry, install binding
and network isolation must be reviewed together.

Proposed RBAC envelope: one installation namespace; get/watch and patch only named
core, board and runner Deployments and the named runner agent-image configuration;
create/get/watch/delete controller-owned Jobs using a separate execution identity.
Kubernetes cannot constrain Job creation by resourceNames, and patch permission
cannot constrain Deployment fields. Fixed-template validation plus admission
policy must reject arbitrary images, commands, volumes, service accounts and
non-image Deployment changes. No cluster-wide writes, role binding, pod exec or
broad Secret listing. Backups use explicit database credentials and private storage
access, never permissions borrowed from session pods. Exact names/verbs must be
reviewed against the installed manifests before approval; this is an authority
envelope, not a ready-to-apply Role.

Changing only image tags may be insufficient for a release requiring manifest,
configuration or RBAC changes. Classify those releases as requiring operator deploy
until an explicitly reviewed migration adapter exists. Pin component images and
worker image to accepted release digests; capture old configuration and validate
rollout as a set. Remote workstation daemons need a compatibility check, not an
unsolicited remote reinstall. Reject unsupported version skew.

## Recovery and acceptance evidence required before shipping

Keep backups private, outside checkout/replaceable pods, with capacity preflight
and an owner-approved retention policy. After migration starts, partial rollout or
uncertain completion means recovery-required: preserve evidence and fence writes.
Restore all three databases, matching keys/settings and the recorded old release
as one operator procedure; warn that writes since backup are lost. Restoring only
images is permitted only when schema compatibility is explicitly proven. Never
label a successful backup as a tested restore.

Implementation proof must cover stable/draft/RC/moved-identity rejection, dirty
checkout, insufficient space, live-session/admission races, backup failure,
idempotent confirmation, revoked/non-admin/session-token access, disabled switch,
worker/daemon/server crash and reconnect, partial rollout and unsafe rollback.
Use deterministic contract and implementation tests before boundary integration;
real disposable Linux/macOS upgrades and restores plus a Kubernetes upgrade/restore
are release acceptance. UI checks include keyboard focus, cancel, expired consent,
reconnect and 390/3840 px states. No such product tests are claimed by this draft.

## Owner decisions still open

1. Approve independent host-service authority, supported OS service managers and
   explicit enrollment/disable behavior; may any site admin upgrade an enrolled
   host, or only its owner who is also a site admin? Recommend the latter.
2. Approve or reject the opt-in cluster controller and its exact reviewed RBAC,
   request authentication, admission policy, backup storage and deployment scope.
3. Confirm the off switch blocks web initiation as well as notifications; decide
   where an administrator intentionally re-enables it using existing settings.
4. Choose backup retention/capacity limits and recovery ownership. Recommend no
   automatic deletion of the latest known recovery set or automatic post-migration restore.
5. Define the 0.6.0 supported install matrix, including source-only, GitOps and
   offline installs. Recommend honest fallback for unsupported web executors;
   decide whether that meets the all-install delivery requirement.
6. Accept real upgrade/restore drill evidence and workstation version-skew policy
   before release. Recommend refusing unsupported skew and manifest-changing
   cluster releases rather than expanding controller authority implicitly.
