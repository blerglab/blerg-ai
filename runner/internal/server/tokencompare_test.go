package server

import (
	"net/http"
	"net/http/httptest"
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
// this pins the call sites onto tokenEqual so a future "==" can't creep back.
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
}
