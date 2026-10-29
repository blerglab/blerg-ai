package daemon

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os/exec"
	"testing"
)

func TestP6NativeBashUsesHostWriterBoundary(t *testing.T) {
	host, _, cwd := newTestHost(t, &scriptedAgentProvider{}, "")
	denied := errors.New("owned start refused")
	cfg := host.nativeLoopConfig(protocol.SpawnSession{SessionID: "native-boundary", Kind: "agent"}, nativeLoopInputs{workDir: cwd, confined: true, startCommand: func(*exec.Cmd) error { return denied }})
	bash, ok := cfg.Registry.Get("bash")
	if !ok {
		t.Fatal("missing bash tool")
	}
	if _, err := bash.Execute(t.Context(), json.RawMessage(`{"command":"true"}`)); !errors.Is(err, denied) {
		t.Fatal("NATIVE_START_BOUNDARY: bash escaped the host writer boundary", err)
	}
}
