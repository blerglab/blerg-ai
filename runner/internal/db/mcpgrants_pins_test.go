package db_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	pinSess1 = "aaaaaaaa-1111-4111-8111-111111111111"
	pinConn1 = "bbbbbbbb-1111-4111-8111-111111111111"
)

func pinGrant(sess, conn string, tools map[string]db.MCPGrantTool, token byte) db.MCPGrant {
	return db.MCPGrant{
		SessionID: sess, ConnectionID: conn, Name: "cal", AccountID: "acct-a", Tools: tools,
		URLSnapshot: "https://mcp.example.com/mcp", ProofKind: "session_id", ProofValue: "sid-1",
		TokenHash:  []byte{token, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		CallBudget: 200, Preset: db.PresetApproval, KnownTools: []string{"read", "send", "off_tool"},
	}
}

func TestGrantStoresPresetAndKnownTools(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}}, 1)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	got, err := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preset != db.PresetApproval {
		t.Fatalf("preset = %q, want approval", got.Preset)
	}
	if len(got.KnownTools) != 3 || got.KnownTools[2] != "off_tool" {
		t.Fatalf("known_tools = %v", got.KnownTools)
	}
	if !got.Tools["read"].IsListed() {
		t.Fatal("an entry without listed must read as listed")
	}
}

func TestGrantWithoutPresetOrKnownToolsIsCustomAndNil(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}}, 2)
	g.Preset, g.KnownTools = "", nil
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	got, err := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Preset != db.PresetCustom || got.KnownTools != nil {
		t.Fatalf("preset=%q known=%v, want custom and nil", got.Preset, got.KnownTools)
	}
}

func TestListedFalseSurvivesRoundTrip(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	f := false
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1", Listed: &f}}, 3)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	got, err := db.MCPGrantByTokenHash(ctx, pool, g.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tools["read"].IsListed() {
		t.Fatal("listed:false was dropped by the round trip")
	}
	// The wire form keeps the key only when set.
	raw, _ := json.Marshal(db.MCPGrantTool{Mode: "allow", Hash: "h1"})
	if string(raw) != `{"mode":"allow","hash":"h1"}` {
		t.Fatalf("unset listed must be omitted: %s", raw)
	}
}

// Review Focus 1: a resume (ReplaceMCPGrants) must carry the ROW's tools, preset and known_tools,
// not the caller's copy, so an accept committed between the read and the replace survives.
func TestReplaceCarriesRowToolsPresetAndKnownTools(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	g := pinGrant(pinSess1, pinConn1, map[string]db.MCPGrantTool{"read": {Mode: "allow", Hash: "h1"}}, 4)
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{g}); err != nil {
		t.Fatal(err)
	}
	// Something (an accept) moves the row's pin to h2 after the caller read h1.
	if _, err := pool.Exec(ctx, `UPDATE session_mcp_grants SET tools = jsonb_set(tools, '{read,hash}', '"h2"'), calls_used = 7
		WHERE session_id = $1 AND connection_id = $2`, pinSess1, pinConn1); err != nil {
		t.Fatal(err)
	}
	stale := g // the caller's copy still says h1, approval, three known tools
	stale.TokenHash = []byte{9, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	stale.Preset, stale.KnownTools = db.PresetCustom, nil
	if err := db.ReplaceMCPGrants(ctx, pool, pinSess1, []db.MCPGrant{stale}); err != nil {
		t.Fatal(err)
	}
	got, err := db.MCPGrantByTokenHash(ctx, pool, stale.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tools["read"].Hash != "h2" {
		t.Fatalf("replace lost the accepted hash: %q", got.Tools["read"].Hash)
	}
	if got.Preset != db.PresetApproval || len(got.KnownTools) != 3 {
		t.Fatalf("replace lost preset/known_tools: %q %v", got.Preset, got.KnownTools)
	}
	if got.CallsUsed != 7 {
		t.Fatalf("calls_used = %d, want 7", got.CallsUsed)
	}
}
