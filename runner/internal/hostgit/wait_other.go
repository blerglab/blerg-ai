//go:build !linux && !darwin

package hostgit

import "errors"

func platformObserveExit(_ int) error {
	return errors.New("Git non-reaping exit observation unsupported on this platform")
}
