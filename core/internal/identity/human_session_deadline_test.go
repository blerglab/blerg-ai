package identity_test

import (
	"testing"
	"time"
)

func TestHumanSessionDeadlinePinsTheOriginalLogin(t *testing.T) {
	svc, st := newTestService(t)
	ctx := t.Context()
	owner := insertAccount(t, st, "deadline-owner")
	other := insertAccount(t, st, "deadline-other")
	raw, err := svc.IssueRefreshToken(ctx, owner, "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	var chain string
	var expected time.Time
	if err = st.Pool().QueryRow(ctx, `SELECT chain_id::text,expires_at FROM human_sessions WHERE account_id=$1`, owner).Scan(&chain, &expected); err != nil {
		t.Fatal(err)
	}
	id, deadline, live, err := svc.HumanSessionDeadline(ctx, owner, chain)
	if err != nil || !live || id == "" || !deadline.Equal(expected) {
		t.Fatal("missing login deadline", err)
	}
	_, fresh, err := svc.RefreshAccessToken(ctx, raw, "blerg-runner")
	if err != nil {
		t.Fatal(err)
	}
	_, deadline, live, err = svc.HumanSessionDeadline(ctx, owner, chain)
	if err != nil || !live || !deadline.Equal(expected) {
		t.Fatal("refresh extended the original login deadline", err)
	}
	if _, until, ok, lookupErr := svc.HumanSessionDeadline(ctx, other, chain); lookupErr != nil || ok || !until.IsZero() {
		t.Fatal("another owner learned login metadata")
	}
	if err = svc.RevokeHumanSession(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, until, ok, lookupErr := svc.HumanSessionDeadline(ctx, owner, chain); lookupErr != nil || ok || !until.IsZero() {
		t.Fatal("revoked login kept its lifetime authority")
	}
}
