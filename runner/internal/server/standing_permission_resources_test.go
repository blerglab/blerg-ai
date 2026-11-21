package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestStandingResourceDiscoveryRestartRefusalsAreUnavailable(t *testing.T) {
	for _, kind := range []string{"board", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body coreListRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !body.Standing {
					t.Error("standing resource discovery omitted standing authorization")
				}
				http.Error(w, "not found", http.StatusNotFound)
			}))
			defer srv.Close()
			c := HTTPMCPCoreClient{BaseURL: srv.URL, InternalKey: "fixture-key", HTTP: srv.Client()}
			proof := mcpgw.Proof{AccountID: "owner", SessionID: "original-login"}
			var err error
			if kind == "board" {
				_, err = c.listSessionBoards(t.Context(), proof, "board", true)
			} else {
				_, err = c.listConnections(t.Context(), proof, true)
			}
			if err == nil {
				t.Fatal("restart refusal authorized discovery")
			}
			if errors.Is(err, ErrMCPProofInvalid) || errors.Is(err, errStandingRevoked) {
				t.Fatalf("restart refusal classified as permanent: %v", err)
			}
		})
	}
}

func TestStandingPermissionsVerifyOwnerlessBoardScope(t *testing.T) {
	fx, _ := newPermFx(t)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/credentials/list" {
			_, _ = w.Write([]byte(`{"engines":[]}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["board_id"] != "legacy-board" {
			t.Error("Permissions did not verify the specific board's owner")
			_, _ = w.Write([]byte(`{"boards":[]}`))
			return
		}
		w.Header().Set("X-Blerg-Standing-Cause", "board_owner_missing")
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer core.Close()
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-key", HTTP: core.Client()}})
	if got := fx.api.permissionResourceAuthority(t.Context(), map[string]string{}, "owner", "original-chain", "board", "legacy-board"); got != "board_owner_missing" {
		t.Fatalf("ownerless legacy board presented as %s", got)
	}
}

func TestStandingResourceDiscoveryOldCoreEmptyListIsUnavailable(t *testing.T) {
	fx, _ := newPermFx(t)
	var classified atomic.Bool
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/credentials/list" {
			_, _ = w.Write([]byte(`{"engines":[]}`))
			return
		}
		if classified.Load() {
			w.Header().Set("X-Blerg-Standing-State", "active")
		}
		_, _ = w.Write([]byte(`{"boards":[]}`))
	}))
	defer core.Close()
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-key", HTTP: core.Client()}})
	if got := fx.api.permissionResourceAuthority(t.Context(), map[string]string{}, "owner", "chain", "board", "legacy-board"); got != "unavailable" {
		t.Fatalf("old Core's unclassified empty list became %s", got)
	}
	classified.Store(true)
	if got := fx.api.permissionResourceAuthority(t.Context(), map[string]string{}, "owner", "chain", "board", "legacy-board"); got != "revoked" {
		t.Fatalf("verified scoped absence became %s", got)
	}
}
