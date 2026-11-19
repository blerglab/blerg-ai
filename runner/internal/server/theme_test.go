package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountThemeRoutesRelayToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me/theme" || r.Header.Get("X-Blerg-Theme-Audience") != "blerg-runner" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("wrong relay request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"theme":"high-contrast"}`))
	}))
	defer core.Close()
	app := NewAPI(NewHub(), nil, "", nil, "")
	app.SetCoreCredentials(core.URL, "")
	mux := http.NewServeMux()
	app.RegisterThemeRoutes(mux)
	for _, method := range []string{"GET", "PUT"} {
		r := httptest.NewRequest(method, "/api/me/theme", strings.NewReader(`{"theme":"high-contrast"}`))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"theme":"high-contrast"}` {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
}
