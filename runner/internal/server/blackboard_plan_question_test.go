package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"testing"
)

func TestBlackboardPlanAskDoesNotBlockLinkedCards(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	actor := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: actor, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: "old", Owner: actor, Name: "0.6.0", Version: 1}, {ID: "new", Owner: actor, Name: "0.7.0", Version: 1}}}
	if err := fx.api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	// Main has accumulated card links across both releases. Its next scope
	// approval is a project decision, not a blocker on every linked card.
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO session_card_links(session_id,board_id,card_number,source,linked_at) VALUES($1,'board-a',1,'pin',now()),($1,'board-a',2,'pin',now())`, fx.sess); err != nil {
		t.Fatal(err)
	}
	var ask string
	if err := fx.pool.QueryRow(t.Context(), `INSERT INTO messages(session_id,kind,body,status,person_required) VALUES($1,'ask','Approve next milestone scope?','open',true) RETURNING id::text`, fx.sess).Scan(&ask); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO bound_asks(message_id,workspace_id,blackboard_id,owner_id,subject) VALUES($1,$2,$3,$4,'{"kind":"plan","milestone_id":"new"}')`, ask, fx.sess, record.ID, topicOwner); err != nil {
		t.Fatal(err)
	}
	questions, err := fx.api.blackboardQuestions(t.Context(), fx.sess, topicOwner, "board-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 0 {
		t.Fatalf("plan approval marked historical card links needs_you: %v", questions)
	}
	if _, err = fx.pool.Exec(t.Context(), `UPDATE messages SET status='answered' WHERE id=$1`, ask); err != nil {
		t.Fatal(err)
	}
	if _, err = fx.pool.Exec(t.Context(), `INSERT INTO messages(session_id,kind,body,status,person_required) VALUES($1,'ask','Work is blocked on a person decision','open',true)`, fx.sess); err != nil {
		t.Fatal(err)
	}
	questions, err = fx.api.blackboardQuestions(t.Context(), fx.sess, topicOwner, "board-a")
	if err != nil || !questions[1] || !questions[2] {
		t.Fatalf("ordinary work question lost: %v %v", questions, err)
	}
}
