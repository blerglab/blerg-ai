package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDatasetReviewShortSHAUsesDeliveryPrefix(t *testing.T) {
	fx := newTopicFx(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='claude' WHERE id=$1`, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO session_card_links VALUES($1,'board-a',7,'pin',now())`, fx.ended); err != nil {
		t.Fatal(err)
	}
	deliver := func(sid, sha string) {
		t.Helper()
		raw, _ := json.Marshal([]any{map[string]any{"summary": map[string]any{"commits": []any{map[string]any{"sha": sha}}}}})
		if _, err := fx.pool.Exec(t.Context(), `INSERT INTO session_commit_deliveries(session_id,digest,manifest,results,title,ticket_through,runtime_generation) VALUES($1,$2,'{}',$3,'',0,1)`, sid, sha, raw); err != nil {
			t.Fatal(err)
		}
	}
	deliver(fx.ended, fixtureSHA)
	if _, _, err := db.AppendAgentEventAt(t.Context(), fx.pool, fx.sess, newUUID(), "mark", `{"kind":"review","board_id":"board-a","card":7,"by":"codex","target":"`+fixtureSHA[:7]+`","round":1,"verdict":"approved"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	p := map[string]string{"board": "board-a", "card": "7"}
	rows, _, cause := reviewPairDatasetRows(t.Context(), fx.pool, topicOwner, p)
	if cause != "" || len(rows) != 1 || rows[0]["author_engine"] != "claude" {
		t.Fatalf("short target lost its author: %+v %s", rows, cause)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='codex' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	deliver(fx.sess, fixtureSHA[:7]+strings.Repeat("f", 33))
	rows, _, cause = reviewPairDatasetRows(t.Context(), fx.pool, topicOwner, p)
	if cause != "" || len(rows) != 1 || rows[0]["author_engine"] != "unknown" {
		t.Fatalf("ambiguous prefix picked an author: %+v %s", rows, cause)
	}
}
