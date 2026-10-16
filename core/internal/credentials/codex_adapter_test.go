package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureJWT() string {
	raw := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account","chatgpt_plan_type":"plus"}}`, time.Now().Add(time.Hour).Unix())
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".fixture"
}
func TestCodexAdapterPinnedDeviceProtocolAndRefresh(t *testing.T) {
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" {
				t.Error("wrong pinned public client ID")
			}
			_, _ = fmt.Fprint(w, `{"device_auth_id":"fixture-device","user_code":"ABCD-EFGH","interval":"2"}`)
		case "/api/accounts/deviceauth/token":
			_, _ = fmt.Fprint(w, `{"authorization_code":"fixture-code","code_verifier":"fixture-verifier"}`)
		case "/oauth/token":
			var grantType string
			if r.Header.Get("Content-Type") == "application/json" {
				var in map[string]string
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
				}
				grantType = in["grant_type"]
				if grantType != "refresh_token" || in["client_id"] != codexPublicClientID || in["refresh_token"] != "fixture-refresh" {
					t.Error("wrong pinned native refresh JSON")
				}
			} else {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				grantType = r.Form.Get("grant_type")
				if r.Form.Get("client_id") != codexPublicClientID || grantType != "authorization_code" || r.Form.Get("code_verifier") != "fixture-verifier" || !strings.HasSuffix(r.Form.Get("redirect_uri"), "/deviceauth/callback") {
					t.Error("wrong pinned native PKCE form")
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fixtureJWT(), "refresh_token": "fixture-refresh", "id_token": fixtureJWT(), "expires_in": 3600})
		default:
			t.Error("unexpected route")
		}
	}))
	defer srv.Close()
	a := newCodexAdapter()
	a.issuer = srv.URL
	grant, err := a.StartDevice(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.PollDevice(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccountID != "fixture-account" || token.RefreshToken != "fixture-refresh" {
		t.Fatal("incomplete provider result")
	}
	if _, err = a.Refresh(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 4 {
		t.Fatalf("provider calls %v", paths)
	}
}
func TestCodexAdapterRejectsRedirectAndOversizedOrMalformedProviderOutput(t *testing.T) {
	for _, mode := range []string{"redirect", "large", "malformed", "expired", "unsafe-code"} {
		t.Run(mode, func(t *testing.T) {
			followed := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "large":
					_, _ = fmt.Fprint(w, strings.Repeat("x", (64<<10)+1))
				case "malformed":
					_, _ = fmt.Fprint(w, `{"device_auth_id":"private-provider-error"}`)
				case "expired":
					_, _ = fmt.Fprint(w, `{"access_token":"private-provider-error","refresh_token":"fixture"}`)
				case "unsafe-code":
					_, _ = fmt.Fprint(w, `{"device_auth_id":"fixture","user_code":"<script>","interval":"1"}`)
				}
			}))
			defer srv.Close()
			a := newCodexAdapter()
			a.issuer = srv.URL
			_, err := a.StartDevice(context.Background())
			if err == nil || strings.Contains(err.Error(), "private-provider-error") || followed {
				t.Fatalf("unsafe output: %v, redirect followed %v", err, followed)
			}
		})
	}
}

func TestCodexAdapterRejectsInvalidTokenMaterial(t *testing.T) {
	for _, mode := range []string{"opaque-access", "expired-access", "overflow-expiry", "invalid-account"} {
		t.Run(mode, func(t *testing.T) {
			access, id, expiry := fixtureJWT(), fixtureJWT(), int64(3600)
			switch mode {
			case "opaque-access":
				access = "fixture-private-opaque"
			case "expired-access":
				raw := fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Minute).Unix())
				access = "e30." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".fixture"
			case "overflow-expiry":
				expiry = 9223372036854775807
			case "invalid-account":
				raw := fmt.Sprintf(`{"exp":%d,"https://api.openai.com/auth":{"chatgpt_account_id":"bad\r\naccount"}}`, time.Now().Add(time.Hour).Unix())
				id = "e30." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".fixture"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "id_token": id, "refresh_token": "fixture-private-refresh", "expires_in": expiry})
			}))
			defer srv.Close()
			a := newCodexAdapter()
			a.issuer = srv.URL
			_, err := a.Refresh(context.Background(), ManagedToken{RefreshToken: "fixture-private-refresh", AccountID: "fixture-account"})
			if err == nil || strings.Contains(err.Error(), "fixture-private") {
				t.Fatal("malformed token material was accepted or exposed")
			}
		})
	}
}

func TestCodexManagedAccessWireCompatibility(t *testing.T) {
	access := managedAccess(newCodexAdapter(), ManagedToken{
		AccessToken: "fixture-access", RefreshToken: "fixture-refresh",
		AccountID: "fixture-account", PlanType: "plus",
		ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}, "fixture-generation")
	raw, err := json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"mode":"chatgptAuthTokens","access_token":"fixture-access","chatgpt_account_id":"fixture-account","plan_type":"plus","generation":"fixture-generation","expires_at":"2030-01-02T03:04:05Z"}`
	if string(raw) != want {
		t.Fatal("Codex access-only wire contract changed")
	}
}
