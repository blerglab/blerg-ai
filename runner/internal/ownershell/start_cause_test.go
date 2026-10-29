package ownershell

import (
	"fmt"
	"strings"
	"testing"
)

func TestStageRefusalsPreserveOnlyAllowlistedCauses(t *testing.T) {
	for _, cause := range []string{"verify_identity", "pty_open", "exec_shell", "containment_setup", "workspace_admission"} {
		err := fmt.Errorf("private upstream details: %w", RefuseStart(ReasonRuntimeStart, cause))
		if StartCause(err) != cause || RefusalReason(err) != ReasonRuntimeStart {
			t.Fatal("stage lost at refusal boundary")
		}
	}
	for _, cause := range []string{"Bearer fixture-secret\nforged", "/private/shell", strings.Repeat("x", 65536), "unknown_stage"} {
		err := RefuseStart(ReasonRuntimeStart, cause)
		if StartCause(err) != "" || SafeStartStage(cause) != "" || StartStageMessage(cause) != "" {
			t.Fatal("untrusted cause accepted")
		}
	}
}
