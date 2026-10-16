package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// GET /api/daemon/whoami checks a daemon token without touching the live connection: 200 JSON
// for the master token or a current runtime token, 401 for anything else, never echoing it.
func TestDaemonWhoami(t *testing.T) {
	api := NewAPI(NewHub(), nil, "master-token-1234567890", nil, "")
	call := func(header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/daemon/whoami", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		api.HandleDaemonWhoami(w, r)
		return w
	}
	ok := call("Bearer master-token-1234567890")
	if ok.Code != http.StatusOK {
		t.Fatalf("master token: %d %s", ok.Code, ok.Body.String())
	}
	if ct := ok.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(ok.Body.Bytes(), &body); err != nil || body["ok"] != true || body["mode"] != "daemon" {
		t.Errorf("body = %s, want ok true, mode daemon", ok.Body.String())
	}
	if strings.Contains(ok.Body.String(), "master-token") {
		t.Fatal("whoami echoes the token")
	}
	for _, h := range []string{"", "Bearer wrong-token-1234567890", "Bearer ", "master-token-1234567890", "Basic master-token-1234567890"} {
		got := call(h)
		if got.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d, want 401", h, got.Code)
		}
		if strings.Contains(got.Body.String(), "token-1234567890") {
			t.Errorf("Authorization %q: the 401 echoes the token", h)
		}
	}
}

// A pod's runtime token is a daemon token too (it opens /ws/daemon), so whoami accepts it, and
// refuses it once the launch it belongs to is replaced.
func TestDaemonWhoamiRuntimeToken(t *testing.T) {
	api, _, host, pool := spawnTokenFixture(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-000000000a02"
	if err := db.InsertClusterSession(ctx, pool, sid, host.ID, "starting", "/repos/fixture", "fixture", "", ""); err != nil {
		t.Fatal(err)
	}
	runtime, message, err := db.MintRuntimeLaunch(ctx, pool, sid, "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	call := func(tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/daemon/whoami", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		api.HandleDaemonWhoami(w, r)
		return w
	}
	got := call(runtime)
	var body map[string]any
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &body) != nil || body["mode"] != "runtime" {
		t.Fatalf("runtime token: %d %s, want 200 mode runtime", got.Code, got.Body.String())
	}
	if strings.Contains(got.Body.String(), runtime) {
		t.Fatal("whoami echoes the runtime token")
	}
	// The session's messaging token is not a daemon token.
	if got := call(message); got.Code != http.StatusUnauthorized {
		t.Errorf("messaging token: %d, want 401", got.Code)
	}
	if _, _, err := db.MintRuntimeLaunch(ctx, pool, sid, "", 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := call(runtime); got.Code != http.StatusUnauthorized {
		t.Errorf("replaced runtime token: %d, want 401", got.Code)
	}
}
