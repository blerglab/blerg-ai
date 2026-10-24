package protocol

import "errors"

// WorkspaceGitRef qualifies Git reads by immutable member and current runtime.
// A zero reference is accepted only by legacy single-repository adapters.
type WorkspaceGitRef struct {
	WorkspaceRuntimeRef
	MemberID string `json:"member_id,omitempty"`
}

func (r WorkspaceGitRef) Member() WorkspaceMemberRef {
	return WorkspaceMemberRef{MemberID: r.MemberID, LaunchGeneration: r.LaunchGeneration}
}
func (r WorkspaceGitRef) IsZero() bool { return r == (WorkspaceGitRef{}) }
func (r WorkspaceGitRef) Validate(m WorkspaceManifest, generation int64) error {
	if r.LaunchGeneration != m.LaunchGeneration || r.Generation != generation || generation <= 0 {
		return errors.New("stale Git workspace")
	}
	for _, member := range m.Members {
		if member.ID == r.MemberID {
			return nil
		}
	}
	return errors.New("unknown Git member")
}
