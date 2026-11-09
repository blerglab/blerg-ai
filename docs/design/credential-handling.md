# Credential handling (0.7.0)

Status: draft for owner review, 2026-11-09, revised after adversarial review round 1 (see the end). Card: BLG-611. Related: BLG-574 (Claude renewal
spike), BLG-323 (engine sign-in on every target), BLG-553 (token storage audit), BLG-565 (user
secrets unreadable by admins), BLG-563 (email sender), BLG-566 (general notification email,
0.8.0). Pulls together rules already decided in `team-scope.md`, `accounts-and-placement.md` and
`managed-install.md`; where this document repeats them, those documents' review sections remain
authoritative.

## Why

The startup's first week depends on each person's own Claude and Codex credentials keeping their
Mains and workers running on a cloud install. Credentials have to get in easily, stay alive
without anyone babysitting them, and fail loudly to the right people when a human is needed.

## Goals

1. **Adding a credential happens in the browser.** Each person adds their own credentials from
   personal settings: **[+] → Claude or Codex**, as many as they like (Simplification below).
   The card runs that provider's sign-in from the browser: no laptop CLI, no local callback. The
   mechanics differ by provider (see "Sign-in per provider").
2. **Core keeps the long-lived credential and renews it.** The refresh credential is stored
   encrypted in Core. Core renews before expiry, serialized and generation-fenced, checking the
   invoking person's authority on each refresh (accounts design, call 5). Sessions receive only
   short-lived access.
3. **When a human is needed, the right people hear about it** (owner, 2026-11-09):
   - **Email** to the credential's owner, the only person who can fix it. This narrow case uses
     the email sender being added for sign-in (BLG-563) and ships in 0.7.0; general notification
     email stays in 0.8.0 (BLG-566).
   - **Meters:** the credential's segment turns red; its hovercard and the Accounts usage tab say
     "needs sign-in".
   - **Banner** for the owner, linking straight to the credential's card to sign in again.
   - **No Needs you item** for this, so the same thing doesn't appear in four places.
4. **Workers on a credential that needs sign-in pause with the reason**; signing in again from the
   card lets them resume. A worker never switches to another credential on its own; Main picks
   another of the person's credentials for new work.

## Non-goals for 0.7.0

- General notification email (BLG-566).
- Team-owned AI credentials and sharing a credential with anyone (0.8.0 at the earliest, as
  service identities alongside team secrets; Simplification below).
- Team MCP connections and team secrets (0.8.0, with BLG-145).
- Automatic rotation of provider subscription logins (providers control those; Blerg renews what
  the provider allows and asks a human otherwise).

## Sign-in per provider

The providers' own clients decide what works; none of these redirect back to the install.

- **Codex: device code.** This ships today (`docs/design/engine-credential-lifecycle.md`): the
  card shows a code and opens the provider's page; the person signs in there and enters the code;
  Core polls and stores the result. The attempt is bound to the person's browser session.
- **Claude: probably code paste.** Claude's sign-in client appears to allow only fixed redirect
  addresses, so it probably can't finish at the install's own address. The expected flow: the card opens the provider's
  page, the person signs in, the provider shows a code, and the person pastes it into the card.
  The BLG-574 spike confirms this, and renewal, before the UI is built (call 1).
- **API keys are not the fallback for subscription seats** (owner, 2026-11-09: "API key is not
  acceptable"). A person may still add an API key card by choice (billed per use, visible to
  sessions for its lifetime, as the card says), but the startup's first week must work on
  subscription seats.
- Every flow is a bound transaction (call 2): the state is single use and tied to the person,
  the browser session, the card and the generation. "Exact allowlisted redirects" in call 2
  applies only to a provider that does redirect.

## Personal Git tokens

Git access in 0.7.0 is personal (owner decision below). *Main's calls:*

- **Reachability check.** When a person connects a Git token, when they include a project in a
  workspace, and once a day, Blerg checks the token can read each repository registered on the
  project and push to its target branches where the host can tell. The card lists each
  repository as passing or failing, with the reason. It reveals nothing about repositories the
  person can't already see.
- **Expiry is a typed incident**, like needs-sign-in (call 6): states are expiring, expired,
  revoked, and lost access to a project repository. Blerg reads the expiry date when the host
  reports it and warns 7 days ahead.
- **Needs attention.** Only the token's owner can fix it, so only they hear: an email, a banner
  linking to their Git card, and a red state on the card. Workers that need to push pause with
  the reason; the merge queue's push preflight sends items back with that reason
  (`merge-queue-and-delivery.md`, call 3). Blerg never substitutes another person's token.
- **Acceptance** (in the first-week gate): each of the three people connects their own token;
  it passes the reachability check from a cloud workspace; they clone, fetch and push a branch
  with their own commit identity (the token authenticates; the author name and email are set
  separately); the merge queue pushes with it; and an expired or refused token sends work back
  without using anyone else's.

## Rules gathered from the other designs

- **Exposure (owner):** refresh tokens never enter a session; access tokens are short-lived and
  visible to code running in the session, which can also learn which account it is; static API
  keys are visible for their lifetime, and the card says so. A credential is usable only by its
  owner's own sessions. Privacy promises cover Blerg's own records, not what a session prints; Blerg
  redacts known token shapes on a best-effort basis (team scope, call 10).
- **Revocation (owner, 2026-11-09):** deleting a credential, removing a member or an org policy
  denial (for example a provider the org no longer allows) refuses new work and stops issuing access to whoever lost it at once. Dependent
  running work stops at once when its executor is reachable; an unreachable executor stops itself
  within 30 seconds. An access token already handed out lasts until it expires (team scope,
  round 2, decision 1 and call 5). A disabled or compromised account's personal work on its own
  machine stops when the daemon reconnects (team scope, call 5).
- **Ownership:** every AI credential is personal: never shared, never transferred. An org may
  restrict which providers its workspaces may use (accounts, call 2).
- **Same login on several cards:** allowed and not coordinated. Each card is separate; cards on
  the same provider login show the same provider quota on their meters (Simplification below).
- **Install secrets** (database passwords, encryption keys, service keys): generated once, fail
  closed when data exists but keys are missing, never in Helm values or history, external stores
  by reference (managed install, calls 3 and 4).

## Rotation

- **Install secrets:** a documented, coordinated rotation for database passwords, the encryption
  key, and service keys: create the new value, roll consumers so they accept both, switch, then
  retire the old value. Backups record which key generation they need. Rotation never runs during
  an upgrade.
- **Credential encryption key:** in 0.7.0, only the documented emergency rotation, which may need
  a brief planned outage. Re-encryption in place without downtime moves to 0.8.0 (see the end).
- **Offboarding:** removing a person stops their work at once (within 30 seconds on an
  unreachable executor). With no team credentials in 0.7.0 there is nothing team-owned to rotate;
  the person's own credentials stay theirs (call 8, superseded).

## Claude renewal is the riskiest part

Codex renewal is automatic today. Claude still needs an occasional manual sign-in (BLG-574 spike).
Because the first week relies on each person's own Claude seats, prove Claude renewal on the cloud install
early: renewal before expiry with three people's workers running, simultaneous refresh, and a
human re-sign-in from the card. If a provider cannot renew without a human, the "needs sign-in"
path above is the designed behavior, not a failure. If the code-paste sign-in itself can't work,
**the 0.7.0 alpha is blocked** until a working subscription sign-in is found; API keys are not an
acceptable substitute (owner, 2026-11-09).

## Decided by the owner (2026-11-09)

- Team credentials are added by teamspace admins from the browser through the provider's sign-in
  on the card; personal credentials the same way. *(Superseded 2026-11-09: credentials are
  personal only; each person adds their own the same way.)*
- Needs-sign-in alerts: email to those who can fix it, red meter segment and hovercard, and a
  banner linking to the card for those who can fix it; no Needs you item. *(Since 2026-11-09 that
  is the credential's owner only.)*

## Left to the builder

- The per-provider sign-in details within the flows named in "Sign-in per provider".
- Storage layout and the encryption scheme in Core.
- Email templates, banner placement and wording (UX skill rules apply).

## Review round 1 (2026-11-09): contracts added

An adversarial review (Codex, 15 findings, 9 high, including a cross-document check) found the
draft left its boundaries implicit. The owner decided one point; the rest are **Main's calls**
(the owner may overrule).

**Owner decision:**

- **Git access in 0.7.0 is personal.** Each person uses their own GitHub token, so work commits and
  pushes as that person. A team Git credential (a GitHub App or deploy key) comes with team secrets
  in 0.8.0. "Team-only" onboarding means team-only AI credentials. *(Superseded 2026-11-09: AI
  credentials are personal only.)*

**Main's calls:**

1. **Provider flows are proven before the UI.** A tested per-provider contract (registered client
   and redirect rules, browser flow, scopes, refresh behavior, supported account types) is
   recorded first. The Claude cloud renewal spike (BLG-574) is a **blocking milestone** with a set
   duration and renewal and reconnect drills; if it fails, the alpha is blocked until a working
   subscription sign-in exists (owner, 2026-11-09: API keys are not acceptable).
2. **Browser sign-in is a bound transaction.** Starting it requires an authenticated,
   CSRF-protected request from the credential's owner. Core holds short-lived,
   single-use state bound to the browser session, the person, the owner scope, the card, the
   provider and the expected generation, and rechecks authority at completion. PKCE where
   supported, exact allowlisted redirects, fixed issuer and token endpoints.
3. **Signing in again can't silently swap the account.** Core keeps a private binding to the
   provider account. Re-authorizing the same account is routine; signing in as a different account
   is refused unless the person explicitly chooses "replace", is shown the affected work, and
   confirms. The owner may see which provider account they signed in as, through the provider's
   own screens; nobody else does.
4. **Refresh survives crashes honestly.** Every refresh goes through one serialized path with
   durable attempt and generation state and conditional writes; reconnect, delete and revoke are
   fenced against it. Access is handed out only after the new generation is committed. When a
   crash leaves a provider token unrecoverable, the credential goes to "needs sign-in" rather than
   retrying forever.
5. **Background renewal has its own authority.** Keeping a credential alive is a maintenance
   action by Core on the credential owner's behalf, recorded as such. Handing access to a session
   still checks that the session's invoker is the credential's owner and its workspace bounds,
   every time.
6. **Needs-sign-in is a typed incident.** Credential states are distinct: transient error, quota,
   revoked, needs sign-in; personal Git tokens add expiring, expired and lost repository access
   ("Personal Git tokens"). One deduplicated incident per credential, with bounded retries and a
   resolution. The only recipient is the credential's owner, at their verified Blerg
   address, never the provider account's email. Recipients are rechecked before sending. Delivery
   failures are logged and shown to admins. Links go to the install's canonical address.
7. **Resuming after a sign-in is fenced.** "Paused for sign-in" is resumable; "stopped for
   authority" is not. Before anything resumes, including Main, workers and scheduled work, Blerg
   rechecks person, project, claim, policy and runtime authority and takes a fresh runtime
   lease (credentials need no admission lease). The native conversation continues only if the same provider account was verified (call
   3).
8. *(Superseded 2026-11-09: no team credentials in 0.7.0.)* **Offboarding rotation is mandatory.** Removing a person creates required actions, not optional
   review: replace the refresh credential of every team credential they could use, revoke
   upstream where the provider supports it, and replace and confirm any static keys. The docs
   state each provider's maximum access-token lifetime as the residual window.
9. *(0.8.0, with full key rotation.)* **Encryption-key rotation uses versioned envelopes.** Each encrypted record carries a key
   identifier. Re-encryption runs with a durable cursor and read/write cutover. Every consumer holds
   the keyring, and a mixed-generation snapshot can be restored. A key is retired only when no
   retained backup or point-in-time window still needs it.
10. *(Planned procedures 0.8.0; the emergency procedure stays in 0.7.0.)* **Each secret class has
    its own rotation procedure.** That covers database roles (parallel
    identities), signing keys (overlapping verification), the vault key and the live-data issuer.
    Each has an inventory of its consumers, a reload and readiness check, an abort point and a
    retirement rule. Emergency rotation after a compromise is a separate, faster procedure.
11. **Restores start inert.** A restore runs in quarantine before services start: no admission,
    refresh, crons, email, provider calls or daemon reconnections. The recovery runbook reconciles
    current revocations and invalidates old session and daemon authority before reopening. Restore
    drills use test identities, never production provider credentials.
12. **Core owns credential state; Runner reconciles.** Core holds each credential's authoritative,
    versioned lifecycle state and the single email outbox. Runner reconciles workers against it
    after disconnects or restarts, without relying on exactly-once events. Core owns banner state;
    each view filters by permission.
13. *(Superseded 2026-11-09: no team or inherited credentials in 0.7.0.)* **Using an inherited org credential is not administering it.** Use and administer bind to the
    owning teamspace under the team-scope permission matrix (`team-scope.md`). Inheriting use never implies the right
    to repair or reconnect. Repair recipients come from administer rights.
14. *(Superseded 2026-11-09: no team seats in 0.7.0.)* **Upstream seat continuity is documented.** For each team credential, Blerg records who
    controls the upstream seat and who can reconnect it. Offboarding the person who connected a
    seat requires either a remaining recovery operator or a documented replacement-seat procedure.

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

- New "Sign-in per provider": Codex device code (shipped), Claude probably code paste (fixed
  redirects; spike confirms); API keys are not a fallback for seats (owner).
- Goal 1 no longer says the install's public address completes the sign-in.
- New "Personal Git tokens": reachability check against the project's repositories, expiry as a
  typed incident with an owner-only needs-attention path, and a three-person acceptance test
  (Main's calls).
- Revocation stops issuance for the removed person; renewal continues for the rest; unreachable
  executors stop themselves within 30 seconds.
- Rotation body and calls 9 and 10 are marked 0.8.0 where moved; emergency rotation stays.
- Call 13 points at the permission matrix in `team-scope.md`.
- Revocation and offboarding say "at once when reachable, within 30 seconds when not" (owner,
  2026-11-09), and a disabled account's personal work stops at the daemon's reconnect.
- The provider-proof gate is unchanged: a failed BLG-574 spike blocks the alpha; API keys are not
  a fallback (owner, 2026-11-09).

## Spike result and provider terms (2026-11-09)

The BLG-574 spike (report `spike-blg574.md`) found:

- Claude's sign-in works as the design expected: the person opens Anthropic's page and pastes a
  code back. The alpha is not blocked on sign-in.
- **Recommended mechanism (Main's call):** a "seat host", meaning one Blerg-managed, long-running,
  signed-in `claude` process per connected seat. It keeps itself signed in through the official
  CLI. Core and Runner hand short-lived access copies to sessions, generalizing today's sandbox
  credential sync from one desktop to cloud seats. This avoids Anthropic's undocumented token
  endpoint. `claude setup-token` (a static one-year bearer token) is not used for session
  distribution.
- Deferred drills, which need a dedicated test seat and an interactive terminal: two separate
  sign-ins (two seat hosts) to the same Claude account renewing without breaking each other; the real
  access-token lifetime, refresh rotation, forced expiry, concurrent refresh, revocation
  visibility, and the full reconnect.
- **Provider terms (owner, 2026-11-09):** "We're just a tool; it's on the startup to comply with
  the terms of service of individual providers." *(Revised 2026-11-09: Blerg no longer shares
  credentials. Whether several people sign in to the same provider account is their choice and
  their responsibility; the [+] connect screen notes that the provider's terms apply, without
  blocking.)*

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- **AI credentials are personal only.** Each person adds any number, in any mix (for example 3
  Claude and 3 Codex), from [+] in personal settings. No teamspace credentials, no sharing.
- **Needs-sign-in goes to the owner only:** email, banner and red meter segment. Seat host,
  renewal and segmented meters are unchanged.
- **Removed:** teamspace admins adding or repairing seats; inherited org credentials (call 13);
  upstream seat continuity (call 14); mandatory offboarding rotation of team credentials (call 8);
  duplicate-login refusal; renewal continuity for remaining members.
- **Org policy:** the switch that disabled personal AI accounts is replaced by an org choosing
  which providers its workspaces may use.
- **No throttling:** credentials need no admission lease; runtime leases and the 30-second
  self-stop stay.
- **Same login, several people** (household or startup): see `accounts-and-placement.md`,
  "Examples". Blerg is a tool; following the provider's terms is the people's responsibility.
- Team-owned credentials as service identities are 0.8.0, with team secrets.
