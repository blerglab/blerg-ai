package main

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

type systemClock struct{}

func newClock() (clock, error) { return systemClock{}, nil }

func (systemClock) sample() (sample, error) {
	// Reads are sequential, so small differences include sampling overhead.
	wall := time.Now().UTC()
	var boot, mono unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &boot); err != nil {
		return sample{}, err
	}
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
		return sample{}, err
	}
	return sample{Wall: wall, BootNS: boot.Nano(), MonoNS: mono.Nano()}, nil
}

func (systemClock) wait(ctx context.Context) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
