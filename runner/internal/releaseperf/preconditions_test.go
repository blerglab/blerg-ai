package releaseperf

import (
	"errors"
	"testing"
)

func TestCheckHostQuietRefusesHighLoad(t *testing.T) {
	err := CheckHostQuiet(HostState{Load1: 9.0, LogicalCPUs: 8, AvailableMemGiB: 16})
	if err == nil {
		t.Fatal("expected refusal: load 9.0 exceeds 0.5*8=4.0")
	}
}

func TestCheckHostQuietAllowsLowLoad(t *testing.T) {
	err := CheckHostQuiet(HostState{Load1: 1.0, LogicalCPUs: 8, AvailableMemGiB: 16})
	if err != nil {
		t.Fatalf("expected no refusal, got: %v", err)
	}
}

func TestCheckHostQuietRefusesLowMemory(t *testing.T) {
	err := CheckHostQuiet(HostState{Load1: 0.1, LogicalCPUs: 8, AvailableMemGiB: 2})
	if err == nil {
		t.Fatal("expected refusal: 2 GiB available is under the 4 GiB floor")
	}
}

func TestPidfileGuardRefusesWhenOwnerIsAlive(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/release-perf.pid"
	if err := WritePidfile(path, currentPID()); err != nil {
		t.Fatal(err)
	}
	err := CheckPidfile(path)
	if err == nil {
		t.Fatal("expected refusal: the pidfile names a live process (this test's own)")
	}
	if !errors.Is(err, ErrConcurrentRun) {
		t.Fatalf("expected ErrConcurrentRun, got: %v", err)
	}
}

func TestPidfileGuardCleansUpAStaleOwner(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/release-perf.pid"
	// A PID essentially guaranteed not to be alive in this test process's PID namespace.
	if err := WritePidfile(path, 1<<30); err != nil {
		t.Fatal(err)
	}
	if err := CheckPidfile(path); err != nil {
		t.Fatalf("expected a stale pidfile to be cleaned up silently, got: %v", err)
	}
	if _, err := ReadPidfile(path); !errors.Is(err, ErrNoPidfile) {
		t.Fatalf("expected the stale pidfile to be removed, got: %v", err)
	}
}

func TestPidfileGuardAllowsNoPidfile(t *testing.T) {
	dir := t.TempDir()
	if err := CheckPidfile(dir + "/release-perf.pid"); err != nil {
		t.Fatalf("expected no refusal with no pidfile present, got: %v", err)
	}
}
