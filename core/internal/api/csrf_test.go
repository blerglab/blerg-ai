package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// TestLoginCSRFMatrix is the login-CSRF regression test (R4/I-4): POST /auth/login must reject
// a cross-site caller even when the submitted credentials are correct, and must still accept a
// same-origin (or non-browser, header-less) caller. httptest.NewTLSServer is required (not
// NewServer) because sameOriginRequest derives publicOrigin from the request when
// Deps.PublicURL is unset, and the origin comparison below is written against srv.URL, which is
// only meaningful once TLS is up (matching the convention in TestFullLoginRefreshLogoutFlow).
func TestLoginCSRFMatrix(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	insertLocalAccount(t, pool, "csrfuser", "correct-horse-battery-staple")

	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	doLogin := func(t *testing.T, setHeaders func(*http.Request)) int {
		t.Helper()
		form := url.Values{"provider_subject": {"csrfuser"}, "password": {"correct-horse-battery-staple"}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if setHeaders != nil {
			setHeaders(req)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("cross-site Sec-Fetch-Site is rejected", func(t *testing.T) {
		got := doLogin(t, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		if got != http.StatusForbidden {
			t.Fatalf("login with Sec-Fetch-Site: cross-site = %d, want 403", got)
		}
	})

	t.Run("foreign Origin is rejected", func(t *testing.T) {
		got := doLogin(t, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
		if got != http.StatusForbidden {
			t.Fatalf("login with Origin: https://evil.example = %d, want 403", got)
		}
	})

	t.Run("own origin is accepted", func(t *testing.T) {
		got := doLogin(t, func(r *http.Request) { r.Header.Set("Origin", srv.URL) })
		if got != http.StatusOK {
			t.Fatalf("login with own-origin Origin header = %d, want 200", got)
		}
	})

	t.Run("neither header is accepted (non-browser client)", func(t *testing.T) {
		got := doLogin(t, nil)
		if got != http.StatusOK {
			t.Fatalf("login with neither Sec-Fetch-Site nor Origin = %d, want 200", got)
		}
	})
}

// TestLogoutCSRFMatrix runs the same matrix against POST /auth/logout. Unlike login, a missing
// or invalid refresh cookie is a no-op success for logout (see handleLogout's own doc comment),
// so every accepted case here is still expected to return 200 even with no live session —
// what's under test is only whether the CSRF guard itself lets the request through.
func TestLogoutCSRFMatrix(t *testing.T) {
	deps, _ := newTestDeps(t, []string{"http://board.example.com"}, nil)
	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	doLogout := func(t *testing.T, setHeaders func(*http.Request)) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout", nil)
		if setHeaders != nil {
			setHeaders(req)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("cross-site Sec-Fetch-Site is rejected", func(t *testing.T) {
		got := doLogout(t, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		if got != http.StatusForbidden {
			t.Fatalf("logout with Sec-Fetch-Site: cross-site = %d, want 403", got)
		}
	})

	t.Run("foreign Origin is rejected", func(t *testing.T) {
		got := doLogout(t, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
		if got != http.StatusForbidden {
			t.Fatalf("logout with Origin: https://evil.example = %d, want 403", got)
		}
	})

	t.Run("own origin is accepted", func(t *testing.T) {
		got := doLogout(t, func(r *http.Request) { r.Header.Set("Origin", srv.URL) })
		if got != http.StatusOK {
			t.Fatalf("logout with own-origin Origin header = %d, want 200", got)
		}
	})

	t.Run("neither header is accepted (non-browser client)", func(t *testing.T) {
		got := doLogout(t, nil)
		if got != http.StatusOK {
			t.Fatalf("logout with neither Sec-Fetch-Site nor Origin = %d, want 200", got)
		}
	})
}

// TestLogoutAllCSRFMatrix runs the matrix against POST /auth/logout-all. Unlike plain logout,
// this route is wrapped in requireHumanPrincipal in router.go, which runs BEFORE handleLogoutAll
// (and its internal sameOriginRequest check) ever executes — so a request needs a genuinely
// valid human bearer token for the CSRF guard's own verdict to be observable at all; otherwise
// every case below would just see requireHumanPrincipal's 401 and the matrix would prove
// nothing about the CSRF guard specifically.
func TestLogoutAllCSRFMatrix(t *testing.T) {
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	idSvc := deps.Identity.(*identity.Service)

	srv := httptest.NewTLSServer(api.NewRouter(deps))
	defer srv.Close()

	// A successful logout-all revokes the account's sub in the shared revocations table
	// (identity.Service.RevokeAccountEverywhere), which would make every token minted for the
	// SAME account fail verification on any later call — so each case below gets its own
	// fresh account and its own freshly minted token, rather than reusing one across the
	// matrix.
	doLogoutAll := func(t *testing.T, subject string, setHeaders func(*http.Request)) int {
		t.Helper()
		insertLocalAccount(t, pool, subject, "another-password-1")
		var accountID string
		if err := pool.QueryRow(context.Background(),
			`SELECT id::text FROM accounts WHERE provider_subject=$1`, subject).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		tok, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout-all", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if setHeaders != nil {
			setHeaders(req)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("cross-site Sec-Fetch-Site is rejected even with a valid bearer token", func(t *testing.T) {
		got := doLogoutAll(t, "csrfall-a", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		if got != http.StatusForbidden {
			t.Fatalf("logout-all with Sec-Fetch-Site: cross-site = %d, want 403", got)
		}
	})

	t.Run("foreign Origin is rejected even with a valid bearer token", func(t *testing.T) {
		got := doLogoutAll(t, "csrfall-b", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
		if got != http.StatusForbidden {
			t.Fatalf("logout-all with Origin: https://evil.example = %d, want 403", got)
		}
	})

	t.Run("own origin is accepted", func(t *testing.T) {
		got := doLogoutAll(t, "csrfall-c", func(r *http.Request) { r.Header.Set("Origin", srv.URL) })
		if got != http.StatusOK {
			t.Fatalf("logout-all with own-origin Origin header = %d, want 200", got)
		}
	})

	t.Run("neither header is accepted (non-browser client)", func(t *testing.T) {
		got := doLogoutAll(t, "csrfall-d", nil)
		if got != http.StatusOK {
			t.Fatalf("logout-all with neither CSRF header = %d, want 200", got)
		}
	})
}
