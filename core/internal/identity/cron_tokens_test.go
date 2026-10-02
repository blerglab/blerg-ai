package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

func TestCreateUnsignedAgentTokenIsUnpresentable(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "cron-a", "member")

	rec, err := svc.CreateUnsignedAgentToken(ctx, acct, "nightly", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Aud != "blerg-runner" || len(rec.Caps) != 1 || rec.Caps[0] != "session.start" {
		t.Fatalf("aud/caps = %q %v", rec.Aud, rec.Caps)
	}
	if d := time.Until(rec.ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("expiry %v not ~30d", d)
	}

	var kind, hash string
	if err := st.Pool().QueryRow(ctx, `SELECT kind, token_hash FROM agent_tokens WHERE id = $1`, rec.ID).Scan(&kind, &hash); err != nil {
		t.Fatal(err)
	}
	if kind != AgentTokenKindCron {
		t.Fatalf("kind = %q", kind)
	}

	// Nothing derivable from what exists can be a credential: the id, the hash and the empty
	// string all fail verification. (There is no signed token to try; that is the point.)
	keys, err := svc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, cand := range []string{rec.ID, hash, "", rec.ID + "." + hash + "." + hash} {
		if _, err := cid.Verify(cand, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); err == nil {
			t.Fatalf("Verify(%q) succeeded", cand)
		}
	}
	// No signed token was minted for it: hashes of two cron rows differ and are not sha256(id).
	if hash == agentTokenHash(rec.ID) {
		t.Fatal("token_hash is derivable from the id")
	}
	rec2, err := svc.CreateUnsignedAgentToken(ctx, acct, "other", 0)
	if err != nil {
		t.Fatal(err)
	}
	var hash2 string
	if err := st.Pool().QueryRow(ctx, `SELECT token_hash FROM agent_tokens WHERE id = $1`, rec2.ID).Scan(&hash2); err != nil {
		t.Fatal(err)
	}
	if hash2 == hash || strings.Count(hash, "") < 60 {
		t.Fatalf("hashes not random/distinct: %q %q", hash, hash2)
	}
}

func TestCreateUnsignedAgentTokenCapCountsBothKinds(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "cron-cap", "member")
	for i := 0; i < 25; i++ {
		if _, _, err := svc.CreateAgentToken(ctx, acct, "t", "run-sessions", time.Hour); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	for i := 0; i < 25; i++ {
		if _, err := svc.CreateUnsignedAgentToken(ctx, acct, "c", time.Hour); err != nil {
			t.Fatalf("cron %d: %v", i, err)
		}
	}
	if _, err := svc.CreateUnsignedAgentToken(ctx, acct, "c", time.Hour); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("51st cron err = %v, want ErrTooManyTokens", err)
	}
	if _, _, err := svc.CreateAgentToken(ctx, acct, "t", "run-sessions", time.Hour); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("51st token err = %v, want ErrTooManyTokens (cron rows count)", err)
	}
}

func TestCreateUnsignedAgentTokenClampsExpiry(t *testing.T) {
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "cron-exp", "member")
	rec, err := svc.CreateUnsignedAgentToken(context.Background(), acct, "long", 1000*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(rec.ExpiresAt) > 366*24*time.Hour {
		t.Fatalf("expiry %v exceeds 365 days", rec.ExpiresAt)
	}
}

func TestCronTokensHiddenFromList(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "cron-list", "member")
	if _, err := svc.CreateUnsignedAgentToken(ctx, acct, "c", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAgentToken(ctx, acct, "real", "run-sessions", time.Hour); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListAgentTokens(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "real" {
		t.Fatalf("list = %+v", list)
	}
}

func TestCronTokenStatusAndRevocation(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	a := insertAccount(t, st, "cron-st-a", "member")
	b := insertAccount(t, st, "cron-st-b", "member")
	rec, err := svc.CreateUnsignedAgentToken(ctx, a, "c", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if live, owned, err := svc.AgentTokenStatus(ctx, a, rec.ID); err != nil || !live || !owned {
		t.Fatalf("fresh: live=%v owned=%v err=%v", live, owned, err)
	}
	if live, owned, _ := svc.AgentTokenStatus(ctx, b, rec.ID); live || owned {
		t.Fatalf("other account: live=%v owned=%v", live, owned)
	}
	if live, ok, _ := svc.AgentTokenStatus(ctx, a, "not-a-uuid"); live || ok {
		t.Fatal("malformed id reported owned")
	}

	// Another account cannot revoke it, by either path.
	if err := svc.RevokeCronToken(ctx, b, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account RevokeCronToken = %v", err)
	}
	if err := svc.RevokeAgentToken(ctx, b, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account RevokeAgentToken = %v", err)
	}
	if live, _, _ := svc.AgentTokenStatus(ctx, a, rec.ID); !live {
		t.Fatal("token died from a refused revoke")
	}

	// Revoke is idempotent and leaves the token not live.
	for i := 0; i < 2; i++ {
		if err := svc.RevokeCronToken(ctx, a, rec.ID); err != nil {
			t.Fatalf("revoke #%d: %v", i, err)
		}
	}
	if live, owned, _ := svc.AgentTokenStatus(ctx, a, rec.ID); live || !owned {
		t.Fatalf("after revoke: live=%v owned=%v", live, owned)
	}
}

func TestRevokeCronTokenRefusesOrdinaryTokens(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	a := insertAccount(t, st, "cron-ord", "member")
	rec, _, err := svc.CreateAgentToken(ctx, a, "real", "run-sessions", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeCronToken(ctx, a, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeCronToken on an ordinary token = %v, want ErrNotFound", err)
	}
	if live, _, _ := svc.AgentTokenStatus(ctx, a, rec.ID); !live {
		t.Fatal("ordinary token was revoked by the cron-only path")
	}
}

func TestCronTokenDeadAfterLogOutEverywhereAndExpiry(t *testing.T) {
	svc, st := agentTokenSvc(t)
	ctx := context.Background()
	a := insertAccount(t, st, "cron-lo", "member")
	rec, err := svc.CreateUnsignedAgentToken(ctx, a, "c", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAccountEverywhere(ctx, a); err != nil {
		t.Fatal(err)
	}
	if live, _, _ := svc.AgentTokenStatus(ctx, a, rec.ID); live {
		t.Fatal("cron token live after log-out-everywhere")
	}

	b := insertAccount(t, st, "cron-exp2", "member")
	rec2, err := svc.CreateUnsignedAgentToken(ctx, b, "c", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE agent_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, rec2.ID); err != nil {
		t.Fatal(err)
	}
	if live, owned, _ := svc.AgentTokenStatus(ctx, b, rec2.ID); live || !owned {
		t.Fatalf("expired: live=%v owned=%v", live, owned)
	}

	// A disabled account's cron identity is not live either.
	c := insertAccount(t, st, "cron-dis", "member")
	rec3, err := svc.CreateUnsignedAgentToken(ctx, c, "c", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	if live, _, _ := svc.AgentTokenStatus(ctx, c, rec3.ID); live {
		t.Fatal("cron token live for a disabled account")
	}
	if _, err := svc.CreateUnsignedAgentToken(ctx, c, "again", time.Hour); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled mint = %v", err)
	}
}
