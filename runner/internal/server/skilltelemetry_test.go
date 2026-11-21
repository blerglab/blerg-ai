package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"strings"
	"testing"
)

func TestSkillReportingCannotBecomeConfirmedLoad(t *testing.T) {
	fx := newTopicFx(t)
	for _, basis := range []string{"content_mismatch", "file_unavailable", "partial_native_observation"} {
		msg := protocol.AgentEvent{SessionID: fx.sess, Kind: "skill_load", Payload: json.RawMessage(`{"skill":"ux-design","version":"` + strings.Repeat("a", 64) + `","mode":"on_demand","bytes":10,"basis":"` + basis + `"}`)}
		if ok, err := canonicalSkillEvent(t.Context(), fx.pool, &msg); ok || err != nil {
			t.Fatalf("unverified load accepted: %v %v", ok, err)
		}
		msg.Kind = "skill_reporting"
		if ok, err := canonicalSkillEvent(t.Context(), fx.pool, &msg); !ok || err != nil {
			t.Fatalf("gap lost: %v %v", ok, err)
		}
	}
}
