package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestUpdateNoticeSettingPersistsAndStopsNetwork(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	svc := deps.Identity.(*identity.Service)
	t.Setenv("BLERG_UPDATE_CHECK", "true")
	enabled, err := svc.UpdateNoticeEnabled(t.Context())
	if err != nil || !enabled {
		t.Fatalf("default %v %v", enabled, err)
	}
	admin := insertCredAccount(t, pool, "update-admin")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v0.5.1","html_url":"https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1"}`))
	}))
	defer server.Close()
	check := svc.ReleaseChecker("0.5.0", "test")
	check.APIBase = server.URL
	check.Check(t.Context())
	if hits.Load() != 1 {
		t.Fatal("expected initial request")
	}
	if err := svc.SetUpdateNotice(t.Context(), admin, false); err != nil {
		t.Fatal(err)
	}
	check.Check(t.Context())
	if hits.Load() != 1 {
		t.Fatal("admin off made a network request")
	}
	deps.LatestRelease = check.Latest
	rec := httptest.NewRecorder()
	api.NewRouter(deps).ServeHTTP(rec, httptest.NewRequest("GET", "/api/site", nil))
	if strings.Contains(rec.Body.String(), "latest_version") {
		t.Fatal("site exposed disabled notice")
	}
	if v, _ := check.Latest(); v != "" {
		t.Fatal("admin off shows notice")
	}
	var on bool
	var by string
	if err := pool.QueryRow(t.Context(), `SELECT enabled, updated_by::text FROM update_notice_settings WHERE id`).Scan(&on, &by); err != nil || on || by != admin {
		t.Fatalf("persisted: %v %s %v", on, by, err)
	}
	if err := svc.SetUpdateNotice(t.Context(), admin, true); err != nil {
		t.Fatal(err)
	}
	check.Check(t.Context())
	if hits.Load() != 2 {
		t.Fatal("reenabling did not restore check")
	}
}

func TestInstallType(t *testing.T) {
	for _, tc := range []struct{ configured, kubernetes, want string }{
		{"", "", "unknown"}, {"", "192.0.2.1", "kubernetes"}, {"desktop", "192.0.2.1", "desktop"}, {"kubernetes", "", "kubernetes"}, {"invalid", "", "unknown"},
	} {
		t.Setenv("BLERG_INSTALL_TYPE", tc.configured)
		t.Setenv("KUBERNETES_SERVICE_HOST", tc.kubernetes)
		if got := api.InstallType(); got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}

func TestUpdateNoticeAdminAPI(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	svc := deps.Identity.(*identity.Service)
	admin := insertCredAccount(t, pool, "release-admin")
	member := insertCredAccount(t, pool, "release-member")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	mint := func(account string, live bool) string {
		t.Helper()
		var tok string
		var err error
		if live {
			tok, err = svc.MintHumanAccessTokenForSession(t.Context(), account, "blerg-core", insertLiveHumanSession(t, pool, account))
		} else {
			tok, err = svc.MintHumanAccessToken(t.Context(), account, "blerg-core")
		}
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	adminTok, memberTok, noSession := mint(admin, true), mint(member, true), mint(admin, false)
	handler := api.NewRouter(deps)
	for _, tc := range []struct {
		method, token, body string
		status              int
	}{
		{"GET", memberTok, "", 403}, {"PUT", memberTok, `{"enabled":false}`, 403},
		{"PUT", noSession, `{"enabled":false}`, 401},
		{"GET", adminTok, "", 200}, {"PUT", adminTok, `{}`, 400},
		{"PUT", adminTok, `{"enabled":"false"}`, 400},
		{"PUT", adminTok, `{"enabled":false,"other":true}`, 400},
		{"PUT", adminTok, `{"enabled":false}`, 200},
	} {
		req := httptest.NewRequest(tc.method, "/api/settings/update-notice", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.body, rec.Code, rec.Body.String())
		}
	}
	enabled, err := svc.UpdateNoticeEnabled(t.Context())
	if err != nil || enabled {
		t.Fatalf("saved off: %v %v", enabled, err)
	}
	t.Setenv("BLERG_UPDATE_CHECK", "false")
	if err := svc.SetUpdateNotice(t.Context(), admin, true); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.UpdateNoticeEnabled(t.Context()); err != nil || !on {
		t.Fatalf("saved setting must override environment: %v %v", on, err)
	}
	if checker := svc.ReleaseChecker("0.5.0", "test"); checker.Disabled || !checker.Allowed(t.Context()) {
		t.Fatal("saved on failed to override environment off in checker")
	}
}

func TestUpdateNoticeDemotedAdminAndPrivateInstall(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	svc := deps.Identity.(*identity.Service)
	admin := insertCredAccount(t, pool, "demoted-release-admin")
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	token, err := svc.MintHumanAccessTokenForSession(t.Context(), admin, "blerg-core", insertLiveHumanSession(t, pool, admin))
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewRouter(deps)
	t.Setenv("BLERG_INSTALL_TYPE", "kubernetes")
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET", "/api/site", "", ""); strings.Contains(rec.Body.String(), "install_type") {
		t.Error("public site disclosed install type")
	}
	if rec := request("GET", "/api/settings/update-notice", "", token); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"install_type":"kubernetes"`) {
		t.Errorf("admin install type: %d %s", rec.Code, rec.Body.String())
	}
	if err := svc.SetUpdateNotice(t.Context(), admin, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE accounts SET role='member' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		if rec := request(method, "/api/settings/update-notice", `{"enabled":true}`, token); rec.Code != 403 {
			t.Errorf("demoted %s: %d", method, rec.Code)
		}
	}
	if enabled, err := svc.UpdateNoticeEnabled(t.Context()); err != nil || enabled {
		t.Fatalf("demoted admin reenabled checks: %v %v", enabled, err)
	}
}
