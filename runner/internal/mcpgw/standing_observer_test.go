package mcpgw

import (
	"context"
	"testing"
)

func TestStandingAuthorityObserverIsSeparateFromStandaloneCalls(t *testing.T) {
	var observed []AuthorityObservation
	b := newBoardHarness(t, func(c *Config) {
		c.ObserveAuthority = func(_ context.Context, o AuthorityObservation) { observed = append(observed, o) }
	})
	proof := Proof{AccountID: "acct-1", SessionID: "login-1"}
	call := func(p Proof) {
		_, _ = b.gw.CallBoard(t.Context(), BoardCall{Proof: p, BoardID: boardTestID, SessionRef: "audit-only", Tool: "blerg_card_get", Arguments: []byte(`{"card_id":"c1"}`), ReadOnly: true})
	}
	call(proof)
	if len(observed) != 1 || observed[0].State != "active" || observed[0].SessionID != "audit-only" || observed[0].Kind != "Ticket board" {
		t.Fatalf("observations=%+v", observed)
	}
	b.ex.err = ErrStandingSuspended
	call(proof)
	if len(observed) != 2 || observed[1].State != "suspended" {
		t.Fatalf("suspension observations=%+v", observed)
	}
	b.ex.err = ErrBoardOwnerMissing
	call(proof)
	if len(observed) != 3 || observed[2].State != "unavailable" || observed[2].Cause != "board_owner_missing" {
		t.Fatalf("ownerless observation lost safe cause: %+v", observed)
	}
	call(Proof{AccountID: "acct-1", TokenID: "agent-token"})
	if len(observed) != 3 {
		t.Fatal("token-origin call selected standing observation")
	}
}

func TestStandingAuthorityObserverSkipsProviderSessionGrant(t *testing.T) {
	h, grant, _, core := newScopedGatewayHarness(t)
	var observed []AuthorityObservation
	h.gw.cfg.ObserveAuthority = func(_ context.Context, o AuthorityObservation) { observed = append(observed, o) }
	if r := h.callTool(grant, "echo", map[string]any{"msg": "synthetic"}); r.result() == nil || r.result()["isError"] == true {
		t.Fatalf("provider call failed: %s", r.raw)
	}
	if core.legacyCalls.Load() != 0 {
		t.Fatal("provider binding used account token path")
	}
	if len(observed) != 0 {
		t.Fatalf("provider-session grant sent standing observations: %+v", observed)
	}
}
