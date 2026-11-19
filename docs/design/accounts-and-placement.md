# Accounts, credentials and worker placement (0.7.0)

Status: draft for owner review, 2026-11-08, revised after adversarial review round 1 (see the end). Card: BLG-609. Builds on the team scope design
(`docs/design/team-scope.md`, BLG-606). Related: BLG-536 (multiple engine logins), BLG-580 (team
account pools; superseded in 0.7.0, see Simplification), BLG-579 and BLG-500 (worker limits per target), BLG-323 and BLG-574 (engine
sign-in and renewal), BLG-611 (credential handling), BLG-613 (usage), BLG-618 (docking, 0.8.0).

## Why

The startup's first week depends on each person using their own Claude and Codex credentials
successfully (owner, revised 2026-11-09; earlier "the team-owned collection of logins"). Today a
person connects one Claude and one Codex login; workers run on whichever login the session has;
the usage meter shows one bar per service. People need any number of their own credentials,
chosen deliberately for Main and for each worker, balanced by load, and visible at a glance.

## Goals

1. **Credentials: an arbitrary number, of any configuration** (owner), for example 3 Claude and
   3 Codex. Adding one is a **[+]**
   that opens a service picker (Claude, Codex, and others later through an extensible list) and
   produces a **credential card**.
2. Every credential has a **nickname** and a **Blerg credential ID**: an opaque Blerg handle,
   never the provider's account ID.
3. **Credentials are personal** (owner, 2026-11-09): each belongs to the person who added it and
   is used only by their own sessions. No teamspace credentials and no sharing. An org may
   restrict which providers its workspaces may use (team scope design).
4. **Per workspace** (in the new-workspace screen and in workspace settings): choose the
   **orchestrator credential** Main runs on, and the **worker credentials** allowed (several).
5. **Main chooses** among the person's own credentials for each worker, guided by a
   **recommendation** from Blerg based on the existing meters (usage windows, workers per
   credential) and round robin by load. No throttling: if a provider refuses for limits, Main
   moves on to another credential.
6. **Main chooses the runtime** for each worker, within what the person authorized for the
   workspace (cloud, host or a machine), unless the person stated a preference.
7. **Meters**: one row per service, the bar split into one segment per credential, each segment
   showing its own percentage on the bar, with a hovercard. A live **Accounts usage tab** shows
   everything in detail.

## Non-goals for 0.7.0

- Docking the usage tab into a sidebar (BLG-618, 0.8.0).
- Automatic credential choice without Main (Main decides; Blerg recommends).
- Provider billing integration beyond what the meters already read.
- Team-owned or shared credentials (0.8.0 at the earliest, as service identities with team
  secrets).
- Throttling: fair share, queueing, per-credential reservations or admission leases, and
  coordination of the same login across cards or people.

## Anti-goals

- Exposing a provider account ID, email or refresh token through Blerg itself (its APIs, UI,
  logs, Main's context or other members' views). Main sees only Blerg credential IDs and
  nicknames. **Honest limit (owner, 2026-11-09):** an engine process needs a short-lived access
  token, so code running in a session could print that token or ask the provider which account it
  is. Refresh tokens never enter a session; access tokens are short-lived; a credential is used
  only by its owner's sessions; the doc and UI say so plainly, as for team secrets.
- A credential usable by anyone but its owner.
- A recommendation that silently overrides the person's runtime authorization or preference.

## Credential cards

- **[+] → pick a service → connect.** The service list is extensible: each service type declares
  its connect flow (sign-in, API key or endpoint), how to read its usage, and how sessions use it.
  0.7.0 ships Claude and Codex. **Local model** (below) moves to 0.8.0.
- A card shows the nickname (editable), service, status (connected, expired, needs sign-in), its
  meter, and workers currently running on it.
- Cards live in personal settings only.
- Renewal and storage follow BLG-611 and the existing rule: refresh tokens never land in a session.
- **Claude seats are subscription seats.** An API-key card is allowed by choice, but it is
  not an acceptable fallback for seats; if the BLG-574 spike fails, the alpha is blocked
  (owner, 2026-11-09; `credential-handling.md`).

## Examples

- **A family on a homelab.** Each person adds the same Claude login to their own Blerg account by
  signing in with it. Blerg treats them as separate personal cards; each person's meter shows the
  same provider quota, because it is the same login.
- **A startup.** Each founder adds the seats they use, for example one Claude and one Codex each,
  or several of either.
- Whether people sign in to the same provider account is their choice and their responsibility
  under the provider's terms (owner, 2026-11-09: "we're just a tool").

## Workspace credential choices

- **Orchestrator credential:** exactly one, which Main runs on. Changeable in workspace settings.
  A change lets Main finish its current turn, then starts a fresh native conversation on the new
  credential, carrying context from Blerg's archive (team scope round 2, call 11).
- **Worker credentials:** one or more, which workers may use. A credential can be chosen only if
  it is the person's own and its provider is allowed for every project the workspace includes
  (by the org's provider policy; team scope round 2, decision 2).
- Defaults for a new workspace come from the **eligible set**: the person's credentials that pass
  that check and fit the chosen runtime. The orchestrator default is the first eligible one;
  workers default to all of them. A person with no credential is asked to add one with [+].
  *(Main's call; the person changes them in the new-workspace screen.)*
- This replaces BLG-536's fixed "Main on login 0".

## Main chooses, Blerg recommends

- When Main starts a worker, it passes a **credential ID** from the workspace's allowed set, and
  a **runtime** from the authorized set.
- Blerg gives Main, on request and in its turn context, the allowed credentials with: nickname,
  service, workers running on each (from this workspace and in total), meter state (each window's
  percentage and reset), status, and a **recommended** credential.
- The recommendation balances by load and round robin: prefer the credential with the fewest
  running workers among those with headroom; break ties round robin; steer away from credentials
  near a usage limit (the existing 80/90/95 percent thresholds) or expired. Deterministic, no LLM
  judgment. It only reads meters; it never throttles or queues.
- Main may choose a different allowed credential (for example a stronger model for a hard task).
  A refused choice (not the person's, provider not allowed, needs sign-in) comes back with a
  plain reason. If the provider refuses because of limits, Main moves on to another credential.

## Runtime placement

- The person authorizes which runtimes a workspace may use: cloud (the cluster), host, or named
  machines. They may state a preference, for example "prefer cloud".
- Within that, **the orchestrator chooses** (owner). Blerg recommends a runtime from free capacity
  (per-target worker limits, BLG-579 and BLG-500) and the preference.
- Running sessions on a machine is separate from using that machine for inference (owner).

## Meters and the Accounts usage tab

- **One row per service.** The bar is divided into equal segments, one per credential the person
  owns; each segment fills to that credential's percentage in the usual warning colors and
  shows **its own percentage over the segment** (owner). The separate number on the right goes
  away; the bar is tall enough for readable text.
- **Hovercard per segment:** nickname, each usage window with its reset time, workers
  running on it.
- **Many credentials:** past about six segments, the row shows a summary instead, and a click
  opens the tab. *(Main's call on the exact threshold.)*
- **Accounts usage tab:** opened from the meters, a live tab (using the 0.6.0 live datasets, data
  updates in place without redrawing): every credential the person owns with nickname, windows, resets,
  workers running and usage over time. Works at 390 px and at 4K.
- The meters grow more complex as accounts multiply; keep the sidebar row calm (one glyph per
  state, words in hovercards).

## Local models (moved to 0.8.0)

*Kept as the 0.8.0 design; not built in 0.7.0 (owner, see the end).*

- A **Local model** card is an inference endpoint, for example a model served by the startup's Mac
  Studio. Workers anywhere can use it as a worker credential.
- It has no provider meter. Its segment shows **capacity**: worker slots in use out of the slots
  the owner allows, plus GPU or memory load if the machine reports it. Its hovercard shows
  nickname, machine, model name, workers running and online status. The recommendation treats free
  slots like headroom on a seat.
- **Reaching it:** two options.
  - **Daemon relay (default):** traffic goes through the daemon's existing outbound connection,
    so no network setup is needed. The startup struggled to set up a VPN, so this is their path.
    It costs latency and cloud bandwidth, and needs a streaming proxy with backpressure and
    limits.
  - **Direct URL:** a URL and optional key, for teams that already have networking (for example
    Tailscale).
- Built after the first-week acceptance path works.

## Decided by the owner (2026-11-08)

- Any number of credentials of any configuration; [+] with an extensible service picker; cards.
- Orchestrator credential and multiple worker credentials per workspace, in the new-workspace
  screen and in settings.
- Main chooses, recommended by load and round robin; Blerg credential IDs, never provider IDs.
- Nicknames; meters get more complex; segmented bar with a percentage over each segment and a
  hovercard; a live tab of the data; docking in 0.8.0.
- Local inference: separate from running sessions on a machine; daemon relay plus direct URL; low
  priority in 0.7.0.
- Runtime: the person authorizes runtimes and may set a preference; otherwise the orchestrator
  chooses.

## Left to the builder

- Storage and APIs for cards, IDs and workspace choices across Core and Runner.
- The service-type extension interface.
- How the recommendation is delivered to Main (turn context, a CLI command, or both).
- UI details within the UX skill's rules.

## Review round 1 (2026-11-09): contracts added

An adversarial review (Codex, 12 findings, 7 high) found gaps at the boundaries. The owner
decided the credential-exposure line above. The rest are **Main's calls** (the owner may
overrule):

1. **The allowed set is an upper bound, never a grant.** At every launch, resume and account use,
   Blerg intersects: the person's current authority, the workspace's one org, the projects it
   includes and each item's scope, org policy (including allowed providers), and the workspace
   choices. The same applies to Main
   and to every descendant worker. Core authorizes the credential; Runner enforces the session and
   workspace bounds. Defaults for a new workspace come only from credentials that pass that check
   and are compatible with the chosen runtime.
2. **Ownership is permanent; there is no sharing** (revised 2026-11-09). Every AI credential is
   personal and used only by its owner's sessions. An org may restrict which providers its
   workspaces use; that replaces the switch for personal AI accounts. Blerg never falls back to
   another person's credential.
3. **Advice is separate from launch checks** (revised 2026-11-09). The recommendation is only
   advice. A launch checks the credential (the invoker owns it, its provider is allowed, it is
   connected) and reserves **runtime** capacity with an expiring, idempotent lease (team scope
   round 2, call 5). Nothing is reserved on the credential: no credential lease, no fair share,
   no queue. A provider refusal is a normal failure; Main moves on to another credential.
4. **Same login, separate cards** (revised 2026-11-09). Blerg doesn't detect, refuse or
   coordinate the same provider login on several cards or across people. Each card reads the
   provider's meter, so cards on one login show the same quota. Unknown meter state shows as
   unknown, never as 0%.
5. **Renewal is serialized and checks the invoker.** Core's generation-fenced managed renewal
   covers every credential, and checks the owner's authority on each refresh. If
   renewal needs a human sign-in, the affected worker pauses with a plain reason; if authority is
   denied, it stops at once (team scope, round 2, decision 1). It never switches credential. The first-week acceptance includes simultaneous
   refresh, a human reconnect, and removal during a refresh.
6. **Switching Main's credential starts a fresh native conversation** (refined in team scope round 2, call 11: any credential change),
   carrying context from Blerg's archive. Credential state is never swapped under a running
   process, and one credential's private state is never reused for another.
7. **Meters show only the owner's credentials.** Nobody else sees them. Each meter states its source, when it
   was observed, its window and reset, or "unknown". Provider identity stays private in Core, and
   nicknames never auto-import an email.
8. **The local-model relay carries inference only.** It accepts only a narrow inference protocol,
   to an endpoint and model the machine's owner approved. No caller-chosen destinations, paths,
   methods, administrative APIs or redirects. Each stream is bound to a short-lived capability for
   that session, org and credential, checked at both ends, with size, concurrency, timeout and
   backpressure limits. Blerg doesn't claim to isolate a model server it doesn't run; the card
   labels it "trusted external endpoint" (team scope round 2, call 13). The relay ships only when
   this boundary is met (0.8.0).
9. **Runtimes are concrete.** Placement uses runtime IDs carrying the owner, the isolation mode
   and live grants, not a bare "host". On a shared machine, other people's sessions are always
   sandboxed, whatever Main prefers. Sending prompts to an inference endpoint is authorized
   separately from where a worker runs, and the UI shows who can observe those prompts.
10. **Mixed versions refuse rather than guess.** The launch and refresh contract is versioned
    across Core, Runner, Board and the daemon. Older components refuse a launch that names a
    credential instead of falling back to a default login. Upgrade, partial deployment, rollback and restore are
    tested.
11. **Claims bind to the current criteria.** Completing a claimed ticket checks the source's
    current acceptance-criteria version and the project's completion-rules version; any change to
    either requires another review (team scope round 2, call 12; `merge-queue-and-delivery.md`). Deleting the copy, the source or the project releases or detaches the claim
    idempotently (refining team-scope contract 4).
12. **The 0.7.0 gate,** in addition to team-scope's acceptance path: three people each using
    their own renewable Claude and Codex credentials from cloud Main and workers, plus selection
    near a limit and moving on after a provider refusal, simultaneous refresh, a human
    reconnect, removal or policy denial mid-turn, recovery after changing Main's credential, and
    claim completion after a source edit.

## Review round 2 (2026-11-09)

The second review covered both documents together; its decisions and calls are recorded once, in
`team-scope.md` under "Review round 2", and apply here. In particular: revoked means stopped at
once when the executor is reachable, and within 30 seconds when it isn't (decision 1, refined by
the owner on 2026-11-09); credentials, runtimes and endpoints must be allowed for every project in the
workspace (decision 2); one admission record fences execution on runtimes (call 5); any credential change gives Main a fresh
native identity (call 11); the relay and direct URLs share one authority model (call 13); and the
mandatory invariant tests (call 14).

## Scope moved to 0.8.0 (owner, 2026-11-09)

After a readiness review by two models (Codex and Claude), the owner moved these out of 0.7.0;
they can be pulled forward if 0.7.0 goes well. Where this list conflicts with earlier sections,
this list wins.

1. **Shared personal machines and local models** (BLG-607, BLG-608): sharing a person's machine
   with a team, sandbox-only use by others, the local-model card, the daemon relay and direct
   URLs. The startup works on the cloud in 0.7.0.
2. **Nested orgs and projects in the UI, and blackboard rollup.** The data model stays nested so
   no door closes; the 0.7.0 screens show one org, flat projects and one level of teams.
3. **Release extras:** the signed bill of materials, multi-architecture images and mirror coverage
   for dynamic worker Jobs. Digest pinning stays.
4. **Full key rotation:** versioned-envelope re-encryption and a planned no-downtime rotation
   procedure for every secret class. 0.7.0 keeps the **emergency rotation** procedure (which may
   need a brief planned outage, documented) and the **mandatory offboarding rotation**. *(Since
   2026-11-09 there are no team credentials to rotate in 0.7.0; see Simplification.)*
5. **The restore drill on first install** becomes a **release-gate check we run ourselves**;
   installs keep whole-install backups and a written restore procedure. Customers can add their
   own verification pipelines.
6. **Magic links.** Sign-in in 0.7.0 is **username + password + an authenticator-app code**.
   Email is still used for invites, password resets and credential alerts.

## Final round (2026-11-09)

- Workspace defaults come from the eligible set; team-only onboarding works (Main's call: team
  credential first for Main). *(Superseded 2026-11-09.)*
- Worker credentials must be allowed for every project in the workspace.
- A Main credential change starts a fresh native conversation after the current turn, not "the
  next turn".
- Admission counts Main only while it runs a turn (call 3, matching team scope call 6).
  *(Superseded 2026-11-09: no credential admission.)*
- Call 8: an external model server is a "trusted external endpoint", not claimed isolated.
- Call 11: any criteria or completion-rules change needs another review.
- Local models are marked 0.8.0 in the body.
- *(Superseded 2026-11-09: no shared credentials.)* A teammate's shared personal credential is
  eligible under the team-scope matrix, never a default, and blocked by the personal-accounts
  policy.
- *(Superseded 2026-11-09.)* Admission for a shared credential rechecks the share, team
  membership, policy and the owner's account; it counts as team work for the 30-second self-stop.
- Revocation: at once when reachable, within 30 seconds when not (owner, 2026-11-09).
- Claude seats are subscription seats; API keys are not a fallback (owner, 2026-11-09).

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- **Personal credentials only:** any number, any mix, from [+] in personal settings. Teamspace
  cards, sharing, and teammates' credentials in the eligible set are removed.
- **Org policy:** "switch off personal AI accounts" becomes "restrict which providers the org's
  workspaces may use".
- **No throttling:** removed fair share, credential reservations and admission leases, and
  duplicate-login refusal and coordination. Main picks among the person's credentials from the
  existing meters plus round robin by load, and moves on when a provider refuses. Runtime
  reservations and leases stay.
- **Meters:** segments, hovercards and the usage tab cover the person's own credentials; the
  owner field is gone.
- **Examples** added: a household on a homelab, and a startup.
- **The gate:** each person's own Claude and Codex credentials, not a team-owned collection.
- BLG-580 (team account pools) no longer applies to 0.7.0; team-owned credentials as service
  identities are 0.8.0, with team secrets.
