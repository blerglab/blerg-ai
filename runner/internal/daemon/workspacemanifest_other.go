//go:build !unix

package daemon

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func workspaceDirectoryIdentity(string) (protocol.WorkspaceRoot, error) {
	return protocol.WorkspaceRoot{}, errors.New("sandbox workspace identity unavailable on this platform")
}
