package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestMessageInfoDecisionFromBoundAsk(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	for _, kind := range []string{"plan", "draft", "cut", "live_binding", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			var id string
			if err := fx.pool.QueryRow(t.Context(), `INSERT INTO messages(session_id,kind,body) VALUES($1,'ask','Decide?') RETURNING id`, fx.sess).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if kind != "ordinary" {
				if _, err := fx.pool.Exec(t.Context(), `INSERT INTO bound_asks(message_id,workspace_id,owner_id,subject,status) VALUES($1,$2,$3,jsonb_build_object('kind',$4::text),'declined')`, id, fx.sess, topicOwner, kind); err != nil {
					t.Fatal(err)
				}
			}
			row, err := db.GetMessage(t.Context(), fx.pool, id)
			if err != nil {
				t.Fatal(err)
			}
			if got := messageRowToInfo(*row).Decision; got != (kind != "ordinary") {
				t.Fatalf("Decision = %v for %s", got, kind)
			}
			rows, err := db.ListMessagesFor(t.Context(), fx.pool, 100, topicOwner)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range messageInfosFor(t.Context(), fx.pool, rows, topicOwner) {
				if message.ID == id {
					found = true
					if message.Decision != (kind != "ordinary") {
						t.Fatalf("feed decision = %v", message.Decision)
					}
				}
			}
			if !found {
				t.Fatal("message missing from feed")
			}
		})
	}
}
