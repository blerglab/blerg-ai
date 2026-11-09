# Sign-in and email (0.7.0)

Status: draft for owner review, 2026-11-09, revised after adversarial review round 1 (see the end). Cards: BLG-563 (email provider), BLG-564 (user
management). BLG-610 (OIDC) moved to 0.8.0 (owner, 2026-11-09). Builds on `team-scope.md`,
`managed-install.md` and `credential-handling.md`.

## Why

A cloud install is on the public internet and holds each person's Claude and Codex credentials. The
startup's three people need to be invited, sign in safely, recover a forgotten password, and be
told when a credential needs them, without depending on an identity provider they may not have.

## Goals

1. **Keep it simple** (owner, 2026-11-09): people sign in with **username + password + an
   authenticator-app code**. The username is today's; email is not a sign-in name. Accounts gain
   a verified email address for invites, resets and alerts. Magic links and OIDC wait for 0.8.0;
   the design leaves room for both.
2. *(Moved to 0.8.0: an admin turning magic links on or off.)*
3. **Two-factor sign-in** with authenticator-app codes (TOTP). **An admin can require it** for
   everyone (owner). *Main's call:* the requirement starts **on** for a new managed (chart)
   install, and **off** on desktop installs and upgraded installs (upgrade activates nothing).
4. **One email sender** (BLG-563) serves invites, password resets, email changes and the
   credential needs-sign-in alerts (`credential-handling.md`).

## Non-goals for 0.7.0

- OIDC and single sign-on (BLG-610, 0.8.0), and identity-provider group sync.
- Magic sign-in links (0.8.0).
- General notification email (BLG-566, 0.8.0).
- Passkeys and hardware keys (later; the second-factor model should allow adding them).

## Email

- Sent over standard **SMTP** or a provider's **HTTP API**, so any provider works; settings come
  from chart values and a Secret reference (`managed-install.md`, call 4). Cloud hosts may block
  the usual SMTP ports, so the runbook recommends a transactional service on an allowed port or
  its HTTP API, and preflight sends a real test email (`managed-install.md`).
- Emails never contain secrets, tokens in clear text beyond the single-use link itself, or
  provider account details. Links point only at the install's own configured address.
- Delivery failures are logged with their cause and shown to admins in settings.

## Accounts and links

- **Invites** (extending 0.6.0 invites, BLG-208, to orgs and teams per `team-scope.md`): single
  use; redeeming one confirms the email address. Expired or used invites say so plainly.
- **Who may invite** (Main's call). Today `InvitePerson` is install-admin only, shows the code
  once and sends no email. In 0.7.0:
  - **Org admins invite people into their org**, with the roles the invite states. If the
    address has no account, redeeming the invite creates one.
  - **Team admins add existing org members to their team**; they don't invite new accounts.
  - **Install admins create install accounts** (an invite with no org), and invite into an org
    only if they are also its admin.
- **Invite lifetime:** **7 days** (today's), single use, sent by email to the named address. The
  inviter or any admin of that org can resend or cancel it. The code is not shown to the inviter
  once email is configured, so the inviter can't redeem it as the invitee. Before email is
  configured (a fresh install), the install admin may copy a code once, as today.
- **Password reset:** single-use link, valid **1 hour**; using it signs out all sessions
  everywhere and stops the person's running work (calls 3 and 7).
- **Rate limits** on every email-sending action and on sign-in attempts, per address and per
  network origin; responses don't reveal whether an address has an account.

## Two-factor sign-in

- Authenticator-app codes (TOTP), set up from the account page with a QR code.
- **Enrollment completes only on proof** (Main's call): the factor becomes active only after the
  person types a valid code from the new app. Then Blerg shows **one-time recovery codes** once.
  An abandoned setup leaves nothing active.
- **An admin can reset a member's second factor** within their authority (call 1); the reset is
  logged (BLG-614) and emailed to the member.
- When an admin requires two-factor sign-in, it applies at once (call 6). Unenrolled people's
  sessions are limited to the setup screen, which can't start work, read data or mint tokens.
- **Warning before turning the requirement on:** unenrolled people's **API tokens and daemon
  tokens stop working** until they enroll, so their desktop daemons disconnect and work on them
  stops. The settings screen lists those people and tokens before the admin saves.

## Step-up: a fresh sign-in for sensitive changes

*Main's call.* These actions need a **fresh sign-in**: the password, plus the second factor when
the account has one, within the last 10 minutes:

- Enrolling, replacing or removing your own second factor, and regenerating recovery codes
  (new codes replace all old ones). Removing your factor is refused while the install requires
  two-factor.
- Resetting someone else's second factor (the admin steps up).
- Changing your email address.
- Changing admin security policies: the two-factor requirement, rate limits, invite rules,
  sign-out everywhere for others, and the org policy switches.

A browser session older than that, or a token, can't do any of these.

**Email change without a second factor** (Main's call). The person signs in fresh with their
password, confirms the new address by link, and the old address gets a notice with a cancel
link. The change then waits **72 hours** unless the old address also approves it, so a stolen
password alone can't take the account quietly. With a second factor, it takes effect on
confirmation of the new address (call 2).

## Installs on Core's `github` or `oidc` providers

*Main's call.* Today Core can sign people in through `github` or `oidc` instead of `local`
(`core/docs/CONFIG.md`, "Auth providers"). In 0.7.0 those installs keep signing in through their
provider, unchanged. Passwords, Blerg's TOTP, password resets and break-glass apply to `local`
accounts only; the identity provider owns factors there, and the two-factor requirement setting
is hidden. Org and team invites go to existing accounts by verified email. People without a
verified email are asked to add one (call 11), because credential alerts need it. Switching an
install's provider waits for 0.8.0 with OIDC.

## The first admin and recovery

- The first admin keeps today's first-run setup: a human-held single-use link, valid 24 hours
  while no account exists, never printed by the installing agent (`managed-install.md`, call 5).
- When the two-factor requirement is on, the first admin enrolls during setup and confirms
  their email there.
- **The install admin always has a documented recovery route** for lost passwords or second
  factors when email is unavailable: a break-glass procedure run by someone with cluster access,
  which is logged.
- **Today's tool is too broad** and must be narrowed (call 8). `blerg-core users set-password`
  resets any account and prints a password (`install/k8s/README.md`, "First login"). In 0.7.0 it
  works only on install-admin accounts, can also clear their second factor, prints a single-use
  reset link instead of a password, signs the account out everywhere, writes an audit record and
  emails all install admins. `users create` likewise prints a single-use setup link, not a
  password, and is for install admins when email is down.

## Decided by the owner (2026-11-09)

- Email and password plus optional magic links; OIDC moves to 0.8.0. *(Superseded the same day:
  username + password + authenticator-app code; magic links moved to 0.8.0.)*
- An admin turns magic links on or off (0.8.0).
- Two-factor sign-in with authenticator codes, which an admin can require.
- The remaining calls (SMTP, link lifetimes, rate limits, recovery codes, admin reset, the
  install-admin recovery route) were accepted as proposed. (SMTP widened to "SMTP or HTTP API" in
  the final round.)

## Left to the builder

- Session and cookie mechanics within Core's existing identity service.
- Email templates and wording (American English; plain and short).
- The break-glass procedure's exact commands, documented in the runbook.

## Review round 1 (2026-11-09): contracts added

An adversarial review (Codex, 16 findings, 9 high) found the account-recovery paths underspecified.
These are **Main's calls** (the owner may overrule):

1. **Factor resets stay inside their authority.** Accounts are install-wide; an org admin may reset
   a member's second factor only when that person belongs only to orgs that admin administers.
   Otherwise only the install admin can. Every reset is logged and emailed.
2. **Changing your email is protected.** It requires a fresh sign-in ("Step-up" above), with the
   second factor when the account has one. It sends a confirmation to the new address and a
   notice with a cancel link to the old one, and takes effect only after confirmation (plus the
   72-hour wait without a second factor). It never removes or bypasses the second factor.
3. **Password reset keeps the second factor.** A reset changes only the password. Signing in
   afterwards still requires the second factor or a recovery code. A reset signs out all sessions
   everywhere.
4. *(0.8.0, with magic links.)* **Magic links finish only where they started.** The link
   completes sign-in only in the browser that requested it. A different device gets a code to type instead, never a session. A magic
   link never satisfies a required second factor.
5. **Invites bind to an address and a person.** An invite names one email address. Redeeming it
   requires proving that address, by the link plus a confirmation, and grants only the roles the
   invite stated, revalidated against the inviter's current authority at redemption.
6. **Requiring two-factor applies at once.** When an admin turns the requirement on, existing
   sessions without a second factor are limited to the setup screen until enrollment, within a
   short grace period the admin sees. API tokens and daemon tokens owned by unenrolled people stop
   working until they enroll.
7. **Signing out is install-wide.** Password reset, factor reset, account disable and "sign out
   everywhere" revoke sessions and tokens across Core, Runner and Board, and stop the person's
   running work (team scope, decision 1).
8. **Break-glass is narrow.** The recovery procedure can only reset the install admin's password
   and second factor. It needs cluster access, writes an audit record and emails all admins, and
   can't read data or credentials.
9. **Rate limits fail safe.** Limits apply per address, per account and per network origin, with
   exponential backoff. They never lock the legitimate owner out permanently, and recovery codes
   keep working. Responses are uniform and constant-time, so they don't reveal whether an account
   exists.
10. **Emails avoid phishing patterns.** They contain no clickable links other than the install's
    canonical address. Each says what action produced it and how to report one you didn't request.
    Tokens never appear in logs or analytics, and link tokens are redeemed only by POST after a
    landing page.
11. **Upgraded accounts verify their email.** Existing accounts without a verified email are asked
    to add and confirm one at next sign-in. Email recovery stays off for them until they do. The first admin confirms their email during first-run setup.
12. **The release gate exercises recovery.** The gate covers a password reset, a factor reset, an
    email change (with and without a second factor), first enrollment, the requirement turning on
    with an unenrolled person's API and daemon tokens, and a break-glass drill, in addition to the
    first-week path.

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

- Goals: sign-in is username + password + authenticator-app code; magic links and their admin
  switch are marked 0.8.0 throughout.
- The two-factor requirement starts on for new managed installs, off for desktop and upgraded
  installs (Main's call).
- Who may invite: org admins into their org, team admins add org members, install admins create
  install accounts; invites last 7 days and are emailed (Main's call).
- Enrollment activates only after a valid code; recovery codes come after (Main's call).
- New step-up rules: a fresh sign-in within 10 minutes for factor changes, recovery codes, email
  changes, factor resets and security policies (Main's call).
- Email change without a second factor waits 72 hours unless the old address approves (Main's
  call).
- Installs on `github` or `oidc` keep their provider; local-only features stay local (Main's call).
- Break-glass: `users set-password` and `users create` are narrowed to install admins and print a
  link, not a password (Main's call).
- Password reset signs out all sessions and stops running work, matching calls 3 and 7.
- Requiring two-factor warns that unenrolled people's API and daemon tokens stop.
- Email may use SMTP on an allowed port or an HTTP API; preflight sends a test email.

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- The install holds each person's own Claude and Codex credentials, not team credentials.
  Needs-sign-in alert email goes only to the credential's owner (`credential-handling.md`).
- The mandatory offboarding rotation has no team credentials to rotate in 0.7.0.
