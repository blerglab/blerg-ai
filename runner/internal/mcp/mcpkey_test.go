package mcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
)

// MCP connections can only be attached from the launch sheet by a signed-in person. The
// runner's own start_session tool refuses an `mcp` argument (runner key, agent token or a
// human token alike) instead of dropping it and starting a session without the connections
// the caller expected, and starts nothing.
func TestMCPStartSessionRefusesTheMCPArgument(t *testing.T) {
	api, pool := clusterMCPAPI(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.SetCoreAuth(mcpCoreAuth(t, pub, "core-1"))
	srv := serveMCP(t, api)
	exp := time.Now().Unix() + 300
	agent := mcpToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-a", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-a",
		Caps: []string{"session.start"}, ExpiresAt: exp,
	})
	human := mcpToken(t, priv, "core-1", identity.Claims{
		Sub: "acct-a", Aud: "blerg-runner", Kind: "human", Sid: "sid-1",
		Caps: []string{"session.start"}, ExpiresAt: exp,
	})
	for name, bearer := range map[string]string{"runner key": mcpTestKey, "agent token": agent, "human token": human} {
		for _, arg := range []string{"mcp", "MCP"} {
			res := callAs(t, srv, bearer, "start_session", map[string]any{
				"repo": "org/proj", "prompt": "go",
				arg: []map[string]any{{"connection": "00000000-0000-4000-8000-000000000001"}},
			})
			if res["isError"] != true {
				t.Errorf("%s with %q: %v, want a tool error", name, arg, res)
				continue
			}
			body := toolJSON(t, res)
			msg, _ := body["error"].(string)
			if body["status"] != float64(http.StatusBadRequest) || !strings.Contains(msg, "mcp is not accepted on this route") {
				t.Errorf("%s with %q: %v, want 400 and the refusal", name, arg, body)
			}
		}
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions after the refused starts = %d (%v), want 0", n, err)
	}
	// The same tool without the argument still starts.
	ok := toolJSON(t, call(t, srv, "start_session", map[string]any{"repo": "org/proj", "prompt": "go"}))
	if ok["session_id"] == nil {
		t.Errorf("start without mcp: %v", ok)
	}
}
