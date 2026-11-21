package protocol

const RepositoryCloneVersion = 1

// SelectionRef names an entry in one authenticated daemon's configured root.
// A path alone is never authority: it must still have this exact source identity.
type SelectionRef struct {
	// GitURL stays unresolved until the owner approves and the clone completes.
	GitURL         string `json:"git_url,omitempty"`
	Path           string `json:"path"`
	SourceIdentity string `json:"source_identity"`
}

type WorkspaceSelectionRequest struct {
	Layout               WorkspaceLayout `json:"layout,omitempty"`
	DaemonID             string          `json:"daemon_id,omitempty"`
	OwnershipRevision    int64           `json:"ownership_revision,omitempty"`
	ConnectionGeneration string          `json:"connection_generation,omitempty"`
	RootIdentity         string          `json:"root_identity,omitempty"`
	Members              []SelectionRef  `json:"members"`
	Folder               *SelectionRef   `json:"folder,omitempty"`
	MembershipSnapshot   string          `json:"membership_snapshot,omitempty"`
	// Cluster selections name separately authorized hosted sources, not desktops.
	Hosted []HostedSelectionRef `json:"hosted,omitempty"`
}
type HostedSelectionRef struct {
	Provider string `json:"provider"`
	FullName string `json:"full_name"`
}
type DiscoveryEntry struct {
	Origin         *RepoOrigin `json:"origin,omitempty"`
	Name           string      `json:"name"`
	Path           string      `json:"path"`
	Kind           string      `json:"kind"` // directory or repository
	SourceIdentity string      `json:"source_identity"`
}
type DiscoveryPage struct {
	RootIdentity string           `json:"root_identity"`
	Root         DiscoveryEntry   `json:"root"`
	Entries      []DiscoveryEntry `json:"entries"`
	NextCursor   string           `json:"next_cursor,omitempty"`
	Truncated    bool             `json:"truncated"`
}

// DiscoverySource is a daemon/server control-plane DTO. Path is a canonical
// relative source path; it is never copied into the public launch manifest.
type DiscoverySource struct {
	ID                      string      `json:"id"`
	Name                    string      `json:"name"`
	Path                    string      `json:"path"`
	SourceIdentity          string      `json:"source_identity"`
	CommonDirectoryIdentity string      `json:"common_directory_identity"`
	Origin                  *RepoOrigin `json:"origin,omitempty"`
}
type DiscoveryResolved struct {
	Layout             WorkspaceLayout   `json:"layout,omitempty"`
	SelectionIdentity  string            `json:"selection_identity"`
	RootIdentity       string            `json:"root_identity"`
	Root               WorkspaceRoot     `json:"root"`
	Folder             *SelectionRef     `json:"folder,omitempty"`
	MembershipSnapshot string            `json:"membership_snapshot,omitempty"`
	Members            []DiscoverySource `json:"members"`
}
type DiscoverWorkspace struct {
	// Clone fields are server-only, after an owner's membership approval.
	GitURL   string          `json:"git_url,omitempty"`
	GitToken string          `json:"git_token,omitempty"`
	Layout   WorkspaceLayout `json:"layout,omitempty"`
	// ParentLaunchGeneration names an already-bound selection (s.bound, discovery/service.go)
	// to read from: "bind_child"/"clone_addition" use it to inherit the parent's own real
	// member paths, and "resolve_base" (design §13) uses it the same way, to recover a
	// member's real on-disk path instead of trusting the manifest's own public, possibly
	// collapsed Folder.Path.
	ParentLaunchGeneration string `json:"parent_launch_generation,omitempty"`

	Type               string             `json:"type"`
	RequestID          string             `json:"request_id"`
	Operation          string             `json:"operation"` // list, expand, resolve, initialize (host launch only), bind, addition, clone_addition (server only), resolve_base (server only)
	Path               string             `json:"path,omitempty"`
	Cursor             string             `json:"cursor,omitempty"`
	RootIdentity       string             `json:"root_identity,omitempty"`
	Members            []SelectionRef     `json:"members,omitempty"`
	Folder             *SelectionRef      `json:"folder,omitempty"`
	MembershipSnapshot string             `json:"membership_snapshot,omitempty"`
	Runtime            string             `json:"runtime,omitempty"`
	SelectionIdentity  string             `json:"selection_identity,omitempty"`
	Manifest           *WorkspaceManifest `json:"manifest,omitempty"` // trusted server bind only
	// Base is "resolve_base" only (BLG-501 design §10): the branch name the daemon resolves
	// to its own local commit by reading a ref — never an object, never the network. Any
	// history question (reachability, how far ahead) is answered server-side, against the
	// provider's own compare API, never here.
	Base string `json:"base,omitempty"`
}
type WorkspaceDiscoveryResult struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	OK        bool               `json:"ok"`
	ErrorCode string             `json:"error_code,omitempty"`
	Page      *DiscoveryPage     `json:"page,omitempty"`
	Resolved  *DiscoveryResolved `json:"resolved,omitempty"`
	Addition  *DiscoveryAddition `json:"addition,omitempty"`
	// BaseSHA answers "resolve_base" only: Main's own local commit for the requested branch.
	BaseSHA string `json:"base_sha,omitempty"`
	// Cause is a fixed, resolve_base-only diagnostic label distinguishing which specific
	// check inside resolveBase refused the request (BLG-501 design §13) — distinct from
	// ErrorCode, which stays in its existing, server-validated vocabulary (validDiscoveryResult,
	// workspace_discovery.go) so the wire contract for every other operation is unchanged.
	// Logged by the daemon only (discovery.go); never interpreted by the server.
	Cause string `json:"cause,omitempty"`
}
