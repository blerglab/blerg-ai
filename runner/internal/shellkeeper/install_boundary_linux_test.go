package shellkeeper_test

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
)

func TestRound8InstallNeverConsultsKeeperState(t *testing.T) {
	binary := keepertest.Build(t)
	next := keepertest.DifferentImage(t, binary)
	original, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"missing", "broken", "public_directory", "live_lock", "hung_socket", "empty_keeper"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "daemon")
			stateDir := filepath.Join(root, "state")
			if err := os.WriteFile(destination, original, 0755); err != nil {
				t.Fatal(err)
			}
			if state != "missing" {
				if err := keepertest.Run(t, binary, stateDir, "shell-keeper", "init"); err != nil {
					t.Fatal(err)
				}
			}
			var process *keepertest.Process
			switch state {
			case "broken":
				if err := os.WriteFile(filepath.Join(stateDir, "shell-keeper", "daemon-key.json"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "public_directory":
				if err := os.Chmod(filepath.Join(stateDir, "shell-keeper"), 0777); err != nil {
					t.Fatal(err)
				}
			case "live_lock", "hung_socket", "empty_keeper":
				process = keepertest.Start(t, destination, stateDir)
				if state == "hung_socket" {
					process.Signal(t, syscall.SIGSTOP)
				}
			}
			if err := keepertest.Run(t, next, stateDir, "shell-keeper", "install", destination); err != nil {
				t.Fatal("keeper state vetoed installation", err)
			}
			installed, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(installed, wanted) {
				t.Fatal("image not replaced", err)
			}
			if process != nil {
				if err := syscall.Kill(process.Cmd.Process.Pid, 0); err != nil {
					t.Fatal("installation ended old keeper", err)
				}
			}
		})
	}
}
