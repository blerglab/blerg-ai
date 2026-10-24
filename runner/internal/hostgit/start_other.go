//go:build !darwin

package hostgit

import "os/exec"

func platformStartCommand(_ Options, cmd *exec.Cmd) error { return cmd.Start() }
