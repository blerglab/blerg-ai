package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestWhatsNewReadsUntilDismissAndIsolatesAccounts(t *testing.T) {
	t.Setenv("BLERG_RELEASE", "0.6.0")
	deps, pool := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-key"
	id := insertCredAccount(t, pool, "reader")
	svc := deps.Identity.(*identity.Service)
	if _, err := svc.IssueRefreshToken(t.Context(), id, "fixture", ""); err != nil {
		t.Fatal(err)
	}
	var sid string
	if err := pool.QueryRow(t.Context(), "SELECT id::text FROM human_sessions WHERE account_id=$1", id).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	router := api.NewRouter(deps)
	call := func(account, session, version string, dismiss bool) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"account_id": account, "session_id": session, "release": version, "dismiss": dismiss})
		r := httptest.NewRequest("POST", "/internal/accounts/whats-new", strings.NewReader(string(body)))
		r.Header.Set("X-Internal-Key", "fixture-key")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var out map[string]any
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, out
	}
	// The first app visit is quiet for an account with no baseline.
	if _, err := pool.Exec(t.Context(), "UPDATE accounts SET whats_new_seen=NULL WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if code, got := call(id, sid, "0.6.0", false); code != 200 || got["pending"] != false {
		t.Fatalf("first visit: %d %v", code, got)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE accounts SET whats_new_seen='0.5.4' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	// Show, reload, another device, and lost reads cannot acknowledge an upgrade.
	for range 3 {
		if code, got := call(id, sid, "0.6.0", false); code != 200 || got["pending"] != true {
			t.Fatalf("read: %d %v", code, got)
		}
	}
	if code, _ := call(id, sid, "0.5.4", true); code != 409 {
		t.Fatalf("old release dismiss: %d", code)
	}
	other := insertCredAccount(t, pool, "other-reader")
	if code, _ := call(other, sid, "0.6.0", true); code != 403 {
		t.Fatalf("wrong account proof: %d", code)
	}
	if code, got := call(id, sid, "0.6.0", true); code != 200 || got["pending"] != false {
		t.Fatalf("dismiss: %d %v", code, got)
	}
	if code, got := call(id, sid, "0.6.0", false); code != 200 || got["pending"] != false {
		t.Fatalf("after dismiss: %d %v", code, got)
	}
	t.Setenv("BLERG_RELEASE", "0.5.4")
	if code, _ := call(id, sid, "0.5.4", true); code != 200 {
		t.Fatalf("downgrade: %d", code)
	}
	var seen string
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE id=$1", id).Scan(&seen); err != nil || seen != "0.6.0" {
		t.Fatalf("downgrade baseline: %q %v", seen, err)
	}
}

func TestWhatsNewInternalRequiresKeyAndLiveHumanSession(t *testing.T) {
	deps, _ := newTestDeps(t, nil, nil)
	deps.InternalKey = "fixture-key"
	router := api.NewRouter(deps)
	for _, key := range []string{"", "wrong", "fixture-key"} {
		r := httptest.NewRequest("POST", "/internal/accounts/whats-new", strings.NewReader("{}"))
		r.Header.Set("X-Internal-Key", key)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 400 {
			t.Fatalf("key %q: %d", key, w.Code)
		}
	}
}

func TestWhatsNewFirstLoginAfterSeveralUpgradesIsQuiet(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	if _, err := pool.Exec(t.Context(), "INSERT INTO app_release_baseline(singleton,version) VALUES(true,'0.5.4')"); err != nil {
		t.Fatal(err)
	}
	id := insertCredAccount(t, pool, "late-reader")
	if _, err := pool.Exec(t.Context(), "UPDATE app_release_baseline SET version='0.6.0'"); err != nil {
		t.Fatal(err)
	}
	svc := deps.Identity.(*identity.Service)
	if _, err := svc.IssueRefreshToken(t.Context(), id, "fixture", ""); err != nil {
		t.Fatal(err)
	}
	var seen string
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE id=$1", id).Scan(&seen); err != nil || seen != "0.6.0" {
		t.Fatalf("first login baseline: %q %v", seen, err)
	}
	// Returning logins must leave an unread upgrade unread.
	if _, err := pool.Exec(t.Context(), "UPDATE app_release_baseline SET version='0.7.0'"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.IssueRefreshToken(t.Context(), id, "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT whats_new_seen FROM accounts WHERE id=$1", id).Scan(&seen); err != nil || seen != "0.6.0" {
		t.Fatalf("returning login baseline: %q %v", seen, err)
	}
}
