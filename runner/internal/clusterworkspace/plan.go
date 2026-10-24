// Package clusterworkspace owns trusted pod launch state, never agent manifests.
package clusterworkspace

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const Env = "BLERG_RUNNER_CLUSTER_WORKSPACE"
const PublicRoot = "/workspace/projects"

var ErrPlan = errors.New("cluster workspace launch is invalid or unavailable")
var stableID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Member is PRIVATE server/runtime state. It travels only in the per-session
// Secret, never in a Job's literal environment, public manifest or event.
// CredentialHost includes any explicit port. Each context has its own scope;
// neither public read access nor another member's token grants push authority.
type Member struct {
	ID                  string `json:"id"`
	Provider            string `json:"provider"`
	CloneURL            string `json:"clone_url"`
	PushURL             string `json:"push_url"`
	DestinationIdentity string `json:"destination_identity"`
	CredentialHost      string `json:"credential_host"`
	Token               string `json:"token,omitempty"`
	PushAuthorized      bool   `json:"push_authorized"`
}

type Plan struct {
	PrepareDeadlineMillis int64                            `json:"prepare_deadline_millis,omitempty"`
	Identities            []MemberIdentity                 `json:"identities,omitempty"`
	SessionID             string                           `json:"session_id"`
	Manifest              protocol.WorkspaceManifest       `json:"manifest"`
	Runtime               protocol.WorkspaceRuntimeRef     `json:"runtime"`
	Members               []Member                         `json:"members"`
	PreviousResults       []protocol.WorkspaceMemberResult `json:"previous_results,omitempty"`
}

func (p Plan) Validate() error {
	if !stableID.MatchString(p.SessionID) || p.Manifest.Validate() != nil || p.Manifest.Runtime != "cluster" || p.Runtime.LaunchGeneration != p.Manifest.LaunchGeneration || p.Runtime.Generation <= 0 || len(p.Members) != len(p.Manifest.Members) || p.Manifest.ValidateResults(p.PreviousResults) != nil {
		return ErrPlan
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
		if member == nil || member.Path != c.ID || member.Provider != c.Provider || member.Destination != c.DestinationIdentity || c.Provider == "" {
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
			return ErrPlan
		}
		if c.Token != "" && clone.Scheme != "https" {
			return ErrPlan
		}
		if c.PushAuthorized && c.Token == "" {
			return ErrPlan
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
