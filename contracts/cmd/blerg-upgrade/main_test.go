package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestUpgradeSignalsRunDeferredRecovery(t *testing.T) {
	if raw := os.Getenv("BLERG_UPGRADE_TEST_SIGNAL"); raw != "" {
		ctx, cancel := upgradeContext()
		defer cancel()
		defer fmt.Println("deferred recovery ran")
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = syscall.Kill(os.Getpid(), syscall.Signal(n)); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		return
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUpgradeSignalsRunDeferredRecovery$")
			cmd.Env = append(os.Environ(), "BLERG_UPGRADE_TEST_SIGNAL="+strconv.Itoa(int(sig)))
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "deferred recovery ran") {
				t.Fatalf("signal bypassed recovery: %v: %s", err, out)
			}
		})
	}
}
