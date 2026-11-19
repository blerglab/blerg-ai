> rc.5 backport scope: rc.4 has no standing Blackboard management feature. Its management toggle, permission table, managed-plan hooks and management-cut path are excluded. Ordinary bound release cuts retain exact authorization. The standing-call-site inventory is adapted to those existing rc interfaces. This backport adds no migrations and does not import BLG-692 infrastructure.

# BLG-748: Standing grant liveness

Status: approved design (Claude round 2); implementation review round 1 corrections submitted, including M1 and L2–L6.
Implementation baseline: integrated main at 45d50c3a3 (the approved spec began at 8b92c4aa6). Target: main and 0.6.0 rc; Main reviews, merges and backports.
Card estimates: M, medium risk, medium complexity. Product phase is undeclared in the checked repository instructions; existing gates remain required.

## Failure rule

A standing permission belongs to the granting account and its recorded scope, with its original human chain retained as provenance. Ordinary expiration or single-browser sign-out must not permanently revoke it. Another live chain of the same account restores use automatically, without Renew. An arbitrary account ID, another account’s chain, an agent token, or an empty proof never establishes standing human authority.

Sign-out-everywhere, chain reuse and account security resets permanently end older standing authority. Explicit grant revoke, resource access loss, missing generation, account disable/removal and terminal runner-session lifecycle rules still deny execution. Regrant must not revive pending work from an old generation. All existing ownership, resource, scope, tool-hash, quota, transaction and external-effect fences remain.

## Approved rc scope

Only these paths change:

- Core adds the standing-proof check inside `principalLiveness` (`core/internal/api/internal_handlers.go`). Existing internal-key endpoints carry the flag; there is no new endpoint.
- Runner sets server-only `standing: true` at standing ticket-board exchange sites, both gateway and `CallBoard`; Blackboard management/cut checks; cluster owner liveness and child credential fetch at pod start; external MCP token fetch. It is not a global proof fallback.
- Blackboard passes the flag into `cutBoardProof`; management plan commits and management cut execution recheck standing authority. Sensitive human one-shot cut execution remains exact. Reconciliation retains token revocation checks while Core owns human standing-chain checks. The executor fence in `blackboard_management_cut.go:74` retains exact generation and login equality. No rewriting stored login IDs to the current login.
- Cluster coverage includes `clusterOwnerState`, `delegation_cluster.go:151-203`, and `matchDelegation` at `delegation_start.go:436`, including the pod-start credential fetch using the parent’s expired launch sid.
- External MCP token fetch includes `mcpgw/core.go:133,174`. Provider-session MCP is excluded and remains exact-chain bound, including Permissions presentation from its frozen binding login; it does not use account connection discovery or standing notices.
- Permissions evaluates truthful effective state, needs no Renew while account authority is live, and labels the existing Board row “Ticket board.” The dead-chain messages at `mcpgw/boardlevel.go` and `board_filing_proof.go` describes permission loss rather than blaming the current sign-in.
- Exchange authority is selected explicitly by each caller: standing gateway/filing calls opt in, one-shot cuts and cron failure cards remain exact, and management cuts opt in from their server-owned execution record. The call-site test inventories the actual exchange callers as well as wire constructors.
- Gateway on-use observations use a server-installed callback in `routes_mcpgw.go`; standalone gateway clients do not require Runner session metadata. The journal compares persisted state before opening a transaction; only changed observations take the advisory lock and shared lifecycle fence, compatible with release execution read fences.
- Notices evaluate on use and Permissions GET only, with one DB-deduplicated notice per grant per state transition. No background reconciliation in rc.

## Exact proof and core predicate

Wire fields remain `{account_id, session_id}` with `session_id` naming the original chain, plus server-set `standing: true` on runner internal-key calls. Engines and browser/agent request bodies cannot select or propagate this flag. Omission/false preserves exact-proof behavior. Token-origin and empty proofs never enter the standing branch (`PersonBehind`, `mcpgw/board.go:106`); preserve same-account binding (`session_board_access.go:577`). Reject mixed token/human proof shapes under existing parser rules.

Core accepts standing authority only if all conditions hold:

1. The original chain exists and all its rows belong to `account_id`.
2. Every revoked row in that chain has a reason in the allowlist `{rotated, superseded, logout}`. Empty, unknown and every other reason deny authority, including `reuse` and `logout_all`.
3. There is no account `lineage` revocation with `revoked_at >=` the original chain’s **first** `created_at`.
4. The account exists, is enabled and is not `must_change_password`.
5. The account has at least one live human chain under existing live-chain rules.

The lineage timestamp condition covers password reset, TOTP reset, break-glass, disable/removal and security policy through `RevokeAccountEverywhereTx` (`human_session.go:903-931`). A reset after the grant followed by a new login cannot resurrect that grant. An older lineage revocation before the original chain was created does not invalidate newly granted authority.

Use this predicate inside the same `principalLiveness` invocation at both initial authorization and recheck-after-mint (`internal_token_exchange.go:176`); do not perform a separate early standing query followed by an exact-only recheck, or skip the post-mint check. Preserve cleanup/refusal when authority changes during minting. Existing browser authentication, shell custody and excluded credential endpoints keep exact proof semantics.

## Legacy records and stores

No migration. Effective-state transitions reuse Runner’s `agent_events` journal and `session_inputs` outbox; they do not rewrite grants. `human_sessions` rows are never deleted; existing `proof_value`, `proof_session_id`, and `login_id` chain IDs establish provenance. Read the complete chain and its first creation timestamp, not only the latest refresh row. Missing/foreign/malformed chain or deleted account denies authority. Keep original proof fields for audit and executor equality; do not periodically rewrite grants. Credential fetch audit separately records the live human row that authorizes access, selected in the same SQL snapshot as the original-chain predicate. Current login existence alone cannot bypass original-chain revocation or lineage reset.

## Inventory: creation, checks, expiration, stores and entry points

Paths below are relative to the repository. Inventory covers standing grants and adjacent authorities that must be deliberately preserved. New entry points discovered during implementation must be added here before changing them.

| Authority | Creation and persistent state | Checks and entry points | Expiration / intended treatment |
| --- | --- | --- | --- |
| Ticket-board permission levels and filing grants | `runner/internal/server/session_permissions.go`, proposal approval in `proposals.go`; `session_proposal_grants` via `db/proposal_grants.go` | Session Permissions GET/create/revoke; session board access/list/call and ticket CLI; `mcpgw/boardlevel.go`; grant-backed proposal auto-approval in `db/grant_approval.go` | Preserve levels, project scope, board ownership and generation checks; replace old-login liveness with account-backed standing authority. Revoke and session end still fence execution. |
| Launch ticket-board connection and discovery proof | Launch handlers `runner.go`, `launch_permissions.go`, `mcpstart.go`; `session_mcp_grants`, `session_board_filing_proof` (`board_filing_proof.go`) | Built-in gateway tools/list and tools/call, `filingProof`, board discovery, ticket filing, plan board adapters (`blackboard_board.go`), child board inheritance | Human-origin launch consent survives same-account sign-in replacement. Token/cron origin remains exact-token scoped and cannot gain human authority. No implicit board-level widening. |
| Blackboard management | `blackboard_management.go`; `blackboard_management_permissions` (account, login, enabled, generation) and audit | Permissions toggle/status; `blackboard_management_plan.go`, `blackboard_management_undo.go`, `blackboard_management_cut.go`; Main-only eligible workspace checks | Original login is audit provenance. Account liveness replaces creator-login gate; enabled/generation transaction fences remain. Workers cannot inherit management. |
| Child-session standing rules | Settings `delegation_rules.go` and “always allow” decisions in `delegation_approval.go`; `delegation_rules` | `delegation_start.go` automatic authorization, `delegation.go` delegate construction, `delegation_cluster.go` owner checks; session-start CLI/API, channels, child pod start (resume/recovery changes deferred) | Rules are already account-scoped; old parent launch proof can still make ownerLive false. Resolve standing human authority for automatic starts without fresh approval. Preserve engine/runtime/daemon/repository limits, quotas and owner checks. |
| Child-session per-parent grants | “for this session” approval; `delegation_grants`, parent lifetime and end epochs in `db/delegation*.go` | Same start paths, grant selection and fenced dispatch; grant revoke API | Same-account login replacement preserves consent; parent terminal end, explicit revoke, discard and ownership loss still invalidate. Already running children retain existing lifecycle behavior. |
| External MCP standing connections and per-tool grants | Launch, resume, Permissions tool settings, approved proposals; `mcpgw/grants.go`, `session_mcp_grants`, `session_proposal_grants` | Gateway bearer-token lookup, tools/list/call; `mcpgw/core.go`, approved execution, `session_permissions.go`, `toolchanges.go`; core `mcp_handlers.go` and `internal_mcp_defaults.go` | Distinguish account-backed standing consent from provider credentials. Preserve URL snapshot, tool definition hash, allow/propose mode, budget, connection ownership, token rotation and removal. No login replacement may silently approve changed tools. |
| Session-bound/provider MCP dispatch | `db/mcpsession*`, `mcpgw/session*`, core `mcpconn/session_identity*`; stored `MCPSessionScope` includes human session, connection, launch/runtime and grant-token binding | Proposal creation, approval, provider dispatch and recovery | Exact provider identity/custody must not be retargeted to a different login by generic standing-grant resolution. Keep exact chain binding and existing suspension; no automatic rebinding. Must receive an explicit negative regression test. |
| Release-cut and other bound one-shot decisions | Questions and approved proposals, release-cut authorization/outbox tables; `release_cut_execution.go`, `release_cut_reconciliation.go`, Blackboard cut services | Approval, dispatch/retry, receipt recovery and reconciliation, board token exchange | Keep 24-hour authorization deadline, exact subject/revision, generation, outbox and receipt fencing. Standing management may create new authorization using current authority; a re-sign-in must not rewrite or revive an old one-shot approval. |
| Auto-resume and managed engine credentials | `session_limit_resume`, `session_managed_credential`, recorded launch auth in `sessions`; limit-resume and enginecredentials handlers | Resume/message/recovery and internal credential fetch/list | Inventory adjacency: do not broadly replace `coreProof` semantics. Standing resume consent must be evaluated separately from exact credential proof; any required credential rebinding must be explicit and reviewed before code. |
| Owner shell, persistent shell, explorer/workspace custody | `ownershell`, shellruntime, persistent consent and workspace launch/custody stores | Shell attach/input/renew, explorer and workspace transport; signed protocol grants | Out of change: retain exact login, audience, runtime, launch, connection, expiry and custody fences. Another live login must not renew an old shell grant. |
| Agent/cron/board-automation tokens and publication authorities | Core identity token stores, cron grant rows; runner publication ownership/adoption records | Token verification/exchange, cron execution, publication operations | Out of change: token revoke/expiry and publication owner checks remain unchanged. Human liveness cannot replace a revoked token or transfer ownership. |

Core stores are `accounts`, `human_sessions` and token/revocation state in `core/internal/identity`; MCP connections/provider bindings live under `core/internal/mcpconn`. Runner grants live in its Postgres database. Board ownership/access is checked through core's board exchange and board service authorization, not inferred from a cached board name. Existing browser sign-in caches (`permission_signin_cache.go`) and gateway credential/read caches are authority-sensitive stores and require invalidation/deadline tests.

## Effective states, notices and Permissions

- Active: all five core conditions and resource/grant/session checks pass.
- Suspended: provenance/security conditions hold but no live chain remains. Single-browser sign-out or expiry may suspend; same-account sign-in automatically restores use.
- Suspended also covers `must_change_password`: execution is denied until the account requirement is resolved. This is the explicit UI classification exception to condition 4; it does not relax the acceptance predicate. A lineage reset remains a permanent denial even if the password requirement later clears.
- Revoked/expired: provenance fails, reuse/logout_all or applicable lineage revocation exists, account is disabled/removed, resource access refuses, generation is gone, or session ended.
- Unavailable: core/store/transport error prevents verification. Fail closed for execution; do not display active or infer permanent revoke.

Never persist suspended/unavailable as grant revocation. Existing permission provenance and generation remain stable across ordinary suspension/restoration. Permissions refreshes effective state on entry. Its first render retains the account-scoped cached presentation, and switching Live tabs preserves an open modal and its draft; cached display never authorizes execution. Preserve authority deadlines and existing cache fences.

On use and Permissions GET, persist state and deduplicate the Main notice transactionally per grant per authority transition. Record short `unavailable` observations silently. BLG-764 adds one verification notice on use/GET after an outage has lasted five minutes, with no expired/revoked or outage-recovery notice. Notify authority loss only for suspended/revoked transitions and recovery from those states; transient outages do not cause loss/recovery notices or repeat a previously notified suspended/revoked state. Repeated checks/concurrent GETs/restart do not emit duplicates for the same transition; a later transition may emit its own notice. Include a safe reason and restoration action. No background timeliness guarantee: an idle, unqueried grant produces no notice until evaluated. Outages must not be described as revoked access. Preserve session visibility, wake and delivery rules and prevent stale queued notices from misreporting a recovered state. Never expose login IDs, credentials or private resource metadata in diagnostics.

Mock and capture only the actual changed Permissions row in the real app at 390px and 3840px; inspect the encoded widths and rendered images. Reuse existing settings, primitives and theme tokens; use American English. No wider page redesign/mockup work. Bump `@blerglab/chat` if changed.

## Required rc tests and mutation proof

Write behavioral red first, then the smallest fix. Exercise wire parsing, internal-key enforcement and persistent legacy records through the affected call paths.

1. Original human chain expires or signs out; another chain of the same account keeps ticket-board gateway, `CallBoard`, plan/Blackboard and standing child starts working without Renew or per-request approval. No live chain suspends; subsequent same-account login restores.
2. Original chain rows must all match account; unknown, empty, foreign and token-origin proofs never use standing authority. Preserve exact nonstanding calls and reject agent/browser attempts to set the server flag.
3. Grant then lineage reset then re-login denies; include original chain already expired before the reset. Test equality at the chain-first-created timestamp, lineage before chain creation, and multiple refresh rows. Reuse and logout_all deny permanently. Account disable/delete deny. `must_change_password` denies execution and displays suspended, without permanent grant revoke.
4. Child board inheritance covers `board_filing_proof.go:17` / `msgChildFilingSignedOut`. Cluster child credential fetch uses the parent’s dead launch sid with standing authority and succeeds only while its standing conditions hold; automatic rule/grant selection does not fall back to fresh approval merely because that sid expired.
5. External MCP token fetch survives same-account login replacement. Provider-session MCP remains exact-chain bound and suspended by `suspendMCPBindings` (`human_session.go:653`): a different live chain cannot revive it. Token-origin grants remain exact-token bound.
6. Explicit permission revoke, lost board access, changed tool hash/removed connection, parent/session end and stale generation still deny. Blackboard cut executor retains exact generation + stored login equality; no new-login substitution. Preserve one-shot deadline/subject/outbox/receipt fencing.
7. Race authority loss with mint: both initial and post-mint `principalLiveness` use standing rules, recheck refuses and cleans up when authority changes. No external effect follows denied authority. Core failure is unavailable, not active/revoked.
8. Permissions and CLI messaging agree with effective state. One DB-deduplicated notice per grant per authority transition on use/GET, including concurrent evaluations, restart, recovery and queued delivery. Outage flapping is silent; unchanged observations do not acquire the publisher lock. Do not require background notice delivery.
9. A Runner AST inventory pins every production standing-field constructor/assignment. Real managed-access and shell authorization transports assert omission/false; the automatic limit-resume integration asserts credential fetches remain exact. Browser/agent JSON cannot set the private proof flag.
10. Mutation: remove the lineage-timestamp condition and observe the reset-after-grant regression fail for the intended assertion. Restore and rerun. Also prove the survival test fails when reverting the standing branch. Every changed authorization guard needs an applicable negative test.
11. Run complete touched-region unit/browser suites, lint, types/build, scrub and links; record exact commands, timings, failures and skips. Use throwaway local Postgres if available; otherwise list the DB tests not run. Main owns merged gate and rc backport.

## Main-only follow-up (outside rc)

- Background reconciliation and proactive notices for idle grants; the rc only evaluates on use and Permissions GET.
- Limit-resume, managed credential renewal and broader resume/recovery authority changes. `session_limit_resume`, `session_managed_credential` and their credential paths stay exact in rc; the explicitly scoped child pod-start fetch above is the exception.
- Any broader standing-site rollout, provider-session rebinding design, new proof endpoints, schema/provenance migrations or generalized evaluator refactor. None is necessary for this rc proof shape.
- Broader UI work beyond the Permissions row and its required responsive captures.

Owner shell/persistent shell, explorer/workspace custody, token-origin cron/agent authority, publication ownership and sensitive bound one-shot approvals retain their exact bindings. Inventory inclusion is not authorization to broaden them.
