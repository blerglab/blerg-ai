package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestStandingPermissionsRechecksResourceAccess(t *testing.T) {
	for _, state := range []string{"revoked", "unavailable", "board_owner_missing"} {
		t.Run(state, func(t *testing.T) {
			fx, _ := newPermFx(t)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/credentials/list" {
					_ = json.NewEncoder(w).Encode(map[string]any{"engines": []string{}})
					return
				}
				var p map[string]any
				if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
					t.Error(err)
				}
				if p["standing"] != true {
					t.Error("standing resource check omitted flag")
				}
				if state == "board_owner_missing" {
					w.Header().Set("X-Blerg-Standing-State", "unavailable")
					w.Header().Set("X-Blerg-Standing-Cause", "board_owner_missing")
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if state == "unavailable" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("X-Blerg-Standing-State", "active") // scoped ownership was verified
				_ = json.NewEncoder(w).Encode(map[string]any{"boards": []any{}})
			}))
			defer core.Close()
			fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-key"}})
			if _, err := db.InsertProposalGrant(t.Context(), fx.pool, db.ProposalGrant{SessionID: fx.sess, AccountID: topicOwner, Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: "old-login", CreatedVia: "panel", Level: db.BoardLevelRead}); err != nil {
				t.Fatal(err)
			}
			rec := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/permissions", fx.token(topicOwner, "human"), nil)
			var view struct {
				Boards []struct {
					Active   bool
					Inactive string
				}
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 200 || len(view.Boards) != 1 || view.Boards[0].Active || view.Boards[0].Inactive != state {
				t.Fatalf("status=%d boards=%+v want %s", rec.Code, view.Boards, state)
			}
			if state == "board_owner_missing" {
				var stored, cause string
				if err := fx.pool.QueryRow(t.Context(), `SELECT payload::jsonb->>'state',payload::jsonb->>'cause' FROM agent_events WHERE session_id=$1 AND kind='grant_authority' ORDER BY seq DESC LIMIT 1`, fx.sess).Scan(&stored, &cause); err != nil {
					t.Fatal(err)
				}
				if stored != "unavailable" || cause != "board_owner_missing" {
					t.Fatalf("ownerless board stored state=%s cause=%s want unavailable/board_owner_missing", stored, cause)
				}
			}

		})
	}
}

func TestStandingPermissionsMissingProvenanceIsExpired(t *testing.T) {
	api := &API{hub: NewHub()}
	for _, proof := range []struct{ account, sid string }{{"owner", ""}, {"", "original-login"}} {
		if state := api.permissionAuthority(t.Context(), map[string]string{}, proof.account, proof.sid); state != "revoked" {
			t.Fatalf("missing provenance state=%q want revoked", state)
		}
	}
}

func TestStandingPermissionsProviderScopeStaysExact(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "expired"}[live], func(t *testing.T) {
			fx, _ := newPermFx(t)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/credentials/list" {
					t.Errorf("provider scope used account discovery: %s", r.URL.Path)
				}
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)
				if p["standing"] == true || p["session_id"] != "bound-login" {
					t.Errorf("provider proof widened: %+v", p)
				}
				if !live {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(`{"engines":[]}`))
			}))
			defer core.Close()
			fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &HTTPMCPCoreClient{BaseURL: core.URL, InternalKey: "fixture-key"}})
			got := fx.api.permissionScopedMCPAuthority(t.Context(), topicOwner, &db.MCPSessionScope{HumanSessionID: "bound-login"})
			want := "active"
			if !live {
				want = "revoked"
			}
			if got != want {
				t.Fatalf("state=%s want %s", got, want)
			}
		})
	}
}
