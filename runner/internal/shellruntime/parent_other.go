//go:build !linux

package shellruntime

import "os/exec"

func startOwnedCommand(cmd *exec.Cmd, _ <-chan struct{}) error { return cmd.Start() }
