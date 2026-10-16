package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/jackc/pgx/v5/pgxpool"
)

type apiManagedAdapter struct {
	started, release chan struct{}
	calls            atomic.Int32
}

func (*apiManagedAdapter) AccessMode() string         { return "chatgptAuthTokens" }
func (*apiManagedAdapter) Engine() string             { return "codex" }
func (*apiManagedAdapter) Kind() string               { return "codex_oauth" }
func (*apiManagedAdapter) VerificationOrigin() string { return "https://auth.openai.com" }
func (*apiManagedAdapter) StartDevice(context.Context) (credentials.DeviceGrant, error) {
	return credentials.DeviceGrant{DeviceID: "fixture-device-secret", UserCode: "ABCD-EFGH", VerificationURL: "https://auth.openai.com/codex/device", IntervalSeconds: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (*apiManagedAdapter) PollDevice(context.Context, credentials.DeviceGrant) (credentials.ManagedToken, error) {
	return apiManagedToken(), nil
}
func (a *apiManagedAdapter) Refresh(context.Context, credentials.ManagedToken) (credentials.ManagedToken, error) {
	a.calls.Add(1)
	if a.started != nil {
		close(a.started)
		<-a.release
	}
	return apiManagedToken(), nil
}
func apiManagedToken() credentials.ManagedToken {
	return credentials.ManagedToken{AccessToken: "fixture-private-access", RefreshToken: "fixture-private-refresh", AccountID: "provider-account", ExpiresAt: time.Now().Add(time.Hour)}
}
func newManagedAPIDeps(t *testing.T, a *apiManagedAdapter) (api.Deps, *pgxpool.Pool, string, string, credentials.Access) {
	t.Helper()
	deps, pool := newInternalTestDeps(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	deps.Credentials = credentials.NewServiceWithAdapters(db.NewPgStore(pool), backend, a)
	account := insertCredAccount(t, pool, "managed-api")
	sid := insertLiveHumanSession(t, pool, account)
	start, err := deps.Credentials.StartLogin(context.Background(), account, "codex", sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deps.Credentials.PollLogin(context.Background(), account, "codex", sid, start.AttemptID); err != nil {
		t.Fatal(err)
	}
	access, err := deps.Credentials.Obtain(context.Background(), account, "codex", false, "")
	if err != nil {
		t.Fatal(err)
	}
	return deps, pool, account, sid, access
}
func managedAPIRequest(t *testing.T, srv *httptest.Server, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/credentials/managed/access", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testInternalKey)
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func TestManagedAccessBindsAccountProofAndNeverReturnsRefreshToken(t *testing.T) {
	a := &apiManagedAdapter{}
	deps, pool, account, sid, _ := newManagedAPIDeps(t, a)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	other := insertCredAccount(t, pool, "other-managed-account")
	otherSID := insertLiveHumanSession(t, pool, other)
	cases := []struct {
		account, sid string
		want         int
	}{{account, otherSID, 404}, {other, sid, 404}, {account, sid, 200}}
	for _, c := range cases {
		response := managedAPIRequest(t, srv, map[string]any{"account_id": c.account, "engine": "codex", "session_id": c.sid})
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != c.want || strings.Contains(string(raw), "fixture-private-refresh") || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("credential response did not respect authorization/redaction: status %d", response.StatusCode)
		}
	}
	if a.calls.Load() != 0 {
		t.Fatal("proof check or healthy access triggered refresh")
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM credential_access_log WHERE account_id=$1`, account).Scan(&count); err != nil || count != 1 {
		t.Fatalf("access audit count %d: %v", count, err)
	}
}
func TestManagedAccessRechecksRevocationAfterProviderIO(t *testing.T) {
	a := &apiManagedAdapter{started: make(chan struct{}), release: make(chan struct{})}
	deps, pool, account, sid, access := newManagedAPIDeps(t, a)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	response := make(chan *http.Response, 1)
	go func() {
		response <- managedAPIRequest(t, srv, map[string]any{"account_id": account, "engine": "codex", "session_id": sid, "force": true, "previous_generation": access.Generation})
	}()
	<-a.started
	if _, err := pool.Exec(context.Background(), `UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1`, account); err != nil {
		t.Fatal(err)
	}
	close(a.release)
	got := <-response
	defer got.Body.Close()
	raw, _ := io.ReadAll(got.Body)
	if got.StatusCode != 404 || strings.Contains(string(raw), "fixture-private") {
		t.Fatalf("revoked proof disclosed access: %d", got.StatusCode)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM credential_access_log`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked disclosure was audited as successful: %d, %v", count, err)
	}
}
func TestManagedAccessFailsClosedOnAuditFailureAndMalformedProof(t *testing.T) {
	deps, pool, account, sid, _ := newManagedAPIDeps(t, &apiManagedAdapter{})
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	for _, body := range []map[string]any{
		{"account_id": account, "engine": "codex"},
		{"account_id": account, "engine": "codex", "session_id": sid, "token_id": sid},
		{"account_id": account, "engine": "codex", "session_id": sid, "force": true},
		{"account_id": account, "engine": "codex", "session_id": sid, "arbitrary": "value"},
	} {
		response := managedAPIRequest(t, srv, body)
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("malformed proof accepted: %d", response.StatusCode)
		}
	}
	if _, err := pool.Exec(context.Background(), `ALTER TABLE credential_access_log RENAME TO unavailable_access_log`); err != nil {
		t.Fatal(err)
	}
	response := managedAPIRequest(t, srv, map[string]any{"account_id": account, "engine": "codex", "session_id": sid})
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != 500 || strings.Contains(string(raw), "fixture-private") {
		t.Fatalf("audit failure disclosed access: %d", response.StatusCode)
	}
}

func TestManagedBrowserEndpointsRequireOwnLiveBrowserAndOnlyExposeMetadata(t *testing.T) {
	deps, pool, account, sid, _ := newManagedAPIDeps(t, &apiManagedAdapter{})
	identityService, ok := deps.Identity.(*identity.Service)
	if !ok {
		t.Fatal("identity fixture missing")
	}
	token, err := identityService.MintHumanAccessTokenForSession(context.Background(), account, "blerg-core", sid)
	if err != nil {
		t.Fatal(err)
	}
	noSID, err := identityService.MintHumanAccessToken(context.Background(), account, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	request := func(method, path, bearer string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.Header.Get("Cache-Control") != "no-store" && response.StatusCode == 200 {
			t.Fatal("managed metadata response is cacheable")
		}
		if strings.Contains(string(raw), "fixture-private") || strings.Contains(string(raw), "fixture-device-secret") {
			t.Fatal("browser response leaked vault material")
		}
		return response.StatusCode, string(raw)
	}
	if status, _ := request(http.MethodPost, "/api/credentials/codex/managed/login", noSID); status != 401 {
		t.Fatal("login accepted a token without a browser SID")
	}
	if status, _ := request(http.MethodPost, "/api/credentials/codex/managed/login", ""); status != 401 {
		t.Fatal("login accepted an unauthenticated caller")
	}
	if status, body := request(http.MethodGet, "/api/credentials/managed/capabilities", token); status != 200 || !strings.Contains(body, `"verification_origin":"https://auth.openai.com"`) {
		t.Fatal("managed adapter capabilities unavailable")
	}
	status, body := request(http.MethodPost, "/api/credentials/codex/managed/login", token)
	if status != 200 {
		t.Fatalf("login start status %d", status)
	}
	var start credentials.LoginStatus
	if err = json.Unmarshal([]byte(body), &start); err != nil {
		t.Fatal(err)
	}
	otherSID := insertLiveHumanSession(t, pool, account)
	otherToken, err := identityService.MintHumanAccessTokenForSession(context.Background(), account, "blerg-core", otherSID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/credentials/codex/managed/login/" + start.AttemptID
	if status, _ = request(http.MethodGet, path, otherToken); status != 404 {
		t.Fatal("another browser read a pending login")
	}
	if status, _ = request(http.MethodDelete, path, otherToken); status != 404 {
		t.Fatal("another browser canceled a pending login")
	}
	if status, _ = request(http.MethodDelete, path, token); status != 204 {
		t.Fatal("initiating browser could not cancel its login")
	}
	if _, err = pool.Exec(context.Background(), `UPDATE human_sessions SET revoked_at=now() WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if status, _ = request(http.MethodPost, "/api/credentials/codex/managed/login", token); status != 401 {
		t.Fatal("revoked browser started a managed login")
	}
}
