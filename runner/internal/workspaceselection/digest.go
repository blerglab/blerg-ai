// Package workspaceselection defines P1's canonical digest, shared by its
// trusted server selection builder and daemon-private source binder.
package workspaceselection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func Digest(manifest protocol.WorkspaceManifest) string {
	manifest.Digest = ""
	raw, _ := json.Marshal(manifest)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
