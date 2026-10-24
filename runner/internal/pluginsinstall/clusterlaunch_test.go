package pluginsinstall

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestClusterPluginCommandBoundary(t *testing.T) {
	wanted := errors.New("fixture boundary refusal")
	called := false
	installer := newInstaller(Options{StartCommand: func(cmd *exec.Cmd) error {
		called = true
		if cmd.Dir == "" || cmd.Path == "" {
			t.Fatal("missing launch context")
		}
		return wanted
	}})
	_, err := installer.execRunner(context.Background(), "sh", "-c", "exit 77")
	if !called || !errors.Is(err, wanted) {
		t.Fatal("plugin bypassed cluster launch boundary")
	}
}
