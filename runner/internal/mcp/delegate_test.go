package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A session's own token (the credential a parent session uses for its children) is never an MCP
// credential: /mcp answers 401 before any tool runs, so the in-process delegate kind can never be
// reached from here.
func TestMCPRefusesASessionToken(t *testing.T) {
	api, pool := clusterMCPAPI(t)
	srv := serveMCP(t, api)
	ctx := context.Background()
	const daemon = "00000000-0000-4000-8000-0000000000d1"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	const parent = "00000000-0000-4000-8000-00000000aa01"
	if err := db.InsertSessionAs(ctx, pool, parent, daemon, "running", "/repos/app", "app", "Parent", "",
		db.SessionOrigin{SpawningAccount: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	tok, err := db.MintSessionToken(ctx, pool, parent, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"tools/list", "tools/call"} {
		resp, _ := rpc(t, srv, tok, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method,
			"params": map[string]any{"name": "list_sessions", "arguments": map[string]any{}}})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s with a session token = %d, want 401", method, resp.StatusCode)
		}
	}
}
