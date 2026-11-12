package mcpconn_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

func TestSessionOAuthAuthorityTransport(t *testing.T) {
	const key = "synthetic-authority-key"
	expected := mcpconn.SessionOAuthAuthorization{LaunchGeneration: 7, Definition: mcpconn.SessionServerDefinition{ID: sessA, Version: 2, Source: "settings"}}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-Internal-Key") != key {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/internal/mcp/session-oauth/resolve":
			var in mcpconn.SessionOAuthRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.AccountID != liveSid {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(expected)
		case "/internal/mcp/session-oauth/validate":
			var in mcpconn.SessionOAuthAuthorization
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.LaunchGeneration != 7 {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"live": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client := &mcpconn.HTTPSessionOAuthAuthority{BaseURL: server.URL, InternalKey: key}
	got, err := client.Resolve(context.Background(), mcpconn.SessionOAuthRequest{AccountID: liveSid})
	if err != nil || got.LaunchGeneration != 7 {
		t.Fatal("authority resolution failed")
	}
	if err := client.Validate(context.Background(), got); err != nil {
		t.Fatal("authority validation failed")
	}
	client.InternalKey = "wrong"
	if _, err := client.Resolve(context.Background(), mcpconn.SessionOAuthRequest{AccountID: liveSid}); err == nil {
		t.Fatal("wrong internal key accepted")
	}
	if requests != 3 {
		t.Fatal("unexpected authority calls")
	}
}

func TestSessionOAuthAuthorityRefusesPublicPlaintext(t *testing.T) {
	for _, host := range []string{"authority.example.com", "runner", "runner.svc.attacker.example.com", "runner.svc.cluster.local.attacker.example.com"} {
		client := &mcpconn.HTTPSessionOAuthAuthority{BaseURL: "http://" + host, InternalKey: "synthetic-key"}
		// Cancellation is a tripwire: refusal must be configuration, before network IO.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Resolve(ctx, mcpconn.SessionOAuthRequest{})
		if mcpconn.FailureCause(err) != "authority_configuration" {
			t.Fatalf("plaintext authority accepted: %s", host)
		}
	}
}
