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

	"github.com/blerglab/blerg-ai/contracts/sessionstorage"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// Session storage is an administrator's setting: a member can neither read nor set it, a set
// needs a live sign-in and valid values and leaves an audit row, and the runner reads what was
// set with its internal key (and learns when nothing was, so its environment value applies).
func TestSessionStorageIsAnAuditedAdministratorsSettingTheRunnerReads(t *testing.T) {
	t.Setenv(sessionstorage.UploadEnv, "500")
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = childLimitsInternalKey
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	idSvc := deps.Identity.(*identity.Service)
	ctx := context.Background()

	admin := insertCredAccount(t, pool, "storage-admin")
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role = 'admin' WHERE id = $1`, admin); err != nil {
		t.Fatal(err)
	}
	member := insertCredAccount(t, pool, "storage-member")
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
	type internalView struct {
		sessionstorage.Limits
		Set bool `json:"set"`
	}
	internal := func() (int, internalView) {
		t.Helper()
		code, body := do(http.MethodPost, "/internal/session-storage", "", map[string]any{}, map[string]string{"X-Internal-Key": childLimitsInternalKey})
		var v internalView
		_ = json.Unmarshal([]byte(body), &v)
		return code, v
	}
	audits := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_storage_limits_audit WHERE actor = $1`, admin).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if code, v := internal(); code != 200 || v.Set || v.DefaultUploadMB != 500 || v.MaxUploadMB != sessionstorage.DefaultMaxUploadMB {
		t.Fatalf("runner read of the defaults: %d %+v", code, v)
	}
	if code, _ := do(http.MethodPost, "/internal/session-storage", "", map[string]any{}, map[string]string{"X-Internal-Key": "wrong"}); code != http.StatusUnauthorized {
		t.Fatalf("runner read with a wrong key: %d", code)
	}
	if code, _ := do(http.MethodGet, "/api/settings/session-storage", memberTok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("member GET: %d", code)
	}
	set := sessionstorage.Limits{DefaultUploadMB: 800, MaxUploadMB: 3000}
	if code, _ := do(http.MethodPut, "/api/settings/session-storage", memberTok, set, nil); code != http.StatusForbidden {
		t.Fatalf("member PUT: %d", code)
	}
	if code, _ := do(http.MethodPut, "/api/settings/session-storage", adminNoSid, set, nil); code != http.StatusUnauthorized {
		t.Fatalf("admin PUT without a live sign-in: %d", code)
	}
	for _, bad := range []sessionstorage.Limits{{DefaultUploadMB: 0, MaxUploadMB: 10}, {DefaultUploadMB: 900, MaxUploadMB: 800}, {DefaultUploadMB: 1, MaxUploadMB: sessionstorage.MaxConfigMB + 1}} {
		if code, body := do(http.MethodPut, "/api/settings/session-storage", adminTok, bad, nil); code != http.StatusBadRequest {
			t.Fatalf("invalid settings %+v: %d %s", bad, code, body)
		}
	}
	if n := audits(); n != 0 {
		t.Fatalf("refused saves left %d audit rows", n)
	}
	code, body := do(http.MethodGet, "/api/settings/session-storage", adminTok, nil, nil)
	if code != 200 || !strings.Contains(body, `"custom":false`) || !strings.Contains(body, `"default_upload_mb":500`) || !strings.Contains(body, `"step_mb":500`) {
		t.Fatalf("admin GET defaults: %d %s", code, body)
	}
	if code, body := do(http.MethodPut, "/api/settings/session-storage", adminTok, set, nil); code != 200 || !strings.Contains(body, `"custom":true`) || !strings.Contains(body, `"max_upload_mb":3000`) {
		t.Fatalf("admin PUT: %d %s", code, body)
	}
	if code, v := internal(); code != 200 || !v.Set || v.Limits != set {
		t.Fatalf("runner read after the set: %d %+v", code, v)
	}
	var prevDefault, prevMax, newDefault, newMax int64
	if err := pool.QueryRow(ctx, `SELECT previous_default_mb, previous_max_mb, default_upload_mb, max_upload_mb FROM session_storage_limits_audit WHERE actor = $1`, admin).
		Scan(&prevDefault, &prevMax, &newDefault, &newMax); err != nil {
		t.Fatal(err)
	}
	if prevDefault != 500 || prevMax != sessionstorage.DefaultMaxUploadMB || newDefault != 800 || newMax != 3000 {
		t.Fatalf("audit row: %d/%d -> %d/%d", prevDefault, prevMax, newDefault, newMax)
	}
}

// The runner checks every person a broker names (#231 rereview): exists, active, admin from the
// account's real role. A disabled admin is neither active nor admin.
func TestInternalAccountStandingIsTheAccountsRealRole(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = childLimitsInternalKey
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	admin := insertCredAccount(t, pool, "standing-admin")
	member := insertCredAccount(t, pool, "standing-member")
	disabled := insertCredAccount(t, pool, "standing-disabled")
	if _, err := pool.Exec(ctx, `UPDATE accounts SET role = 'admin' WHERE id = $1 OR id = $2`, admin, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE accounts SET disabled_at = now() WHERE id = $1`, disabled); err != nil {
		t.Fatal(err)
	}
	ask := func(account, key string) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"account": account})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/accounts/standing", bytes.NewReader(raw))
		req.Header.Set("X-Internal-Key", key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(b))
	}
	for _, tc := range []struct{ account, want string }{
		{admin, `{"exists":true,"active":true,"admin":true}`},
		{member, `{"exists":true,"active":true,"admin":false}`},
		{disabled, `{"exists":true,"active":false,"admin":false}`},
		{"00000000-0000-4000-8000-000000000000", `{"exists":false,"active":false,"admin":false}`},
		{"not-a-uuid", `{"exists":false,"active":false,"admin":false}`},
	} {
		if code, body := ask(tc.account, childLimitsInternalKey); code != 200 || body != tc.want {
			t.Errorf("%s: %d %s, want %s", tc.account, code, body, tc.want)
		}
	}
	if code, _ := ask(admin, "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong key = %d", code)
	}
}
