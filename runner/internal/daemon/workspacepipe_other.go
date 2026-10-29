//go:build !linux && !darwin

package daemon

import "os"

func workspacePollablePipe(*os.File) (*os.File, error) { return nil, os.ErrPermission }
