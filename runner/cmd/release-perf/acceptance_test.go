package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAcceptanceEnvOnlySoaksPrimaryRun(t *testing.T) {
	pids := map[string]int{"core": 11, "runner": 12, "board": 13}
	env, err := acceptanceEnv(0, 30*time.Minute, 14, pids, "acceptance.json")
	if err != nil {
		t.Fatal(err)
	}
	if env["RELEASE_PERF_SOAK_SECONDS"] != "1800" {
		t.Fatalf("missing soak duration: %v", env)
	}
	var got map[string]int
	if json.Unmarshal([]byte(env["RELEASE_PERF_PROCESS_PIDS"]), &got) != nil || got["daemon"] != 14 || got["runner"] != 12 {
		t.Fatal("missing process samples")
	}
	if _, ok := pids["daemon"]; ok {
		t.Fatal("mutated caller process list")
	}
	for _, run := range []int{1, 2} {
		env, err = acceptanceEnv(run, 30*time.Minute, 14, pids, "acceptance.json")
		if err != nil || len(env) != 0 {
			t.Fatal("calibration must not repeat the sustained soak")
		}
	}
}
func TestAcceptanceEnvRejectsIncompleteConfiguration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		pid      int
	}{{-time.Second, 1}, {time.Minute, 0}, {time.Minute, -1}} {
		if _, err := acceptanceEnv(0, test.duration, test.pid, nil, "out.json"); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	env, err := acceptanceEnv(0, 0, 0, nil, "")
	if err != nil || len(env) != 0 {
		t.Fatal("ordinary release benchmark must remain unchanged")
	}
}
