package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

func TestManagedEngineCredentialSourceUsesRegisteredMode(t *testing.T) {
	t.Cleanup(engineaccess.Register(engineaccess.TokenCodec("fixture-engine", "fixtureAccessTokens")))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(engineaccess.Access{Mode: "fixtureAccessTokens", AccessToken: "fixture-access", AccountID: "fixture-provider", Generation: "fixture-generation", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	access, err := ManagedEngineCredentialSource(server.URL, "fixture-session", "fixture-engine")(t.Context(), false, "")
	if err != nil || access.Mode != "fixtureAccessTokens" || access.AccountID != "fixture-provider" {
		t.Fatal("second native engine mode was refused")
	}
}

func fixtureEngineAccess() EngineCredentialAccess {
	return EngineCredentialAccess{Mode: "chatgptAuthTokens", AccessToken: "fixture-private-access", AccountID: "fixture-provider-account", Generation: "fixture-generation", ExpiresAt: time.Now().Add(time.Hour)}
}
func TestManagedEngineCredentialSourceBindsRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Force              bool   `json:"force"`
			PreviousGeneration string `json:"previous_generation"`
		}
		if r.URL.Path != "/api/session/credentials/access" || r.Header.Get("Authorization") != "Bearer fixture-session" || json.NewDecoder(r.Body).Decode(&request) != nil || !request.Force || request.PreviousGeneration != "fixture-generation" {
			t.Error("request lost current session/generation binding")
		}
		_ = json.NewEncoder(w).Encode(fixtureEngineAccess())
	}))
	defer server.Close()
	access, err := ManagedEngineCredentialSource(server.URL, "fixture-session", "codex")(context.Background(), true, "fixture-generation")
	if err != nil || access.AccessToken != "fixture-private-access" {
		t.Fatal("access delivery failed")
	}
}
func TestManagedEngineCredentialSourceRejectsFailures(t *testing.T) {
	for _, scenario := range []string{"redirect", "provider", "malformed", "wrong-mode", "expired", "oversized", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			followed := false
			dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				followed = true
				_ = json.NewEncoder(w).Encode(fixtureEngineAccess())
			}))
			defer dest.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "redirect":
					http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
				case "provider":
					w.WriteHeader(503)
					_, _ = w.Write([]byte("fixture-private-access"))
				case "malformed":
					_, _ = w.Write([]byte(`{"access_token":"fixture-private-access"}`))
				case "wrong-mode":
					v := fixtureEngineAccess()
					v.Mode = "opaque"
					_ = json.NewEncoder(w).Encode(v)
				case "expired":
					v := fixtureEngineAccess()
					v.ExpiresAt = time.Now().Add(-time.Second)
					_ = json.NewEncoder(w).Encode(v)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", 33<<10)))
				case "deadline":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			access, err := ManagedEngineCredentialSource(server.URL, "fixture-session", "codex")(ctx, false, "")
			if err == nil || access.AccessToken != "" || followed {
				t.Fatal("unsafe credential response accepted")
			}
			if strings.Contains(err.Error(), "fixture-private-access") {
				t.Fatal("provider detail escaped")
			}
		})
	}
}
