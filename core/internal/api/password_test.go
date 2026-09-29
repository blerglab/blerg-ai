package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// TestChangePasswordFlow is R7/I-7's end-to-end proof: a must-change-password account can only
// reach POST /auth/password with its access token; a wrong old password is 401; a weak new
// password is 400; a correct change is 200 (and clears the refresh cookie), clears the flag,
// revokes the account EVERYWHERE (this stale refresh session AND, per the controller ruling, an
// access token already minted on another device before the change), and a fresh mint afterward
// carries the full role caps again — and the new password actually works at POST /auth/login.
func TestChangePasswordFlow(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	ctx := context.Background()
	insertLocalAccount(t, pool, "boot", "old-password-123")
	var accountID string
	pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject='boot'`).Scan(&accountID)
	pool.Exec(ctx, `UPDATE accounts SET must_change_password = true WHERE id = $1`, accountID)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	idSvc := deps.Identity.(*identity.Service)

	// A session that must die when the password changes.
	staleRefresh, err := idSvc.IssueRefreshToken(ctx, accountID, "ua", "ip")
	if err != nil {
		t.Fatal(err)
	}
	// The very token used to authenticate these requests: it must ALSO stop verifying after a
	// successful change — RevokeAccountEverywhere revokes the account's "sub", so an access
	// token minted before the change (imagine it living on some OTHER, now-compromised device)
	// dies immediately via the revocation snapshot, not just on its next refresh attempt.
	tok, err := idSvc.MintHumanAccessToken(ctx, accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	change := func(old, newPw string) *http.Response {
		body := `{"old_password":` + jsonString(old) + `,"new_password":` + jsonString(newPw) + `}`
		req, _ := http.NewRequest("POST", srv.URL+"/auth/password", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := change("wrong", "a-long-enough-new-password"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong old password = %d, want 401", resp.StatusCode)
	} else if got := resp.Header.Get("X-Blerg-Error"); got != "invalid_credentials" {
		t.Fatalf("wrong old password X-Blerg-Error = %q, want invalid_credentials (ChangePassword.tsx keys off it)", got)
	}
	// A stale/missing token is ALSO a 401 on this route, but must NOT carry that header — the
	// page refreshes on that one instead of blaming the password.
	{
		req, _ := http.NewRequest("POST", srv.URL+"/auth/password", strings.NewReader(`{"old_password":"x","new_password":"a-long-enough-new-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("X-Blerg-Error") != "" {
			t.Fatalf("no token = %d with X-Blerg-Error=%q, want 401 without the header", resp.StatusCode, resp.Header.Get("X-Blerg-Error"))
		}
	}
	if resp := change("old-password-123", "short"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak new password (too short) = %d, want 400", resp.StatusCode)
	}
	if resp := change("old-password-123", strings.Repeat("x", 73)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("weak new password (73 bytes, over bcrypt's 72-byte limit) = %d, want 400", resp.StatusCode)
	}
	resp := change("old-password-123", "a-long-enough-new-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change = %d, want 200", resp.StatusCode)
	}
	var cleared bool
	for _, c := range resp.Cookies() {
		if c.Name == refreshCookieNameForTest && c.MaxAge <= 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("a successful change must clear the refresh cookie (Max-Age <= 0), got %v", resp.Cookies())
	}
	var must bool
	pool.QueryRow(ctx, `SELECT must_change_password FROM accounts WHERE id = $1`, accountID).Scan(&must)
	if must {
		t.Fatal("must_change_password still set after a successful change")
	}
	if _, _, err := idSvc.RefreshAccessToken(ctx, staleRefresh, "blerg-core"); err == nil {
		t.Fatal("pre-change session must be revoked by a password change")
	}
	// The controller ruling: RevokeAccountEverywhere, not just RevokeAllSessions — the access
	// token used to make these very requests must now fail Verify via the revocation snapshot.
	keys, err := idSvc.JWKS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := idSvc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := map[string]bool{}
	for _, r := range snap {
		rev[r.Kind+":"+r.Value] = true
	}
	if _, err := cid.Verify(tok, "blerg-core", keys, testRevChecker(rev), func(string) bool { return false }); !errors.Is(err, cid.ErrRevoked) {
		t.Fatalf("pre-change access token Verify after the password change = %v, want ErrRevoked (RevokeAccountEverywhere must revoke \"sub\", not just sessions)", err)
	}
	fresh, _ := idSvc.MintHumanAccessToken(ctx, accountID, "blerg-core")
	if !strings.Contains(fresh, ".") || len(decodeClaims(t, fresh).Caps) < 2 {
		t.Fatal("after the change the token must carry the full role caps again")
	}
	loginResp, err := srv.Client().PostForm(srv.URL+"/auth/login",
		url.Values{"provider_subject": {"boot"}, "password": {"a-long-enough-new-password"}})
	if err != nil || loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login with the new password = %v %d", err, loginResp.StatusCode)
	}
}

// testRevChecker is a minimal cid.RevocationChecker built from a snapshot map, matching the
// shape auth_handlers_test.go/human_session_test.go's own test checkers use.
type testRevChecker map[string]bool

func (c testRevChecker) Revoked(kid, lineage, sub string) bool {
	return c["kid:"+kid] || c["lineage:"+lineage] || c["sub:"+sub]
}
func (testRevChecker) StaleBeyondCeiling() bool { return false }

// TestChangePasswordRejectsCrossSite proves POST /auth/password is guarded by the same
// same-origin CSRF check as the other cookie/session-mutating auth endpoints (login, logout,
// logout-all) — a cross-site POST (no Sec-Fetch-Site, a foreign Origin) is rejected before the
// body is ever read, even with a fully valid bearer token.
func TestChangePasswordRejectsCrossSite(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	ctx := context.Background()
	insertLocalAccount(t, pool, "csrf-target", "old-password-123")
	var accountID string
	pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject='csrf-target'`).Scan(&accountID)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(ctx, accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}

	body := `{"old_password":"old-password-123","new_password":"a-long-enough-new-password"}`
	req, _ := http.NewRequest("POST", srv.URL+"/auth/password", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST /auth/password = %d, want 403", resp.StatusCode)
	}
}

// TestPasswordChangeOnlyTokenCannotReadCredentials is R7's "nothing else works" server-side
// enforcement, verified end to end: a must-change-password account's access token (caps =
// ["password.change"] only) must be refused by GET /api/credentials (403), even though it is
// otherwise a perfectly valid, non-expired, non-revoked human bearer token.
func TestPasswordChangeOnlyTokenCannotReadCredentials(t *testing.T) {
	deps, pool, _ := newCredentialsTestDeps(t)
	ctx := context.Background()
	accountID := insertCredAccount(t, pool, "must-change-creds")
	pool.Exec(ctx, `UPDATE accounts SET must_change_password = true WHERE id = $1`, accountID)
	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(ctx, accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/credentials with a password.change-only token = %d, want 403", resp.StatusCode)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestChangePasswordIsRateLimited is the controller addendum to Task 7: handleChangePassword
// verifies old_password with bcrypt and, before this, had NO attempt limiting at all — a
// stolen/hijacked access token bought unlimited guesses at the real password, and success
// revokes every other session (full takeover). It must share the SAME LoginLimiter as
// POST /auth/login, keyed on the authenticated principal's Sub (there is no unauthenticated
// subject on this path) and the caller's IP. N wrong old passwords must 429 (with Retry-After);
// the CORRECT old password must still be refused while blocked (the limit is checked before
// ChangePassword ever runs, matching handleLogin's own shape); and a fresh attempt must succeed
// once the injected clock has advanced past the window.
func TestChangePasswordIsRateLimited(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	ctx := context.Background()
	insertLocalAccount(t, pool, "rl-changer", "correct-old-password")
	var accountID string
	pool.QueryRow(ctx, `SELECT id::text FROM accounts WHERE provider_subject='rl-changer'`).Scan(&accountID)

	limiter := api.NewLoginLimiter(3, time.Minute)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	limiter.SetClock(func() time.Time { return now })
	deps.LoginLimiter = limiter

	idSvc := deps.Identity.(*identity.Service)
	tok, err := idSvc.MintHumanAccessToken(ctx, accountID, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	change := func(old, newPw string) *http.Response {
		body := `{"old_password":` + jsonString(old) + `,"new_password":` + jsonString(newPw) + `}`
		req, _ := http.NewRequest("POST", srv.URL+"/auth/password", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	for i := 0; i < 3; i++ {
		if resp := change("wrong-old-password", "a-long-enough-new-password"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong old password %d = %d, want 401", i+1, resp.StatusCode)
		}
	}
	resp := change("correct-old-password", "a-long-enough-new-password")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th attempt (correct old password) = %d, want 429", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Fatal("429 must carry Retry-After")
	}

	// Advance the injected clock past the window: the correct old password now succeeds.
	now = now.Add(61 * time.Second)
	resp = change("correct-old-password", "a-long-enough-new-password")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("correct old password after window elapsed = %d, want 200", resp.StatusCode)
	}
}

// TestRefreshDivertsMustChangePasswordAccountToChangePassword: a must-change-password account
// refreshing on its way to board (the landing-page → board tile → sign-in-required → login path)
// must NOT be handed a password.change-only token for board's audience — board has no screen
// for one and just renders 403s. handleRefresh sends it to core's own /change-password instead,
// with a core-audience token and the original return_to preserved for after the change. Once
// the flag is cleared, the same refresh goes to board as usual.
func TestRefreshDivertsMustChangePasswordAccountToChangePassword(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, map[string]string{"http://board.example.com": "blerg-board"})
	ctx := context.Background()
	insertLocalAccount(t, pool, "fresh", "bootstrap-password-1")
	pool.Exec(ctx, `UPDATE accounts SET must_change_password = true WHERE provider_subject='fresh'`)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := srv.Client()
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	form := url.Values{"provider_subject": {"fresh"}, "password": {"bootstrap-password-1"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", resp.StatusCode)
	}

	refresh := func(returnTo string) string {
		t.Helper()
		resp, err := client.Get(srv.URL + "/auth/refresh?return_to=" + url.QueryEscape(returnTo))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("refresh = %d, want 302", resp.StatusCode)
		}
		return resp.Header.Get("Location")
	}

	boardURL := "http://board.example.com/boards/1"
	loc := refresh(boardURL)
	wantPrefix := srv.URL + "/change-password?return_to=" + url.QueryEscape(boardURL) + "#access_token="
	if !strings.HasPrefix(loc, wantPrefix) {
		t.Fatalf("must-change refresh redirected to %q, want prefix %q", loc, wantPrefix)
	}
	claims := decodeClaims(t, strings.TrimPrefix(loc, wantPrefix))
	if claims.Aud != "blerg-core" || len(claims.Caps) != 1 || claims.Caps[0] != "password.change" {
		t.Fatalf("diverted token aud=%q caps=%v, want blerg-core / [password.change]", claims.Aud, claims.Caps)
	}

	// The change-password page's own refresh (apiFetch on a 401) must not nest return_to.
	self := srv.URL + "/change-password?return_to=" + url.QueryEscape(boardURL)
	if loc := refresh(self); !strings.HasPrefix(loc, self+"#access_token=") {
		t.Fatalf("refresh from the change-password page itself redirected to %q, want it unchanged", loc)
	}

	pool.Exec(ctx, `UPDATE accounts SET must_change_password = false WHERE provider_subject='fresh'`)
	if loc := refresh(boardURL); !strings.HasPrefix(loc, boardURL+"#access_token=") {
		t.Fatalf("refresh after the flag cleared redirected to %q, want board", loc)
	}
}
