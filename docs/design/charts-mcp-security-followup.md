# BLG-514 security follow-up

Polling remains disabled. No push or merge into the named main branch was performed.
Work is on the assigned session branch and worktree. Main was refreshed through
`8f2c0a93e` in merge `212c6d0a9`.

## Delivered

| Request | Implementation and proof |
|---|---|
| Human revocations during authority outage | `e5250d31c`: migration 161 commits grant denial plus a durable local retry ticket; outstanding tickets deny polling. Seven real SQL/service action cases prove commit, denial and acknowledgement retry. `6347098b3` additionally completes startup revocations before issuer readiness. |
| Per-guard record | Published full-ledger session artifact (linked from the proof index): 1,816 current indexed entries, source hashes, test selectors, proof commits and observations. No indexed entry lacks a record. 669 assertion-detected removals; 1,117 survivors; 29 runtime/harness outcomes; one timeout. This is a complete generated catalogue record, not an exhaustive semantic authorization certification. |
| MCP dashboard export | `2c8289dc1`: dashboard SVG/PNG exports are explicitly blocked; original source contains references. `03ad5ddfc` prevents a retained dashboard drawing from entering a later visual kind's export cache. Poisoned renderer and bypassed disabled-control tests cover the boundary. |
| Queue and owner rotation | `17a49ec5a`: durable ordered owner turns. `TestAttemptQueueEightySlotsAcrossReplicas`, `TestScheduleQueueWindowIsEighty` and `TestScheduleFairOwnerRotationBeyondQueueWindow`; independent 81-row window and removed-owner-priority mutations both fail. |
| Unsupported setup | `1ea1ea00a`: new configuration requires native stat/gauge without sparklines or custom specs. Core and Runner refuse unsupported/missing declarations before listing, binding proposal or ask creation; refusal names supported types. |
| Additional client expiry defect | `522065cfe`: every render checks the absolute authority deadline. A layout-phase observer proves no expired cached value is committed when a binding is selected again. |
| Owner scope decision | Owner-name private-source display and authorized static snapshots were dropped by owner decision on **2026-11-05**. They are not remaining work. |
| Review #6: mutation database target | Backend audits, including catalogue-only mode, refuse before compilation unless both URLs are identical, PostgreSQL URLs on localhost/127.0.0.1, and marked by port 55440 or a delimited test/tests database name; connection-target overrides are refused. `TestBackendRefusesUnsafeDatabaseBeforeCompiling` and `TestDatabaseTargetValidation` prove refusal and allowed targets; removing the entry guard fails all five refusal subcases. |

## Enable checklist — blockers from Claude review of `290449660`

Review approved with low findings; polling stays disabled. Items 1–5 were subsequently authorized as a security follow-up. Polling remains disabled; neither enable flag changed.

- [x] #1 — Startup invalidates every existing approval before readiness. This conservatively fences a pre-revocation snapshot even when its lost revocation never reached the independent authority. **Ordinary Core restarts now require reapproval**, including a replica joining the installation. `TestRevocationCheckpointStartupInvalidatesUncheckpointedRestore` restores local authority state after an outage and proves denial after recovery. Checkpointed rollback still requires an independently advanced epoch. Restoring into a running database without restarting Core remains unsupported; database administrators can bypass database-local history. Stop Core replicas for a restore.
- [x] #2 — Sync acknowledges committed `high_water`, not nontransactional sequence reservations. Check uses that same committed boundary. `TestRevocationCheckpointRollbackRecovers` proves outage/rollback recovery without an operator fence.
- [x] #3 — Both startup authority operations finish before the singleton row lock. `TestRevocationCheckpointStartupDoesNotLockHumanActions` explicitly blocks the authority write and requires a concurrent human revocation to commit within its one-second failure deadline.
- [x] #4 — A failed startup Sync leaves a constructed issuer unavailable, with bounded background retries. Approval admission remains zero until Sync succeeds. `TestLiveDataStartupSyncRetriesAndRecovers` and `TestLiveDataApprovalWaitsForStartupRecovery` prove refusal and recovery without timer sleeps.
- [x] #5 — `Claim` already acquires the shared attempt advisory transaction lock before binding and owner rows, so concurrent claimers are serialized. `TestScheduleConcurrentClaimers` verifies three replicas over five rounds, distinct fences/leases, full batches, and no claim refusal. The owner-turn ordering was retained.

No polling enablement decision is made here. The concurrent test passed before any production scheduling change; only the lock contract comment was clarified.

## Enable-blocker verification (2026-11-06)

Focused Core `livedata`: 36 test/subtest passes, no failures or skips, 5.392s.
Core live-data command tests: 28 passes, one expected child-fixture skip, 4.402s.
Runner `livepoll`: 66 passes, no failures or skips, 9.775s.
The full filtered cross-process live-data matrix: 22 top-level tests,
79 test/subtest passes, no failures or skips, 179.252s. It proves startup
invalidation, explicit reapproval, checkpointed restore refusal, key persistence,
and the existing transport/privacy boundaries. No unfiltered server tests ran.
Four independent guard removals failed their intended regression assertions;
the restored source passed. The concurrent-claimer proof passed on the existing
serialized implementation, rather than reproducing a current deadlock.

Core and Runner region vet, builds and pinned lint passed. Formatting, source
scrub and whitespace checks passed. A final Gateway process rerun after a
fixture-only lint correction passed all six tests/subtests (26.771s). The full
repository gate, browsers and performance suites were not run.

## Review round 1 follow-up

Items 2–4 are implemented independently of clean-restart preservation. The
complete optional issuer bootstrap, including authority initialization and
`RevocationCheckpoint.Start`, retries at the same 100 ms cadence as Sync.
An immutable issuer snapshot is published atomically; copied API epoch callbacks
and the private transport recover together. Until then, admission is zero and
the private issuer refuses requests. Human authentication remains available.
`TestLiveDataBootOutageRetriesAndRecovers` and the real authority outage fixture
prove retries, transport publication, admission recovery and cancellation.
Requested reservations are raised above committed high-water, requested and
acknowledged; `TestRevocationCheckpointReservationFloor` proves the floor.
Migration 161's comment now describes enforced startup invalidation, and Check
reads high-water only once.

**Main decision, delegated by the owner (2026-11-06): retain conservative startup revocation.**
Live-data grants require reapproval after a Core restart. Polling remains off, so
this changes no currently enabled behavior. Clean-restart preservation is deferred
to the enable blocker below. The tested, uncommitted marker prototype preserves
graceful restarts and rejects ordinary
crashes. However, clean stop → authority outage at the next boot → human
revocation → crash → restore of the earlier snapshot leaves the external clean
certificate unused and matching the restored database. The prototype's real SQL
counterexample resurrects the grant, even with an independent replica registry.
An offline boot cannot register there. Preserving approvals while keeping human
authentication independent requires a boot journal outside database backups
that remains available during the authority outage.
The conservative startup invalidation remains in delivered code; no marker schema
or authority-file change is delivered. `TestRevocationCheckpointOfflineBootRestoreRevokes`
proves the retained fallback for this history. Both enable flags remain unchanged.

### Enable blocker: independent durable boot journal

- [ ] Before enabling polling, implement and prove an **independent durable boot
  journal** that allows approvals to survive Core restarts safely. Keep it outside
  database backups and available during an authority outage. It must record every
  Core boot before human
  revocations can occur, including failed issuer boots, and account for crashes
  and multiple replicas. A marker in the authority store alone cannot establish
  this history when that store is unavailable.
- The clean-shutdown marker prototype is rejected as sufficient evidence for
  preservation: clean stop → offline boot and human revocation → crash → restore
  reuses an unconsumed matching marker and resurrects a revoked grant. Its real
  SQL regression failed; the prototype is not shipped. The retained startup
  revocation regression passes for the same history.
- Require proofs for graceful restart, crash restart, authority outage at boot
  and stop, replica overlap, replayed markers, and pre-revocation snapshot restore
  before changing preservation behavior. Human sign-in, sign-out and reset must
  remain independent of optional live-data authority availability.
- Review items 2–4 are delivered now: complete Start/bootstrap retry, reservation
  floor above acknowledged tickets, and corrected comments/single high-water read.
  The owner alone decides when to change either enable flag.

Independent follow-up verification: Core `livedata` 38 test/subtest passes
(6.971s); all Core command tests 61 passes plus one expected child-fixture skip
(10.608s); focused Gateway/CoreOutage/PersistedKeyReplacement process consumers
8 passes (19.256s); boot/startup/admission race tests 5 passes (3.203s).
Four guard mutations fail their intended assertions. Regional vet, build, pinned
lint, formatting, whitespace and scrub checks pass. The failing clean-marker
prototype remains outside the delivery tree and is not reported as a passing fix.


## Evidence and limits

The source-indexed ledger includes broad control flow as well as authorization:
ordinary branch selection, simple/tagless switch choices, boolean-result variants,
SQL conjunctions/disjunctions, active trigger predicates and explicitly named
side effects. Survivors include redundant checks, unreachable branches, ordinary
control flow and missing negative fixtures. They are not automatically classified
as safe or equivalent. Runtime failures and timeout results are not clean
assertion proofs. Review must establish semantic source completeness and classify
these observations before any enablement decision. Type switches, complex switch
selectors and unindexed side effects remain explicit mechanical-inventory limits.

The local-only revocation interval can lose its SQL record in a database restore.
Startup now enforces conservative invalidation rather than trusting that lost
record or assuming an operator advanced the epoch. All old approvals are denied
before the issuer becomes ready, and new approval is required after every Core
startup. Independent checkpoint history still detects a checkpointed rollback.
This is a startup boundary, not protection against an administrator modifying or
restoring a database underneath running Core replicas.

The audit found and fixed two client defects: expired values briefly committed
before passive cleanup on binding reselection, and retained dashboard SVGs being
exportable after a visual-kind change. Negative fixtures now poison later checks
with otherwise valid data, including HTTP refusals, oversize/truncated receipts,
queued callbacks, old epochs, stale binding/session tags and removed access.

## Verification

Final touched browser: 4/4 PASS, zero skips, 134.91s (127.37s test time), including
Vite dependency reoptimization. Final process JSON: 22 top-level tests, 79 tests/subtests PASS, zero failures or
skips, 148.005s on the final integrated tree.
Earlier whole process matrix: PASS, 197.556s. Earlier touched browser run: 4/4
PASS, 73.98s. Restored client audit baseline: 49/49 PASS, zero pending; latest
focused client reproof baseline has 61 tests and all seven selected removals fail.
Types and chat/Runner/Board builds pass; focused Go/chat lint and source scrub pass.
The final touched browser and full process runs used the source/test tree at
`9191e2fed`, containing main `8f2c0a93e`; they did not run in the shared main
checkout. All build steps passed after correcting the optional test view field.
The repository-wide gate, unrelated browser suites and performance suites were
not run.

Initial failures were retained and corrected: asynchronous checkpoint fixtures
assumed an external acknowledgement, startup reported ready before a new ticket
settled, a test fixture used a row ID instead of a login-chain ID, an optional
view field failed TypeScript checking, and mutation tooling needed separate
production-package naming, null-safe partial removals and DOM matcher handling.
These are not reported as product-test passes. Deliberate mutations are red
proofs; only the restored baselines are green tests.

## Commit manifest

This first-parent manifest includes the incoming package version commit and main
merges, then all session commits through the final code/test revision. Later
ledger/report commits are listed in the final response.

| Commit | Subject |
|---|---|
| `d7fc7bace89d5a9e7f4deaed1badce341a926fc7` | chat: 1.59.39 (BLG-514 chunk 4 changed chat content) |
| `e5250d31c458e7aaa2d3740c23b096336563a340` | fix(live-data): commit human revocations during authority outages |
| `2c8289dc181416f9265f225eddc4de5e14dca441` | fix(charts): explicitly block dashboard payload exports |
| `17a49ec5ad119f740e4aa6b87cfce405260a7c6f` | fix(live-data): rotate owners fairly within the bounded queue |
| `1ea1ea00abe558b15395b8f0e56f2780c01413bb` | fix(live-data): refuse unsupported native card setup before approval |
| `fb1f8a78402de108257cdc5265449d9d3c19f630` | Merge branch 'main' into wip/blg-514-chunk-4-finish-from-5e5a68df-f7c2807c |
| `bbf12d0682eaa9f6a3c48d4956f5834f61a5e4b0` | test(live-data): cover credential parser boundaries and isolate process builds |
| `7a2d61dc894deeac20b8fa6e3e23e3e49f9a20de` | Merge branch 'main' into wip/blg-514-chunk-4-finish-from-5e5a68df-f7c2807c |
| `9668e70d9e0286a7d5cfebca5d0eb4f8c212ea5e` | test(live-data): reject ambiguous identities and invalid issuer responses |
| `4595c7b0861e7abe4e06bb46dd75275858bb7260` | test(live-data): exercise closed schema rejection boundaries |
| `e45c1e50aecc6116093f81896354e656dbf69f89` | test(live-data): reject tampered immutable approval selections |
| `6347098b373fc804c121adea4a4e39c52916be21` | fix(live-data): settle startup revocations before issuer readiness |
| `6468a10d96fc620cfb212851f8b63321d75728eb` | test(live-data): add isolated source-indexed predicate mutation audit |
| `c05508451fa7838985b5dd094a16d5af74e36461` | test(live-data): await checkpoint completion in restore fixture |
| `aa73298b107a29c121d8ecacdd68c7dc9b5e31a1` | docs(live-data): index follow-up proofs and checkpoint restore limits |
| `f375a92d6d4188e17b9d95ce4b8a1f93a78c9d11` | test(live-data): prove authority again at approval transaction boundary |
| `38d45c597e25e70bf82d231dcbd0dbf4676c7cde` | test(live-data): cover each transaction SQL authority predicate |
| `5d06f79d5755187f0dfb3595a329aa5c244a7b90` | test(live-data): exercise account join isolation during approval |
| `e8915ba16023f3e9e74896ce750c176b7c2aa210` | test(live-data): inventory shared authentication and secret-release guards |
| `66d29c5bcd062e16dde9f234a73ac115436036c8` | test(live-data): include queue window and owner rotation removals |
| `55276f341f8f7635dc850d5184e5e9d16a58447b` | docs(live-data): describe focused guard reproofs and scheduling mutations |
| `697bd9272c8a7d5e2a803cbbc467adecad604108` | test(live-data): poison callback fallbacks and audit redirect refusals |
| `522065cfeaaca6cbe8fc56537237b76855e83c2f` | fix(charts): check current authority deadline before rendering cached MCP rows |
| `dae9b4cb2c5d6c8e13a4bf8dad0995fa515aae48` | test(charts): audit client authority, retention and export guards independently |
| `1b3010d8e20ea0d9b12857f40d5fa1f4e0bed5be` | test(charts): retain null safety in scoped guard removals |
| `212c6d0a973774e0f8c230dbd3f5b650c037d723` | Merge branch 'main' into wip/blg-514-chunk-4-finish-from-5e5a68df-f7c2807c |
| `6410f5a459fae365f7dca51ae68eed12e316223f` | chore(chat): version MCP boundary and expiry fixes as 1.59.42 |
| `23624e18c2e67b3bfaa859166fd75b1b8de00efc` | test(live-data): include canonical approval and mapping guards in audit |
| `3185b82f40c0aa560c97bae45dfe46c5075b1908` | test(live-data): cover authorization path selection and shared mapping contracts |
| `03680795acee4537bc73be87a8fd2d41a2c4bdb4` | test(live-data): enumerate authorization branch choices and canonical input bounds |
| `03ad5ddfc918f1c2f2d881f618a8525b5e8fd2c2` | fix(charts): keep dashboard payloads out of reusable export state |
| `e4f45d51938e86e1017a08893c20e2489d2793ed` | test(live-data): derive mutation helper package from production source |
| `bf7c49cda2be4de8b86315e9b3ab9d36fa00085e` | docs(live-data): document audit scope and measured mutation deadlines |
| `9191e2fed94beba72cf35296d317ed4a688738ea` | test(live-data): retain source provenance when joining guard audit phases |

Final restored client region: six files, **67/67 PASS**, zero skips, 3.24s.
Chat/Runner types, chat/Runner/Board production builds, focused Go/chat lint,
source scrub and whitespace checks pass. The unchanged disabled rollout constants
were checked again before committing this evidence.
