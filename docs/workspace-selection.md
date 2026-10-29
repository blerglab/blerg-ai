# Workspace selection (0.5)

The launch sheet uses one checked folder tree for **This machine** and **Local
sandbox**, rooted at the desktop owner's configured repositories folder. Expand
folders to browse, select individual Git repositories, or check a folder to
snapshot its repositories. Checking a folder with more than twenty repositories
requires an explicit smaller selection. The cluster picker searches hosted
repositories by provider and org/name and supports up to twenty selections.
Local-only folders never become cluster repositories by matching their names.
An empty folder can run on This machine only.

Desktop discovery requires an enrolled, person-owned desktop. Administrators do
not gain access to another person's desktop inventory. Unknown desktops appear
only in ownership Settings until assigned and enrolled. Reconnects, root changes
and ownership changes invalidate a selection. Refresh and select again; Blerg
never silently adopts newly discovered repositories or launches a truncated list.

Folder workspaces preserve relative repository paths. Separately picked
repositories on desktop, sandbox and cluster runtimes appear at the workspace root under their names, such as
`ai-playground/` and `nika/`. Unsafe names use the source ID instead. Names are
compared after Unicode normalization and case folding; duplicates receive `-2`, `-3`, and so on in selection order,
skipping suffixes that would collide with another selected repository's name.
Names are truncated at a UTF-8 boundary as needed to fit 255 bytes including any
suffix.

On desktop runtimes, for two or more separately picked repositories, **Keep folder structure** places
each at its path relative to the deepest directory all picks share. For example,
`home/repositories/ai-playground` and `home/dev/ll/api` appear as
`repositories/ai-playground` and `dev/ll/api`. Nothing above that common ancestor is
projected. Only selected repositories appear; intermediate virtual directories
contain only the paths leading to them. If any projected component fails the
public explorer's safe-segment policy, the whole selection uses names instead and
the picker explains the fallback. Nested repository picks and linked worktrees
sharing Git data remain rejected. The option defaults off and remembers the last
choice for the signed-in account in this browser, like other launch preferences.
The requested layout is bound into the selection identity and manifest digest;
children retain their parent's layout and paths. Desktop single-repository and
checked-folder selections are unchanged. Hosted cluster selections put every
repository, including a single repository, in its own named directory under `/workspace/projects`. Clones never use the
volume root. The case-folded name `lost+found` is reserved for filesystem metadata
and falls back to the member ID. Sessions start in the first member directory;
HOME and runtime metadata stay outside the public root. Stored legacy cluster
manifests with no layout field keep their ID directories for restart, resume,
stop and children. **Keep folder structure** is unavailable for hosted
repositories because they have no local folder layout; cluster requests carrying
`layout=structure` are rejected.

On the host, separately picked repositories form a virtual tree and the first explicit selection is
the shell anchor. A selection itself grants no shell permission. Runtime consumers
must advertise workspace v2 before a new selection can launch. A desktop that
only supports discovery can preview a new folder or multi-selection but cannot
run it yet; discovery-capable peers require a v2 consumer even for a single selected
repository, so an immutable reference is never reduced to a bare name. Older
peers without discovery retain their legacy single-repository launch behavior. One authorized hosted repository likewise retains the existing
cluster launch path.

Children select the immutable parent membership with repeatable
`blerg-runner session start --repo <member-id-or-unambiguous-name-or-path>` or
`--all-repos`. They retain the parent's owner, runtime and member paths. A newly
created or discovered repository does not expand a running parent's membership.
Work within the listed runtime paths, commit each changed repository separately,
and report every commit ID. Legacy single-repository parents on old peers retain their prior
single `--repo` / `--no-repo` behavior. Discovery-capable desktops require immutable
source bindings for existing repositories; scratch/create/clone are separate intents.

## Desktop confidentiality upgrade

Desktop inventories, models and source metadata are collected only after exact
persisted owner and connection checks. The browser cache is scoped to the signed-in
account and invalidated after ownership changes and reconnects. Board proxies
forward the actual signed-in person's proof; an automation credential never lends
its owner's desktop metadata to somebody viewing a board.

Automatic upload of desktop skills, rules, memory and engine configuration to an
install-wide agent-config archive has been retired. The server ignores historical
`agent-config.tar.gz` uploads and returns 410 to automatic uploads. Remove that old
archive from the runner data volume during upgrade. Desktop-local configuration
remains local; existing sessions do not copy it into another person's runtime.

Operators who intentionally provide shared, nonpersonal configuration can set
`BLERG_RUNNER_SHARED_AGENT_CONFIG_BUNDLE` to an absolute path to their archive.
This explicit operator input is shared with runtime sessions on the installation;
keep personal desktop files and secrets out of it. An unset value serves no shared
archive. It does not re-enable desktop uploads or turn a historical cache into a
shared source.

## Integration boundary

P1 supplies daemon discovery version 1 separately from workspace runtime version
2. Its `discover_workspace` control-plane RPC performs list, expand, resolve, bind
and child-bind requests on one current connection, with bounded time, entries,
bytes and concurrency. Errors use fixed codes without filesystem paths.

The server derives the immutable P0 manifest from freshly resolved references.
Private source paths remain in the daemon's selection service. The local runtime
consumer calls `Manager.PinWorkspaceSelection` for descriptor-pinned sources;
preparation, cloning, runtime persistence and recovery belong to P2. P3 registers
`WorkspaceClusterConsumer` for allocated roots and launch dispatch. Until those
consumers are integrated and advertise v2, new selections fail explicitly.
P4 consumes runtime roots; P5 separately grants shell authority. P1 implements
neither runtime preparation nor shell access.

P2 keeps active parent source selections available for children, releases completed
or aborted handoffs, and can use `SnapshotWorkspaceSelection` /
`RestoreWorkspaceSelection` with its integrity-protected private launch state on
recovery. These bytes include private source information and must never enter an
agent workspace or public payload. Restoration revalidates source and anchor
identities without adopting newly created repositories. The approved P0b runtime
binding supplies actual file authority after preparation; the legacy manifest
root alone grants none.

Cluster children use the optional `WorkspaceClusterChildConsumer.StartChild`
handoff after normal child authorization, idempotency, parent-epoch insertion,
proof/token recording and immutable subset persistence. P3 receives the normal
private child launch spec and both manifests, and owns runtime allocation,
P0b binding persistence, credentials and cleanup. No desktop RPC is involved.
