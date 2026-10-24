package protocol

// SelectionRef names an entry in one authenticated daemon's configured root.
// A path alone is never authority: it must still have this exact source identity.
type SelectionRef struct {
	Path           string `json:"path"`
	SourceIdentity string `json:"source_identity"`
}

type WorkspaceSelectionRequest struct {
	DaemonID             string         `json:"daemon_id,omitempty"`
	OwnershipRevision    int64          `json:"ownership_revision,omitempty"`
	ConnectionGeneration string         `json:"connection_generation,omitempty"`
	RootIdentity         string         `json:"root_identity,omitempty"`
	Members              []SelectionRef `json:"members"`
	Folder               *SelectionRef  `json:"folder,omitempty"`
	MembershipSnapshot   string         `json:"membership_snapshot,omitempty"`
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
	SelectionIdentity  string            `json:"selection_identity"`
	RootIdentity       string            `json:"root_identity"`
	Root               WorkspaceRoot     `json:"root"`
	Folder             *SelectionRef     `json:"folder,omitempty"`
	MembershipSnapshot string            `json:"membership_snapshot,omitempty"`
	Members            []DiscoverySource `json:"members"`
}
type DiscoverWorkspace struct {
	ParentLaunchGeneration string `json:"parent_launch_generation,omitempty"`

	Type               string             `json:"type"`
	RequestID          string             `json:"request_id"`
	Operation          string             `json:"operation"` // list, expand, resolve, bind
	Path               string             `json:"path,omitempty"`
	Cursor             string             `json:"cursor,omitempty"`
	RootIdentity       string             `json:"root_identity,omitempty"`
	Members            []SelectionRef     `json:"members,omitempty"`
	Folder             *SelectionRef      `json:"folder,omitempty"`
	MembershipSnapshot string             `json:"membership_snapshot,omitempty"`
	Runtime            string             `json:"runtime,omitempty"`
	SelectionIdentity  string             `json:"selection_identity,omitempty"`
	Manifest           *WorkspaceManifest `json:"manifest,omitempty"` // trusted server bind only
}
type WorkspaceDiscoveryResult struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	OK        bool               `json:"ok"`
	ErrorCode string             `json:"error_code,omitempty"`
	Page      *DiscoveryPage     `json:"page,omitempty"`
	Resolved  *DiscoveryResolved `json:"resolved,omitempty"`
}
