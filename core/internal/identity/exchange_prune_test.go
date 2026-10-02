package identity

import (
	"context"
	"testing"
	"time"
)

func TestPruneExchangeTokens(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-prune", "member")

	mint := func() string {
		rec, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.RevokeExchangeToken(ctx, acct, rec.ID); err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	age := func(id string, d time.Duration) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, `UPDATE agent_tokens SET expires_at = now() - make_interval(secs => $2) WHERE id = $1`, id, d.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	revocations := func(kind, value string) int {
		t.Helper()
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM revocations WHERE kind = $1 AND value = $2`, kind, value).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rows := func(id string) int {
		t.Helper()
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM agent_tokens WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	expired := mint() // past exp plus the margin: prunable
	recent := mint()  // expired, but inside the margin: a verifier could still see it
	live := mint()    // unexpired and revoked: the revocation still matters
	age(expired, ExchangeRevocationMargin+time.Minute)
	age(recent, ExchangeRevocationMargin/2)

	// A cron token that expired long ago and was revoked is NOT an exchange token: untouched.
	cron, err := svc.CreateUnsignedAgentToken(ctx, acct, "cron", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeCronToken(ctx, acct, cron.ID); err != nil {
		t.Fatal(err)
	}
	age(cron.ID, 48*time.Hour)
	// A plain token likewise, and a lineage and a kid revocation.
	plain, _, err := svc.CreateAgentToken(ctx, acct, "p", "board", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAgentToken(ctx, acct, plain.ID); err != nil {
		t.Fatal(err)
	}
	age(plain.ID, 48*time.Hour)
	if err := svc.Revoke(ctx, "lineage", acct); err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, "kid", "old-key"); err != nil {
		t.Fatal(err)
	}
	// A revocation for a sub that has no agent_tokens row at all (a human session id) stays too.
	if err := svc.Revoke(ctx, "sub", "00000000-0000-0000-0000-00000000abcd"); err != nil {
		t.Fatal(err)
	}

	n, err := svc.PruneExchangeTokens(ctx, ExchangeRevocationMargin)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d exchange rows, want 1", n)
	}
	if rows(expired) != 0 || revocations("sub", expired) != 0 {
		t.Errorf("expired exchange token: rows=%d revocations=%d, want both gone", rows(expired), revocations("sub", expired))
	}
	if rows(recent) != 1 || revocations("sub", recent) != 1 {
		t.Errorf("exchange token inside the margin was pruned (rows=%d revocations=%d)", rows(recent), revocations("sub", recent))
	}
	if rows(live) != 1 || revocations("sub", live) != 1 {
		t.Errorf("live revoked exchange token was pruned (rows=%d revocations=%d)", rows(live), revocations("sub", live))
	}
	if rows(cron.ID) != 1 || revocations("sub", cron.ID) != 1 {
		t.Errorf("cron token touched (rows=%d revocations=%d)", rows(cron.ID), revocations("sub", cron.ID))
	}
	if rows(plain.ID) != 1 || revocations("sub", plain.ID) != 1 {
		t.Errorf("ordinary token touched (rows=%d revocations=%d)", rows(plain.ID), revocations("sub", plain.ID))
	}
	if revocations("lineage", acct) != 1 || revocations("kid", "old-key") != 1 || revocations("sub", "00000000-0000-0000-0000-00000000abcd") != 1 {
		t.Error("a lineage, kid or foreign sub revocation was pruned")
	}

	// Idempotent.
	if n, err := svc.PruneExchangeTokens(ctx, ExchangeRevocationMargin); err != nil || n != 0 {
		t.Errorf("second prune = %d, %v; want 0, nil", n, err)
	}
}

func TestCreateExchangeTokenHasNoColumnWriteAndHonoursProofCaps(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	acct := insertAccount(t, st, "exch-proofcaps", "member")

	caps := func(proof []string) []string {
		t.Helper()
		rec, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", proof)
		if err != nil {
			t.Fatalf("proof %v: %v", proof, err)
		}
		return rec.Caps
	}
	has := func(c []string, w string) bool {
		for _, x := range c {
			if x == w {
				return true
			}
		}
		return false
	}
	// Human session proof (nil): the role's caps intersected with the target, never column.write.
	if c := caps(nil); len(c) != 2 || !has(c, "card.read") || !has(c, "card.write") {
		t.Errorf("human proof caps = %v, want card.read, card.write", c)
	}
	// A read-only token as proof yields read only.
	if c := caps([]string{"card.read"}); len(c) != 1 || !has(c, "card.read") {
		t.Errorf("read-only proof caps = %v, want [card.read]", c)
	}
	// A board-preset token (card.*, column.write) cannot give column.write either.
	if c := caps([]string{"card.read", "card.write", "column.write"}); len(c) != 2 || has(c, "column.write") {
		t.Errorf("board-preset proof caps = %v", c)
	}
	// A run-sessions token (session.start): delegating session start is what the exchange is for.
	if c := caps([]string{"session.start"}); len(c) != 2 {
		t.Errorf("run-sessions proof caps = %v, want card.read, card.write", c)
	}
	// A token with nothing relevant gets nothing: refused.
	if _, _, err := svc.CreateExchangeToken(ctx, acct, "board", "b", []string{"membership.write"}); err == nil {
		t.Error("a proof token with no relevant capability was accepted")
	}
}
