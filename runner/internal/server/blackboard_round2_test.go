package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type blackboardRound2Gateway struct {
	blackboardGateway
	failTool, message string
}

func (b *blackboardRound2Gateway) CallBoard(c context.Context, call mcpgw.BoardCall) (json.RawMessage, error) {
	if call.Tool == "blerg_card_search" && b.failTool == "blerg_card_get" && strings.Contains(string(call.Arguments), `"numbers"`) {
		if b.message == "card not found" {
			return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"cards":[]}`}}})
		}
		return json.Marshal(map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": b.message}}})
	}
	if call.Tool == b.failTool {
		return json.Marshal(map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": b.message}}})
	}
	if call.Tool == "blerg_card_search" {
		return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": `{"cards":[]}`}}})
	}
	return b.blackboardGateway.CallBoard(c, call)
}
func TestBlackboardRound2ProposalContentsOwnerOnly(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	ctx := context.Background()
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Proposals: []blackboard.Proposal{{ID: "private-proposal", Status: "pending", Changes: []blackboard.Change{{ID: "secret-change", Operation: "add", Item: blackboard.Item{Title: "PROPOSAL_SECRET"}}}}}}
	if err := fx.api.blackboardStore().Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, account := range []string{topicOwner, topicOther} {
		r := httptest.NewRequest("GET", "/api/blackboards/"+record.ID+"?session="+fx.sess, nil)
		r.Header.Set("Authorization", "Bearer "+fx.token(account, "human"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("read %s: %d %s", account, w.Code, w.Body.String())
		}
		var body struct {
			Record  blackboard.Record `json:"record"`
			Pending int               `json:"pending_proposals"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if account == topicOwner {
			if len(body.Record.Proposals) != 1 {
				t.Fatal("owner lost proposal")
			}
		} else if strings.Contains(w.Body.String(), "PROPOSAL_SECRET") || len(body.Record.Proposals) != 0 {
			t.Fatal("viewer saw proposal contents")
		}
	}
}
func TestBlackboardRound2MissingCardMapping(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	b := &blackboardRound2Gateway{failTool: "blerg_card_get", message: "card not found"}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: b})
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Milestones: []blackboard.Milestone{{ID: "m", BoardID: "board-a", ReleaseID: "release", Version: 1}}, Items: []blackboard.Item{{ID: "item", MilestoneID: "m", BoardID: "board-a", CardNumber: 1, State: "planned"}}}
	if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	for _, message := range []string{"card not found", "permission denied"} {
		b.message = message
		r := httptest.NewRequest("GET", "/api/blackboards/"+record.ID+"/milestones/m/slice?session="+fx.sess, nil)
		r.Header.Set("Authorization", "Bearer "+human)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if message == "card not found" {
			var view blackboard.SliceView
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || !view.Complete || len(view.Cards) != 1 || !view.Cards[0].Deleted {
				t.Fatalf("missing mapping %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 403 {
			t.Fatalf("permission must not tombstone: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestBlackboardRound2BoardResultFailuresLogged(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	b := &blackboardRound2Gateway{failTool: "blerg_board_get", message: "permission denied"}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: b})
	record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}}
	if err := fx.api.blackboardStore().Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	logs := captureLog(t)
	r := httptest.NewRequest("GET", "/api/blackboards/"+record.ID+"?session="+fx.sess, nil)
	r.Header.Set("Authorization", "Bearer "+human)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 || !strings.Contains(logs.String(), `"cause":"*server.APIError"`) {
		t.Fatalf("board failure: %d %s logs=%s", w.Code, w.Body.String(), logs.String())
	}

}
