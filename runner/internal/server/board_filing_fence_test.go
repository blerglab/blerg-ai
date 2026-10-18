package server

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type filingPrepareFence struct {
	*fixtureFiling
	prepare func()
}

func (f *filingPrepareFence) Prepare(_ context.Context, _ mcpgw.Proof, _ string, draft boardfiling.Draft) (boardfiling.Draft, error) {
	f.prepare()
	return draft, nil
}

// Remote preflight can finish after the session was ended or reassigned. The
// insertion must validate the current session row, not the preflight snapshot.
func TestBoardFilingPrepareCannotQueueAfterSessionChanges(t *testing.T) {
	for _, change := range []string{"ended", "stopped", "error", "owner"} {
		t.Run(change, func(t *testing.T) {
			fx, upstream := newFilingFx(t)
			prepared := false
			fx.api.boardFiling = &filingPrepareFence{fixtureFiling: upstream, prepare: func() {
				prepared = true
				query, value := `UPDATE sessions SET status=$2 WHERE id=$1`, change
				if change == "owner" {
					query, value = `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, topicOther
				}
				if _, err := fx.pool.Exec(t.Context(), query, fx.sess, value); err != nil {
					t.Fatal(err)
				}
			}}
			status, _ := fx.postWork("file-ticket", fx.sess, fx.sessTok, map[string]any{"board": "A", "title": "Work", "body": "Private reproduction"})
			if !prepared || status != 409 {
				t.Fatalf("prepared=%t status=%d, want true/409", prepared, status)
			}
			var queued int
			if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM mcp_proposals WHERE session_id=$1`, fx.sess).Scan(&queued); err != nil {
				t.Fatal(err)
			}
			if queued != 0 || upstream.creates != 0 || len(fx.events) != 0 {
				t.Fatalf("refused draft had side effects: queued=%d created=%d events=%d", queued, upstream.creates, len(fx.events))
			}
		})
	}
}
