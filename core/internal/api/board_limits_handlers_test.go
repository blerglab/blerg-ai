package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

const boardLimitsInternalKey = "board-limits-internal-key"

// Board call limits are an administrator's setting: a member can neither read nor set them, a set
// needs a live sign-in and valid values, and the runner reads what was set with its internal key.
func TestBoardLimitsAreAnAdministratorsSettingAndTheRunnerReadsThem(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = boardLimitsInternalKey
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	idSvc := deps.Identity.(*identity.Service)
	ctx := context.Background()

	admin := insertCredAccount(t, pool, "limits-admin")
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role = 'admin' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	member := insertCredAccount(t, pool, "limits-member")
	adminSid, memberSid := insertLiveHumanSession(t, pool, admin), insertLiveHumanSession(t, pool, member)
	mint := func(account, sid string) string {
		t.Helper()
		tok, err := idSvc.MintHumanAccessTokenForSession(ctx, account, "blerg-core", sid)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	adminTok, memberTok := mint(admin, adminSid), mint(member, memberSid)
	adminNoSid, err := idSvc.MintHumanAccessToken(ctx, admin, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, path, tok string, body any, hdr map[string]string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	internal := func() (int, identity.BoardLimits) {
		t.Helper()
		code, body := do(http.MethodPost, "/internal/board-limits", "", map[string]any{}, map[string]string{"X-Internal-Key": boardLimitsInternalKey})
		var l identity.BoardLimits
		_ = json.Unmarshal([]byte(body), &l)
		return code, l
	}

	if code, l := internal(); code != 200 || l != identity.DefaultBoardLimits {
		t.Fatalf("runner read of the defaults: %d %+v", code, l)
	}
	if code, _ := do(http.MethodPost, "/internal/board-limits", "", map[string]any{}, map[string]string{"X-Internal-Key": "wrong"}); code != http.StatusUnauthorized {
		t.Fatalf("runner read with a wrong key: %d", code)
	}
	if code, _ := do(http.MethodGet, "/api/settings/board-limits", memberTok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("member GET: %d", code)
	}
	set := identity.BoardLimits{CallsPerMinute: 1200, CallsInFlight: 12, UnrevokedPerAccount: 30, LiveExchangeTokens: 200}
	if code, _ := do(http.MethodPut, "/api/settings/board-limits", memberTok, set, nil); code != http.StatusForbidden {
		t.Fatalf("member PUT: %d", code)
	}
	if code, _ := do(http.MethodPut, "/api/settings/board-limits", adminNoSid, set, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin PUT without a live sign-in: %d", code)
	}
	bad := set
	bad.CallsInFlight = 0
	if code, body := do(http.MethodPut, "/api/settings/board-limits", adminTok, bad, nil); code != http.StatusBadRequest || !strings.Contains(body, "calls_in_flight must be between 1 and 64") {
		t.Fatalf("admin PUT out of range: %d %s", code, body)
	}
	code, body := do(http.MethodGet, "/api/settings/board-limits", adminTok, nil, nil)
	if code != 200 || !strings.Contains(body, `"custom":false`) || !strings.Contains(body, `"calls_per_minute":600`) || !strings.Contains(body, `"runner_refresh_seconds":60`) {
		t.Fatalf("admin GET defaults: %d %s", code, body)
	}
	if code, body := do(http.MethodPut, "/api/settings/board-limits", adminTok, set, nil); code != 200 || !strings.Contains(body, `"custom":true`) || !strings.Contains(body, `"calls_in_flight":12`) {
		t.Fatalf("admin PUT: %d %s", code, body)
	}
	if code, l := internal(); code != 200 || l != set {
		t.Fatalf("runner read after the set: %d %+v", code, l)
	}
}
