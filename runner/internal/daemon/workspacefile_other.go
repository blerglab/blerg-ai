//go:build !linux && !darwin

package daemon

import (
	"errors"
	"os"
)

func workspaceOpenFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("workspace file reads require a supported secure filesystem implementation")
}
func workspaceRegularFile(_ os.FileInfo) bool { return false }
