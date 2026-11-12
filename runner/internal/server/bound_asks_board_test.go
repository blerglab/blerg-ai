package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The unrelated board is canned; real SQL proves neither its creation preflight
// nor its answer preflight runs while the runner holds a blackboard row lock.
type boundLockCheckingBoard struct {
	blackboardGateway
	pool  *pgxpool.Pool
	id    string
	reads int
}

func (b *boundLockCheckingBoard) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE blackboards SET version=version WHERE id=$1`, b.id); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	b.reads++
	return b.blackboardGateway.CallBoard(ctx, c)
}
func TestBoundBoardReadsDoNotHoldThePlanLock(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	if code, body := setLevel(fx, fx.token(topicOwner, "human"), "board-a", "read"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	r := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Name: "Plan", Version: 1, Links: []blackboard.Link{{WorkspaceID: fx.sess, BoardID: "board-a"}}}
	if err := fx.api.blackboardStore().Create(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	pid, err := (blackboard.Service{Store: fx.api.blackboardStore(), BlackboardID: r.ID}).Propose(t.Context(), blackboard.Actor{Kind: "agent", ID: fx.sess}, "", []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "One change"}}})
	if err != nil {
		t.Fatal(err)
	}
	board := &boundLockCheckingBoard{pool: fx.pool, id: r.ID}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", fx.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/{id}/reply", fx.api.HandlePostMessageReply)
	raw, _ := json.Marshal(map[string]any{"kind": "ask", "body": "ignored", "subject": boundSubject{Kind: "plan", BlackboardID: r.ID, ProposalID: pid, Revision: 0}})
	response := draftRequest(fx, mux, "POST", "/api/messages", fx.sessTok, string(raw))
	if response.Code != 201 {
		t.Fatalf("creation preflight held a lock: %d %s", response.Code, response.Body.String())
	}
	var ask struct{ ID string }
	_ = json.Unmarshal(response.Body.Bytes(), &ask)
	response = draftRequest(fx, mux, "POST", "/api/messages/"+ask.ID+"/reply", fx.token(topicOwner, "human"), `{"answer":"yes"}`)
	if response.Code != 200 || board.reads < 2 {
		t.Fatalf("answer preflight held a lock: %d %s; reads %d", response.Code, response.Body.String(), board.reads)
	}
}
