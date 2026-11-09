// Package releaseperf implements BLG-453's release performance check: an
// occasional, near-release tool that drives the real, built app against a
// seeded dataset and compares its timings to the last release's baseline.
// It never runs as part of the ordinary gate.
package releaseperf

import (
	"context"
	"fmt"
	"net"
	"os"
)

// IsolationCheck names the ports and data directories a release-perf run
// intends to use.
type IsolationCheck struct {
	Ports    []int
	DataDirs []string
}

// CheckIsolation refuses to proceed when any requested port or data
// directory already looks live: a port something else is already listening
// on, or a directory that already holds state. Either is treated as
// evidence of a live daemon (or a previous run) the tool must never collide
// with, never silently reuse, and never wipe.
func CheckIsolation(c IsolationCheck) error {
	var lc net.ListenConfig
	for _, port := range c.Ports {
		listener, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("port %d is already in use by another process; refusing to collide with a live daemon: %w", port, err)
		}
		if cerr := listener.Close(); cerr != nil {
			return fmt.Errorf("port %d: closing the probe listener: %w", port, cerr)
		}
	}
	for _, dir := range c.DataDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("data directory %s: %w", dir, err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("data directory %s already exists and is not empty; refusing to reuse what may be a live daemon's state", dir)
		}
	}
	return nil
}
