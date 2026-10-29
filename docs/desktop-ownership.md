# Desktop ownership and credentials

Desktop credentials are issued by core for one person and one stable desktop ID.
Runner persists the exact owner, an increasing ownership revision and the current
connection generation. A daemon name is a display label: two people may have desktops
with the same name, without sharing identity, paths, repository locations or authority.

Runner migration 077 extends existing desktop records without guessing their owners.
Core migration 026 stores only hashes of opaque `bdt_` credentials, with account,
desktop ID, revision, expiration and revocation metadata. Bearer transport is restricted to internal component-key issuance and the
authenticated daemon enrollment frame; browser responses never carry it. It is
never broadcast or logged.
A person-issued credential grants no agent, browser, admin or install-wide authority.
It cannot select another person's desktop by changing the hello name, desktop ID or
claimed revision. A bearer still authorizes its own bound identity if copied; do not
share it. Account disablement, revocation and expiration are checked by core at hello
and each heartbeat; local expiration is also checked on every frame and delivery.

## Install and upgrade

Existing unassigned desktops recorded at the 0.5 upgrade keep working automatically,
including launches, children and session file references. Runner migration 087
snapshots those desktops and the people with historical sessions on them. This
temporary access never assigns ownership or grants another person's desktop or
session files. New desktop rows do not enter compatibility. Desktops with no
historical people remain visible only in administrator assignment inventory.

Desktops are managed in core: **Settings → Account → Your desktops** lists each of
the person's desktops (enrolled, needs enrolling, offline, last seen) with Rename,
Remove and Enroll. A session on a desktop that needs enrolling shows a one-line
notice linking there. Core reaches the runner's internal desktop routes on
`BLERG_CORE_RUNNER_URL` with the internal key and the person's account; the runner
keeps every guard below. Choose **Enroll** there. The human login proves the person. Before confirming enrollment, review the
connection's reported hostname and OS user, connected-since time, and source
address as observed by runner. Select **Confirm enrollment** only if those details
match the intended desktop. Hostname and OS user are reported by the daemon;
the time and peer address are recorded by runner, without trusting forwarded
address headers (a reverse proxy may be the peer runner sees). The daemon must support the enrollment
frame; update it with `daemon/install.sh install` if Settings asks you to. The
upgrade installer preserves a working shared configuration while updating the
binary. The button needs no internal key and performs no reinstall. Only an
unassigned desktop or the current person's desktop is eligible. Administrators
can explicitly assign a desktop first.

The confirmation submits exactly the desktop ID and connection generation shown.
Runner checks both under the desktop fence before issuing a credential. If a
connection was replaced after presentation, enrollment is refused: select Enroll
again, review the new connection details, and confirm again. Old daemons that do
not report these details must be updated first.

The server issues a credential for the stable desktop ID, person and a new
revision and delivers it only over that confirmed connection generation. The browser
receives status, never the bearer. The daemon atomically stores the credential,
acknowledges and reconnects. A matching acknowledgment retires shared access; a
reconnect with the exact pending credential also proves it was stored if an ack
was lost. Every re-issue rotates the token and reserves a higher revision. Old
queued frames and acknowledgments cannot complete a newer request. Replacing the
shared connection invalidates its outstanding delivery. Until a personal welcome
confirms enrollment, a pending credential rejected after a lost delivery may
restore the original compatibility socket so Enroll can be retried. A confirmed
credential never falls back. The server still refuses shared access to owned
desktops. Issuance and completion
are transactionally audited on runner with actor, desktop ID, revision and time.
Explicit confirmations record `confirmed` and their displayed connection generation;
issuance, completion, already-enrolled results and refusals carry that generation
too. Authorized attempts refused before delivery record a `refused` outcome,
without bearer material; unauthorized foreign callers cannot write to this audit.

The all-in-one installer can also use its internal component key. It supplies
`BLERG_RUNNER_INSTALLING_ACCOUNT_ID` when configured; otherwise core selects the
oldest enabled admin. Create the account in setup before enrolling a fresh install.

The issuer response is saved atomically to
`~/.blerg-runner-daemon/desktop-credential.json` (0600 in the private daemon state
directory; `BLERG_RUNNER_DAEMON_STATE_DIR` can override that directory). The daemon
loads this file in preference to legacy environment credentials. Reinstalling uses
its recorded desktop ID. The install-wide server secret stays in the install's
private configuration; enrolling does not replace that secret with a personal token.

If Enroll is unavailable, ask the installation operator to create a private file
on the component host. No browser download or browser credential issuer exists.
The operator uses the private install configuration (including the internal key),
the desktop ID shown in Settings, and the intended enabled account ID:

```sh
python3 daemon/enroll.py --export /private/install.env /private/desktop-export https://runner.example.com DESKTOP_ID OWNER_ID
```

Run this from `install/desktop` on the component host. The helper calls the
internal bootstrap endpoint and atomically writes
`/private/desktop-export/desktop-credential.json` with mode 0600 in a mode 0700
directory, never printing the bearer. It preserves an existing desktop's recorded
owner; assigning another person remains an explicit administrator operation.
For a new desktop, pass an empty desktop ID (`''`); bootstrap creates a fresh identity.
Transfer that file privately to the desktop, then run:

```sh
daemon/install.sh import-credential /private/desktop-credential.json
daemon/install.sh install
```

Import accepts the private two-field format or the internal issuer format and
refuses shared or linked source files. Remove the transfer copy after import.
Neither the operator's internal key nor the credential belongs in a browser.
The old runner `/api/desktop-ownership/{id}/credential` and core
`/api/desktop-tokens` browser issuance routes have been removed.

## Add a new desktop with a one-time code

**Settings → Account → Your desktops → Add a desktop** creates a code bound to the
person (`POST /api/desktop-enrollment-codes`, human browser session only) and shows
one command for the new machine:
`BLERG_RUNNER_SERVER_URL=<runner>/ws/daemon install/desktop/daemon/install.sh install --enroll <code>`.
The code is shown once. Core stores only its hash (migration 030), it expires after
15 minutes, works once, can be revoked (`DELETE /api/desktop-enrollment-codes/{id}`),
and a person can create ten an hour. The installer passes the code to `enroll.py` on
stdin, never on a command line, and never prints or stores it. It posts it with the
desktop's name to the runner's `POST /api/desktops/enroll` (ten attempts a minute per
client address; behind an ingress, list it in `BLERG_RUNNER_TRUSTED_PROXIES`); the runner redeems it through core's internal
`/internal/desktop-enrollment-codes/redeem`, creates a new desktop identity (never an
existing record, even one with the same name), has core issue the credential for the
code's person and records the owner. Only the installer receives the credential.
Issuing, redeeming, refusing and revoking codes, and removing, renaming, enrolling and
assigning desktops are audited in core's `desktop_audit`; the runner audits the
enrollment (`code_enrolled`) and removal (`revoked`) in `desktop_enrollment_audit`.

## Remove a desktop

**Remove** (the owner, or an administrator for anyone's) makes the desktop unassigned
at a new revision, closes its connection, leaves upgrade compatibility and drops a
pending enrollment, and core revokes every credential it issued for it. The steps are
idempotent and ordered so that a retry finishes whatever failed: the runner authorizes the
person, core revokes the credentials, then the runner removes the desktop at the checked
revision; once removed, the runner's durable `revoked` audit record lets the same person (or an
administrator) finish core's revocation after a lost answer. A desktop added with a code is
owned and audited (`code_enrolled`) in one transaction, or the installer gets no credential.
To use the machine again, add it again.

## Assign or reassign

Settings → Account → Your desktops shows each person their desktops and state. Human
administrators also see everyone's desktops (read and remove) and unassigned ones that
are connected or in upgrade compatibility, and can assign those. No cross-person
repository inventory, paths, origins or runtime availability are included. Choose an
enabled account and assign it. The `PUT /api/desktops/{id}/owner` body contains
`owner_id` and the current `ownership_revision`; a stale revision is refused.

An assignment increases the revision, clears the current connection generation,
cancels pending desktop requests, removes and closes the old connection and emits
an ownership-change removal event to the old/new owners. Other browsers receive
only a metadata-free inventory invalidation. The new owner must enroll again.
Previously issued credentials fail the persisted owner/revision check. Session token
healing, recovery inventories and outbound session messages are checked against the
session's persisted owner too: reassignment does not transfer old session authority.

## Native helper migration

Codex/Hermes questions and the native engine's messenger, project tools and
explicit publishing tools use the existing per-session message credential. They
read its private token file on each request to honor renewal. They never send a
person's desktop bearer to an agent HTTP endpoint. Native project/publication
helpers require a live session on its exact persisted owner's current desktop
connection; wrong owner, obsolete connection generation and reassignment are
refused. Existing restrictions and exact single-project checks still apply;
folder sessions cannot use these unqualified project operations. This does not
broaden P0 member/project authority. Explicit legacy publication remains an
unguessable non-enumerable link; ordinary CLI session artifacts keep their existing
session scope.

## Frozen consumer interface for P1–P6

- `desktopauth.Binding`: desktop ID, owner ID, ownership revision, token ID and expiry;
  no bearer or private source path.
- `db.GetDesktop` / `db.ListDesktops`: persisted exact ownership, revision and connection
  generation. Filter by owner before collecting metadata. Unknown owners have no launch
  authority. The dedicated admin assignment inventory is a separate limited surface.
- `db.WithDesktopFence`: serializes hello, every desktop frame/delivery and assignment
  across server replicas using the existing bounded advisory-lock pool with its own
  namespace. A callback must complete before another assignment can enter.
- `db.BindDesktopConnection` / `db.ValidateDesktopConnection`: enforce exact owner,
  revision and current connection generation. New desktop identities start at revision 1;
  an existing unowned row can only be adopted through trusted enrollment/assignment.
- `db.AssignDesktop`: compare the expected revision, increase it and invalidate the
  connection. Its caller holds the desktop fence. `desktopOwnershipChanged` tears down
  pending requests and sockets after persistence. Old session ownership remains intact.
- `desktop_ownership_changed` carries desktop ID and revision only to its old/new owner;
  `desktop_inventory_invalidated` carries no desktop metadata. P1 must clear all affected
  browser caches and invalidate owner/generation-bound picker requests on these events.
- The install bootstrap endpoint is `POST /internal/desktops/bootstrap`, authenticated
  only with the component internal key. Its core issuer is
  `POST /internal/desktop-tokens/issue`; core validation is
  `POST /internal/desktop-tokens/validate`, also internal-key-only. No browser/daemon
  credential is accepted as a substitute for that key.

## Deprecation window

The default `BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=auto` (or unset variable) admits
only the persisted upgrade snapshot during **0.5**. Explicit `false` disables shared
connections. Explicit `true` permits unowned legacy connections but grants no new
desktop launch or discovery scope. No shared bearer can attach an owned desktop
by name. Compatibility is removed in **0.6**, after this release's session and
Settings warnings. Enroll every desktop before upgrading to 0.6.

Authenticated shared-credential refusals and closed sockets update status and a
safe reason immediately. Session recovery grace still preserves running work.
Automatic desktop config uploads remain retired, including in compatibility;
the historical install-wide archive is ignored. Personal configuration is never
distributed through a shared desktop credential.
