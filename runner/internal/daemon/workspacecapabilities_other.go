//go:build !linux

package daemon

import "errors"

func workspacePlatformProbe() error { return errors.New("workspace host containment unsupported") }
