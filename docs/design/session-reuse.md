# Refresh-token reuse: what it means and what it costs

*2026-10-06. Core, contracts, board, runner.*

## The problem

Core rotates the refresh cookie on every `GET /auth/refresh`. A rotated-out token presented again
used to sign the account out of every device (and, until recently, revoke its agent tokens too).
That is the right response to a stolen cookie and the wrong one to what actually happened, twice in
a week: a browser presented a cookie that core had rotated minutes earlier because the response
carrying the new cookie never reached it. The hidden refresh frame is torn down after 15 seconds;
a phone on a slow link, or a tab the browser froze, loses the response after core has committed
the rotation. The browser then holds the old cookie, and the next refresh looks like theft.

A grace window does not fix this: it only moves the cliff. A tab that wakes an hour later is the
same case.

## What the mature providers do

Refresh-token rotation with reuse detection (OAuth 2.1 §4.3.1; Auth0 and Okta ship it) has three
parts, and Blerg now has all three:

1. **Reuse revokes the token family, never the account.** Each browser session is a rotation
   chain (`human_sessions.chain_id`, carried in access tokens as `sid`). Reuse ends that chain:
   its rows, and its minted access tokens through a `sid` entry in the shared revocations table
   that core, board and runner all apply. Other devices and the person's agent tokens are
   untouched. Account-wide sign-out stays for explicit actions: password change, admin disable,
   `POST /auth/logout-all`.
2. **A lost rotation is recognised, not punished.** A successor row starts with no
   `last_used_at`; it gets one when it is presented. A rotated-out token whose successor was
   never presented is a browser that never received the rotation, and the rotation is redone
   (the never-used successor is revoked `superseded`, a new one is issued). This replaces the
   60-second grace window entirely and covers the two-tab race the same way: no clock, no cliff.
   Detection fires exactly when a token is presented twice and its successor has been used, which
   is the only state in which two parties demonstrably held the same token.
3. **The client stops manufacturing stale cookies.** The shared `authClient.ts` holds a Web
   Lock (`blerg.auth.refresh`) for the whole life of a refresh frame, so the tabs of an origin
   refresh one after another; and a frame is never torn down while its request is in flight: a
   caller gives up after 15 seconds and falls back, but the frame stays (up to two minutes) so
   the cookie core sets on its way back is taken up.

`human_sessions.revoke_reason` (migration 018: `rotated`, `superseded`, `reuse`, `logout`,
`logout_all`) records why each row ended, for the operator log today and a signed-in-devices page
later.

## What a stolen cookie gets an attacker now

Up to the victim's next refresh. If the attacker presents the stolen token before the victim uses
its successor, the rotation is redone in the attacker's favour; the victim's next refresh then
presents a token whose successor has been used, which revokes the chain, and the victim signs in
again. If the victim refreshes first, the attacker's presentation is the reuse and the chain is
revoked at once. Either way the exposure is bounded by one access-token lifetime (ten minutes)
plus one refresh interval, the same bound Auth0's and Okta's reuse detection gives, and no other
device is affected.

## Not done

- A signed-in devices page (list chains by user agent, IP and last use; revoke one or all), which
  the `revoke_reason` column and `RevokeChain` are the groundwork for.
- A per-reuse audit row beyond the operator log line.
