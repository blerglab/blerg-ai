package mcpgw

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestReissueGrantsReplacesTokensAtomically(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grant("echoer", map[string]string{"echo": ModeAllow}, 0)
	ctx := context.Background()
	proof := Proof{AccountID: "acct-1", SessionID: "login-2"}
	spec := GrantSpec{ConnectionID: gh.connectionID, Name: "echoer", URL: h.up.url(),
		Tools: map[string]ToolGrant{"echo": {Mode: ModeAllow, Hash: h.up.hash("echo")}}}

	if r := h.call(gh, "tools/list", map[string]any{}); r.status != 200 {
		t.Fatalf("old token before reissue: %d %s", r.status, r.raw)
	}
	toks, err := ReissueGrants(ctx, h.pool, gh.sessionID, "acct-1", proof, []GrantSpec{spec})
	if err != nil {
		t.Fatalf("ReissueGrants: %v", err)
	}
	fresh := toks[gh.connectionID]
	if fresh == "" || fresh == gh.token {
		t.Fatalf("new token %q must differ from the old one", fresh)
	}
	if r := h.call(gh, "tools/list", map[string]any{}); r.status != 401 {
		t.Errorf("old token after reissue: %d, want 401", r.status)
	}
	nh := gh
	nh.token = fresh
	if r := h.call(nh, "tools/list", map[string]any{}); r.status != 200 {
		t.Errorf("new token: %d %s", r.status, r.raw)
	}

	// A failing reissue (an invalid name) changes nothing: the current token still works.
	bad := spec
	bad.Name = "Not A Name"
	if _, err := ReissueGrants(ctx, h.pool, gh.sessionID, "acct-1", proof, []GrantSpec{bad}); err == nil {
		t.Fatal("reissue with an invalid spec succeeded")
	}
	if r := h.call(nh, "tools/list", map[string]any{}); r.status != 200 {
		t.Errorf("token after a failed reissue: %d, want it untouched", r.status)
	}
}

// F3 MAJOR 8: a resume re-issues the tokens but must not hand out a fresh call budget.
func TestReissueGrantsCarriesCallsUsed(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grant("echoer", map[string]string{"echo": ModeAllow}, 5)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if r := h.call(gh, "tools/list", map[string]any{}); r.status != 200 {
			t.Fatalf("list %d: %d %s", i, r.status, r.raw)
		}
	}
	proof := Proof{AccountID: "acct-1", SessionID: "login-2"}
	spec := GrantSpec{ConnectionID: gh.connectionID, Name: "echoer", URL: h.up.url(), CallBudget: 5,
		Tools: map[string]ToolGrant{"echo": {Mode: ModeAllow, Hash: h.up.hash("echo")}}}
	toks, err := ReissueGrants(ctx, h.pool, gh.sessionID, "acct-1", proof, []GrantSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMCPGrantsForSession(ctx, h.pool, gh.sessionID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("grants: %v %v", rows, err)
	}
	if rows[0].CallsUsed < 3 {
		t.Fatalf("calls_used after reissue = %d, want the 3+ already spent carried over", rows[0].CallsUsed)
	}
	nh := gh
	nh.token = toks[gh.connectionID]
	ok := 0
	for i := 0; i < 5; i++ {
		if r := h.call(nh, "tools/list", map[string]any{}); r.status == 200 && r.errMessage() == "" {
			ok++
		}
	}
	if ok > 2 {
		t.Errorf("%d calls succeeded after the reissue, want at most the 2 left of the budget", ok)
	}
	// A connection that is new to the set starts at zero.
	spec2 := spec
	spec2.ConnectionID = newUUID(t)
	spec2.Name = "other"
	if _, err := ReissueGrants(ctx, h.pool, gh.sessionID, "acct-1", proof, []GrantSpec{spec2}); err != nil {
		t.Fatal(err)
	}
	rows, _ = db.ListMCPGrantsForSession(ctx, h.pool, gh.sessionID)
	if len(rows) != 1 || rows[0].CallsUsed != 0 {
		t.Errorf("a new connection must start at zero: %+v", rows)
	}
}
