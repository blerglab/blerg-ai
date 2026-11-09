//go:build !linux

package shellruntime

import "github.com/blerglab/blerg-ai/runner/internal/ownershell"

func keeperRestartClock() (ownershell.LeaseClock, error) { return nil, ErrClosed }
