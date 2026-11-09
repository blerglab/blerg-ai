//go:build linux && !persistent_shell_test

package shellruntime

import "github.com/blerglab/blerg-ai/runner/internal/ownershell"

func persistentClock() (ownershell.LeaseClock, error) { return nativePersistentClock() }
