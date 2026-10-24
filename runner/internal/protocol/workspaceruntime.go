package protocol

import "errors"

// WorkspaceRuntimeRef identifies one physical runtime, independently of the
// immutable selection's LaunchGeneration. The server allocates Generation before
// dispatch, after fencing the previous writer. It is never agent supplied.
type WorkspaceRuntimeRef struct {
	LaunchGeneration string `json:"launch_generation"`
	Generation       int64  `json:"generation"`
}

// WorkspaceRuntimeBinding is reported by the authenticated current runtime once
// its public directory exists. Root is that directory's physical identity, never
// HOME or a source directory. A manifest's legacy Root grants no file access.
type WorkspaceRuntimeBinding struct {
	WorkspaceRuntimeRef
	ManifestDigest string        `json:"manifest_digest"`
	Root           WorkspaceRoot `json:"root"`
}

func (b WorkspaceRuntimeBinding) Validate(m WorkspaceManifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if b.Generation <= 0 || b.LaunchGeneration != m.LaunchGeneration || b.ManifestDigest != m.Digest || b.Root.Inode == 0 {
		return errors.New("invalid workspace runtime binding")
	}
	return nil
}

// Matches must be checked against current trusted state before dispatch AND
// delivery. A pending (nil) binding is unavailable. Physical root replacement
// requires a new server-allocated generation, even if inode numbers are reused.
func (r WorkspaceRuntimeRef) Matches(b *WorkspaceRuntimeBinding) bool {
	return b != nil && r.Generation > 0 && r == b.WorkspaceRuntimeRef && b.Root.Inode != 0
}

// WorkspaceRuntimePrepared travels on the existing authenticated runtime
// connection. Receivers must authorize the session and current incarnation;
// possession of a generation or this message alone is not authority.
type WorkspaceRuntimePrepared struct {
	Type      string                  `json:"type"`
	SessionID string                  `json:"session_id"`
	Binding   WorkspaceRuntimeBinding `json:"binding"`
}
