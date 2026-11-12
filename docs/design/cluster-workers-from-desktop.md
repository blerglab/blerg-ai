# Cluster workers from a desktop workspace (BLG-460)

Status: design draft, round 4, 2026-11-03; no product code. Main reviews this
before anyone builds. Baseline: branch head `9fc2215c` (round 1's draft).
Round 2 replaced round 1's push-based transfer with the owner's option B
ruling and folded in reuse-the-file-path and location-independence. Round 3
tried removing the base-clone credential entirely (a bundle Main builds and
hands the pod); the owner's final call in round 4 drops that in favor of the
plain, well-understood CI shape: the pod clones directly from the provider,
read-only, with a partial clone, from a base Main has pushed. Round 4 is the
current design; round 3's cache/bundle/no-credential base-clone material is
removed, not kept as a rejected alternative, per the owner's instruction.

## Intent

The owner: "hosts really need to be able to launch pod workers, because the pods
don't use my desktop resources." With the gate plus five desktop workers running,
the desktop hit 75% CPU and memory and the owner felt typing stutter. A desktop
workspace's Main needs to push some of its workers onto the cluster instead of the
local daemon, without losing the workspace model (multi-repo members, approval
cards, Permissions rows) it already has on the desktop.

Today, `selectWorkspaceChild` in
[`workspace_child.go`](../../runner/internal/server/workspace_child.go)#L17-28
hard-refuses any child start whose requested runtime differs from the parent
workspace's runtime ("keep the parent's runtime"). A desktop (`host`) workspace
can only start `daemon` children. [`docs/talking-to-an-agent.md`](../talking-to-an-agent.md)
("Children on the cluster") already documents a *non-workspace* desktop session
starting a cluster child with `--runtime cluster`; that text predates workspaces
and the restriction above was never relaxed for them.

## Owner ruling: option B, the canonical CI shape (2026-11-03)

> Owner 2026-11-03: option B, in 0.6.0. Cluster workers get read-only clone
> credentials and never push. Blerg carries their commits back to Main as a
> git bundle through the runner. Main imports and verifies by exact commit
> before merging. The base is a published `alpha/<release>` branch recorded
> by SHA.

Round 2 folded in reusing the general file path for the *result* bundle, and
making the whole chain location-independent (§4, §5) — both still stand.
Round 3's attempt to remove the base-clone credential (shipping the base as a
bundle Main builds) didn't survive: a per-repository cache with chunked
seeding was the natural next step for large repositories, and the owner judged
that more machinery than the problem needs.

> Owner's final call, same day: the canonical CI approach. The pod clones
> from the provider with a read-only token: a partial clone
> (`--filter=blob:none`) at the recorded base SHA — fast even for large
> repositories. The base is a branch Main pushes (`alpha/<release>`), recorded
> by SHA; Main is the only one that pushes. Results come back as a delta
> bundle of the worker's own commits through the file mechanism, imported by
> `session import` with verification; the pod never has a write credential.
> Read-only tokens are per GitHub owner (a fine-grained token covers one
> owner), stored one per owner in the existing vault, shown by owner and
> expiry only, never the value. GitLab gets the same shape, briefly.

§2 and §3 below carry this. §4 (result bundles) and §5 (location
independence) are largely unchanged from round 2/3, since neither depended on
how the base got to the pod.

## Relationship to BLG-385

[`docs/design/host-controls-cluster.md`](host-controls-cluster.md) (BLG-385,
drafted 2026-11-02) covers the same runtime-transition point in the same file
and proposes a much larger capability: transferring a parent's **unpublished,
possibly uncommitted** source into the cluster via broker-stored Git bundles
(its "Source transfer" section), then importing worker results back through a
bespoke broker-and-quarantine import path (its "Results: data returns"
section), with six unresolved owner decisions and an unchecked BLG-273/BLG-318
overlap.

With round 4's ruling, BLG-460 sidesteps the source-transfer half cleanly
again: **a cluster worker only ever clones a branch Main has already
pushed** (`alpha/<release>`, maintained by Main from its local `main`,
recorded by exact SHA at launch). There is no unpublished-commit transfer and
no broker upload of Main's own working tree — round 3's brief overlap with
BLG-385's source-transfer problem (bundling unpublished commits) no longer
applies, because the base travels by an ordinary provider clone again, which
only ever sees what's pushed.

The results half still folds into the ordinary published-file path instead of
being built as BLG-385's separate broker (§4). BLG-385's bespoke "Results"
machinery (its own upload capability, its own storage, its own quarantine
import path) is not needed and should not be built in parallel with this.

**Recommendation:** treat BLG-460 as the first increment of the
runtime-transition problem BLG-385 is solving, built directly on
`workspace_child.go` and the existing non-workspace cluster-child path
(`cluster_repo.go`'s `resolveClusterRepo`/`RepoOrigin`), plus the ordinary
artifact path for the return trip. BLG-385's remaining, narrower scope after
this — unpublished/dirty source transfer into the cluster — stays its own
card for 0.7.0, unrelated to how results come back (open question 1).

## Scope

In scope: a workspace Main (desktop, sandboxed desktop, or cluster — see §5)
starting a `--runtime cluster` child for a subset of its members, each member
resolved to a provider repository and cloned in the pod — a partial clone,
`--filter=blob:none` — at an owner-chosen base branch (`alpha/<release>`)
resolved and recorded by exact SHA at launch, using a read-only, per-owner
provider credential stored once in the vault; the pod never holding a
credential that can write; the child publishing its commits as a delta
`git-bundle` file per member, through the ordinary artifact channel, at turn
end and on request; Main importing each bundle with a verify-then-fetch CLI
verb into a local `wip/` branch in its own checkout. Approval, sign-in, and
credential rules match `docs/talking-to-an-agent.md`.

Out of scope: any push credential or push path for the pod (there is none —
see §2); unpublished/uncommitted source transfer (BLG-385); automatic merging;
a cluster workspace Main starting a *desktop* child (§5, open question 6);
submodules/LFS; a deep GitLab design (stated briefly in §2, not built out to
the same depth as GitHub).

## 1. Failure rule

A cluster child failing costs only that child. The workspace Main, and its
other children (desktop, sandboxed, or cluster), keep working — a cluster-launch
failure must not touch the parent's process, its daemon connection, or any
sibling child's state. A cluster worker started from a workspace Main never
gains more authority than the person's own direct cluster sessions would: it
gets a pod with the owner's engine credentials (as any cluster session does)
and a **read-only** provider credential, scoped to one GitHub owner, with no
write permission anywhere — never the owner's own personal write-capable
credential, and never the shared operator token (`NoOperatorFallback`,
[`delegation_cluster.go`](../../runner/internal/server/delegation_cluster.go)#L436)
— and nothing that lets it reach the desktop daemon, the parent's host
filesystem, or another session. There is no push path to lock down, because
the pod is never given a credential capable of writing anywhere (§2). This is
the same hard boundary BLG-385 states for cluster actors against host/sandbox
targets, and applies unchanged here: a host-spawned cluster child is, from the
server's perspective, a cluster session like any other, authorized only by its
own recorded runtime and relationship — never by "my parent is a host Main."

Concretely: `childStart`/`clusterChildWorkspace`-style dispatch must resolve the
*child's own* runtime from what the server persisted at launch, not from the
parent's workspace runtime or a caller-supplied field, exactly as
`selectWorkspaceChild` already resolves runtime from the parent record rather
than trusting `cs.Runtime` blindly. A failed cluster launch reports failure on
the child's own launch-stage timeline (reusing
`clusterWorkspaceStartPlan`/`launchStage` from
[`cluster_launch_stages.go`](../../runner/internal/server/cluster_launch_stages.go))
and leaves the parent session's own state (its daemon connection, its other
children, its workspace manifest) untouched.

## 2. Credential scope: a read-only, per-owner token; the pod clones directly

**The pod clones from the provider itself**, the ordinary CI shape: a partial
clone (`git clone --filter=blob:none --branch alpha/<release> <url>`, then
`git checkout <recorded-sha>`), fast even for a large repository because a
blobless partial clone fetches commit/tree metadata up front and blobs on
demand, not the whole object graph. This needs a credential — but only a
**read-only** one, and only in the pod, never anything that can push.

**The existing credential stage must not be reused as-is.**
[`cluster_workspace.go`](../../runner/internal/server/cluster_workspace.go)#L104-120
already mints a credential for a cluster launch, but it calls
`fetchPersonalCredential` — the owner's *real, write-capable* personal provider
credential — and explicitly sets `PushAuthorized = true` when one is found
(`cluster_workspace.go`#L106). That path exists for cluster sessions that are
allowed to push (today's ordinary cluster children). Feeding a
host-cluster-child launch through it unchanged would hand the pod exactly the
write-capable token this design forbids. `selectHostClusterChild` (§3) needs
its own credential path that only ever reads a stored read-only token and
never sets `PushAuthorized`.

**Per-owner read-only tokens, stored once, in the existing vault.**
A GitHub fine-grained personal access token is scoped to exactly one owner (a
user or an organization) and can be scoped to "all repositories" under that
owner — it cannot span owners. So the natural shape is one token per owner the
account works across, not one token per repository and not one per launch:

- The owner creates a fine-grained PAT for each GitHub owner they work under
  (their own user account, and each organization — the owner names two
  organizations plus their own user, so three tokens total today), scoped to
  **Contents: Read-only** and **Metadata: Read-only**, on all repositories
  under that owner. No other permission.
- A new Settings section, **"Read-only tokens for workers,"** stores one
  token per owner in the existing credential vault (the same store
  `fetchPersonalCredential` reads from, extended with a new credential kind
  keyed by **provider owner login**, not by engine — `fetchCoreCredential`'s
  existing shape of "accountID + a key, fetched over the internal boundary
  with a liveness proof, never persisted at the runner" generalizes directly;
  only the key changes from an engine name to an owner login).
- The section lists each stored token by **owner and expiry only** — never
  the value, matching how a credential is already never echoed back once
  stored — and warns before expiry (a dated, dismissible notice once the
  token is inside some fixed window of expiring, reusing whatever notice
  mechanism Settings already has for other expiring things, not a new
  channel).
- At launch, the server resolves each selected member's provider owner (from
  the `org/name` identification step, §3) and fetches **only that owner's**
  token — never a token belonging to a different owner than the member's own,
  even if the account holds several. A member whose owner has no stored token
  is refused with that member and owner named (failure matrix row for
  "missing owner token") — **unless the repository is public**, in which case
  no token is needed at all: a partial clone of a public repository needs no
  authentication, so the credential step is skipped entirely for those
  members, and an account that only ever touches public repositories needs to
  store nothing in Settings.
- **GitLab**, stated briefly and not built out to the same depth: the
  equivalent shape is a read-only deploy token or project access token scoped
  to `read_repository` only, one per... GitLab's scoping is per-project or
  per-group rather than cleanly per-owner the way a GitHub fine-grained PAT
  is, so the vault entry's key for a GitLab token is the group or project path
  it was scoped to, and the same-owner-match rule becomes a same-group-or-
  project-prefix match. This needs its own pass before a GitLab builder
  starts; this draft states the shape, not the full design (open question 2).

**The minted/fetched token lives only in the pod's environment/credential
helper** for the partial clone; it is never written into the workspace
manifest, launch record, or logs Main's side persists, and the pod discards it
once the clone completes — there is nothing it needs the token for
afterward, since it never pushes and never re-fetches (failure matrix row 4).

Drop any pod-local pre-push hook. A read-only token can't push regardless of
what a hook would check, so a hook adds nothing beyond a thing that looks like
a boundary.

## 3. Manifest translation: host members → cluster members

Reuse, don't replace. The existing pieces:

- [`workspace_child.go`](../../runner/internal/server/workspace_child.go)'s member
  subset selection (`workspaceSelectorMatches`, ID/name/path matching, `--repo`/
  `--all-repos`) is runtime-agnostic already; only the `runtime := map[string]string{...}`
  lookup and its equality check against `cs.Runtime` are what currently forbid a
  cross-runtime request. The selection-by-member-ID logic itself does not need to
  change.
- [`cluster_repo.go`](../../runner/internal/server/cluster_repo.go)'s
  `resolveClusterRepo`/`resolveClusterRepoName` already turns a desktop folder
  name into `org/name` using the named desktop daemon's reported `RepoOrigin`
  (populated from the daemon's own `git remote` at hello/heartbeat time — see
  `hub.go`'s `SetRepoOrigins`). This is exactly acceptance criterion "each
  member's provider repository is resolved from its git remote," already built
  for the non-workspace desktop→cluster-child path, and is now also where the
  **owner** (the first path segment of `org/name`) comes from for the §2
  token lookup. Any member without a resolvable remote fails selection with
  that member named (failure matrix #2); no other member partially starts.
- [`cluster_workspace_child.go`](../../runner/internal/server/cluster_workspace_child.go)'s
  `selectClusterChild`/`ClusterWorkspaceChild` show the existing shape of a
  cluster child's workspace manifest (`protocol.WorkspaceManifest{Runtime:
  "cluster", ...}` built from a parent's members) — but that path requires
  `parent.Runtime == "cluster"` and projects cluster-native storage contexts that
  don't exist for a desktop parent. BLG-460 cannot call it directly; it needs a
  sibling function for the `host → cluster` transition specifically.

**Design:** add a `selectHostClusterChild` next to `selectWorkspaceChild` in
`workspace_child.go`, taken only when `parent.Runtime == "host"` and
`cs.Runtime == "cluster"` is explicit (never inferred). It:

1. Runs the existing member-subset selection unchanged.
2. For each selected member, resolves `org/name` via `resolveClusterRepo`,
   naming the originating desktop daemon (the parent's own, since only the
   parent's daemon's `git remote` is trustworthy for its own checkout — never
   another daemon's). Any member without a resolvable remote fails selection
   with that member named (failure matrix #2); no other member partially starts.
3. Requires an explicit `--base <branch>` (no default-branch fallback, matching
   BLG-385's "no default branch fallback" posture, and avoiding an accidental
   clone of `main` when the owner meant `alpha/<release>`). It resolves that
   branch to its current HEAD SHA from the provider advertisement at launch.
   It also reads Main's local clone
   (`git rev-parse alpha/<release>`) and checks that the local SHA is reachable
   at the provider remote — **Main is the only one that pushes `alpha/*`**;
   this step only verifies the push already happened, it never pushes on
   Main's behalf. If the recorded local SHA isn't present upstream yet, the
   launch refuses, naming the member and branch, with the instruction to push
   first (failure matrix row "base branch not yet pushed"). The same base
   applies to every selected member unless a later increment wants per-member
   overrides (open question 4).
4. Resolves the read-only owner token for each member per §2, refusing
   per-member if the repository is private and no token is stored for its
   owner.
5. Builds a `protocol.WorkspaceManifest{Runtime: "cluster", Kind: "repo"/"folder",
   OwnerID: ..., Members: [...]}` whose members carry `org/name` + base branch +
   recorded base SHA — never a desktop path, launch generation, or a
   `PushAuthorized: true` field; the credential itself is handed to the pod at
   bootstrap, never stored in the manifest record. This manifest must fail
   `protocol.Validate()` if it ever contains a desktop-shaped path or a
   push-authorized member, so a bug can't silently leak either.
6. Picks a branch label server-side as `wip/<card-or-topic>-<short session id>`
   (never client-supplied verbatim) for the pod's own local branch. This label
   never reaches a remote — the pod never pushes — but it still needs to be
   collision-free, because it is also the ref name the published result bundle
   carries (§4) and the name Main's import step creates locally; two children
   on the same card must not produce bundles that import to the same local
   branch name.

The cluster launch itself (pod scheduling, credential issuance, clone) then
reuses the existing cluster launch pipeline (`cluster_launch_stages.go`,
`cluster_pod.go`, `cluster_requests.go`) exactly as a non-workspace
`--runtime cluster` desktop child does today, with the bootstrap clone being a
partial (`--filter=blob:none`) clone of `org/name` at the recorded SHA using
the per-owner read-only token (§2) instead of the single bare repo argument
and `fetchPersonalCredential` path that path uses now.

## 4. Commits home: publish, download, import

The base travels by an ordinary provider clone (§2); results travel the other
way, through the file mechanism, because the pod never has a credential that
could push them.

**The general rule, stated once:** Main can pull any file a remote worker
publishes. [`docs/artifacts.md`](../artifacts.md)'s "Reading a worker's
published files" section already documents this for screenshots and reports —
a parent lists and downloads a **direct child's** published files with its own
session token (`blerg-runner session files <child-id>`, `blerg-runner session
download <child-id> <name>`), privacy-scoped to that parent/child relationship
only. A result bundle is not a new channel; it is one more file on this one,
with a recognizable kind and the metadata the import step needs.

**Publishing the bundle.** At turn end (and on request, for a long-running
worker — matching the "and on request" acceptance text), the server sets
`BLERG_WORKER_RESULT_BUNDLE=1` on a worker pod's own container, from the same
`ReadOnlyGitWorker` derivation (`isClusterPodWorker`, `k8sjobs.go`) — never a
signal a compromised pod could set itself. `agentsession.go`'s existing
`turn_done` handler, which already runs in the pod's own process (its PID 1
imports that package), runs `git bundle create <member>.bundle
<started-at>..<work-branch>` when that env var is set. `<started-at>` is the
SHA the server itself recorded for this member's clone at job creation —
stamped for bundle minimality only (so the bundle doesn't have to carry the
whole repository, just this turn's new commits), never for trust: importing
never reads or checks it (see "Importing the bundle" below). The clone stays partial (`--filter=blob:none`,
§2) for everything else, but `bundle create` runs without
`GIT_NO_LAZY_FETCH=1` (unlike this design's other git calls), so git fetches
any blob the bundled range actually needs, using the same read-only token,
before packing — a complete bundle, never a promisor bundle with missing
objects. This must happen before the pod's clone and token go away, i.e. at
`turn_done` itself. The result is published via the existing path
(`blerg-runner publish <member>.bundle`).

**Known kind, no elevated trust.** `content_type: application/x-git-bundle`
is the server's own sniff/classification of the bytes
(`classifyArtifact`/`artifacts.go` calling into `fileview.Classify`,
`artifact_types.go`) — **a new sniff rule**: `fileview.Classify` doesn't
recognize a git bundle's header (`# v2/v3 git bundle`) today, so this design
adds it there, not anything the publisher claims; size
and SHA-256 are computed by the server from the bytes it actually received
(`artifacts.go` around L304-331), not from any client-supplied header. **The
agent inside the pod holds the same session token `turn_done` does and can
run `blerg-runner publish` on a `.bundle` of its own choosing** — there is no
way to tell a trusted-driver bundle from an agent-made one, so this design
doesn't try to: every bundle of this kind, however it was published, is
treated as untrusted, and Main's checks in "Importing the bundle" below are
the entire control, independent of who published it. The `metadata jsonb`
column proposed in an earlier round of this section is dropped — nothing
here needs it now that `base_sha`/`head_sha` are no longer part of the trust
decision (next section); size and SHA-256 are already native `ArtifactRow`
columns.

**Size bounds reuse what already exists** — unchanged: the existing
per-file/per-session caps (`filelimits.go`) apply, and an oversized bundle is
refused at publish time with the existing 413 sentence (failure matrix row
10).

**Importing the bundle.** `blerg-runner session import <child> <bundle>`
(`cmd_session_import`), running in Main's own session process, never the
shared server container. `<id>` throughout means the artifact's id **and**
version together (`<artifact-id>@<version>`) — pinned once at the start, so
a second bundle published between listing and importing can never be
substituted underneath the operator mid-import:

0. Check `git --version` on Main's own host against this design's floor,
   **git 2.12** (where `protocol.ext.allow` — step 5's closed-off remote-
   helper transport — was added; every other git call here is far older);
   below that floor, refuse immediately and name the floor — never fall
   back to a different config silently. Chosen so the import actually runs
   on the machine it's for: a fixed, unenforced floor that refuses on a
   real desktop is worse than none.
1. Download the bundle named by `<id>` through the existing child-artifact
   path (same privacy limit as `session download`).
2. Check the downloaded bytes against the server's own recorded size and
   SHA-256 for that exact `<id>` before touching git at all; a mismatch
   (truncated transfer, corrupted bytes) is refused immediately.
3. `git bundle verify` the file — a structural check on its header and pack,
   before anything is fetched.
4. Require the bundle's header to advertise **exactly one head**; a bundle
   naming zero or more than one ref is refused here, before any fetch —
   there is no fixed expected label to match against (no
   `wip/<card>-<session>` comparison) because exactly one is the whole rule.
5. Remove any `refs/blerg-import/<id>` left over from an earlier, interrupted
   attempt at this same `<id>` (row 17's crash case), then fetch the bundle
   into a fresh `refs/blerg-import/<id>`, hooks and filters disabled — never
   directly onto any branch.
6. Run `fetch.fsckObjects`/`git fsck --full` against what just landed in
   `refs/blerg-import/<id>` — only possible now that the quarantine fetch in
   step 5 has actually happened; this is what proves the object graph, which
   `bundle verify` alone does not.
7. The integration branch is the parent session's own launch branch, or an
   explicit `--into`. Main fetches it fresh from its own origin with its own
   credential first, so an honest import is never refused merely because
   Main's local copy is behind — a prerequisite reachable from origin's
   current tip is accepted even if it wasn't reachable from Main's tip
   before that fetch; one not reachable from origin either is refused. Only
   after that fetch does Main `git rev-parse` the **freshly fetched**
   integration ref, once, and reuse that one value for every check below.
8. `git merge-base --is-ancestor <prereq> <integration-sha>` for every
   prerequisite `refs/blerg-import/<id>`'s header named. This, not anything
   the bundle claims about its own base, is the entire base check.
9. `git merge-base --all <integration-sha> <landed-sha>` and require
   **exactly one** line back. More than one (a criss-cross merge: two
   commits each reachable from the other's history in more than one way)
   or zero means there is no single commit the review diff could be
   honestly computed from, so this is refused here — "the merge base
   between this bundle and `<integration>` is not unique" — never landed
   even though steps 3-8 all passed. This is the same guarantee a 3-way
   `merge-tree` diff would have given, on git versions far older than
   `merge-tree --write-tree` needs.
10. Only then: a zero-old CAS onto a new branch,
    `refs/heads/wip/<full-child-id>/import-<n>` (`git update-ref <ref>
    <head> 0000…`) — never an existing ref, so there is nothing to rewind
    or replay onto. `n` starts as the count the server reports back for
    this child's completed imports, plus one (**new**: a small report-back
    call from `cmd_session_import` to the server on every completed
    import, the same call the not-yet-imported count below reads — there
    is no such bookkeeping today). If `n` is stale — the server's count
    was never updated after a prior import crashed between its CAS and
    its report-back — the CAS target already exists; `cmd_session_import`
    then probes locally for `wip/<full-child-id>/import-<n>`, steps `n`
    past whatever it finds, and retries with that value. A lost
    report-back can delay `n`, never wedge an import.
11. The review diff is `git diff <merge-base> <landed-sha>` — the same
    unique merge-base step 9 already found, reused rather than
    recomputed, against the SHA that actually landed (never anything from
    metadata).
12. On any refusal before step 10's CAS completes, no ref is created or
    moved; the downloaded bundle stays on disk for inspection;
    `refs/blerg-import/<id>`, if it was created this attempt, is removed —
    but a *successful* import's quarantine ref is also removed once step
    10's CAS lands, so step 5's leftover-cleanup case is specifically a
    previous attempt that crashed before reaching step 10 or step 12's own
    cleanup.

**Not-yet-imported bundles.** **New**: the server gains a small per-child
counter of completed imports (step 9's report-back) and a query over it;
`blerg-runner session import --list <child>` shows published bundles this
counter hasn't caught up to, and the worker's card shows their count, so an
unreviewed result never goes unnoticed. Add a test: publish two bundles,
import one, assert `--list` and the card count show exactly one outstanding.

**Multiple members, one turn.** One bundle per member, matching the
acceptance text; `session import` runs one bundle at a time, each
independently verify-then-import-or-refuse. The all-or-nothing property the
acceptance text asks for is a Main-side workflow convention: import every
member's bundle from a turn before treating any as merged, retrying only the
member that failed.

**Explicit non-goal.** Nothing downloaded from a child ever executes on
Main's machine as part of import — no hooks, no checkout, no merge driver,
no `core.fsmonitor`, nothing from the bundle's packed content runs; a bundle
carries objects and refs only, never configuration. `merge-base` and `diff`
read history and compute a textual diff; neither checks anything out or
runs a merge driver.

## 4.1 Security re-review history

Three rounds against the import design, each folded into §4 directly so it
stays the one source of truth rather than a second copy here:

- **Round 2** found the import running inside the shared runner-server
  container, trusting the artifact's own stamped `base_sha` as a launch
  record that didn't exist, a force-move `update-ref`, and a missing
  `head_sha` cross-check.
- **Round 3** drew two more highs at the same boundary (running in the
  server; trusting a launch-base record) — per this project's redesign-not-
  patch rule, the model changed instead of patching again: every import
  lands on a brand-new branch (never an existing one), and Main's own
  history, not any recorded launch base, is the sole authority for what a
  bundle may build on.
- **Round 4** (this update) found consistency gaps between §4 and the round-3
  model (stale metadata reads, a fixed expected ref name, no CAS on the
  quarantine-to-branch move), asked for a real merge-tree diff instead of a
  bare `merge-base`, real resource limits instead of named-but-unused config
  keys, and the observation that the agent inside a pod holds the same
  token the trusted publish step does — so no bundle of this kind can be
  trusted by its publisher's identity, only by Main's own checks. All folded
  into §4 above; see its commit for the exact diff.
- **Round 5** found round 4's own floor (git 2.38, for `merge-tree
  --write-tree`) refuses the import on the owner's actual desktop, which
  runs git 2.34 — a fixed, unenforced floor that refuses on the machine the
  feature is for defeats the point of checking it at all. Step 9/11 above
  replace the `merge-tree` diff with the same guarantee computed the older
  way: `git merge-base --all <integration> <head>` must return exactly one
  commit (a criss-cross merge, which would make a two-way diff dishonest
  about what the merge changes, is refused by name instead); the floor
  drops to **git 2.12** (`protocol.ext.allow`, step 5), which the owner's
  desktop already clears.
- **Round 6** reproduced two of its own findings on the owner's real git
  2.34.1: `fetch.fsckObjects` does not apply to a local bundle fetch, and
  `fsck --full --connectivity-only` skips per-object checks entirely — a
  bundle whose tree has a `.GIT` entry, a `.gitmodules` naming a
  disallowed (option-injection-shaped) url, or a symlink named `.git` all
  pass both. Step 6 above now runs a plain `fsck --full --strict`, with
  any warning-level fsck output treated as a refusal, never just a hard
  error. The bundle's advertised head ref is validated
  (`refs/heads/` prefix, `check-ref-format`) and fetched by its object id,
  never by the bundle-supplied name, closing an argument-injection path
  into `git fetch`. The git subprocesses this import starts now carry real
  RLIMIT_AS/RLIMIT_FSIZE/RLIMIT_CPU bounds via `preexec_fn`, and the
  download step shares the same overall deadline every git call already
  did. Quarantine-ref cleanup runs through its own short, deadline-
  independent timeout — the shared deadline's own `git.run` refuses once
  spent, which used to leave a quarantine ref behind forever — and the
  integration-branch quarantine ref moved from the leftover
  `refs/blerg-import-integration/<name>` to a sibling leaf under the same
  `refs/blerg-import/<id>/` prefix as the head quarantine ref, removed on
  every exit path, refused imports included. Step 11's review diff is now
  printed, not just computed and discarded.

## 5. Location independence

Main itself may run anywhere a workspace can run — on a desktop (sandboxed or
not) or as a cluster workspace — and its workers may run anywhere a child can
run. Publish, download and import must not assume either side's runtime.

The result-bundle path already satisfies this, because it is built on the
session-to-session artifact path (a parent reads a *direct child's* files
through its own token), never a host-to-host assumption. The base-clone path
(§2) is independent of Main's runtime by construction too: the read-only
token comes from the server-side vault, keyed by provider owner, not from
anything specific to where Main itself happens to run. Concretely:

| Main runtime | Worker runtime | Needs beyond this design |
|---|---|---|
| Cluster workspace | Cluster child | Nothing new: both already live on the cluster; Main's `session import` runs inside its own cluster pod's session process, against its own cluster-side checkout. The pod's partial clone and read-only token lookup are identical to the desktop case — the vault doesn't care where Main runs. |
| Desktop (host) workspace | Cluster child | **BLG-460's actual build target.** Main's `session import` runs inside the desktop daemon's session process, against its own locally-checked-out repo on disk; the pod's partial clone and token lookup are unchanged. The rest of this doc. |
| Sandboxed desktop workspace | Cluster child | Unchanged sandboxed-cluster-child approval rules (ask every time; see `docs/talking-to-an-agent.md`). Once approved, publish/download/import and the token lookup are identical to the unsandboxed desktop case — the mechanism doesn't know or care that the parent is sandboxed. |
| Cluster workspace | Desktop child | **Raised in round 2; not possible today, and not in this card's scope.** `docs/talking-to-an-agent.md` already states "a cluster session cannot start a desktop child" — this is refused by existing authorization before any BLG-460 code runs, independent of this design. Getting here would need two things neither of which this doc builds: (a) a symmetric relaxation of `workspace_child.go`'s runtime-equality pin (the same code BLG-460 relaxes for host→cluster, generalized to any explicit parent/child runtime pair, not just one direction), and (b) a target-daemon selection step, since a cluster pod has no desktop daemon of its own — the launch would need to name one of the owner's enrolled desktop daemons, which must be online and willing. **The publish/download/import chain designed here needs no changes to support this later**: it is already location-independent. The gap is entirely in child-start authorization and daemon targeting, not in file transfer. Recommend this stay out of scope for BLG-460 and get raised as its own increment if wanted (open question 6). |

Add a regression test for the last row specifically: starting a cluster-runtime
workspace's Main and attempting a desktop-runtime child must still be refused
by existing authorization after BLG-460 ships, so this design's relaxation
(host→cluster only) never accidentally widens into the reverse direction.

## 6. Privacy guard decisions

- Never send the desktop's `git remote` URL verbatim to the cluster if it
  embeds a credential (e.g. an HTTPS URL with an embedded PAT) — resolve only
  `provider` + `org/name` (as `resolveClusterRepoName` already does), and give
  the pod the **read-only, per-owner token fetched from the vault for this
  launch** (§2) — never the owner's own stored personal write-capable
  credential and never the shared operator token.
- Never let a cluster child read another workspace member the parent didn't
  select, or another session's manifest — member selection is resolved
  server-side from the parent's persisted manifest, never from a client-supplied
  member list taken at face value (matching the existing `selectWorkspaceChild`
  pattern of re-deriving from the parent record).
- The approval card and Permissions rows must say "on the cluster" exactly as
  the existing non-workspace cluster-child card does, including the
  sandboxed-parent always-ask behavior; a host workspace session is not exempt
  from that even though it's a workspace rather than a plain session.
- Failure reasons returned to Main must stay in the existing safe-reason
  allowlist pattern (`safeJobSubmissionReason`, `launchDiagnosticReason`) —
  never echo raw provider error bodies, which can contain repo visibility or
  org-membership details beyond what the refusal needs to say. This now also
  covers import refusals (bundle-verify failure, base mismatch, wrong ref)
  and token refusals (never echo the stored token's owner-scoped API error
  text verbatim).
- A published git-bundle is visible only through the existing direct-child
  artifact privacy limit (`docs/artifacts.md`: "this access is limited to the
  authenticated parent's own direct children, including private children.
  Sharing an owner does not grant another session access."). Recognizing the
  `application/x-git-bundle` kind exposes nothing new — the same row-level
  scoping that already protects a worker's screenshots protects its bundles.
- Nothing about a bundle's own claims is trusted, by construction (§4): there
  is no launch record, no stamped `base_sha`/`head_sha`, and no `metadata`
  column — a compromised worker holds the same session token the trusted
  publish step does, so this design doesn't try to distinguish a trusted
  publisher from an untrusted one (§4, "Known kind, no elevated trust").
  Everything Main accepts, it accepts because its own history and its own
  freshly-fetched integration branch already contain it (`git merge-base
  --is-ancestor`), never because something upstream vouched for it
  (failure matrix row 12).
- The vault never returns a stored token's plaintext to anything but the
  internal credential-fetch boundary at launch time (the same boundary
  `fetchPersonalCredential` already uses) — Settings shows owner and expiry
  only, the launch record never persists the fetched value, and the pod
  discards it once its one clone completes.

## 7. Failure matrix

| # | Scenario | Expected behavior | Test |
|---|---|---|---|
| 1 | Cluster unavailable (no cluster runtime configured, or cluster API unreachable) | Refused before any pod object is created, with "this install has no cluster" (existing message) or a connectivity reason; parent and siblings unaffected | Start with cluster disabled/misconfigured; assert refusal message, assert parent session untouched, assert no pod/job object created |
| 2 | Member has no provider remote (local-only folder, or daemon reports no `RepoOrigin` for it) | Refused per-member with that member named, before launch; other selected members do not partially launch | Start with one member that is a plain folder with no git remote; assert named refusal; assert no cluster objects for any member |
| 3 | Base branch not yet pushed (Main's local `alpha/<release>` SHA isn't reachable at the provider remote) | Refused before clone, naming the member, the branch and the unpushed SHA, with the instruction to push first; the system never pushes on Main's behalf | Advance Main's local `alpha/<release>` without pushing, then start a cluster child at that base; assert refusal at a pre-clone stage naming the gap, not a cloned-then-failed pod |
| 4 | Missing owner token (private repository, no stored read-only token for its owner) | Refused per-member, naming the member and its owner, before launch; a public repository under the same or a different owner is unaffected and needs no token | Select one private member under an owner with no stored token and one public member; assert the private one is refused by name while the public one proceeds |
| 5 | Expired owner token | Refused at the credentials stage with a named reason ("the read-only token for `<owner>` expired on `<date>`; add a new one in Settings"), not a generic provider auth failure; Settings also surfaces the pending expiry beforehand so this is rarely a surprise | Store a token with a past expiry (or one the provider now reports as revoked/expired); start a cluster child for a member under that owner; assert the named expiry refusal, assert no pod reaches "ready" |
| 6 | Partial clone failure inside the pod (network blip, provider rate limit, a ref that moved between resolution and clone) | Treated like any other cluster-launch failure: the child's own launch-stage timeline records it, the parent and siblings are unaffected, and the failure reason is reported plainly rather than retried silently with a different base | Simulate a clone failure (block egress mid-clone, or point at a SHA that's since been garbage-collected upstream); assert the child reaches a failed state with a named reason, assert the parent's other children are unaffected |
| 7 | Pod attempts to push with its read-only token | The provider rejects the push for permission reasons specific to the token's scope, not because of a local guard; proven with any pod-local pre-push guard removed | From inside the pod, attempt `git push origin HEAD:main`, `git push origin HEAD:alpha/<release>`, and `git push origin HEAD:some-new-branch`; assert the provider rejects all three with a permission error, with no local hook installed |
| 8 | Sign-in expired (owner's engine sign-in behind the parent is no longer valid — distinct from the owner token in row 5, which is provider-repo scoped, not engine scoped) | Launch asks on the card again, same as the existing desktop-cluster-child sign-in rule; never silently falls back to a stale sign-in | Expire the engine sign-in used by the parent's standing cluster rule; start a cluster child; assert it re-prompts rather than launching with stale creds |
| 9 | Pod evicted mid-bundle-export | No partial artifact: publishing is all-or-nothing (a complete file plus a matching `session_artifacts` row, or neither); `session files` never lists a half-uploaded bundle; child transitions to failed/interrupted; a retried turn re-exports from the same recorded base, not a moved one | Kill the pod mid-`publish` call; assert `session files` shows either the complete prior bundle or nothing new, never a truncated entry; assert child reaches a failed/interrupted state |
| 10 | Bundle at the size cap | Refused at publish time by the existing per-file/per-session limits, with the existing 413 sentence; the child session is told plainly so a turn with an unusually large diff fails loud, not silently truncated | Diverge a pod's clone past `BLERG_RUNNER_MAX_FILE_MB`; assert `publish` refuses with the existing too-large message, and the child session surfaces that refusal rather than retrying silently |
| 11 | Corrupt or hostile bundle (foreign objects, malformed pack) | `git bundle verify` and `fsck --full`/`fetch.fsckObjects` on the quarantine ref refuse before any branch exists | Publish a truncated/doctored bundle; assert refusal, no quarantine ref survives, no import branch created |
| 12 | Bundle's prerequisite is not an ancestor of Main's integration branch, even after fetching it fresh from origin | Refused by `git merge-base --is-ancestor` against the once-read integration SHA, naming the mismatch; nothing the bundle claims about its own base is ever read | Build a bundle from a commit not reachable from the integration branch's tip at origin; assert refusal before any branch is created |
| 13 | Replayed bundle (same one imported twice) | A second, independent import branch (`import-<n+1>`) is created with the same content — harmless, not an error; no existing ref ever moves | Import the same bundle twice; assert two import branches exist with the same tip, and neither moved after creation |
| 14 | Truncated or corrupted download (connection cut mid-transfer, bytes altered in transit) | The SHA-256 check against the server's own recorded hash catches it before any git command runs | Cut the download partway, or flip a byte after a full download; assert the hash check refuses it, no git subprocess started |
| 15 | Import takes longer than the one overall deadline spanning every git call (slow fetch, hung process) | Killed, refused, logged — never hangs the session, and never resets to 120s per call | A fixture `git` that sleeps past the deadline on any one of its calls; assert refusal at the overall bound, not N times that bound |
| 16 | Inflating/adversarial pack, or a git binary older than this design assumes | The fetch runs under a `ulimit`/cgroup bound, not a named-but-unenforced config key; the image's `git --version` is checked against the minimum this design requires and named in the refusal if it's too old | A pack with a maliciously large blob or deep delta chain; assert refusal, bounded memory, no hang; separately, point at a git binary below the minimum version and assert a named refusal before any git call touches the bundle |
| 17 | Main's session process is killed or restarts mid-import | No partial state: either the new import branch exists complete, or no ref was ever created — nothing half-written, since the branch is only created once at the very end (zero-old CAS) | Kill the session process between quarantine fetch and the final `update-ref`; assert the repository shows no new `wip/.../import-*` ref, never a partial one |
| 18 | Resumed pod | Starts fresh from the base on its new work branch; does not attempt to recover pre-resume commits | Resume a pod mid-turn, let it publish a new bundle; assert the new import branch is independent of any earlier one and contains only post-resume work |
| 19 | Main's local integration branch is stale relative to origin | Main fetches the integration branch from its own origin first; a prerequisite reachable only after that fetch is accepted, not refused for a staleness that was never the bundle's fault | Advance the integration branch at origin without updating Main's local copy first, then import a bundle based on the new tip; assert the fetch happens and the import succeeds |
| 20 | Bundle advertises more than one head | Refused before any fetch into quarantine — exactly one head is required, with no fallback to "pick one" | Publish a bundle built with two ref ranges; assert refusal naming "more than one head" |
| 21 | The landed head and the integration branch have more than one merge base (a criss-cross merge) | Refused by name ("the merge base ... is not unique") after `git merge-base --all` returns more than one commit; no branch lands, no diff is shown | Build two branches that merge each other twice (two independent merge commits before the bundle's head), so `--all` returns 2+; assert refusal naming the ambiguity, no import branch created |
| 22 | Bundle published by the agent's own shell, not the trusted `turn_done` step | Imported (or refused) by exactly the same checks as a `turn_done`-published bundle — publisher identity grants no trust | From inside a worker pod's own session, run `blerg-runner publish` on a hand-built `.bundle`; assert it is accepted or refused purely on §4's checks, identically to a `turn_done`-published one |
| 23 | Cluster workspace Main attempts to start a desktop-runtime child (§5) | Refused today by existing authorization ("a cluster session cannot start a desktop child"), unaffected by and unrelated to this design | Regression test: start a cluster-runtime workspace Main, request a desktop-runtime child; assert the existing refusal still fires after BLG-460 ships |

Every refusal in this matrix logs its cause (`cluster launch stage=... outcome=failed reason=...`, reusing the existing `logLaunchStages`/`launchDiagnosticReason` pattern, and the equivalent for import refusals), per the project's "every failure path leaves a log line with its cause" rule.

## 8. Open questions, with recommendation

1. **Does this doc's file-based transfer retire BLG-385's "Results" section
   entirely, or does BLG-385 keep its own broker design for some other
   payload shape?** Recommend: retire it for the result-transfer case covered
   here — there is no reason to build a second channel for the same job.
   BLG-385's remaining scope narrows to "Source transfer" (unpublished
   commits into the cluster), which this doc doesn't touch and which stays
   deferred to 0.7.0.
2. **GitLab's token shape** (read-only deploy token or project access token,
   scoped per project/group rather than per owner) **needs its own design
   pass** before a GitLab builder starts — this draft states the shape only.
   Recommend resolving it as part of implementation planning, not blocking
   this draft's GitHub-first build.
3. **General `metadata jsonb` column on `session_artifacts`, or a narrower
   bundle-specific side table?** Recommend the general nullable column — it's
   additive, keeps the "general file path, not a special channel" framing the
   owner asked for, and avoids a second table just for one kind.
4. **Per-member base branch override, or one base for the whole launch?**
   Recommend one shared `--base` for the first increment (matches the owner's
   stated flow: clone from the alpha branch Main maintains) — per-member
   overrides can be added later without changing the manifest shape, since each
   member already carries its own base field.
5. **Does a host-spawned cluster child's Live/Permissions/Insights treatment
   need anything new, or does it fall entirely under "cluster children as
   documented"?** Recommend the latter — no new UI surface, reusing the
   existing cluster-child card, Permissions row, and sidebar grouping verbatim.
6. **Cluster workspace Main starting a desktop child (§5), raised in round
   2 — build as its own later increment, or fold into BLG-460 now?** Recommend
   a later increment: it is a child-start/runtime-selection change symmetric to
   this card's own host→cluster relaxation, not a transfer-mechanism change,
   and doesn't block anything in this design.
7. **BLG-273/BLG-318 overlap**, flagged by BLG-385 and never resolved: does
   either touch workspace runtime selection or cluster manifest shape in a way
   that conflicts with `selectHostClusterChild`? Recommend checking both before
   a builder starts, same as BLG-385 already asked.

## 9. BLG-501 build note: failure rule and §2/§3 matrix check

Written before BLG-501's code, per that card's own requirement. §1's failure
rule and §7's matrix are this design's authority; this section only records
the pre-build check against them and one limitation this increment accepts.

**Failure rule re-check (§1).** Unchanged by BLG-501: a host Main's cluster
worker is authorized the same way any cluster child already is — by its own
persisted runtime and relationship, never by "my parent is a host Main." The
new code this card adds (`selectHostClusterChildSelectors`,
`hostClusterChildWorkspace`) only reaches the *existing*, already-audited
`startClusterChild`/`withClusterWorkspace`/`fetchReadOnlyGitToken` path — it
mints nothing, authorizes nothing by itself, and never sets
`PushAuthorized`. A failure in either new function returns before any pod
object or credential fetch, exactly like every other refusal in §7's matrix.

**§2/§3 matrix check, row by row, for what this increment actually builds:**

| Row | Covered by this increment | Note |
|---|---|---|
| 1 (cluster unavailable) | Yes, unchanged | Falls through to the existing `msgClusterNotConfigured`/`jm == nil` checks already in `startClusterChild` |
| 2 (no provider remote) | Yes, new | `hostClusterChildWorkspace` calls the existing `resolveClusterRepo` per member and refuses by name before building the manifest |
| 3 (base not yet pushed) | Yes, new — **see §10** | Daemon resolves the local SHA by reading the ref only; server checks reachability and the ahead count against the provider's compare API |
| 4 (missing owner token) | Yes, reuses §2 | `fetchReadOnlyGitToken` (already shipped) returns `found=false` for an unstored owner; this increment adds the per-member named refusal at launch instead of letting it fail silently downstream |
| 5 (expired owner token) | No — blocked upstream | core has no "Read-only tokens for workers" Settings section yet (same gap `fetchReadOnlyGitToken`'s own comment already names); there is no expiry to check until that exists |
| 6 (partial clone failure in-pod) | Yes, unchanged | Generic cluster-launch failure handling, not specific to a host parent |
| 7 (pod push attempt rejected) | Yes, unchanged | §2's existing `ReadOnlyGitWorker` token-scope guard; this increment never reaches a code path that could set `PushAuthorized` |
| 8 (sign-in expired) | Yes, unchanged | Existing desktop-cluster-child sign-in rule, untouched |
| 9 (pod evicted mid-export) | Yes, unchanged | BLG-479's all-or-nothing publish, untouched by this card |
| 18 (resumed pod) | Yes, unchanged | Resume starts fresh from the recorded base; this card does not add resume-time state |
| 23 (cluster Main → desktop child refused) | Yes, regression only | Existing authorization (`msgChildrenOnDaemon`/`onDesktopDaemon`/`onClusterParent`), unaffected by `selectHostClusterChildSelectors` because that function only ever fires for `parent.Runtime == "host"` |

**Limitation accepted for this increment (row 3), superseded by §10.** This
section originally shipped the row-3 check as provider-side-only (the
`refs/heads/<base>` advertisement, not Main's own local SHA), with the
daemon RPC tracked as a follow-up. That RPC was then built and reviewed
twice; its final, correct shape — ref-only on the daemon, history-and-
reachability on the server via the provider's compare API — is §10. This
paragraph is kept for history; §10 is the current design for row 3.

## 10. §9's own redesign: the daemon RPC may only read refs, never objects or network

Round 1 of §9 (the daemon RPC resolving `--base` to Main's own local SHA) shipped
`git rev-parse`/`git rev-list --count` against the member's working directory. Codex's
second review of that code reproduced a real escape: `rev-list` walking history in a
partial clone can trigger a lazy fetch of missing objects, and git's own configured
remote helpers (`credential.helper`, a `remote.<name>.vcs` transport helper, anything a
hostile `.git/config` or `.gitmodules`-adjacent config names) are exactly the kind of
thing this daemon's own `internal/discovery` package was built from the start to never
let a repository-controlled file invoke (see `inspectRepository`'s doc comment,
`internal/discovery/git.go`: "No host Git process parses a repository-controlled config,
index, include or gitdir"). §9's RPC broke that invariant for the sake of one `git`
subprocess; per this project's redesign-not-patch rule (two findings in the same
boundary means the model is wrong, not the patch), the model changes:

**The daemon RPC may only read refs — never an object, never the network, never a
configured helper, and (round 3, below) no git subprocess at all.** `--base` resolves to
Main's local commit by reading `.git/refs/heads/<branch>` directly as a plain file, or
(when there is no loose ref) scanning `packed-refs` for an exact name match — see §11,
which replaced this round's own first answer (`git show-ref`) after a further review
found that command insufficient. No `rev-parse` peeling (a tag object, an annotated ref,
`@{upstream}` — none of that is needed; `--base` is always a plain branch name, already
validated), and no `rev-list` of any kind. The branch-name validation §3.3 already
applies (no leading `-`, no control characters, no `..`) is unchanged and still runs
first.

**Everything that needs history — is this SHA reachable from the pushed branch, and by
how much is a local-only SHA ahead — moves to the server**, using the same read-only
token §2 already fetches, against the provider's own compare API (GitHub:
`GET /repos/{owner}/{repo}/compare/{branch}...{sha}`): `status: "identical"` or
`"behind"` (the SHA is at or behind the branch tip — already pushed, possibly to an
earlier point) means pushed; `"ahead"` names the gap directly (`ahead_by`); `"diverged"`
or the SHA being unknown to the provider at all means not pushed, with no count to
report. This is server-side, outbound-only, and has no access to Main's filesystem at
all — the class of bug this round found cannot recur here because there is no git
subprocess on this path, only an HTTPS call with a bearer token.

**§1's failure rule, amended:** a cluster-launch failure must not touch the parent's
process, its daemon connection, or any sibling child's state (unchanged) — and the
daemon RPC this design uses to resolve `--base` must never let a member's own repository
configuration choose what runs or what network call the daemon's own process makes;
reading one ref by name is the only capability it has. §11 strengthens this further: no
git subprocess runs at all, so there is no process whose environment needs sanitizing in
the first place.

**Implementation proof added for this round:** a regression test with a repository
whose `.git/config` names a remote helper (`remote.origin.vcs` or equivalent) and a
promisor remote, where the helper's own invocation would write a marker file if run;
the test asserts the marker file never appears.

## 11. §10's own redesign: no git subprocess at all

§10's round-2 fix narrowed the daemon RPC to one command, `git show-ref --verify --hash`,
in a sanitized environment. A third review found that insufficient on its own terms:
`show-ref` still opens the repository's `.idx`/`.pack` files to resolve and validate the
ref's object, and still honors `objects/info/alternates` while doing so — a "ref-reading
command" is not the same guarantee as "reads only a ref." Per this project's own rule
(a second finding in the same boundary means the model is wrong), the daemon RPC runs no
git subprocess at all:

- **The repository's real git directory** is resolved exactly as
  `internal/discovery/git.go`'s `inspectRepository` already does for every other
  operation this service supports (handling a `.git` file's `gitdir:` indirection for a
  linked worktree, with the same root-containment checks) — reused via
  `repository.commonPath`, not re-derived.
- **A loose ref** (`<gitdir>/refs/heads/<branch>`) is read as a plain file through the
  same `openBeneath`/`readSmall` primitives every other read in this package already
  uses — `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS` at the kernel level, so a symlinked ref
  file, a symlinked intermediate directory, or any path that would resolve outside the
  git directory is refused before a single byte is read. Its content is validated as a
  bare 40/64-hex object id, or as one `ref: refs/heads/<name>` indirection (followed at
  most once, through the same path and the same containment).
- **Otherwise, `packed-refs`** (bounded to 8 MiB; a real file with tens of thousands of
  refs stays well under this) is scanned line by line for an exact match on the ref's
  full name; a peeled line (`^<oid>`, the dereferenced target of an annotated tag
  immediately above a loose or packed tag ref) is skipped — it never names a branch ref
  itself. A loose ref overrides a stale `packed-refs` entry for the same name, matching
  git's own precedence.
- **Nothing under `objects/`, `config`, `hooks/`, or any `info/` file is ever opened.**
  Reachability (is this SHA actually part of the branch's history) and how far ahead a
  local-only commit is are answered entirely server-side, against the provider's compare
  API (§10) — this daemon RPC has no way to answer that question and does not try to.

**Implementation proof added for this round:** a repository with `objects/info/alternates`
naming a nonexistent path and the commit object itself deleted — `resolve_base` still
returns the ref's own SHA, because it was never asked to open that object; a symlinked
loose ref and a symlinked intermediate ref directory are both refused; packed-refs-only
resolution and loose-overrides-packed precedence are both proven; an oversized
`packed-refs` file is refused rather than read.

**Round 4's own follow-up, folded in here rather than a new section:** this function
still confirmed a member's identity by calling the *general* discovery path
(`safeDirectory`/`inspectRepository`), which reads `config` and opens `objects/`/`refs`
for a check `resolve_base` never needed. `internal/discovery/git.go`'s gitdir-resolution
(handling a linked worktree's `.git`-file indirection) was extracted into its own
`resolveGitDir`, shared by both `inspectRepository` (unchanged behavior for every other
discovery operation) and a new `resolveRepositoryIdentity` that computes the same
identity hash `inspectRepository` always has, stopping there — no config, HEAD, index or
object read. `resolve_base` uses only the latter. Proven with a repository whose
`.git/config` and `.git/objects` are both `chmod 0000`: `resolve_base` still succeeds,
because neither is ever opened. `validRefBranch` also gained the two checks the design
text above implied but never stated outright: no leading `/`, and no empty path segment
(so `//`, a leading `/`, or a trailing `/` are all refused before any path is built) —
the server's own `validBaseBranch` (workspace_child.go) carries the identical check.

## 12. Live e2e: a silent "RPC not routed" gap, and the missing failure logs

The real end-to-end run (a desktop Main, `--runtime cluster --base main`, single-repo
install whose configured repos root is the checkout itself — member path `.`, matching
`entryFor`'s own pre-existing root convention) refused at `resolve_base` with no log line
anywhere — violating this project's own "every failure path leaves a log line with its
cause" rule, and leaving the actual cause undiagnosable from the daemon's own log. Two
gaps, both in `internal/daemon/discovery.go`'s `handleDiscoverWorkspace`, not in
`resolve_base`'s own logic (proven correct for a root-path member by a real daemon
process, both `internal/discovery`'s own tests and a real-wire test against the shipped
binary):

- `resolve_base` shared `discoverySlots`, the same fixed two-slot budget every ordinary
  list/resolve/bind UI call already competes for. On a desktop with an active Explorer or
  file panel, a launch's own `resolve_base` call could be silently dropped by the daemon's
  own `select { default: return }` — never answered, never retried, never logged — while
  the server's own wait simply timed out. It now has its own capacity (`resolveBaseSlots`),
  so ordinary discovery traffic can never starve a launch.
- Every exit from `handleDiscoverWorkspace` before this round was silent: a missing
  websocket link, a dropped request, a daemon-side refusal (any `ErrorCode`), all reached
  the caller as the same unlogged failure. `resolve_base`'s own exits now call
  `failurelog.Default.Event` with the stage and typed cause (never a path, a body or free
  text); the server's own `resolveBase` (`cluster_host_workspace.go`) logs the matching
  three-way split (transport error, daemon refusal with its `ErrorCode`, or a malformed
  reply) on its own side too, so either log alone names what happened.

## 13. Live e2e, round 2: a manifest Path is not a filesystem path, and one ambiguous cause

§12's fix shipped and deployed, but the same live desktop still refused with
`cause=invalid_selection` at `resolve_base`, with no visible anomaly anywhere in the real
repository — a real `.git` directory, a loose `main` ref matching `packed-refs`, no symlinks.
A test reproducing that exact layout (a single-member host selection, bound through the real
`resolve`+`bind` round trip `workspace_selection.go` performs, then `resolve_base` against the
bound manifest's own member) passed cleanly. The gap only reproduced once the daemon's
configured root held *more than one* repository and only one was selected — Main's own
desktop shape (one root, several checkouts, one chosen per workspace), not the single-repo
fixture §12's own regression test used.

The cause: `workspaceselection.MemberPaths` deliberately collapses a single-member
selection's public, manifest-facing `Path` to `"."` for display (desktop single-repo
convention) — `"."` is correct only when the daemon's configured root *is* the repository.
Every other post-bind daemon operation already knows the manifest's `Path` is a display
value, not a filesystem one, and recovers the real on-disk path from the daemon's own
remembered bound selection instead (`s.bound`, keyed by `LaunchGeneration`): `PinBound`
(`pins.go`), `bindChild` (`child.go`), `RestoreBound` (`recovery.go`). `resolveBase` was the
one RPC that skipped that lookup and opened `req.Folder.Path` directly. For Main's desktop,
that path was `.` — the *root*, which holds several checkouts and no `.git` of its own —
so `resolveRepositoryIdentity` correctly refused it (`found=false`): a real refusal, over a
request shaped wrong, not a corrupt repository.

The fix: `resolve_base` now carries `ParentLaunchGeneration` (the parent host workspace's
own, already in `cluster_host_workspace.go`'s hand — it is the workspace being resolved
against) and looks up the real `SelectionRef` within that bound selection matching the
request's `SourceIdentity`, the same pattern every sibling operation above already uses.
`req.Folder.Path` is no longer read as a filesystem path at all.

The second half of this round, closing the actual ask that started it: every refusal site
inside `resolveBase` and the helpers it calls (`resolveRepositoryIdentity`, `readRef`,
`readPackedRef`) used to return the same shared `errInvalid` sentinel, which collapsed to
the one string `"invalid_selection"` everywhere it was logged — indistinguishable without a
live reproduction, exactly what cost this round its own round-trip. Each site now also
returns a distinct, fixed `cause` label (e.g. `resolve_base:gitdir_not_found`,
`resolve_base:unknown_member`, `resolve_base:ref_not_found`). This travels only in
`protocol.WorkspaceDiscoveryResult.Cause`, a field read by the daemon's own log
(`discovery.go`) alone — never folded into the wire-level `ErrorCode`, which stays in its
existing, server-validated vocabulary (`validDiscoveryResult`, `workspace_discovery.go`) so
every other operation's contract, and every other consumer of a resolve_base `ErrorCode`
(`cluster_host_workspace.go`'s own log, the launch's generic refusal message), is unchanged.

## Implementation proof required (for the build phase, not this doc)

Public API/CLI path tests for the failure matrix above, including the
token-scope test with any pod-local guard removed (row 7), the owner-token
resolution tests (rows 4-5), and the bundle-verification and replay tests
(rows 11-12). Then the real end-to-end check the card requires: from a desktop
workspace, start one cluster worker for a single member at a named `--base`,
confirm the pod's partial clone succeeds with only a read-only owner token and
that token cannot push, have it commit, publish its bundle (at turn end and
once on request mid-turn), read it back with `session files`/`session
download` including its member/base/head metadata, run the new `session
import` to verify and land it in a local `wip/` branch in Main's own
checkout, and inspect the resulting commit. No test suite is claimed by this
draft; this section only records what the build phase must prove.
