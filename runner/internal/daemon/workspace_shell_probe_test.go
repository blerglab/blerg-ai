package daemon

import (
	"os/exec"
	"testing"
)

func TestWorkspaceShellProbeRefusesPermissionErrorWithoutTraceback(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	script := `import os,types
os.listdir=lambda path:['123']
def stat(path):
 if path.endswith('blerg-shell-supervisor'):return types.SimpleNamespace(st_dev=1,st_ino=1)
 if path.endswith('/exe'):raise PermissionError('protected process')
 return types.SimpleNamespace(st_uid=os.geteuid())
os.stat=stat
` + workspaceShellProbe
	out, err := exec.Command(python, "-I", "-S", "-c", script).CombinedOutput()
	if err == nil {
		t.Fatal("permission error supplied false empty proof")
	}
	if len(out) != 0 {
		t.Fatalf("permission failure was not handled: %s", out)
	}
}
