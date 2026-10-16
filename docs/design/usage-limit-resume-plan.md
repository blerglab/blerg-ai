# Usage-limit continuation implementation plan

1. Add normalized `usage_limit` agent events containing provider, optional UTC reset
   and turn ID. Claude uses its existing refusal parser. Codex recognizes usage
   refusal error events and explicit reset timestamps/text; unknown times remain
   unknown. Add sanitized fixtures and failing tests before implementation.
2. Add a durable per-session continuation table: enabled, owner browser-session
   proof, current/cancelled turn, reset, state/reason, attempts and attempt ID.
   The owner-only authenticated GET/PUT endpoint enables, disables and reads it;
   UI controls poll this endpoint and display unknown/blocked/pending states.
3. Consume persisted user-message and usage-limit events idempotently. Store turn
   identity and cancellation fences; preserve source event IDs. No old event
   replay may schedule another attempt. Explicit user actions cancel pending work
   before dispatch. Use the same Postgres advisory session lock for event updates,
   toggles, manual actions, claiming and dispatch to order concurrent changes.
4. Run a server worker every 15 seconds. Claim due work using durable conditional
   updates and transaction advisory locking; commit dispatch intent before a
   nonblocking send. Connected daemon/pod gets normal system continuation input.
   Offline desktop waits without consuming attempts. Disconnected cluster uses
   existing resume with stored verified owner proof and existing grant checks;
   preflight credential liveness and fail closed on missing proof. Uncertain
   dispatch is visible and never automatically resent. Three dispatched attempts
   maximum, at least one minute grace, global per-worker pacing plus owner/provider
   coordination across replicas. Do not dispatch into work, questions, restart
   pause, background work or a manually ended session.
5. Exempt pending resumable waits from inactivity expiry for at most eight days.
   Suppress one-shot auto-stop for a limited turn with an armed continuation.
6. Test normalization, DB transitions and races, worker authorization and delivery,
   API ownership, expiry and UI. Run Go tests/lint, web checks, scripts/scrub and links.
7. Adversarially review final diff, fix findings, commit, merge and push dev.
   Document desktop daemon rebuild/reinstall and cluster rollout behavior.

## Adversarial plan review and corrections

The review accepted retaining verified SID with core revalidation, but identified
an expiry race after preflight: automatic Job specs must forbid operator fallback
at the actual credential fetch as well. Hold a dedicated session advisory lock
through dispatch commit AND enqueue, with manual cancellation using the same lock.
Land the armed-refusal auto-stop guard before enabling the ingestion hook. Never
use a transaction advisory lock that is released before sending.

## Implementation review

Independent adversarial review of the implementation led to additional fixes:

- Keep the event ingestion gate closed until retry state is durable; replay can
  recover an event whose transcript insert succeeded before scheduling failed.
- Compare cancellation fences with original engine timestamps, before display-time
  clamping. Preserve turn IDs across Claude daemon recovery.
- Serialize manual operations through dispatch enqueue, report stop failures,
  and reserve separate short-lived database connections for advisory locks so
  callbacks cannot exhaust the normal query pool.
- Protect a running one-shot session when its setting is disabled, and fence a
  delayed old completion while a manual replacement message is being accepted.
- Preserve retry budgets across system adoption notes when a pod is replaced.
- Distinguish a sleeping workstation's revivable error status from a final end.
- Keep personal Codex credentials in strict replacement pods while excluding
  unrelated operator account credentials.

Each lifecycle correction has a database-backed regression test. Provider guard
and server scheduling tests also run with Go's race detector. Full frontend tests,
production build, installer/messaging checks, privacy scrub and link checks are
part of verification. The pinned Go linter reports no new findings; a clean
baseline checkout has 33 existing findings with the toolchain used here.

Codex's existing exec transport still queues mid-turn messages and does not
render collaboration events. This feature does not change that transport.
Codex refusals without an unambiguous reset remain manual. Codex desktop sessions
also retain their existing lack of daemon-restart adoption; ordinary websocket
reconnection and cluster pod replacement are supported.
