//go:build !linux && !darwin

package daemon

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
)

func workspaceOpenFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("workspace file reads require a supported secure filesystem implementation")
}
func workspaceRegularFile(_ os.FileInfo) bool { return false }

func workspaceOpenDirectory(root *os.Root, name string) (*os.File, error) {
	return workspaceOpenFile(root, name)
}

func workspaceMetadataFingerprint(_ os.FileInfo) string { return "" }

func workspaceRootIdentity(_ os.FileInfo) protocol.WorkspaceRoot { return protocol.WorkspaceRoot{} }
