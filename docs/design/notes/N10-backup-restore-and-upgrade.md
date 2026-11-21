# N10 — backup, restore and upgrade consistency

Blocks: BLG-658 (1.18 whole-install backup, checkpoint, inert restore), BLG-659 (1.19 ordered
upgrade with a version gate and rollback). Source: `docs/design/managed-install.md` ("Backups and
restore", "Upgrades", calls 1, 2, 9); `docs/design/credential-handling.md` call 11;
`docs/design/0.7.0-build-plan.md` ("Hard-rules notes" N10, call 11; N6's self-fencing executors).
Must agree with N1 (install secrets), N2 (email and links), and N3 (sign-in and recovery) where
restore or key state overlaps them.

## Failure rule

Anything in this area that fails, or whose state cannot be proven (a missing checkpoint record, an
unreadable security ledger, an unverified image digest, a schema compatibility check that errors),
durably records the stage it reached and stops there. The guarantee is that the operation stays
inert and can safely resume from, or stop for manual recovery at, that recorded stage — not a
literal guarantee that no write happened anywhere; a multi-service, multi-file operation cannot
promise that. **Inertness itself is restore-only** (rule 7): a failed or canceled routine backup
never takes the install offline — only that backup's own run fails, its resources and lease are
cleaned up, and ordinary work keeps running. Upgrade freezes admission for its own stated reason
(rule 12), which is narrower than restore's inertness. The person sees which stage stopped and why;
the agent logs the cause (which record, which gap, which check) without secret values, the same
pattern N1 rule 7 uses for install failures.

## Rules

1. **A backup is the whole install, at one point, described by a checksummed manifest.** All three
   databases, the vault and Board keys, the live-data authority and issuer state, runner files, and
   workspace/native-history volumes are captured — except Claude seat-host state, which
   `0.7.0-build-plan.md` call 1 already says a restore never brings back (the person signs in
   again) — encrypted outside the cluster (`managed-install.md` call 2). The backup carries a
   manifest naming the install identity, backup identity, checkpoint identity, the recovery target
   (rule 2), volume-to-claim mappings, and which key each encrypted piece needs. The manifest is
   checksummed and authenticated; so is every backup data part, not only the manifest. Restore
   authenticates and stages all needed content — safely, rejecting absolute paths or symlink
   escapes during extraction — before touching any live resource; staging content into an isolated
   namespace to inspect its checkpoint is explicitly allowed before any destructive replacement of
   live data runs. A swapped, truncated, or tampered manifest or data part stops the restore before
   it touches a live resource. A backup that cannot reach every listed piece names which one and
   either states it lost explicitly or stops — it never ships a partial backup silently — and a
   backup whose decryption key is missing or unavailable (including an unreachable external key
   backend) for existing ciphertext is never labeled a usable recovery set, even though the
   ciphertext itself is present.
2. **The backup takes a bounded write barrier that drains in-flight work, commits the checkpoint
   record, then establishes the cluster-wide recovery point that includes it — in that order, with
   the volume snapshot taken inside the same drained window, all under one barrier lease** (Main's
   calls below, replacing the journal design from round 1, the plain write-pause from round 2, and
   the impossible pre-checkpoint target confirmation from round 3). Every operation that writes the
   database and files together (the artifact commit-then-rename path is one instance:
   `runner/internal/server/artifacts.go:520-526` renames only after its DB row commits under the
   per-session lock in `runner/internal/db/artifacts.go:215-229`) holds a shared maintenance read
   lock for the duration of that coupled write. The backup barrier takes this lock exclusively: new
   coupled writes are refused from the moment the barrier is requested, and the barrier waits for
   every writer already in flight — including DB-only writers and direct worker file writes, not
   only Runner-mediated artifact writes — to finish and release, up to a bounded drain timeout. A
   drain that cannot finish inside the timeout fails that backup, never the install (failure rule),
   and releases the lock. Once draining completes and application state is stable, the order is:
   commit the checkpoint record — a uniquely identified database transaction naming the barrier and
   the security ledger's current head (rule 5) — then establish the one shared cluster-wide
   Postgres recovery point and confirm it includes that checkpoint transaction; the copy-on-write
   volume snapshot is taken inside this same drained window, with application state stable
   throughout, and does not need to wait for the recovery-point confirmation to start. A recovery
   point that cannot be confirmed to include the checkpoint transaction is rejected the same as a
   missing checkpoint (rule 1). **The barrier is itself a lease with its own expiry**, separate from
   the install-maintenance lease (rule 4): ordinary writers resume automatically once the barrier
   lease expires, even if the backup Job that requested it died without releasing it, so a stuck or
   killed backup Job cannot block ordinary writes past the barrier's own bounded lifetime — and a
   backup whose barrier lease expires before the recovery point and manifest are confirmed and
   finalized is abandoned, never finalized after writers resume, even if the Job's longer
   maintenance lease (rule 4) has not itself expired.
3. **Storage without snapshot support has no backup in 0.7.0.** Backups require volume and
   database storage that can take a consistent copy-on-write (or equivalent atomic) snapshot at the
   barrier; preflight checks this capability and refuses to configure backups where it is absent,
   naming the unsupported storage class or provider. Install verify shows whether backups are
   configured and capable, not just whether a backup job is scheduled.
4. **Backup, restore, upgrade, and key rotation share one fenced install-maintenance lease, and
   every Job holding it self-fences the way N6's daemon executors do** (`0.7.0-build-plan.md` N6:
   renew on a short interval, self-stop when renewal fails; Main's calls below): a single row in
   Core names the holder, operation kind, a generation, and an expiry. The lease fences, it does
   not only exclude: every write or publish the holding operation makes carries its lease
   generation, and Core refuses that write once a newer generation holds the lease. Each operation
   runs only inside a Kubernetes Job — never a freestanding long-lived process — and that Job
   renews its lease every few seconds, stopping every further write the moment a renewal fails,
   rather than waiting to be killed. The lease's expiry is set longer than the Job's self-fence
   window plus a safety margin, so a healthy Job always notices a failed renewal and stops itself
   before the lease could be reassigned; `activeDeadlineSeconds`, shorter than the lease expiry,
   remains a cluster-side backstop on the Job's lifetime, not the primary fencing mechanism. Each
   destructive external step the Job takes (a volume write, an object-store write, a direct
   database restore) rechecks the lease and generation immediately before executing that step, not
   only once at the Job's start. A new destructive holder is refused to start until the lease it
   would take has fully expired — never merely because the old Job's API object was deleted or its
   deadline passed, since neither is proof the old process actually stopped. **Residual risk,
   stated plainly:** a Job on a network partition that can still reach storage or the database but
   not Core can keep writing for up to its self-fence window after a failed renewal should have
   stopped it, bounded by that window and the lease expiry, not eliminated by them. A completed
   backup becomes usable only after its own generation-checked finalization commits; an
   in-progress backup that loses its lease is abandoned, not partially published. Routine backups
   do not freeze admission (`managed-install.md` call 1); restore and upgrade do, under rules 7
   and 12. Restoring Core's own database necessarily rolls back its lease row; the recovery runbook
   treats any lease row a restore brings back as expired and reconciles the lease table before any
   ordinary maintenance operation is allowed to trust it.
5. **The security ledger is write-ahead: appended, with a contiguous sequence number and a unique
   ID, before the matching Core transaction commits, and Core's own record references that ledger
   ID** (Main's call below). It is a small, append-only store beside the backups, under its own
   storage prefix, independent of the database backup and never rolled back by restoring one — and
   its own credentials, access, and retention are published separately from, and never pruned or
   replaced by, an ordinary backup's retention policy. It records every security-relevant change:
   account disable, grant removal, password change, factor reset or removal, and
   sign-out-everywhere. On restore, every ledger event with a sequence number after the backup's
   checkpoint is replayed — including an event whose Core commit never actually happened, because
   the write-ahead append landed and the process then crashed before committing. Replaying such an
   event is safe only because replay is deny-only and monotonic (rule 6): an event that never took
   effect in Core can, at most, further restrict access on replay, never grant anything that wasn't
   otherwise there. Sequence numbers are contiguous; a gap, or any entry after the checkpoint that
   is missing, corrupted, or fails its signature, leaves the restore inert until an admin has
   reviewed every member by hand — the same fallback as an unreadable ledger.
6. **Ledger replay is deny-only and monotonic: it only ever removes access, never grants or
   restores it** (Main's call below). Each event kind has one fixed, one-directional recovery
   effect: a disable event means the restored account is disabled, full stop. A password-change
   event invalidates the restored account's old password hash and requires an approved password
   reset before any sign-in; replay never stores or re-applies the new password material itself,
   so the ledger never carries plaintext passwords, recovery codes, TOTP seeds, or live tokens. A
   factor-reset or factor-removal event clears that factor and puts the restored account into a
   **recovery-required state that blocks sign-in outright**, not merely a step-up prompt, until an
   approved recovery clears it: N3's admin factor-reset flow (with its own step-up) or install-admin
   break-glass, never an ordinary password-only step-up or self-service re-enrollment. The old
   password alone never re-enrolls the factor or lifts this block, even when no separate
   password-change event also invalidated that password — N3 rule 9 defines ordinary step-up as
   password plus the factor "if present", so clearing the factor by replay must not quietly reduce
   that to password-only step-up for whoever holds the old password. A grant-removal
   event removes the grant; replay never recreates it. A sign-out-everywhere event revokes; replay
   never lowers an existing revocation epoch or authority cutoff, and never lets an older recorded
   sign-out supersede a newer one already present in the restored state — the merge always takes
   the more restrictive of the two. An account created after the checkpoint, or a token or grant
   issued after it, is covered by this note's ordinary revoke-and-reconcile rules (rule 7), not by
   ledger replay, which only ever subtracts access.
7. **A restore starts inert and stays inert until every authority is reconciled and acknowledged.**
   No admission, refresh, crons, email, provider calls, or daemon reconnection runs until
   reconciliation finishes. Reconciliation: (a) ordinary human/agent/daemon session and token
   authority is revoked through the same shared `revocations` table and poll N3 rule 8 uses for
   Core/Runner/Board — restore does not keep a second, divergent revocation path for the cases N3
   already owns; (b) the security ledger (rules 5–6) is replayed, deny-only, on top of the restored
   state; (c) the live-data authority keeps its own independent fencing (rule 11), never folded
   into (a) or (b); (d) every serving verifier — each running Board and Runner process, including
   one holding a cached pre-restore view (`runner/internal/coreauth/coreauth.go:156-173`) — must
   fetch and acknowledge the post-restore revocation/epoch state before serving another request.
   Admission reopens only once (a)–(d) are all done, not merely once the database rows are
   written. N3's normal revocation-propagation bound is not permission to expose stale authority at
   a restore cutover.
8. **Restore invalidates every pre-restore single-use link authority**, regardless of whether its
   outbox entry is pending, sent, or terminal, and regardless of whether its payload still exists:
   password reset, email verification/change confirm, email change cancel **and approve** (both
   sides of an email-change link, not only confirm/cancel), install invite, bootstrap setup link,
   and break-glass link. For a pending email change specifically, restore cancels the whole
   operation and its scheduled application, not just the individual links, so N3's
   pending-email-change sweep never applies a restored stale change. N2's own cleanup erasing a
   sent payload does not make the underlying record (`core/internal/identity/people.go:92,111,132`,
   the same shape `person_invites` uses) safe to leave redeemable — the verification authority is
   separate from the outbox, and payload cleanup after invalidation still follows N2. This runs
   before any redemption or send endpoint reopens. It applies to every restore, including an
   incompatible-upgrade rollback (rule 13), not only disaster recovery. Admin-visible nonsecret
   status (sent, redeemed, canceled, expired) is kept; secret link contents are never reconstructed.
9. **Restore drills run fully quarantined regardless of what the restored data contains.** The
   populated, isolated drill (`managed-install.md` "A drill proves it") disables outbound network
   and provider calls, the same inertness rule 7 requires for a real restore, whatever credentials
   the restored data actually holds. Isolation never depends on classifying a credential as "test":
   a drill restored from real encrypted data can contain production ciphertext even when its own
   configuration names only test identities, and nothing decrypts or prints a credential merely to
   classify it. The drill replays into a fresh, isolated namespace with explicitly mapped volume
   identities (never the production PVCs) and allowlists only what it needs to prove (database
   references, file existence, session resume); production SMTP, provider refresh/calls,
   object-store writes, daemon reconnect, and any live production database or volume target stay
   blocked whether the attempt happens before or after the drill reaches its own "ready" state.
10. **Restore drill config pointing at a real provider credential is refused before the drill
    runs**, in addition to the network quarantine in rule 9 — belt and suspenders, since neither
    depends on the other catching every case.
11. **Live-data authority keeps its own restore fence; neither ordinary revocation nor the
    security ledger substitutes for it.** Its existing epoch/high-water fencing
    (`core/internal/livedata/revocation_checkpoint.go:22-55,70-75`,
    `core/cmd/blerg-core/live_data_issuer.go:53-74`) already treats "every restore" as a case
    needing a newer fenced epoch before any approval is issued again — this note generalizes that
    existing behavior rather than replacing it. `RevokeAccountEverywhereTx`
    (`core/internal/identity/human_session.go:793-815`) covers session/agent authority; restore
    must drive all three mechanisms (ordinary revocation, the security ledger, and live-data's own
    fence), not use one in place of another.
12. **Upgrade is one ordered operation that fences every writer and background side effect, not
    only new team operations.** Freeze admission (no new sessions or workers) and fence running
    Jobs, connected daemons, migrations-on-open (`core/internal/db/postgres.go:11-18`), crons,
    refresh, and live-data startup (`core/cmd/blerg-core/live_data_issuer.go:69-77`). Take one
    coordinated recovery point under the shared lease (rule 4, the same checkpoint mechanism as
    rules 1–3), record the old release and image digests, upgrade, wait for migrations and the
    version record (rule 14) to confirm compatibility, verify, then reopen
    (`managed-install.md` call 1). A step that fails leaves admission frozen and every fenced writer
    fenced; it never reopens on a partial result.
13. **There is no automatic rollback across an incompatible schema migration — full stop, not only
    for the destructive case.** An incompatible migration or a failed verification stops and fences
    the services, keeps admission frozen, and surfaces an explicit compatibility/data-loss
    assessment to the person. Nothing restores data, keys, or files, and nothing reinstates the old
    release, until the person approves a stated recovery and what it loses — which, once approved,
    is a restore and follows rules 1–3 and 5–11 exactly. Every incompatible-migration recovery is
    person-approved; this is deliberately not "automatic unless it looks destructive."
14. **Rollout is version-gated by a generation- and bundle-bound install version record** (Main's
    call below). The record is invalidated before any migration in a new attempt starts, and is
    written only from each service's current, authoritative report of its actually-applied schema
    for the live lease generation — a stale completion from an earlier abandoned attempt, or a
    delayed worker reporting under an old generation, is rejected, never accepted as current. Until
    the record is current for the live generation, every writer stays fenced (rule 12), not only
    team operations. An unknown or newer-than-expected schema on any service fails closed rather
    than being read as compatible. N4 may read this record to decide activation but never creates
    grants or team activation from the record's mere presence.
15. **Restored volume claims are rebound or refused explicitly**, never silently mismatched
    (`managed-install.md` call 2). A PVC that cannot be bound to the restored data stops the restore
    with the mismatch named; a restore drill's volume mapping into its isolated namespace follows
    the same explicit-mapping rule (rule 9).

## Test matrix

| Situation | Expected result | Layer | Slice |
|---|---|---|---|
| Backup under write load, normal path | Barrier takes the shared maintenance lock exclusively, drains in-flight coupled writes, checkpoint record commits, cluster-wide recovery point established and confirmed to include it, COW snapshot taken inside the drained window, lock releases; restore into an isolated namespace matches | 4 (e2e, restore drill) | 1.18 |
| Storage class has no snapshot support | Preflight refuses to configure backups, names the unsupported storage; install verify shows backups unconfigured | 2 (impl) | 1.18 |
| An artifact write commits its DB row, then pauses before its rename, exactly as the barrier is requested | Barrier holds the shared maintenance lock and waits for this in-flight coupled write (including the rename) to finish before snapshotting | 3b (integration, real Runner artifact path) | 1.18 |
| A DB-only writer (no paired file write) is in flight when the barrier is requested | Drained by the same shared lock; not assumed to be outside the barrier's scope | 3b (integration) | 1.18 |
| A direct worker file write is in flight when the barrier is requested | Paused at the same boundary as Runner-mediated writes, not only instrumented paths | 3b (integration) | 1.18 |
| Drain exceeds its bounded timeout | Backup fails and releases the lock; install stays available (failure rule); ordinary writers resume | 3b (integration) | 1.18 |
| Checkpoint's own transaction is checked against the cluster-wide recovery point | Checkpoint record commits first; the recovery point is established afterward and confirmed to include that transaction; a target that fails to include it is rejected | 3b (integration, real Postgres) | 1.18 |
| Volume snapshot's timing relative to the checkpoint commit | Taken inside the same drained barrier window; may start before or after recovery-point confirmation, with application state stable throughout | 3b (integration, real volumes) | 1.18 |
| Backup Job dies after the checkpoint transaction commits but before the recovery point/manifest is confirmed and finalized | Backup is abandoned, never finalized, even if the Job's maintenance lease (rule 4) has not itself expired | 4 (e2e, simulated Job death) | 1.18 |
| Cluster recovery point cannot be established at the barrier (provider refuses, times out) | Backup stops, names the failure; writers still resume, ordinary work continues | 3b (integration, real Postgres) | 1.18 |
| Checkpoint record missing from a backup | Restore stops before writing anything, names the missing record | 2 (impl) | 1.18 |
| Backup requested while a key rotation holds the lease | Backup is refused, names the rotation | 3b (integration, real Core database) | 1.18 |
| Key rotation requested while a backup holds the lease | Rotation is refused, names the backup | 3b (integration) | 1.18 |
| A paused backup's lease expires, rotation takes the next generation, the paused backup resumes and tries to write | Write is refused: it carries the old generation, which is no longer current | 3b (integration, real Core database) | 1.18 |
| A maintenance Job's lease renewal fails | Job stops every write immediately, before being killed, rather than continuing until `activeDeadlineSeconds` or an external kill | 3b (integration, real cluster) | 1.18 |
| A maintenance Job is on a network partition that still reaches storage/the database but not Core | Its write window is bounded by the self-fence interval and lease expiry, as rule 4 states plainly; a new destructive holder still waits out the full lease expiry | 4 (e2e, simulated partition) | 1.18 |
| Old Job's Kubernetes object is deleted, or its `activeDeadlineSeconds` passes, but its process may still be running | Not treated as proof of termination; a new destructive holder still waits for the lease's own expiry | 2 (impl, policy check) | 1.18 |
| Core's own database, holding the maintenance lease table, is itself restored | Any lease row the restore brings back is treated as expired; the lease table is reconciled before any ordinary maintenance operation trusts it | 3b (integration, real Core) | 1.18 |
| Maintenance Job process is suspended (not killed) past its configured renewal interval, then resumes | Checks lease age against the configured margin before issuing another write, not only at its next scheduled renewal | 3b (integration) | 1.18 |
| Restored, stale lease generation collides with a fresh holder's current generation | Fresh holder's generation wins; the stale restored generation is refused | 3b (integration) | 1.18 |
| Configured renewal interval, self-fence window, and lease margin values | Implementation asserts against the actual configured values, including canceling in-flight subprocesses/requests as part of self-fencing, not a placeholder "a few seconds" | 2 (impl) | 1.18 |
| Backup Job dies mid-drain, before the barrier lease expires | Ordinary writers stay paused only until the barrier lease's own expiry, not indefinitely; no second Job is needed to release it | 4 (e2e, simulated Job death) | 1.18 |
| Backup Job dies between snapshot creation and recovery-point confirmation | Barrier lease still expires on schedule; backup is abandoned/unpublished; writers resume automatically | 4 (e2e, simulated Job death) | 1.18 |
| Backup Job dies after capture completes but before releasing the barrier | Barrier lease expiry releases writers without waiting for the dead Job; no stale capture actor can still mutate live state once resumed | 4 (e2e, simulated Job death) | 1.18 |
| Two restore commands issued for the same install | Only the one holding the fenced lease proceeds; the other is refused | 3b (integration) | 1.18 |
| Crash between a maintenance operation's mutation and its finalization | Resumed or retried operation is fenced by generation; nothing half-applied is published as done | 4 (e2e, simulated crash) | 1.18 |
| Core commits a disable, then crashes before the matching ledger append lands | Impossible by construction: the ledger append is write-ahead and durable before Core's transaction can commit | 3b (integration, real Core + ledger) | 1.18 |
| Ledger append succeeds, then the matching Core transaction never commits (crash) | Replay still applies the ledger event on restore; safe because replay is deny-only (rule 6) | 3b (integration) | 1.18 |
| Ledger entries are produced by concurrent writers or exported with delay | Sequence numbers are assigned at append time and contiguous; replay follows sequence order, never arrival order | 3b (integration) | 1.18 |
| Ledger's recorded tail after the checkpoint is truncated, corrupted, or missing (not merely a gap before the checkpoint) | Treated the same as a gap; restore stays inert until an admin reviews every member | 2 (impl) | 1.18 |
| Routine backup retention pruning runs against the security ledger's storage prefix | Ledger history a pending restore still needs is never deleted or replaced by ordinary backup retention | 3b (integration) | 1.18 |
| A ledger event is replayed twice (duplicate replay after a restart) | Idempotent; replaying the same event again does not change the outcome | 2 (impl) | 1.18 |
| Entire final suffix of the ledger after the checkpoint is truncated, not just one entry (each remaining event is still internally well-formed and correctly signed) | Checked against a durable authenticated head, not inferred from the remaining prefix looking contiguous; treated as incomplete, restore stays inert for review | 3b (integration) | 1.18 |
| Ledger append is requested after the barrier/checkpoint is acquired, with its Core commit still pending | Checkpoint commit ordering (rule 2) and the ledger-head binding (rule 5) keep this event from being silently excluded from the checkpoint's security history | 3b (integration) | 1.18 |
| Ledger cannot be appended to at the moment of a security mutation | Core's transaction cannot commit without the write-ahead append landing first (rule 5); the mutation itself fails rather than completing unlogged | 3b (integration, real ledger store) | 1.18 |
| Restored account's old password was compromised before the checkpoint; a later password-change event exists | Replay invalidates the old hash and requires an approved reset; the compromised old password cannot sign in after restore | 3b (integration) | 1.18 |
| Password change and factor removal both happened after the checkpoint, in either order | Both replay to their deny-only effect regardless of order: old hash invalidated and reset required, factor cleared and fresh enrollment required | 3b (integration) | 1.18 |
| An old sign-out-everywhere event is replayed after the restore's own broader revocation already set a newer cutoff | Merge takes the more restrictive of the two; the older event never lowers the cutoff | 2 (impl, unit on the merge) | 1.18 |
| Factor-removal/reset event replays on a restored account whose old password is still otherwise valid (no separate password-change event) | Account enters a recovery-required state; sign-in is blocked outright, not just a step-up prompt | 3b (integration) | 1.18 |
| Someone holding the restored old password alone attempts ordinary step-up after a factor-removal replay | Refused: N3's ordinary step-up needs the factor, which is absent and blocked, not reduced to password-only | 3b (integration, real N3 step-up path) | 1.18 |
| Someone holding the restored old password alone attempts self-enrollment of a new factor after a factor-removal replay | Refused; only N3's admin factor-reset flow (with its own step-up) or install-admin break-glass clears the recovery-required state | 3b (integration) | 1.18 |
| An approved recovery (N3 admin factor-reset, or install-admin break-glass) runs against a recovery-required restored account | Clears the block; ordinary use resumes only after that approved path completes | 4 (e2e) | 1.18 |
| Ledger event references an account ID created after the checkpoint, or unknown to the restored snapshot | Rejected or deferred to ordinary reconciliation (rule 7), never silently dropped or misapplied | 2 (impl) | 1.18 |
| A disable event is followed by a later, stale-looking event for the same account | Disable's deny-only effect is never reversed by a subsequent replay | 2 (impl) | 1.18 |
| A member is disabled, or a grant is removed, after the backup's checkpoint, then restore runs | Security ledger replay re-applies the disable/removal on top of the restored state, deny-only | 3b (integration, real Core) | 1.18 |
| An account is created after the checkpoint and holds a live externally-issued token | Ordinary reconciliation (rule 7), not ledger replay, accounts for it; never silently re-admitted on stale state | 3b (integration) | 1.18 |
| Security ledger is unreadable, missing, or has a gap before the checkpoint at restore time | Restore stays inert; admission does not reopen until an admin reviews every member by hand | 4 (e2e) | 1.18 |
| Restore starts | No admission, refresh, cron, email, provider call, or daemon reconnection until reconciliation finishes | 3b (integration, real Core/Runner/Board) | 1.18 |
| Restore reconciliation, ordinary session/token authority | Revoked through the same `revocations` table/poll as N3 rule 8; no second path | 3b (integration) | 1.18 |
| Restore reconciliation, live-data authority | Driven by live-data's own epoch/high-water fence (rule 11), not the ordinary-revocation or ledger path | 3b (integration, real livedata package) | 1.18 |
| A Board or Runner process holds a cached pre-restore authority view when restore finishes writing | Admission stays closed until that process fetches and acknowledges the post-restore state | 3b (integration, real Board/Runner) | 1.18 |
| An unused password-reset or invite link was sent before the backup point | After restore, redeeming it is refused even though its outbox entry was already terminal | 3b (integration, real Core) | 1.18 |
| An invite/reset link was used after the backup point, then restore runs | Restoring the unused record does not make the already-used link redeemable again | 3b (integration) | 1.18 |
| An email-change approve link (old-address side) is outstanding at backup time | Invalidated on restore the same as the confirm/cancel links | 3b (integration) | 1.18 |
| A pending email change (any of its links) exists at backup time, restore runs, then N3's sweep fires | Whole pending-change operation is canceled; sweep does not apply it | 3b (integration) | 1.18 |
| Restore runs, then a pre-restore link is POSTed | Refused | 3b (integration) | 1.18 |
| Incompatible-upgrade rollback (rule 13) restores pre-upgrade link authorities | Same invalidation as a disaster-recovery restore, not skipped because it's a rollback | 4 (e2e) | 1.19 |
| Restore drill is configured with test identities, but the restored data contains production ciphertext | Drill stays quarantined (no egress, no provider calls) regardless of the configured identity names | 4 (e2e, restore drill) | 1.18 |
| Restore drill's target endpoints are misconfigured to point at a live resource | Blocked, not merely discouraged by naming convention | 4 (e2e, restore drill) | 1.18 |
| Egress or provider-call attempt from inside a drill, before or after it reaches "ready" | Blocked in both cases | 4 (e2e, restore drill) | 1.18 |
| Restore drill replays into its isolated namespace | Volume identities are explicitly mapped; production PVCs are never targeted | 3b (integration, real cluster) | 1.18 |
| Restore drill config points at a real provider credential | Refused before the drill runs, in addition to the network block above | 2 (impl) | 1.18 |
| One piece of the whole-install set (e.g. workspace volumes) cannot be captured | Backup names that piece and either states it lost explicitly or stops; never silently partial | 3b (integration) | 1.18 |
| Backup manifest is missing, truncated, or fails its checksum | Restore refuses before touching any live resource | 2 (impl) | 1.18 |
| A backup data part (not the manifest) is corrupted | Restore refuses before touching any live resource, names the part | 2 (impl) | 1.18 |
| A backup archive entry uses an absolute path or a symlink that escapes the extraction root | Rejected during staging; never followed | 2 (impl) | 1.18 |
| Backup references a decryption key that is missing or unavailable (including an unreachable external key backend), ciphertext otherwise intact | Not treated as a usable recovery set, even though bytes are present | 2 (impl) | 1.18 |
| Staging a restored DB into an isolated namespace to inspect its checkpoint, before any destructive replacement | Allowed; distinct from the destructive live-replacement step, which still waits for full authentication and staging to pass | 3b (integration, real cluster) | 1.18 |
| Backup's recorded volume/backup identity does not match the target install | Restore refuses rather than binding to the wrong volume | 3b (integration, real cluster) | 1.18 |
| Restore attempted outside the documented recovery path (e.g. a role without restore rights) | Refused | 2 (impl) | 1.18 |
| A restore completes | Claude seat-host state is not present; the person signs in again, as `0.7.0-build-plan.md` call 1 states | 4 (e2e) | 1.18 |
| A PVC cannot be rebound to restored data | Restore stops, names the mismatch | 3b (integration, real cluster) | 1.18 |
| Upgrade: schema change is backward-compatible | Freeze, checkpoint, upgrade, migrate, verify, reopen; no rollback needed | 4 (e2e) | 1.19 |
| Upgrade: schema change is incompatible, verification fails | Services stay stopped and fenced, admission stays frozen, the person sees an explicit compatibility/data-loss assessment; no restore or reinstatement runs until the person approves | 4 (e2e) | 1.19 |
| Upgrade: incompatible migration, person has not yet responded | Services remain stopped and fenced; nothing proceeds while approval is outstanding | 4 (e2e) | 1.19 |
| Upgrade: incompatible migration, person declines | No restore runs; install stays stopped for manual handling | 4 (e2e) | 1.19 |
| Upgrade: compatibility assessment itself is uncertain (cannot classify the migration) | Treated as incompatible — stop and ask, never assumed safe | 2 (impl) | 1.19 |
| Upgrade: migration step fails partway through, before compatibility is known | Admission stays frozen; treated as the incompatible/unknown case, not auto-retried | 3b (integration) | 1.19 |
| Upgrade: verification fails again after an approved restore | Stops again and surfaces to the person; does not loop automatically | 4 (e2e) | 1.19 |
| Upgrade: a Job or daemon still on the old version after cutover | Fenced from writing against the new schema | 3b (integration) | 1.19 |
| Upgrade: a cron, refresh cycle, or live-data startup fires mid-upgrade | Fenced the same as a Job or daemon, not just "new team operations" | 3b (integration) | 1.19 |
| Version record from an earlier, abandoned upgrade attempt is still present | Rejected as stale; not read as current for the live generation | 3b (integration, real Core) | 1.19 |
| A delayed worker reports schema completion under an old lease generation | Rejected; does not mark the record current | 3b (integration) | 1.19 |
| One service's migration commits, a second service's fails | Version record is not written; writers stay fenced | 3b (integration) | 1.19 |
| Process restarts between upgrade stages (freeze, checkpoint, migrate, verify, reopen) | Resumes from, or stops for recovery at, the recorded stage per the failure rule | 4 (e2e, simulated crash) | 1.19 |
| A service reports an unexpected, newer-than-bundle schema version | Fails closed, not read as compatible | 2 (impl) | 1.19 |
| N4 checks activation after migrations finish | N4 reads the same install version record this note's upgrade writes, and never activates from the record's presence alone | 3b (integration, Core + N4's activation check) | 1.19 |
| Upgrade requested while a key rotation holds the lease | Upgrade is refused, names the rotation | 3b (integration) | 1.19 |
| Key rotation requested while an upgrade holds the lease | Rotation is refused, names the upgrade | 3b (integration) | 1.19 |
| Ordinary install verify (every install, `0.7.0-build-plan.md` call 11) | Passes with a database restore point plus a file snapshot present | 4 (e2e) | 1.18 |
| Alpha-gate release check (`0.7.0-build-plan.md` call 11) | Additionally requires the checkpoint record and the populated isolated restore drill to pass — not satisfied by install verify alone | 4 (e2e, release gate) | 1.18 |
| Backup upload or finalization fails partway | Recorded stage is durable; resume or manual recovery from that stage; no partial backup advertised as complete; install itself stays available (failure rule) | 4 (e2e, simulated failure) | 1.18 |
| Routine backup fails or times out (e.g. object-store quota exhausted) | Only that backup run fails/unpublished; its lease and resources clean up; ordinary work continues uninterrupted | 3b (integration) | 1.18 |
| Operator cancels a routine backup mid-operation | Lease is released; backup is unpublished; install was never made inert and stays available throughout | 4 (e2e) | 1.18 |
| Operator cancels a restore or upgrade mid-operation | Lease is released or expires; recorded stage reflects the abort; install stays inert/fenced at that stage (restore and upgrade only) | 4 (e2e) | 1.18 |
| Restore of an in-cluster Postgres backup (physical backup, one point for all three DBs) | Same barrier/drain/snapshot/recovery-point rules apply as managed Postgres | 3b (integration, in-cluster Postgres) | 1.18 |

## Decisions from Main (2026-11-09)

1. **One shared install-maintenance lease**, not pairwise locks: a single row in Core's database
   naming the holder, the operation kind (backup, restore, upgrade, rotation), a generation, and an
   expiry. A second operation is refused, named by the current holder. This is the lease rules 4,
   12, and 14 rely on.
2. **One migration-completion signal.** An install version record in Core names each service's
   applied schema version and is written only after all three migrations finish. Rule 14's version
   gate and N4's "upgrade activates nothing" both read this one record; neither keeps a second
   signal.
3. **The lease is fenced, not just mutually exclusive.** Every write an operation makes carries its
   lease generation and is refused if a newer generation now holds the lease, so a paused holder
   that resumes after a takeover cannot act (round 1 finding 5; folded into rule 4).
4. **No automatic restore, ever.** An incompatible migration or failed verification stops and asks
   the person, exactly as rule 13 already states; the test matrix's "automatic rollback" row was
   wrong and is replaced (round 1 finding 6; folded into rule 13 and the matrix).
5. **Restore drills run with outbound network and provider calls disabled**, inert as for a real
   restore, whatever credentials the restored data contains — isolation never rests on how the
   drill's own configuration happens to be labeled (round 1 finding 8; folded into rule 9).
6. **Backups use a write-barrier-plus-snapshot capture, not a journal.** A short coordinated writer
   pause, copy-on-write volume snapshots, and one cluster-wide database recovery point, all at the
   same barrier, replace the undo/redo journal design from round 1 (round 2 findings 1 and 2;
   folded into rules 2–3). Storage without snapshot support has no backup in 0.7.0.
7. **Maintenance operations run only as deadline-bound Kubernetes Jobs, with a lease recheck before
   each destructive external step**, so a paused holder is killed by the cluster before its lease
   can pass to a new generation (round 2 finding 3; folded into rule 4). The residual half-applied-
   write window is stated plainly in that rule, not eliminated.
8. **A small append-only security ledger, beside the backups under its own prefix, records
   disable/grant-removal/password-change/factor-reset/sign-out-everywhere and is replayed after a
   restore, before reopening.** An unreadable ledger keeps the restore inert until an admin reviews
   every member by hand (round 2 finding 4; folded into rules 5 and 7).
9. **Only a restore starts the install inert.** A failed or canceled routine backup never does
   (round 2 finding 7; folded into the failure rule and the matrix).
10. **The ledger is write-ahead and Core references its ID.** The event is appended, with a
    contiguous sequence number and a unique ID, before the matching Core transaction commits, and
    Core's own record references that ledger ID. Restore applies every ledger event after the
    checkpoint, including one whose Core commit never happened — safe because of decision 11.
    Sequence numbers are contiguous; a gap leaves the restore inert until an admin review (round 3
    finding 1; folded into rule 5).
11. **Replay is deny-only and monotonic.** Disable means disabled. A password change means the
    restored account must reset its password (the old hash is invalidated). A factor removal or
    reset means fresh enrollment is required. A grant removal removes the grant. Sign-out
    everywhere revokes. Replay never adds or restores access (round 3 finding 2; folded into rule
    6, new in this round).
12. **The barrier drains in-flight work.** Each operation that writes the database and files
    together holds a shared maintenance read lock for its duration; the barrier takes it
    exclusively and waits for in-flight holders, with a bounded drain timeout. A timeout fails the
    backup, never the install (round 3 finding 3; folded into rule 2).
13. **Every maintenance Job self-fences like N6 executors**: it renews its lease every few seconds
    and stops all writes when renewal fails; lease expiry is longer than the self-fence window plus
    a margin. The residual partition risk — a partitioned Job can keep writing to storage/DB,
    though not Core, for up to its self-fence window after a renewal should have stopped it — is
    stated plainly, not eliminated (round 3 finding 4; folded into rule 4).
14. **The barrier is itself a lease with an expiry; writers resume when it expires, even if the Job
    died** (round 3 finding 5 / medium finding; folded into rule 2).
15. **Checkpoint order is fixed: commit the checkpoint record first, then establish the
    cluster-wide recovery target at or after that commit and confirm it includes the checkpoint
    transaction.** Volume snapshots are taken inside the same drained barrier (round 4 finding 1;
    folded into rule 2).
16. **Factor-removal/reset replay blocks sign-in outright, in a recovery-required state.** Only an
    approved recovery clears it — N3's admin factor-reset flow with step-up, or install-admin
    break-glass. The old password alone never re-enrolls a factor (round 4 finding 2; folded into
    rule 6).

## Review round 1 (Codex, 2026-11-09)

An adversarial review found 8 high and 2 medium findings, all folded into the rules and test
matrix of that round: journal replay needed explicit undo/redo semantics instead of discarding
post-checkpoint entries (1); the checkpoint's journal sequence and the database restore point
needed a durable, confirmed, cross-database frontier instead of an allocated sequence number (2);
restore left pre-restore invite/reset/email links redeemable because it tracked only outbox state,
not link authority itself (3); ordinary account revocation did not cover live-data's independent
epoch fence or force running Board/Runner verifiers to acknowledge fresh state before reopening
admission (4); the maintenance lease excluded concurrent holders but did not fence a paused
holder's later writes under a stale generation (5, Main's call 3 above); the test matrix's
"automatic rollback" row contradicted the rule it was supposed to test (6, Main's call 4 above);
the version-gate record and "team operations" fencing were not bound to the live lease generation
and left background jobs unfenced (7); restore-drill isolation depended on credential
classification rather than unconditional network/provider quarantine (8, Main's call 5 above); the
backup manifest and key/volume inventory needed explicit binding and failure modes, including the
documented exclusion of Claude seat-host state (9); and the failure rule's "leaves the install in
its current state" overstated what a multi-service recovery can guarantee, and the single
"install verify" row conflated the ordinary gate with the stricter alpha gate (10). No
disagreements — all ten held up on inspection.

## Review round 2 (Codex, 2026-11-09)

A second adversarial pass found 4 high and 3 medium findings against the round-1 fix. The two most
consequential (1, 2) found the undo/redo journal round 1 adopted was still not a bounded,
buildable contract — unspecified replay order, unbounded image growth, and no real commit barrier
between a file mutation and its database transaction — and recommended a simpler capture mechanism
instead of a bigger journal subsystem; Main dropped the journal entirely in favor of a brief
writer pause plus copy-on-write snapshots and one cluster-wide recovery point (decision 6). Finding
3 found that fencing Core's own API does not stop a paused holder's direct writes to storage/object
targets/the database; Main added deadline-bound Jobs and a per-step lease recheck (decision 7).
Finding 4 found that revoking sessions does not preserve a security decision (disable, grant
removal) made after the backup's checkpoint; Main added the append-only security ledger and its
replay-or-stay-inert rule (decision 8). The three mediums — email-change approve-link and
whole-operation cancellation (5), corrupted backup parts/archive-traversal/external-key-backend
and explicit staged-inspection-before-replacement (6), and routine backup failure wrongly implying
whole-install inertness (7, Main's call 9 above) — are folded into rules 1, 8, 9, and the failure
rule, with matching matrix rows. No disagreements.

## Review round 3 (Codex, 2026-11-09)

A third, narrower pass closed most of round 2's contract-level items but found 4 high and 1 medium
remaining. Two attacked the round-2 ledger: appending to separate storage has no atomicity with
Core's own transaction, so a crash between the two could lose a security decision or misorder it
against the checkpoint (1); and even a reliable ledger needs fixed recovery semantics per event
kind, since naively replaying "password changed" or "factor removed" can resurrect old access
instead of restricting it (2). Main resolved both: the ledger is write-ahead with a contiguous
sequence number referenced from Core (decision 10), and replay is deny-only and monotonic per
event kind, never adding or restoring access (decision 11) — together these are why an event whose
Core commit never happened is still safe to replay. The other two attacked the round-2 barrier
mechanism: pausing only new file writes does not drain a write already in flight that commits its
DB row before renaming its file, so the snapshot and the recovery point could still disagree (3);
and killing a Job's API object is not proof the underlying process stopped writing to shared
storage (4). Main added barrier draining of in-flight coupled writers under a shared lock
(decision 12) and Job self-fencing modeled on N6's executors, with the residual partition risk
stated plainly rather than assumed away (decision 13). The medium finding — a dead backup Job
could otherwise hold the barrier open indefinitely — is resolved by making the barrier itself an
expiring lease (decision 14). All five folded into rules 2, 4, 5, 6, and 7, and the matching test
rows above. No disagreements.

## Review round 4 (Codex, 2026-11-09)

A fourth, narrow pass found round 3's closures held except for two highs. The checkpoint-ordering
rule had the sequence backward: it required the cluster-wide recovery point to include the
checkpoint transaction before that transaction was written, which no builder can implement — a
target fixed before a transaction exists cannot contain it. Main fixed the order: commit the
checkpoint record first (naming the barrier and the security ledger's current head), then
establish the recovery target and confirm it includes that transaction, with the volume snapshot
taken inside the same drained window (decision 15, folded into rule 2). Separately, round 3's
deny-only factor-removal replay still allowed an unsafe path: clearing the factor while the old
password stays valid lets N3's own "password plus factor if present" step-up rule degrade to
password-only, so someone holding only the old password could step up and self-enroll a new
factor. Main closed this by making factor-removal/reset replay put the account into a
recovery-required state that blocks sign-in outright until an approved recovery — N3's admin
factor-reset flow or install-admin break-glass — clears it; the old password alone never lifts the
block (decision 16, folded into rule 6). The two medium findings — making ledger tail-completeness
and checkpoint/ledger-head binding concretely testable, and exercising actual configured
renewal/self-fence/margin values plus suspended-process and stale-generation cases rather than
accepting the policy's prose as its own proof — are folded into rules 2, 4, and 5 as matrix rows.
No disagreements.
