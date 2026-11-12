package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBoardLinkDraftUnlinkReview(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	ctx := t.Context()
	if err := db.SaveBlackboardDraft(ctx, fx.pool, fx.sess, fx.sess, json.RawMessage(`{"name":"Draft-created plan","board_id":"board-a"}`)); err != nil {
		t.Fatal(err)
	}
	d, err := db.GetBlackboardDraft(ctx, fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := fx.api.applyBoundDraft(ctx, tx, boundSubject{DraftID: d.ID, Revision: d.Revision}, topicOwner, fx.sess, "committed", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := fx.api.blackboardStore().Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Links) != 1 || r.Links[0].BoardID != "board-a" || r.Links[0].WorkspaceID != fx.sess {
		t.Fatalf("draft link: %+v", r.Links)
	}
	service := blackboard.Service{Store: fx.api.blackboardStore(), BlackboardID: id}
	pid, err := service.Propose(ctx, blackboard.Actor{ID: fx.sess, Kind: "agent"}, "", []blackboard.Change{{Operation: "unlink", Item: blackboard.Item{BoardID: "board-a", Reason: "Stop tracking"}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	raw, _ := json.Marshal(map[string]any{"kind": "ask", "subject": boundSubject{Kind: "plan", BlackboardID: id, ProposalID: pid, Revision: 0}})
	response := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
	if response.Code != 201 {
		t.Fatalf("ask: %d %s", response.Code, response.Body.String())
	}
	var ask struct{ ID string }
	_ = json.Unmarshal(response.Body.Bytes(), &ask)
	response = draftRequest(fx, mux, "POST", "/api/messages/"+ask.ID+"/reply", fx.token(topicOwner, "human"), `{"answer":"yes"}`)
	if response.Code != 200 {
		t.Fatalf("answer: %d %s", response.Code, response.Body.String())
	}
	r, err = fx.api.blackboardStore().Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Links) != 1 || r.Links[0].WorkspaceID != fx.sess || r.Links[0].BoardID != "" {
		t.Fatalf("workspace link lost: %+v", r.Links)
	}
	var count int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM blackboard_links WHERE blackboard_id=$1 AND workspace_id=$2 AND board_id=''`, id, fx.sess).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("workspace lookup row lost")
	}
}

func TestBoardLinkInUseAnswerCancelsReview(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &blackboardGateway{}})
	ctx := t.Context()
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: topicOwner, Kind: "human"}, WorkspaceID: fx.sess, Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}}
	if err := fx.api.blackboardStore().Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	pid, err := (blackboard.Service{Store: fx.api.blackboardStore(), BlackboardID: r.ID}).Propose(ctx, blackboard.Actor{ID: fx.sess, Kind: "agent"}, "", []blackboard.Change{{Operation: "unlink", Item: blackboard.Item{BoardID: "board-a", Reason: "Stop tracking"}}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	raw, _ := json.Marshal(map[string]any{"kind": "ask", "subject": boundSubject{Kind: "plan", BlackboardID: r.ID, ProposalID: pid, Revision: 0}})
	response := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
	if response.Code != 201 {
		t.Fatalf("ask: %d %s", response.Code, response.Body.String())
	}
	var ask struct{ ID string }
	_ = json.Unmarshal(response.Body.Bytes(), &ask)
	// Scope stays the same, but an item now uses the linked board.
	if err = fx.api.blackboardStore().Update(ctx, r.ID, func(record *blackboard.Record) error {
		record.Items = append(record.Items, blackboard.Item{ID: "new-card", BoardID: "board-a", CardNumber: 1, State: "planned"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response = draftRequest(fx, mux, "POST", "/api/messages/"+ask.ID+"/reply", fx.token(topicOwner, "human"), `{"answer":"yes"}`)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "Main needs to ask again") {
		t.Fatalf("refusal: %d %s", response.Code, response.Body.String())
	}
	var status, note, messageStatus string
	if err = fx.pool.QueryRow(ctx, `SELECT b.status,b.note,m.status FROM bound_asks b JOIN messages m ON m.id=b.message_id WHERE b.message_id=$1`, ask.ID).Scan(&status, &note, &messageStatus); err != nil {
		t.Fatal(err)
	}
	if status != "canceled" || note != "board_in_use" || messageStatus != "answered" {
		t.Fatalf("binding left open: %s %s %s", status, note, messageStatus)
	}
	receipt, _, err := fx.api.boundReceipt(ctx, ask.ID)
	if err != nil || receipt["cause"] != "board_in_use" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	var notices int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, fx.sess, messageEventID(ask.ID, "superseded")).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatal("cancellation notice missing")
	}
	stored, err := fx.api.blackboardStore().Load(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PlanRevision != 0 || len(stored.Items) != 1 || len(stored.Links) != 1 || stored.Proposals[0].Status != "pending" {
		t.Fatal("failed decision partially applied")
	}
}
