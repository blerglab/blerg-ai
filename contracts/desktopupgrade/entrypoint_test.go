package desktopupgrade

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerRejectsMixedUpgradeOptions(t *testing.T) {
	script, err := filepath.Abs("../../install/desktop/blerg-up.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--upgrade", "--no-build"}, {"--upgrade", "v0.6.0", "--no-daemon"}, {"--yes", "--upgrade"}, {"--upgrade", "--reset-admin"}} {
		cmd := exec.CommandContext(t.Context(), "bash", append([]string{script}, args...)...)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "cannot be mixed") {
			t.Fatalf("arguments %v: %s, %v", args, out, err)
		}
	}
}
