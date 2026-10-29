//go:build linux || darwin

package hostgit

import (
	"errors"
	"time"
)

// Darwin proc.h SZOMB. Keep the typed observation decision testable on Linux.
const darwinZombieState = 5
const exitPollInterval = 5 * time.Millisecond

func observeDarwinExit(pid int, sample func(int) (int, int8, error)) error {
	for {
		observed, state, err := sample(pid)
		if err != nil {
			return err
		}
		if observed != pid {
			return errors.New("observe Git exit: child identity unavailable")
		}
		if state == darwinZombieState {
			return nil
		}
		time.Sleep(exitPollInterval)
	}
}
