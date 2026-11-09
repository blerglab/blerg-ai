package livedata

import (
	"testing"
	"time"
)

func TestPostureReportRejectsUnknownOrContradictoryClaims(t *testing.T) {
	for _, tc := range []struct {
		safe  bool
		cause string
		want  bool
	}{
		{true, "isolated_cluster", true}, {false, "desktop_container_runtime", true}, {false, "host_runtime", true}, {false, "privileged_runtime", true}, {true, "host_runtime", false}, {false, "isolated_cluster", false}, {true, "agent_says_safe", false}, {false, "unknown", false},
	} {
		if got := (PostureReport{Safe: tc.safe, Cause: tc.cause, CheckedAt: time.Now()}).Valid(); got != tc.want {
			t.Fatal("posture contract accepted an invalid claim", tc.cause, got)
		}
	}
	if (PostureReport{Safe: true, Cause: "isolated_cluster"}).Valid() {
		t.Fatal("posture report without a check time accepted")
	}
}
