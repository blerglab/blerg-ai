# Release safety proposal (BLG-322)

Status: partially implemented for 0.6.0. The release script now uses Go gates
for required upgrade notes, exact-SHA CI, platform jobs, atomic publication,
release creation last, RC metadata and unchanged accepted-tree promotion.
Fake-remote tests cover export compatibility and publication ordering/refusal.
Automatic recovery after tag publication and the full adversarial API fixture
matrix below remain unimplemented. Upgrade drills and owner acceptance have not
been performed. No public release, tag or push was performed by this change.

## Enforced release sequence

Keep scripts/release-public.sh as the supported entry point; move new validation
and orchestration into Go, with injectable Git and GitHub clients for tests. The
default remains a local build/inspection operation. Publication must require an
explicit flag and verified upstream identity. Preserve private-history exclusion,
public-parent ancestry, scrub, clean-tree, VERSION, changelog, and embedded notes
checks already present in the shell script.

1. Validate the requested version: stable vX.Y.Z or candidate vX.Y.Z-rc.N, with
   positive N and canonical numeric components. Reject arbitrary refs and options.
   Require a release-notes Markdown input with a nonempty `How to upgrade` section
   containing desktop and Kubernetes instructions. Validate the published notes,
   not just the embedded in-app JSON catalog. Keep the existing JSON catalog gate.
2. Build the filtered public commit locally with its exact parent, tree, and SHA.
   Persist a private operation record so a retry reuses that commit instead of
   generating another SHA through a changed commit timestamp. A changed tree or
   public parent invalidates earlier evidence.
3. Push only the exact commit to `ci/<version>`. Both workflows must trigger there:
   `ci.yml` and `workspace-explorer.yml` (display name `workspace explorer platform
   security`). Do not push main or any tag yet.
4. Poll GitHub for this repository, exact head SHA, CI branch, push event, and the
   expected workflow identities. Handle pagination and bounded timeouts. Require
   completed success for both workflows, including successful Linux and macOS
   platform jobs. Missing, skipped, canceled, stale, pending, failure, and
   inconclusive results fail closed. Use the latest run/attempt for each workflow;
   an earlier green must not hide a later red rerun. An API error or unavailable
   credentials must not count as success. Retain run IDs and attempt numbers.
5. Immediately before publication, recheck all evidence and remote main. Require
   unchanged public parent; another release winning that race requires rebuilding
   and rerunning CI. Refuse any existing remote tag, including an unpublished tag
   pointing elsewhere. Never force-push a tag or rewrite a release.
6. Atomically push main and the new tag with a lease on the expected main parent.
   If atomic pushes are unsupported, refuse and explain; do not fall back to two
   independent pushes. The only intentionally published branch before this point
   is the CI branch. Tests must prove no main/tag mutation for every failed gate.
7. Create the GitHub release last, at that tag and exact SHA, attaching the validated
   notes. Mark RC releases prerelease; stable releases must not be prerelease or
   draft. Verify the created release. A release-creation failure leaves an immutable
   tag; resume only by verifying its identity and rerunning the green checks before
   creating its missing release. Never retag to recover from an API error.
8. Record completion. Keep local merge-back behavior explicit and after successful
   publication; failed API calls must not imply success. Do not change unrelated
   local branches. Resume must detect already-created releases without editing them.

A release published with a defect ships a new patch version. In particular, do not
move v0.5.0 to repair the original deep-folder listing omission. The next release's
notes should explain that it includes the fix for installs on the first v0.5.0.

## Candidate promotion

The RC is tested through the same pipeline but never triggers a stable notice:
contracts/updatenotice only accepts exact stable names. Record owner acceptance
and upgrade-drill evidence tied to the candidate's public tree and artifact IDs.

Promotion requires the stable export tree to equal the accepted RC tree. Stage
final VERSION and embedded stable release notes before cutting the RC so promotion
does not silently change source files. The RC suffix belongs to release/tag
metadata; document that its binaries can report the intended final release version.
If that reporting convention is unacceptable, obtain an owner decision on an
explicit metadata-only difference policy instead of relaxing tree equality.

The stable public commit can have a different parent and SHA even with an identical
tree. Build it once and run both workflows on that exact stable SHA before tagging.
Candidate green checks never substitute for stable-SHA green checks. Any source or
notes change after acceptance requires another RC and renewed acceptance.

## Local failing-first test plan

Use a Go test harness with temporary bare repositories and a local fake GitHub HTTP
server. No public network or remotes are required. Separate a pure planning command
from execution so reviewers can inspect the ordered plan and refusal reasons.

Cover these cases before implementing publication:

- Existing private-history exclusion and ancestry guarantees stay intact.
- Version parsing distinguishes RCs and stable tags; malformed versions refuse.
- Missing, empty, malformed, or mismatched JSON/Markdown notes refuse before push.
- A local dry run does not mutate any remote ref or call a release write endpoint.
- API fixtures cover wrong SHA, branch, repository, workflow, event, pagination,
  missing platform jobs, queued/running/skipped/red runs, later failed reruns,
  rate limits, authentication failure, and timeout.
- A command journal proves CI push precedes polling, green checks precede atomic
  tag/main push, and release creation occurs last. Inject failure at every boundary.
- Existing stable tags/releases never move, even with explicit retry flags.
- Concurrent public-main advancement refuses publication; no forced overwrite.
- Release-creation failure resumes at the same tag and commit; a conflicting tag
  or release refuses. Repeated success is idempotent and does not edit release notes.
- Candidate promotion rejects any tree difference and checks the stable SHA anew.

Use the existing scripts/release-public_test.sh fixture as compatibility coverage,
not as permission to run its historical push sequence against a real remote.
New Go tests must exercise the actual command entry point as well as validation.

## Upgrade acceptance evidence

Before publication install the previous published stable release in disposable
Linux desktop, macOS desktop, and Kubernetes environments. Seed core, board, and
runner data, encrypted credentials, artifacts, and retained workspace state. Upgrade
to the RC through the supported user command/button, using an explicit RC-only
acceptance-test path that production stable selection does not expose.

Record exact initial and final commit/image identifiers, commands, migration
results for all databases, daemon reinstall/reconnect, browser reconnect, and
health checks. Exercise backup failure and a restore to the previous version.
Compare the notes' desktop and Kubernetes commands with the commands actually used;
missing steps fail acceptance. Retain private logs outside the repository and a
sanitized evidence summary keyed to the accepted candidate tree.

These drills require actual installers and a disposable cluster; a fake remote
proves publication ordering, not upgrade compatibility or backup restorability.
The reliable pre-merge gate (#298) and macOS memory sampling (BLG-319) must be
verified as landed before interpreting red checks as release regressions. Do not
waive a red run because it resembles a historical flake.

## Documentation when implementation lands

Replace CONTRIBUTING.md's current manual release-creation sequence with the
implemented command, RC acceptance, exact-SHA checks, immutable stable releases,
recovery/resume instructions, and required upgrade evidence. Add verified desktop
and Kubernetes `How to upgrade` examples to the release-notes template. Do not
advertise script enforcement before the Go tool and fake-remote tests exist.
