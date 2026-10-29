package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestDesktopCredentialsArePersonalHashedAndCannotClaimAnotherPerson(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-desktop-internal-key"
	svc := deps.Identity.(*identity.Service)
	a, b := insertCredAccount(t, pool, "desktop-person-a"), insertCredAccount(t, pool, "desktop-person-b")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	sidA, sidB := insertLiveHumanSession(t, pool, a), insertLiveHumanSession(t, pool, b)
	tokenA, err := svc.MintHumanAccessTokenForSession(t.Context(), a, "blerg-core", sidA)
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := svc.MintHumanAccessTokenForSession(t.Context(), b, "blerg-core", sidB)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	call := func(path, token, key string, body any) (int, []byte) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, bytes.NewReader(raw))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if key != "" {
			req.Header.Set("X-Internal-Key", key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.Header.Get("Cache-Control") != "no-store" && res.StatusCode == 200 {
			t.Fatal("credential response can be cached")
		}
		return res.StatusCode, data
	}
	code, body := call("/api/desktop-tokens", tokenA, "", map[string]any{})
	if code == 200 || strings.Contains(string(body), "bdt_") {
		t.Fatal("browser response contains a desktop credential")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM desktop_tokens`).Scan(&count); err != nil || count != 0 {
		t.Fatal("browser request minted a credential", err)
	}
	desktop := "00000000-0000-4000-8000-000000000174"
	code, body = call("/internal/desktop-tokens/issue", "", deps.InternalKey, map[string]any{"desktop_id": desktop, "ownership_revision": 1, "owner_id": a})
	if code != 200 {
		t.Fatalf("personal issuance: %d %s", code, body)
	}
	var issued struct {
		Binding desktopauth.Binding `json:"binding"`
		Token   string              `json:"token"`
	}
	if json.Unmarshal(body, &issued) != nil || issued.Binding.OwnerID != a || issued.Binding.DesktopID == "" || !strings.HasPrefix(issued.Token, "bdt_") {
		t.Fatal("wrong personal binding")
	}
	var stored string
	if err := pool.QueryRow(t.Context(), `SELECT token_hash FROM desktop_tokens WHERE id=$1`, issued.Binding.TokenID).Scan(&stored); err != nil || stored == issued.Token || len(stored) != 64 {
		t.Fatal("bearer stored in clear text", err)
	}
	code, body = call("/internal/desktop-tokens/validate", "", deps.InternalKey, map[string]string{"token": issued.Token})
	if code != 200 || strings.Contains(string(body), issued.Token) {
		t.Fatalf("private validation: %d", code)
	}
	if code, _ := call("/api/desktop-tokens", tokenB, "", map[string]any{"desktop_id": desktop, "ownership_revision": 1, "owner_id": a}); code != 404 {
		t.Fatalf("person B claimed A: %d", code)
	}
	if code, _ := call("/api/desktop-tokens", tokenA, "", map[string]any{"desktop_id": desktop, "ownership_revision": 2}); code != 404 {
		t.Fatalf("future or existing scope minted: %d", code)
	}

	if code, _ := call("/internal/desktop-tokens/issue", tokenA, "", map[string]any{"desktop_id": desktop, "ownership_revision": 1}); code != 401 {
		t.Fatalf("person called installer issuer: %d", code)
	}
	code, body = call("/internal/desktop-tokens/issue", "", deps.InternalKey, map[string]any{"desktop_id": desktop, "ownership_revision": 2})
	var bootstrap struct {
		Binding desktopauth.Binding `json:"binding"`
	}
	_ = json.Unmarshal(body, &bootstrap)
	if code != 200 || bootstrap.Binding.OwnerID != a || bootstrap.Binding.Revision != 2 {
		t.Fatalf("first-admin upgrade: %d %s", code, body)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE desktop_tokens SET revoked_at=now() WHERE id=$1`, issued.Binding.TokenID); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("/internal/desktop-tokens/validate", "", deps.InternalKey, map[string]string{"token": issued.Token}); code != 401 {
		t.Fatalf("revoked token accepted: %d", code)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET disabled_at=now() WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.IssueDesktopToken(t.Context(), b, desktop, 1); err == nil {
		t.Fatal("disabled account issued a desktop token")
	}
}
