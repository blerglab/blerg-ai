//go:build !linux && !darwin

package daemon

import "os"

func openWorkspaceExternalRoot(string) (*os.Root, error) { return nil, os.ErrPermission }
