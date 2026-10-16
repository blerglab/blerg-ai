//go:build !linux

package daemon

import (
	"errors"
	"os"
)

func codexOpenRestrictedFile(_ *os.Root, _ string, _, _, _ bool) (*os.File, error) {
	return nil, errors.New("restricted Codex file tools require Linux")
}

func codexRestrictedRegular(_ os.FileInfo) bool { return false }
