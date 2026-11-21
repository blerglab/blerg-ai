package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestCutBoardProofUsesExactAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
	}{
		{"one-shot exact refusal", "", "lost_credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newPlanFx(t, db.BoardLevelFull)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				standing, _ := body["standing"].(bool)
				if standing {
					t.Error("one-shot cut selected standing authority")
				}
				if tc.state != "" {
					w.Header().Set("X-Blerg-Standing-State", tc.state)
				}
				http.Error(w, "not found", http.StatusNotFound)
			}))
			defer core.Close()
			fx.api.coreURL = core.URL
			fx.api.coreInternalKey = "fixture-key"
			record := blackboard.Record{WorkspaceID: fx.sess, Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}}
			_, err := fx.api.cutBoardProof(t.Context(), record, "board-a", fx.sess, "", false)
			if got := cutFailureCause(err, ""); got != tc.want {
				t.Fatalf("cut refusal=%s want %s", got, tc.want)
			}
		})
	}
}

type cutScopeBoard func(context.Context, mcpgw.BoardCall) (json.RawMessage, error)

func (f cutScopeBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	return f(ctx, c)
}

func TestCutReceiptReadUsesExactAuthority(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.api.hub.mcpStart.set(&mcpStartConfig{Board: cutScopeBoard(func(_ context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
		if !c.CutExecutor {
			t.Errorf("cut receipt read widened authority: executor=%v", c.CutExecutor)
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"{\"release\":{\"id\":\"r\",\"board_id\":\"b\"}}"}]}`), nil
	})})
	if _, err := fx.api.readCutRelease(t.Context(), mcpgw.Proof{AccountID: topicOwner, SessionID: "login"}, "b", "r", fx.sess); err != nil {
		t.Fatal(err)
	}
}

func (cutScopeBoard) BoardConfigured() bool                 { return true }
func (cutScopeBoard) BoardMCPURL() string                   { return "https://board.example/mcp" }
func (cutScopeBoard) RevokeSession(context.Context, string) {}
