# Team and individual scope (0.7.0)

Status: draft for owner review, 2026-11-08, revised after adversarial review round 1 (see the end). Card: BLG-606. Related: BLG-607 (shared machines),
BLG-609 (account pools and placement), BLG-611 (secrets), BLG-612 (managed
install), BLG-613 (usage), BLG-614 (audit), BLG-480 and BLG-313 (merge queue and delivery,
`merge-queue-and-delivery.md`); BLG-610 (OIDC), BLG-615 and BLG-616 are deferred to 0.8.0.

## Why

Blerg is single-person shaped today: every workspace, board, blackboard, secret, machine and
engine login belongs to one account, and an install's admin manages accounts. 0.7.0 serves a
startup of three technical people (an engineer, a quant and a CEO, all with computer science
backgrounds). They work as one team on one project, all orchestrating code on it. They will run
Blerg on a managed cloud Kubernetes install and later add local hardware.

The model must also scale to an enterprise: one install, several orgs with sub-orgs and teams,
separate ops engineers and admins, teams working across unrelated projects, and projects nested
inside larger org projects with many subprojects. Nothing decided for the startup may close that
door.

## Success (owner's words, the first week)

The startup signs in to a cloud Kubernetes Blerg, collaboratively defines the project and its
top-level blackboard, sets up their workspaces, and each begins contributing to the project,
successfully using each person's own Claude and Codex credentials. *(Revised by the owner on
2026-11-09; it said "the team-owned collection of Claude and Codex logins".)*

## Goals

1. Four kinds of scope, each with one clear job:
   - **Org tree: who.** Orgs contain sub-orgs and teams. It holds members and roles.
   - **Project tree: what.** A project contains subprojects. It holds the work: repositories,
     a board, a blackboard, data.
   - **Teamspace: how.** A team's home. Over time it holds the team's skills, gates, MCP
     connections, workflows, conventions, secrets and compute, and it gathers the projects the
     team works on. **In 0.7.0 it holds only the team's compute** (owner, 2026-11-08; AI
     accounts are personal only since 2026-11-09); the rest follows in 0.8.0 on the same model.
   - **Workspace: me doing the work.** It stays personal, as today. It draws the "what" from
     projects and the "how" from the person's teamspaces, and adds personal things.
2. Hierarchy everywhere: orgs nest, projects nest, and teamspaces inherit along the org tree.
   Separating concerns stays cheap as an org grows.
3. Ownership by default, sharing opt-in: everything personal is "mine" until its owner shares it.
   Sharing is revocable.
4. Repositories are one resource type, not the foundation. A team without code must still fit.

## Non-goals for 0.7.0

- Custom roles. Three built-in roles only.
- Identity-provider group sync and OIDC sign-in (BLG-610, 0.8.0). Membership comes from invites;
  0.7.0 signs in with username, password and an authenticator-app code (`sign-in.md`).
- Stalled claims: expiry, reminders and reassignment (BLG-616, 0.8.0).
- Sharing personal artifacts, skills, charts, documents or diffs with other people (BLG-615,
  0.8.0). The model must allow it later.
- Resource types beyond those listed under Resources.
- Teamspace MCP connections, teamspace secrets, and teamspace skills, gates and workflows (0.8.0;
  team MCP connections land alongside BLG-145). Personal MCP connections and personal secrets keep
  working as today.
- Workspaces that draw on more than one org (owner, 2026-11-08: one org per workspace for now).
- Required second approvers for plan changes (BLG-617, 0.8.0).
- Protected (non-exportable) secrets behind a broker, for org-level gates (with org gates, 0.8.0).

## Anti-goals (failure even if it works)

- Anything that assumes one org per install, one team per project or one project per team.
- A team able to impose rules, gates or costs on people outside it.
- Personal secrets readable by an org, or org secrets readable by people.
- Repositories baked in as the thing every scope must have.
- An upgrade that changes how an existing single-person install behaves before anyone opts in.

## The model

### Org tree (who)

- An install holds one or more orgs. An org contains sub-orgs and teams, nested to any depth.
- A person can belong to several orgs and teams.
- **Install admin is separate from org admin.** An enterprise's ops engineers run the install
  (upgrades, backups, install-wide settings) without automatically seeing any org's work.

### Roles

- Three built-in roles: **Admin** (members, roles, settings, secrets and policies at that level),
  **Member** (work on projects, claim tickets, use the teamspace's how and compute), and
  **Viewer** (read only).
- A role can be granted at any node: an org, a sub-org, a team, a project or a teamspace. It
  flows down that node's own tree only, within the limits in the permission matrix below:
  membership is separate from grants, projects stay private to their people, and team compute
  needs team membership.
- Each org decides how to use the roles. The startup can simply make all three people org admins.
- Custom roles come later. The permission checks are built so new roles can be added without
  changing call sites. *(Builder's call how.)*

### Permission matrix (0.7.0)

*Main's call, final round.* Four actions: **read** (see it), **use** (work with it, spend it),
**administer** (change or remove it) and **delegate** (grant others access). Viewer at a node
gets read only, with one exception: on teamspace compute a viewer of the
owning team sees status only, like an org admin. "Org admin" means admin of the owning org or an
ancestor org. **Teamspace admins** are the owning team's admins plus anyone given admin directly
on the teamspace (for the org teamspace, the org admins).

| Resource | Read | Use | Administer | Delegate |
|---|---|---|---|---|
| Install (settings, accounts, backups) | Install admins | None | Install admins | Install admins name other install admins |
| Org (members, teams, policies) | Org members and viewers | Members create projects | Org admins: members, roles, policies, create teams | Org admins grant roles in their subtree |
| Team and its teamspace settings | Team members and viewers, org admins | None | Team admins, org admins | Team admins and org admins add org members to the team, grant team roles, and grant teamspace admin to team members |
| Teamspace compute | Those with use (card, meter, status); team viewers and org admins see status only | Members of the owning team or a team below it; for the org teamspace, org members | Teamspace admins: connect, reconnect, limits, remove; org admins of a team's teamspace may only disable or remove | Use only through team membership, granted by team admins or org admins; no direct grants to outsiders in 0.7.0. Teamspace admin is granted by team admins or org admins; a direct teamspace admin can't grant it on |
| Project (settings, repositories, gates, completion rules, merge queue) | People and teams added to it, org admins | Project members: include it in a workspace, submit to its queue | Project admins, org admins | Project admins add people and teams; org admins |
| Project board and tickets | As project | Project members: create and edit tickets, claim | Project admins, org admins: columns, after-delivery column, unlock claims | As project |
| Project blackboard | As project | Project members propose changes (through Main) | Project admins, org admins approve, bound to the revision | As project |
| A claim | As project | The claimer: update, release, deliver (`merge-queue-and-delivery.md`) | Project admins, org admins unlock; a gate, deploy check or project admin reopens | None |
| Personal things (workspace, its board and blackboard, AI credentials, Git tokens, secrets, MCP connections, machines) | Owner only | Owner only; org policy can forbid use in that org (for AI credentials: by provider) | Owner only | None in 0.7.0 |

Rules that go with the table:

- **Org admins administer, they don't use.** They see and administer every project and team in
  their org, so they **can approve project plans and unlock claims** (Main's call). They can't
  use a team's compute, or work a project, until they add themselves as a team or
  project member. Every action taken through an org-admin grant is recorded as "via org admin".
- **Org members don't see projects** until a person or team is added to them (owner, decision 3
  in round 2). A project's creator is its admin.
- **Install admins have no org rights** through being install admin. On a fresh install the
  first person is both, as two grants.
- **Direct teamspace admins administer resources, not the team.** A direct teamspace-admin
  grant goes only to a member of the owning team and ends when that membership ends. It allows
  connect, reconnect, limits and remove on the teamspace's compute; it doesn't
  allow changing team membership or roles, or granting teamspace admin to anyone. Team admins
  have all of it.
- **One authority check.** Core is authoritative for orgs, teams, projects, memberships and
  grants. Runner and Board ask Core before any read, use, administer or delegate action, so every
  service accepts the same grants. Grant mutations (delegating, sharing, changing roles or
  membership) always go to Core itself, never to a cache. For read, use and admin checks a service
  may use a cache only while it is fresh: it carries Core's revocation version **and** confirmed
  that version with Core within the last 30 seconds. A version alone can't reveal a revocation the
  cache never saw, so during a disconnection a cache older than 30 seconds fails closed. Every
  resource can answer "why can this person access this?" with the grant used.

### Project tree (what)

- Projects belong to an org and nest: an org project contains team projects, which contain
  subprojects.
- Each project has a board and a blackboard. A parent project's blackboard can roll up its
  subprojects. *(The rollup view is the builder's call. The plan itself stays human-committed as
  in the 0.6.0 blackboard design.)*
- **Project plan authority** (owner, 2026-11-08): any project admin can approve a change to the
  project blackboard, whoever's Main proposed it. Every proposal is bound to the plan revision it
  was based on; a stale proposal is refused and must be redone, so two approvals never silently
  overwrite each other. Personal workspace blackboards stay approved by their owner only.
- Teams receive roles on projects at any level. A team can work across unrelated projects, and
  many teams can work inside one large project.

### Teamspace (how)

- Every team has a teamspace, and every org has one too, for the org-wide "how". Teamspaces
  inherit along the org tree: my team's teamspace inherits the org teamspace and adds its own
  items.
- A teamspace gathers the projects the team works on, so the team sees them side by side.
- A teamspace holds "how" resources: skills, gates, MCP connections, workflows and conventions,
  secrets, and compute.
- Each item applies either to all of the team's projects or only to named ones. Example: one
  review gate for every project, plus a stricter gate only for the large project.
- **A teamspace's items govern the team's own work.** A teamspace never imposes gates on a
  project for people outside the team. A project that needs rules for everyone, like an org
  gatekeeper, defines them on the project, or the org teamspace scopes them to that project.
- Example: the org teamspace defines a twelve-team end-to-end simulation gate scoped to project
  P. I can run it because my teamspace inherits the org teamspace and I work on P. The gate runs
  on the org's terms, with the org's resources and secrets. I only trigger it.

### Workspace (me)

- Workspaces stay personal: Main, channels, workers, and the workspace's own board and blackboard
  as in 0.6.0.
- **A workspace works in exactly one org, plus the person's own things** (owner, 2026-11-08).
  Cross-org work uses a separate workspace. This bounds every session to one org's data,
  policies and payer.
- A workspace draws resources from the person's teamspaces (all of a teamspace's projects, or a
  subset), from projects directly, and from personal additions such as a tool repository only I
  use.
- **What a session gets** starts from the union of the levels its workspace draws on (inherited
  org teamspace, team teamspace, the projects it includes, and the workspace's own items). That
  union is only the candidate set: a credential, runtime or endpoint is then usable only if it is
  allowed for every project in the workspace (round 2, decision 2).

### Resources

- A general resource model: a resource has a type, an owner (a person, a teamspace, a project or
  an org) and a scope (where it applies). New types can be added later without redesign.
- 0.7.0 builds: **repositories, boards and blackboards** at project level; **compute and AI
  accounts**: compute at teamspace level (BLG-607), AI accounts personal only (BLG-609); and
  personal MCP connections and personal secrets exactly as today. Team MCP connections and team secrets come in 0.8.0.
- Defining something and applying it are separate. A gate, skill or connection is defined where
  its owners maintain it, then applied to targets (all or named projects). Applying something to a
  project requires admin on that project, unless the item only governs the applier's own team's
  work.

### Secrets

- **Personal by default.** My GitHub token is mine and is never readable by an org.
- (0.8.0) A teamspace (team or org) can hold secrets for team use, like a team database password
  behind an MCP connection. **Simple by default** (owner, 2026-11-08): a secret a session can use
  is a secret the person running that session could see, because their code can print it. So
  using a team secret is granted only to team members, and the doc and UI say so plainly.
- **Every secret shows who has access to it** (owner, 2026-11-08): the people and teams, and
  through which grant.
- Secrets that must never be seen, such as an org gate's credentials, stay behind a trusted broker
  or gate that acts on the session's behalf without handing the secret over (with org gates,
  0.8.0).
- Org-level secrets exist only for org-level constructs, such as an org gatekeeper.
- Storage, delivery and rotation are designed in BLG-611. The existing rule that refresh tokens
  never land in a session holds.

### Compute: machines, models and AI accounts

- Compute can be added at the **individual** or the **teamspace** level.
  - Individual: a company-issued Mac Studio or RTX card that is mine. It stays "mine" by default.
    I can share it with my team, and revoke that at any time. Revoking stops any team work on it at once (round 2, decision 1); new
    ones are refused. *(Sharing a personal machine moves to 0.8.0; see the end.)*
  - Teamspace: a rack of 4× GB200 issued to the team belongs to the teamspace. Its admins control
    it, and members' sessions share its capacity.
- AI accounts are personal only in 0.7.0 (owner, 2026-11-09). My Claude login is mine; I can
  add any number of credentials, and nobody else uses them. A teamspace holds no AI credentials,
  and nothing is shared. Team-owned credentials, as service identities, come with team secrets
  in 0.8.0. (A model served by team hardware is 0.8.0 with local models.)
- Others never get my host. Team members' sessions on my machine run only in its sandbox.
- Owner priority, placement and load are designed in BLG-607 and BLG-609.

### Org policies

- Org admins can switch off what members may bring in personally: **personal compute and
  personal MCP connections**. Policies apply down the org tree.
- AI credentials are always personal, so there is no switch for them. Instead an org may
  **restrict which providers** its workspaces may use, for example Claude only (2026-11-09).

### Boards and claiming work

- Each project has a board. Each workspace has its own board, as today.
- **Claiming clones the ticket.** When someone claims a team ticket, Blerg copies it onto their
  workspace board and locks the team ticket to them. Others see "claimed by" and cannot claim it.
- While it's locked, the copy's status, commits and reviews flow back to the team ticket
  (only the named fields in round 1, call 3). Finishing the copy doesn't complete the team ticket
  by itself: completion follows the project's **completion rules**, defined in
  `merge-queue-and-delivery.md` (current claim generation, matching criteria and completion-rules
  versions, a reviewed delivery manifest whose merges landed through the queue). Releasing or
  abandoning it unlocks the team ticket.
- In 0.7.0, a stalled claim is freed by the claimer releasing it or a project admin (or org
  admin) unlocking it.
- **One coordinator per person per project is the intended use** (owner, 2026-11-09): separate
  streams are channels under the same Main. Two workspaces on one project are allowed but not
  encouraged, and nothing extra is built to prevent or coordinate them. The claim lock is what
  stops two orchestrators working the same ticket, which the startup ran into with two
  instances.

### Membership and sign-in

- Org and team membership is managed in Blerg with invites. This extends the 0.6.0 invites
  (BLG-208) to orgs and teams.
- Sign-in in 0.7.0 is username, password and an authenticator-app code (`sign-in.md`). Org admins
  invite people into their org; install admins create install accounts. OIDC sign-in (BLG-610) and
  group sync are 0.8.0.

### Upgrading existing installs

- On upgrade, each install gets one **default org**. Existing accounts become its members and the
  existing install admin becomes its admin.
- Every current workspace, board, blackboard, secret, machine and login stays personal, exactly
  as today. Nothing changes until someone creates a project or a teamspace.

### Solo use stays simple

- **A desktop or homelab install never has to use teams** (owner, 2026-11-09). One person works
  in personal workspaces exactly as today, with their own credentials, machines and boards. The
  default org exists underneath but stays out of sight: no team screens, prompts or extra setup
  steps until someone creates a teamspace or invites a second person.
- **Projects work solo too** (owner, 2026-11-09). A single person can create a project and attach
  it to their workspaces, without any team, as a unit of organization: its board, blackboard and
  repositories. (A project brief for agents is not in 0.7.0.)

## Constraints

- Three services own their data: Core (identity, orgs, teams, projects' registry, roles,
  grants, secrets), Runner (workspaces, sessions, machines, teamspace how-items, and **project
  blackboards**) and Board (projects' boards and tickets). Project blackboards live in Runner
  because Runner already holds blackboards with revision-bound proposals and approvals; they are
  keyed by the project ID that Core registers, and every read or approval checks Core's grants.
  *(Main's call.)*
  The model has to be enforced in each, not only in the UI.
- Isolation between orgs and teams needs a cross-model security review of the design before any
  build, and again before the alpha.
- Deterministic rules over LLM judgment for permissions and policies (owner rule).
- Person-facing vocabulary: workspace, Main, channel, worker, teamspace, project, org.
- American English in person-facing text.

## Decided by the owner (2026-11-08)

- Built for enterprises from day one: multiple orgs, sub-orgs and teams, separate admins.
- Hierarchical primitives, with personal scope beside the org tree. Claim org tickets into my own
  space.
- Personal secrets by default. Org secrets only for org constructs. Teamspace secrets allowed.
- Role-based access, with each org deciding its use. Three built-in roles for 0.7.0.
- Workspace is personal. Teamspace is the team's home and its "how". Projects nest and hold the
  "what". Teams work across projects.
- Teamspace items can be scoped to specific projects. Teamspaces inherit from the org teamspace.
- A general resource model; five types in 0.7.0.
- Claiming clones the ticket and locks the team ticket; an admin unlocks stalled claims for now.
- Compute at the individual or teamspace level; org admins can switch off personal compute,
  personal AI accounts and personal MCP connections. *(2026-11-09: AI accounts are personal only;
  the switch for them became an allowed-providers policy.)*
- Invites for membership; the default org on upgrade.
- One org per workspace for now; simple team secrets with visible access; any project admin
  approves project plan changes, bound to the plan revision; teamspace MCP connections, secrets,
  skills, gates and workflows wait for 0.8.0.

## Left to the builder

- Data model and storage per service, IDs, and the migration mechanics for the default org.
- How permission checks are structured so custom roles fit later.
- How union and inheritance are computed and cached for sessions.
- The UI for orgs, teams, projects and teamspaces (follow the UX skill: inviting, joyful, works at
  390 px and at 4K).
- Blackboard rollup presentation for nested projects.

## Review round 1 (2026-11-08): contracts added

An adversarial review (Codex, 12 findings, 8 high) found the draft under-specified at its
boundaries. The owner decided four of them above (one org per workspace; simple secrets with
visible access; project plan authority; teamspace MCP connections and secrets in 0.8.0). The
contracts below are **Main's calls** (the owner may overrule); they define behavior the build must
meet, while storage and code shape stay the builder's.

1. **Role inheritance has boundaries.** Grants are additive and flow down their own tree only.
   Org membership alone grants org-level items, never a descendant team's compute or (later)
   secrets: those need membership in that team or a team below it. A team's members get
   their own teamspace's items plus what it inherits from ancestor teamspaces, never siblings'.
   Project roles flow down the project tree. A restricted sub-team or subproject is expressed by
   granting at that node, not at its parent. Read, use, administer and delegate are checked
   separately, for direct and inherited grants.
2. **Revocation takes effect at the next use.** Removing a member, a role, a share or an account
   is checked at every launch, resume, credential refresh and tool or account use, including
   children, crons and queued work; unavailable authority fails closed. Running work that depends
   on the revoked access is **stopped at once** (owner, round 2, decision 1). Cleanup (stopping and
   collecting the person's own sessions) stays allowed.
3. **Claims cross the personal boundary only through named fields.** What flows back from a
   personal copy to the team ticket is limited to status, claimer, linked commits on project
   repositories, and review verdicts with links the team can open. Private diffs, personal
   repositories, personal artifacts and comments never flow back. When the claimer loses access,
   synchronization stops; their copy keeps what they already saw, and is marked detached.
4. **Claims are fenced by a claim generation.** Board owns a versioned claim state on the team
   ticket. Each claim gets a unique generation; every update from a personal copy carries it and
   is refused if stale, so an unlocked or reclaimed ticket can never be completed by an old copy.
   Claiming is a single atomic step on Board; copying to the workspace board is idempotent and
   recoverable. A personal copy never has unconditional authority over the source.
5. **One canonical registry binds resources to orgs.** Core is authoritative for orgs, teams,
   projects, teamspaces and which org owns each resource. Runner and Board validate that every
   referenced org-owned resource (a project, a board, a blackboard, team compute) belongs to the
   session's one org before use, not only that the person exists. Personal resources stay the
   person's and follow round 2, call 4. Moves and deletes go through Core and are
   reconciled in the other services; unknown bindings fail closed.
6. **A team's items only act within the team's authority.** (Relevant from 0.8.0, when team gates
   and connections arrive.) Anything that changes shared project state, blocks others or spends
   shared capacity needs project authority; "own team" never means "affects only the team" unless
   the item's capabilities are actually confined.
7. **Install admin separation is about application rights, honestly stated.** An install admin
   has no org access through Blerg itself. Infrastructure operators with database, backup or key
   access remain trusted; Blerg does not claim cryptographic isolation from them. Exceptional
   operational access is audited (BLG-614).
8. **Upgrade activates nothing.** The default org starts with permissive policies, no projects,
   an empty org teamspace and no shared compute, so no existing personal workspace, secret, board or login is
   reachable by other members or affected by policy until an admin creates something or turns a
   policy on. Mapping the old install admin to default-org admin is an explicit compatibility
   rule for the default org only. Upgrade, rollback and mixed daemon versions are tested; older
   daemons cannot run team work.
9. **Payer and identity are recorded.** Every operation records the invoking person and the
   credential used. In 0.7.0 the credential's owner is always the invoking person (BLG-613).
10. **One acceptance path gates 0.7.0.** End to end on a cloud install, in this order:
    1. The first person completes first-run setup (`managed-install.md`); they are install admin
       and default-org admin.
    2. They invite the other two into the org (as org admins, for the startup). All three set a
       password and enroll an authenticator app.
    3. Each person adds their own Claude and Codex credentials from [+] in personal settings, as
       subscription seats. No team needs creating, and the org teamspace stays empty. API keys
       are not an acceptable fallback; if the BLG-574 spike fails, the alpha is blocked (owner,
       2026-11-09; `credential-handling.md`).
    4. Each person connects their own Git token; it passes the reachability check against the
       project's repositories once they're registered (`credential-handling.md`).
    5. One person creates the project (becoming its admin), registers its repositories, and adds
       the other two as project admins.
    6. Each person creates a personal workspace in the org that includes the project, with
       their own credentials.
    7. One person's Main proposes the project blackboard from that workspace; any project admin
       approves it, bound to the revision; all three see the same committed plan.
    8. Each claims a ticket and works it with their own credentials, including a credential
       renewal and a needs-sign-in alert to the owner; work merges through the queue and the claim is delivered
       (`merge-queue-and-delivery.md`).
    Dependencies: BLG-606 (this), BLG-609 (accounts and placement), BLG-563 and BLG-564
    (sign-in), BLG-611 (credential handling), BLG-612 (install), BLG-480 and BLG-313 (merge
    queue). Everything not on this path is deferred or optional.

## Comparison with established software (2026-11-08): refinements

A comparison against Google Cloud, AWS Organizations, Azure, GitHub, GitLab, HCP Terraform,
Vercel, Atlassian, Linear, Notion, CI runner groups and Copilot seats (published report
`canonical-team-scope.md`) found the pieces standard and their combination distinctive, and kept
the people / work / how / me split. These refinements are **Main's calls**; the owner may
overrule:

1. **Capabilities, not just role names.** An action matrix (read, use, administer, delegate) for
   each resource type, covering direct and inherited grants: see "Permission matrix" above. A broad ancestor grant
   cannot be narrowed below it (as in GCP and GitLab), so restricted areas are granted at their
   own node. Every resource can answer "why can this person access this?".
2. **Restrictions beat grants.** For the org policy switches, an ancestor prohibition wins; local
   grants never cancel it; an exception needs authority at the prohibiting level. Policy previews
   show the effect before saving.
3. **Sessions get only what their task needs inside the org.** One org per workspace bounds
   tenancy; within it, a session receives the resources of the projects its workspace includes,
   not everything the person could reach.
4. **Lifecycle and ownership.** Manual offboarding in 0.7.0 must be complete and audited: remove
   memberships, stop the person's team work, rotate team credentials they used (none in 0.7.0
   since 2026-11-09), and transfer
   ownership of team-owned resources. Protect against removing the last admin. A person's private
   credentials are never transferred. Non-human principals (crons, integrations, workers) get
   scoped, expiring identities.
5. **A claim is a linked execution record.** The team ticket stays authoritative for title,
   description and acceptance criteria; the personal copy keeps its source ID and shows whether it
   is synced or detached. Completion follows the project's completion rules
   (`merge-queue-and-delivery.md`), not just a personal status change.
6. **Every org has a teamspace from the start, empty.** On upgrade the default org's teamspace
   exists but holds nothing, consistent with "upgrade activates nothing".
7. **Naming in the UI:** say "Personal workspace", "Install administrator" and "Organization
   administrator" explicitly. Whether to call nested orgs "sub-orgs" or "groups" is a wording call
   for the UI pass.

## Review round 2 (2026-11-09)

A second adversarial review (Codex, 14 findings, 5 high) checked the round-1 contracts and found
contradictions and gaps. The owner decided three; the rest are **Main's calls** (the owner may
overrule). Where these conflict with earlier text, this section wins.

**Owner decisions:**

1. **Revoked means stopped, at once, in every case.** Unsharing a machine, removing
   a member or role, an org policy denial, deleting a credential and a security revocation all
   stop the affected running work immediately and refuse new work. The workspace, its
   uncommitted changes and its transcript are kept; the session shows as stopped with the reason,
   and the person can resume on something they're still allowed to use. Blerg stops refreshing at
   once; an access token already handed out stays usable until it expires, which is why access
   tokens are short-lived. **Refined by the owner (2026-11-09):** work is stopped at once when its
   executor is reachable; an unreachable executor stops itself within 30 seconds (call 5).
2. **Everything used in a workspace must be allowed for every project in it.** A credential,
   runtime or inference endpoint can be chosen for a workspace only if it is allowed for all the
   projects the workspace includes; the new-workspace screen doesn't offer ones that don't fit.
   The rule is rechecked when a project is added. For a personal credential this means its
   provider is allowed by the org (2026-11-09).
3. **Projects are private to their people.** The creator is the project's admin. Org members see a
   project only when a team or person is added to it; org admins see every project in their org.
   Someone invited as a member never sees an existing project until it is shared with them.

**Main's calls:**

4. **Personal things are owned by the person, not the org.** The one-org rule binds org-owned
   resources. A personal resource (credential, repository, secret, the workspace's own board and
   blackboard) stays the person's; the person brings it into a workspace, where the workspace's org
   policies apply to its use. The same personal resource may be used in workspaces of different
   orgs; it is never exposed to org members and its ownership never moves.
5. **One admission record fences execution.** Runner is the admission coordinator. A launch writes
   one durable reservation for the runtime, with a lease generation. The credential is checked
   (owner, provider allowed, connected) but not reserved (2026-11-09).
   The executor must hold the current generation to keep running; when a lease expires (for
   example a daemon disconnects), the old executor is told to stop, and its capacity is reused
   only after the stop is confirmed or the stop deadline passes. Where a target can't confirm a
   stop, the reservation is held; the hard limit is never silently exceeded.
   **Executors fence themselves** (owner, 2026-11-09), so decision 1 holds within 30 seconds even
   when Runner can't reach the executor:
   - Every executor (a cloud Job's pod, a daemon) renews its lease with Runner every 10 seconds.
     A renewal must carry the current generation and fresh authority; Runner refuses it after any
     revocation.
   - An executor that can't renew within 30 seconds stops its own engine processes and workers
     locally, without waiting for a command, and keeps the workspace, uncommitted changes and
     transcript. So a connected executor stops as soon as the stop arrives, and an unreachable one
     stops itself within 30 seconds of the revocation. A shorter bound is impossible for a
     machine nobody can reach; an access token already handed out still lives until it expires.
   - Runner also stops cloud Jobs through the Kubernetes API, which doesn't depend on the pod's
     connection.
   - Capacity is reused only on proof: the executor confirmed the stop, Kubernetes reports the
     pod gone, or the lease expired on an executor version known to self-fence (its own clock
     stops it; Runner adds a 15-second margin). Otherwise the reservation is held.
   - Daemons that can't self-fence can't run team work (round 1, call 8).
   - **Connectivity loss:** during a partition the 30-second self-stop applies to all team work,
     meaning any work that uses something its executor's owner doesn't own (team compute, an
     org project) and all work on cloud executors. Only a person's own work on
     their own machine with only their own resources keeps running through a network blip. The
     cost: on a desktop, a blip longer than 30 seconds stops team work, which resumes from the
     kept workspace.
   - **Security revocation** (an account disabled or marked compromised) also stops that
     personal work. A partitioned daemon can't hear it, so it is enforced at reconnect: Runner
     accepts nothing from the daemon until it has stopped every session of that account and
     confirmed the stops, and refuses its enrollment after that. No work continues past the
     reconnect. All of the account's team work is already stopped by the 30-second rule.
6. *(Superseded 2026-11-09: no throttling.)* **Fair share is per person, per pool.** The fairness unit is the invoking person across all
   their workspaces and workers. Each person may use up to their equal share of a team pool's
   worker slots while others are waiting, and borrow idle capacity when nobody is. Main reserves a
   slot only while it is running a turn. A meter older than its freshness limit or in an unknown
   state counts as "no headroom known": allowed only within the hard concurrency limit.
7. *(Superseded 2026-11-09: the same login on several cards is allowed and not coordinated.)* **Duplicate detection is honest.** Credentials are "verified" when the provider identifies the
   account, otherwise "unverified". Unverified credentials never count as independent capacity
   beyond their own hard limit. Using one personal login in two orgs is allowed but not
   coordinated; the docs say quota is shared and unattributed across orgs.
8. **A claim belongs to one workspace.** A claim binds to exactly one workspace's copy plus its
   generation. A retry from that workspace recovers the same copy; any other workspace, including
   the same person's, gets a conflict. Reclaiming a ticket stops the old claim's ticket-linked
   workers (decision 1). The lock prevents duplicate claims and stale updates; it doesn't stop a
   person running unrelated work on the same repository.
9. **Team features turn on only when a person acts.** The persisted activation event is: an admin
   creates a teamspace item, invites a second person, or shares something. The empty org
   teamspace and solo projects never activate team screens.
10. **Privacy promises cover Blerg's own metadata.** "No provider identity through Blerg" means
    Blerg's APIs, UI, logs and Main's context never carry it from Blerg's own records. Agent and
    provider output (anything a session prints) is outside that promise; Blerg redacts known token
    shapes on a best-effort basis. Exposure by credential mode: refresh tokens never enter
    sessions; access tokens are short-lived and visible to the session; static API keys are visible
    to the session for their lifetime (the card says so); local-endpoint keys likewise.
11. **Any credential change gives Main a fresh native identity.** Changing Main's credential, even
    within the same service, stops the old process at the end of its current turn, starts a new
    native conversation under the new credential, and carries context from Blerg's archive.
    Re-authorizing the same credential keeps the conversation.
12. **Completion binds to a versions tuple.** Board records the source ticket's criteria version
    and the project's completion-rules version. Completing a claim requires both to match what the
    review covered, checked atomically with the claim generation. Any change to either invalidates
    the review in 0.7.0 (no materiality judgment); the copy resyncs and is re-reviewed.
13. **The relay and direct URLs share one authority model** (0.8.0 with local models; see the
    end). Inference traffic on the daemon
    connection is scheduled below control traffic with reserved control capacity, per-stream and
    aggregate limits, and is cancelled on authority loss or disconnect. Direct URLs are called by
    Runner, not by workers, with the destination pinned at approval (no redirects, DNS rebinding
    checks, approved private addresses only when the owner allows), so the same per-session
    checks apply. Blerg doesn't claim to isolate a model server it doesn't run; the card labels it
    "trusted external endpoint". Local inference stays low priority and ships only with these
    limits.
14. **Invariants are mandatory beyond the happy path.** Compatibility, isolation and every owner
    decision are release requirements, not optional extras. The 0.7.0 gate adds focused tests for:
    solo upgrade, solo project creation and the first invite; a personal resource in two orgs;
    multi-project credential eligibility; same-person claims from two workspaces; lease recovery
    with a partitioned daemon and a partitioned cloud Job, where the stop command is never
    delivered; an account disabled while its daemon is partitioned, stopped at reconnect;
    same-service credential cutover; and revocation stopping work.

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

Made the doc consistent with the review rounds and both readiness reviews. Changes:

- OIDC (BLG-610) is 0.8.0 everywhere; 0.7.0 sign-in is username, password and an
  authenticator-app code.
- Roles flow down their own tree only, within a new permission matrix (Main's call).
- Org admins see and administer every project and team in their org, including approving
  project plans and unlocking claims, but don't use team resources without joining (Main's call).
- Org members create projects; org admins create teams (Main's call).
- Project blackboards live in Runner, keyed by Core's project ID (Main's call).
- "What a session gets" is a candidate set, narrowed by the all-projects eligibility rule.
- Claim completion links to the completion rules in `merge-queue-and-delivery.md`.
- Round 1 call 5 binds org-owned resources only; personal resources follow round 2 call 4.
- The acceptance path is ordered: setup, invites, org teamspace, Git tokens, project and
  repositories, workspaces, blackboard approval, claims and delivery.
- Executors fence themselves within 30 seconds when Runner can't reach them (owner approved, 2026-11-09).
- The partition tests include a stop command that never arrives.
- Machine sharing and the relay are marked 0.8.0 where the body mentions them.
- Revocation is stopped at once when reachable, within 30 seconds when not (owner, 2026-11-09);
  decision 1 and call 5 now say so.
- Matrix: a new row gives recipients of a shared personal AI credential use and metadata read;
  administration stays owner-only. *(Removed 2026-11-09.)*
- Matrix: team viewers see team compute and credentials as status only.
- Matrix: teamspace admins are defined; direct teamspace admins connect and reconnect but can't
  change membership or grant admin; the delegate column is complete.
- The authority check covers delegate and grant mutations; a cache older than 30 seconds fails
  closed.
- Call 5 separates connectivity loss (30-second self-stop for all team work) from security
  revocation (personal work stopped at the daemon's reconnect); a test is added.
- The acceptance path says team Claude seats are subscription seats; API keys are not a fallback
  (owner, 2026-11-09).

## Simplification (owner, 2026-11-09)

"We still uncap how many providers a user can have, but we don't let a team add one and share
it." "We're still reading meter limits like we do, don't overcomplicate or try to throttle."
Where this conflicts with earlier sections, this section wins.

- **AI credentials are personal only.** Any number, any mix, from [+]. Teamspaces hold no AI
  credentials; nothing is shared. The matrix loses the shared-credential row and the credential
  part of the teamspace row.
- **Org policy:** the personal-AI-accounts switch is replaced by an allowed-providers policy; the
  personal compute and MCP switches stay.
- **No throttling:** fair share (call 6) and duplicate coordination (call 7) are removed. Call 5's
  admission record covers runtimes only; its leases and the 30-second self-stop stay.
- **Success and acceptance path:** "each person's own Claude and Codex credentials" replaces
  "the team-owned collection of logins"; the org teamspace step is gone.
- **Payer:** the credential's owner is always the invoking person.
- **Team gates** need no AI credential (`merge-queue-and-delivery.md`). Org gates with their own
  identity, and team-owned credentials as service identities, are 0.8.0 with team secrets.
- Same login across people: see `accounts-and-placement.md`, "Examples".

### Teamspaces in 0.7.0 (owner, 2026-11-09)

"Teamspaces might still hold value as a model and we can add more UX as we go if necessary."
0.7.0 keeps the teamspace in the data model (one per team and one per org, inheriting along the
org tree, gathering the team's projects) with minimal UI: a team's page lists its projects and
members. It holds no credentials (personal-only) and no hardware (0.8.0) yet; team MCP
connections, secrets, skills, gates and hardware attach to it in 0.8.0 without a redesign.
