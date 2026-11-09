//go:build !linux

package shellruntime

import (
	"context"
	"os/exec"
)

func helperProcessLedger(context.Context, *exec.Cmd, bool) (func() bool, func(), error) {
	return nil, func() {}, nil
}
