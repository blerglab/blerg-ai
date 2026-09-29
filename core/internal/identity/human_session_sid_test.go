package identity_test

import (
	"context"
	"testing"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

// verifyHuman verifies tok as the runner would and returns its claims.
func verifyHuman(t *testing.T, ctxSvc interface {
	JWKS(context.Context) (cid.KeySet, error)
}, tok, aud string) cid.Principal {
	t.Helper()
	keys, err := ctxSvc.JWKS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := cid.Verify(tok, aud, keys, testChecker(map[string]bool{}), func(string) bool { return false })
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return p
}

// The access token a refresh mints carries the session's rotation-chain id as `sid`, the same
// value across rotations, and that id names a live session of the account until it is revoked.
func TestHumanAccessTokenSidRoundTripsAndSurvivesRotation(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "sid-owner")
	other := insertAccount(t, st, "sid-other")

	raw, err := svc.IssueRefreshToken(ctx, acct, "ua", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	var chain string
	if err := st.Pool().QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE account_id = $1`, acct).Scan(&chain); err != nil {
		t.Fatal(err)
	}

	access1, raw2, err := svc.RefreshAccessToken(ctx, raw, "blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	p1 := verifyHuman(t, svc, access1, "blerg-runner")
	if p1.Sid != chain || p1.Sid == "" {
		t.Fatalf("sid = %q, want the session chain id %q", p1.Sid, chain)
	}
	access2, raw3, err := svc.RefreshAccessToken(ctx, raw2, "blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	if p2 := verifyHuman(t, svc, access2, "blerg-runner"); p2.Sid != chain {
		t.Fatalf("sid after a second rotation = %q, want the same chain %q", p2.Sid, chain)
	}

	// The OLD access token's sid is still a live session after the rotation: it names the chain.
	rowID, live, err := svc.HumanSessionLive(ctx, acct, p1.Sid)
	if err != nil || !live || rowID == "" {
		t.Fatalf("HumanSessionLive after rotation = %q %v %v, want live", rowID, live, err)
	}
	// Someone else's account is told exactly what an unknown session is told.
	if _, live, err := svc.HumanSessionLive(ctx, other, p1.Sid); err != nil || live {
		t.Fatalf("another account's view of the session = %v %v, want not live", live, err)
	}
	for _, bad := range []string{"", "not-a-uuid", "00000000-0000-4000-8000-00000000dead"} {
		if _, live, err := svc.HumanSessionLive(ctx, acct, bad); err != nil || live {
			t.Fatalf("session %q = %v %v, want not live", bad, live, err)
		}
	}

	// Logging out this device kills it; so does an admin disabling the account.
	if err := svc.RevokeHumanSession(ctx, raw3); err != nil {
		t.Fatal(err)
	}
	if _, live, _ := svc.HumanSessionLive(ctx, acct, chain); live {
		t.Fatal("a revoked session must not be live")
	}
}

func TestHumanSessionLiveRefusesExpiredAndDisabled(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "sid-expiry")
	if _, err := svc.IssueRefreshToken(ctx, acct, "ua", "ip"); err != nil {
		t.Fatal(err)
	}
	var chain string
	if err := st.Pool().QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE account_id = $1`, acct).Scan(&chain); err != nil {
		t.Fatal(err)
	}
	if _, live, _ := svc.HumanSessionLive(ctx, acct, chain); !live {
		t.Fatal("fresh session must be live")
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, live, _ := svc.HumanSessionLive(ctx, acct, chain); live {
		t.Fatal("a disabled account's session must not be live")
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = NULL WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE human_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, live, _ := svc.HumanSessionLive(ctx, acct, chain); live {
		t.Fatal("an expired session must not be live")
	}
}

// A token minted without a session (the pre-`sid` shape, or MintHumanAccessToken called directly)
// carries no sid: it verifies fine everywhere else, but names no session to core, so the
// internal endpoints have nothing to accept (the callers fail closed).
func TestMintHumanAccessTokenWithoutSessionHasNoSid(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "sid-none")
	tok, err := svc.MintHumanAccessToken(ctx, acct, "blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	if p := verifyHuman(t, svc, tok, "blerg-runner"); p.Sid != "" {
		t.Fatalf("sid = %q, want empty", p.Sid)
	}
}
