# Cluster workspace runtime integration

P3 consumes persisted P0 selections through `SessionJobSpec.Workspace` and
private `WorkspaceMembers`. P1 owns human selection authorization. P3 never
infers membership from a folder or from mutable repository configuration.

Each session has its own retained PVC mounted only at `/workspace/projects`; HOME
and runtime scratch are outside it. Its storage mode is decided by the server at the
session's first launch, stored with the claim, and never changed:

- **bounded** when `BLERG_RUNNER_WORKSPACE_STORAGE_CLASS` is set: the claim is in that
  class, and the pod refuses a volume without enforced byte and inode capacity.
- **standard** otherwise: the claim is in `BLERG_RUNNER_WORKSPACE_DEFAULT_CLASS` (the
  installer sets it to the class of `blerg-runner-data`), or names no class and gets
  the cluster's default. The pod skips the capacity check; the checkout/object
  budgets are still counted at preparation and at every sync round, but nothing
  physically bounds the workspace (see [install/k8s/README.md, "Workspace
  storage"](../install/k8s/README.md#workspace-storage)).

The pod learns the mode from the server's plan and never chooses it; an agent image
older than the server enforces bounded storage. Missing quota in bounded mode,
changed claim UID, unavailable credentials, uncertain previous writer cleanup, and
unsupported negotiation refuse preparation. Never delete retained claims just because
the Job ended or every committed head was pushed.

## Production adapter binding

`cmd/server` registers the concrete P1 launch consumer. Authorized hosted members
use their stable ID relative to `/workspace/projects`, with provider and repository
destination kept separately. The consumer persists the owner and immutable
selection before `JobManager` prechecks each member and allocates retained storage.
A child resolves the persisted parent contexts and fetches credentials afresh.
`protocol.ClusterWorkspaceEnabled` is true following the real-pod happy-path
smoke. It controls both cluster workspace and owner-shell policy; actual protected
storage, containment, configured consumers and negotiated services remain required. Linux/WSL host and local sandbox
use their own enabled switches.

Each fenced workspace generation retains one consumer Job name, while a
`WaitForFirstConsumer` claim may still be Pending. PVC, Secret and Job creates
retry explicit Kubernetes `Conflict` responses from optimistic quota accounting
with jittered exponential backoff, bounded to ten seconds per object and the
shared launch deadline. Retries keep the same request and create marker. A lost
response requires read-back verification of that marker, namespace, labels,
owner references and a stable nonempty UID before reporting success. Foreign
`AlreadyExists`, opaque 409 responses and permanent quota/admission refusals do
not authorize retries or adoption. Model-catalog probe creates use the same policy.

The `storage`, `secrets` and `queued` launch stages retain allowlisted Kubernetes
reason classes, including `Conflict` when retries exhaust their deadline.
Malformed responses fall back to an HTTP reason class.
Local scheduling policy and transport failures have separate reasons. Server logs
and acceptance smoke receipts retain these classes without API messages, validation
details or manifests. The session-start HTTP 409 alone does not identify the
Kubernetes response. Failed submission cleans up the newly allocated unused Secret.

`cmd/runner` calls `ConfigureClusterRuntime` before `Run`. The workspace Job uses a
fixed `setpriv` bootstrap that drops to controller UID 1001, with only CHOWN,
DAC_OVERRIDE, SETUID, SETGID, KILL and SYS_PTRACE. The Go runner must be PID 1 in
its container PID namespace. HOME, temporary files, private runtime scratch and
the retained projects PVC have separate mounts. The init container sets
`/run/blerg-workspace-private` to UID 1001, mode 0700. Startup verifies ownership,
permissions, a distinct mount identity and the actual required capabilities.
There is no environment path override for private storage.

Agent and PTY exec drop to UID/GID 1000 and explicitly clear ambient and
inheritable capabilities before the engine or account shell starts. Loader
injection variables are removed before trusted helper execution. The inherited
no-new-privileges and seccomp restrictions prevent capability and untraced-clone
escapes. Same-UID directory permissions alone do not provide this boundary.

Each engine command has a private inherited control descriptor and a dedicated
kernel tracer. The tracer tracks forks, threads and detached descendants before
they execute; EXITKILL covers helper failure. Freeze acknowledges only after all
tracked tasks have kernel stop records. Thaw resumes that same tree. Stop waits
for reaping and returns an explicit empty receipt. The runner and Git workers
stay outside these trees. Uncertain helper loss fences settlement and retains
storage. Plugin install commands use the same launcher. Launcher-created engine
support files are transferred through bounded descriptor-rooted traversal;
private runtime state is excluded.

P3 owns the outer workspace coordinator. P5 owns a separate leaf mutex. Atomic
shell admission tries the outer mutex before creating a PTY; a contended open
refuses promptly so it cannot deadlock the lifecycle worker. Syncing precedes
freeze, readers cancel and join before copying, and Running follows verified
thaw. Only the immutable copy runs frozen; publication runs after thaw.
Unchanged heads and same-head push retries retain the existing no-freeze path.

P5 reconciliation retains every coordinator from this physical pod process,
including replaced connections and failed configurations. It requires actual
cleanup receipts for their reservations and helpers. A new map or lost helper
cannot manufacture an empty proof. The fresh PID namespace is checked at pod
startup; uncertain cleanup requires retained recovery and replacement.

A member depth-policy refusal does not end aggregate accounting: safe pinned
no-symlink/no-mount traversal continues up to a separate depth ceiling of 256.
Only complete accounting permits unrelated members to publish. Unreadable,
cross-mount, over-ceiling or canceled accounting leaves usage unknown and blocks
the entire round; physical capacity alone cannot prove logical category limits.

`Config.ClusterServices` binds P4's descriptor-pinned `LocalExplorer` inside the
pod after clone-all and writer admission. Explorer and file viewing share bounded
reader registrations, cancellation and join. P5's registered service handles its
versioned RPC, signing key, grants, leases and PTY lifecycle on the existing
outbound connection. Production cluster shell capability follows the enabled cluster switch and verified runtime readiness. The P3 adapter envelope also dispatches through that same P5 service.

Requests travel only on the pod's existing authenticated outbound socket:

- `workspace_explorer` / `cancel_workspace_explorer` use P4's request envelope.
- `owner_shell` / `cancel_owner_shell` are P3's adapter envelope: `request_id`,
  `session_id`, `launch_generation`, `generation`, plus P5's verified operation
  data. This envelope is not a grant. Versioned P5 manager RPC uses its dedicated authenticated handler;
  do not treat runtime identity as human authorization.
- Replies are `workspace_explorer_result` and `owner_shell_result`, stamped with
  the trusted request/session/runtime reference. P3 limits one explorer and four
  shell operations, 32 KiB requests/shell replies, 1 MiB explorer replies and
  two-second reply lock/write waits. P5 additionally enforces decoded frame,
  aggregate rates, leases and output queue limits. Shell bytes never become
  agent input, transcript events, history, artifacts or broadcasts.
- `DaemonConn.SetClusterWorkspaceReplyHandler` installs P4/P5's bounded private
  request correlator. It receives only current authenticated, prepared-generation
  replies. Register only current requests; never log raw payloads.
- Disconnect cancels active operations and old-link delivery before reconnect;
  Cancel must synchronously fence and cancel backend work. Close joins readers
  and closes held descriptors before settlement. Transport loss is not writer
  cleanup proof. P6 still needs cross-feature transport fairness under load.

Workspace-v2 refuses legacy unqualified commit reads; file viewing uses P4's
member and runtime-bound reader. Scalar legacy sessions retain their existing behavior.

## Replacement and stop settlement

Replacement re-reads the immutable manifest, results, private contexts and member
identities after physical writer fencing and token revocation under the runtime
launch fence. A success reported by the old generation during prechecks is the
new plan's starting state, rather than a stale pending retry.

For workspace-v2, Stop first revokes agent work authority under the frame fence
while retaining the existing runtime socket's authority for its final report.
New runtime hellos refuse revoked work authority. The server sends
`cluster_workspace_stop` with `request_id`, `session_id`, `launch_generation` and
`generation`; the pod synchronously fences admissions, closes readers and kills
and settles the agent/writers. Its `session_ended` carries `workspace_stop_id`
and `workspace_revision` alongside the complete member results. Only a matching
request/incarnation and a newly persisted revision acknowledge the stop. The
server then finishes Job deletion and terminal marking with the requested reason.
Migration 080 adds P3's private `settled_generation` ledger marker. Final outcomes,
revision and this exact-generation marker commit atomically; later reports cannot
rewrite them. Cleanup retries reuse that durable evidence without requesting a
second report, including after disconnect or server restart. Replacement runtime
generations cannot reuse the marker.

The report wait is bounded to 85 seconds, within a 95-second coordinator budget.
An unreachable peer, full command queue or timeout produces conservative pending
results with known heads and retained recovery paths. The trusted coordinator
persists that evidence and revokes runtime authority atomically under the frame
fence, sealing that generation before terminal cleanup; arbitrary late frames remain refused. Workspace
auto-stop runs this handshake off the read pump without an early terminal claim.
No Kubernetes exec API is involved. PVCs are retained in both cases.

Trusted cleanup reads lifecycle state independently of runtime-frame admission.
An already terminal session can be cleaned up without another final report,
while preserving its results, original status, end timestamp and attribution.
Terminal or recoverable observations revoke runtime/work tokens before the
existing UID-preconditioned foreground Job fence proves Job and pod absence.
A recoverable observation without a final seal records pending uncertainty
through the trusted coordinator, then confirms the explicit stop. Runtime
terminal/token/result guards remain unchanged.

A missing storage ledger permits cleanup only for generation zero with no root
binding and positively absent workspace PVC, Job and pods, under the lifecycle
lock. Missing records for allocated generations, an ambiguous claim, failed
trusted reads or unproven teardown remain retryable failures; claims are retained.
Runner Stop, workspace browser DELETE and connected-owner discard share this
coordinator. Browser DELETE returns 503 when cleanup fails instead of reporting
204 after discarding the error.

Pod resources are fully validated before the credential Secret is allocated, so
an invalid pod cannot strand a new Secret or delete one owned by a live Job.

## Children

The existing child endpoint accepts `repo` for one parent member ID, `repos` for
an explicit subset, or `all_repos: true`. CLI `session start --repo` is repeatable;
a single flag retains the legacy scalar body. Folder selections are resolved in
parent order before hashing, rules/grants, limits and approval. Duplicate,
foreign, ambiguous friendly-name, conflicting and cross-runtime selections
refuse. The approval card and unloaded Needs you review list the exact IDs.

Dispatch re-reads the parent manifest and server-private contexts, rechecks the
stored launch, then creates a fresh child manifest, volume and runtime binding.
Each member checks out `wip/<child-id>`. Credentials are fetched afresh for the
owner, and shell grants are not inherited. Resume uses the child's retained
manifest/results/claim, never the parent PVC or a remote reconstruction fallback.

## Real-cluster acceptance still required

Run against the production image, nonprivileged pod user and chosen CSI driver:
capacity/inode exhaustion, HOME/credential exclusion, mixed providers/duplicate
names, exact-host credential refusal, public anonymous clone/no push, rollback,
partial push + retained-PVC resume, Job/Pod UID replacement fencing and stale
socket replies. Verify physical containment for detached agent/shell descendants,
freeze/thaw/stop failures and recovery, orphan cleanup after pod restart, reader
join before storage release, policy revocation/expiry, slow/flooding PTY output
alongside explorer/Git traffic, and no `pods/exec` permission. These checks and
independent adversarial review are release gates, not established by fake tests.
