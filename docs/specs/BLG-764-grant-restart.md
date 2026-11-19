> rc.5 backport scope: rc.4 lacks dev Board ownership and Blackboard management.
> Use existing scoped Board discovery; an absent board stays unavailable without
> positive verification. Exact one-shot cuts never opt into standing authority.
> Migration 196 is byte-identical to dev; no intervening dev migrations are added.

# BLG-764: standing grant observation during restart

Execution fails closed on every refusal. A standing request to an older Core may
receive a bare 404 because that Core ignores `standing`. Only an explicit
`X-Blerg-Standing-State: suspended|revoked` classifies authority loss; unmarked,
unknown, unavailable, transport, canceled, timeout and 5xx failures are unavailable.
Exact/token-origin behavior remains unchanged. Core defaults standing responses
to unavailable, overriding only authoritative refusals. Ownerless legacy boards
are unavailable (`board_owner_missing`), with a rate-limited safe diagnostic;
this does not assign ownership or widen authority.

Entry points: Core token exchange (initial principal, account, board ownership,
post-mint resource/proof checks), MCP token gate and FetchSecret, board discovery,
credential list; board discovery accepts an optional scoped `board_id` to verify
owner provenance before a missing-list entry can be called revoked. A scoped
empty result without the new Core active-state marker remains unavailable,
because older Core versions can ignore the scope and omit ownerless boards. Runner gateway board exchange and MCP token parsing, resource
list parsing and principal checks, Permissions GET, bound board and Blackboard
checks. Stores: original chain/account provenance stays unchanged; observation
state/time and the one-shot outage marker use keyed
`grant_authority_observations` rows; transitions and delivery retain the existing
agent_events journal and session_inputs outbox. The migration backfills old
journal keys, adopted once when original provenance is supplied. No credential/proof identifiers enter notices or logs.

Notices use target Main + kind + resource + hashed account/original proof,
so inherited observers share history and independent proof chains stay separate.
Asked-at timestamps are captured before Core requests; an older completed result
cannot replace a newer observation. Same-key transitions remain serialized in
Postgres across processes. Unchanged observations avoid the publisher lock except
when an unavailable observation reaches five minutes: publish one verification
notice for that outage, never an expired/revoked or active-again notice. Genuine
suspension/revocation and recovery still notify once per ordered transition.

Claude spec attack through Main requested: explicit headers on every standing
refusal, separate provenance keys, ordered observations, bounded outage silence.
Main requested these changes and the ownerless legacy regression before code.

## Review corrections

One-shot cut proof checks and Board reads retain exact authority and terminal
refusals. Only management cuts explicitly opt into standing verification; their
explicit revoked/suspended responses remain terminal. This applies to preflight,
execution, receipt recovery reads, and the gateway BoardCall. No standing notice
is emitted by an exact one-shot proof check. Creation preflight may use standing
consent only after selecting an enabled management permission for eligible Main;
that read-only selection does not authorize a cut. The later management
transaction still rechecks the permission and generation before authorization.

The safe `board_owner_missing` diagnostic stays unavailable for execution and
is carried into the Permissions explanation and the bounded notice: the board
has no recorded owner; its owner or an administrator must assign an owner. A
retry alone is not presented as a repair. Different original proof chains on
the same board at Main must each retain their own loss and recovery notices.
