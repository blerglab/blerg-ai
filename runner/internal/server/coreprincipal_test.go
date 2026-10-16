package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyCorePrincipalRequiresCurrentBoundProof(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"live-without-credentials", `{"engines":[]}`, 200, true},
		{"live-with-credentials", `{"engines":["codex"]}`, 200, true},
		{"revoked", "fixture-private-value", 404, false},
		{"unavailable", "fixture-private-value", 503, false},
		{"malformed", `{}`, 200, false},
		{"oversized", strings.Repeat("x", 20<<10), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req internalCredentialListRequest
				if r.URL.Path != "/internal/credentials/list" || r.Header.Get("X-Internal-Key") != "fixture-component-key" || json.NewDecoder(r.Body).Decode(&req) != nil || req.AccountID != "fixture-account" || req.SessionID != "fixture-proof" || req.TokenID != "" {
					t.Error("principal check lost its account, proof or component binding")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			err := verifyCorePrincipal(context.Background(), nil, srv.URL, "fixture-component-key", "fixture-account", coreProof{SessionID: "fixture-proof"})
			if (err == nil) != tc.ok {
				t.Fatalf("verified=%v want=%v", err == nil, tc.ok)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-private-value") {
				t.Fatal("principal check disclosed a core response")
			}
		})
	}
}

func TestVerifyCorePrincipalRefusesRedirectAndMissingProof(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; _, _ = w.Write([]byte(`{"engines":[]}`)) }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if err := verifyCorePrincipal(context.Background(), nil, source.URL, "fixture-key", "fixture-account", coreProof{SessionID: "fixture-proof"}); err == nil || called {
		t.Fatal("principal check followed a credential-bearing redirect")
	}
	for _, proof := range []coreProof{{}, {TokenID: "token", SessionID: "session"}} {
		if err := verifyCorePrincipal(context.Background(), nil, dest.URL, "fixture-key", "fixture-account", proof); err == nil || called {
			t.Fatal("principal check sent an unbound request")
		}
	}
}
