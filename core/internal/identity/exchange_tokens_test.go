package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

func TestCreateExchangeTokenClaims(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-claims", "member")

	rec, raw, err := svc.CreateExchangeToken(ctx, acct, "board", "board-42", nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := svc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Kind != string(cid.Agent) || p.Sub != rec.ID || p.Project != "board-42" {
		t.Errorf("kind/sub/project = %q/%q/%q", p.Kind, p.Sub, p.Project)
	}
	if p.OnBehalfOf != acct || p.Lineage != acct {
		t.Errorf("on_behalf_of/lineage = %q/%q, want %q", p.OnBehalfOf, p.Lineage, acct)
	}
	if want := []string{"card.read", "card.write"}; len(p.Caps) != 2 || !p.Has(want[0]) || !p.Has(want[1]) || p.Has("column.write") {
		t.Errorf("caps = %v, want %v", p.Caps, want)
	}
	if d := time.Until(time.Unix(p.ExpiresAt, 0)); d < 9*time.Minute || d > 10*time.Minute+5*time.Second {
		t.Errorf("exp in %v, want ~10m", d)
	}
	if p.ExpiresAt != rec.ExpiresAt.Unix() {
		t.Errorf("exp %d != row expires_at %d", p.ExpiresAt, rec.ExpiresAt.Unix())
	}
	var kind, aud string
	if err := st.Pool().QueryRow(ctx, `SELECT kind, aud FROM agent_tokens WHERE id = $1`, rec.ID).Scan(&kind, &aud); err != nil || kind != AgentTokenKindExchange || aud != "blerg-board" {
		t.Fatalf("row kind/aud = %q/%q err=%v", kind, aud, err)
	}
	// Wrong audience does not verify.
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); err == nil {
		t.Error("exchange token verified for another audience")
	}
}

func TestCreateExchangeTokenCapsAreIntersectedWithRole(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	admin := insertAccount(t, st, "exch-admin", "admin")
	member := insertAccount(t, st, "exch-member", "member")
	keys, _ := svc.JWKS(ctx)

	// Admin holds more than the target, yet the token carries ONLY the target's caps.
	_, raw, err := svc.CreateExchangeToken(ctx, admin, "board", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Caps) != 2 || p.Has("column.write") || p.Has("board.admin") || p.Has("gate.bypass") || p.Has("secrets.read") || p.Has("session.start") {
		t.Errorf("admin exchange caps = %v, want exactly card.read/card.write", p.Caps)
	}

	// A role that lacks a capability yields a token without it (the map is a package var).
	saved := PlatformRoleCaps["member"]
	PlatformRoleCaps["member"] = []string{"card.read", "session.start"}
	defer func() { PlatformRoleCaps["member"] = saved }()
	_, raw, err = svc.CreateExchangeToken(ctx, member, "board", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err = cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Caps) != 1 || !p.Has("card.read") {
		t.Errorf("reduced-role exchange caps = %v, want [card.read]", p.Caps)
	}
}

func TestCreateExchangeTokenUnknownTargetAndDisabled(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-bad", "member")
	if _, _, err := svc.CreateExchangeToken(ctx, acct, "runner", "b", nil); !errors.Is(err, ErrUnknownExchangeTarget) {
		t.Errorf("unknown target err = %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); !errors.Is(err, ErrAccountDisabled) {
		t.Errorf("disabled err = %v", err)
	}
}

func TestExchangeTokenRevocationAndLogOutEverywhere(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-rev", "member")
	other := insertAccount(t, st, "exch-rev-other", "member")
	keys, _ := svc.JWKS(ctx)

	rec, raw, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Another account cannot revoke it; a plain token of the same account is not an exchange token.
	if err := svc.RevokeExchangeToken(ctx, other, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-account revoke = %v, want ErrNotFound", err)
	}
	plain, _, err := svc.CreateAgentToken(ctx, acct, "p", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeExchangeToken(ctx, acct, plain.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a non-exchange token = %v, want ErrNotFound", err)
	}
	if _, err := cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps); err != nil {
		t.Fatalf("token died from refused revokes: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := svc.RevokeExchangeToken(ctx, acct, rec.ID); err != nil {
			t.Fatalf("revoke #%d: %v", i, err)
		}
	}
	if _, err := cid.Verify(raw, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("after revoke verify err = %v, want ErrRevoked", err)
	}

	// Log out everywhere kills a live one.
	_, raw2, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // past the whole-second revocation boundary
	if err := svc.RevokeAccountEverywhere(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := cid.Verify(raw2, "blerg-board", keys, liveChecker(t, svc), noSensitiveCaps); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("after log-out-everywhere verify err = %v, want ErrRevoked", err)
	}
}

func TestExchangeTokensAreHiddenAndUncounted(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-cap", "member")

	for i := 0; i < maxLiveAgentTokens; i++ {
		if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); err != nil {
			t.Fatalf("exchange #%d: %v", i, err)
		}
	}
	// 50 live exchange tokens leave the whole 50-token user cap free, for both mint paths.
	for i := 0; i < maxLiveAgentTokens-1; i++ {
		if _, _, err := svc.CreateAgentToken(ctx, acct, "t", "board", time.Hour); err != nil {
			t.Fatalf("user token #%d: %v", i, err)
		}
	}
	if _, err := svc.CreateUnsignedAgentToken(ctx, acct, "cron", time.Hour); err != nil {
		t.Fatalf("cron token (50th): %v", err)
	}
	if _, err := svc.CreateUnsignedAgentToken(ctx, acct, "cron", time.Hour); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("51st user-visible token err = %v, want ErrTooManyTokens", err)
	}
	list, err := svc.ListAgentTokens(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != maxLiveAgentTokens-1 {
		t.Fatalf("list has %d tokens, want %d (no exchange rows)", len(list), maxLiveAgentTokens-1)
	}
	// Exchange tokens have their own bound.
	for i := 0; i < maxLiveExchangeTokens; i++ {
		if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil); errors.Is(err, ErrTooManyTokens) {
			return
		} else if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("exchange tokens have no cap of their own")
}
