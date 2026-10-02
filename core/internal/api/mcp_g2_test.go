package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const queryURLSecret = "SUPERSECRETKEY"

// MINOR 34: a connection row that predates the query-string rule (or was written by hand) holds a
// secret in its URL. The public and the internal LIST and every other response that carries the
// connection show the URL without the query, with has_query set; only the internal token route,
// which the gateway needs, returns the URL as stored.
func TestMCPQueryStringSecretsAreRedactedInResponses(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET url = $2 WHERE id = $1`,
		e.connA, "https://mcp.example.com/mcp?key="+queryURLSecret+"#frag"); err != nil {
		t.Fatal(err)
	}
	wantURL := "https://mcp.example.com/mcp?…"
	check := func(name, body string) {
		t.Helper()
		if strings.Contains(body, queryURLSecret) || strings.Contains(body, "frag") {
			t.Errorf("%s leaks the query string: %s", name, body)
		}
		var doc map[string]any
		_ = json.Unmarshal([]byte(body), &doc)
		conns, _ := doc["connections"].([]any)
		c := doc
		if len(conns) > 0 {
			c, _ = conns[0].(map[string]any)
		}
		if c["url"] != wantURL || c["has_query"] != true {
			t.Errorf("%s: url=%v has_query=%v, want %q and true (%s)", name, c["url"], c["has_query"], wantURL, body)
		}
	}

	code, body := e.do(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil)
	if code != 200 {
		t.Fatalf("list = %d %s", code, body)
	}
	check("public list", body)

	code, body = e.do(t, http.MethodPatch, "/api/mcp/connections/"+e.connA, e.humanA, map[string]any{"name": "notes2"}, nil)
	if code != 200 {
		t.Fatalf("patch = %d %s", code, body)
	}
	check("public patch", body)

	code, body, _ = e.internal(t, "/internal/mcp/connections/list", testInternalKey, map[string]string{"account_id": e.acctA, "session_id": e.sidA})
	if code != 200 {
		t.Fatalf("internal list = %d %s", code, body)
	}
	check("internal list", body)

	code, body, _ = e.internal(t, defaultsPath, testInternalKey, map[string]any{
		"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA,
		"default_tools": map[string]any{"t": map[string]string{"mode": "allow", "hash": "h"}},
	})
	if code != 200 {
		t.Fatalf("internal defaults = %d %s", code, body)
	}
	check("internal defaults", body)

	// A connection without a query has no has_query and an unchanged url.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET url = 'https://mcp.example.com/mcp' WHERE id = $1`, e.connA); err != nil {
		t.Fatal(err)
	}
	_, body = e.do(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil)
	if strings.Contains(body, "has_query") || !strings.Contains(body, `"url":"https://mcp.example.com/mcp"`) {
		t.Errorf("a clean url was altered: %s", body)
	}

	// The token route hands the runner the URL as stored: the gateway needs it.
	if _, err := e.pool.Exec(ctx, `UPDATE mcp_connections SET url = $2 WHERE id = $1`, e.connA, "https://mcp.example.com/mcp?key="+queryURLSecret); err != nil {
		t.Fatal(err)
	}
	code, body, _ = e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA})
	var tok struct {
		URL string `json:"url"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &tok) != nil || tok.URL != "https://mcp.example.com/mcp?key="+queryURLSecret {
		t.Fatalf("token route = %d %s, want the stored url", code, body)
	}
}

// largestDefaultTools is the biggest default_tools the service accepts: 500 tools with
// 200-character names, 128-character hashes and the longest mode.
func largestDefaultTools() map[string]map[string]string {
	m := make(map[string]map[string]string, 500)
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("%03d-", i) + strings.Repeat("n", 196)
		m[name] = map[string]string{"mode": "propose", "hash": strings.Repeat("h", 128)}
	}
	return m
}

// MINOR 38: the body cap of the public PATCH and of the internal defaults route is above what the
// service accepts, so the largest legal default_tools round-trips through both.
func TestMCPLargestDefaultToolsRoundTrip(t *testing.T) {
	e := newMCPEnv(t)
	tools := largestDefaultTools()
	raw, _ := json.Marshal(map[string]any{"default_tools": tools})
	if len(raw) <= 128<<10 {
		t.Fatalf("test payload is only %d bytes: it no longer exercises the old 128 KiB cap", len(raw))
	}
	countTools := func(body string) int {
		t.Helper()
		var c struct {
			DefaultTools map[string]json.RawMessage `json:"default_tools"`
		}
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatalf("decoding %.200s: %v", body, err)
		}
		return len(c.DefaultTools)
	}

	code, body := e.do(t, http.MethodPatch, "/api/mcp/connections/"+e.connA, e.humanA, map[string]any{"default_tools": tools}, nil)
	if code != 200 {
		t.Fatalf("public PATCH of the largest default_tools = %d %.200s", code, body)
	}
	if n := countTools(body); n != 500 {
		t.Fatalf("public PATCH returned %d tools, want 500", n)
	}
	// Round trip: the stored value reads back whole through the internal list.
	code, body, _ = e.internal(t, "/internal/mcp/connections/list", testInternalKey, map[string]string{"account_id": e.acctA, "session_id": e.sidA})
	var list struct {
		Connections []struct {
			DefaultTools map[string]json.RawMessage `json:"default_tools"`
		} `json:"connections"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list.Connections) != 1 || len(list.Connections[0].DefaultTools) != 500 {
		t.Fatalf("internal list after PATCH = %d, %d tools", code, len(list.Connections))
	}

	// And through the internal defaults route.
	small := map[string]map[string]string{"x": {"mode": "allow", "hash": "h"}}
	if code, body, _ := e.internal(t, defaultsPath, testInternalKey, map[string]any{
		"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA, "default_tools": small,
	}); code != 200 || countTools(body) != 1 {
		t.Fatalf("internal defaults (small) = %d %.200s", code, body)
	}
	code, body, _ = e.internal(t, defaultsPath, testInternalKey, map[string]any{
		"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA, "default_tools": tools,
	})
	if code != 200 || countTools(body) != 500 {
		t.Fatalf("internal defaults of the largest default_tools = %d %.200s", code, body)
	}

	// Beyond the cap is still refused (413), so the limit is a limit and not absent.
	huge := map[string]any{"default_tools": map[string]any{"x": strings.Repeat("a", 1<<20)}}
	if code, _ := e.do(t, http.MethodPatch, "/api/mcp/connections/"+e.connA, e.humanA, huge, nil); code != http.StatusRequestEntityTooLarge {
		t.Errorf("a 1 MiB PATCH = %d, want 413", code)
	}
}
