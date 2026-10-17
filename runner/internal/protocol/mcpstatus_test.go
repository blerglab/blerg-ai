package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPStatusSanitizesOnlyPublicMetadata(t *testing.T) {
	var input MCPStatusPayload
	raw := `{"engine":"codex","since":"2026-10-17T12:00:00Z","settled":false,"url":"https://private.example","token":"fixture-private","env":{"SECRET":"fixture-private"},"servers":[{"name":"jira","status":"connected","source":"project","tools":3,"url":"https://private.example","token":"fixture-private"},{"name":"https://user:fixture-private@private.example","status":"connected","source":"user","tools":8},{"name":"jira","status":"failed","source":"user","tools":9},{"name":"other","status":"nonsense","source":"https://private.example","tools":-1}],"hold":{"since":"2026-10-17T12:00:00Z","until":"2026-10-17T12:00:30Z"}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	out := SanitizeMCPStatus(input)
	if len(out.Servers) != 2 || out.Servers[0].Name != "jira" || out.Servers[0].Tools != 3 || out.Servers[1].Status != "unknown" || out.Servers[1].Tools != 0 || out.Servers[1].Source != "" || out.Hold == nil {
		t.Fatalf("sanitized metadata: %+v", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private.example", "fixture-private", "SECRET", "\"url\"", "\"env\"", "\"token\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("private metadata retained: %s", forbidden)
		}
	}
}

func TestMCPStatusReleaseClearsHoldAndRejectsMalformedTimes(t *testing.T) {
	p := MCPStatusPayload{Engine: "claude", Since: "2026-10-17T12:00:00Z", Released: "person", Hold: &MCPHold{Since: "2026-10-17T12:00:00Z", Until: "2026-10-17T12:00:30Z"}}
	if clean := SanitizeMCPStatus(p); clean.Hold != nil || clean.Released != "person" || clean.Servers == nil {
		t.Fatalf("release: %+v", clean)
	}
	p.Released = "https://private.example"
	p.Since = "invalid"
	p.Engine = "https://private.example"
	if clean := SanitizeMCPStatus(p); clean.Since != "" || clean.Engine != "" || clean.Released != "" {
		t.Fatalf("malformed fields: %+v", clean)
	}
	p.Engine = "future-engine"
	p.Since = "2026-10-17T12:00:00Z"
	p.Hold.Until = "2026-10-17T11:00:00Z"
	if clean := SanitizeMCPStatus(p); clean.Engine != "future-engine" || clean.Hold != nil {
		t.Fatalf("engine extension/invalidhold: %+v", clean)
	}
}
