// Package boardfiling describes exactly the draft a person approved for a
// one-card Board write. It contains no credential or transport policy.
package boardfiling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Origin is server-derived attribution, retained as structured card metadata.
type Origin struct {
	MemberID         string `json:"member_id,omitempty"`
	LaunchGeneration string `json:"launch_generation,omitempty"`
	Generation       int64  `json:"generation,omitempty"`
	SessionURL       string `json:"session_url"`
	Repo             string `json:"repo"`
	Commit           string `json:"commit"`
	Engine           string `json:"engine"`
	ComponentVersion string `json:"component_version,omitempty"`
	ComponentCommit  string `json:"component_commit,omitempty"`
}

// Draft is the exact approved payload. Verify its digest before applying Board
// defaults or dropping component metadata under the target board's settings.
type Draft struct {
	Size       string   `json:"size,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	Complexity string   `json:"complexity,omitempty"`
	Repos      []string `json:"repos,omitempty"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Type       string   `json:"type,omitempty"`
	Sensitive  bool     `json:"sensitive"`
	Origin     Origin   `json:"origin"`
}

// Digest binds every approved field and the target board. All callers marshal
// this same typed structure; incoming JSON key order does not affect the digest.
// Text is deliberately not normalized: edits require a new approval digest.
func Digest(boardID string, draft Draft) string {
	payload := struct {
		BoardID string `json:"board_id"`
		Draft
	}{boardID, draft}
	raw, _ := json.Marshal(payload) // Strings, booleans and structs cannot fail JSON encoding.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidEstimates permits missing estimates while constraining any supplied value.
func (d Draft) ValidEstimates() bool {
	switch d.Size {
	case "", "S", "M", "L", "XL":
	default:
		return false
	}
	for _, v := range []string{d.Risk, d.Complexity} {
		switch v {
		case "", "low", "medium", "high":
		default:
			return false
		}
	}
	return true
}
