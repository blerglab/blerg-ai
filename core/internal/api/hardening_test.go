package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
)

func credentialsPost(t *testing.T, srv *httptest.Server, tok, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/credentials", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCredentialBodyIsCappedAndEngineAllowlisted(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	acct := insertCredAccount(t, pool, "hardening")
	tok, _ := idSvc.MintHumanAccessToken(context.Background(), acct, "blerg-core")
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	big := credentialsPost(t, srv, tok, `{"engine":"claude","credential":"`+strings.Repeat("a", 70<<10)+`"}`)
	big.Body.Close()
	if big.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("70 KiB body = %d, want 413", big.StatusCode)
	}
	bad := credentialsPost(t, srv, tok, `{"engine":"notreal","credential":"x"}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown engine = %d, want 400", bad.StatusCode)
	}
}

func TestPasswordChangeOnlyTokenCannotUseCredentials(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	acct := insertCredAccount(t, pool, "must-change")
	pool.Exec(context.Background(), `UPDATE accounts SET must_change_password = true WHERE id = $1`, acct)
	tok, _ := idSvc.MintHumanAccessToken(context.Background(), acct, "blerg-core")
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/credentials", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("password-change-only token on /api/credentials = %d, want 403", resp.StatusCode)
	}
}

func TestBearerErrorsAreGeneric(t *testing.T) {
	deps, _, _ := newCredentialsTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/credentials", nil)
	req.Header.Set("Authorization", "Bearer not.a.token")
	resp, _ := srv.Client().Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || strings.TrimSpace(string(body)) != "unauthorized" {
		t.Fatalf("bad token = %d %q, want 401 \"unauthorized\"", resp.StatusCode, body)
	}
}

func TestInternalFetchStatusesDoNotEnumerate(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	resp := doFetch(t, srv, "Bearer "+testInternalKey, "not-a-uuid", "claude", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-uuid account_id = %d, want 400", resp.StatusCode)
	}
	acct := insertCredAccount(t, pool, "no-session")
	resp = doFetch(t, srv, "Bearer "+testInternalKey, acct, "claude", noSuchSession) // no live session
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no live session = %d, want 404 (not 403)", resp.StatusCode)
	}
}
