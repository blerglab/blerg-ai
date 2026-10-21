package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The runner app reads the Board URL from its own origin; the runner asks core's public /api/site
// server-side (core sends no CORS headers), validates it and caches it.
func TestSiteBoardReadsCoreSiteServerSide(t *testing.T) {
	var calls atomic.Int64
	board := "https://boards.example.com/apps/board/"
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/site" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected core request %s", r.URL.Path)
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"board_url": board, "version": "dev"})
	}))
	defer core.Close()
	api := NewAPI(NewHub(), nil, "fixture-daemon-key", nil, "")
	api.coreURL = core.URL
	now := time.Unix(1_000_000, 0)
	api.siteBoard.now = func() time.Time { return now }
	token := enableBrowserAuth(t, api)("session.start")
	get := func(auth bool) (int, string) {
		r := httptest.NewRequest(http.MethodGet, "/api/site-board", nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.HandleGetSiteBoard(w, r)
		var out map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out["board_url"]
	}
	if code, _ := get(false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", code)
	}
	for range 3 {
		if code, got := get(true); code != http.StatusOK || got != "https://boards.example.com/apps/board" {
			t.Fatalf("site board = %d %q", code, got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("core asked %d times, want once while cached", calls.Load())
	}
	// An invalid URL is never handed to the browser, and the failure is cached briefly.
	board = "javascript:alert(1)"
	now = now.Add(siteBoardTTL + time.Second)
	if _, got := get(true); got != "" {
		t.Fatalf("invalid board URL passed through: %q", got)
	}
	get(true)
	if calls.Load() != 2 {
		t.Fatalf("a failed lookup was not cached: %d calls", calls.Load())
	}
	board = "https://user:pw@boards.example.com"
	now = now.Add(siteBoardRetry + time.Second)
	if _, got := get(true); got != "" || calls.Load() != 3 {
		t.Fatalf("credentialed URL passed through, or the failure was cached too long: %q %d", got, calls.Load())
	}
}

func TestSiteBoardWithoutCoreIsEmpty(t *testing.T) {
	api := NewAPI(NewHub(), nil, "fixture-daemon-key", nil, "")
	token := enableBrowserAuth(t, api)("session.start")
	r := httptest.NewRequest(http.MethodGet, "/api/site-board", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.HandleGetSiteBoard(w, r)
	if w.Code != http.StatusOK || w.Body.String() == "" {
		t.Fatalf("no core: %d %s", w.Code, w.Body.String())
	}
}
