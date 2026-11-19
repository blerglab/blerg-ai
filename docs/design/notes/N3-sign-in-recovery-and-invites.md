# N3: sign-in, recovery and invites

Covers phase 1 slices 1.10–1.17 (BLG-649 to BLG-656) and the invite part of 2.4. Source:
`docs/design/sign-in.md` (full), `docs/design/team-scope.md` ("Org admins administer, they
don't use", line 115, and the permission matrix). Where this note and the designs differ, this
note wins; where the designs' own "Simplification"/"Scope moved to 0.8.0" sections differ from
earlier text in them, those sections already win and are folded in below. Revised after three
adversarial attack rounds (Codex, 2026-11-09: round 1, 6 high/2 medium; round 2, 4 high/2 medium;
round 3 (narrow), 1 high/2 medium); every finding from all three rounds is folded in below, with
a pointer from each changed/added rule back to its round and finding number.

Today's code (read before writing tests):

- `accounts` has only `role` (`'member'|'admin'`, install-wide), unverified `email`,
  `must_change_password`, `password_hash`; no TOTP, recovery-code, verified-email, or org
  columns yet (`core/internal/db/migrations/005_identity_accounts.sql:1-11`).
- `InvitePerson` is install-admin only (`core/internal/identity/people.go:49-62`), creates the
  account and a 7-day single-use hashed code atomically (`people.go:65-101`), shows the code
  once, sends no email. `AcceptPersonInvite` unconditionally overwrites `provider_subject` and
  `password_hash` and starts a session (`people.go:106-141`, `core/internal/api/people.go:140-142`)
  — fine for today's always-new-account invites, but a gap once existing accounts get invited too.
- `DisablePerson` is the existing precedent for "mutate and revoke atomically": it calls
  `RevokeAccountEverywhereTx` inside the same transaction as the disable, under a stable lock
  order (`core/internal/identity/people.go:150-188`).
- `RevokeAccountEverywhereTx` is the one cross-service revocation primitive: revokes
  `human_sessions`, inserts `revocations` rows, revokes `agent_tokens`, bumps `live_data_epoch`
  (`core/internal/identity/human_session.go:793-819`, lock order at `:787-815`). Runner caches
  these: it refuses anything on the list (`runner/internal/coreauth/coreauth.go:158`), but on a
  fetch failure it **keeps the old list** and only starts fail-closing sensitive checks after a
  5-minute stale ceiling (`coreauth.go:23-26`), not at the 60-second poll interval (`:23`).
- `blerg-core users set-password`/`users create` is today's broad CLI
  (`core/cmd/blerg-core/users.go:16-87`) — the tool sign-in.md narrows to install admins, a link,
  and an audit/outbox trail (rules 25–26).
- The refresh handler already mints a scope-limited "password.change-only" token for a
  must-change-password account (`core/internal/api/auth_handlers.go:355-368`) — the precedent
  for "setup-only" scoping used below.
- No org/team model exists yet (phase 2, N4). Through phase 1, "admin" means today's
  install-wide `role='admin'`. Where a rule says "org admin", it applies once 2.4 adds org-scoped
  invites and factor resets; until then, substitute "install admin".

## Failure rule

Any failure or unknown state in sign-in, recovery, or invites fails closed: the person sees a
generic "something went wrong, try again" (sign-in attempts) or "that link no longer works"
(any redemption failure — expired, already used, wrong account, or invalid, all identical),
never a stack trace, a hint about which part failed, or whether an account exists. Every failure
writes one log line naming the cause (expired token, DB error, SMTP rejection, rate limit,
revoked session) and the account/invite ID, never a secret, password, TOTP seed, QR payload,
code, or raw token. A send failure, a DB error mid-redemption, and an expired-but-present row all
produce the same user-visible refusal. **Redemption messages never differentiate by cause**
(finding 7's matrix note): rule 24's "expired or used" wording is replaced by this same generic
message, not a more specific one.

## Disagreements

None, in any round. All findings are folded in as proposed; round-1 finding 6, round-2 findings
1 and 6, and round-3 finding 1 follow Main's explicit calls (rules 25–26, rule 12, rule 1, rule
29), each quoted where it lands.

## Rules

1. A TOTP factor is active only after the person enters one valid code from the app they just
   enrolled, checked against that specific pending enrollment's generation (a stale or
   previously-abandoned enrollment can't be completed late). The seed is stored encrypted at
   rest. **It appears in exactly one place: the enrollment QR shown to the authenticated,
   setup-scoped session enrolling it, over that protected channel, and nowhere else** — never in
   logs, error messages, analytics, or any API response after that enrollment completes, and
   never re-shown by regenerating or re-requesting the same QR later (Main's call on round-2
   finding 6, replacing the prior draft's blanket "never in QR payloads," which made enrollment
   impossible).
2. Accepted TOTP codes use a standard window (current step ± 1 for clock skew) and a bounded
   count of consecutive wrong attempts before the rate limit (rule 27) trips. An accepted code is
   rejected if presented again within its window — no replay (finding 2).
3. Recovery codes are a fixed-size one-time set, stored hashed (never plaintext after the single
   display), each consumed exactly once: two concurrent redemption attempts on one code succeed
   exactly once, the other refused (finding 2).
4. Regenerating recovery codes atomically invalidates every previous code in the same operation
   that issues the new set, and is its own action — distinct from changing email (fixes the
   wrong cross-reference in the prior draft's rule 2, finding 2).
5. A password reset changes only the password hash; it never clears or bypasses an existing TOTP
   factor. Sign-in afterward still requires that factor or a recovery code.
6. **Every** account mutation that is supposed to end existing access — password reset, factor
   reset, account disable, sign-out-everywhere — calls `RevokeAccountEverywhereTx` (or its
   equivalent) in the *same transaction* as the mutation, following the `DisablePerson` precedent
   and its lock order. None of these commits without the revoke committing with it; a session
   refresh or token mint racing the mutation observes either the fully-old or fully-new state,
   never a window where the factor/password changed but the old session is still live (finding 1,
   rewrites the prior draft's rules 5 and 8's "every reset" gap).
7. A password reset link is single-use, hashed at rest (same hashing precedent as invite codes),
   and expires at 1 hour. Using it invalidates every other outstanding reset link for that
   account atomically with the password change (finding 7).
8. Resets, factor resets, and disables revoke across Core, Runner, and Board through the single
   `revocations` table and poll; no service keeps a separate list that could drift.
9. Step-up (fresh password, plus the factor if present, within the last 10 minutes) is required
   before: enrolling/replacing/removing your own factor, regenerating recovery codes, resetting
   someone else's factor, changing your own email, and changing admin security policies. A valid
   access token alone, however recent, never satisfies step-up.
10. Removing your own factor is refused outright while the install requires two-factor sign-in,
    separately from the step-up check in rule 9.
11. In phase 1, only install admin resets a factor; there is no stub org check. The org-scoped
    reach rule (an org admin resets a member's factor only when every org that member is in is
    one that admin administers) lands and is tested with slice 2.4, not before (Main's call).
    Every reset is logged, revokes per rule 6, and emails the affected person only.
12. Turning on the 2FA requirement, in one transaction: for every unenrolled account it inserts a
    per-account `revocations` entry (not a single global flag), so Core refuses their tokens at
    the next verification and `/revocations` actually carries an entry for Runner/Board to find —
    a policy-row change alone writes nothing they can see (round-1 finding 4). **Single
    freshness rule (Main's call on round-2 finding 1, superseding today's read-only exemption at
    `contracts/identity/verify.go`'s `sensitive()` classifier and `board/internal/coreauth`'s
    equivalent): once a service's revocation view is older than the stale ceiling, it refuses
    every token outright — reads included, no capability is exempt.** The ceiling is phase 1's
    existing bound (today's 5 minutes, `coreauth.go:23-26`) until slice 2.2 tightens it to 30 s;
    this note does not touch 2.9's separate running-work lease guarantee. When Core is reachable,
    Runner/Board catch up within one ordinary poll interval (60 s today, 30 s after 2.2) well
    before that ceiling; the ceiling itself is what governs an actual Core outage. State the
    ceiling value in the settings copy. Sessions without a factor are limited to the enrollment
    screen meanwhile (same scoping precedent as the password-change-only token).
13. Completing enrollment under rule 12 upgrades that account's own new sessions; it never
    resurrects tokens or sessions already revoked by rule 12 — the person must sign in or mint
    fresh credentials after enrolling (finding 4).
14. The settings screen that turns the requirement on lists every affected person and token
    before the admin confirms.
15. An email change keeps **three distinct, action-bound, single-use tokens** under one pending
    row: a confirm token (new address, confirm-only endpoint), an approve token (old address,
    approve-sooner-only endpoint), and a cancel token (old address, cancel-only endpoint). No
    token's action is chosen by a parameter; which endpoint/token pair is used is what decides
    the action (round-1 finding 5; round-2 finding 2 adds the missing approve token — "cancel"
    alone can't also mean "approve sooner").
16. The pending-email-change row has one explicit status per account at a time:
    `pending → waiting → applied`, or `pending|waiting → cancelled`, or `pending → expired`, or
    `pending|waiting → superseded`, plus a generation. `applied`, `cancelled`, and `expired` are
    terminal and atomic with clearing the row's tokens; `pending` and `waiting` are not terminal
    and keep the row's old-address tokens live (round-2 finding 2 — the prior draft wrongly made
    `confirmed` both terminal and still-waiting). Starting a new email change while one is
    `pending` or `waiting` atomically marks it `superseded` and invalidates its tokens.
17. With a factor on the account, confirming the new address moves `pending → applied` at once.
    The old address's notice, sent after the fact, states plainly that the change already took
    effect and gives the real recovery route (step-up sign-in, then rule 9's security-policy or
    factor-reset paths) — it carries no cancel link, because there is nothing left to cancel
    (round-3 finding 2; a dead cancel button would be worse than none). Without a factor,
    confirming moves `pending → waiting` (not terminal): the change is effective only once
    `waiting → applied`, reached either by the old address's approve token or by the 72-hour
    sweep. The old address's cancel token moves `pending` or `waiting → cancelled` at any point
    before `applied`. **Linearization, not priority:** every one of confirm/approve/cancel/the
    sweep is a single conditional update (`UPDATE ... WHERE status IN ('pending','waiting')`);
    whichever commits first wins the transition, and every other concurrent attempt matches zero
    rows and gets the generic failure-rule message — cancel has no inherent precedence over a
    simultaneous approve or sweep, only over whichever hasn't yet committed (round-3 finding 2,
    replacing the prior draft's unenforceable "cancellation always wins at the same moment").
18. A password reset or an account disable that lands while an email change is `pending` or
    `waiting` atomically moves it to `cancelled`, so a security event never leaves a stale
    pending email change to resolve later.
19. **Existing-account invites never take the account over.** When the invited address already
    has an account, redemption requires authenticating as that account — its current password and
    factor, or an already-live session for it — before adding the stated membership. It never
    resets that account's password or factor and never signs a different, already-authenticated
    browser into it (finding 3; today's `AcceptPersonInvite` at `people.go:106-141` only covers
    the new-account case and must not be reused unconditionally for this one).
20. **New-account invites grant setup-only authority first.** Until a new invitee has set a
    password, confirmed the invited address, and (when the install requires it) completed TOTP
    enrollment, their session can't read data, start work, or mint ordinary API/daemon tokens —
    the same setup-only scoping as rule 12's unenrolled sessions (finding 3).
21. An invite names one email address. Redemption must prove that address and grants only the
    stated roles, revalidated against the inviter's current authority at redemption time, not the
    authority they had when they sent it.
22. Who may invite: install admins create install accounts, and invite into an org only where
    they're also its admin; org admins invite into their own org; team admins only add existing
    org members, never new accounts. (Org/team half activates with 2.4.)
23. Every invite expires at 7 days, single-use. An expired, used, or otherwise invalid code
    produces the failure rule's single generic redemption message — no case-specific wording
    (finding 7's matrix note; supersedes the prior draft's differentiated message).
24. Once email is configured, the inviter can't see or copy the code; before that, an install
    admin may copy a code once, as today.
25. **Break-glass issuance proceeds when email is down** (Main's call on round-1 finding 6). It is
    cluster-access only. **Issuing the link changes nothing about the account yet** — no password
    reset, no factor clear — it only mints the link (rule 26) and, in the same transaction,
    writes the audit record and queues the all-admins notification in the durable outbox; none of
    that waits on SMTP (round-3 finding 3 — issuance and redemption are deliberately separate
    steps, not two descriptions of one reset). Email delivery failure never blocks or rolls back
    issuance. Every install admin sees the still-pending notification in Settings on their next
    sign-in until they acknowledge it.
26. Break-glass **redemption** is the one exception to rule 5: only this path, only for install
    admins, only via the cluster-access CLI itself — never an ordinary admin API route or an
    agent/API token — may clear a factor as part of recovery. **The link** (minted at issuance,
    rule 25): a cryptographically random single-use token, hashed at rest (same precedent as
    invite/reset tokens), bound to one target account id and a recovery generation, expiring at
    15 minutes; redeeming it twice, redeeming it against a different account than its target, or
    redeeming either of two outstanding links for two different recovery generations each
    resolves to exactly one success, decided by whichever redemption transaction commits first
    (same linearization as rule 17) — the rest fail with the generic message. Redemption, in one
    transaction: resets the password, clears the TOTP factor, and atomically invalidates every
    recovery code, every other outstanding reset/invite/email-change token, and any live step-up
    proof for that account (a proof survives only its own session/token, which this revokes) —
    clearing only the factor row while old recovery codes stayed valid would defeat the reset
    (round-2 finding 3). If the process crashes after this transaction commits but before the link
    is printed, the reset already happened and the link is unrecoverable; break-glass must be run
    again (a fresh issuance, rule 25) rather than treated as failed. Redemption grants setup-only
    authority (rule 32) until the admin re-enrolls, if the install still requires 2FA. It can't
    read data or any stored credential. **Audit identity**: the record names the actual
    cluster-execution identity that ran the CLI (the operator's own authenticated access to the
    cluster — e.g. the `kubectl`/service-account identity the exec required), never a
    caller-supplied `--subject` value or an assumed human name; when that execution context can't
    be attributed to one individual, the audit record says so explicitly rather than implying it
    can.
27. Rate limits (sign-in, every email-sending action) apply per address/account/origin with
    exponential backoff, never permanently lock out the real owner, and recovery codes always
    keep working. Responses are uniform and constant-time regardless of whether the account or
    invite exists.
28. Emails carry exactly one clickable link (the install's own address), say what produced them
    and how to report an unwanted one, and never log a token. A link is redeemed only by POST
    after a landing page, never by the GET that loads it.
29. On `github`/`oidc` installs, the exemption is a fixed, by-number list of **local password and
    TOTP rules only** — rules 1, 2, 3, 4, 5, 7, 10, 11, and 26 — because the provider owns the
    factor and password equivalents those rules govern, and nothing else (Main's call on round-3
    finding 1, replacing the prior draft's "rules 1–11" range, which wrongly swept in rules 6, 8
    and 9). **Every other rule applies to provider accounts exactly as to local ones**, in
    particular:
    - Rule 6's atomic revocation and rule 8's cross-service revocation: disabling a provider
      account or signing it out everywhere still revokes its Blerg sessions, agent tokens, and
      `revocations` entries via `RevokeAccountEverywhereTx` — none of that is the provider's job
      (`core/internal/identity/human_session.go:793-815`).
    - Rule 9's step-up: for a provider account, **a fresh provider re-authentication (sign in
      through `github`/`oidc` again) is the step-up check**, in place of password+factor, before
      any sensitive Blerg change (own email, security policy, someone else's factor reset). A
      step-up proof is tied to the session/token it was performed on; revoking that session or
      token invalidates the proof — it is never carried over to, or replayed against, a new
      session.
    - Rule 19's existing-account invite binding still requires proving the account with a fresh
      provider-authenticated session before adding membership.
    Their verified email is treated as provider-sourced and immutable through Blerg (no local
    email change, so rules 15–18 don't operationally apply); open question 1 below asks Main to
    confirm this default. Invites to these installs go to existing verified-email accounts only.
30. **Security policy values (the 2FA requirement, rate limits, invite rules) are rows in Core's
    database, not chart/Helm values.** A redeploy or an upgrade with different chart values never
    changes a persisted policy row; only an explicit step-up-gated admin action (rule 9/14) does.
    A fresh managed (chart) install defaults the requirement on; desktop and upgraded installs
    default it off (owner's call). First-run setup gives the first admin setup-only authority
    (rule 20's mechanism) until they've set a password, confirmed their email, and — when the
    chart default is on — completed TOTP enrollment (finding 8).
31. An existing `local` account with no verified email is asked to add and confirm one at next
    sign-in; email-based recovery stays off for it until then.
32. **Setup-only scope (rules 12, 20, 30) is an explicit endpoint allowlist, not a vague
    "can't read/work/mint."** It permits only: setup status, email-proof confirmation, TOTP
    enrollment, the one-time recovery-code display, and sign-out. It refuses every other Core,
    Runner, and Board data/work/token endpoint, and refuses any refresh or audience exchange that
    would mint a broader-scoped token — a setup-only session cannot escalate itself by asking for
    a different audience. A newly re-scoped setup session issued after a rule-12 policy-on
    revocation gets a fresh setup-only token; it is never derived from, or able to resurrect, the
    session it replaced (round-2 finding 5 — this rule applies to both the first-admin and
    ordinary-invitee cases, across all three services, not just Core's password-change screen
    that today's precedent, `middleware.go`'s `requireHumanPrincipal`, was built for).

## Test matrix

| Situation | Expected result | Layer | Slice |
|---|---|---|---|
| Valid TOTP code on first enrollment | Factor activates, codes shown once | unit | 1.11 |
| Enrollment abandoned, no code submitted | No active factor; retry starts clean | unit | 1.11 |
| Stale enrollment generation completed late (old QR, new attempt) | Refused | unit | 1.11 |
| Wrong code repeated | Stays inactive; rate limit trips | unit/contract | 1.11 |
| Same accepted TOTP code presented again within its window | Refused (no replay) | unit | 1.11 |
| TOTP seed/QR/error paths inspected for leakage | Appears only in the one enrollment QR response; never in logs, errors, or later API reads | unit | 1.11 |
| DB contents inspected after enrollment | Seed stored encrypted, not plaintext; recovery codes stored hashed | unit | 1.11 |
| Unauthorized or unscoped request for the same account's QR | Refused | unit | 1.11 |
| QR re-requested after enrollment completes | Refused; seed not re-shown | unit | 1.11 |
| Recovery codes re-fetched after activation | Refused, shown once only | unit | 1.11 |
| Two concurrent redemptions of one recovery code | Exactly one succeeds | contract | 1.11 |
| Recovery-code regeneration races a use of an old code | Old code loses atomically; new set is the only valid set | integration | 1.12 |
| Password reset, factor present | Password changes; factor still required | integration | 1.14 |
| Password reset races a session refresh/token mint | Old-or-new state only, never a mixed window | integration | 1.14 |
| Reset: DB error after hash write, before revoke | Rolls back; no partial state | integration | 1.14 |
| Factor reset races session refresh/token issuance | Old-or-new state only, same as password reset | integration | 1.15 |
| Reset link reused | Refused, generic message, logged | unit | 1.14 |
| Reset link used at 59 min vs. 61 min | Allowed vs. refused | unit | 1.14 |
| Two outstanding reset links for one account, first used | Second invalidated atomically | unit | 1.14 |
| Reset racing a pending email change | Pending email change cancelled atomically | integration | 1.14/1.17 |
| Reset racing an account disable | Deterministic single outcome, no partial state | integration | 1.14 |
| Reset completes, Core reachable | Core refuses old tokens at once; Runner/Board catch up within one poll interval (60s/30s) | cross-service | 1.14 |
| Reset completes, Core unreachable from Runner/Board | Old tokens still honored only until the stale ceiling (5min today/30s after 2.2), then every token refused | cross-service | 1.14 |
| Step-up check 9 min old | Allowed | unit | 1.12 |
| Step-up check 11 min old | Refused, re-authenticate | unit | 1.12 |
| Step-up via access token only, no fresh check | Refused | unit | 1.12 |
| Remove own factor while 2FA required | Refused outright | unit | 1.12 |
| Org admin resets factor for member in an org that admin doesn't administer | Refused (tested at 2.4, not 1.15) | integration | 2.4 |
| Install admin resets any factor | Allowed, logged, revoked per rule 6, member emailed | unit | 1.15 |
| Admin turns on 2FA requirement, Core and Runner/Board reachable | Per-account revocation rows written; both bounds observed | integration | 1.15 |
| Admin turns on 2FA requirement, Runner's revocation fetch is failing | Old list kept until the stale ceiling; past it, every token refused (reads included), not just sensitive-capability ones | integration | 1.15 |
| Read-only-capability token used at 61s / 301s / final-contract 31s of an outage | Honored at 61s (under ceiling); refused at 301s today and at 31s after 2.2 (past ceiling), same as any other token | integration | 1.15/2.2 |
| Write-capability token at the same three marks | Same refuse-past-ceiling outcome as the read-only token — no distinction | integration | 1.15/2.2 |
| Old API/daemon token reused right after its owner enrolls | Stays revoked; not resurrected by enrollment | unit | 1.15 |
| Requirement-on settings screen, before save | Lists every affected person and token | unit | 1.15 |
| Email change, factor present | Step-up required; `pending → applied` on new-address confirmation | integration | 1.17 |
| Email change, no factor | `pending → waiting` on confirmation; stays `waiting` until approve, cancel, or 72h sweep | integration | 1.17 |
| Old address uses approve token during `waiting` | `waiting → applied` immediately | integration | 1.17 |
| Old address uses cancel token during `pending` or `waiting` | `→ cancelled`, immediately, no further transitions possible | integration | 1.17 |
| Cancel and approve/sweep land at the same moment | Cancel wins | integration | 1.17 |
| A second email change started while the first is `pending`/`waiting` | First row's tokens marked `superseded` | integration | 1.17 |
| Factor-protected change applies before the old-address notice is delivered | Notice states the change already took effect, with the real recovery route; no cancel link | integration | 1.17 |
| Confirm/approve/cancel/sweep attempted truly simultaneously | Exactly one conditional update commits first and wins; the rest match zero rows, generic message | contract | 1.17 |
| New address already verified on another account | Refused | unit | 1.17 |
| Core restarts mid-`waiting` on a pending email change | Row survives; state resumes correctly on next read/sweep | integration | 1.17 |
| Invite redeemed by a different address | Refused | unit | 1.13 |
| Existing-account invite redeemed without that account's password/factor | Refused | integration | 1.13 |
| Invite redeemed while invitee's browser is signed in as a different account | Refused; no account merge | integration | 1.13 |
| Invite targets a disabled account | Refused | unit | 1.13 |
| New invitee reads data/mints an ordinary token before setup completes | Refused; setup-only scope | unit | 1.13 |
| Invite redeemed after inviter's authority was revoked | Refused on stale authority | integration | 1.13 (full effect 2.4) |
| Invite redeemed twice at once (race) | Exactly one succeeds | contract | 1.13 |
| Invite code copied, then email gets configured | Copy path stops; email-only from then on | unit | 1.13 |
| Invite expired vs. already used | Identical generic message, no differentiation | unit | 1.13 |
| Team admin invites a brand-new address | Refused; adds existing members only | unit | 2.4 |
| `users set-password` on a non-admin account | Refused | unit | 1.16 |
| Break-glass run with SMTP reachable | Link printed, not a password; factor and old recovery codes cleared; audited; outbox entry sent; signed out everywhere | integration | 1.16 |
| Break-glass run with SMTP down | Proceeds and is audited anyway; outbox entry queued, not sent; recovery not blocked | integration | 1.16 |
| Break-glass link used at 14 min vs. 16 min | Allowed vs. refused | unit | 1.16 |
| Break-glass link used against a different account than its target | Refused | unit | 1.16 |
| A pre-break-glass recovery code used after redemption | Refused; old codes invalidated with the factor | unit | 1.16 |
| Break-glass redemption while install still requires 2FA | Grants setup-only scope (rule 32) until re-enrollment | integration | 1.16 |
| Attempt to invoke break-glass via an ordinary admin API route or an agent/API token | Refused; CLI-only | unit | 1.16 |
| Audit record after a break-glass run | Names the actual cluster-execution identity, not a caller-supplied `--subject`; states explicitly if unattributable | unit | 1.16 |
| Admin signs in after a break-glass run while the notice is still queued | Sees the pending notice in Settings, unacknowledged | integration | 1.16 |
| Admin acknowledges the pending break-glass notice | Notice clears for that admin only | unit | 1.16 |
| Two concurrent redemptions of one break-glass link | Exactly one succeeds | contract | 1.16 |
| Two outstanding break-glass links from two different recovery generations | Exactly one resolves per the commit-order rule; the stale generation's link is refused | integration | 1.16 |
| Process crashes after the redemption transaction commits, before the link is printed | Reset already applied; link unrecoverable; a fresh break-glass run is required, not a retry | integration | 1.16 |
| Setup-only token granted by break-glass redemption attempts a broader audience exchange | Refused, same as rule 32 | unit | 1.16 |
| Pending vs. active TOTP seed storage inspected | Both encrypted at rest | unit | 1.11 |
| Restart before either the first admin's or an invitee's setup proofs complete | Setup-only state persists for both; neither gets escalated early | integration | 1.10/1.13 |
| Sign-in: nonexistent vs. existing username, same bad password | Identical response and timing | contract | 1.17 |
| Burst of sign-in attempts, one account/IP | Backoff; owner can still use a recovery code | integration | 1.17 |
| Reset/invite send fails (SMTP rejects), not break-glass | Admin sees cause; person sees generic refusal | integration | N2/1.9 |
| Landing page GET without follow-up POST | Token not consumed, not logged | unit | 1.13/1.14/1.17 |
| `local`-only reset attempted on a `github`-provider account | Refused; factors belong to the provider | unit | 1.10 |
| Existing GitHub/OIDC-account invite redeemed with no fresh authenticated provider session | Refused | integration | 1.13 |
| Existing GitHub/OIDC-account invite redeemed while signed in as the wrong provider identity | Refused | integration | 1.13 |
| Provider (`github`/`oidc`) account disabled or signed out everywhere in Blerg | Revoked per rules 6/8, identically to a local account | integration | 1.15 |
| Provider-account disable races a session refresh/token mint | Old-or-new state only, same as a local account | integration | 1.15 |
| Security-policy change attempted from a provider account's stale (not-freshly-reauthenticated) session | Refused; fresh provider re-authentication required as step-up | integration | 1.15 |
| Old token for a disabled provider account read cross-service after the fact | Refused, same propagation bound as a local account | cross-service | 1.15 |
| Setup-only session calls an allowlisted endpoint (setup status, email proof, enrollment, recovery-code display, logout) | Allowed | unit | 1.13/1.15 |
| Setup-only session calls any ordinary Core/Runner/Board data, work, or token endpoint | Refused | integration | 1.13/1.15 |
| Setup-only session attempts refresh/audience exchange for a broader-scoped token | Refused | unit | 1.13/1.15 |
| Policy-on revocation reissues a setup-only session for an already-unenrolled account | Fresh token issued; old session not resurrected or reused | integration | 1.15 |
| Upgraded account, no verified email, signs in | Prompted to confirm one; recovery stays off until then | integration | 1.10 |
| Fresh managed install, chart default 2FA-on, first admin before enrolling | Setup-only authority; can't read/work/mint until proofs complete | integration | 1.10 |
| Desktop/upgraded install with existing accounts | Persisted requirement defaults off; unaffected by later chart value edits | integration | 1.10 |
| Helm values re-applied (redeploy) after install with a different 2FA default | Persisted policy row unchanged | integration | 1.10 |
| Restart mid first-run setup, before first admin finishes proofs | Setup-only state persists; no escalated session created early | integration | 1.10 |
| Hostile: forged header claiming fresh step-up | Refused; step-up is server-verified | unit | 1.12 |
| Hostile: invite code brute-forced | Infeasible (32 random bytes, `people.go:71-75`); limiter trips regardless | contract | 1.13 |

## Open questions for Main

1. Rule 29: is a `github`/`oidc` account's verified email always provider-sourced and immutable
   through Blerg (no local email change, so rules 15–18 never operationally trigger for these
   accounts), or should an install be able to let such a person add/change a separate
   Blerg-held contact address under the same state machine? Assumed immutable for now; this
   changes slice 1.17/2.4's scope if not.

Previously resolved: install-admin-only factor resets until 2.4 (rule 11); the email-change wait
as a durable Core row (rules 15–18); the 2FA-requirement freshness bound, now a single
no-exemption rule tied to the stale ceiling (rule 12, Main's call on round-2 finding 1);
break-glass proceeding under an email outage (rules 25–26, Main's call on round-1 finding 6); the
TOTP seed appearing only in the enrollment QR (rule 1, Main's call on round-2 finding 6); and the
`github`/`oidc` exemption narrowed to a fixed by-number list of local password/TOTP rules only,
with fresh provider re-authentication as the step-up for sensitive Blerg changes on those
accounts (rule 29, Main's call on round-3 finding 1).
