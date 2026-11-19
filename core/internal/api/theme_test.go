package api_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/theme"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

func TestAccountThemeAcrossApps(t *testing.T) {
	deps, pool := newTestDeps(t, nil, nil)
	id := insertCredAccount(t, pool, "theme-reader")
	other := insertCredAccount(t, pool, "other-theme-reader")
	svc := deps.Identity.(*identity.Service)
	tokens := map[string]string{}
	for _, account := range []string{id, other} {
		refresh, err := svc.IssueRefreshToken(t.Context(), account, "fixture", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, audience := range []string{"blerg-core", "blerg-runner", "blerg-board"} {
			token, nextRefresh, err := svc.RefreshAccessToken(t.Context(), refresh, audience)
			if err != nil {
				t.Fatal(err)
			}
			refresh = nextRefresh
			tokens[account+audience] = token
		}
	}
	router := api.NewRouter(deps)
	upstream := httptest.NewServer(router)
	defer upstream.Close()
	call := func(method, body, account, audience string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/me/theme", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens[account+audience])
		r.Header.Set("X-Blerg-Theme-Audience", audience)
		w := httptest.NewRecorder()
		if audience == "blerg-board" || audience == "blerg-runner" {
			theme.Proxy(upstream.URL, audience)(w, r)
		} else {
			router.ServeHTTP(w, r)
		}
		return w
	}
	// An agent acting for this account cannot read or change its human preference.
	for _, audience := range []string{"blerg-core", "blerg-board", "blerg-runner"} {
		agent, err := svc.MintAgentToken(t.Context(), identity.AgentTokenInput{Sub: id, Aud: audience, OnBehalfOf: id, Caps: []string{"card.read"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{"GET", "PUT"} {
			request := httptest.NewRequest(method, "/api/me/theme", strings.NewReader(`{"theme":"dark"}`))
			request.Header.Set("Authorization", "Bearer "+agent)
			request.Header.Set("X-Blerg-Theme-Audience", audience)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != 403 {
				t.Fatalf("agent %s %s: %d", audience, method, response.Code)
			}
		}
	}

	for _, theme := range []string{"system", "dark", "light", "colorblind-dark", "colorblind-light", "high-contrast",
		"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night"} {
		body := `{"theme":"` + theme + `"}`
		if w := call("PUT", body, id, "blerg-runner"); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body.String())
		}
		for _, audience := range []string{"blerg-core", "blerg-board"} {
			if w := call("GET", "", id, audience); w.Code != 200 || strings.TrimSpace(w.Body.String()) != body {
				t.Fatalf("read: %d %s", w.Code, w.Body.String())
			}
		}
	}
	if w := call("GET", "", other, "blerg-board"); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"theme":""}` {
		t.Fatalf("isolation: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"theme":"unknown"}`, `{"theme":"dark","account_id":"other"}`, `{"theme":null}`, `{"theme":"dark"} {}`, `{}`} {
		if w := call("PUT", body, id, "blerg-core"); w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
	}
	if w := call("GET", "", id, "untrusted"); w.Code != 401 {
		t.Fatalf("audience: %d", w.Code)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE human_sessions SET revoked_at=now() WHERE account_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "", id, "blerg-board"); w.Code != 403 && w.Code != 401 {
		t.Fatalf("revoked: %d", w.Code)
	}
}
