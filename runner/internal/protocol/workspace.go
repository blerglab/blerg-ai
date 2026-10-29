package protocol

import (
	"errors"
	"path"
	"strings"
)

const WorkspaceVersion = 2
const MaxWorkspaceMembers = 20

// WorkspaceLayout selects the public paths for separately picked desktop repos.
// An omitted layout retains the default names layout.
type WorkspaceLayout string

const (
	WorkspaceLayoutNames     WorkspaceLayout = "names"
	WorkspaceLayoutStructure WorkspaceLayout = "structure"
)

func (l WorkspaceLayout) Valid() bool {
	return l == "" || l == WorkspaceLayoutNames || l == WorkspaceLayoutStructure
}

// WorkspaceCapabilities is negotiated independently at both runtime hops.
// An omitted value is a legacy peer and grants no workspace-v2 or shell support.
type WorkspaceCapabilities struct {
	LinuxHost       bool `json:"linux_host,omitempty"`
	MacOSHost       bool `json:"macos_host,omitempty"`
	Sandbox         bool `json:"sandbox,omitempty"`
	Cluster         bool `json:"cluster,omitempty"`
	ManifestVersion int  `json:"manifest_version"`
	ShellVersion    int  `json:"shell_version"`
}

func (c WorkspaceCapabilities) Require(m WorkspaceManifest, shell bool) error {
	if !c.Supports(m.Runtime, false) {
		return errors.New("peer does not support this workspace runtime")
	}
	if shell && (c.ManifestVersion != WorkspaceVersion || c.ShellVersion != 1) {
		return errors.New("peer does not support owner shell")
	}
	return nil
}

type WorkspaceRoot struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

// WorkspaceMember contains authorized public metadata only. Private source paths,
// object mounts and credential references must never be serialized here.
type WorkspaceMember struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	Path                    string `json:"path"`
	SourceIdentity          string `json:"source_identity"`
	CommonDirectoryIdentity string `json:"common_directory_identity"`
	Provider                string `json:"provider"`
	Project                 string `json:"project,omitempty"`
	// Destination is the authorized remote/source destination identity; the
	// per-session wip branch is derived separately and reported in results.
	Destination string `json:"destination"`
}

type WorkspaceManifest struct {
	Layout           WorkspaceLayout `json:"layout,omitempty"`
	Version          int             `json:"version"`
	Kind             string          `json:"kind"`
	OwnerID          string          `json:"owner_id"`
	Runtime          string          `json:"runtime"`
	LaunchGeneration string          `json:"launch_generation"`
	// Root is legacy metadata only; use WorkspaceRuntimeBinding for file access.
	Root    WorkspaceRoot     `json:"root"`
	Digest  string            `json:"digest"`
	Members []WorkspaceMember `json:"members"`
}

func (m WorkspaceManifest) Validate() error {
	if !m.Layout.Valid() || m.Version != WorkspaceVersion || (m.Kind != "repo" && m.Kind != "folder") || m.OwnerID == "" || m.LaunchGeneration == "" || m.Digest == "" {
		return errors.New("invalid workspace binding")
	}
	if m.Runtime != "host" && m.Runtime != "sandbox" && m.Runtime != "cluster" {
		return errors.New("invalid workspace runtime")
	}
	if len(m.Members) > MaxWorkspaceMembers || (len(m.Members) == 0 && (m.Kind != "folder" || m.Runtime != "host")) || (m.Kind == "repo" && len(m.Members) != 1) {
		return errors.New("invalid workspace member count")
	}
	ids, sources, common := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, v := range m.Members {
		if v.ID == "" || v.Name == "" || v.SourceIdentity == "" || v.CommonDirectoryIdentity == "" || v.Destination == "" || ids[v.ID] || sources[v.SourceIdentity] || common[v.CommonDirectoryIdentity] {
			return errors.New("invalid or duplicate workspace member")
		}
		if v.Path == "" || path.IsAbs(v.Path) || path.Clean(v.Path) != v.Path || v.Path == ".." || strings.HasPrefix(v.Path, "../") || strings.ContainsAny(v.Path, "\\\x00") {
			return errors.New("invalid member path")
		}
		for _, prev := range m.Members[:i] {
			if v.Path == prev.Path || strings.HasPrefix(v.Path, prev.Path+"/") || strings.HasPrefix(prev.Path, v.Path+"/") || v.Path == "." || prev.Path == "." {
				return errors.New("overlapping member paths")
			}
		}
		ids[v.ID], sources[v.SourceIdentity], common[v.CommonDirectoryIdentity] = true, true, true
	}
	return nil
}

// ValidateWorkspaceChild checks selection only; existing child authorization is
// still required by the caller. Grants are never inherited.
func ValidateWorkspaceChild(parent, child WorkspaceManifest) error {
	if err := parent.Validate(); err != nil {
		return err
	}
	if err := child.Validate(); err != nil {
		return err
	}
	if child.Layout != parent.Layout || child.OwnerID != parent.OwnerID || child.Runtime != parent.Runtime || child.LaunchGeneration == parent.LaunchGeneration {
		return errors.New("invalid child binding")
	}
	for _, c := range child.Members {
		found := false
		for _, p := range parent.Members {
			if c.ID == p.ID && c.SourceIdentity == p.SourceIdentity && c.CommonDirectoryIdentity == p.CommonDirectoryIdentity && c.Project == p.Project && c.Provider == p.Provider && c.Destination == p.Destination && c.Path == p.Path {
				found = true
			}
		}
		if !found {
			return errors.New("child selection outside parent manifest")
		}
	}
	return nil
}

type WorkspaceMemberRef struct {
	MemberID         string `json:"member_id"`
	LaunchGeneration string `json:"launch_generation"`
}

type WorkspaceMemberResult struct {
	WorkspaceMemberRef
	Base             string `json:"base"`
	Head             string `json:"head"`
	Branch           string `json:"branch"`
	SyncError        string `json:"sync_error,omitempty"`
	PushError        string `json:"push_error,omitempty"`
	Pending          bool   `json:"pending,omitempty"`
	RecoveryLocation string `json:"recovery_location,omitempty"`
}

func (m WorkspaceManifest) ValidateResults(results []WorkspaceMemberResult) error {
	seen := map[string]bool{}
	for _, r := range results {
		found := false
		for _, v := range m.Members {
			if v.ID == r.MemberID {
				found = true
			}
		}
		if !found || seen[r.MemberID] || r.LaunchGeneration != m.LaunchGeneration {
			return errors.New("invalid result provenance")
		}
		seen[r.MemberID] = true
	}
	return nil
}

// Aggregate budgets apply once per session, never once per member.
type WorkspaceBudget struct {
	CheckoutBytes   int64 `json:"checkout_bytes"`
	CheckoutEntries int   `json:"checkout_entries"`
	FetchedBytes    int64 `json:"fetched_bytes"`
	MetadataBytes   int64 `json:"metadata_bytes"`
	MetadataEntries int   `json:"metadata_entries"`
	GitMemoryBytes  int64 `json:"git_memory_bytes"`
	PrepareSeconds  int   `json:"prepare_seconds"`
	SyncSeconds     int   `json:"sync_seconds"`
}

func DefaultWorkspaceBudget() WorkspaceBudget {
	return WorkspaceBudget{10 << 30, 500000, 1 << 30, 256 << 20, 200000, 512 << 20, 300, 80}
}

// AuthorizesProject requires an explicit member and current launch; a repository
// merely discovered under a folder has no project authority.
func (m WorkspaceManifest) AuthorizesProject(ref WorkspaceMemberRef, project string) bool {
	if ref.LaunchGeneration != m.LaunchGeneration || project == "" {
		return false
	}
	for _, member := range m.Members {
		if member.ID == ref.MemberID && member.Project == project {
			return true
		}
	}
	return false
}
