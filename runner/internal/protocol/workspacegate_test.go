package protocol

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceReleaseGateSuppressesBothHopCapabilities(t *testing.T) {
	if WorkspaceRuntimeGateVerified {
		t.Fatal("workspace release acceptance has not been recorded")
	}
	capabilities := VerifiedWorkspaceCapabilities()
	if capabilities != (WorkspaceCapabilities{}) {
		t.Fatal("unverified workspace runtime advertised")
	}
	for _, message := range []any{
		DaemonHello{WorkspaceCapabilities: capabilities},
		DaemonWelcome{WorkspaceCapabilities: capabilities},
	} {
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]json.RawMessage
		if err = json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if _, exists := envelope["workspace_capabilities"]; exists {
			t.Fatal("gated capabilities must be absent on the wire")
		}
	}
}
