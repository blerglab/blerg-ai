package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

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

// hashOf is the test's copy of the token hash the rows are keyed by.
func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c testChecker) Revoked(kid, lineage, sub, sid string) bool {
	return c["kid:"+kid] || c["lineage:"+lineage] || c["sub:"+sub] || c["sid:"+sid]
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

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
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

	// The browser uses the successor (so it is known to have arrived), then the old token comes
	// back: two parties have held it. That is reuse, whatever its age.
	_, raw3, err := svc.RefreshAccessToken(ctx, raw2, "blerg-core")
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("reusing a rotated-out token whose successor was used: %v, want ErrSessionReplayed", err)
	}
	var liveCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 0 {
		t.Fatalf("live session count after reuse = %d, want 0 (the chain is revoked)", liveCount)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw3, "blerg-core"); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Fatalf("the whole chain must be revoked after reuse: %v, want ErrSessionRevoked", err)
	}
	var reason string
	if err := st.Pool().QueryRow(ctx, `SELECT revoke_reason FROM human_sessions WHERE token_hash = $1`, hashOf(raw3)).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "reuse" {
		t.Fatalf("revoke_reason of the chain's live row = %q, want reuse", reason)
	}
}

// TestLostRotationIsRedone covers the benign case: the browser presents the token it still holds
// because the response that carried its successor never arrived (a torn-down frame, a dropped
// connection, two tabs racing). The successor was never presented, so the rotation is redone —
// at any age — and nothing is revoked beyond the successor the browser never saw.
func TestLostRotationIsRedone(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "lost-rotation")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	_, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// Long after any race window: the device was asleep.
	if _, err := st.Pool().Exec(ctx,
		`UPDATE human_sessions SET revoked_at = now() - interval '3 hours' WHERE account_id = $1 AND revoked_at IS NOT NULL`, acct); err != nil {
		t.Fatal(err)
	}

	access, raw3, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("presenting the old token when its successor was never used must redo the rotation: %v", err)
	}
	if access == "" || raw3 == "" || raw3 == raw1 || raw3 == raw2 {
		t.Fatalf("expected a fresh access token and a new refresh token, got access=%v raw3=%v", access != "", raw3 != "")
	}
	// The never-used successor is superseded; the new one works; the chain is alive.
	var reason string
	if err := st.Pool().QueryRow(ctx, `SELECT revoke_reason FROM human_sessions WHERE token_hash = $1`, hashOf(raw2)).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "superseded" {
		t.Fatalf("revoke_reason of the lost successor = %q, want superseded", reason)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw3, "blerg-core"); err != nil {
		t.Fatalf("the redone rotation's token must work: %v", err)
	}
	var liveCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("live rows after a redone rotation = %d, want 1", liveCount)
	}
	// Now the chain has moved on (raw3 was used). The superseded successor turning up after that
	// means a second holder: reuse.
	if _, _, err := svc.RefreshAccessToken(ctx, raw2, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("the superseded successor presented after the chain moved on: %v, want ErrSessionReplayed", err)
	}
}

// TestRacingTabsHealWithoutRevocation: two tabs refresh with the same token at once. Both get a
// rotation (the second is a redo of the first), and whichever cookie the browser keeps, the next
// refresh works — no chain is revoked, because at no point was a used token presented twice.
func TestRacingTabsHealWithoutRevocation(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "race")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	_, tabA, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("tab A: %v", err)
	}
	_, tabB, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("tab B (the race): %v", err)
	}
	// The browser ended up keeping A's cookie (B's response arrived first, A's last).
	if _, _, err := svc.RefreshAccessToken(ctx, tabA, "blerg-core"); err != nil {
		t.Fatalf("refreshing with the cookie the browser kept: %v", err)
	}
	var liveCount int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM human_sessions WHERE account_id = $1 AND revoked_at IS NULL`, acct).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if liveCount != 1 {
		t.Fatalf("live rows after a healed race = %d, want 1", liveCount)
	}
	_ = tabB
}

// TestReplayAgainstLoggedOutSuccessorIsReuse: a successor that was logged out is no longer live,
// so the old token coming back is not a lost rotation but a second holder — reuse.
func TestReplayAgainstLoggedOutSuccessorIsReuse(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "succ-dead")
	raw1, _ := svc.IssueRefreshToken(ctx, acct, "ua", "ip")
	_, raw2, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := svc.RevokeHumanSession(ctx, raw2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, raw1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("replay against a logged-out successor: %v, want ErrSessionReplayed", err)
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

// TestReuseRevokesOnlyItsChain: a detected reuse ends the browser session it happened in — its
// rows and, through a "sid" entry in the shared revocations table, the access tokens it minted —
// and nothing else: the person's other devices stay signed in and the agent tokens their tools
// hold keep working. Neither the account's "sub" nor its "lineage" is revoked.
func TestReuseRevokesOnlyItsChain(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()
	acct := insertAccount(t, st, "reuse-victim")
	toolToken, _, err := svc.CreateAgentToken(ctx, acct, "tool", "run-sessions", 24*time.Hour)
	if err != nil {
		t.Fatalf("agent token: %v", err)
	}
	// Two devices: a phone (chain A) and a laptop (chain B).
	phone1, _ := svc.IssueRefreshToken(ctx, acct, "phone", "ip-a")
	laptop1, _ := svc.IssueRefreshToken(ctx, acct, "laptop", "ip-b")
	phoneAccess1, phone2, err := svc.RefreshAccessToken(ctx, phone1, "blerg-core")
	if err != nil {
		t.Fatalf("phone refresh: %v", err)
	}
	var phoneChain string
	if err := st.Pool().QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE token_hash = $1`, hashOf(phone2)).Scan(&phoneChain); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RefreshAccessToken(ctx, phone2, "blerg-core"); err != nil {
		t.Fatalf("phone uses its successor: %v", err)
	}

	// The phone's old token comes back from somewhere: reuse on chain A.
	if _, _, err := svc.RefreshAccessToken(ctx, phone1, "blerg-core"); !errors.Is(err, identity.ErrSessionReplayed) {
		t.Fatalf("reuse: %v, want ErrSessionReplayed", err)
	}

	snapshot, err := svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sidRevoked, subRevoked, lineageRevoked bool
	for _, r := range snapshot {
		if r.Kind == "sid" && r.Value == phoneChain {
			sidRevoked = true
		}
		if r.Kind == "sub" && r.Value == acct {
			subRevoked = true
		}
		if r.Kind == "lineage" && r.Value == acct {
			lineageRevoked = true
		}
	}
	if !sidRevoked {
		t.Fatal("a detected reuse must revoke the chain's sid so its minted access tokens stop verifying")
	}
	if subRevoked || lineageRevoked {
		t.Fatalf("a detected reuse must not revoke the account (sub=%v lineage=%v): other devices and agent tokens stay", subRevoked, lineageRevoked)
	}
	// The phone's already-minted access token is dead: its sid is revoked and the chain is gone.
	if _, live, err := svc.HumanSessionLive(ctx, acct, phoneChain); err != nil || live {
		t.Fatalf("the phone's chain after reuse: live=%v err=%v, want not live", live, err)
	}
	_ = phoneAccess1
	// The laptop is untouched, and so is the tool.
	if _, _, err := svc.RefreshAccessToken(ctx, laptop1, "blerg-core"); err != nil {
		t.Fatalf("the laptop's session must survive a reuse on the phone: %v", err)
	}
	if live, err := svc.AgentTokenLive(ctx, acct, toolToken.ID); err != nil || !live {
		t.Fatalf("the account's agent token after a reuse: live=%v err=%v, want live", live, err)
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
