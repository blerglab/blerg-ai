package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestShellAuthorityTypedDenialIsCorrelated(t *testing.T) {
	for _, kind := range []string{"typed", "raw", "unauthorized", "wrong-login", "wrong-nonce", "unknown-version", "unknown-reason", "oversized", "extra-field"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("bad request")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "unauthorized" {
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(403)
				if kind == "raw" {
					_, _ = w.Write([]byte("forbidden"))
					return
				}
				reply := map[string]any{"version": 1, "outcome": "deny", "owner_id": req["owner_id"], "login_id": req["login_id"], "nonce": req["nonce"], "reason": "login_unavailable"}
				switch kind {
				case "wrong-login":
					reply["login_id"] = "other"
				case "wrong-nonce":
					reply["nonce"] = "other"
				case "unknown-version":
					reply["version"] = 2
				case "unknown-reason":
					reply["reason"] = "arbitrary"
				case "oversized":
					reply["nonce"] = string(make([]byte, 4096))
				case "extra-field":
					reply["extra"] = "field"
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			defer srv.Close()
			_, err := (shellAuthority{srv.URL, "fixture-key", srv.Client()}).Authorize(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"})
			if err == nil {
				t.Fatal("denial admitted")
			}
			// Only the correlated typed denial is authoritative enough to end custody.
			if got := ownershell.Unknown(err); got != (kind != "typed") {
				t.Fatalf("unknown=%v for %s", got, kind)
			}
		})
	}
}

func TestShellAuthorityLegacyCoreRemainsConservative(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("bad request")
			return
		}
		if _, hasNonce := request["nonce"]; hasNonce {
			http.Error(w, "bad body", 400)
			return
		}
		if request["owner_id"] != "owner" || request["login_id"] != "login" {
			t.Error("wrong actor")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "revision": 1, "enabled": true})
	}))
	defer srv.Close()
	policy, err := (shellAuthority{srv.URL, "fixture-key", srv.Client()}).Authorize(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"})
	if err != nil || !policy.Enabled || calls != 2 {
		t.Fatal("legacy authorization regressed", err, calls)
	}
}

func TestShellPersistentAuthorityRequiresTypedFreshApproval(t *testing.T) {
	for _, kind := range []string{"typed", "legacy", "wrong-login", "wrong-nonce", "expired"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("bad request")
					return
				}
				reply := map[string]any{"version": 1, "revision": 1, "enabled": true}
				if kind != "legacy" {
					reply["authorization_version"] = 2
					reply["outcome"] = "allow"
					reply["owner_id"] = req["owner_id"]
					reply["login_id"] = req["login_id"]
					reply["nonce"] = req["nonce"]
					reply["login_expires_at"] = time.Now().Add(time.Hour).Unix()
					switch kind {
					case "wrong-login":
						reply["login_id"] = "other"
					case "wrong-nonce":
						reply["nonce"] = "stale"
					case "expired":
						reply["login_expires_at"] = time.Now().Add(-time.Second).Unix()
					}
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			defer srv.Close()
			policy, until, err := (shellAuthority{srv.URL, "fixture-key", srv.Client()}).AuthorizePersistent(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"})
			if kind == "typed" {
				if err != nil || !policy.Enabled || !until.After(time.Now()) {
					t.Fatal("fresh approval refused", err)
				}
			} else if !ownershell.Unknown(err) {
				t.Fatal("unverified approval retained custody", err)
			}
		})
	}
}

func TestShellAuthorityDoesNotRedirectCredentialsOrLogin(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true); w.WriteHeader(http.StatusForbidden) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := source.Client()
	original := client.CheckRedirect
	_, _, err := (shellAuthority{source.URL, "fixture-key", client}).AuthorizePersistent(t.Context(), ownershell.Actor{Kind: "human", Owner: "owner", Login: "login"})
	if !ownershell.Unknown(err) {
		t.Fatal("redirect became authoritative", err)
	}
	if reached.Load() {
		t.Fatal("authorization followed redirect with internal credentials and login")
	}
	if original == nil && client.CheckRedirect != nil {
		t.Fatal("shared client mutated")
	}
}
