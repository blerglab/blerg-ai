package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
)

func TestSessionPluginsCoreTransportBoundedPrivateAndNoRedirect(t *testing.T) {
	const key = "fixture-internal-key"
	const sid = "fixture-human-session"
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	for _, mode := range []string{"ok", "redirect", "oversize", "bad-engine", "bad-plugin", "bad-policy", "private-error"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Internal-Key") != key {
					t.Error("missinginternalkey")
				}
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("badrequest")
					return
				}
				if r.URL.Path == "/internal/plugins/add" {
					if string(body["session_id"]) != `"`+sid+`"` || len(body["token_id"]) != 0 {
						t.Error("wrong humanproof")
					}
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				case "oversize":
					_, _ = fmt.Fprint(w, strings.Repeat("x", maxPluginsResponse+1))
					return
				case "private-error":
					http.Error(w, key+sid, http.StatusForbidden)
					return
				}
				engine := "claude"
				plugin := "release-notes"
				market := pluginspec.DefaultMarketplaces
				if mode == "bad-engine" {
					engine = "codex"
				}
				if mode == "bad-plugin" {
					plugin = "../bad"
				}
				if mode == "bad-policy" {
					market = "https://private.example/token"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"engine": engine, "plugins": []pluginspec.Entry{{Marketplace: market, Plugin: plugin}}, "allowed_marketplaces": []string{market}})
			}))
			defer srv.Close()
			c := &httpSessionPluginCore{url: srv.URL, key: key, client: coreHTTPClient(nil)}
			_, err := c.Catalog(context.Background(), "owner", coreProof{SessionID: sid}, "claude")
			if mode == "ok" && err != nil {
				t.Fatal(err)
			}
			if mode != "ok" && (err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), sid)) {
				t.Fatalf("unsafe error %v", err)
			}
			if mode == "ok" {
				if err := c.Add(context.Background(), "owner", coreProof{SessionID: sid}, "claude", pluginspec.Entry{Marketplace: pluginspec.DefaultMarketplaces, Plugin: "release-notes"}); err != nil {
					t.Fatal(err)
				}
				if err := c.Add(context.Background(), "owner", coreProof{TokenID: "agent"}, "claude", pluginspec.Entry{}); err == nil {
					t.Fatal("agent proof accepted")
				}
			}
		})
	}
	if reached {
		t.Fatal("followedcredentialredirect")
	}
}
