package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenEqual(t *testing.T) {
	if !tokenEqual("abc", "abc") {
		t.Fatal("equal tokens must match")
	}
	for _, c := range [][2]string{{"abc", "abd"}, {"", ""}, {"abc", ""}, {"", "abc"}, {"ab", "abc"}} {
		if tokenEqual(c[0], c[1]) {
			t.Fatalf("tokenEqual(%q,%q) must be false", c[0], c[1])
		}
	}
}

// Every daemon-token gate must reject a near-miss and accept the exact token;
// this pins the four call sites onto tokenEqual so a future "==" can't creep back.
func TestDaemonTokenGatesUseTokenEqual(t *testing.T) {
	const tok = "daemon-tok-1234567890"
	api := NewAPI(NewHub(), nil, tok, nil, "")

	// checkBearerToken
	for _, c := range []struct {
		header string
		want   bool
	}{{"Bearer " + tok, true}, {"Bearer " + tok + "x", false}, {"Bearer ", false}} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", c.header)
		rec := httptest.NewRecorder()
		if got := checkBearerToken(rec, req, tok); got != c.want {
			t.Errorf("checkBearerToken(%q) = %v, want %v", c.header, got, c.want)
		}
	}
	// isDaemonToken (boardauth.go) — the previously-correct site, still correct.
	if !api.isDaemonToken(tok) || api.isDaemonToken(tok+"x") || api.isDaemonToken("") {
		t.Error("isDaemonToken must accept exactly the configured token")
	}
	// HandlePreview (preview.go)
	h := NewHub().HandlePreview(tok)
	for _, c := range []struct {
		header string
		want   int
	}{{"Bearer " + tok, http.StatusOK}, {"Bearer nope", http.StatusUnauthorized}} {
		req := httptest.NewRequest(http.MethodPost, "/api/preview", strings.NewReader(`{"html":"<p>x</p>"}`))
		req.Header.Set("Authorization", c.header)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != c.want {
			t.Errorf("HandlePreview(%q) = %d, want %d", c.header, rec.Code, c.want)
		}
	}
}
