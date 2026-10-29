//go:build !linux

package daemon

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (*FilesystemWorkspaceStorage) allocate(context.Context, string, protocol.WorkspaceBudget) (string, error) {
	return "", errors.New("persistent quota filesystem backend unavailable on this platform")
}
func (*FilesystemWorkspaceStorage) release(context.Context, string, string) error {
	return errors.New("persistent quota filesystem backend unavailable on this platform")
}
