package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestMCPPinPolicyPatchListAndInternalResponses(t *testing.T) {
	e := newMCPEnv(t)
	sameOrigin := map[string]string{"Origin": e.srv.URL}
	// Public PATCH flips the policy and the public view shows it.
	code, body := e.do(t, http.MethodPatch, "/api/mcp/connections/"+e.connA, e.humanA, map[string]any{"pin_policy": "auto_accept"}, sameOrigin)
	if code != http.StatusOK || !strings.Contains(body, `"pin_policy":"auto_accept"`) {
		t.Fatalf("patch: %d %s", code, body)
	}
	// An unknown value is a 400.
	if code, _ := e.do(t, http.MethodPatch, "/api/mcp/connections/"+e.connA, e.humanA, map[string]any{"pin_policy": "yes"}, sameOrigin); code != http.StatusBadRequest {
		t.Fatalf("bad policy status %d", code)
	}
	// The events route lists the flip for the owner and 404s for another account.
	code, body = e.do(t, http.MethodGet, "/api/mcp/connections/"+e.connA+"/events", e.humanA, nil, nil)
	if code != http.StatusOK || !strings.Contains(body, `"action":"pin_policy"`) || !strings.Contains(body, `"to_value":"auto_accept"`) {
		t.Fatalf("events: %d %s", code, body)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/mcp/connections/"+e.connA+"/events", e.humanB, nil, nil); code != http.StatusNotFound {
		t.Fatalf("other account events status %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/mcp/connections/"+e.connA+"/events", e.agentA, nil, nil); code != http.StatusForbidden {
		t.Fatalf("agent token events status %d", code)
	}
	// The internal list and token responses carry it.
	code, body, _ = e.internal(t, "/internal/mcp/connections/list", testInternalKey, map[string]string{"account_id": e.acctA, "session_id": e.sidA})
	if code != http.StatusOK || !strings.Contains(body, `"pin_policy":"auto_accept"`) {
		t.Fatalf("internal list: %d %s", code, body)
	}
	code, body, _ = e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "session_id": e.sidA, "connection_id": e.connA})
	if code != http.StatusOK || !strings.Contains(body, `"pin_policy":"auto_accept"`) {
		t.Fatalf("internal token: %d %s", code, body)
	}
}
