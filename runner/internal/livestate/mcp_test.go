package livestate

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMCPLatestSnapshotHoldAndRelease(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	pending := json.RawMessage(`{"engine":"codex","since":"2026-10-16T12:00:00Z","servers":[{"name":"jira","status":"pending","source":"gateway","tools":0}],"hold":{"since":"2026-10-16T12:00:00Z","until":"2026-10-16T12:00:30Z"}}`)
	settled := json.RawMessage(`{"engine":"codex","since":"2026-10-16T12:00:00Z","servers":[{"name":"jira","status":"connected","source":"gateway","tools":3}],"settled":true,"released":"settled"}`)
	events := []Event{{Seq: 1, Kind: "mcp_status", Payload: pending}}
	st := Build(events, Options{Now: now, Status: "starting"})
	if st.MCP == nil || st.MCPHold == nil || st.MCP.Servers[0].Status != "pending" {
		t.Fatalf("pending snapshot %+v", st)
	}
	for _, status := range []string{"ended", "stopped", "error", "disconnected"} {
		st = Build(events, Options{Now: now, Status: status})
		if st.MCPHold != nil || st.MCP.Hold != nil {
			t.Fatalf("stale hold for %s", status)
		}
	}
	st = Build(events, Options{Now: now.Add(31 * time.Second), Status: "running"})
	if st.MCPHold != nil {
		t.Fatal("expired hold remained")
	}
	events = append([]Event{{Seq: 2, Kind: "mcp_status", Payload: settled}}, events...)
	st = Build(events, Options{Now: now, Status: "running"})
	if st.MCP == nil || !st.MCP.Settled || st.MCPHold != nil || st.MCP.Released != "settled" || st.MCP.Servers[0].Tools != 3 {
		t.Fatalf("latest %+v", st)
	}
	for _, release := range []string{"person", "timeout"} {
		p := strings.Replace(string(pending), `"engine":"codex"`, `"released":"`+release+`","engine":"codex"`, 1)
		st = Build([]Event{{Seq: 1, Kind: "mcp_status", Payload: json.RawMessage(p)}}, Options{Now: now, Status: "running"})
		if st.MCPHold != nil || st.MCP.Released != release {
			t.Fatalf("release %s: %+v", release, st)
		}
	}
}

func TestMCPOldPeersAndMalformedSnapshot(t *testing.T) {
	st := Build([]Event{{Seq: 1, Kind: "future_fixture_kind", Payload: json.RawMessage(`{}`)}}, Options{})
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), `"mcp`) {
		t.Fatalf("old peer fabricated MCP state: %s", b)
	}
	st = Build([]Event{{Seq: 1, Kind: "mcp_status", Payload: json.RawMessage(`{"engine":"claude","since":"2026-10-16T12:00:00Z","servers":[]}`)}, {Seq: 2, Kind: "mcp_status", Payload: json.RawMessage(`{"servers":"bad"}`)}}, Options{})
	if st.MCP == nil || st.MCP.Engine != "claude" {
		t.Fatal("malformed snapshot erased valid state")
	}
}
