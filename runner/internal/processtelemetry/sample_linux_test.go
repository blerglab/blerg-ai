//go:build linux

package processtelemetry

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSampleGeneratedGoTestProcess(t *testing.T) {
	if os.Getenv("BLERG_PROCESS_SAMPLE_FIXTURE") == "1" {
		time.Sleep(600 * time.Millisecond)
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "generated.sh")
	if err = os.WriteFile(path, []byte("#!/bin/sh\n\"$1\" -test.run=^TestSampleGeneratedGoTestProcess$\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path, binary)
	cmd.Env = append(os.Environ(), "BLERG_PROCESS_SAMPLE_FIXTURE=1")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	root := SampleRoot{PID: cmd.Process.Pid, Identity: ProcessIdentity(cmd.Process.Pid)}
	sampler := NewSampler()
	var duration time.Duration
	for i := 0; i < 20; i++ {
		batch := sampler.Sample([]SampleRoot{root}, time.Now())
		for _, s := range batch.Spans {
			if s.Program == "go-test" {
				duration += s.End.Sub(s.Start)
				if !s.Approximate || s.Basis != "proc_sample" {
					t.Fatal(s)
				}
			}
		}
		raw, _ := json.Marshal(batch)
		if strings.Contains(string(raw), path) || strings.Contains(string(raw), "test.run") {
			t.Fatal("private arguments retained")
		}
		time.Sleep(40 * time.Millisecond)
	}
	if duration < 300*time.Millisecond {
		t.Fatalf("generated script's go-test not observed: %s", duration)
	}
	t.Logf("generated shell script go-test lower bound: %s", duration)
}
