# Desktop ownership and credentials

Desktop credentials are issued by core for one person and one stable desktop ID.
Runner persists the exact owner, an increasing ownership revision and the current
connection generation. A daemon name is a display label: two people may have desktops
with the same name, without sharing identity, paths, repository locations or authority.

Runner migration 077 extends existing desktop records without guessing their owners.
Core migration 026 stores only hashes of opaque `bdt_` credentials, with account,
desktop ID, revision, expiration and revocation metadata. The bearer is returned once,
in a `Cache-Control: no-store` issuance response, and is never broadcast or logged.
A person-issued credential grants no agent, browser, admin or install-wide authority.
It cannot select another person's desktop by changing the hello name, desktop ID or
claimed revision. A bearer still authorizes its own bound identity if copied; do not
share it. Account disablement, revocation and expiration are checked by core at hello
and each heartbeat; local expiration is also checked on every frame and delivery.

## Install and upgrade

Finish active work, then run the desktop installer after core and runner have upgraded.
Legacy adoption fences the old connection; the installer asks its existing active-work
confirmation before enrollment changes ownership. The local installer
uses its existing internal component key to enroll the machine. It supplies the
installing account's ID from `BLERG_RUNNER_INSTALLING_ACCOUNT_ID` when configured;
otherwise core chooses the oldest enabled admin. The server preserves an existing
unowned desktop's stable ID and session associations. It never infers ownership from
a session launcher, a matching repository name or a daemon's self-reported account.
On a fresh install, create your account in setup and rerun `daemon/install.sh install`;
the first pass exits 7 with these instructions while keeping the stack available.

The issuer response is saved atomically to
`~/.blerg-runner-daemon/desktop-credential.json` (0600 in the private daemon state
directory; `BLERG_RUNNER_DAEMON_STATE_DIR` can override that directory). The daemon
loads this file in preference to legacy environment credentials. Reinstalling uses
its recorded desktop ID. The install-wide server secret stays in the install's
private configuration; enrolling does not replace that secret with a personal token.

On a hosted installation, sign in to core and call `POST /api/desktop-tokens` with
`{}` to issue a new desktop credential. Core chooses a fresh desktop ID at revision 1;
the caller cannot request another account or an existing/future identity. Save the
returned `binding.desktop_id` and `token` as the two fields `desktop_id` and `token`
in the private credential file. The hosted installer accepts that file. Alternatively,
set the person-issued `BLERG_RUNNER_DAEMON_TOKEN` and `BLERG_RUNNER_DESKTOP_ID` in the
installer's private configuration; enrollment copies them to private daemon state.
For an assigned existing desktop, its exact owner obtains the current credential with
`POST /api/desktop-ownership/{id}/credential` on runner, using their live human login.
An administrator acting as another person cannot issue that person's credential.

## Assign or reassign

Runner Settings → Desktop ownership is available to human administrators. It lists
only unassigned desktops and the administrator's own desktops; no cross-person
repository inventory, paths, origins or runtime availability are included. Choose an
enabled account and assign it. The `PUT /api/desktop-ownership/{id}` body contains
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

`BLERG_RUNNER_ALLOW_LEGACY_DESKTOPS=true` explicitly enables the old install-wide
credential during **0.5 only**. The default is false; support is removed in 0.6.
Use it only while finishing an upgrade to person-owned credentials. A shared credential
cannot attach an owned desktop by name, and an unowned legacy connection is not a normal
launch target or user-visible desktop. Only admin ownership Settings can discover it.
The legacy connection is fenced against assignment and cannot continue after the desktop
becomes owned. Automatic desktop config uploads are retired in P1, including legacy mode; the server
ignores the historical install-wide archive. See [workspace selection](workspace-selection.md#desktop-confidentiality-upgrade)
for removal and explicit operator-supplied configuration. Personal configuration is not
made available through a shared desktop credential.
