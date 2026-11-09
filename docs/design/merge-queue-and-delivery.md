# Merge queue and claim delivery (0.7.0)

Status: draft for owner review, 2026-11-09. Cards: BLG-480 (merge train), BLG-313 (merge
workflow), back in 0.7.0 by owner decision. Builds on `team-scope.md` (claims, claim generations,
the versions tuple) and `accounts-and-placement.md` (admission, runtimes).

## Why

Three people's Mains work on one project at once. Each worker's branch can pass its own tests, yet
two passing changes can break each other once combined. The owner rejects the GitHub
pull-request-and-merge flow with AI ("it creates so much noise"), and rejects people merging:
that is the "meat proxy" work Blerg exists to remove. Merging belongs to release management, and
Blerg does it.

## Goals

1. **No pull requests and no human merges** (owner). Blerg merges deterministically after gates
   and a cross-model review.
2. **One queue per target branch** (owner): the 0.7.0 release branch has its own queue; an
   experiment such as `0.6.0-alpha-build-extra-intelligence` has a separate one, so neither holds
   up the other. A release defines its branch; the branch gets its queue.
3. **A fixed item keeps its place near the front** (owner): on its first return it is requeued
   right after whatever is currently running its gates. If it comes back again, it rejoins in
   normal order, so it can't starve fresh work (call 7, owner approved).
4. **The queue is deterministic code in the runner**, not an AI session (owner's rule: static over
   LLM for gates and policy). It orders, merges in a scratch copy, runs gates as jobs, decides and
   merges. AI only appears when something fails: the claimer's Main fixes it.
5. **Visible to everyone on the project**: a state chip on each board card (queued 2nd, running
   gates, merged, sent back with the reason; glyphs with hovercards), a live **Merge queue** tab per
   project (order, who claimed it, gate progress, recent merges and send-backs), and each claimer's
   Main sees its own items in its context. Nothing goes to Needs you; a person hears only if Main
   gives up and asks.
6. **Claim delivery is explicit** (owner adopted the reviewed rule): a claim ends when the
   claimer's Main declares its delivery complete and Blerg verifies it; the team ticket then moves
   to the project's configured column.

## Non-goals for 0.7.0

- Atomic merges across several repositories (each repository's branch has its own queue).
- Automatic deploys from the queue.
- Pull requests as an interface (a team may still mirror to GitHub; Blerg doesn't drive review
  there).

## The queue

1. A claimer's Main submits work for a target branch when its own tests and the cross-model review
   pass. The review runs in the claimer's Main, before submission, on that person's own
   credentials (a different model from the builder's). The submission names the claim, its
   generation and the reviewed commits.
2. The queue takes items in order. For each: merge onto the current target branch in a scratch
   copy, run the project's gates on the combined result (as deterministic jobs on the install's
   compute, with no AI credential), then:
   - **Pass:** fast-forward the target branch to the tested merge, record a merge receipt, update
     the board chip, move on.
   - **Conflict or failure:** nothing lands; the item steps out (the queue keeps moving), and the
     claimer's Main gets a message with the conflicting files or failing gates and the change that
     caused it. Main fixes it on its own branch (pulling in the latest target, resolving, rerunning
     its tests, and repeating the cross-model review if code changed), then requeues; the item goes
     right after whatever is running, on its first return (call 7).
3. The merge is pushed with the claimer's own Git token (owner: personal Git tokens in 0.7.0), so
   project history shows who delivered it, and the queue holds no team Git credential. The token's
   reachability check and expiry handling are in `credential-handling.md` ("Personal Git
   tokens").
4. A change already on the target branch can't be merged twice: receipts are idempotent and keyed
   by the reviewed commits.

## Claim delivery

1. **Delivery record:** each claim keeps a record of its merge receipts (possibly several, across
   repositories), and for work with no code (an investigation, a decision, a document outside
   Git) an outcome with evidence the team can open.
2. **Declaring completion:** the claimer's Main runs one command (for example
   `blerg-runner claim complete --manifest <...>`) naming a reviewed **delivery manifest** (call 8).
   Blerg verifies, in code: the claim generation is current; the ticket's criteria version and the
   project's completion-rules version match what the review covered (team scope, round 2, call
   12); every merge the manifest lists landed through the queue; evidence for no-code work is
   stored on the team ticket (call 9). Merges before that count as progress, never completion.
   These checks are the project's **completion rules** that `team-scope.md` refers to.
3. **Then:** the claim ends, the personal copy closes, the lock releases, and the team ticket
   moves to the project's **after-delivery column**, a per-project setting with "To test" as the
   default (the team's definition of done). Teams may let automatic test or deploy evidence move
   tickets further, so nobody shuffles cards by hand; a person confirms only where the project
   wants judgment.
4. **Afterwards:** a revert or a failed deploy reopens the ticket: it returns to the project
   board unclaimed, flagged with the failure, and the next claim gets a new generation (call 11).
   The old delivery record stays as history.

## Decided by the owner (2026-11-09)

- No pull requests, no human merges; merging is release management, done by Blerg.
- The queue lives in the runner as deterministic code; gates run as jobs; AI only fixes failures.
- One queue per target branch; fixed items keep their place near the front (bounded to the first
  return, call 7, owner approved).
- Visibility on board cards, a live per-project Merge queue tab, and Main's context.
- Definition of done is the team's; the reviewed delivery rule above; reviewed again before build.

## Left to the builder

- Queue storage, ordering and leases (reuse the admission records from the accounts design).
- How the project's gate commands are declared.
- Chip glyphs and the tab layout (UX skill rules, 390 px to 4K).

## Review round 1 (2026-11-09): contracts added

An adversarial review (Codex, 16 findings, 9 high) found the draft named the right flow but left
its safety boundaries open. These are **Main's calls** (the owner may overrule); call 7 refines an
owner decision and is flagged for the owner.

1. **Only the exact tested result lands.** The queue serializes per repository and target ref,
   across projects. Each attempt records the expected base, the candidate commit and an attempt
   generation; the push updates the target only from the expected base to the exact tested
   candidate (compare-and-swap, never a force push). If the target moved, the candidate is rebuilt
   and the gates rerun.
2. **Uncertain pushes are reconciled, not retried blindly.** Submissions and attempts have separate
   durable IDs. After a crash or timeout, the queue checks whether the exact candidate is already
   on the remote before retrying; landed receipts survive restarts.
3. **Authority is live.** Submitting, requeuing and cancelling require the current claimer's Main,
   workspace, project, repository and claim generation. Authority is rechecked before each gate
   job and before the push; revocation cancels affected work (team scope, decision 1); if
   authority can't be checked, the queue fails closed. The personal Git token is used only by the
   trusted push step, never inside gate jobs. An expired token or missing push rights sends the
   item back with that reason before gates run (a preflight).
4. **Gate jobs are untrusted and credential-free.** Gates run in disposable sandboxed jobs with no
   Git or AI credentials, no controller tokens, no privileged mounts and no cluster API access,
   with network limits and cloud metadata denied. Fetching the candidate, reporting results and
   pushing are done by trusted steps outside the job.
5. **Evidence is bound.** Each merge records the gate-policy version, the gate configuration's
   digest, the reviewed revision and an authenticated cross-model review verdict from a model
   different from the builder's, per the project's policy. The review runs in the claimer's Main on
   the claimer's own credentials. If the claimer has no credential for a different model, the item
   waits and Main tells its person; review is never silently waived. A base update that changes code the review
   covered requires another review.
6. **Gates have deadlines and bounded retries.** Each gate and attempt has a deadline; a flaky
   failure gets a bounded number of retries on the same candidate, each recorded. Failures are
   classified (product, infrastructure, timeout, push) and only product failures go back to Main;
   infrastructure failures are retried or escalated to project admins. Elapsed time and retries
   are visible.
7. **Repairs keep their place, with a bound** (owner approved 2026-11-09, refining "keep near the front"; the
   owner may still change it). A repaired item goes right after the running item, ahead of fresh work,
   for its first return; if it comes back again, it rejoins in normal order, so a repeatedly
   failing item can't starve fresh work. Only one active revision per item is queued.
8. **Delivery is a reviewed manifest.** Completing a claim needs a delivery manifest: the required
   deliverables, the repository and target pairs with exact revisions, and how they cover the
   criteria. It is reviewed, bound to the claim generation and the versions tuple, and Blerg checks
   every listed item landed. Merges outside the manifest don't count; missing items keep the claim
   open.
9. **No-code evidence has a home.** In 0.7.0, no-code evidence is a document or note stored on the
   team ticket itself (team-visible by construction, within the claim field allowlist), with a
   version and a review. Blerg never fetches arbitrary URLs as proof.
10. **Several repositories mean partial until complete.** The manifest lists every repository and
    target; the ticket shows "partially integrated" until all have landed, with declared
    integration checks where they depend on each other. The claim stays open meanwhile. A partial
    failure after some merges is fixed forward by the claimer; Blerg doesn't promise atomic
    multi-repository merges.
11. **Reopen has an owner.** A revert or failed deploy is recorded against the delivery receipt by
    an authorized source (a gate, a deploy check or a project admin). Reopening is idempotent and
    ignores stale events. The reopened ticket returns to the project board, unclaimed, flagged with
    the failure, so anyone on the team can claim it; a removed person never silently gets it back.
12. **Completion is atomic on Board.** The review record and queue submission carry the immutable
    versions tuple and the manifest revision. Board compares the generation and tuple and commits
    completion in one step; a mismatch refuses with the reason.
13. **Completion is idempotent and recoverable.** Completion has an idempotency key and a durable
    receipt. Moving the ticket, closing the copy and releasing the lock happen through an outbox
    with reconciliation, and pending synchronization is shown. The after-delivery column is
    stored by a stable ID inside the versioned completion rules, and an invalid destination
    refuses.
14. **Main always hears about its items.** A send-back is stored durably and delivered at least once
    to the claimer's Main, including after a resume, with acknowledgement. If Main can't act (no
    credential, stopped), the item stays blocked, showing its age, owner and the action needed, and
    the claimer's own Main raises it with its person, as today. Project admins can cancel a stuck
    item.
15. **Visibility follows project access.** Chips, the tab, receipts and evidence links check live
    project read access, show only team-visible fields and sanitized gate output (full logs need
    separate authorization), and close on revocation.
16. **The first-week gate includes the queue.** On the cloud install: a project admin sets up the
    gates, target branch and after-delivery column; each person's Git token passes the push
    preflight; three concurrent claims deliver through one queue, including a conflict repair, the
    target moving during gates, a flaky gate, a cross-model review and a completed claim moving to
    "To test".

## Final round (2026-11-09)

- Goal 3 and the queue steps carry the approved bound: a repaired item jumps ahead only on its
  first return.
- Claim delivery names the reviewed manifest and on-ticket evidence (calls 8 and 9), and is named
  as the "completion rules" that `team-scope.md` links to.
- A reopened ticket returns to the board unclaimed (call 11), matching the body.
- The push step links to the personal Git token checks in `credential-handling.md`.

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- **Gates need no AI credential.** They are deterministic jobs on the install's compute (call 4
  already kept them credential-free).
- **The cross-model review** runs in the claimer's Main, on that person's own credentials, before
  submission. A person without a second provider's credential can't get one; the item waits.
- **Org gates that need their own identity,** and team-owned credentials as service identities,
  are 0.8.0, with team secrets.
