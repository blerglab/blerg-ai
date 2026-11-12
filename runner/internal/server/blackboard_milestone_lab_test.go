package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type labMilestoneGateway struct{ boundLockCheckingBoard }

func (b *labMilestoneGateway) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	raw, err := b.boundLockCheckingBoard.CallBoard(ctx, c)
	if err != nil {
		return nil, err
	}
	if c.Tool == "blerg_release_list" {
		return json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": "[]"}}})
	}
	return raw, nil
}

// Exercise the shipped agent endpoint with the real partitioned Postgres store.
// Historical board cards still reference a linked board even though the old
// milestone has never been linked to a release.
func TestBlackboardMilestoneAddLabShape(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	board := &labMilestoneGateway{boundLockCheckingBoard: boundLockCheckingBoard{pool: fx.pool}}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &planBoardList{}, Board: board})
	owner := blackboard.Actor{ID: topicOwner, Kind: "human"}
	mid := blackboard.NewID()
	r := blackboard.Record{ID: blackboard.NewID(), WorkspaceID: fx.sess, Owner: owner, Version: 1,
		Links:      []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}},
		Milestones: []blackboard.Milestone{{ID: mid, Owner: owner, Name: "0.6.0", Version: 1}},
	}
	for n := 1; n <= 237; n++ {
		r.Items = append(r.Items, blackboard.Item{ID: blackboard.ItemID(blackboard.NewID()), MilestoneID: mid, BoardID: "board-a", CardNumber: n, Title: "Existing work", Reason: strings.Repeat("x", 60), State: "planned"})
	}
	for n := 0; n < 2; n++ {
		q := blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), MilestoneID: mid, Actor: blackboard.Actor{ID: fx.sess, Kind: "agent"}, Status: "pending", CreatedAt: time.Now().UTC()}
		for k := 0; k < 24; k++ {
			q.Changes = append(q.Changes, blackboard.Change{ID: blackboard.ItemID(fmt.Sprintf("change-%d", k)), Operation: "add", Item: blackboard.Item{Title: "Pending work", Reason: strings.Repeat("x", 800)}})
		}
		r.Proposals = append(r.Proposals, q)
	}
	board.id = r.ID
	header, err := json.Marshal(blackboard.Header(r))
	if err != nil {
		t.Fatal(err)
	}
	if len(header) > 85657 {
		t.Fatalf("fixture header too large: %d", len(header))
	}
	r.Vision = strings.Repeat("x", 85657-len(header))
	if err = fx.api.blackboardStore().Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	var scopePending int
	if err = fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM plan_proposals WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')='' AND status='pending'`, r.ID).Scan(&scopePending); err != nil || scopePending != 0 {
		t.Fatalf("workspace pending %d: %v", scopePending, err)
	}
	mux := http.NewServeMux()
	fx.api.RegisterBlackboardRoutes(mux)
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/plan?operation=propose&blackboard="+r.ID, strings.NewReader(`{"changes":[{"operation":"milestone_add","milestone":{"name":"0.7.0","description":"Next work"}}]}`))
	req.Header.Set("Authorization", "Bearer "+fx.sessTok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("workspace milestone_add: %d %s", w.Code, w.Body)
	}
	got, err := fx.api.blackboardStore().Load(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 237 || len(got.Milestones) != 1 || len(got.Proposals) != 3 {
		t.Fatalf("proposal changed scope or history: items=%d milestones=%d proposals=%d", len(got.Items), len(got.Milestones), len(got.Proposals))
	}
	q := got.Proposals[2]
	if q.MilestoneID != "" || q.Status != "pending" || q.Changes[0].Milestone.Name != "0.7.0" {
		t.Fatalf("wrong proposal scope: %+v", q)
	}
	var response struct {
		Data struct {
			ID string `json:"proposal_id"`
		} `json:"untrusted_data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	call := func(path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	ask := call("/api/messages", fx.sessTok, `{"session_id":"`+fx.sess+`","kind":"ask","body":"Open next milestone?","person_required":true,"subject":{"kind":"plan","blackboard_id":"`+r.ID+`","proposal_id":"`+response.Data.ID+`","revision":0}}`)
	if ask.Code != 201 {
		t.Fatalf("bound ask: %d %s", ask.Code, ask.Body)
	}
	messages, err := db.ListMessagesBySession(t.Context(), fx.pool, fx.sess)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %v %v", messages, err)
	}
	approved := call("/api/messages/"+messages[0].ID+"/reply", fx.token(topicOwner, "human"), `{"answer":"yes"}`)
	if approved.Code != 200 {
		t.Fatalf("owner approval: %d %s", approved.Code, approved.Body)
	}
	got, err = fx.api.blackboardStore().Load(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Milestones) != 2 || got.Milestones[1].Name != "0.7.0" || len(got.Items) != 237 {
		t.Fatalf("owner commit: milestones=%+v items=%d", got.Milestones, len(got.Items))
	}

}
