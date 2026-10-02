package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const defaultsPath = "/internal/mcp/connections/defaults"

func TestMCPInternalDefaultsMatrix(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	tokA, _, err := e.idSvc.CreateAgentToken(ctx, e.acctA, "a-tool", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"read_note": map[string]string{"mode": "allow", "hash": "h1"}}
	req := func(account, sid, conn string, tools any) map[string]any {
		return map[string]any{"account_id": account, "session_id": sid, "connection_id": conn, "default_tools": tools}
	}
	stored := func() string {
		var s string
		if err := e.pool.QueryRow(ctx, `SELECT default_tools::text FROM mcp_connections WHERE id = $1`, e.connA).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := []struct {
		name string
		key  string
		body map[string]any
		want int
	}{
		{"no key", "", req(e.acctA, e.sidA, e.connA, good), 401},
		{"wrong key", "wrong", req(e.acctA, e.sidA, e.connA, good), 401},
		{"a human bearer is not the key", e.humanA, req(e.acctA, e.sidA, e.connA, good), 401},
		{"dead session", testInternalKey, req(e.acctA, noSuchSession, e.connA, good), 404},
		{"other account's session", testInternalKey, req(e.acctA, e.sidB, e.connA, good), 404},
		{"cross-account connection", testInternalKey, req(e.acctB, e.sidB, e.connA, good), 404},
		{"unknown connection", testInternalKey, req(e.acctA, e.sidA, noSuchSession, good), 404},
		{"no proof", testInternalKey, map[string]any{"account_id": e.acctA, "connection_id": e.connA, "default_tools": good}, 400},
		{"token_id proof refused", testInternalKey, map[string]any{"account_id": e.acctA, "token_id": tokA.ID, "connection_id": e.connA, "default_tools": good}, 400},
		{"both proofs", testInternalKey, map[string]any{"account_id": e.acctA, "token_id": tokA.ID, "session_id": e.sidA, "connection_id": e.connA, "default_tools": good}, 400},
		{"bad connection id", testInternalKey, req(e.acctA, e.sidA, "nope", good), 400},
		{"missing default_tools", testInternalKey, map[string]any{"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA}, 400},
		{"default_tools not an object", testInternalKey, req(e.acctA, e.sidA, e.connA, []string{"x"}), 400},
		{"bad mode", testInternalKey, req(e.acctA, e.sidA, e.connA, map[string]any{"t": map[string]string{"mode": "off", "hash": "h"}}), 400},
		{"missing hash", testInternalKey, req(e.acctA, e.sidA, e.connA, map[string]any{"t": map[string]string{"mode": "allow"}}), 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, _ := e.internal(t, defaultsPath, tc.key, tc.body)
			if code != tc.want {
				t.Fatalf("= %d (%s), want %d", code, body, tc.want)
			}
			if strings.Contains(body, mcpSecret) {
				t.Fatalf("secret in a body: %s", body)
			}
		})
	}
	if got := stored(); got != "{}" {
		t.Fatalf("rejected requests changed default_tools: %s", got)
	}

	// The 404 bodies are uniform whatever the reason.
	_, dead, _ := e.internal(t, defaultsPath, testInternalKey, req(e.acctA, noSuchSession, e.connA, good))
	_, cross, _ := e.internal(t, defaultsPath, testInternalKey, req(e.acctB, e.sidB, e.connA, good))
	_, missing, _ := e.internal(t, defaultsPath, testInternalKey, req(e.acctA, e.sidA, noSuchSession, good))
	if dead != cross || cross != missing {
		t.Fatalf("404 bodies differ: %q %q %q", dead, cross, missing)
	}

	// Success persists and leaves the secret untouched.
	code, body, _ := e.internal(t, defaultsPath, testInternalKey, req(e.acctA, e.sidA, e.connA, good))
	if code != 200 {
		t.Fatalf("success = %d %s", code, body)
	}
	if strings.Contains(body, mcpSecret) {
		t.Fatalf("secret in the response: %s", body)
	}
	var got struct {
		ID           string          `json:"id"`
		DefaultTools json.RawMessage `json:"default_tools"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != e.connA || !strings.Contains(string(got.DefaultTools), `"read_note"`) {
		t.Fatalf("response = %s", body)
	}
	if s := stored(); !strings.Contains(s, `"read_note"`) || !strings.Contains(s, `"hash": "h1"`) && !strings.Contains(s, `"hash":"h1"`) {
		t.Fatalf("not persisted: %s", s)
	}
	code, tokBody, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA})
	if code != 200 || !strings.Contains(tokBody, mcpSecret) {
		t.Fatalf("secret changed by a defaults update: %d %s", code, tokBody)
	}
	// An empty object clears the defaults.
	if code, body, _ := e.internal(t, defaultsPath, testInternalKey, req(e.acctA, e.sidA, e.connA, map[string]any{})); code != 200 {
		t.Fatalf("clear = %d %s", code, body)
	}
	if s := stored(); s != "{}" {
		t.Fatalf("not cleared: %s", s)
	}
}
