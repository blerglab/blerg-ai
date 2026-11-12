// Package clusterworkspace owns trusted pod launch state, never agent manifests.
package clusterworkspace

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workbranch"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

const Env = "BLERG_RUNNER_CLUSTER_WORKSPACE"
const PublicRoot = "/workspace/projects"

var ErrCredentialScope = errors.New("repository credential scope refused")

var ErrClone = errors.New("repository clone failed")
var ErrRetainedStorage = errors.New("retained repository storage is missing")

var ErrPlan = errors.New("cluster workspace launch is invalid or unavailable")
var stableID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Member is PRIVATE server/runtime state. It travels only in the per-session
// Secret, never in a Job's literal environment, public manifest or event.
// CredentialHost includes any explicit port. Each context has its own scope;
// neither public read access nor another member's token grants push authority.
type Member struct {
	// Path is derived from the admitted manifest by New, never credential state.
	Path                string `json:"-"`
	ID                  string `json:"id"`
	Provider            string `json:"provider"`
	CloneURL            string `json:"clone_url"`
	PushURL             string `json:"push_url"`
	DestinationIdentity string `json:"destination_identity"`
	CredentialHost      string `json:"credential_host"`
	Token               string `json:"token,omitempty"`
	PushAuthorized      bool   `json:"push_authorized"`
	// BaseRef is BLG-501's required --base <branch> for a host Main's read-only cluster
	// worker (design §3.3): Prepare clones this branch instead of the remote's default.
	// Empty for every other member shape (an ordinary cluster workspace's own members,
	// which have no such concept and keep cloning the default branch unchanged).
	BaseRef string `json:"base_ref,omitempty"`
	// BaseSHA is the provider's advertised BaseRef tip at launch. Main's local tip
	// is separately checked for unpushed history and may lag this commit. The launch
	// pins an object ID, never BaseRef's mutable name alone (Codex review of
	// 1524a5d8). Prepare checks this out right after cloning, before creating the pod's own
	// work branch, so the pod is pinned to the exact commit the launch actually verified,
	// not whatever BaseRef happens to point at by the time the clone runs.
	BaseSHA string `json:"base_sha,omitempty"`
}

type Plan struct {
	WorkBranch string `json:"work_branch,omitempty"`
	// Zero retains bounded enforcement for plans issued before standard storage.
	UnboundedStorage bool `json:"unbounded_storage,omitempty"`
	// LayoutSources preserves the ordered naming context of an immutable ancestor
	// selection, so a child subset keeps names such as app-2 instead of renaming.
	LayoutSources         []protocol.DiscoverySource       `json:"layout_sources,omitempty"`
	PrepareDeadlineMillis int64                            `json:"prepare_deadline_millis,omitempty"`
	Identities            []MemberIdentity                 `json:"identities,omitempty"`
	SessionID             string                           `json:"session_id"`
	Manifest              protocol.WorkspaceManifest       `json:"manifest"`
	Runtime               protocol.WorkspaceRuntimeRef     `json:"runtime"`
	Members               []Member                         `json:"members"`
	PreviousResults       []protocol.WorkspaceMemberResult `json:"previous_results,omitempty"`
}

func (p Plan) Validate() error {
	if p.WorkBranch != "" && !workbranch.Valid(p.WorkBranch) {
		return ErrPlan
	}
	if p.Manifest.Layout == protocol.WorkspaceLayoutStructure || !stableID.MatchString(p.SessionID) || p.Manifest.Validate() != nil || p.Manifest.Runtime != "cluster" || p.Runtime.LaunchGeneration != p.Manifest.LaunchGeneration || p.Runtime.Generation <= 0 || len(p.Members) != len(p.Manifest.Members) || p.Manifest.ValidateResults(p.PreviousResults) != nil {
		return ErrPlan
	}
	sources := p.LayoutSources
	if len(sources) == 0 {
		for _, m := range p.Manifest.Members {
			sources = append(sources, protocol.DiscoverySource{ID: m.ID, Name: m.Name})
		}
	}
	if len(sources) > protocol.MaxWorkspaceMembers {
		return ErrPlan
	}
	paths, _ := workspaceselection.MemberPaths(protocol.DiscoveryResolved{Layout: protocol.WorkspaceLayoutNames, Members: sources}, workspaceselection.MemberPathOptions{Cluster: true})
	expected := map[string]struct{ name, path string }{}
	for i, source := range sources {
		if _, exists := expected[source.ID]; exists || !stableID.MatchString(source.ID) || source.Name == "" {
			return ErrPlan
		}
		// Pre-upgrade manifests have no layout field and immutable ID paths.
		// Preserve those paths for recovery and children; never reinterpret them.
		expectedPath := paths[i]
		if p.Manifest.Layout == "" {
			expectedPath = source.ID
		}
		expected[source.ID] = struct{ name, path string }{source.Name, expectedPath}
	}
	seen := map[string]bool{}
	for _, c := range p.Members {
		if seen[c.ID] || !stableID.MatchString(c.ID) {
			return ErrPlan
		}
		seen[c.ID] = true
		var member *protocol.WorkspaceMember
		for i := range p.Manifest.Members {
			if p.Manifest.Members[i].ID == c.ID {
				member = &p.Manifest.Members[i]
				break
			}
		}
		if member == nil || member.Path != expected[c.ID].path || member.Name != expected[c.ID].name || member.Provider != c.Provider || member.Destination != c.DestinationIdentity || c.Provider == "" {
			return ErrPlan
		}
		clone, e := endpoint(c.CloneURL)
		if e != nil {
			return ErrPlan
		}
		push, e := endpoint(c.PushURL)
		if e != nil {
			return ErrPlan
		}
		if !destinationMatches(c.DestinationIdentity, push) {
			return ErrPlan
		}
		if clone.Scheme != push.Scheme || clone.Host != push.Host || clone.Host != c.CredentialHost {
			return ErrCredentialScope
		}
		if c.Token != "" && clone.Scheme != "https" {
			return ErrPlan
		}
		if c.PushAuthorized && c.Token == "" {
			return ErrCredentialScope
		}
	}
	if len(p.Identities) > 0 {
		if len(p.Identities) != len(p.Members) {
			return ErrPlan
		}
		identities := map[string]bool{}
		for _, pin := range p.Identities {
			if identities[pin.ID] || !seen[pin.ID] || pin.Directory.Inode == 0 || pin.GitDirectory.Inode == 0 || pin.Objects.Inode == 0 {
				return ErrPlan
			}
			identities[pin.ID] = true
		}
	}
	return nil
}

func endpoint(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(raw, "\x00\r\n") {
		return nil, ErrPlan
	}
	return u, nil
}

// MemberIdentity is trusted recovery state, scoped to the retained PVC UID.
// Devices can be renumbered across nodes; the current runtime pins full tuples,
// while recovery compares inodes only within the same server-bound volume UID.
type MemberIdentity struct {
	ID           string                 `json:"id"`
	Directory    protocol.WorkspaceRoot `json:"directory"`
	GitDirectory protocol.WorkspaceRoot `json:"git_directory"`
	Objects      protocol.WorkspaceRoot `json:"objects"`
}

func destinationMatches(identity string, u *url.URL) bool {
	normalized := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Contains(identity, "://") {
		v, e := endpoint(identity)
		return e == nil && v.Scheme == u.Scheme && v.Host == u.Host && strings.TrimSuffix(strings.Trim(v.Path, "/"), ".git") == normalized
	}
	return identity == normalized
}
