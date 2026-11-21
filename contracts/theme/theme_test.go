package theme

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVocabulary(t *testing.T) {
	for _, id := range []string{"system", "dark", "light", "colorblind-dark", "colorblind-light", "high-contrast",
		"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		if !Valid(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"", "Dark", "custom"} {
		if Valid(id) {
			t.Fatal(id)
		}
	}
}
func TestProxyForwardsOnlyThemeAndFixedAudience(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "/api/me/theme" || r.Header.Get("X-Blerg-Theme-Audience") != "blerg-board" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("wrong proxy request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"theme":"high-contrast"}`))
	}))
	defer upstream.Close()
	r := httptest.NewRequest("PUT", "/api/me/theme?account=other", strings.NewReader(`{"theme":"high-contrast"}`))
	r.Header.Set("Authorization", "Bearer fixture")
	r.Header.Set("X-Blerg-Theme-Audience", "untrusted")
	w := httptest.NewRecorder()
	Proxy(upstream.URL, "blerg-board")(w, r)
	if w.Code != 200 || w.Body.String() != `{"theme":"high-contrast"}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}
func TestProxyRefusesRedirectsAndOversizedResponses(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	for _, oversized := range []bool{false, true} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if oversized {
				_, _ = w.Write([]byte(strings.Repeat("x", 2049)))
				return
			}
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		w := httptest.NewRecorder()
		Proxy(upstream.URL, "blerg-runner")(w, httptest.NewRequest("GET", "/api/me/theme", nil))
		if w.Code != 503 {
			t.Errorf("response: %d", w.Code)
		}
		upstream.Close()
	}
}
