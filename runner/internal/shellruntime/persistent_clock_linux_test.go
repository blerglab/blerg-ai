//go:build linux

package shellruntime

import "testing"

func TestPersistentClockRefusesUnverifiedVirtualSleep(t *testing.T) {
	for _, kernel := range []string{"microsoft-standard-WSL2", "linuxkit", "docker-desktop", ""} {
		if verifiedPersistentKernel(kernel) {
			t.Fatalf("unverified host-sleep clock advertised for %q", kernel)
		}
	}
	if !verifiedPersistentKernel("generic-linux") {
		t.Fatal("native Linux boot-time clock refused")
	}
}

func TestPersistentClockReadsSuspendAwareElapsedTime(t *testing.T) {
	// This checks the syscall implementation, not real WSL/Docker Desktop sleep.
	clock, err := persistentClock()
	if err != nil {
		t.Skip("installed kernel has no verified host-sleep clock")
	}
	wall, elapsed, err := clock()
	if err != nil || wall.IsZero() || elapsed <= 0 {
		t.Fatal("boot-time clock unavailable", err)
	}
	_, later, err := clock()
	if err != nil || later < elapsed {
		t.Fatal("boot-time clock moved backward", err)
	}
}
