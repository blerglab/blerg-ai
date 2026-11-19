# BLG-514 MCP polling proof index

Polling remains disabled. This is an evidence inventory, not permission to enable
it. The issuer constant is false and Runner has no production activation wiring.
Test-only fixtures opt in explicitly. No provider credential belongs in a card.

The tests below are exact searchable test names. Existing proofs are inherited
from the prior worker through `5e5a68df8`; listing them does not claim they were
rerun in this session. This continuation adds stream recovery and presentation
proofs. Backend process tests use operator-seeded approvals, separate Core,
Runner/gateway and visibly simulated MCP processes; human approval has its own
process test. Browser tests use a real Runner socket and private PostgreSQL
fixture with canned authorized samples. They prove presentation and transport,
not a real provider invocation or a production activation.

## Addendum rules

| Rule | Proof test(s) and boundary |
|---|---|
| Dispatch only approved exact revision, fresh authority, metadata, allow, visible demand, lease, quotas and committed audit; no session/agent fallback | `TestVerifierFreshAuthorityAndApprovedArguments`, `TestVerifierFailureGuards`, `TestSchedulerCommittedAttemptBeforeMintAndCall`, `TestLiveDataCrossProcessGateway` |
| Transient failures preserve authorized rows and original time, never zero | `TestLiveDataCrossProcessProviderFailures`; `useDashboardDataset.test.tsx`: “shows safe … state without changing last-good rows or time”; `MCPDatasetCards.browser.test.tsx` checks value and unchanged fetch time |
| Terminal loss/expiry/deletion/no-view pause purges derived payload and preserves audit | `TestLiveDataCrossProcessAuthorityLoss`, `TestLiveDataCrossProcessExpiredApproval`, `TestLiveDataCrossProcessRetentionPurgesQueuedRows`, `TestLiveRetentionAndNoViewPause`, `TestLiveAuditRetention` |
| One-second shared subscriber authority, exact scope/sequence; local owner/expiry/epoch uncached; periodic recheck | `TestMCPAuthorityCacheScopeSequenceExpiryAndBound`, `TestDatasetMCPAuthoritySharedAcrossSubscribers`, `TestDatasetMCPSubscriptionChecksOnlyChangedSamplesAndPeriodicAuthority`, `TestDatasetMCPCachedAuthorityCannotLowerEpoch`, `TestDatasetMCPReadCacheCannotExtendExpiredApproval` |
| Read audits only for refusals/state changes; mint/renew/call audits remain | `TestIssuerReadAuditsOnlyStateChanges`, `TestIssuerAuditAndScopeRefusal`, `TestIssuerProviderAuditKeepsAttempt` |
| Core records owner approval using actual verified human credential and exact bound ask; service/agent receipt cannot approve | `TestLiveDataApprovalRealProcess`, `TestLiveDataApprovalGuards`, `TestLiveDataApprovalRequiresLiveOwner`, `TestLiveDataRegisteredAskAndRunner`, `TestLiveDataApprovalNeedsPostProposalToken` |
| Immutable canonical configuration/digest, complete scope, ambiguity refusal | `TestRevisionDigest`, `TestRevisionSchemaRefusals`, `TestVerifierRejectsUnapprovedConfiguration`, `TestLiveDataApprovalGuards` |
| Exact idempotent decision confirmation/outbox, canceled/stale/expired revisions cannot activate | `TestLiveDataDecisionReconciliation`, `TestLiveOutboxTerminalRevocation`, `TestScheduleActivateResetsViewOnlyForNewRevision` |
| Default-off per-tool policy; owner-only Settings raise, lenient reductions, no resurrection | `TestLiveDataOwnerSettings`, `TestLiveDataSafeRuntimeStillRequiresPolicy`, `TestPostgresDurableAuthority`, `TestLiveDataCrossProcessAuthorityLoss` |
| Untrusted explicit read-only claim and exact fresh hash, auto_accept ignored, old hash cannot restore grant | `TestLiveDataRequiresFreshReadOnlyListing`, `TestLiveDataListingFreshnessBound`, `TestLiveDataCrossProcessToolFences`, `TestLiveDataCrossProcessFailedRevocationFencesLocally` |
| Arguments schema/nonsecret validation before proposal and approval; no stripping; canary rejection without persistence | `TestSecretArgumentCanaries`, `TestLiveDataSecretArgumentsNeverPersist`, `TestLiveDataApprovalRealProcess`; numeric/schema edge cases: `TestRevisionNumericBounds`, `TestRevisionSchemaRefusals` |
| Dedicated mTLS identity; shared key insufficient; identities bound to installation/Runner | `TestRunnerIdentityRequiresVerifiedCertificate`, `TestIssuerDedicatedTransport`, `TestIssuerTransportRequiresDedicatedVerifier`, `TestLiveApprovalDedicatedTransport`, `TestLiveDefinitionServiceIdentity` |
| Runtime/admin posture refusal; explicit epoch-bound owner consent, no caller attestation | `TestLivePostureDesktopAndUnknownHost`, `TestLivePostureTrustedInventoryAndRecordedHosts`, `TestLivePostureManagedPodBoundary`, `TestLivePostureKubernetesFreshAndFailClosed`, `TestIssuerDesktopAlwaysRequiresConsent`, `TestVerifierDesktopAlwaysRequiresConsent`, `TestLiveDataConsentMustBeRenewedForActivationEpoch` |
| Capability is not authority to create/reuse attempts or substitute arguments; DB role not isolation | `TestAttemptSingleUseAndEpoch`, `TestVerifierFreshAuthorityAndApprovedArguments`, `TestScheduleCredentialStageSingleUse`, `TestLiveDataCrossProcessGateway` |
| Exact EdDSA/type/kid/audience/issuer/scope/time; dedicated signing state; no token-selected key | `TestCredentialIsolation`, `TestLiveDataCrossProcessSignedCapabilityVariants`, `TestPrivateKeyMountRefusal` |
| Ordinary key rotation bounded overlap; compromise immediate epoch fence/new approval | `TestKeyRotationAndCompromise`, `TestLiveDataCrossProcessCompromisedKeyFence`, `TestLiveDataCrossProcessPersistedKeyReplacement` |
| UTC, max five-minute capability, no expiry grace, bounded 30-second uncertainty; no extending leases | `TestCredentialIsolation`, `TestLiveDataCrossProcessCapabilityAndClock`, `TestLiveDataCrossProcessExpiredApproval`, `TestLiveDataCrossProcessUnusedAttemptExpires` |
| In-process verifier unreachable from public/sandbox/session routes; no credential in browser/source | `TestLiveDataCrossProcessSessionListenersRefuseCapabilities`, `TestLiveAttemptDedicatedIdentityAndDisabledGate`, `datasets.mcp.test.ts`: opaque binding references only |
| Exact durable attempt/demand/due/fence/scope/argument validation | `TestAttemptFences`, `TestAttemptCannotLowerApprovedInterval`, `TestVerifierFreshAuthorityAndApprovedArguments`, `TestLiveDataCrossProcessDemandAndLeaseFences` |
| Atomic consume/commit before provider call; quotas shared across replicas | `TestAttemptSingleUseAndEpoch`, `TestAttemptConcurrencyCaps`, `TestAttemptConnectionRateLimit`, `TestLiveDataCrossProcessSharedQuotas`, `TestLiveDataCrossProcessAuditFailures` |
| Fresh tools/list and grant/credential check on every dispatch; no positive outage cache | `TestLiveDataCrossProcessToolFences`, `TestLiveDataCrossProcessCoreOutage`, `TestLiveDataCrossProcessGatewayOutageAndRestart`, `TestVerifierDistinguishesRevocationAndOutage` |
| Persist highest epoch; fresh storage/delivery authority; revoke in-flight results | `TestAttemptSingleUseAndEpoch`, `TestVerifierRevocationDuringCall`, `TestLiveDataCrossProcessGateway`, `TestDatasetMCPOwnerOnlyAndDeliveryRevocation`, `TestDatasetMCPQueuedFrameCannotBorrowNewScope` |
| Crash/uncertain completion stays consumed, new audited retry, no exactly-once claim | `TestLiveDataCrossProcessCrashKeepsAttemptConsumed`, `TestScheduleReplicaLeaseAndNoCatchUp`, `TestAttemptExpiredAuditIsRefusedWithoutReusingReceipt` |
| Owner/account/connection/policy/read-only/hash revocation immediate; security epoch on logout/reset; ordinary login expiry does not revoke durable grant | `TestLiveDataCrossProcessAuthorityLoss`, `TestPostgresDurableAuthority`, `TestLiveDataApprovalSessionFence`; inspect authority-loss subcases for password reset/logout/connection/policy/owner |
| Every scheduled call attempt and every mint/renew/refusal audited; audit failure prevents dispatch/release | `TestLiveDataCrossProcessAuditFailures`, `TestLiveDataCrossProcessSecretAuditFailure`, `TestScheduleRefusalCompletesAuditWithoutDispatch`, `TestIssuerAuditAndScopeRefusal` |
| Safe typed rate-limited diagnostics, no arguments/bodies/credentials; actual access-log canary | `TestLiveDataApprovalRealProcess` checks Core access logs; process harness checks child logs and serialized replies; `TestMapSafeDiagnostics`, `TestMapSafeErrors` |
| Dedicated live audit, no fake session ID/double counting; 180-day metadata, 24-hour payload | `TestLiveAuditRetention`, `TestLiveRetentionAndNoViewPause`, `TestScheduleWorkspaceDeletionRetainsAudit`, `TestLiveDataCrossProcessRetentionPurgesQueuedRows` |
| Independent external epoch/key across database restore; lost state refuses; tombstones precede reuse | `TestLiveDataCrossProcessGateway` restore subcase, `TestExternalAuthorityMissingStateRefusesReset`, `TestExternalRevocationMarkSurvivesRestartAndFence`, `TestRevocationCheckpointRollbackRequiresFence`, `TestSecretAuthorityCASAndFailure` |
| Limits: 20 bindings, owner/connection rates and concurrency, global four workers/80 queue, cadence, timeout, fairness/no catch-up | `TestLiveDataCrossProcessSharedQuotas`, `TestAttemptConcurrencyCaps`, `TestAttemptConnectionRateLimit`, `TestSchedulerBoundedBackoffAndCancellation`, `TestScheduleReplicaLeaseAndNoCatchUp`, `TestLiveDataCrossProcessCallDeadline`; `TestAttemptQueueEightySlotsAcrossReplicas`, `TestScheduleQueueWindowIsEighty`, `TestScheduleFairOwnerRotationBeyondQueueWindow` additionally prove the 80-slot boundary and persistent fair owner rotation; not every limit has an independent process mutation proof |
| Dedicated CA renewal Job; service pods never receive CA; renewal preserves identity/epoch | `core/cmd/blerg-core/live_data_renew_job_test.go`, `install/live_data_tls_test.py` (inherited BLG-520) |

## Failure matrix, row by row

| Condition | Proof |
|---|---|
| Timeout/outage/disconnect | `TestLiveDataCrossProcessProviderFailures`, `TestLiveDataCrossProcessCallDeadline`; original good time retained |
| 429/quota | `TestLiveDataCrossProcessProviderFailures`, `TestLiveDataCrossProcessSharedQuotas`, `TestSchedulerProviderDelayCannotBeShortenedByJitter`; browser Rate limited and retry; `TestDatasetMCPFailureCauseChangesEmitWithoutNewRows` proves cause-only and retry-only deltas with unchanged fetch time |
| Missing/null/invalid/nonfinite/schema drift | `TestMapAtomicFailure`, `TestMapNullFilterAndCorrections`, `TestLiveDataCrossProcessProviderFailures`; browser Invalid sample |
| Oversized input/rows/mapping | `TestMapLimitsAndMalformedInput`, `TestMapNormalizedByteLimit`, `TestLiveProviderBoundsWireResponseBeforeParsing`, `TestLiveDataCrossProcessProviderFailures` |
| Tool/read-only/policy/hash loss | `TestLiveDataCrossProcessToolFences`, `TestLiveDataCrossProcessAuthorityLoss` |
| Denied/canceled/stale/expired proposal | `TestLiveDataDecisionReconciliation`, `TestLiveDataApprovalGuards`; pending edit preserves old authority in `TestLiveDataDecisionReconciliation` |
| Credentials/verifier/audit/DB unavailable | `TestLiveDataCrossProcessAuditFailures`, `TestLiveDataCrossProcessSecretAuditFailure`, `TestVerifierFailureGuards`; browser safe failure states |
| Revocation/permission/deletion/approval expiry | `TestLiveDataCrossProcessAuthorityLoss`, `TestLiveDataCrossProcessExpiredApproval`; browser expiry/recovery/revoke |
| No visible viewer | `TestLiveDataCrossProcessNoViewerDoesNotPoll`, `TestLiveDataCrossProcessDemandAndLeaseFences`, `TestLiveDataCrossProcessRetentionPurgesQueuedRows`; hook visibility tests |
| Restart/duplicate dispatch | `TestLiveDataCrossProcessCrashKeepsAttemptConsumed`, `TestLiveDataCrossProcessGatewayOutageAndRestart`, `TestAttemptSingleUseAndEpoch` |
| Owner isolation/shared dashboard | `TestLiveDataCrossProcessGateway` owner/other/admin socket subcase, `TestDatasetMCPOwnerOnlyAndDeliveryRevocation`; rows withheld, generic Access removed label; owner-name display dropped by owner decision on 2026-11-05 |
| Sequence gap/reconnect/reordered delivery | `datasets.test.ts`: “recovers MCP gaps and reconnects with a fresh subscription and rejects obsolete frames”; real Runner/browser recovery in `MCPDatasetCards.browser.test.tsx`; document bytes unchanged, scalar nodes retained |
| Unsupported renderer | `useDashboardDataset.test.tsx`: “creates no MCP demand for unsupported … cards”; setup refusal in `TestLiveDataUnsupportedCardRefusedBeforeProposal`, `TestBoundLiveAskRelay`, and `TestNativeMCPCardSetup`: only native stat/gauge without sparklines. Legacy unsupported cards create no demand and name supported types. Authorized static snapshots dropped by owner decision on 2026-11-05 |
| Socket outage/TTL/export failure | `datasets.test.ts` disconnected deadline, `useDashboardDataset.test.tsx` offscreen expiry, browser disconnected/expiry/recovery; `ArtifactViewer.visualDownload.test.tsx` isolates generic PNG export errors. MCP dashboard payload export is not exposed or proven |
| Market closed/halted plus failure | `DashboardBlock.mcp.test.tsx` closed/halted/unknown entitlement; browser closed plus each failure. Optional mapped `market_status` accepts open/closed/halted; optional `entitlement` accepts delayed/real-time. Unknown values remain unknown. These are mapped provider claims, not a calendar inference |
| Forged/replayed/expired capability | `TestLiveDataCrossProcessSignedCapabilityVariants`, `TestLiveDataCrossProcessGateway`, `TestLiveDataCrossProcessExpiredApproval` |
| Gateway down | `TestLiveDataCrossProcessGatewayOutageAndRestart`; browser Gateway unavailable, retained time and retry |
| Core down mid-renewal | `TestLiveDataCrossProcessCoreOutage`; browser Authority unavailable; no actual Core outage is induced in the browser fixture |
| Owner disabled/deleted | `TestLiveDataCrossProcessAuthorityLoss`; existing disabled-owner lifecycle, no new deletion path |
| OAuth needs sign-in | `TestLiveDataCrossProcessAuthorityLoss`, `TestLiveDataCrossProcessProviderFailures`; hook Connection needs sign-in label |
| Rotated/compromised key | `TestKeyRotationAndCompromise`, `TestLiveDataCrossProcessPersistedKeyReplacement`, `TestLiveDataCrossProcessCompromisedKeyFence` |
| Core restored | `TestLiveDataCrossProcessGateway` checkpointed restore subcase; external state is never restored with SQL. An uncheckpointed SQL-only crash window requires the mandatory operator fence before restored dispatch |
| Clock skew | `TestLiveDataCrossProcessCapabilityAndClock`, `TestCredentialIsolation`; hook Clock skew label |
| Sign-out everywhere/password reset | `TestLiveDataCrossProcessAuthorityLoss`, `TestLiveDataApprovalSessionFence` |

## Proof limits before enablement

The prior addendum records independent guard-removal proofs (four initial,
subsequent type/signature/clock/audit, and eleven crash/quota/expiry removals).
Those historical statements are not a claim that **every** authorization guard
has its own archived mutation result. This continuation reverts the recovery fix, disables cause/retry emission, and
removes the unsupported-card no-demand guard independently. Each mutation fails
its intended assertion and is restored. The generated per-guard record is now in
the published full-ledger artifact linked in the compact disposition below. Its current
1,816 indexed entries all have observations: 669 assertion-detected removals,
1,117 survivors, 29 runtime/harness outcomes and one timeout. This is not a claim
that every semantic authorization path is proved. Source completeness, redundant
checks and surviving/inconclusive cases still require review before enablement.

MCP dashboard payload exports are explicitly blocked in both viewers. Source
remains the authored document, with binding references instead of collected rows.
`PayloadExportBoundary.test.tsx` exercises stale private SVG and bypassed disabled
controls; `ArtifactViewer.visualDownload.test.tsx` and the real Runner browser
harness prove source-only behavior.

**Owner decision, 2026-11-05:** owner-name private-source display and authorized
static snapshots are dropped, not remaining work. New MCP wiring requires an
explicit native card declaration. Unsupported/missing card specs are refused
before provider listing, proposal persistence or ask creation, with an actionable
reason naming native stat and gauge cards without sparklines. Legacy unsupported
cards remain non-demanding. The wider market/session-axis demo remains outside
the initial scalar scope. Polling remains disabled; independent review and the
complete guard-mutation record still control any future enablement decision.

## Continuation verification (2026-11-05)

Commits before this index: `ac5ed71d6` merges the previous worker branch (main was
already included); `34bc1cf61` fixes stream recovery; `accd6ed11` fixes cause/retry
emission and native-card presentation. All are on the assigned session branch.
No push or production activation was performed.

| Exact focused command (from its workspace) | Result |
|---|---|
| Runner: `go test ./internal/server -run '^(TestDataset\|TestMCPAuthority)' -timeout 5m` using the supplied Go/database environment | PASS, 18.225s. Browser-owned fixture is skipped without its harness variable; no claim of a full server suite |
| Chat: `vitest run src/visual/dashboard.test.ts src/transport/datasets.test.ts src/components/DashboardBlock.test.tsx src/components/DashboardBlock.mcp.test.tsx src/components/useDashboardDataset.test.tsx src/components/ArtifactViewer.visualDownload.test.tsx src/visual/datasets.mcp.test.ts src/visual/liveFile.test.ts` | 77/77 PASS, 8 files, 3.30s |
| Chat after restoring no-demand mutation: `vitest run src/components/useDashboardDataset.test.tsx` | 14/14 PASS, 3.59s |
| Runner frontend: `vitest run --project unit src/lib/chatTransport.datasets.test.ts` | 1/1 PASS, 3.92s |
| Runner frontend: `vitest run --project browser src/components/MCPDatasetCards.browser.test.tsx` using supplied database/Go and workspace-local screenshot directory | 4/4 PASS, 72.98s; phone 390×844, wide 3840×2160, 1024px pane, dark/color-blind themes |
| Runner: `golangci-lint run --config ../.golangci.yml ./internal/server` with supplied Go first on PATH and `GOWORK=off` | PASS, zero issues |
| ESLint on changed chat files and browser harness | PASS |
| Chat typecheck; Runner and Board `tsc --noEmit` | PASS (production builds also typecheck application projects) |
| `npm run build --workspace packages/chat`, `--workspace runner/frontend`, `--workspace board/web` | PASS; existing chunk-size warnings |
| `git diff --check`, `bash scripts/scrub.sh` | PASS |

The initial transport command could not start before `npm ci --ignore-scripts`.
Behavioral red tests then reproduced reused subscription IDs, missing presentation
labels/market facts, whole-document rejection of unsupported MCP cards, and
suppressed cause-only changes. Intentional mutations independently reproduced the
recovery, no-demand and cause/retry failures; all mutations were restored.
The Runner build caught a test-frame union type error, corrected to DatasetSample.
One capture run was refused by Vite because the output directory was outside the
workspace; workspace-local captures passed. Initial Go lint selected the old PATH
Go; selecting the supplied Go resolved that environment failure.

The earlier full process matrix and full pre-merge/browser/performance gates were
not rerun. The negative mutations intentionally fail; they are not green test
runs. Dependency installation reported 16 audit findings (14 moderate, two high);
no dependency remediation or lockfile changes are included here. No unverified
suite is counted as passing.


## Security follow-up verification (2026-11-05)

Polling remains disabled. Main was merged first, then updated through
`8f2c0a93e` in session merge `212c6d0a9`. Verification ran in the assigned
session worktree with that main integrated; these follow-up commits have not
been pushed or merged into the named main branch by this worker.

| Change | Commit | Focused proof |
|---|---|---|
| Human security actions commit during authority-store outage; local grant deny and durable checkpoint retry | `e5250d31c` | `TestRevocationCheckpointOutageCommitsHumanActionsAndRetries`: seven real SQL/service actions; removing pending-ticket refusal fails before retry |
| Dashboard payload export explicitly refused; reference-only original source remains downloadable | `2c8289dc1` | `PayloadExportBoundary.test.tsx`, `ArtifactViewer.visualDownload.test.tsx`, real MCP browser suite; independently removing SVG or PNG handler refusal fails the stale-private-render assertion |
| Shared 80-slot queue and durable owner turn rotation | `17a49ec5a` | `TestAttemptQueueEightySlotsAcrossReplicas`, `TestScheduleQueueWindowIsEighty`, `TestScheduleFairOwnerRotationBeyondQueueWindow` |
| Native card declaration required before new binding approval or ask | `1ea1ea00a` | `TestNativeMCPCardSetup`, `TestLiveDataUnsupportedCardRefusedBeforeProposal`, `TestBoundLiveAskRelay`; stat/gauge only, no sparkline or custom spec |
| Issuer startup completes the checkpoint created by old-epoch grant invalidation | `6347098b3` | `TestLiveDataCrossProcessCompromisedKeyFence`; full cross-process family passes, 197.556s |
| Checkpointed restore fixture explicitly awaits completion | `c05508451` | Core command `^TestLiveData` passes, 2.401s; uncheckpointed restores retain the operator-fence requirement |
| Source-indexed isolated predicate audit and credential/schema negative fixtures | `bbf12d068`, `9668e70d9`, `4595c7b08`, `e45c1e50a`, `6468a10d9` | See `scripts/test-live-data-mutations.md`; generated predicates are a review aid, not a claim of exhaustive authorization coverage |

The whole cross-process family (`^TestLiveDataCrossProcess`, including the real
30-second call deadline) passes in 197.556s after two failed runs exposed
checkpoint completion races. The touched real browser file
`MCPDatasetCards.browser.test.tsx` passes 4/4 cases in 73.98s: phone, narrow pane
in a wide window, wide pane, and colorblind dark. The four touched chat unit files
pass 29/29 in 3.55s. No browser tests were skipped. Go's nonverbose process result
does not enumerate skips; fixture-only child tests intentionally run in their
separate controlled processes. Core and contracts lint pass after retrying the
shared lint lock; chat lint, chat/Runner types, chat production build and source
scrub pass. These are focused regional checks, not the repository-wide gate.

The asynchronous checkpoint intentionally removes the former external-ack wait
from human transactions. It cannot detect a restore that loses a SQL-only ticket
before its external checkpoint. Every database restore must stop dispatch and
advance the independent authority epoch before serving restored state, as
specified in `docs/live-data-transport.md`. Checkpointed-history rollback detection
is an additional automatic refusal, not a substitute for that runbook.


## Final source-indexed follow-up

[Security follow-up and commit manifest](charts-mcp-security-followup.md) records
all session commits, test results and inventory limits. The generated ledger
covers 1,760 Go/SQL entries and 56 client entries. Later targeted proofs supersede
earlier gaps while preserving their history and proof commit. The join tool
checks source hashes and refuses to turn missing observations into passes.

`522065cfe` fixes the first committed render after an expired binding is selected
again; a layout-phase observer reproduced the leak before the fix. `03ad5ddfc`
refuses to retain dashboard renderings in a reusable export cache; a poisoned
renderer and a visual-kind transition reproduced the SVG/PNG export leak before
the fix. These are additional proofs of the expiry and export rules, not static
snapshot support. The owned mapper tests now live with their shared contract.

The final touched browser file passes 4/4 cases, zero skips, in 134.91s after the
last main merge and viewer changes. This run included Vite dependency
reoptimization and 127.37s of real browser test time. Final cross-process JSON results: 22 top-level tests, 79 tests/subtests PASS,
zero failures/skips, 148.005s on the final integrated tree. No enablement or push was performed.

Final restored client region: six files, **67/67 PASS**, zero skips, 3.24s.
Chat/Runner types, chat/Runner/Board production builds, focused Go/chat lint,
source scrub and whitespace checks pass. The unchanged disabled rollout constants
were checked again before committing this evidence.

## Guard audit compact disposition (2026-11-05)

The full 1,816-row ledger is generated, not a tracked source file.
Regenerate it with the Go audit tooling described in `scripts/test-live-data-mutations.md`.

| Outcome | Count |
|---|---:|
| Assertion-detected (`killed`) | 669 |
| Survived | 1,117 |
| Harness or panic | 27 |
| Harness or runtime error | 2 |
| Timeout | 1 |

Every unresolved guard is named below by stable ID, function/kind and location.
**N** means needs a test: the selected suite did not produce an independent assertion
when this guard was disabled; equivalence has not been established. **H** means
needs a test/harness repair: panic/runtime failure is inconclusive. **T** means
needs a bounded fixture: timeout is inconclusive. These are proof gaps, not claims
of exploitable production bypass. No unresolved guard is silently certified equivalent.
Independent semantic completeness review and mandatory restore fencing remain gates.

| Guard | Name and location | View/reason |
|---|---|---|
| `live-2ef7082bf009` | Has — `contracts/identity/principal.go:17` | N |
| `live-e0cebe7e270e` | Revoked — `contracts/identity/revocations.go:37` | N |
| `live-c86d7ec0dd4f` | Revoked — `contracts/identity/revocations.go:40` | N |
| `live-abad0523d344` | Revoked — `contracts/identity/revocations.go:41` | N |
| `live-a242ad93f5fc` | Revoked — `contracts/identity/revocations.go:50` | N |
| `live-64ef1f670dee` | Revoked — `contracts/identity/revocations.go:51` | N |
| `live-c7e98601e08b` | RevokedFor — `contracts/identity/revocations.go:76` | N |
| `live-6d96aa9b9d2b` | RevokedFor — `contracts/identity/revocations.go:77` | N |
| `live-a247d5b9b208` | Verify — `contracts/identity/verify.go:27` | N |
| `live-ae87f4a73335` | Verify — `contracts/identity/verify.go:31` | N |
| `live-1d08a922537a` | Verify — `contracts/identity/verify.go:45` | N |
| `live-29ea1033312c` | Verify — `contracts/identity/verify.go:48` | N |
| `live-f8260447404f` | Verify — `contracts/identity/verify.go:54` | N |
| `live-d9b867dfd81f` | Verify — `contracts/identity/verify.go:61` | N |
| `live-5c55050ef20e` | Verify — `contracts/identity/verify.go:65` | N |
| `live-d5f8495a2b1e` | Canonical — `contracts/livedata/revision.go:51` | N |
| `live-38e3b08ef119` | Canonical — `contracts/livedata/revision.go:59` | N |
| `live-e0bce0b1aac0` | Canonical — `contracts/livedata/revision.go:68` | N |
| `live-7e308aaa5bc4` | SecretArguments — `contracts/livedata/revision.go:79` | N |
| `live-438d9e90e194` | secretValue — `contracts/livedata/revision.go:91` | N |
| `live-a77cec829186` | secretValue — `contracts/livedata/revision.go:103` | N |
| `live-d0845a4cf002` | secretValue — `contracts/livedata/revision.go:117` | N |
| `live-6661b5be5e9b` | secretName — `contracts/livedata/revision.go:142` | N |
| `live-8d5b3d4d8615` | secretName — `contracts/livedata/revision.go:142` | N |
| `live-3f73491ca9f7` | AuthenticatedRunner — `contracts/livedata/runner_identity.go:13` | H |
| `live-9a46d24b8221` | AuthenticatedRunner — `contracts/livedata/runner_identity.go:17` | H |
| `live-217df787a85a` | ValidateToolArguments — `contracts/livedata/schema.go:22` | N |
| `live-6d5fa1c47a38` | ValidateToolArguments — `contracts/livedata/schema.go:38` | N |
| `live-04d0387266da` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-55d82fc68a4b` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-5a51adc0fc89` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-84d84bd84929` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-17e5326fa198` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-685558f8cf40` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-5e7e4ebf7f43` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-3e0e1841d189` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-635c99e5a126` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-e652973e22b4` | validateSchema — `contracts/livedata/schema.go:57` | N |
| `live-1529e6533c3e` | validateSchema — `contracts/livedata/schema.go:63` | N |
| `live-effbe943928a` | validateSchema — `contracts/livedata/schema.go:68` | N |
| `live-37842e8ff7cb` | validateSchema — `contracts/livedata/schema.go:75` | N |
| `live-2df271972dd1` | validateSchema — `contracts/livedata/schema.go:123` | N |
| `live-8687ebaa4ba3` | validateSchema — `contracts/livedata/schema.go:126` | N |
| `live-c29f2dcfed53` | validateSchema — `contracts/livedata/schema.go:134` | N |
| `live-ca460258a889` | validateSchema — `contracts/livedata/schema.go:140` | N |
| `live-189732730fff` | validateSchema — `contracts/livedata/schema.go:169` | N |
| `live-24f04425c946` | validateSchema — `contracts/livedata/schema.go:172` | N |
| `live-34a56c10accf` | schemaEligible — `contracts/livedata/schema.go:217` | N |
| `live-f0e3d4c5fdbc` | schemaEligible — `contracts/livedata/schema.go:221` | N |
| `live-d32d5e870c2f` | schemaEligible — `contracts/livedata/schema.go:221` | N |
| `live-4699cd7ffbcb` | schemaEligible — `contracts/livedata/schema.go:221` | N |
| `live-05473fe7ac16` | schemaEligible — `contracts/livedata/schema.go:228` | N |
| `live-8a176450f87c` | schemaEligible — `contracts/livedata/schema.go:228` | N |
| `live-89f4155ac1f8` | schemaEligible — `contracts/livedata/schema.go:232` | N |
| `live-c3645cbfd8ea` | schemaEligible — `contracts/livedata/schema.go:237` | N |
| `live-a873443ed3e7` | schemaEligible — `contracts/livedata/schema.go:257` | N |
| `live-70da90811de9` | schemaEligible — `contracts/livedata/schema.go:258` | N |
| `live-38de8992f388` | schemaEligible — `contracts/livedata/schema.go:261` | N |
| `live-976fabd45363` | schemaEligible — `contracts/livedata/schema.go:266` | N |
| `live-740a8925452b` | schemaEligible — `contracts/livedata/schema.go:273` | N |
| `live-d75366b35462` | schemaEligible — `contracts/livedata/schema.go:273` | N |
| `live-068c6ffe5dd2` | schemaEligible — `contracts/livedata/schema.go:273` | N |
| `live-c9d5d17e88cc` | schemaEligible — `contracts/livedata/schema.go:273` | N |
| `live-f7a2683f274a` | Verify — `contracts/livedata/token.go:107` | N |
| `live-2cca12de3f73` | Verify — `contracts/livedata/token.go:111` | N |
| `live-659a216ea861` | Verify — `contracts/livedata/token.go:115` | N |
| `live-093b25324285` | Verify — `contracts/livedata/token.go:123` | H |
| `live-3751e46a918a` | Verify — `contracts/livedata/token.go:131` | N |
| `live-50089b3c05b4` | strict — `contracts/livedata/token.go:158` | N |
| `live-012ce7580def` | strict — `contracts/livedata/token.go:161` | N |
| `live-943cfd1ed1e8` | request — `contracts/livedata/transport.go:44` | N |
| `live-aedc3877f513` | request — `contracts/livedata/transport.go:48` | H |
| `live-da748bfdbd48` | request — `contracts/livedata/transport.go:52` | N |
| `live-76f221d23827` | request — `contracts/livedata/transport.go:56` | N |
| `live-57c34cf2aa84` | request — `contracts/livedata/transport.go:63` | N |
| `live-a7ed0b7a15b7` | request — `contracts/livedata/transport.go:64` | N |
| `live-f19a1e55248b` | request — `contracts/livedata/transport.go:70` | N |
| `live-b6b915785d48` | request — `contracts/livedata/transport.go:77` | N |
| `live-268042c0d7e1` | request — `contracts/livedata/transport.go:80` | N |
| `live-a1955c8a46ad` | Mint — `contracts/livedata/transport.go:91` | N |
| `live-0dd56d9fdecc` | PublicKeys — `contracts/livedata/transport.go:102` | N |
| `live-c293c376a773` | Invalidate — `contracts/livedata/transport.go:117` | N |
| `live-880d6cded96d` | Map — `contracts/livemcp/mapper.go:78` | N |
| `live-f7760cebb273` | Map — `contracts/livemcp/mapper.go:82` | N |
| `live-bc598498bc9d` | Map — `contracts/livemcp/mapper.go:97` | N |
| `live-f584063aa8ac` | Map — `contracts/livemcp/mapper.go:101` | H |
| `live-1cc2fcdcd026` | Map — `contracts/livemcp/mapper.go:112` | N |
| `live-d9e256f80a1c` | Map — `contracts/livemcp/mapper.go:117` | N |
| `live-90253122f91b` | prepare — `contracts/livemcp/mapper.go:185` | N |
| `live-74f265a0dd0e` | prepare — `contracts/livemcp/mapper.go:196` | N |
| `live-bf21fd01b320` | prepare — `contracts/livemcp/mapper.go:202` | N |
| `live-9fa1a148d6eb` | pointer — `contracts/livemcp/mapper.go:234` | N |
| `live-9ee7405ed92f` | lookup — `contracts/livemcp/mapper.go:268` | N |
| `live-63147016256b` | lookup — `contracts/livemcp/mapper.go:272` | N |
| `live-daa5c9f6d074` | convert — `contracts/livemcp/mapper.go:301` | N |
| `live-49a672b688a1` | convert — `contracts/livemcp/mapper.go:316` | N |
| `live-cf4483f42b38` | convert — `contracts/livemcp/mapper.go:323` | N |
| `live-8a0a728c07eb` | convert — `contracts/livemcp/mapper.go:333` | N |
| `live-e121bc9373a4` | convert — `contracts/livemcp/mapper.go:336` | N |
| `live-b9be523de048` | convert — `contracts/livemcp/mapper.go:337` | N |
| `live-031776dce4ed` | convert — `contracts/livemcp/mapper.go:342` | N |
| `live-8458809ce43f` | decode — `contracts/livemcp/mapper.go:361` | N |
| `live-029e46d86ba3` | decode — `contracts/livemcp/mapper.go:368` | N |
| `live-c76730add3a9` | decodeValue — `contracts/livemcp/mapper.go:383` | N |
| `live-1a06f4fe28bb` | decodeValue — `contracts/livemcp/mapper.go:395` | N |
| `live-37b72d13b1c8` | decodeValue — `contracts/livemcp/mapper.go:399` | N |
| `live-6e53d9c92870` | decodeValue — `contracts/livemcp/mapper.go:406` | N |
| `live-56d0e2d5f389` | decodeValue — `contracts/livemcp/mapper.go:420` | T |
| `live-9d54deab6680` | decodeValue — `contracts/livemcp/mapper.go:426` | N |
| `live-5338f65315cf` | logFailure — `contracts/livemcp/mapper.go:435` | N |
| `live-1c753eb1e10d` | logFailure — `contracts/livemcp/mapper.go:441` | N |
| `live-3e461f899f77` | logFailure — `contracts/livemcp/mapper.go:443` | N |
| `live-e1ceba4c24b3` | CanonicalObject — `contracts/livemcp/mapper.go:453` | N |
| `live-e1c57682de9c` | CanonicalObject — `contracts/livemcp/mapper.go:462` | N |
| `live-cb4750eae9d8` | Available — `contracts/livetls/availability.go:18` | H |
| `live-0fd823cb4cda` | Available — `contracts/livetls/availability.go:31` | N |
| `live-274278986cde` | Available — `contracts/livetls/availability.go:37` | H |
| `live-f783a6ac2199` | Listen — `contracts/livetls/listener.go:22` | N |
| `live-822a84e57dcd` | Listen — `contracts/livetls/listener.go:27` | N |
| `live-602fb869a05b` | PeerURL — `contracts/livetls/listener.go:45` | N |
| `live-a5f286ac9529` | ProvisionAt — `contracts/livetls/provision.go:26` | N |
| `live-2160a22cec01` | ProvisionAt — `contracts/livetls/provision.go:38` | N |
| `live-029341538067` | ProvisionAt — `contracts/livetls/provision.go:42` | N |
| `live-62d7702f10bf` | ProvisionAt — `contracts/livetls/provision.go:47` | N |
| `live-5390243738af` | ProvisionAt — `contracts/livetls/provision.go:53` | N |
| `live-0591d295928c` | ProvisionAt — `contracts/livetls/provision.go:58` | N |
| `live-a738279054fb` | ProvisionAt — `contracts/livetls/provision.go:62` | N |
| `live-ef4b1d73181f` | ProvisionAt — `contracts/livetls/provision.go:66` | N |
| `live-a6c525e3d432` | ProvisionAt — `contracts/livetls/provision.go:84` | N |
| `live-89e79c403231` | ProvisionAt — `contracts/livetls/provision.go:88` | N |
| `live-6d1b1f2982a8` | ProvisionAt — `contracts/livetls/provision.go:92` | N |
| `live-cddc24f1e159` | ProvisionAt — `contracts/livetls/provision.go:97` | N |
| `live-00fda08bc127` | ProvisionAt — `contracts/livetls/provision.go:101` | N |
| `live-0a381de2fda0` | ProvisionAt — `contracts/livetls/provision.go:105` | N |
| `live-a2c20230ab4b` | ProvisionAt — `contracts/livetls/provision.go:110` | N |
| `live-5e98eaae37ea` | ProvisionAt — `contracts/livetls/provision.go:120` | N |
| `live-6a0978c188b5` | ProvisionAt — `contracts/livetls/provision.go:124` | N |
| `live-ef8b9bbc51b5` | ProvisionAt — `contracts/livetls/provision.go:129` | N |
| `live-43946eb4b01d` | ProvisionAt — `contracts/livetls/provision.go:133` | N |
| `live-fab41e09b8c2` | ProvisionAt — `contracts/livetls/provision.go:138` | N |
| `live-a2136f5b3acb` | write — `contracts/livetls/provision.go:151` | N |
| `live-d981c2adf917` | write — `contracts/livetls/provision.go:158` | N |
| `live-25a42fe43690` | write — `contracts/livetls/provision.go:162` | N |
| `live-f5d93368e5ab` | write — `contracts/livetls/provision.go:165` | N |
| `live-7e6dd14d5ce4` | serial — `contracts/livetls/provision.go:172` | N |
| `live-1fde265f5ba0` | newUUID — `contracts/livetls/provision.go:179` | N |
| `live-c7cd0a172f8c` | caPublicKey — `contracts/livetls/provision.go:189` | N |
| `live-eb77e3a9dacb` | Refused — `contracts/livetls/refusal.go:13` | N |
| `live-838bbd5ef199` | Refused — `contracts/livetls/refusal.go:17` | N |
| `live-0b885cf5cfc0` | Refused — `contracts/livetls/refusal.go:21` | N |
| `live-cda8a650ccf6` | Refused — `contracts/livetls/refusal.go:25` | N |
| `live-b00a2778cb42` | Refused — `contracts/livetls/refusal.go:29` | N |
| `live-9e554376005f` | RenewAtStart — `contracts/livetls/renew.go:14` | N |
| `live-25cbd27031b1` | RenewAtStart — `contracts/livetls/renew.go:17` | N |
| `live-727836898877` | RenewAtStart — `contracts/livetls/renew.go:21` | N |
| `live-f3e3d4e06912` | RenewAtStart — `contracts/livetls/renew.go:37` | N |
| `live-5afb40fa4161` | RenewAtStart — `contracts/livetls/renew.go:40` | N |
| `live-83f65883c550` | RenewAtStart — `contracts/livetls/renew.go:60` | N |
| `live-07e95ee3fdd4` | RunRenew — `contracts/livetls/renew.go:78` | N |
| `live-bbee07f75336` | uri — `contracts/livetls/transport.go:39` | N |
| `live-a7f33f89d465` | loadAt — `contracts/livetls/transport.go:49` | N |
| `live-67a4a6c15495` | loadAt — `contracts/livetls/transport.go:53` | N |
| `live-0d87a7f909c3` | loadAt — `contracts/livetls/transport.go:65` | H |
| `live-08e1709d1b1a` | loadAt — `contracts/livetls/transport.go:69` | N |
| `live-5e571df5afdb` | loadAt — `contracts/livetls/transport.go:77` | N |
| `live-35700eee5806` | uuid — `contracts/livetls/transport.go:91` | N |
| `live-c875345266b4` | uuid — `contracts/livetls/transport.go:96` | N |
| `live-95cb61028249` | uuid — `contracts/livetls/transport.go:99` | N |
| `live-963ad359dbce` | verify — `contracts/livetls/transport.go:106` | N |
| `live-7492a3762539` | ServerTLS — `contracts/livetls/transport.go:121` | H |
| `live-0d99ea62a7f9` | VerifyRequest — `contracts/livetls/transport.go:151` | N |
| `live-9b7b5c5e2dca` | VerifyRequest — `contracts/livetls/transport.go:154` | H |
| `live-71cfd31ddd0e` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-dd3ce204c475` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-085a2dc2eef5` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-a50ce2cc4009` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-627e437bbead` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-87ed53b71766` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-a3e5a4a20111` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-8981ac3456cb` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-66013b5284f3` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-ea6a6d776035` | Handler — `contracts/livetls/transport.go:180` | N |
| `live-826ef6256cf2` | runLiveDataFence — `core/cmd/blerg-core/live_data_fence.go:23` | N |
| `live-9f2405040d2e` | runLiveDataFence — `core/cmd/blerg-core/live_data_fence.go:25` | N |
| `live-8973efc14808` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:22` | N |
| `live-65d09af7f3d5` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:26` | N |
| `live-dc48799fd858` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:32` | N |
| `live-704f20213a79` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:34` | N |
| `live-3a5700ba40b1` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:38` | N |
| `live-1aa548cf2622` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:44` | N |
| `live-c25f7845ed98` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:48` | H |
| `live-4b60374dbf7a` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:54` | N |
| `live-80d2c09a1964` | SQL predicate — `core/cmd/blerg-core/live_data_issuer.go:64` | N |
| `live-c5510fb99d53` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:64` | N |
| `live-9db04af25608` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:69` | N |
| `live-1041192694f5` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:74` | H |
| `live-803787bafaed` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:81` | N |
| `live-8ee313e4310d` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:90` | N |
| `live-13703cfc1874` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:93` | N |
| `live-44c99e3cdc0d` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:96` | N |
| `live-16f1aed9c504` | configureLiveDataIssuer — `core/cmd/blerg-core/live_data_issuer.go:99` | N |
| `live-1c2082843fc8` | liveDataPrivateMux — `core/cmd/blerg-core/live_data_issuer.go:110` | N |
| `live-80969bca2046` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:29` | H |
| `live-277df23e3685` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:35` | N |
| `live-9b38874bd564` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:43` | N |
| `live-5cfbe82a1074` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:47` | N |
| `live-347f40954811` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:51` | N |
| `live-46200a27092c` | liveDataPosture — `core/cmd/blerg-core/live_data_posture.go:63` | N |
| `live-419d2bd3cc00` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:22` | N |
| `live-66815994581d` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:25` | N |
| `live-edf3b4dbe8e3` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:31` | N |
| `live-c33fb2de78f5` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:35` | N |
| `live-2d9d85d1c739` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:43` | N |
| `live-6f1fd45c4679` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:62` | N |
| `live-6011f42bdc7c` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:63` | N |
| `live-3f06d5a233c0` | liveDataProvider — `core/cmd/blerg-core/live_data_provider.go:69` | N |
| `live-f86f7722f4bb` | runLiveLeafJob — `core/cmd/blerg-core/live_data_renew_job.go:30` | N |
| `live-436bade44a29` | request — `core/cmd/blerg-core/live_data_renew_job.go:37` | N |
| `live-17b3ac03a52f` | request — `core/cmd/blerg-core/live_data_renew_job.go:44` | N |
| `live-2dbd0e94092d` | request — `core/cmd/blerg-core/live_data_renew_job.go:49` | N |
| `live-c1f8b56039a2` | request — `core/cmd/blerg-core/live_data_renew_job.go:53` | N |
| `live-d0b1d3f1dedf` | request — `core/cmd/blerg-core/live_data_renew_job.go:61` | N |
| `live-4275515573b1` | request — `core/cmd/blerg-core/live_data_renew_job.go:66` | N |
| `live-b79431780add` | run — `core/cmd/blerg-core/live_data_renew_job.go:81` | N |
| `live-29e4cd3d0dc9` | run — `core/cmd/blerg-core/live_data_renew_job.go:104` | N |
| `live-1e089f94eee6` | run — `core/cmd/blerg-core/live_data_renew_job.go:114` | N |
| `live-bdbe0a199d5a` | run — `core/cmd/blerg-core/live_data_renew_job.go:130` | N |
| `live-e733a1340243` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:27` | N |
| `live-e2b5502ed5ee` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:28` | N |
| `live-ebea9fadf365` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:31` | N |
| `live-be2851d09b53` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:35` | N |
| `live-4b48791a5f25` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:38` | N |
| `live-5ba6add8a2e2` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:41` | N |
| `live-db47ab2d7b3f` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:45` | N |
| `live-382a348c1099` | runLiveDataPKI — `core/cmd/blerg-core/live_data_tls.go:50` | N |
| `live-b34fab85867e` | configureLiveDataTLS — `core/cmd/blerg-core/live_data_tls.go:60` | N |
| `live-57e3e38442f8` | configureLiveDataTLS — `core/cmd/blerg-core/live_data_tls.go:64` | H |
| `live-3e9f5d3df68b` | configureLiveDataTLS — `core/cmd/blerg-core/live_data_tls.go:73` | N |
| `live-9cdf6fe10911` | startLiveDataTLS — `core/cmd/blerg-core/live_data_tls.go:81` | N |
| `live-7d769aee0ed8` | principalLiveness — `core/internal/api/internal_handlers.go:278` | N |
| `live-4a21a1b98a75` | principalLiveness — `core/internal/api/internal_handlers.go:283` | N |
| `live-c9d68a4190aa` | principalLiveness — `core/internal/api/internal_handlers.go:289` | N |
| `live-4ac3ec28d10b` | validInternalKey — `core/internal/api/internal_handlers.go:323` | N |
| `live-5c44f4ea903d` | validInternalKey — `core/internal/api/internal_handlers.go:328` | N |
| `live-7a97c3f2c7df` | validInternalKey — `core/internal/api/internal_handlers.go:331` | N |
| `live-c5541c30b600` | liveComponent — `core/internal/api/live_data.go:45` | N |
| `live-9439e3841770` | liveDecode — `core/internal/api/live_data.go:59` | N |
| `live-23809508bac3` | liveDecode — `core/internal/api/live_data.go:63` | N |
| `live-2b0806b3274c` | liveFail — `core/internal/api/live_data.go:79` | N |
| `live-fef60ea55684` | livePerson — `core/internal/api/live_data.go:99` | N |
| `live-32dd21254280` | livePerson — `core/internal/api/live_data.go:103` | N |
| `live-f1df9e3076be` | livePerson — `core/internal/api/live_data.go:107` | N |
| `live-09bcd13ce683` | livePerson — `core/internal/api/live_data.go:115` | N |
| `live-14547669c1fd` | livePerson — `core/internal/api/live_data.go:119` | N |
| `live-1db8ad6c4d54` | liveHumanTx — `core/internal/api/live_data.go:127` | N |
| `live-08bc7be00b04` | liveHumanTx — `core/internal/api/live_data.go:131` | N |
| `live-a9b566236bff` | liveProposalProof — `core/internal/api/live_data.go:153` | N |
| `live-aab14893b551` | liveProposalProof — `core/internal/api/live_data.go:161` | N |
| `live-0121d00c2949` | handleLiveProposal — `core/internal/api/live_data.go:176` | N |
| `live-9ecc027ae8f9` | handleLiveProposal — `core/internal/api/live_data.go:188` | N |
| `live-ba0d1c39e859` | handleLiveProposal — `core/internal/api/live_data.go:193` | N |
| `live-21d786e52dff` | handleLiveProposal — `core/internal/api/live_data.go:198` | N |
| `live-8a0f8c441525` | SQL predicate — `core/internal/api/live_data.go:203` | N |
| `live-af5fc90d5310` | SQL predicate — `core/internal/api/live_data.go:203` | N |
| `live-57aae776916a` | SQL predicate — `core/internal/api/live_data.go:203` | N |
| `live-97a6fc5c5660` | SQL predicate — `core/internal/api/live_data.go:203` | N |
| `live-afd38882b769` | SQL predicate — `core/internal/api/live_data.go:203` | N |
| `live-6a9f5c567873` | handleLiveProposal — `core/internal/api/live_data.go:204` | N |
| `live-df850183ad92` | handleLiveProposal — `core/internal/api/live_data.go:217` | N |
| `live-941272ae67ae` | handleLiveProposal — `core/internal/api/live_data.go:224` | N |
| `live-5c7c4c77503c` | SQL predicate — `core/internal/api/live_data.go:229` | N |
| `live-f473d4a43eb9` | handleLiveProposalRead — `core/internal/api/live_data.go:247` | N |
| `live-6046a98ce3eb` | handleLiveProposalRead — `core/internal/api/live_data.go:251` | N |
| `live-8f04a6457372` | SQL predicate — `core/internal/api/live_data.go:263` | N |
| `live-601edcad99ff` | SQL predicate — `core/internal/api/live_data.go:263` | N |
| `live-b61edad53054` | readLiveProposal — `core/internal/api/live_data.go:264` | N |
| `live-4d772fe3eb34` | SQL predicate — `core/internal/api/live_data.go:265` | N |
| `live-1f87f33e79c9` | readLiveProposal — `core/internal/api/live_data.go:270` | N |
| `live-323e62490271` | readLiveProposal — `core/internal/api/live_data.go:276` | N |
| `live-842a8784aa53` | readLiveProposal — `core/internal/api/live_data.go:283` | N |
| `live-973ce995432c` | handleLiveApproval — `core/internal/api/live_data.go:313` | N |
| `live-e82d3e45867f` | handleLiveApproval — `core/internal/api/live_data.go:320` | N |
| `live-fa19df5fb193` | handleLiveApproval — `core/internal/api/live_data.go:339` | N |
| `live-0d219d13cf45` | handleLiveApproval — `core/internal/api/live_data.go:345` | N |
| `live-51a190153019` | handleLiveApproval — `core/internal/api/live_data.go:349` | N |
| `live-c0c19735e572` | handleLiveApproval — `core/internal/api/live_data.go:353` | N |
| `live-7463d476fa3b` | handleLiveApproval — `core/internal/api/live_data.go:358` | N |
| `live-546bbf6db282` | SQL predicate — `core/internal/api/live_data.go:370` | N |
| `live-e1c2b40f91fd` | SQL predicate — `core/internal/api/live_data.go:370` | N |
| `live-b08e2fa5120b` | handleLiveApproval — `core/internal/api/live_data.go:371` | N |
| `live-f032d48615f5` | SQL predicate — `core/internal/api/live_data.go:380` | N |
| `live-3b2e03c9c820` | SQL predicate — `core/internal/api/live_data.go:380` | N |
| `live-1fbca7b52586` | handleLiveApproval — `core/internal/api/live_data.go:380` | N |
| `live-b8d2d6016b4d` | SQL predicate — `core/internal/api/live_data.go:381` | N |
| `live-327a92920b3a` | SQL predicate — `core/internal/api/live_data.go:381` | N |
| `live-56b19418ca1f` | handleLiveApproval — `core/internal/api/live_data.go:383` | N |
| `live-2b52a1561bfc` | handleLiveApproval — `core/internal/api/live_data.go:394` | N |
| `live-e7134b735451` | SQL predicate — `core/internal/api/live_data.go:399` | N |
| `live-337678f32022` | SQL predicate — `core/internal/api/live_data.go:399` | N |
| `live-bdebf339f380` | SQL predicate — `core/internal/api/live_data.go:399` | N |
| `live-33ef1757f254` | SQL predicate — `core/internal/api/live_data.go:399` | N |
| `live-3bd0d5054355` | SQL predicate — `core/internal/api/live_data.go:399` | N |
| `live-ae4a24c65b05` | handleLiveApproval — `core/internal/api/live_data.go:400` | N |
| `live-90499addb995` | SQL predicate — `core/internal/api/live_data.go:410` | N |
| `live-8adb1ed5d74e` | handleLiveApproval — `core/internal/api/live_data.go:410` | N |
| `live-dba6640a343b` | SQL predicate — `core/internal/api/live_data.go:420` | N |
| `live-0e0315d69e1c` | SQL predicate — `core/internal/api/live_data.go:420` | N |
| `live-3f89d69c714a` | SQL predicate — `core/internal/api/live_data.go:420` | N |
| `live-0c0e2b0d4c88` | SQL predicate — `core/internal/api/live_data.go:425` | N |
| `live-d984fc3b0df8` | SQL predicate — `core/internal/api/live_data.go:425` | N |
| `live-2fad2ec27791` | handleLiveApproval — `core/internal/api/live_data.go:425` | N |
| `live-b0349d4f0b67` | handleLiveApproval — `core/internal/api/live_data.go:432` | N |
| `live-49e050fb667b` | handleLiveApproval — `core/internal/api/live_data.go:438` | N |
| `live-0c378439f64e` | handleLivePolicy — `core/internal/api/live_data.go:450` | N |
| `live-5ffcf8f4108c` | handleLivePolicy — `core/internal/api/live_data.go:453` | N |
| `live-b09793855cd9` | handleLivePolicy — `core/internal/api/live_data.go:471` | N |
| `live-0ae99b71a3bc` | handleLivePolicy — `core/internal/api/live_data.go:476` | Needs a test: person.Sub selects human-session locking; add a policy-raise versus logout race after preauthentication. |
| `live-9027340b276b` | handleLivePolicy — `core/internal/api/live_data.go:481` | N |
| `live-fc89bf4c9de8` | handleLivePolicy — `core/internal/api/live_data.go:492` | Needs a test: policy allow branch skips expiry/current epoch/generation checks; downstream dispatch checks do not prove the policy transaction boundary. |
| `live-f3320625f162` | handleLivePolicy — `core/internal/api/live_data.go:493` | N |
| `live-fa9ff7dbe60b` | SQL predicate — `core/internal/api/live_data.go:498` | N |
| `live-c177670fe854` | SQL predicate — `core/internal/api/live_data.go:498` | N |
| `live-2a685437af21` | SQL predicate — `core/internal/api/live_data.go:498` | N |
| `live-4f1572224c2f` | SQL predicate — `core/internal/api/live_data.go:498` | N |
| `live-d4de7fadf719` | SQL predicate — `core/internal/api/live_data.go:498` | N |
| `live-fe21c193852d` | handleLivePolicy — `core/internal/api/live_data.go:499` | N |
| `live-43241fcbb59b` | handleLivePolicy — `core/internal/api/live_data.go:505` | N |
| `live-fabb225fd830` | handleLiveSettings — `core/internal/api/live_data.go:516` | N |
| `live-66e3e0c0f0af` | handleLiveSettings — `core/internal/api/live_data.go:520` | N |
| `live-e88054d65c55` | handleLiveSettings — `core/internal/api/live_data.go:535` | N |
| `live-a30cbead03e4` | handleLiveSettings — `core/internal/api/live_data.go:541` | N |
| `live-d3ea012e1dc1` | SQL predicate — `core/internal/api/live_data.go:550` | N |
| `live-bdc8a1002241` | handleLiveSettings — `core/internal/api/live_data.go:550` | N |
| `live-60a6ef69e8ca` | SQL predicate — `core/internal/api/live_data.go:554` | N |
| `live-a30cef66cf4c` | SQL predicate — `core/internal/api/live_data.go:554` | N |
| `live-6b6b5d5f230d` | SQL predicate — `core/internal/api/live_data.go:554` | N |
| `live-8d2bb16638ac` | SQL predicate — `core/internal/api/live_data.go:554` | N |
| `live-96efacb5f888` | SQL predicate — `core/internal/api/live_data.go:554` | N |
| `live-d268ad670a49` | handleLiveSettings — `core/internal/api/live_data.go:555` | N |
| `live-95473af695ed` | handleLiveSettings — `core/internal/api/live_data.go:570` | N |
| `live-82579214d354` | liveFreshTool — `core/internal/api/live_data_listing.go:18` | N |
| `live-20fdd336e4b2` | liveFreshTool — `core/internal/api/live_data_listing.go:22` | N |
| `live-533ae0c2acb2` | liveFreshTool — `core/internal/api/live_data_listing.go:29` | N |
| `live-64e39a192513` | liveFreshTool — `core/internal/api/live_data_listing.go:35` | N |
| `live-782fca16aebb` | liveFreshTool — `core/internal/api/live_data_listing.go:39` | N |
| `live-ea9b09050f58` | liveFreshTool — `core/internal/api/live_data_listing.go:44` | N |
| `live-de570f6e55ec` | liveFreshTool — `core/internal/api/live_data_listing.go:55` | N |
| `live-c7ba79b06fb4` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:14` | N |
| `live-b832d47098ac` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:23` | N |
| `live-5c966b9c2011` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:28` | N |
| `live-220d40ad203e` | SQL predicate — `core/internal/api/live_data_reconcile.go:40` | N |
| `live-8190a8083a28` | SQL predicate — `core/internal/api/live_data_reconcile.go:40` | N |
| `live-75c895f3f378` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:41` | N |
| `live-015d18b6df9d` | SQL predicate — `core/internal/api/live_data_reconcile.go:42` | N |
| `live-76601b6ea3fc` | SQL predicate — `core/internal/api/live_data_reconcile.go:42` | N |
| `live-46aeeee35827` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:46` | N |
| `live-f13988a456ed` | SQL predicate — `core/internal/api/live_data_reconcile.go:50` | N |
| `live-325ce5912af2` | SQL predicate — `core/internal/api/live_data_reconcile.go:50` | N |
| `live-b8782532d39f` | SQL predicate — `core/internal/api/live_data_reconcile.go:50` | N |
| `live-71395987e19d` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:52` | N |
| `live-56c2908c9221` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:57` | N |
| `live-27cc0cea92d0` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:62` | N |
| `live-38d084b2fe88` | handleLiveReconcile — `core/internal/api/live_data_reconcile.go:65` | N |
| `live-dac2440e5c82` | live_data_policy_fence — `core/internal/db/migrations/152_live_data_authority.sql:52` | N |
| `live-6dcb8d7a4253` | live_data_grant_immutable — `core/internal/db/migrations/152_live_data_authority.sql:79` | N |
| `live-d877aa4a75bb` | live_data_host_opt_out — `core/internal/db/migrations/153_live_data_approval.sql:47` | N |
| `live-89146441ec3c` | live_data_configuration_immutable — `core/internal/db/migrations/155_live_data_approved_configuration.sql:11` | N |
| `live-ce3e29ca81f3` | live_data_ask_immutable — `core/internal/db/migrations/156_live_data_registered_asks.sql:5` | N |
| `live-ee169980eea2` | live_data_proposal_immutable — `core/internal/db/migrations/157_live_data_decision_reconciliation.sql:4` | N |
| `live-5464c0709e80` | live_data_checkpoint_revocation — `core/internal/db/migrations/161_live_data_nonblocking_revocation.sql:8` | N |
| `live-2bf51a2d447a` | live_data_checkpoint_revocation — `core/internal/db/migrations/161_live_data_nonblocking_revocation.sql:9` | N |
| `live-af967c04cee4` | AgentTokenLive — `core/internal/identity/agent_tokens.go:417` | N |
| `live-ef19f3e03d58` | SQL predicate — `core/internal/identity/human_session.go:105` | N |
| `live-de1fdfb98836` | accountState — `core/internal/identity/human_session.go:107` | N |
| `live-8276720ec9ba` | HumanSessionDeadline — `core/internal/identity/human_session.go:191` | N |
| `live-eb523a1edde0` | SQL predicate — `core/internal/identity/human_session.go:197` | N |
| `live-1b048c238134` | SQL predicate — `core/internal/identity/human_session.go:197` | N |
| `live-5e7ae516aede` | SQL predicate — `core/internal/identity/human_session.go:197` | N |
| `live-465a487a7284` | SQL predicate — `core/internal/identity/human_session.go:197` | N |
| `live-aa9e16e8aaa0` | SQL predicate — `core/internal/identity/human_session.go:197` | N |
| `live-d3a94aa84840` | HumanSessionDeadline — `core/internal/identity/human_session.go:199` | N |
| `live-949df90e28d5` | HumanSessionDeadline — `core/internal/identity/human_session.go:200` | N |
| `live-7cc69c9fb31d` | LockLiveHumanApprovalTx — `core/internal/identity/live_data_approval.go:12` | N |
| `live-6f2e32392b94` | LockLiveHumanApprovalTx — `core/internal/identity/live_data_approval.go:15` | N |
| `live-7659552423e8` | LockLiveHumanApprovalTx — `core/internal/identity/live_data_approval.go:20` | N |
| `live-d01e18bb49e5` | current — `core/internal/livedata/issuer.go:54` | N |
| `live-19986aff0660` | current — `core/internal/livedata/issuer.go:58` | N |
| `live-be24c61a5acc` | current — `core/internal/livedata/issuer.go:61` | N |
| `live-9d3211f905fa` | current — `core/internal/livedata/issuer.go:66` | N |
| `live-e181fe0a04f0` | current — `core/internal/livedata/issuer.go:74` | N |
| `live-8414f23e88b9` | record — `core/internal/livedata/issuer.go:90` | N |
| `live-f1c080468cc3` | record — `core/internal/livedata/issuer.go:93` | N |
| `live-cda6a9790a57` | record — `core/internal/livedata/issuer.go:97` | N |
| `live-bdcbf894c814` | outcome — `core/internal/livedata/issuer.go:121` | N |
| `live-6e8d313f4899` | PublicKeys — `core/internal/livedata/issuer.go:131` | N |
| `live-d7ca8576c7e7` | Current — `core/internal/livedata/issuer.go:147` | N |
| `live-8efa3a40e84c` | Mint — `core/internal/livedata/issuer.go:163` | N |
| `live-d6d0057b7b27` | Mint — `core/internal/livedata/issuer.go:169` | N |
| `live-89ecfd04bcbc` | Mint — `core/internal/livedata/issuer.go:178` | N |
| `live-771fb1879e58` | Invalidate — `core/internal/livedata/issuer.go:187` | N |
| `live-256398c20ff5` | Invalidate — `core/internal/livedata/issuer.go:190` | N |
| `live-71a9e34b5b1f` | epochCurrent — `core/internal/livedata/issuer.go:206` | H |
| `live-0c60694bff95` | epochCurrent — `core/internal/livedata/issuer.go:209` | N |
| `live-7fd21cbf6e1e` | epochCurrent false result — `core/internal/livedata/issuer.go:213` | N |
| `live-bbd60ae09666` | recordRead — `core/internal/livedata/issuer.go:231` | H |
| `live-a6212d7f19b5` | recordRead — `core/internal/livedata/issuer.go:234` | N |
| `live-d4a1679ce08f` | LoadKeyring — `core/internal/livedata/keys.go:44` | N |
| `live-54ae62b45a8e` | loadPrivate — `core/internal/livedata/keys.go:53` | N |
| `live-9f2a2f0b0ccb` | loadPrivate — `core/internal/livedata/keys.go:62` | N |
| `live-21044f9aa989` | loadPrivate — `core/internal/livedata/keys.go:66` | N |
| `live-51ed89dbc585` | loadPrivate — `core/internal/livedata/keys.go:70` | N |
| `live-38ccc7538ae6` | loadPrivate — `core/internal/livedata/keys.go:74` | N |
| `live-a34f4e397ea7` | Rotate — `core/internal/livedata/keys.go:86` | N |
| `live-c1b6b646d110` | Rotate — `core/internal/livedata/keys.go:94` | N |
| `live-5719fc1d978a` | Rotate — `core/internal/livedata/keys.go:101` | N |
| `live-8a8c47596520` | Rotate — `core/internal/livedata/keys.go:106` | N |
| `live-cefe5bbcdd0a` | sign — `core/internal/livedata/keys.go:134` | N |
| `live-ceadb0234780` | keyFailure — `core/internal/livedata/keys.go:144` | N |
| `live-a7e7a93bc8a7` | Current — `core/internal/livedata/postgres.go:17` | N |
| `live-1572920b36d8` | SQL predicate — `core/internal/livedata/postgres.go:22` | N |
| `live-b2cbc5486fe1` | SQL predicate — `core/internal/livedata/postgres.go:23` | N |
| `live-ef9a66634c05` | SQL predicate — `core/internal/livedata/postgres.go:23` | N |
| `live-40412ccd88cc` | SQL predicate — `core/internal/livedata/postgres.go:24` | N |
| `live-109e756af467` | SQL predicate — `core/internal/livedata/postgres.go:25` | N |
| `live-eb4eff0623d9` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-401c4f519984` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-68d50c9507d5` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-bb9b25ad8e2e` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-f867d7ba8018` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-2b02b095f011` | SQL predicate — `core/internal/livedata/postgres.go:26` | N |
| `live-1b431f188db0` | SQL predicate — `core/internal/livedata/postgres.go:27` | N |
| `live-1bf89cd05253` | SQL predicate — `core/internal/livedata/postgres.go:27` | N |
| `live-39ff33b77b15` | SQL predicate — `core/internal/livedata/postgres.go:27` | N |
| `live-81f6313e8183` | SQL predicate — `core/internal/livedata/postgres.go:27` | N |
| `live-1ca4283122d6` | SQL predicate — `core/internal/livedata/postgres.go:28` | N |
| `live-79d60dea0b0e` | SQL predicate — `core/internal/livedata/postgres.go:28` | N |
| `live-c08371920bff` | SQL predicate — `core/internal/livedata/postgres.go:29` | N |
| `live-d65da515c1ba` | SQL predicate — `core/internal/livedata/postgres.go:29` | N |
| `live-62cc556f4c83` | SQL predicate — `core/internal/livedata/postgres.go:29` | N |
| `live-a680fb25df9c` | SQL predicate — `core/internal/livedata/postgres.go:30` | N |
| `live-fa6f5a711afb` | SQL predicate — `core/internal/livedata/postgres.go:31` | N |
| `live-05b88d38726d` | SQL predicate — `core/internal/livedata/postgres.go:31` | N |
| `live-e28e31bb2fa2` | Current — `core/internal/livedata/postgres.go:32` | N |
| `live-2c7a48046881` | Current — `core/internal/livedata/postgres.go:35` | N |
| `live-7525830ee0fc` | Current — `core/internal/livedata/postgres.go:41` | N |
| `live-48020529a4e8` | Audit — `core/internal/livedata/postgres.go:48` | N |
| `live-72b0c0483319` | Audit — `core/internal/livedata/postgres.go:54` | N |
| `live-531abf88e266` | Audit — `core/internal/livedata/postgres.go:57` | N |
| `live-a34d2d5862db` | Audit — `core/internal/livedata/postgres.go:61` | N |
| `live-196ab6e86f3b` | Audit — `core/internal/livedata/postgres.go:61` | N |
| `live-3ec14148b2f6` | Audit — `core/internal/livedata/postgres.go:61` | N |
| `live-4113d7ab556c` | Audit — `core/internal/livedata/postgres.go:66` | N |
| `live-c84a73995a20` | Audit — `core/internal/livedata/postgres.go:69` | N |
| `live-94cd4fa94eeb` | Audit — `core/internal/livedata/postgres.go:72` | N |
| `live-dd3801d869ca` | Audit — `core/internal/livedata/postgres.go:75` | N |
| `live-528288bf658d` | SQL predicate — `core/internal/livedata/postgres.go:79` | N |
| `live-c0e8efd240f7` | SQL predicate — `core/internal/livedata/postgres.go:79` | N |
| `live-0a677d2876cf` | Revoke — `core/internal/livedata/postgres.go:86` | N |
| `live-b988a42427f5` | SQL predicate — `core/internal/livedata/postgres.go:89` | N |
| `live-f938148e0c6a` | SQL predicate — `core/internal/livedata/postgres.go:89` | N |
| `live-0036cb8669ca` | SQL predicate — `core/internal/livedata/postgres.go:89` | N |
| `live-921e14355094` | Revoke — `core/internal/livedata/postgres.go:90` | N |
| `live-55859e2bbaea` | auditAttempt — `core/internal/livedata/postgres.go:97` | N |
| `live-15f7b233559a` | Maintain — `core/internal/livedata/retention.go:12` | N |
| `live-c019edf578b8` | Maintain — `core/internal/livedata/retention.go:18` | N |
| `live-eac5dc78e1a3` | SQL predicate — `core/internal/livedata/retention.go:23` | N |
| `live-93524113a0e2` | SQL predicate — `core/internal/livedata/retention.go:23` | N |
| `live-44c4af4dee7c` | SQL predicate — `core/internal/livedata/retention.go:23` | N |
| `live-7796de43cb6e` | SQL predicate — `core/internal/livedata/retention.go:23` | N |
| `live-5a69ddabbe35` | SQL predicate — `core/internal/livedata/retention.go:23` | N |
| `live-7796c240a8f6` | Maintain — `core/internal/livedata/retention.go:24` | N |
| `live-8cd24d60e573` | Start — `core/internal/livedata/revocation_checkpoint.go:24` | N |
| `live-1ae8dd474b9b` | Start — `core/internal/livedata/revocation_checkpoint.go:28` | N |
| `live-db564b16ae7f` | SQL predicate — `core/internal/livedata/revocation_checkpoint.go:33` | N |
| `live-3f3b947b7596` | Start — `core/internal/livedata/revocation_checkpoint.go:33` | N |
| `live-d5e1cae04360` | Start — `core/internal/livedata/revocation_checkpoint.go:38` | N |
| `live-43fa0b5dfb48` | Start — `core/internal/livedata/revocation_checkpoint.go:48` | N |
| `live-50432a195602` | Start — `core/internal/livedata/revocation_checkpoint.go:51` | N |
| `live-4b720811acbb` | SQL predicate — `core/internal/livedata/revocation_checkpoint.go:54` | N |
| `live-82ab8ffdedfd` | Start — `core/internal/livedata/revocation_checkpoint.go:54` | N |
| `live-5fb6cbc87aab` | Start — `core/internal/livedata/revocation_checkpoint.go:59` | N |
| `live-a9bddafa3a09` | Sync — `core/internal/livedata/revocation_checkpoint.go:66` | N |
| `live-6ec96672f114` | Sync — `core/internal/livedata/revocation_checkpoint.go:69` | N |
| `live-6cf699698e15` | Sync — `core/internal/livedata/revocation_checkpoint.go:76` | N |
| `live-2d545e8de98c` | Check — `core/internal/livedata/revocation_checkpoint.go:84` | N |
| `live-2e58c7cdb31d` | SQL predicate — `core/internal/livedata/revocation_checkpoint.go:91` | N |
| `live-0821afdcfee5` | RevocationMark — `core/internal/livedata/revocation_state.go:22` | N |
| `live-afa34ee0e3b6` | RevocationMark — `core/internal/livedata/revocation_state.go:26` | N |
| `live-7750835acc39` | RevocationMark — `core/internal/livedata/revocation_state.go:29` | N |
| `live-8e3e0e111ce2` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:36` | N |
| `live-d944f979061a` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:40` | N |
| `live-5cce511293e5` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:45` | N |
| `live-f3f109a790fc` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:54` | N |
| `live-5bd4e0a7760a` | RevocationMark — `core/internal/livedata/revocation_state.go:68` | N |
| `live-cd5d583d412b` | RevocationMark — `core/internal/livedata/revocation_state.go:72` | N |
| `live-485f4dbcb434` | RevocationMark — `core/internal/livedata/revocation_state.go:79` | N |
| `live-46d2a5838924` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:86` | N |
| `live-19dbe1f354dd` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:90` | N |
| `live-fb32bedef0f1` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:97` | N |
| `live-bc9bf7ae9ce5` | AdvanceRevocations — `core/internal/livedata/revocation_state.go:106` | N |
| `live-4e327d170f1b` | InClusterAuthority — `core/internal/livedata/secret_state.go:49` | N |
| `live-03d265959ba7` | InClusterAuthority — `core/internal/livedata/secret_state.go:53` | N |
| `live-d32fc0c3cfbd` | InClusterAuthority — `core/internal/livedata/secret_state.go:58` | N |
| `live-1e2a654adee3` | request — `core/internal/livedata/secret_state.go:65` | N |
| `live-f98a6bc5193c` | request — `core/internal/livedata/secret_state.go:74` | N |
| `live-f60d2d0312c1` | request — `core/internal/livedata/secret_state.go:79` | N |
| `live-71e958c37177` | request — `core/internal/livedata/secret_state.go:83` | N |
| `live-118480eb68c2` | request — `core/internal/livedata/secret_state.go:91` | N |
| `live-2db6631aae80` | request — `core/internal/livedata/secret_state.go:95` | N |
| `live-e3a45884f488` | request — `core/internal/livedata/secret_state.go:99` | N |
| `live-d3e377fd6d17` | start — `core/internal/livedata/secret_state.go:120` | N |
| `live-7d39a4ed70dd` | start — `core/internal/livedata/secret_state.go:135` | N |
| `live-fedf19d04a82` | start — `core/internal/livedata/secret_state.go:139` | N |
| `live-8293ba857251` | start — `core/internal/livedata/secret_state.go:143` | N |
| `live-5c5e41f3ea59` | start — `core/internal/livedata/secret_state.go:148` | N |
| `live-4989090384fc` | start — `core/internal/livedata/secret_state.go:152` | H |
| `live-20d104aac74b` | Epoch — `core/internal/livedata/secret_state.go:166` | N |
| `live-abeaa711a2f7` | startAuthority — `core/internal/livedata/state.go:44` | N |
| `live-8f7ef640263f` | startAuthority — `core/internal/livedata/state.go:53` | N |
| `live-06101e6393a5` | startAuthority — `core/internal/livedata/state.go:57` | N |
| `live-5e2e8e1c7aa7` | startAuthority — `core/internal/livedata/state.go:70` | N |
| `live-b0cd96bd1ff4` | startAuthority — `core/internal/livedata/state.go:74` | N |
| `live-cac9db8183af` | startAuthority — `core/internal/livedata/state.go:78` | N |
| `live-d35d5e967783` | startAuthority — `core/internal/livedata/state.go:82` | N |
| `live-641d885e07b8` | readAuthority — `core/internal/livedata/state.go:90` | N |
| `live-a5798bc3cc29` | readAuthority — `core/internal/livedata/state.go:100` | N |
| `live-e1ea01c9378c` | readAuthority — `core/internal/livedata/state.go:104` | N |
| `live-a48e2389d663` | readAuthority — `core/internal/livedata/state.go:114` | N |
| `live-2af1ded795fe` | Epoch — `core/internal/livedata/state.go:120` | N |
| `live-43c8dd68903d` | Epoch — `core/internal/livedata/state.go:124` | N |
| `live-4aad3bf6e026` | validAuthority — `core/internal/livedata/state.go:135` | N |
| `live-dd8379350f0f` | writeAuthority — `core/internal/livedata/state.go:144` | N |
| `live-21e2e34a494f` | writeAuthority — `core/internal/livedata/state.go:149` | N |
| `live-52d5bb9612bd` | writeAuthority — `core/internal/livedata/state.go:153` | N |
| `live-650b7353abe0` | writeAuthority — `core/internal/livedata/state.go:156` | N |
| `live-9fcf44036a1e` | writeAuthority — `core/internal/livedata/state.go:159` | N |
| `live-616ba3bd44c0` | writeAuthority — `core/internal/livedata/state.go:162` | N |
| `live-f948a944927e` | writeAuthority — `core/internal/livedata/state.go:169` | N |
| `live-0c0fd83b0f71` | writeAuthority — `core/internal/livedata/state.go:173` | N |
| `live-1bfc999f2a0e` | handler — `core/internal/livedata/transport.go:39` | N |
| `live-2106528f140e` | handler — `core/internal/livedata/transport.go:44` | N |
| `live-021a37d32853` | handler — `core/internal/livedata/transport.go:50` | N |
| `live-d72a4b537d32` | handler — `core/internal/livedata/transport.go:53` | N |
| `live-c7b9fb965146` | handler — `core/internal/livedata/transport.go:83` | N |
| `live-682d561aadb4` | handler — `core/internal/livedata/transport.go:88` | N |
| `live-780e312649bc` | handler — `core/internal/livedata/transport.go:92` | N |
| `live-12a34eb7ab03` | handler — `core/internal/livedata/transport.go:98` | N |
| `live-a2ea5507b127` | handler — `core/internal/livedata/transport.go:108` | N |
| `live-5124c4df41fa` | handler — `core/internal/livedata/transport.go:112` | N |
| `live-d883ddbb5a94` | handler — `core/internal/livedata/transport.go:117` | N |
| `live-035b85222d94` | handler — `core/internal/livedata/transport.go:123` | N |
| `live-64eb3b1cbbd3` | handler — `core/internal/livedata/transport.go:130` | N |
| `live-51f262b39b8f` | handler — `core/internal/livedata/transport.go:135` | N |
| `live-ceb9817ba484` | handler — `core/internal/livedata/transport.go:154` | N |
| `live-8c4e916817af` | decodeService — `core/internal/livedata/transport.go:178` | N |
| `live-88939141659c` | FetchSecret — `core/internal/mcpconn/service.go:674` | N |
| `live-8fe24ce55160` | FetchSecret — `core/internal/mcpconn/service.go:687` | N |
| `live-084956af7521` | FetchSecret — `core/internal/mcpconn/service.go:690` | N |
| `live-0efa0a5f552e` | FetchSecret — `core/internal/mcpconn/service.go:704` | N |
| `live-68ca5f3c019b` | FetchSecret — `core/internal/mcpconn/service.go:707` | N |
| `live-f0b5dfe43c43` | FetchSecret — `core/internal/mcpconn/service.go:715` | N |
| `live-d0ec2cbde2f6` | FetchSecret — `core/internal/mcpconn/service.go:719` | N |
| `web-d81457a09e38` | predicate — `packages/chat/src/components/ArtifactViewer.tsx:362` | Equivalent mutant for dashboard export: dashboard payload controls are absent; the handler check is defense in depth. |
| `web-3d88a87b23f9` | predicate — `packages/chat/src/components/ArtifactViewer.tsx:368` | Equivalent mutant for dashboard export: dashboard PNG controls are absent; retain the redundant handler refusal. |
| `web-89105c472960` | predicate — `packages/chat/src/components/useDashboardDataset.ts:18` | Needs a test/harness repair: removal throws TypeError; require an explicit no-subscription/no-crash assertion for missing element or transport. |
| `web-f7b24e3e3d9d` | predicate — `packages/chat/src/components/useDashboardDataset.ts:26` | Equivalent for payload authorization: this branch reports expiry; row refusal and render deadline still apply. Diagnostic behavior is unproved. |
| `web-792bbb4df5bb` | predicate — `packages/chat/src/components/useDashboardDataset.ts:50` | Needs a test/harness repair: removal yields a runtime failure; missing sample/access_removed short circuit needs an independent assertion. |
| `web-925d776e1eac` | predicate — `packages/chat/src/components/useDashboardDataset.ts:54` | Equivalent for payload authorization: reportSession only sends status; it does not grant access or restore rows. |
| `web-34a5e4275258` | predicate — `packages/chat/src/components/useDashboardDataset.ts:57` | Needs a test: zero-delay purge and render refusal mask the removed immediate-expiry branch; immediate retention cleanup is unproved. |
| `web-66ac4d066e6d` | predicate — `packages/chat/src/transport/datasets.ts:22` | Equivalent currently: clear is called only with disconnected; its access_removed arm is unreachable. |
| `web-de6c589f4783` | retention_purge — `packages/chat/src/transport/datasets.ts:22` | Equivalent currently: this purge is in the same unreachable clear(access_removed) arm; incoming access_removed has separate enforcement. |
| `web-9fc8b744d4ed` | recovery sequence reset — `packages/chat/src/transport/datasets.ts:27` | Needs a test: new subscription/epoch and mandatory snapshot may mask sequence reset; equivalence across all recovery orderings is unproved. |
| `web-df391a5744fd` | retention_purge — `packages/chat/src/transport/datasets.ts:90` | Needs a test: retired subscription latch prevents delivery, but immediate map erasure is not asserted. |
| `live-7482e538974c` | begin — `runner/internal/livepoll/attempts.go:23` | N |
| `live-be101bbcc2a7` | begin — `runner/internal/livepoll/attempts.go:27` | N |
| `live-c3b08de178c3` | begin — `runner/internal/livepoll/attempts.go:30` | N |
| `live-a2c872933a28` | ObserveEpoch — `runner/internal/livepoll/attempts.go:38` | N |
| `live-5b82ff553625` | SQL predicate — `runner/internal/livepoll/attempts.go:42` | N |
| `live-fcb91188ae92` | SQL predicate — `runner/internal/livepoll/attempts.go:42` | N |
| `live-ad5104bf6841` | ObserveEpoch — `runner/internal/livepoll/attempts.go:44` | N |
| `live-e9888d93ee23` | ObserveEpoch — `runner/internal/livepoll/attempts.go:50` | N |
| `live-c616cd72e67a` | SQL predicate — `runner/internal/livepoll/attempts.go:54` | N |
| `live-b24a9c2a8c01` | ObserveEpoch — `runner/internal/livepoll/attempts.go:55` | N |
| `live-3f6ba8bd2665` | ObserveEpoch — `runner/internal/livepoll/attempts.go:58` | N |
| `live-3e37aeb966c6` | Create — `runner/internal/livepoll/attempts.go:66` | N |
| `live-c5c2e75cedc3` | Create — `runner/internal/livepoll/attempts.go:70` | N |
| `live-cafc0fe8d036` | Create — `runner/internal/livepoll/attempts.go:75` | N |
| `live-85d923758b77` | SQL predicate — `runner/internal/livepoll/attempts.go:81` | N |
| `live-2b68dd66fea1` | SQL predicate — `runner/internal/livepoll/attempts.go:81` | N |
| `live-267ea2210f98` | SQL predicate — `runner/internal/livepoll/attempts.go:81` | N |
| `live-2a030c1d18df` | SQL predicate — `runner/internal/livepoll/attempts.go:81` | N |
| `live-82731fb4797c` | SQL predicate — `runner/internal/livepoll/attempts.go:81` | N |
| `live-6a5f364aa878` | SQL predicate — `runner/internal/livepoll/attempts.go:82` | N |
| `live-fe275273bc47` | SQL predicate — `runner/internal/livepoll/attempts.go:82` | N |
| `live-20c5a02dae56` | SQL predicate — `runner/internal/livepoll/attempts.go:83` | N |
| `live-3be7be04a4bb` | SQL predicate — `runner/internal/livepoll/attempts.go:84` | N |
| `live-92242d903753` | SQL predicate — `runner/internal/livepoll/attempts.go:84` | N |
| `live-fa06957f88d1` | SQL predicate — `runner/internal/livepoll/attempts.go:84` | N |
| `live-dd96f2cc5fa7` | SQL predicate — `runner/internal/livepoll/attempts.go:84` | N |
| `live-39e5e3505ca2` | SQL predicate — `runner/internal/livepoll/attempts.go:85` | N |
| `live-7ade7607f70f` | SQL predicate — `runner/internal/livepoll/attempts.go:85` | N |
| `live-61dc88f34c10` | SQL predicate — `runner/internal/livepoll/attempts.go:85` | N |
| `live-7f330d3796ba` | Create — `runner/internal/livepoll/attempts.go:87` | N |
| `live-0296a862a53b` | Create — `runner/internal/livepoll/attempts.go:90` | N |
| `live-2fa1527dfae3` | Create — `runner/internal/livepoll/attempts.go:93` | N |
| `live-5c46c1250aff` | Consume — `runner/internal/livepoll/attempts.go:102` | N |
| `live-a945f0e1c2d8` | Consume — `runner/internal/livepoll/attempts.go:106` | N |
| `live-e319e493c991` | Consume — `runner/internal/livepoll/attempts.go:111` | N |
| `live-e0b6cb538e15` | SQL predicate — `runner/internal/livepoll/attempts.go:116` | N |
| `live-c9e938805f19` | SQL predicate — `runner/internal/livepoll/attempts.go:116` | N |
| `live-53689cf8c251` | SQL predicate — `runner/internal/livepoll/attempts.go:116` | N |
| `live-2b862b0caccf` | SQL predicate — `runner/internal/livepoll/attempts.go:116` | N |
| `live-d7216df1aa75` | SQL predicate — `runner/internal/livepoll/attempts.go:116` | N |
| `live-cbfb48f91e75` | SQL predicate — `runner/internal/livepoll/attempts.go:118` | N |
| `live-c6c76dc514bc` | SQL predicate — `runner/internal/livepoll/attempts.go:119` | N |
| `live-0aa098b744bf` | SQL predicate — `runner/internal/livepoll/attempts.go:119` | N |
| `live-5e7276dca230` | SQL predicate — `runner/internal/livepoll/attempts.go:119` | N |
| `live-81db73673cc7` | SQL predicate — `runner/internal/livepoll/attempts.go:120` | N |
| `live-65d9a337a504` | SQL predicate — `runner/internal/livepoll/attempts.go:120` | N |
| `live-8672781990c5` | SQL predicate — `runner/internal/livepoll/attempts.go:120` | N |
| `live-2511b507aed1` | SQL predicate — `runner/internal/livepoll/attempts.go:120` | N |
| `live-d871fe69e2c9` | SQL predicate — `runner/internal/livepoll/attempts.go:121` | N |
| `live-97f2ace2cb88` | SQL predicate — `runner/internal/livepoll/attempts.go:121` | N |
| `live-a81dba0b8b4d` | SQL predicate — `runner/internal/livepoll/attempts.go:121` | N |
| `live-94c628112169` | SQL predicate — `runner/internal/livepoll/attempts.go:122` | N |
| `live-78ec6869be0b` | SQL predicate — `runner/internal/livepoll/attempts.go:122` | N |
| `live-56c0eb5a283d` | SQL predicate — `runner/internal/livepoll/attempts.go:123` | N |
| `live-6a5eadf8ba5e` | SQL predicate — `runner/internal/livepoll/attempts.go:123` | N |
| `live-368ae28883a1` | SQL predicate — `runner/internal/livepoll/attempts.go:124` | N |
| `live-6493674b9e04` | SQL predicate — `runner/internal/livepoll/attempts.go:124` | N |
| `live-4935fd1aa221` | SQL predicate — `runner/internal/livepoll/attempts.go:125` | N |
| `live-2bb67d6083ff` | SQL predicate — `runner/internal/livepoll/attempts.go:125` | N |
| `live-f17b27ccbe5d` | SQL predicate — `runner/internal/livepoll/attempts.go:125` | N |
| `live-d723cc14dfea` | Consume — `runner/internal/livepoll/attempts.go:127` | N |
| `live-446d39ca9a16` | Consume — `runner/internal/livepoll/attempts.go:130` | N |
| `live-6e30c9d98c69` | Consume — `runner/internal/livepoll/attempts.go:133` | N |
| `live-7241b87415c9` | Finish — `runner/internal/livepoll/attempts.go:140` | N |
| `live-2ba062699c9a` | Finish — `runner/internal/livepoll/attempts.go:144` | N |
| `live-1a2b2183371e` | SQL predicate — `runner/internal/livepoll/attempts.go:147` | N |
| `live-77407f5b6f7b` | SQL predicate — `runner/internal/livepoll/attempts.go:147` | N |
| `live-4aafbccaf0d1` | Finish — `runner/internal/livepoll/attempts.go:148` | N |
| `live-5989f40e88b3` | Reconcile — `runner/internal/livepoll/attempts.go:160` | N |
| `live-9962f3a8d268` | SQL predicate — `runner/internal/livepoll/attempts.go:163` | N |
| `live-f23cc78239c9` | SQL predicate — `runner/internal/livepoll/attempts.go:163` | N |
| `live-180fc616fe3d` | Reconcile — `runner/internal/livepoll/attempts.go:164` | N |
| `live-62e566010e6e` | SQL predicate — `runner/internal/livepoll/attempts.go:167` | N |
| `live-118775d5dce6` | SQL predicate — `runner/internal/livepoll/attempts.go:167` | N |
| `live-d3bf139a3331` | Reconcile — `runner/internal/livepoll/attempts.go:168` | N |
| `live-07e07dfd42e2` | Reconcile — `runner/internal/livepoll/attempts.go:171` | N |
| `live-d5a54f37524e` | Maintain — `runner/internal/livepoll/attempts.go:179` | N |
| `live-ab07d51d5d32` | Maintain — `runner/internal/livepoll/attempts.go:183` | N |
| `live-12a938fee70c` | SQL predicate — `runner/internal/livepoll/attempts.go:189` | N |
| `live-887e8003f201` | SQL predicate — `runner/internal/livepoll/attempts.go:189` | N |
| `live-b2b4bf71de81` | SQL predicate — `runner/internal/livepoll/attempts.go:189` | N |
| `live-8e950e0b161d` | SQL predicate — `runner/internal/livepoll/attempts.go:190` | N |
| `live-6f513a5f3c76` | SQL predicate — `runner/internal/livepoll/attempts.go:190` | N |
| `live-50d08b7fdc1d` | SQL predicate — `runner/internal/livepoll/attempts.go:191` | N |
| `live-043e9d9f7910` | SQL predicate — `runner/internal/livepoll/attempts.go:192` | N |
| `live-1e65c90de873` | SQL predicate — `runner/internal/livepoll/attempts.go:192` | N |
| `live-c7b358fdffbd` | SQL predicate — `runner/internal/livepoll/attempts.go:193` | N |
| `live-4705262cff79` | SQL predicate — `runner/internal/livepoll/attempts.go:193` | N |
| `live-17632338357a` | SQL predicate — `runner/internal/livepoll/attempts.go:193` | N |
| `live-9ad474260bd0` | Maintain — `runner/internal/livepoll/attempts.go:196` | N |
| `live-587d469f40a8` | Maintain — `runner/internal/livepoll/attempts.go:199` | N |
| `live-2a9728a3cae6` | Maintain — `runner/internal/livepoll/attempts.go:203` | N |
| `live-549bb86d2ae9` | RevokeRevision — `runner/internal/livepoll/attempts.go:216` | N |
| `live-3c2407036cea` | RevokeRevision — `runner/internal/livepoll/attempts.go:219` | N |
| `live-fccf8cc76c89` | RevokeRevision — `runner/internal/livepoll/attempts.go:223` | N |
| `live-1ac84c0c674a` | RevokeRevision — `runner/internal/livepoll/attempts.go:227` | N |
| `live-2f0f3a5c8eb4` | SQL predicate — `runner/internal/livepoll/attempts.go:233` | N |
| `live-c5b7848f750a` | RevokeRevision — `runner/internal/livepoll/attempts.go:234` | N |
| `live-3acce7b95398` | RevokeRevision — `runner/internal/livepoll/attempts.go:239` | N |
| `live-3d3ac9056cb0` | RevokeRevision — `runner/internal/livepoll/attempts.go:242` | N |
| `live-80190094c56c` | CredentialReceipt — `runner/internal/livepoll/credential_receipt.go:13` | N |
| `live-42a4a36da767` | CredentialReceipt — `runner/internal/livepoll/credential_receipt.go:17` | N |
| `live-e2c8137748b9` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:23` | N |
| `live-383e1affe842` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:23` | N |
| `live-7937107b27af` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:23` | N |
| `live-59c0daedd1d1` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:23` | N |
| `live-3ad0ebf3d13b` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:24` | N |
| `live-c544870e4e83` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:24` | N |
| `live-db3defcb4085` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:24` | N |
| `live-a2ca081a6ed5` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:24` | N |
| `live-64a9998f57b7` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:24` | N |
| `live-de9bc518567d` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:25` | N |
| `live-bda8a2980f1f` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:25` | N |
| `live-9d11a4a74cea` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:26` | N |
| `live-9db281bcbf2a` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:26` | N |
| `live-3cdeeceff68b` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:26` | N |
| `live-40b860dfb04b` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:26` | N |
| `live-e5c3e4a6222a` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:27` | N |
| `live-02daaa19a53e` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:27` | N |
| `live-dd6f8d46b3a3` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:27` | N |
| `live-78e3ddd568df` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:27` | N |
| `live-a2602fab897e` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:28` | N |
| `live-9ae260b17ffd` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:28` | N |
| `live-5d899e98ed95` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:29` | N |
| `live-dfbb0491d137` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:29` | N |
| `live-2712bd0d15e0` | SQL predicate — `runner/internal/livepoll/credential_receipt.go:29` | N |
| `live-85cdc6c121f1` | CredentialReceipt — `runner/internal/livepoll/credential_receipt.go:30` | N |
| `live-5c07c32a2f62` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-0340d206d15d` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-ccb233488702` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-d4fd65115a1e` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-7ed8e5979cbd` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-6f3d1524af14` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-130201daa39c` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-22ebd373864d` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-6bb4f72fc011` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-22bf7ceb8369` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-4bb40bc1c4e6` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-7e5583e01edc` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-08b32e804898` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-6a43fbf5605e` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-883981840f5f` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-1b1889f3e1b7` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-fbadf4f95d9d` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-15bb77c984b4` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-f25470042e95` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-73aac1cf82b5` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-9149c5cbfdf7` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-951fb9329039` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-9e89c1b763e8` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-6525eb0b9818` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-b1826b35f659` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-2377f5884f13` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-e64fdd059d82` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-f17202a9b22f` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-4248e38bc062` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-8b85fecd3181` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-29e6f7093cd5` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-9e98e54399ab` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-8a5f958d8607` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-b5f90e51e8c0` | SampleCause — `runner/internal/livepoll/failure.go:30` | N |
| `live-7717e17484e0` | providerFailure — `runner/internal/livepoll/failure.go:42` | N |
| `live-13ddbefb2d03` | providerFailure — `runner/internal/livepoll/failure.go:45` | N |
| `live-6d6fa45e2948` | providerFailure — `runner/internal/livepoll/failure.go:45` | N |
| `live-9e6b6b6cac4b` | providerFailure — `runner/internal/livepoll/failure.go:45` | N |
| `live-a092cfdbb8b5` | Claim — `runner/internal/livepoll/schedule_store.go:14` | N |
| `live-a73185581117` | Claim — `runner/internal/livepoll/schedule_store.go:18` | N |
| `live-51c04bd25b21` | SQL predicate — `runner/internal/livepoll/schedule_store.go:24` | N |
| `live-17b75336ca50` | SQL predicate — `runner/internal/livepoll/schedule_store.go:24` | N |
| `live-89dbe6985e56` | SQL predicate — `runner/internal/livepoll/schedule_store.go:25` | N |
| `live-099d7c3de599` | SQL predicate — `runner/internal/livepoll/schedule_store.go:25` | N |
| `live-514d75b7e61f` | SQL predicate — `runner/internal/livepoll/schedule_store.go:26` | N |
| `live-11f7194259c9` | SQL predicate — `runner/internal/livepoll/schedule_store.go:26` | N |
| `live-d5c6338cba2b` | SQL predicate — `runner/internal/livepoll/schedule_store.go:29` | N |
| `live-6b7c06394bb2` | SQL predicate — `runner/internal/livepoll/schedule_store.go:29` | N |
| `live-87325d876e05` | SQL predicate — `runner/internal/livepoll/schedule_store.go:30` | N |
| `live-008d9f099a96` | SQL predicate — `runner/internal/livepoll/schedule_store.go:30` | N |
| `live-4ff0a2b3b133` | SQL predicate — `runner/internal/livepoll/schedule_store.go:31` | N |
| `live-c702c363b2b8` | SQL predicate — `runner/internal/livepoll/schedule_store.go:31` | N |
| `live-f02d8f63dd6b` | Claim — `runner/internal/livepoll/schedule_store.go:44` | N |
| `live-d5058531254e` | Claim — `runner/internal/livepoll/schedule_store.go:57` | N |
| `live-72c981bcc45c` | Claim — `runner/internal/livepoll/schedule_store.go:61` | N |
| `live-63fb9b322a58` | Complete — `runner/internal/livepoll/schedule_store.go:68` | N |
| `live-77bb847e4208` | Complete — `runner/internal/livepoll/schedule_store.go:72` | N |
| `live-3c4e9f1ae319` | Complete — `runner/internal/livepoll/schedule_store.go:75` | N |
| `live-02228ac26755` | Complete — `runner/internal/livepoll/schedule_store.go:78` | N |
| `live-7329e753494d` | Complete — `runner/internal/livepoll/schedule_store.go:82` | N |
| `live-381c502f7eb1` | Complete — `runner/internal/livepoll/schedule_store.go:87` | N |
| `live-8f8ad8f24603` | Complete — `runner/internal/livepoll/schedule_store.go:89` | N |
| `live-da428d9f3ac7` | SQL predicate — `runner/internal/livepoll/schedule_store.go:97` | N |
| `live-4a0fb84ef90b` | SQL predicate — `runner/internal/livepoll/schedule_store.go:97` | N |
| `live-a0a522b191e2` | SQL predicate — `runner/internal/livepoll/schedule_store.go:97` | N |
| `live-9bfd9661dc00` | Complete — `runner/internal/livepoll/schedule_store.go:97` | N |
| `live-f21633d12adc` | SQL predicate — `runner/internal/livepoll/schedule_store.go:112` | N |
| `live-bb007960933d` | SQL predicate — `runner/internal/livepoll/schedule_store.go:112` | N |
| `live-b07a06ef91bd` | SQL predicate — `runner/internal/livepoll/schedule_store.go:112` | N |
| `live-6958671fc4dd` | SQL predicate — `runner/internal/livepoll/schedule_store.go:113` | N |
| `live-3d97acac9888` | SQL predicate — `runner/internal/livepoll/schedule_store.go:113` | N |
| `live-d793268cab21` | SQL predicate — `runner/internal/livepoll/schedule_store.go:113` | N |
| `live-2cad4e45d4be` | SQL predicate — `runner/internal/livepoll/schedule_store.go:114` | N |
| `live-7bfa01af53cc` | SQL predicate — `runner/internal/livepoll/schedule_store.go:114` | N |
| `live-b63943e39c48` | SQL predicate — `runner/internal/livepoll/schedule_store.go:114` | N |
| `live-37e4a31caf8c` | Complete — `runner/internal/livepoll/schedule_store.go:116` | N |
| `live-429f41523db9` | Demand — `runner/internal/livepoll/schedule_store.go:128` | N |
| `live-632399c0d751` | SQL predicate — `runner/internal/livepoll/schedule_store.go:132` | N |
| `live-cc34cf6abb2d` | SQL predicate — `runner/internal/livepoll/schedule_store.go:132` | N |
| `live-d8f62b94153c` | SQL predicate — `runner/internal/livepoll/schedule_store.go:132` | N |
| `live-75b51d11220a` | Demand — `runner/internal/livepoll/schedule_store.go:133` | N |
| `live-a8e8d046aa87` | Activate — `runner/internal/livepoll/schedule_store.go:146` | N |
| `live-de74f2fe18a9` | Activate — `runner/internal/livepoll/schedule_store.go:150` | N |
| `live-8d2a9adba6d9` | Activate — `runner/internal/livepoll/schedule_store.go:153` | N |
| `live-a82b9e53bece` | Activate — `runner/internal/livepoll/schedule_store.go:162` | N |
| `live-f4da72085bfb` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-1121138143be` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-3b2d71e11ca3` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-858f94070cb1` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-60241686bc75` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-1baa9a7d1388` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-aa95ae68746c` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-a15fcd57f3ea` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-0effcb12a47f` | terminal — `runner/internal/livepoll/scheduler.go:52` | N |
| `live-24900a37298c` | Tick — `runner/internal/livepoll/scheduler.go:64` | N |
| `live-dea1f00a1675` | Tick — `runner/internal/livepoll/scheduler.go:67` | N |
| `live-1d37694b047b` | Tick — `runner/internal/livepoll/scheduler.go:71` | N |
| `live-bbf6236b07cd` | Tick — `runner/internal/livepoll/scheduler.go:74` | N |
| `live-8abacdc22f97` | execute — `runner/internal/livepoll/scheduler.go:108` | N |
| `live-e87d5fbf6f39` | execute — `runner/internal/livepoll/scheduler.go:115` | N |
| `live-738aea45e397` | execute — `runner/internal/livepoll/scheduler.go:119` | N |
| `live-f8268491272c` | execute — `runner/internal/livepoll/scheduler.go:123` | N |
| `live-f19becf9f22e` | execute — `runner/internal/livepoll/scheduler.go:131` | N |
| `live-e1c958e8931e` | execute — `runner/internal/livepoll/scheduler.go:138` | N |
| `live-4d68ef3c874d` | Run — `runner/internal/livepoll/scheduler.go:146` | N |
| `live-c0f679eae117` | Run — `runner/internal/livepoll/scheduler.go:150` | N |
| `live-b3421c0c9fee` | Run — `runner/internal/livepoll/scheduler.go:160` | N |
| `live-f7d35ce3e246` | Run — `runner/internal/livepoll/scheduler.go:163` | N |
| `live-fdd1503bc20f` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-04108c1cca18` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-552831923e65` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-e9920e4bf9cb` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-5d70d09795f0` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-b5bb603a227f` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-77fca030708a` | PurgesSample — `runner/internal/livepoll/scheduler.go:173` | N |
| `live-4c99d7a3dfc4` | current — `runner/internal/livepoll/verifier.go:66` | N |
| `live-060effe86639` | current — `runner/internal/livepoll/verifier.go:75` | N |
| `live-4e4007ab4606` | current — `runner/internal/livepoll/verifier.go:78` | N |
| `live-494c231c6a19` | Execute — `runner/internal/livepoll/verifier.go:96` | N |
| `live-17ed5287ef77` | Execute — `runner/internal/livepoll/verifier.go:97` | N |
| `live-98fcce600c3c` | Execute — `runner/internal/livepoll/verifier.go:103` | N |
| `live-e4a686f47046` | Execute — `runner/internal/livepoll/verifier.go:110` | N |
| `live-0af88ff7e81b` | Execute — `runner/internal/livepoll/verifier.go:113` | N |
| `live-ef8d3b8131af` | Execute — `runner/internal/livepoll/verifier.go:119` | N |
| `live-6279e0f22847` | Execute — `runner/internal/livepoll/verifier.go:121` | N |
| `live-19c6b6a0c3ba` | Execute — `runner/internal/livepoll/verifier.go:131` | N |
| `live-80bf5482a454` | Execute — `runner/internal/livepoll/verifier.go:137` | N |
| `live-d6a91fb49188` | Execute — `runner/internal/livepoll/verifier.go:141` | N |
| `live-aa64db091573` | Execute — `runner/internal/livepoll/verifier.go:151` | N |
| `live-763d5d471703` | Execute — `runner/internal/livepoll/verifier.go:156` | N |
| `live-35fc43bc926d` | Execute — `runner/internal/livepoll/verifier.go:161` | N |
| `live-2e432404919b` | Execute — `runner/internal/livepoll/verifier.go:163` | N |
| `live-29c77205cfa7` | Execute — `runner/internal/livepoll/verifier.go:168` | N |
| `live-1a1a3e02203c` | Execute — `runner/internal/livepoll/verifier.go:171` | N |
| `live-1992d3632c7b` | Execute — `runner/internal/livepoll/verifier.go:178` | N |
| `live-b5afc36f9094` | Execute — `runner/internal/livepoll/verifier.go:182` | N |
| `live-c4acf6ce6f68` | datasetRowsFit — `runner/internal/livepoll/verifier.go:192` | N |
| `live-1e0b139a7eb4` | datasetRowsFit — `runner/internal/livepoll/verifier.go:196` | N |
| `live-d0daee7f739e` | datasetRowsFit — `runner/internal/livepoll/verifier.go:200` | N |
| `live-a4fc7035571f` | fence — `runner/internal/livepoll/verifier.go:213` | N |
| `live-c716d2dab53d` | fence — `runner/internal/livepoll/verifier.go:215` | N |
| `live-0d9b7c350454` | credential — `runner/internal/mcpgw/live_provider.go:43` | N |
| `live-8b4fa8a9a3a8` | credential — `runner/internal/mcpgw/live_provider.go:47` | N |
| `live-ab563fa67fbf` | credential — `runner/internal/mcpgw/live_provider.go:50` | N |
| `live-d1e2f450e24f` | Definition — `runner/internal/mcpgw/live_provider.go:57` | N |
| `live-decf14e12967` | Definition — `runner/internal/mcpgw/live_provider.go:61` | N |
| `live-68ed6ac42c7a` | Definition — `runner/internal/mcpgw/live_provider.go:67` | N |
| `live-cedd2689eb32` | Definition — `runner/internal/mcpgw/live_provider.go:74` | N |
| `live-44de4771b956` | Definition — `runner/internal/mcpgw/live_provider.go:80` | N |
| `live-a6eb2ae51009` | Definition — `runner/internal/mcpgw/live_provider.go:86` | N |
| `live-14d4d5a3f00c` | Definition — `runner/internal/mcpgw/live_provider.go:91` | N |
| `live-e0ab405f4f1d` | Definition — `runner/internal/mcpgw/live_provider.go:97` | N |
| `live-23aace826edb` | Definition — `runner/internal/mcpgw/live_provider.go:101` | N |
| `live-157fbf08e3f8` | Call — `runner/internal/mcpgw/live_provider.go:109` | N |
| `live-8e4dc8338994` | Call — `runner/internal/mcpgw/live_provider.go:116` | N |
| `live-487b6ca31c51` | Call — `runner/internal/mcpgw/live_provider.go:120` | N |
| `live-6e3f8793e8f6` | Call — `runner/internal/mcpgw/live_provider.go:123` | N |
| `live-e07c27d36a74` | Call — `runner/internal/mcpgw/live_provider.go:130` | N |
| `live-662d4d73bbbc` | Call — `runner/internal/mcpgw/live_provider.go:134` | N |
| `live-9a44bf1439cd` | Call — `runner/internal/mcpgw/live_provider.go:145` | N |
| `live-e4e1a4da74e6` | Call — `runner/internal/mcpgw/live_provider.go:148` | N |
| `live-555e9635f5ee` | Call — `runner/internal/mcpgw/live_provider.go:151` | N |
| `live-835522efbf68` | Release — `runner/internal/mcpgw/live_provider.go:161` | N |
| `live-bb6fb9c135c5` | liveRetryAfter — `runner/internal/mcpgw/live_provider.go:181` | N |
| `live-4085ce01147a` | liveUpstreamFailure — `runner/internal/mcpgw/live_provider.go:198` | N |
| `live-29b58941c65b` | liveUpstreamFailure — `runner/internal/mcpgw/live_provider.go:200` | N |
| `live-514168d5e0e0` | liveUpstreamFailure — `runner/internal/mcpgw/live_provider.go:205` | N |
| `live-1ef71039ddc0` | liveUpstreamFailure — `runner/internal/mcpgw/live_provider.go:208` | N |
| `live-605e75762e5a` | liveUpstreamFailure — `runner/internal/mcpgw/live_provider.go:211` | N |
| `live-318d6fecd83c` | ValidConnectionName false result — `runner/internal/mcpgw/livetools.go:13` | N |
| `live-ac2bb2f46317` | ValidConnectionName — `runner/internal/mcpgw/livetools.go:13` | N |
| `live-0dfe61c3b231` | LiveToolHashes — `runner/internal/mcpgw/livetools.go:28` | N |
| `live-f6be837acd24` | LiveToolHashes — `runner/internal/mcpgw/livetools.go:42` | N |
| `live-f319133c2e9d` | LiveToolHashes — `runner/internal/mcpgw/livetools.go:47` | N |
| `live-ad8ba65d3957` | ListToolsComplete — `runner/internal/mcpgw/livetools.go:75` | N |
| `live-a91fbc5aaf87` | ListToolsComplete — `runner/internal/mcpgw/livetools.go:86` | N |
| `live-975c70ced26d` | ListToolsComplete — `runner/internal/mcpgw/livetools.go:89` | N |
| `live-e98a4c1f3962` | ListToolsComplete — `runner/internal/mcpgw/livetools.go:98` | N |
| `live-fa4f4530a890` | createBoundAsk — `runner/internal/server/bound_asks.go:60` | N |
| `live-43a6c4db2a78` | createBoundAsk — `runner/internal/server/bound_asks.go:64` | N |
| `live-4ae9690cb0ec` | createBoundAsk — `runner/internal/server/bound_asks.go:68` | N |
| `live-5662de3e4acf` | createBoundAsk — `runner/internal/server/bound_asks.go:71` | N |
| `live-ff7734aa5171` | createBoundAsk — `runner/internal/server/bound_asks.go:78` | N |
| `live-3f990678f838` | createBoundAsk — `runner/internal/server/bound_asks.go:81` | N |
| `live-9bd5f8630188` | createBoundAsk — `runner/internal/server/bound_asks.go:84` | N |
| `live-a24507eac0fa` | createBoundAsk — `runner/internal/server/bound_asks.go:88` | N |
| `live-8488822cb789` | createBoundAsk — `runner/internal/server/bound_asks.go:91` | N |
| `live-49bf35562893` | createBoundAsk — `runner/internal/server/bound_asks.go:94` | N |
| `live-348b4043d43b` | SQL predicate — `runner/internal/server/bound_asks.go:99` | N |
| `live-5691751f0453` | SQL predicate — `runner/internal/server/bound_asks.go:99` | N |
| `live-fd4a87233b7b` | SQL predicate — `runner/internal/server/bound_asks.go:99` | N |
| `live-ab1e3b2804aa` | createBoundAsk — `runner/internal/server/bound_asks.go:100` | N |
| `live-9987b2b1a5fe` | createBoundAsk — `runner/internal/server/bound_asks.go:103` | N |
| `live-690c6d548d1d` | createBoundAsk — `runner/internal/server/bound_asks.go:107` | N |
| `live-f59472553213` | createBoundAsk — `runner/internal/server/bound_asks.go:110` | N |
| `live-e30e6a381bbf` | createBoundAsk — `runner/internal/server/bound_asks.go:113` | N |
| `live-510667fb60dd` | createBoundAsk — `runner/internal/server/bound_asks.go:118` | N |
| `live-6cd13f01ece3` | createBoundAsk — `runner/internal/server/bound_asks.go:122` | N |
| `live-b0cb5e6440cf` | createBoundAsk — `runner/internal/server/bound_asks.go:128` | N |
| `live-990bd469c62a` | createBoundAsk — `runner/internal/server/bound_asks.go:135` | N |
| `live-2be0f1f8c8f6` | createBoundAsk — `runner/internal/server/bound_asks.go:137` | N |
| `live-827787613c03` | createBoundAsk — `runner/internal/server/bound_asks.go:139` | N |
| `live-261ffe86f45f` | createBoundAsk — `runner/internal/server/bound_asks.go:141` | N |
| `live-4648ae0f030b` | createBoundAsk — `runner/internal/server/bound_asks.go:141` | N |
| `live-2ac82d983321` | createBoundAsk — `runner/internal/server/bound_asks.go:146` | N |
| `live-9ca64cfc99fb` | createBoundAsk — `runner/internal/server/bound_asks.go:152` | N |
| `live-117cd0a18570` | createBoundAsk — `runner/internal/server/bound_asks.go:162` | N |
| `live-b415394c50aa` | createBoundAsk — `runner/internal/server/bound_asks.go:163` | N |
| `live-afed174bfb50` | createBoundAsk — `runner/internal/server/bound_asks.go:169` | N |
| `live-548c2942218c` | createBoundAsk — `runner/internal/server/bound_asks.go:172` | N |
| `live-5415f7521eb4` | createBoundAsk — `runner/internal/server/bound_asks.go:178` | N |
| `live-8c2b601e211f` | createBoundAsk — `runner/internal/server/bound_asks.go:180` | N |
| `live-23491f31d398` | createBoundAsk — `runner/internal/server/bound_asks.go:187` | N |
| `live-77f2094f01af` | createBoundAsk — `runner/internal/server/bound_asks.go:189` | N |
| `live-0b8fabfb0dde` | createBoundAsk — `runner/internal/server/bound_asks.go:199` | N |
| `live-ac6ac8fc42bc` | createBoundAsk — `runner/internal/server/bound_asks.go:202` | N |
| `live-3e6a18dd545b` | createBoundAsk — `runner/internal/server/bound_asks.go:204` | N |
| `live-586ba890ad59` | createBoundAsk — `runner/internal/server/bound_asks.go:208` | N |
| `live-cd7c1cb03156` | createBoundAsk — `runner/internal/server/bound_asks.go:209` | N |
| `live-f147ed812615` | createBoundAsk — `runner/internal/server/bound_asks.go:212` | N |
| `live-bdb8ebe51601` | createBoundAsk — `runner/internal/server/bound_asks.go:221` | N |
| `live-12fc25d8b220` | createBoundAsk — `runner/internal/server/bound_asks.go:224` | N |
| `live-6c5c394d886c` | createBoundAsk — `runner/internal/server/bound_asks.go:231` | N |
| `live-7e1eca04b3fa` | createBoundAsk — `runner/internal/server/bound_asks.go:237` | N |
| `live-71f534daa2ca` | createBoundAsk — `runner/internal/server/bound_asks.go:242` | N |
| `live-d4c90d69834d` | createBoundAsk — `runner/internal/server/bound_asks.go:245` | N |
| `live-d3fa83e237bb` | createBoundAsk — `runner/internal/server/bound_asks.go:248` | N |
| `live-f75d56847e35` | SQL predicate — `runner/internal/server/bound_asks.go:255` | N |
| `live-296cd826c53e` | SQL predicate — `runner/internal/server/bound_asks.go:255` | N |
| `live-91aa826a5318` | SQL predicate — `runner/internal/server/bound_asks.go:255` | N |
| `live-d2837bf2055b` | SQL predicate — `runner/internal/server/bound_asks.go:255` | N |
| `live-dc27a10c7f8e` | SQL predicate — `runner/internal/server/bound_asks.go:271` | N |
| `live-6ad317c90621` | SQL predicate — `runner/internal/server/bound_asks.go:271` | N |
| `live-2968e47a3bf7` | SQL predicate — `runner/internal/server/bound_asks.go:271` | N |
| `live-b450b0164571` | SQL predicate — `runner/internal/server/bound_asks.go:387` | N |
| `live-f9e1929c4bb1` | SQL predicate — `runner/internal/server/bound_asks.go:387` | N |
| `live-57e80e2b5b71` | SQL predicate — `runner/internal/server/bound_asks.go:400` | N |
| `live-d609954e68a9` | SQL predicate — `runner/internal/server/bound_asks.go:444` | N |
| `live-1e90971e4643` | SQL predicate — `runner/internal/server/bound_asks.go:474` | N |
| `live-1b913b4f376f` | SQL predicate — `runner/internal/server/bound_asks.go:486` | N |
| `live-80037b269d75` | SQL predicate — `runner/internal/server/bound_asks.go:498` | N |
| `live-354c2be4aa51` | SQL predicate — `runner/internal/server/bound_asks.go:504` | N |
| `live-9ee6035407dd` | SQL predicate — `runner/internal/server/bound_asks.go:510` | N |
| `live-3bd3540e4f69` | SQL predicate — `runner/internal/server/bound_asks.go:597` | N |
| `live-96d37004487e` | SQL predicate — `runner/internal/server/bound_asks.go:606` | N |
| `live-6bd586960e34` | SQL predicate — `runner/internal/server/bound_asks.go:617` | N |
| `live-9951da29750b` | SQL predicate — `runner/internal/server/bound_asks.go:660` | N |
| `live-9c205377de2c` | SQL predicate — `runner/internal/server/bound_asks.go:660` | N |
| `live-554d2903a652` | SQL predicate — `runner/internal/server/bound_asks.go:663` | N |
| `live-dc00bb0f77ae` | SQL predicate — `runner/internal/server/bound_asks.go:663` | N |
| `live-6ba963f7e2d9` | SQL predicate — `runner/internal/server/bound_asks.go:663` | N |
| `live-c1d424be90c2` | SQL predicate — `runner/internal/server/bound_asks.go:663` | N |
| `live-d18bc02044e7` | SQL predicate — `runner/internal/server/bound_asks.go:678` | N |
| `live-77ed8c7e0bde` | SQL predicate — `runner/internal/server/bound_asks.go:693` | N |
| `live-fa6784674043` | SQL predicate — `runner/internal/server/bound_asks.go:705` | N |
| `live-81ce76f41a6f` | SQL predicate — `runner/internal/server/bound_asks.go:740` | N |
| `live-c71bb489fe65` | SQL predicate — `runner/internal/server/bound_asks.go:759` | N |
| `live-e85fb99c2a09` | boundLoginLive — `runner/internal/server/bound_asks.go:800` | N |
| `live-bd6eeb137262` | boundLoginLive — `runner/internal/server/bound_asks.go:803` | N |
| `live-3b668a74a7ed` | boundLoginLive — `runner/internal/server/bound_asks.go:806` | N |
| `live-90d5bd22b147` | boundLoginLive — `runner/internal/server/bound_asks.go:809` | N |
| `live-2b52123f5f61` | liveCore — `runner/internal/server/bound_live_asks.go:34` | N |
| `live-380cc466245f` | liveCore — `runner/internal/server/bound_live_asks.go:37` | N |
| `live-0e1c400a1f67` | liveCore — `runner/internal/server/bound_live_asks.go:43` | N |
| `live-82715e66decb` | liveCore — `runner/internal/server/bound_live_asks.go:47` | N |
| `live-c80cdcb0a016` | liveCore — `runner/internal/server/bound_live_asks.go:53` | H |
| `live-52eb05188cab` | liveCore — `runner/internal/server/bound_live_asks.go:63` | N |
| `live-c1dbf1cbfcbf` | liveCore — `runner/internal/server/bound_live_asks.go:69` | N |
| `live-57b1462e61ca` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:80` | N |
| `live-d95ad05c9de1` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:91` | N |
| `live-7c6094fa35ec` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:95` | N |
| `live-9975cc76704a` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:107` | N |
| `live-477e76615106` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:111` | N |
| `live-18a06fa20543` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:115` | N |
| `live-92da530efc7d` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:130` | N |
| `live-78ea7a2c4e61` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:136` | N |
| `live-eefecce66055` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:140` | N |
| `live-fdefdd1a3e77` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:144` | N |
| `live-474b30a447bb` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:151` | N |
| `live-a20d075a1e0b` | createBoundLiveAsk — `runner/internal/server/bound_live_asks.go:155` | N |
| `live-9bfd7473061e` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:167` | N |
| `live-36f6e555ca5c` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:171` | N |
| `live-f062fb8ddfb9` | SQL predicate — `runner/internal/server/bound_live_asks.go:179` | N |
| `live-68f3891ec3e6` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:180` | H |
| `live-3e924c2b2c41` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:183` | N |
| `live-3ffa0b6653de` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:186` | N |
| `live-83093c4f8235` | SQL predicate — `runner/internal/server/bound_live_asks.go:193` | N |
| `live-c055ae90cdb4` | SQL predicate — `runner/internal/server/bound_live_asks.go:200` | N |
| `live-3635653703b7` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:200` | N |
| `live-7f6aa399ccd3` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:204` | N |
| `live-5bff09b01f80` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:208` | N |
| `live-15b88bd104bb` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:211` | N |
| `live-3728eb878e72` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:214` | N |
| `live-8c6ecc725795` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:219` | N |
| `live-ea379e40dad0` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:221` | N |
| `live-5d6d93fc289f` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:223` | N |
| `live-8c024358468a` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:230` | N |
| `live-a78de52e47a6` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:232` | N |
| `live-6d3fa1575544` | SQL predicate — `runner/internal/server/bound_live_asks.go:238` | N |
| `live-80ce0161d3f0` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:238` | N |
| `live-0fd115425085` | SQL predicate — `runner/internal/server/bound_live_asks.go:241` | N |
| `live-d2a2c5cfa45c` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:241` | N |
| `live-36423915a33d` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:247` | N |
| `live-483b38cc1726` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:251` | N |
| `live-38880d5bfc00` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:254` | N |
| `live-a03ad19fd99f` | answerBoundLiveAsk — `runner/internal/server/bound_live_asks.go:259` | N |
| `live-c052d7d16bf8` | SQL predicate — `runner/internal/server/bound_live_asks.go:278` | N |
| `live-fb186558b08e` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:279` | H |
| `live-7dd308b70576` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:287` | N |
| `live-6895cae88133` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:291` | N |
| `live-fd785a829780` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:294` | N |
| `live-5c6bf00cdc2c` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:299` | N |
| `live-57e263297ca8` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:301` | N |
| `live-c49da77b83fb` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:301` | N |
| `live-0f47df4a3681` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:303` | N |
| `live-fdfaa5fea9e0` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:309` | N |
| `live-98df7b4f7d7f` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:313` | N |
| `live-bd65eb9bdc0a` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:318` | N |
| `live-78e5f5543546` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:323` | N |
| `live-83cff55c53b0` | preflightLiveAnswer — `runner/internal/server/bound_live_asks.go:342` | N |
| `live-0eb59dc79e87` | SetLiveDataAuthority — `runner/internal/server/bound_live_asks.go:356` | H |
| `live-10807067691a` | SQL predicate — `runner/internal/server/datasets.go:123` | N |
| `live-ca00bd2cce46` | SQL predicate — `runner/internal/server/datasets.go:124` | N |
| `live-bc7e85032e65` | SQL predicate — `runner/internal/server/datasets.go:125` | N |
| `live-cc4ff88de417` | SQL predicate — `runner/internal/server/datasets.go:125` | N |
| `live-6d9377abf1d7` | SQL predicate — `runner/internal/server/datasets.go:126` | N |
| `live-bdea12cca19d` | SQL predicate — `runner/internal/server/datasets.go:140` | N |
| `live-2050405a6f41` | SQL predicate — `runner/internal/server/datasets.go:267` | N |
| `live-1aebaa5a21d5` | datasetVisibilityCause — `runner/internal/server/datasets.go:458` | N |
| `live-f51cf4c168ec` | datasetVisibilityCause — `runner/internal/server/datasets.go:462` | N |
| `live-384c12f8e303` | datasetVisibilityCause — `runner/internal/server/datasets.go:465` | N |
| `live-6cc261b1f9b1` | SQL predicate — `runner/internal/server/datasets.go:533` | N |
| `live-3fcab618092c` | SQL predicate — `runner/internal/server/datasets.go:534` | N |
| `live-5c86198398e5` | SQL predicate — `runner/internal/server/datasets.go:535` | N |
| `live-9e7f0c988308` | SQL predicate — `runner/internal/server/datasets.go:535` | N |
| `live-f566a3657231` | filterDatasetDelivery — `runner/internal/server/datasets.go:680` | N |
| `live-4fc627079a56` | filterDatasetDelivery — `runner/internal/server/datasets.go:693` | N |
| `live-43e199b71a97` | filterDatasetDelivery — `runner/internal/server/datasets.go:696` | N |
| `live-c37e45d0129b` | filterDatasetDelivery — `runner/internal/server/datasets.go:699` | N |
| `live-21e29b79b75a` | filterDatasetDelivery — `runner/internal/server/datasets.go:710` | N |
| `live-b5aa547a3a57` | filterDatasetDelivery — `runner/internal/server/datasets.go:716` | N |
| `live-d2936d713ae2` | filterDatasetDelivery — `runner/internal/server/datasets.go:718` | N |
| `live-cd072e779876` | filterDatasetDelivery — `runner/internal/server/datasets.go:721` | N |
| `live-39bef03ff567` | filterDatasetDelivery — `runner/internal/server/datasets.go:724` | N |
| `live-b84861cdbafd` | filterDatasetDelivery — `runner/internal/server/datasets.go:727` | N |
| `live-47f96ec9fdec` | filterDatasetDelivery — `runner/internal/server/datasets.go:729` | N |
| `live-bb86d470099c` | filterDatasetDelivery — `runner/internal/server/datasets.go:741` | N |
| `live-10c13e129b7e` | SQL predicate — `runner/internal/server/datasets.go:747` | N |
| `live-a0588adb37e7` | SQL predicate — `runner/internal/server/datasets.go:747` | N |
| `live-a2b5055dc370` | SQL predicate — `runner/internal/server/datasets.go:747` | N |
| `live-e7706552e87e` | filterDatasetDelivery — `runner/internal/server/datasets.go:753` | N |
| `live-50bd04e2cb58` | filterDatasetDelivery — `runner/internal/server/datasets.go:756` | N |
| `live-cb1f2bcb9aff` | filterDatasetDelivery — `runner/internal/server/datasets.go:758` | N |
| `live-fdf2249d5a7e` | filterDatasetDelivery — `runner/internal/server/datasets.go:767` | N |
| `live-642db12b78be` | filterDatasetDelivery — `runner/internal/server/datasets.go:777` | N |
| `live-f524974d64ea` | mcpDatasetAuthority — `runner/internal/server/datasets_mcp.go:19` | N |
| `live-3fb8547d8791` | SQL predicate — `runner/internal/server/datasets_mcp.go:26` | N |
| `live-3f6fd20ca2af` | SQL predicate — `runner/internal/server/datasets_mcp.go:26` | N |
| `live-c420d4a9265d` | mcpDatasetAuthority — `runner/internal/server/datasets_mcp.go:26` | N |
| `live-052e02bfedbb` | mcpDatasetAuthority — `runner/internal/server/datasets_mcp.go:36` | N |
| `live-6e15da27b835` | mcpDatasetAuthority — `runner/internal/server/datasets_mcp.go:40` | N |
| `live-a31b9a31e19b` | SQL predicate — `runner/internal/server/datasets_mcp.go:59` | N |
| `live-05af40e9eac3` | SQL predicate — `runner/internal/server/datasets_mcp.go:59` | N |
| `live-6af2f9b4d784` | SQL predicate — `runner/internal/server/datasets_mcp.go:59` | N |
| `live-2fcf21bcca24` | SQL predicate — `runner/internal/server/datasets_mcp.go:59` | N |
| `live-e343e691dda6` | mcpDatasetAuthority — `runner/internal/server/datasets_mcp.go:59` | N |
| `live-e486916de46f` | checkMCPAuthority — `runner/internal/server/datasets_mcp.go:75` | N |
| `live-a22e61677935` | checkMCPAuthority — `runner/internal/server/datasets_mcp.go:83` | N |
| `live-3d791f17bc7a` | checkMCPAuthority — `runner/internal/server/datasets_mcp.go:86` | N |
| `live-a86573522b34` | checkMCPAuthority — `runner/internal/server/datasets_mcp.go:89` | N |
| `live-5744767c02f9` | checkMCPAuthority — `runner/internal/server/datasets_mcp.go:93` | N |
| `live-d2809d55b255` | mcpDatasetRows — `runner/internal/server/datasets_mcp.go:103` | N |
| `live-7017d1ebea1e` | SQL predicate — `runner/internal/server/datasets_mcp.go:111` | N |
| `live-e039534cd2eb` | SQL predicate — `runner/internal/server/datasets_mcp.go:111` | N |
| `live-f624e43a8cad` | SQL predicate — `runner/internal/server/datasets_mcp.go:111` | N |
| `live-d5ad644beb3a` | SQL predicate — `runner/internal/server/datasets_mcp.go:111` | N |
| `live-55329b7be894` | mcpDatasetRows — `runner/internal/server/datasets_mcp.go:111` | N |
| `live-a61007be9667` | mcpDatasetRows — `runner/internal/server/datasets_mcp.go:114` | N |
| `live-88f4dc2bdb84` | mcpDatasetRows — `runner/internal/server/datasets_mcp.go:115` | N |
| `live-c055303fa557` | mcpDatasetRows — `runner/internal/server/datasets_mcp.go:127` | N |
| `live-d6d183c2b687` | mcpDatasetVersion — `runner/internal/server/datasets_mcp.go:138` | N |
| `live-7397a01abc93` | SQL predicate — `runner/internal/server/datasets_mcp.go:142` | N |
| `live-5c98415d9923` | SQL predicate — `runner/internal/server/datasets_mcp.go:142` | N |
| `live-9e139d4c2245` | SQL predicate — `runner/internal/server/datasets_mcp.go:142` | N |
| `live-16ac1df9f76e` | SQL predicate — `runner/internal/server/datasets_mcp.go:142` | N |
| `live-badb05159af9` | mcpDatasetVersion — `runner/internal/server/datasets_mcp.go:142` | N |
| `live-e07329307de1` | check — `runner/internal/server/datasets_mcp_authority_cache.go:65` | H |
| `live-7332e162e4a6` | check — `runner/internal/server/datasets_mcp_authority_cache.go:70` | N |
| `live-a9432def5ca2` | handleLiveAttempt — `runner/internal/server/live_data_attempt.go:28` | N |
| `live-3cb99c8777d6` | handleLiveAttempt — `runner/internal/server/live_data_attempt.go:33` | N |
| `live-23cec9096cd3` | handleLiveAttempt — `runner/internal/server/live_data_attempt.go:35` | N |
| `live-61513c7121c8` | liveFreshDefinition — `runner/internal/server/live_data_listing.go:13` | H |
| `live-90cf3e3f8934` | liveFreshDefinition — `runner/internal/server/live_data_listing.go:17` | N |
| `live-7bf111f2a677` | liveFreshDefinition — `runner/internal/server/live_data_listing.go:21` | N |
| `live-736ad06bd466` | liveFreshDefinition — `runner/internal/server/live_data_listing.go:27` | N |
| `live-85f641d6f6e7` | handleLiveDefinition — `runner/internal/server/live_data_listing.go:52` | N |
| `live-37e521d1a15d` | liveDefinitionAuthenticated — `runner/internal/server/live_data_listing.go:77` | H |
| `live-864c4e6887b0` | handleLivePosture — `runner/internal/server/live_data_posture.go:42` | N |
| `live-c198d13e17f2` | handleLivePosture — `runner/internal/server/live_data_posture.go:46` | N |
| `live-1ac62b4e155d` | livePosture — `runner/internal/server/live_data_posture.go:56` | H |
| `live-1a90ab07fe2f` | SQL predicate — `runner/internal/server/live_data_posture.go:68` | N |
| `live-d2cff4306ab8` | SQL predicate — `runner/internal/server/live_data_posture.go:68` | N |
| `live-09b93e1624b7` | SQL predicate — `runner/internal/server/live_data_posture.go:68` | N |
| `live-84e1d3eb72f8` | SQL predicate — `runner/internal/server/live_data_posture.go:68` | N |
| `live-c48052b8fd7d` | livePosture — `runner/internal/server/live_data_posture.go:69` | N |
| `live-41e6b2bf352d` | livePosture — `runner/internal/server/live_data_posture.go:76` | N |
| `live-96ac88b9f923` | livePosture — `runner/internal/server/live_data_posture.go:80` | N |
| `live-4de257785bdf` | livePosture — `runner/internal/server/live_data_posture.go:83` | N |
| `live-4a5e7c226f8c` | livePodPosture — `runner/internal/server/live_data_posture.go:131` | N |
| `live-af3ca33f8084` | livePodPosture — `runner/internal/server/live_data_posture.go:140` | N |
| `live-c46d1a0707be` | livePodPosture — `runner/internal/server/live_data_posture.go:144` | N |
| `live-b606c6f86b7d` | livePodPosture — `runner/internal/server/live_data_posture.go:149` | N |
| `live-e532bcc9d781` | livePodPosture — `runner/internal/server/live_data_posture.go:154` | N |
| `live-95aa5d5c9a82` | livePodPosture — `runner/internal/server/live_data_posture.go:162` | H |
| `live-4a3096fa5a93` | livePodPosture — `runner/internal/server/live_data_posture.go:166` | N |
| `live-d24558616240` | isolatedPosturePod — `runner/internal/server/live_data_posture.go:206` | N |
| `live-b40313466a0e` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:256` | N |
| `live-4e2c6cbc50ec` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:260` | N |
| `live-c0a23d8487e4` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:265` | N |
| `live-2fa639585504` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:275` | N |
| `live-c4b1d69dc1ac` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:279` | N |
| `live-4ccde3c5e82d` | isolatedPostureContainer — `runner/internal/server/live_data_posture.go:283` | N |
| `live-f67c00fdcf4b` | liveReceiptMatches — `runner/internal/server/live_data_reconcile.go:13` | N |
| `live-c41bdbec12b1` | liveDecision — `runner/internal/server/live_data_reconcile.go:28` | N |
| `live-4b036d323c85` | liveDecision — `runner/internal/server/live_data_reconcile.go:31` | N |
| `live-ff3ac7b3ee8f` | errOrRefusal — `runner/internal/server/live_data_reconcile.go:37` | N |
| `live-db9db74e7fa1` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:45` | N |
| `live-6af639b2081d` | SQL predicate — `runner/internal/server/live_data_reconcile.go:48` | N |
| `live-779dbdc08b7c` | SQL predicate — `runner/internal/server/live_data_reconcile.go:48` | N |
| `live-cc0347626c73` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:49` | N |
| `live-d00e40f08cef` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:61` | N |
| `live-396abb29b154` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:73` | N |
| `live-6731a113d9bc` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:92` | N |
| `live-20aab553f07e` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:97` | N |
| `live-ee613b154a0f` | SQL predicate — `runner/internal/server/live_data_reconcile.go:101` | N |
| `live-a251ed550a03` | SQL predicate — `runner/internal/server/live_data_reconcile.go:101` | N |
| `live-74baf2a3aadd` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:101` | H |
| `live-e8913e3d7967` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:105` | N |
| `live-1ecca86ea0c0` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:109` | N |
| `live-555e5595641c` | SQL predicate — `runner/internal/server/live_data_reconcile.go:110` | N |
| `live-2f6f03a9de9a` | SQL predicate — `runner/internal/server/live_data_reconcile.go:110` | N |
| `live-021525ee8bd2` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:110` | N |
| `live-c99e4da7bc55` | SQL predicate — `runner/internal/server/live_data_reconcile.go:115` | N |
| `live-8e5998df7013` | reconcileLiveDecisions — `runner/internal/server/live_data_reconcile.go:115` | N |
| `live-7c96938b257c` | RunLivePolling — `runner/internal/server/live_data_scheduler.go:14` | N |
| `live-6f0d701335aa` | RunLivePolling — `runner/internal/server/live_data_scheduler.go:18` | N |
| `live-3658392b66ac` | RunLivePolling — `runner/internal/server/live_data_scheduler.go:27` | N |

### Assessment for cross-model review

- **Equivalent mutants (six, scoped to payload authorization):** the two hidden ArtifactViewer export handlers, two diagnostic-only hook branches, and two unreachable clear(access_removed) branches named above. Keep their checks; this is not an argument for deleting defense in depth.
- **Needs a test:** every other survivor. The two policy-transaction guards highlighted above deserve priority. The remaining backend survivors have no independently demonstrated equivalence; the selected suites passing is insufficient to classify them more favorably.
- **Inconclusive, needs a test or harness repair:** all 27 panic/harness results, both client runtime errors and the one timeout. Their exact names and locations are listed above; none count as killed.
- **Real gap outside this mechanical guard list:** the pre-checkpoint database-restore window still requires stopping dispatch and independently fencing the epoch before any restore. The tool cannot certify that operational step. No exploitable bypass is established solely by a survivor.

This is my conservative disposition, not an independent cross-model security review.
The 1,147 unresolved entries comprise six scoped equivalent mutants and 1,141
entries needing tests, fixtures or further semantic review. Owner-name private-source
display and authorized static snapshots were dropped by owner decision on 2026-11-05.

### Go tooling handoff verification (2026-11-05)

`0e8cb165d` removes the tracked full ledger and adds the compact unresolved index;
`017e54fb1` ports the backend/client runners, catalogue and provenance join to
`runner/cmd/live-data-audit` plus `runner/internal/liveaudit`. The three Python
scripts and the JavaScript inventory helper are removed. No product behavior or
rollout gate changes in this structural follow-up.

| Focused check | Exact result |
|---|---|
| Go audit package, eight named tests | PASS, zero skips; includes SQL token spans, UTF-8/regex client positions, assertion/runtime separation, provenance/missing evidence, tagged-switch siblings and unknown ID refusal |
| Backend catalogue parity | All 1,760 guard objects match prior catalogue field for field; run envelope base commit naturally changes |
| Client catalogue parity | All 56 guard objects match prior catalogue field for field |
| Provenance join parity | Entire parsed 1,816-row ledger, including run history and outcome counts, equals the prior ledger |
| Backend guard smoke | Five selected removals killed: branch, allow, false-result, tagged switch and redirect assignment. Baselines: contracts identity 19 PASS/0 SKIP (0.016s), livedata 129 PASS/0 SKIP (0.151s) |
| SQL/effect smoke | All three removals killed: owner epoch, owner rotation and 80-row queue window. Baselines: 1 PASS each/0 SKIP (0.903s, 2.274s, 2.272s) |
| Active migration trigger smoke | One removal killed; baseline 17 PASS/0 SKIP (2.405s) |
| Client classification smoke | One killed, one survivor, one runtime error; expected exit 1. Initial/restored baseline both 61 PASS/0 pending (3.988s/3.976s) |
| Focused Go lint, vet, command build, scrub, whitespace | PASS after correcting lint tool PATH and 19 initial lint findings |

The regex-removal regression test was deliberately made red (lost export refusal),
then restored and passed. Mutation failures above are intentional observations,
not passing product tests. The command package has no standalone tests; its
compiled CLI is exercised by the catalogue/join and mutation smoke runs.
The historical full matrix and browser evidence above was not rerun for this
Go-only tooling follow-up. The client lexer is scoped to the fixed current
surfaces; future syntax changes need inventory review, as documented in the tool
guide. Independent cross-model review and semantic guard completeness remain open.
