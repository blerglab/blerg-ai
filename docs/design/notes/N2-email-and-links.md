# N2: email and links

Hard-rules note for slice 1.9 (BLG-563, email over SMTP, durable outbox, admin-visible delivery
failures) and the slices that depend on it: 1.13 (invites), 1.14 (BLG-653, password reset), 3.8
(needs-sign-in email). Pins the "N2 email and links" bullets in
`docs/design/0.7.0-build-plan.md` and the matching rules in `sign-in.md` (Email; Accounts and
links) and `credential-handling.md` (goal 3, call 6). Where Main's calls in the build plan
conflict with the source designs, the build plan wins. Revised after Codex's adversarial review
of commit `4311007e0` (2026-11-09, 4 high, 2 medium), again after round 2 against `4e44fabe1`
(2026-11-09, 4 high, 2 medium), and again after round 3 against `fd6b3cea8` (2026-11-09, 1 high,
2 medium); see "Disagreements" at the end.

## Failure rule

Anything in this area that fails, or whose state Blerg cannot confirm (recipient address not
provably verified, token lookup error, rate limiter unreachable, SMTP send error, `PublicURL`
unset or unvalidated, an ambiguous SMTP acceptance), fails closed: the action is refused or the
incident is left in a retry/terminal state rather than assumed sent, the person sees the same
generic message used for every other refusal in this area, and a log line names the action and
the failed stage (for example `email_send_failed stage=smtp`) without the address, token, or
code.

## Rules

1. Every emailed or landing-page link is built only from Core's configured canonical public
   address (`PublicURL`, `BLERG_CORE_PUBLIC_URL`). `PublicURL` is validated at configuration load,
   not merely trimmed: `TrimRight` in `core/internal/api/mcp_oauth_handlers.go:81-83` is not URL
   validation. Validation requires HTTPS for a managed install (an explicit, narrower loopback
   exception for desktop), a host with no embedded userinfo, and no query string or fragment on
   the configured base. If `PublicURL` fails validation or is unset when a link would be built,
   the action is refused instead of emitting a link on a guessed, unsafe, or loopback host.
   Core also rejects a `Host`/forwarded-header value as a substitute for `PublicURL` anywhere a
   link or redirect target is built.
2. A single-use token (invite code, password-reset link, email-change confirm/cancel link) is
   redeemed only by a same-origin POST, and only after the person has reached a GET landing page
   first. The landing page never redeems by itself. This matches the existing invite shape:
   `GET /invite` serves the app, `POST /auth/invite` redeems
   (`core/internal/api/people.go:17-21, 101-141`).
3. **Reaching the landing page must not itself leak the token**, even though redemption is
   POST-only (rule 2 stops redemption by GET, not exposure by GET). The token travels in a form
   that the browser, any ingress/proxy, and Core's own access logging do not retain in clear: the
   landing page strips the token from the visible address and from history before doing anything
   else (for example reading it once from the path, then replacing the URL client-side), the
   landing response is `Cache-Control: no-store` and `Referrer-Policy: no-referrer`, and it loads
   no third-party script, image, or analytics endpoint that could observe the URL or a `Referer`
   header carrying it. Core's own request logging and any ingress/proxy logging in front of it
   must omit or redact the path/query portion of token-bearing routes; the existing `no-store` on
   the POST response (`people.go:101-104`) does not, by itself, cover the GET landing page or the
   infrastructure in front of it. **The transport is a path token with client-side URL
   replacement**, not a fragment: Core needs the token server-side to show the right landing
   state (expired/used/valid) before any POST, so it cannot live only in a fragment the server
   never sees. Because that replacement depends on script running, the landing page has a defined
   failure policy for when it doesn't: with JavaScript disabled or the page crashing before
   replacement, the token stays in the visible address, and `no-store`/`no-referrer` do not erase
   browser history, sync, or a copied URL — a client-side copy of the link may outlive the visit.
   What stays bounded is the server side: no caching, no outbound `Referer`, no third-party asset
   load, and the token's own expiry still governs whether a later visit to that same address can
   redeem anything. This residual, possibly-longer-than-the-visit exposure is accepted and
   documented, not hidden by a broken promise of guaranteed removal.
4. **The action, the incident identity, and the encrypted delivery payload commit atomically.**
   A token's clear value is retained only long enough to deliver it, and only encrypted at rest. A
   hash alone (`agentTokenHash`, `core/internal/identity/people.go:75-92`) cannot be put in an
   email body, so the durable outbox entry carries an encrypted copy of the token — but that copy
   is written in the *same* database transaction as the action it belongs to (the invite row, the
   reset request, the needs-sign-in incident), or neither commits. Today's `InvitePerson` commits
   the account and the invite-code hash with no outbox row at all
   (`core/internal/identity/people.go:89-101`); 1.9 extends that same transaction to include the
   outbox insert, so a crash between them is impossible by construction, not just unlikely.
   The encrypted copy is decrypted only at send time and erased once the email is sent (a
   definitive, not ambiguous, success — see rule 9) or the token expires or is invalidated,
   whichever comes first. A token is never written to a log, metric, or trace at any point,
   plaintext or ciphertext.
   - **Encryption uses Core's existing key backend** (`core/internal/keybackend`), the same seam
     already used for the credential vault — never a process-local key, and never a key stored
     beside its ciphertext (`core/internal/keybackend/local.go:15-19`: "a key sitting next to its
     ciphertext is obfuscation, not encryption"). The outbox row stores the backend ID and key ID
     alongside the ciphertext (`core/internal/keybackend/backend.go`'s `Backend` contract), so
     decryption always routes back to the right key. A missing or wrong key fails the decrypt
     closed — the incident goes to the same terminal, admin-visible failure state as any other
     unrecoverable send (rule 9) — and is never silently replaced by a fresh key or a plaintext
     fallback. Nothing outside the sending worker's authorized decrypt path, including an admin's
     outbox/incident list view, can turn ciphertext back into a clear token.
   - **Once email is configured, the clear token is never returned to the person who triggered the
     action** (the inviting admin, the person requesting a reset) — the one email is the only
     channel. Before email is configured, the existing one-time API-response copy
     (`people.go:62-67`, `sign-in.md` "Invite lifetime") stays the only channel, since there is no
     email path yet to leak from; this exception ends the moment 1.9 is live for that install.
5. **A restore cancels every pending token-bearing outbox row in the same step that makes the
   restore inert** (Main's call, 2026-11-09, consistent with N10's inert-restore rule). Encrypted
   token copies are ordinary database rows, so they enter backups and point-in-time recovery like
   anything else, and a restored snapshot can contain a pre-restore token that a person already
   used, or a pending invite/reset that has since been superseded. Rather than letting a restore
   silently resend or revive one of these, restoring an install's database invalidates every
   outstanding token-bearing row as part of the same restore step: the token can no longer be
   redeemed, and the outbox entry is marked canceled rather than resumed. Admins see the canceled
   invites and resets in settings and can resend them from scratch. Email sending stays off until
   the restore finishes and this cancellation step has run, so no worker can act on a row from a
   snapshot mid-restore.
   - **This covers every link authority issued before the restore point, not only pending outbox
     rows** (Main's call, 2026-11-09). A restore invalidates every link authority — every
     invite/reset/email-change token hash and the outbox row it travels in, whatever that outbox
     row's state — issued before the restore's checkpoint. Whether the outbox row is `pending`,
     `sent`, or terminal, and whether or not its encrypted payload has already been deleted under
     rule 9, has no bearing on this: the separate verification-authority row (today's
     `person_invites.code_hash`/`used_at`/`expires_at`, `core/internal/identity/people.go:92,111,132`)
     is what redemption actually checks, and that is what the restore step invalidates, for every
     row issued before the checkpoint, before either redemption or sending is re-enabled.
6. Messages split into two kinds, and only the second is bound by a verified-recipient check:
   - **Verification-bearing and address-protecting messages** — an invite, a first-time email
     verification, an email-change confirmation sent to the *new* address, and the matching
     cancel notice sent to the *old* address — are, by construction, sent to an address that is
     unverified, newly proposed, or being superseded. This exception never blocks these; blocking
     them would make verification and email change impossible. They are instead bound to the
     specific pending-address/change generation they prove or protect, so a stale or superseded
     request — one whose generation no longer matches the account's current pending change —
     is refused, not sent.
   - **Everything else** (password-reset links, credential needs-sign-in alerts, other security
     notices, and any message to the credential owner specifically) is sent only to an address
     Blerg has verified as the account's own, re-read and re-checked immediately before send,
     never cached from when the triggering action started. A recheck that finds the address no
     longer verified, or no longer the account's current address, refuses the send.
   - **Before 1.10 ships the verified-email column** (`core/internal/db/migrations/005_identity_accounts.sql:1-11`
     has no verified flag today), the second kind does not exist yet to send: 1.9 sends only the
     admin's test send and invites (Main's call, 2026-11-09). Needs-sign-in and reset email wait
     for 1.10, even though some of those slices are scheduled earlier — the dependency is on
     N2/1.10 landing, not on build-plan phase order. After 1.10, the first-kind exception
     continues to apply to invites, verification, and email-change messages; it is never widened
     to cover an owner alert.
7. Every sign-in- or redemption-triggering action in this area is rate-limited per address, per
   account, and per network origin, using a budget that survives a Core restart and is shared
   across every Core process serving the install — not a single process's memory. Today's
   `LoginLimiter` is process-local (`core/internal/api/login_limiter.go:13-24`) and invite
   redemption keys it by IP only (`people.go:111-113,132`); 1.9's limiter for every
   email-triggering action must be durable (database-backed or equivalent) and must key on the
   target account/address as well as origin. A response never reveals whether an address has an
   account: the same status and generic message are returned whether it does or not, win or lose.
8. **One incident keeps a durable dedup record through its whole lifetime, not only while a
   delivery is in flight.** One incident (one outstanding invite, one outstanding password-reset
   request, one open needs-sign-in state for a credential) produces at most one outbox entry,
   enforced by a database uniqueness constraint on the incident key — not an application-level
   check then insert, which two racing triggers or two Core processes can both pass, and not a
   constraint that only covers rows still being delivered. The record (or a tombstone standing in
   for it) persists until the incident is resolved, consumed, explicitly canceled, or its token
   expires; only then can the same incident key be reused for a new one. Repeating the trigger
   after a message has sent but before the incident resolves does not mint a second message for
   the same broken credential or the same reset; it either no-ops against the existing record or
   is refused. Delivery for an entry is owned by a leased, generation-fenced worker; a worker
   whose lease has expired is not trusted to send, and another worker reclaims the entry after the
   lease ends.
9. **Retries are bounded, and cleanup has a deadline Blerg can test.** A delivery failure is
   recorded with its cause and shown to admins in settings (`sign-in.md` "Email";
   `credential-handling.md` call 6). Retries have a fixed maximum attempt count and a maximum
   incident age, with backoff between attempts; past the bound the incident becomes a terminal,
   admin-visible failure instead of retrying forever or disappearing. SMTP is at-least-once, not
   exactly-once: when a send is ambiguous (for example the connection drops after the server
   accepts `DATA`), Blerg cannot tell whether the message landed, and a retry may produce a
   duplicate physical email for the same incident; this note does not promise exactly one email,
   only that the token/message identity inside every copy is the same one, so the person is never
   asked to prove ownership against two different tokens for one incident. The encrypted token
   copy is deleted (leaving only nonsecret delivery/audit metadata — timestamps, attempt counts,
   outcome) on whichever of these happens first, and each has a bounded deadline that a sweep
   enforces even across a restart: a *definitive* send acknowledgement (not an ambiguous one — an
   ambiguous send keeps the payload until the retry bound or the token's own expiry resolves it),
   the token's own expiry, explicit cancellation or supersession (including the restore case in
   rule 5), or terminal retry exhaustion. A row consumed by redemption before its email ever sent
   (the person used a code the admin handed them directly, pre-1.10) is cleaned up the same way,
   not left pending. None of this changes rule 5's backup/restore handling: a backup copy outlives
   its live-row deletion until the backup's own retention expires, which is why restore carries
   its own cancellation step.
10. An emailed body contains no secret, no token in clear text other than the single-use link's
    own path — the chosen transport is a path-token with client-side URL replacement on landing
    (rule 3), so the template never needs to embed the token any other way — and no detail about
    the receiving provider account. Each email says what action produced it.

## Test matrix

| Situation | Expected result | Layer | Owning slice |
|---|---|---|---|
| Valid invite code, POST before expiry | Redeemed once; account activates | Contract + integration | 1.13 (existing: `AcceptPersonInvite`) |
| Same invite code POSTed twice | Second POST refused, generic message, no account change | Unit | 1.13 |
| Expired invite code POSTed | Refused with the same generic message as "wrong code" | Unit | 1.13 |
| `GET /invite` reached, no POST follows | No redemption, no state change, no email sent | Integration | 1.9 |
| `GET /invite/<token>` landing request (path-token route, the chosen transport — not a query parameter) | Response is `no-store`/`no-referrer`; no third-party asset load; URL is replaced client-side before any other network call | Integration | 1.9 |
| A token arrives as a query parameter instead of the path | Core's rule-1 link-building never emits one; a request shaped that way is rejected/ignored rather than treated as an alternate valid transport | Unit | 1.9 |
| Token-bearing route hit through the ingress/proxy | Ingress/proxy access log and Core's own access log contain no clear token in the path or query | End to end | 1.9 |
| Server logs, metrics, traces after a full invite/reset cycle | Contain no code, token, or ciphertext in clear | Unit (log-capture assertion) | 1.9 |
| Core crashes after committing an invite row, before the email send completes | On restart, the encrypted token is still available to the leased worker; exactly the same token is eventually sent, never a new one | Integration | 1.9 |
| Injected failure between the action write and the outbox write, inside the shared transaction | Both roll back together; restart finds no partial action and no orphaned outbox row | Integration | 1.9 |
| A unique-constraint conflict fires mid-transaction on the incident key | The whole transaction rolls back; no half-committed action, no half-committed outbox row | Integration | 1.9 |
| The database acknowledges commit but the acknowledgement is lost before the caller sees it | On retry/restart, the caller finds the already-committed action+outbox pair and does not re-create it | Integration | 1.9 |
| Email configured, admin invites someone | API response to the admin never carries the clear code; only the emailed link does | Contract | 1.9 + 1.13 |
| Email not yet configured, admin invites someone | API response carries the one-time code (today's behavior), since there is no email path to leak it to | Contract | 1.9 (interim) |
| Encryption key backend restarts with the same key | Previously queued ciphertext still decrypts; sends proceed | Integration | 1.9 |
| Encryption key is missing or the wrong key ID at decrypt time | Decrypt refused; incident goes to the terminal, admin-visible failure state; never a plaintext fallback or a silently generated replacement key | Unit | 1.9 |
| Outbox ciphertext is tampered with | Authenticated-encryption check fails; decrypt refused, treated as the missing-key case | Unit | 1.9 |
| Admin opens the outbox/incident list in settings | View shows status and metadata only; it cannot decrypt or display a token | Contract | 1.9 |
| An install database is restored from a backup/PITR snapshot with outstanding token-bearing rows | Every outstanding token-bearing row is invalidated and marked canceled in the same restore step; none are resumed or resent from the snapshot | Integration | 1.9 (depends on N10) |
| A token already consumed before the restore's checkpoint reappears in a restored snapshot | The restored row is canceled, not treated as still redeemable | Integration | 1.9 (depends on N10) |
| **Backup taken after a definitive SMTP success (and after live-payload deletion), link used after the checkpoint, then that backup is restored** | The restored outbox row's `sent`/terminal state and missing payload do not matter: the restore step invalidates the link's authority (the token hash row) anyway; a `POST` with the old link afterward is refused | Integration | 1.9 (depends on N10) |
| Restore runs with SMTP/email sending blocked or unreachable | Cancellation of pending rows still completes; no row is left ambiguous because sending was unavailable | Integration | 1.9 (depends on N10) |
| Admin checks settings after a restore | Canceled invites/resets are visible as canceled, with a resend action, not silently gone | Integration | 1.9 (depends on N10) |
| Backup/key dependency: a backup is restored without its matching encryption key available | Restore cancellation still proceeds (cancellation needs no decrypt — it invalidates the hash row, not the ciphertext); any row needing that key for anything else fails closed per the missing-key case | Integration | 1.9 |
| Password-reset, email-change confirm/cancel, and break-glass links issued before a restore checkpoint | Same invalidation as invites: the restore step invalidates their authority too, not only the invite shape | Integration | 1.14 / 1.17 / 1.16 (depend on N10) |
| Post-1.10: brand-new invite to an unverified address | Sent despite the address being unverified — this is the proving message, not blocked by rule 6 | Integration | 1.10 + 1.13 |
| Post-1.10: first-time email verification for an upgraded account | Sent to the address being verified, not blocked by the verified-recipient check | Integration | 1.10 |
| Post-1.10: email-change confirmation to the new address | Sent to the new, as-yet-unverified address; bound to that change's generation | Integration | 1.17 |
| Post-1.10: email-change cancel notice to the old address, sent after the switch request | Delivered to the old address even though it is no longer the "current verified" address at send time | Integration | 1.17 |
| Post-1.10: email-change confirmation requested, then superseded by a second change request before confirming | The first (stale) generation's confirmation is refused if used late; only the current generation's link works | Integration | 1.17 |
| Post-1.10: an ordinary owner alert (needs-sign-in, reset) where the stored address is stale or no longer verified | Send is refused by the recheck; no message goes to the stale address | Integration | 3.8 / 1.14 |
| Needs-sign-in triggered before 1.10 ships verified email | No email sent (rule 6 interim); meter and banner still fire | Integration | 3.8 |
| Needs-sign-in triggered twice for the same credential, after 1.10, before it is fixed | Exactly one outbox row for the incident; the second trigger is rejected by the durable dedup record, not by an application check | Integration | 3.8 |
| Needs-sign-in email already sent, but the credential is still broken (incident not yet resolved) | A repeat trigger does not mint a second message; the existing record still blocks a new one | Integration | 3.8 |
| Needs-sign-in resolved (credential fixed), then breaks again later | A new incident is allowed and sends its own message; this is not the same incident as before | Integration | 3.8 |
| Password-reset link sent, then its token expires with the incident still open | Dedup record clears on expiry; a fresh reset request is allowed to create a new incident | Integration | 1.14 |
| An incident's retries are exhausted (terminal failure), then the person tries again | A fresh incident is allowed; the terminal one stays visible as a closed failure, not reopened | Integration | 1.9 |
| **Retries exhaust while the old token is still unexpired and may have been delivered by an earlier ambiguous attempt** | The old token's authority is explicitly canceled/invalidated and the old incident retired atomically before a new generation is authorized; afterward the old link is refused and only the new one is redeemable | Integration | 1.9 |
| Needs-sign-in: an authorized admin resend of the same unresolved incident | Reuses or replaces the existing incident under an explicit resend action, not a budget reset from an ordinary repeat trigger | Integration | 3.8 |
| Two Core processes both race to enqueue the same password-reset incident | DB uniqueness constraint lets exactly one insert win; the other sees a conflict and no-ops | Integration (multi-process) | 1.9 |
| A worker holding an expired delivery lease tries to send | Send refused; a different worker that reclaims the lease sends instead | Integration | 1.9 |
| Core restarts mid-way through a burst of `POST /auth/invite` from one IP | Rate limit budget survives the restart; the attacker does not get a fresh budget | Integration | 1.9 |
| 50 rapid `POST /auth/invite` from one IP, or at one target account from many IPs | Rate-limited after threshold on either axis; uniform 429 for existing and non-existing addresses | Integration | 1.9 |
| Password-reset requested for an address with no account | Same response and timing profile as a real account | Contract | 1.14 |
| SMTP accepts `DATA`, then the connection drops before the final response | Treated as ambiguous, not as failure or success; the encrypted payload is kept, not deleted, until the retry bound or token expiry resolves it; retried within the bound; never silently dropped | Integration | 1.9 |
| SMTP returns a definitive success response | Encrypted payload is deleted immediately after, leaving only nonsecret metadata | Integration | 1.9 |
| Core crashes after a definitive SMTP success but before committing that outcome | On restart, reconciliation treats the ambiguous state as unresolved and follows the retry/ambiguity path, never silently marking it sent without a commit | Integration | 1.9 |
| An invite's code is redeemed by the admin-handed path (pre-1.10 interim) before its email would ever send | The outbox row is cleaned up as consumed, not left pending with a live payload | Integration | 1.9 (interim) |
| An incident exhausts its retry bound | Terminal, admin-visible failure; no further automatic retry; an admin can trigger an authorized resend; the encrypted payload for the exhausted attempt is gone, leaving only metadata | Integration / end to end | 1.9 |
| Downtime longer than a payload's cleanup deadline, then Core restarts | A sweep on restart finds and cleans up any row past its deadline that a live process missed | Integration | 1.9 |
| `PublicURL` unset, or set to an `http://` URL on a managed install, or with embedded userinfo/query | Action refused closed at configuration time; no link emitted on a wrong or unsafe host | Unit | 1.9 |
| Request carries a forged `Host`/`X-Forwarded-Host` header | Link building ignores it and uses only the configured `PublicURL` | Unit | 1.9 |
| Hostile: well-formed code for a different, real account POSTed | Refused with the generic message; no account detail leaked | Unit | 1.13 |
| Landing page visited with JavaScript disabled or the page crashing before client-side replacement | Token remains visible in the address bar for that visit and **may persist in browser history, sync, or a copied URL beyond the visit itself** — `no-store`/`no-referrer` bound the server-side exposure (no caching, no outbound Referer) but do not erase history; the token's own server-side validity window is what stays bounded, not how long a copy might linger client-side; this is the documented residual exposure, not a silent gap | Integration | 1.9 |
| Back/forward navigation or revisiting a path-token landing URL after the token has expired | Landing page shows "expired," not the original valid state; no redemption is possible regardless of what history retained | Integration | 1.9 |
| Full ingress/proxy and application error-log capture around a path-token landing request | No clear token in access logs, error logs, or trace spans, with or without JavaScript enabled | End to end | 1.9 |
| Email template snapshot | No raw token, no provider-account detail, canonical link only | Unit | 1.9 |
| End to end: admin invites a person | One email sent (or an at-least-once duplicate with the same token); link lands on the GET page, one POST redeems, account activates once | End to end | 1.9 + 1.13 acceptance |

## Disagreements

None, in any round. Round 1's six findings and round 2's six findings are adopted as written.
Round 3's one high and two mediums are also adopted as written, with two findings across rounds
2 and 3 decided by Main rather than by this note on its own:

- **Round 2 finding 2** (key-backend contract): Main's call names `core/internal/keybackend`
  explicitly as the required backend, rather than leaving "an approved backend" open; rule 4
  reflects that.
- **Round 2 finding 3 / round 3 finding 1** (backups/PITR/restore): Main's call, given directly
  in both rounds, is that a restore cancels every pending token-bearing outbox row *and*
  invalidates every link authority issued before the restore point — including sent rows and
  their token hashes, not only pending outbox rows — consistent with N10's inert-restore rule,
  with admins seeing the canceled invites and able to resend. This is rule 5, including its
  round-3 sub-bullet, rather than the note inventing its own restore behavior.

## Resolved by Main (2026-11-09)

Five questions have gone to Main across three rounds; all are decided and reflected above.

1. **Verified email doesn't exist until 1.10.** Resolved: 1.9 sends only the admin's test send
   and invites until 1.10 lands; see rule 6. 3.8's needs-sign-in email therefore waits for 1.10,
   even though 3.8 itself is scheduled earlier in Phase 3 — the dependency is on N2/1.10, not on
   phase order.
2. **Who creates the outbox table.** Resolved: slice 1.9 (BLG-563) owns the outbox table and the
   deduplication rule (rule 8). 1.13, 1.14 and 3.8 add rows to it; none of them create schema.
3. **Restore and pending token-bearing rows.** Resolved: a restore cancels every pending
   token-bearing outbox row and invalidates those tokens in the same step, consistent with N10's
   inert-restore rule; admins see the canceled invites and resend them. See rule 5.
4. **Which key backend encrypts the outbox payload.** Resolved: Core's existing
   `core/internal/keybackend` seam, the same one the credential vault uses — never a
   process-local or co-located key. See rule 4.
5. **Restore and link authority already sent/terminal, not only pending.** Resolved: a restore
   invalidates every link authority issued before the restore point — including sent rows and
   their token hashes, not only pending outbox rows — in the same step as the pending-row
   cancellation above. See rule 5's sub-bullet.
