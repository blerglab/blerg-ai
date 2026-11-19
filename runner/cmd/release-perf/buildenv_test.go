package main

import (
	"os"
	"strings"
	"testing"
)

func TestGoBuildKeepsTemporaryCacheAndWorkerLimits(t *testing.T) {
	for key, value := range map[string]string{"GOCACHE": "private-cache", "GOTMPDIR": "private-tmp", "GOFLAGS": "-p=2", "GOMAXPROCS": "2"} {
		t.Setenv(key, value)
	}
	t.Setenv("PRIVATE_TEST_CANARY", "must-not-reach-build")
	env := goBuildEnv()
	for _, key := range []string{"GOCACHE", "GOTMPDIR", "GOFLAGS", "GOMAXPROCS"} {
		if !strings.Contains(strings.Join(env, "\n"), key+"="+os.Getenv(key)) {
			t.Errorf("missing %s", key)
		}
	}
	if strings.Contains(strings.Join(env, "\n"), "PRIVATE_TEST_CANARY") {
		t.Fatal("unrelated environment leaked")
	}
}
