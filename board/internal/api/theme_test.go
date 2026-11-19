package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/coreauth"
)

func TestAccountThemeRoutesRelayToCore(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me/theme" || r.Header.Get("X-Blerg-Theme-Audience") != "blerg-board" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("wrong relay request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"theme":"colorblind-light"}`))
	}))
	defer core.Close()
	a := auth.New(nil, "")
	a.SetCore(coreauth.NewClient(core.URL))
	app := New(nil, a, nil)
	mux := http.NewServeMux()
	app.Routes(mux)
	for _, method := range []string{"GET", "PUT"} {
		r := httptest.NewRequest(method, "/api/me/theme", strings.NewReader(`{"theme":"colorblind-light"}`))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"theme":"colorblind-light"}` {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
}
