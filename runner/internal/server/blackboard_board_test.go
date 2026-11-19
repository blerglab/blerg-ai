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

type blackboardGateway struct {
	fakeSessionBoard
	closed bool
}

func (b *blackboardGateway) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	_, _ = b.fakeSessionBoard.CallBoard(ctx, c)
	var value any
	column := "done"
	release := "release"
	card := blackboard.Card{BoardID: "board-a", Number: 1, Title: "Visible title", ColumnID: &column, ReleaseID: &release}
	switch c.Tool {
	case "blerg_board_get":
		value = map[string]any{"id": "board-a"}
	case "blerg_column_list":
		value = []blackboard.Column{{ID: "done", Terminal: true}}
	case "blerg_card_search":
		value = blackboard.CardPage{Cards: []blackboard.Card{card}}
	case "blerg_card_get":
		value = card
	case "blerg_release_get":
		var args struct {
			ReleaseID string `json:"release_id"`
		}
		_ = json.Unmarshal(c.Arguments, &args)
		status := "planned"
		if b.closed && args.ReleaseID == "release" {
			status = "released"
		}
		value = map[string]any{"release": blackboard.Release{ID: args.ReleaseID, BoardID: c.BoardID, Status: status}}
	case "blerg_release_close":
		b.closed = true
		value = map[string]any{"ok": true}
	}
	text, _ := json.Marshal(value)
	return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": string(text)}}})
}
func TestBlackboardSliceAndFinalThroughRoutes(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	human := fx.token(topicOwner, "human")
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	b := &blackboardGateway{}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: b})
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path+"?session="+fx.sess, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+human)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Plan", Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Goals: []blackboard.Goal{{ID: "goal", Owner: actor, Name: "Purpose", Version: 1}}, Milestones: []blackboard.Milestone{{ID: "milestone", Owner: actor, Name: "Next", BoardID: "board-a", ReleaseID: "release", Version: 1}}}
	if err := api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var rec *httptest.ResponseRecorder
	path := "/api/blackboards/" + record.ID + "/milestones/" + record.Milestones[0].ID
	rec = request("GET", path+"/slice", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"unplanned":1`) || !strings.Contains(rec.Body.String(), "Visible title") {
		t.Fatalf("slice %d %s", rec.Code, rec.Body.String())
	}
	rec = request("POST", path+"/final", `{"revision":0,"next_release_id":"next"}`)
	if rec.Code != 410 {
		t.Fatalf("read-only Final %d %s", rec.Code, rec.Body.String())
	}
	stored, _ := api.blackboardStore().Load(context.Background(), record.ID)
	if stored.Milestones[0].Stage == "Finalizing" {
		t.Fatal("read-only request poisoned plan")
	}
	if code, body := setLevel(fx, human, "board-a", "full"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	rec = request("POST", path+"/final", `{"revision":0,"next_release_id":"next"}`)
	if rec.Code != 410 || b.closed {
		t.Fatalf("Final %d %s", rec.Code, rec.Body.String())
	}
	rec = request("GET", path+"/slice", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"frozen":true`) {
		t.Fatalf("frozen %d %s", rec.Code, rec.Body.String())
	}
}

// TestBlackboardFinalRefusesAgentsAndNonOwners proves Final is person-only at the HTTP
// route: an agent token and a signed-in non-owner are both refused, and the refusal
// never advances the milestone into Finalizing.
func TestBlackboardFinalRefusesAgentsAndNonOwners(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "full"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	mux := http.NewServeMux()
	api.RegisterBlackboardRoutes(mux)
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Name: "Plan", Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}, Milestones: []blackboard.Milestone{{ID: blackboard.NewID(), Name: "Next", Owner: owner, Version: 1, BoardID: "board-a", ReleaseID: "release"}}}
	if err := api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	path := "/api/blackboards/" + record.ID + "/milestones/" + record.Milestones[0].ID + "/final"
	do := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path+"?session="+fx.sess, strings.NewReader(`{"revision":0,"next_release_id":""}`))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	for _, token := range []string{fx.token(topicOwner, "agent"), fx.token(topicOther, "human")} {
		if w := do(token); w.Code != 403 {
			t.Fatalf("Final authority %d %s", w.Code, w.Body.String())
		}
	}
	stored, err := api.blackboardStore().Load(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Milestones[0].Stage == "Finalizing" {
		t.Fatal("refused Final poisoned the milestone")
	}
}
