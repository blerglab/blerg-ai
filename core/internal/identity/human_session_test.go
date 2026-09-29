package identity_test

import (
	"context"
	"errors"
	"testing"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// insertAccount is a small helper to add next to newTestService (the existing tests inline
// this INSERT; the new tests need it several times).
func insertAccount(t *testing.T, st db.Store, subject string) string {
	t.Helper()
	var id string
	if err := st.Pool().QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local',$1,'member') RETURNING id::text`,
		subject).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// newTestService builds a Service backed by a migrated test database and a signing key
// that is persisted via LoadOrGenerateSigningKey (not a bare generated keypair) so that
// svc.JWKS(ctx) — which reads only from the signing_keys table — actually serves the key
// used to sign tokens minted by this service.
func newTestService(t *testing.T) (*identity.Service, db.Store) {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := db.NewPgStore(pool)
	kp, err := identity.LoadOrGenerateSigningKey(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	return identity.NewService(st, kp), st
}

func TestHumanAccessTokenRoundTrips(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var accountID string
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local','u1','member') RETURNING id::text`,
	).Scan(&accountID); err != nil {
		t.Fatal(err)
	}

	tok, err := svc.MintHumanAccessToken(ctx, accountID, "blerg-board")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := svc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := map[string]bool{}
	for _, r := range snap {
		rev[r.Kind+":"+r.Value] = true
	}
	checker := testChecker(rev)
	p, err := cid.Verify(tok, "blerg-board", keys, checker, func(string) bool { return false })
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.Kind != "human" || p.Sub != accountID {
		t.Errorf("claims = %+v", p.Claims)
	}
	if len(p.Caps) == 0 {
		t.Error("expected member capabilities on the token")
	}
}

type testChecker map[string]bool

func (c testChecker) Revoked(kid, lineage, sub string) bool {
	return c["kid:"+kid] || c["lineage:"+lineage] || c["sub:"+sub]
}
func (testChecker) StaleBeyondCeiling() bool { return false }

func TestRefreshTokenIssueAndUse(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var accountID string
	st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local','u2','member') RETURNING id::text`,
	).Scan(&accountID)

	raw, err := svc.IssueRefreshToken(ctx, accountID, "test-agent", "127.0.0.1")
	if err != nil || raw == "" {
		t.Fatalf("IssueRefreshToken: %v", err)
	}
	access, _, err := svc.RefreshAccessToken(ctx, raw, "blerg-board")
	if err != nil || access == "" {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
}

func TestRevokedRefreshTokenFails(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var accountID string
	st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local','u3','member') RETURNING id::text`,
	).Scan(&accountID)

	raw, _ := svc.IssueRefreshToken(ctx, accountID, "", "")
	if err := svc.RevokeHumanSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw, "blerg-board"); err == nil {
		t.Error("expected RefreshAccessToken to fail after RevokeHumanSession")
	}
}

func TestRevokeAccountEverywhereKillsAllSessions(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var accountID string
	st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local','u4','member') RETURNING id::text`,
	).Scan(&accountID)

	rawA, _ := svc.IssueRefreshToken(ctx, accountID, "", "")
	rawB, _ := svc.IssueRefreshToken(ctx, accountID, "", "")
	if err := svc.RevokeAccountEverywhere(ctx, accountID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, rawA, "blerg-board"); err == nil {
		t.Error("session A should be revoked")
	}
	if _, _, err := svc.RefreshAccessToken(ctx, rawB, "blerg-board"); err == nil {
		t.Error("session B should be revoked")
	}
}

// TestRevokeAccountEverywhereRevokesExistingAccessToken proves RevokeAccountEverywhere
// also defeats an access token minted BEFORE the revocation (not merely future refreshes),
// via the shared revocations table's "sub" entry that cid.Verify's fast-path check consults.
func TestRevokeAccountEverywhereRevokesExistingAccessToken(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	var accountID string
	st.Pool().QueryRow(ctx,
		`INSERT INTO accounts (provider, provider_subject, role) VALUES ('local','u5','member') RETURNING id::text`,
	).Scan(&accountID)

	tok, err := svc.MintHumanAccessToken(ctx, accountID, "blerg-board")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAccountEverywhere(ctx, accountID); err != nil {
		t.Fatal(err)
	}

	keys, _ := svc.JWKS(ctx)
	snap, _ := svc.RevocationSnapshot(ctx)
	rev := map[string]bool{}
	for _, r := range snap {
		rev[r.Kind+":"+r.Value] = true
	}
	checker := testChecker(rev)
	if _, err := cid.Verify(tok, "blerg-board", keys, checker, func(string) bool { return false }); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("Verify after RevokeAccountEverywhere = %v, want ErrRevoked", err)
	}
}

func TestRefreshRotatesAndDetectsReplay(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "rot")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "original-ua", "original-ip")
	_, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil || raw2 == "" || raw2 == raw1 {
		t.Fatalf("refresh must rotate: %v", err)
	}

	// The successor row must carry the original session's user_agent/ip forward, not drop them.
	var succUA, succIP string
	if err := st.Pool().QueryRow(ctx, `SELECT user_agent, ip FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).
		Scan(&succUA, &succIP); err != nil {
		t.Fatal(err)
	}
	if succUA != "original-ua" || succIP != "original-ip" {
		t.Fatalf("successor row user_agent/ip = %q/%q, want %q/%q (rotation must carry attribution forward)", succUA, succIP, "original-ua", "original-ip")
	}

	// Age the rotated-out row's revoked_at past the grace window so this replay is the
	// "outside the window" case (see TestReplayWithinGraceWindow... below for the
	// within-window cases).
	if _, err := st.Pool().Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now() - interval '2 minutes' WHERE account_id = $1 AND revoked_at IS NOT NULL`, acct); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("replaying a rotated-out token outside the grace window: %v, want ErrSessionReplayed", err)
	}
	var liveCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("live session count after replay outside the grace window = %d, want 0 (whole chain revoked)", liveCount)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw2, "blerg-core"); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("the whole chain must be revoked after replay: %v, want ErrSessionRevoked", err)
	}
}

// TestReplayWithinGraceWindowMintsAgainstLiveSuccessor covers the benign-double-refresh case:
// replaying a just-rotated-out token within replayGraceWindow, while its successor session is
// still live, must mint a fresh access token (not fail) and must NOT rotate again (empty
// newRefresh, no third row, the successor session untouched).
func TestReplayWithinGraceWindowMintsAgainstLiveSuccessor(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "grace-live")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	access1, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	_ = access1

	var countBefore int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1`, acct).Scan(&countBefore); err != nil {
		t.Fatal(err)
	}

	access2, newRefresh, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("replay within grace window against a live successor must succeed: %v", err)
	}
	if access2 == "" {
		t.Fatal("expected a minted access token")
	}
	if newRefresh != "" {
		t.Fatalf("newRefresh = %q, want empty (no rotation should happen inside the grace window)", newRefresh)
	}

	var countAfter int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1`, acct).Scan(&countAfter); err != nil {
		t.Fatal(err)
	}
	if countAfter != countBefore {
		t.Fatalf("row count after grace-window replay = %d, want unchanged %d (no new rows)", countAfter, countBefore)
	}

	// The successor session (raw2) must still be live and usable.
	if _, _, err := svc.RefreshAccessToken(ctx, raw2, "blerg-core"); err != nil {
		t.Fatalf("successor session must still be live after a grace-window replay: %v", err)
	}
}

// TestReplayWithinGraceWindowButSuccessorRevokedStillFails covers the third case: even inside
// the grace window, if the successor session is no longer live (e.g. already logged out), the
// replay must still be treated as theft — the grace window only excuses replay against a
// genuinely still-usable successor.
func TestReplayWithinGraceWindowButSuccessorRevokedStillFails(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "grace-dead")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	_, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := svc.RevokeHumanSession(ctx, raw2); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("replay within grace window against a dead successor: %v, want ErrSessionReplayed", err)
	}
}

func TestRefreshRefusesExpiredSession(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "exp")
	raw, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	if _, err := st.Pool().Exec(ctx, `UPDATE human_sessions SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw, "blerg-core"); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("expired session refresh error = %v, want ErrSessionRevoked", err)
	}
}

func TestMintRefusesDisabledAccount(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "disabled")
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MintHumanAccessToken(ctx, acct, "blerg-core"); !errors.Is(err, identity.ErrAccountDisabled) {
		t.Fatalf("MintHumanAccessToken on a disabled account = %v, want ErrAccountDisabled", err)
	}
}

// TestReplayRevokesSubAndLoginUnrevokes covers the controller ruling carried from Task 8: now
// that "sub" revocation is reversible, a detected replay (theft signal) must call
// RevokeAccountEverywhere — sessions AND the account's "sub" in the shared revocations table —
// not just kill the rotation chain, so already-minted access tokens stop verifying immediately
// too. And because a "sub" revocation is no longer permanent, a subsequent successful login
// must clear it (this is what startHumanSession's Unrevoke call does in the api package; here
// we exercise the identity-layer primitives it's built on directly).
func TestReplayRevokesSubAndLoginUnrevokes(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "theft-victim")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	_, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	_ = raw2

	// Age the rotated-out row past the grace window so the replay below is the "outside the
	// window" theft-detection case.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now() - interval '2 minutes' WHERE account_id = $1 AND revoked_at IS NOT NULL`, acct); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("replay outside grace window: %v, want ErrSessionReplayed", err)
	}

	// The replay must have revoked the account's "sub", not just the session chain.
	snapshot, err := svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var subRevoked bool
	for _, r := range snapshot {
		if r.Kind == "sub" && r.Value == acct {
			subRevoked = true
		}
	}
	if !subRevoked {
		t.Fatal("a detected replay must revoke the account's sub (RevokeAccountEverywhere), not just the session chain")
	}

	// A subsequent successful login (here: the Unrevoke call startHumanSession makes on
	// success) clears the stale sub revocation.
	if err := svc.Unrevoke(ctx, "sub", acct); err != nil {
		t.Fatal(err)
	}
	snapshot, err = svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot {
		if r.Kind == "sub" && r.Value == acct {
			t.Fatal("sub revocation must be cleared after a subsequent successful login")
		}
	}
}

// TestMustChangePasswordTokenCarriesOnlyPasswordChange is R7/I-7's server-side enforcement: a
// must-change-password account's minted access token carries ONLY the "password.change"
// capability, overriding its role's normal caps entirely — even for an admin.
func TestMustChangePasswordTokenCarriesOnlyPasswordChange(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "must-change")
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET must_change_password = true, role = 'admin' WHERE id = $1`, acct); err != nil {
		t.Fatal(err)
	}
	tok, err := svc.MintHumanAccessToken(ctx, acct, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := svc.JWKS(ctx)
	p, err := cid.Verify(tok, "blerg-core", keys, testChecker{}, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Caps) != 1 || p.Caps[0] != "password.change" {
		t.Fatalf("caps = %v, want [password.change] only — even for an admin", p.Caps)
	}
}

// TestNormalAccountCapsUnchangedByMustChangePasswordGate proves the new gate in
// MintHumanAccessToken only fires when MustChangePassword is actually set — a normal account's
// caps are exactly its role's PlatformRoleCaps, unaffected by this change.
func TestNormalAccountCapsUnchangedByMustChangePasswordGate(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "normal-caps")
	tok, err := svc.MintHumanAccessToken(ctx, acct, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := svc.JWKS(ctx)
	p, err := cid.Verify(tok, "blerg-core", keys, testChecker{}, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	want := identity.PlatformRoleCaps["member"]
	if len(p.Caps) != len(want) {
		t.Fatalf("caps = %v, want member's normal caps %v", p.Caps, want)
	}
}
