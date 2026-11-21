package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestClusterToolTempDoesNotMoveDaemonPrivateTemp(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	private := os.TempDir()
	cmd := exec.Command("fixture")
	cmd.Env = []string{"TMPDIR=old", "OTHER=kept"}
	clusterToolEnvironment(cmd)
	if !slices.Contains(cmd.Env, "TMPDIR=/workspace/tool-tmp") || slices.Contains(cmd.Env, "TMPDIR=old") || !slices.Contains(cmd.Env, "OTHER=kept") {
		t.Fatalf("tool env %v", cmd.Env)
	}
	if os.TempDir() != private {
		t.Fatal("daemon temp moved")
	}
	f, err := os.CreateTemp("", "private-")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	defer os.Remove(f.Name())
	if filepath.Dir(f.Name()) != private {
		t.Fatal("private file escaped daemon temp")
	}
}
