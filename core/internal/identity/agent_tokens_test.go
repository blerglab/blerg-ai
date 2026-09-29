package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/signing"
)

// agentTokenSvc gives a Service over a fresh isolated schema (openStore, service_test.go) with
// a real signing key, so minted tokens actually verify against JWKS.
func agentTokenSvc(t *testing.T) (*Service, db.Store) {
	t.Helper()
	st := openStore(t)
	kp, err := signing.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(context.Background(),
		`INSERT INTO signing_keys(kid, public_key, private_key, active) VALUES ($1,$2,$3,true)`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
	return NewService(st, kp), st
}

// insertAccount inserts an account with the given platform role and returns its id.
func insertAccount(t *testing.T, st db.Store, subject, role string) string {
	t.Helper()
	var id string
	if err := st.Pool().QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role)
		 VALUES ('local', $1, $1 || '@example.com', $2) RETURNING id::text`,
		subject, role).Scan(&id); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	return id
}

// liveChecker is the revocation view core's own router builds per request: the shared,
// timestamp-aware cid.RevocationSet over one live snapshot (Service.revocationChecker, which
// CreateAgentToken itself uses). Tests here use it rather than an unconditional stub precisely
// because "does an account-wide revocation reach this account's agent tokens" depends on the
// timestamp-scoped RevokedFor rule.
func liveChecker(t *testing.T, svc *Service) cid.RevocationChecker {
	t.Helper()
	checker, err := svc.revocationChecker(context.Background())
	if err != nil {
		t.Fatalf("revocation checker: %v", err)
	}
	return checker
}

// TestAgentTokenCreateListRevokeRoundTrip is the whole lifecycle: the minted token verifies
// with the preset's audience and the right claims, shows up in the owner's list (metadata
// only), and stops verifying the moment it is revoked.
func TestAgentTokenCreateListRevokeRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "agenttokens-owner", "member")

	rec, raw, err := svc.CreateAgentToken(ctx, account, "laptop cli", "run-sessions", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}
	if rec.ID == "" || rec.AccountID != account {
		t.Fatalf("record = %+v, want an id and account %s", rec, account)
	}
	if rec.Name != "laptop cli" || rec.Aud != "blerg-runner" {
		t.Errorf("name/aud = %q/%q, want %q/%q", rec.Name, rec.Aud, "laptop cli", "blerg-runner")
	}
	if len(rec.Caps) != 1 || rec.Caps[0] != "session.start" {
		t.Errorf("caps = %v, want [session.start]", rec.Caps)
	}
	if rec.CreatedAt.IsZero() || rec.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Errorf("created_at/expires_at = %v/%v, want now and ~+30d", rec.CreatedAt, rec.ExpiresAt)
	}
	if rec.LastUsedAt != nil || rec.RevokedAt != nil {
		t.Errorf("fresh token already used/revoked: %+v", rec)
	}

	keys, err := svc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps)
	if err != nil {
		t.Fatalf("verify minted agent token: %v", err)
	}
	if p.Kind != string(cid.Agent) {
		t.Errorf("kind = %q, want agent", p.Kind)
	}
	if p.Sub != rec.ID {
		t.Errorf("sub = %q, want the token id %q", p.Sub, rec.ID)
	}
	if p.OnBehalfOf != account || p.Lineage != account {
		t.Errorf("on_behalf_of/lineage = %q/%q, want %q", p.OnBehalfOf, p.Lineage, account)
	}
	if !p.Has("session.start") {
		t.Errorf("caps = %v, want session.start", p.Caps)
	}
	// The exp must be the requested one, NOT the 15-minute machine-token default.
	if p.ExpiresAt != rec.ExpiresAt.Unix() {
		t.Errorf("exp = %d, want %d (the stored expires_at)", p.ExpiresAt, rec.ExpiresAt.Unix())
	}

	list, err := svc.ListAgentTokens(ctx, account)
	if err != nil {
		t.Fatalf("ListAgentTokens: %v", err)
	}
	if len(list) != 1 || list[0].ID != rec.ID || list[0].Name != "laptop cli" {
		t.Fatalf("list = %+v, want the one token", list)
	}

	if err := svc.RevokeAgentToken(ctx, account, rec.ID); err != nil {
		t.Fatalf("RevokeAgentToken: %v", err)
	}
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("post-revoke verify err = %v, want ErrRevoked", err)
	}
	list, err = svc.ListAgentTokens(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatalf("revoked token's list entry = %+v, want revoked_at set", list)
	}

	// Idempotent: a second revoke succeeds and leaves the FIRST revoked_at in place.
	first := *list[0].RevokedAt
	if err := svc.RevokeAgentToken(ctx, account, rec.ID); err != nil {
		t.Fatalf("second RevokeAgentToken: %v", err)
	}
	list, _ = svc.ListAgentTokens(ctx, account)
	if !list[0].RevokedAt.Equal(first) {
		t.Errorf("revoked_at moved on re-revoke: %v then %v", first, *list[0].RevokedAt)
	}
}

// TestAgentTokenCapsIntersectRole: a token never carries more than its owner's platform role
// does. Every preset in today's closed list is within memberCaps, so member and admin get the
// same caps; a role with no capability entry at all gets an empty (never a preset-wide) list.
func TestAgentTokenCapsIntersectRole(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)

	member := insertAccount(t, st, "caps-member", "member")
	admin := insertAccount(t, st, "caps-admin", "admin")
	stranger := insertAccount(t, st, "caps-stranger", "no-such-role")

	memberRec, _, err := svc.CreateAgentToken(ctx, member, "m", "board", 0)
	if err != nil {
		t.Fatalf("member board token: %v", err)
	}
	adminRec, _, err := svc.CreateAgentToken(ctx, admin, "a", "board", 0)
	if err != nil {
		t.Fatalf("admin board token: %v", err)
	}
	want := []string{"card.read", "card.write", "column.write"}
	for _, got := range [][]string{memberRec.Caps, adminRec.Caps} {
		if len(got) != len(want) {
			t.Fatalf("caps = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("caps = %v, want %v", got, want)
			}
		}
	}

	strangerRec, _, err := svc.CreateAgentToken(ctx, stranger, "s", "board", 0)
	if err != nil {
		t.Fatalf("unknown-role board token: %v", err)
	}
	if len(strangerRec.Caps) != 0 {
		t.Errorf("caps for a role with no PlatformRoleCaps entry = %v, want none", strangerRec.Caps)
	}

	// Default expiry when the caller passes no duration.
	if d := time.Until(memberRec.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Errorf("default expiry = %v, want ~90 days", d)
	}
}

// TestCreateAgentTokenRefusesUnknownPresetAndDisabledAccount: the preset list is closed (no
// free-form audience or capability), and a disabled account cannot mint at all — the same gate
// MintHumanAccessToken applies, so disabling an account is not bypassable by minting first.
func TestCreateAgentTokenRefusesUnknownPresetAndDisabledAccount(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "refuse-owner", "member")

	if _, _, err := svc.CreateAgentToken(ctx, account, "n", "root", 0); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown preset err = %v, want ErrUnknownPreset", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, account); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAgentToken(ctx, account, "n", "platform", 0); !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("disabled-account err = %v, want ErrAccountDisabled", err)
	}
}

// TestRevokeAgentTokenIsOwnerScoped: one account can never revoke another's token, and an
// unknown id is indistinguishable from someone else's — both ErrNotFound.
func TestRevokeAgentTokenIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	owner := insertAccount(t, st, "scoped-owner", "member")
	other := insertAccount(t, st, "scoped-other", "member")

	rec, _, err := svc.CreateAgentToken(ctx, owner, "mine", "platform", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAgentToken(ctx, other, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account revoke err = %v, want ErrNotFound", err)
	}
	if err := svc.RevokeAgentToken(ctx, owner, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown-id revoke err = %v, want ErrNotFound", err)
	}
	// A malformed id must be ErrNotFound too, never a database error surfacing as a 500.
	if err := svc.RevokeAgentToken(ctx, owner, "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed-id revoke err = %v, want ErrNotFound", err)
	}
	// The other account's token survived.
	list, _ := svc.ListAgentTokens(ctx, owner)
	if len(list) != 1 || list[0].RevokedAt != nil {
		t.Fatalf("owner's token = %+v, want untouched", list)
	}
	if other, err := svc.ListAgentTokens(ctx, other); err != nil || len(other) != 0 {
		t.Fatalf("other account's list = %+v (err %v), want empty", other, err)
	}
}

// TestAgentTokenLive covers the liveness gate the internal credential endpoints use in place of
// a live human session: present + unrevoked + unexpired + owned by the account, and it stamps
// last_used_at on the way through.
func TestAgentTokenLive(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	owner := insertAccount(t, st, "live-owner", "member")
	other := insertAccount(t, st, "live-other", "member")

	rec, _, err := svc.CreateAgentToken(ctx, owner, "live", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	live, err := svc.AgentTokenLive(ctx, owner, rec.ID)
	if err != nil || !live {
		t.Fatalf("AgentTokenLive = %v, %v; want true, nil", live, err)
	}
	list, _ := svc.ListAgentTokens(ctx, owner)
	if list[0].LastUsedAt == nil {
		t.Error("AgentTokenLive did not stamp last_used_at")
	}

	if live, err := svc.AgentTokenLive(ctx, other, rec.ID); err != nil || live {
		t.Errorf("another account's AgentTokenLive = %v, %v; want false, nil", live, err)
	}
	if live, err := svc.AgentTokenLive(ctx, owner, "not-a-uuid"); err != nil || live {
		t.Errorf("malformed id AgentTokenLive = %v, %v; want false, nil", live, err)
	}

	// Expired.
	expired, _, err := svc.CreateAgentToken(ctx, owner, "expired", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	if live, err := svc.AgentTokenLive(ctx, owner, expired.ID); err != nil || live {
		t.Errorf("expired AgentTokenLive = %v, %v; want false, nil", live, err)
	}

	// Revoked.
	if err := svc.RevokeAgentToken(ctx, owner, rec.ID); err != nil {
		t.Fatal(err)
	}
	if live, err := svc.AgentTokenLive(ctx, owner, rec.ID); err != nil || live {
		t.Errorf("revoked AgentTokenLive = %v, %v; want false, nil", live, err)
	}
}

// TestRevokeAccountEverywhereKillsAgentTokens: "log out everywhere", a password change, an
// account disable or a reconcile removal must take the account's agent tokens with it. An
// agent token's sub is its own id, so the account-wide entry reaches it through LINEAGE
// (= the account id), checked here through the same timestamp-aware snapshot checker the
// router builds per request.
func TestRevokeAccountEverywhereKillsAgentTokens(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "everywhere-owner", "member")

	_, raw, err := svc.CreateAgentToken(ctx, account, "doomed", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := svc.JWKS(ctx)
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); err != nil {
		t.Fatalf("token should verify before revocation: %v", err)
	}

	if err := svc.RevokeAccountEverywhere(ctx, account); err != nil {
		t.Fatalf("RevokeAccountEverywhere: %v", err)
	}
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("post-RevokeAccountEverywhere verify err = %v, want ErrRevoked", err)
	}

	// Signing back in clears the "sub" entry (startHumanSession's Unrevoke). That must NOT
	// resurrect the agent token: the "lineage" entry is deliberately never cleared, because an
	// agent token can live a year and a revoked one must stay dead.
	if err := svc.Unrevoke(ctx, "sub", account); err != nil {
		t.Fatal(err)
	}
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("agent token resurrected by a re-login's Unrevoke: err = %v, want ErrRevoked", err)
	}

	// A token minted AFTER the account-wide revocation (the owner signed back in) must NOT be
	// caught by the still-present entry — the timestamp scoping is what makes the account-wide
	// revoke a kill switch rather than a permanent lockout. No sleep here on purpose: minting
	// in the very same second as the revocation is the case CreateAgentToken handles itself
	// (TestCreateAgentTokenNeverPersistsABornDeadToken).
	_, fresh, err := svc.CreateAgentToken(ctx, account, "after", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cid.Verify(fresh, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); err != nil {
		t.Fatalf("token minted after the revocation should verify: %v", err)
	}
}

// I-3: an account-wide revocation must also mark the ROWS revoked, so the owner's Settings
// list says what is true. Without it, "log out everywhere" or a password change leaves a page
// of tokens reading "Active" while the lineage entry has already killed every one of them.
func TestRevokeAccountEverywhereMarksAgentTokenRowsRevoked(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "rows-revoked-owner", "member")
	other := insertAccount(t, st, "rows-revoked-bystander", "member")

	for _, name := range []string{"laptop", "ci"} {
		if _, _, err := svc.CreateAgentToken(ctx, account, name, "run-sessions", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := svc.CreateAgentToken(ctx, other, "untouched", "run-sessions", 0); err != nil {
		t.Fatal(err)
	}

	if err := svc.RevokeAccountEverywhere(ctx, account); err != nil {
		t.Fatalf("RevokeAccountEverywhere: %v", err)
	}

	list, err := svc.ListAgentTokens(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListAgentTokens returned %d tokens, want 2", len(list))
	}
	for _, rec := range list {
		if rec.RevokedAt == nil {
			t.Errorf("token %q still lists as live after an account-wide revocation", rec.Name)
		}
	}

	// Somebody else's tokens are untouched — the statement is scoped to the account.
	bystander, err := svc.ListAgentTokens(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(bystander) != 1 || bystander[0].RevokedAt != nil {
		t.Errorf("another account's tokens were revoked too: %+v", bystander)
	}

	// A token minted after the revocation is live again, row and all — the account-wide
	// revoke is a kill switch, not a permanent lockout.
	fresh, _, err := svc.CreateAgentToken(ctx, account, "after", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.RevokedAt != nil {
		t.Errorf("token minted after the revocation is already revoked: %+v", fresh)
	}
}

// TestCreateAgentTokenNeverPersistsABornDeadToken: the account-wide "lineage" revocation is
// permanent and applies to tokens issued at or BEFORE its whole-second revoked_at, so a token
// minted in that same second is dead for its entire life — while listing as perfectly live.
// CreateAgentToken must detect that and re-mint past the second boundary rather than hand the
// user a credential that has never worked and never will.
func TestCreateAgentTokenNeverPersistsABornDeadToken(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "borndead-owner", "member")
	keys, _ := svc.JWKS(ctx)

	// Revoke "now" and mint immediately, with no sleep: without the re-mint this lands in the
	// same second as revoked_at and the returned token is revoked on arrival.
	if err := svc.RevokeAccountEverywhere(ctx, account); err != nil {
		t.Fatal(err)
	}
	rec, raw, err := svc.CreateAgentToken(ctx, account, "same second", "run-sessions", 0)
	if err != nil {
		t.Fatalf("CreateAgentToken right after a revocation: %v", err)
	}
	if _, err := cid.Verify(raw, "blerg-runner", keys, liveChecker(t, svc), noSensitiveCaps); err != nil {
		t.Fatalf("freshly minted token does not verify: %v", err)
	}

	// The persisted row and the token agree: the re-mint did not leave a row describing a
	// token the caller never received.
	var stored string
	if err := st.Pool().QueryRow(ctx, `SELECT token_hash FROM agent_tokens WHERE id = $1`, rec.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != agentTokenHash(raw) {
		t.Error("the stored hash is not the hash of the returned token")
	}
}

// TestCreateAgentTokenCapsLiveTokensPerAccount: an account may hold at most maxLiveAgentTokens
// live tokens at once, so a compromised session cannot quietly mint credentials without bound.
// Only LIVE ones count — revoking or expiring one makes room again.
func TestCreateAgentTokenCapsLiveTokensPerAccount(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "cap-owner", "member")
	other := insertAccount(t, st, "cap-other", "member")

	var first string
	for i := 0; i < maxLiveAgentTokens; i++ {
		rec, _, err := svc.CreateAgentToken(ctx, account, "t", "platform", 0)
		if err != nil {
			t.Fatalf("token %d of %d: %v", i+1, maxLiveAgentTokens, err)
		}
		if i == 0 {
			first = rec.ID
		}
	}
	// Exactly at the limit is still fine; one past it is not.
	if _, _, err := svc.CreateAgentToken(ctx, account, "one too many", "platform", 0); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("token %d = %v, want ErrTooManyTokens", maxLiveAgentTokens+1, err)
	}

	// The cap is per account: somebody else is unaffected.
	if _, _, err := svc.CreateAgentToken(ctx, other, "theirs", "platform", 0); err != nil {
		t.Fatalf("another account's mint = %v, want success", err)
	}

	// Revoking one makes room.
	if err := svc.RevokeAgentToken(ctx, account, first); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAgentToken(ctx, account, "after revoke", "platform", 0); err != nil {
		t.Fatalf("mint after freeing a slot = %v, want success", err)
	}

	// So does expiry: an expired token is not a live one.
	if _, _, err := svc.CreateAgentToken(ctx, account, "blocked again", "platform", 0); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("mint at the cap again = %v, want ErrTooManyTokens", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 minute'
		  WHERE account_id = $1 AND revoked_at IS NULL
		  AND id = (SELECT id FROM agent_tokens WHERE account_id = $1 AND revoked_at IS NULL LIMIT 1)`,
		account); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateAgentToken(ctx, account, "after expiry", "platform", 0); err != nil {
		t.Fatalf("mint after a token expired = %v, want success", err)
	}
}

// TestAgentTokenHashIsHexSHA256OfTheToken: core stores a hash, never the value — and the hash
// is the documented hex SHA-256 so an operator holding a leaked token can identify its row.
func TestAgentTokenHashIsHexSHA256OfTheToken(t *testing.T) {
	ctx := context.Background()
	svc, st := agentTokenSvc(t)
	account := insertAccount(t, st, "hash-owner", "member")

	rec, raw, err := svc.CreateAgentToken(ctx, account, "hashed", "platform", 0)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := st.Pool().QueryRow(ctx, `SELECT token_hash FROM agent_tokens WHERE id = $1`, rec.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != agentTokenHash(raw) {
		t.Errorf("token_hash = %q, want hex sha256 %q", stored, agentTokenHash(raw))
	}
	if len(stored) != 64 {
		t.Errorf("token_hash length = %d, want 64 hex chars", len(stored))
	}
	// The row must not contain the token anywhere.
	var count int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM agent_tokens WHERE token_hash = $1 OR name = $1`, raw).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("the raw token appears in the agent_tokens row")
	}
}
