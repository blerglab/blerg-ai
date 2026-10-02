package server

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// startRequestHash decides whether a reused Idempotency-Key is a retry. Adding the grant to
// the request (json:"-") and the mcp selection must not move the hash of any request that has
// none, or a retry across the upgrade would read as a different request. The constant was
// computed BEFORE the Grant field existed.
func TestStartRequestHashGolden(t *testing.T) {
	req := runnerStartRequest{
		Repo: "acme/widget", Title: "t", Prompt: "do it", Model: "sonnet", Effort: "high",
		Env: map[string]string{"A": "b"}, CallbackURL: "https://example.com/cb", Engine: "claude",
		Runtime: "cluster", AutoStop: true,
	}
	got, err := startRequestHash(req)
	if err != nil {
		t.Fatal(err)
	}
	const want = "bd2cfd14b267160b44c765a4beaf401668012940d93d1f3d50bab32425a6997d"
	if got != want {
		t.Fatalf("startRequestHash = %s, want %s", got, want)
	}

	// The in-process grant is not part of the request's identity and never reaches JSON.
	req.Grant = &ResolvedGrant{AccountID: "a", Connections: []mcpgw.GrantSpec{{ConnectionID: "c", Name: "n"}}}
	withGrant, err := startRequestHash(req)
	if err != nil || withGrant != want {
		t.Errorf("hash with a grant = %s (%v), want the same %s", withGrant, err, want)
	}
}
