package server

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetReviewPairsCorrectLatestTargetAndUnknownAuthor(t *testing.T) {
	fx := newTopicFx(t)
	api, _, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='claude' WHERE id=$1`, fx.ended); err != nil {
		t.Fatal(err)
	}
	viewer, ok := fx.api.verifyBrowserToken(fx.token(topicOwner, "human"))
	if !ok {
		t.Fatal("viewer")
	}
	ctx := context.WithValue(t.Context(), datasetViewerContextKey{}, viewer)
	if _, err := fx.pool.Exec(ctx, `INSERT INTO session_card_links VALUES($1,'board-a',7,'pin',now())`, fx.ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO session_commit_deliveries(session_id,digest,manifest,results,title,ticket_through,runtime_generation) VALUES($1,'fixture','{}',$2,'',0,1)`, fx.ended, `[{"summary":{"commits":[{"sha":"`+fixtureSHA+`"}]}}]`); err != nil {
		t.Fatal(err)
	}
	appendReview := func(raw string) {
		t.Helper()
		if _, _, err := db.AppendAgentEventAt(ctx, fx.pool, fx.sess, newUUID(), "mark", raw, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	appendReview(`{"kind":"review","board_id":"board-a","card":7,"by":" CODEX ","target":"` + fixtureSHA + `","round":1,"verdict":"changes","findings":["high","low"],"text":"private-canary"}`)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.reviews.pairs", Parameters: map[string]string{"board": "board-a", "card": "7"}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["high"] != 1 || rows[0]["author_engine"] != "claude" {
		t.Fatalf("pairs=%+v %s", rows, cause)
	}
	appendReview(`{"kind":"review","board_id":"board-a","card":7,"by":"codex","target":"` + fixtureSHA + `","round":2,"verdict":"approved"}`)
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["high"] != 0 || rows[0]["approved"] != 1 || rows[0]["reviews"] != 1 {
		t.Fatalf("corrected=%+v %s", rows, cause)
	}
	appendReview(`{"kind":"review","board_id":"board-a","card":7,"by":"codex","target":"` + fixtureSHA + `","round":2,"verdict":"changes","findings":["medium"]}`)
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["approved"] != 0 || rows[0]["medium"] != 1 {
		t.Fatalf("same round correction=%+v %s", rows, cause)
	}
	appendReview(`{"kind":"review","board_id":"board-a","card":7,"by":"hermes","target":"abcdef0123456789abcdef0123456789abcdef01","round":1,"verdict":"approved"}`)
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 2 || rows[1]["author_engine"] != "unknown" {
		t.Fatalf("unknown author=%+v %s", rows, cause)
	}
	appendReview(`{"kind":"review","board_id":"board-a","card":7,"by":"","target":"","round":1,"verdict":"approved"}`)
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 2 {
		t.Fatalf("empty target %+v %s", rows, cause)
	}

	if _, _, cause = resolveDataset(t.Context(), api.hub, fx.pool, topicOwner, q, &history, false); cause != "access_removed" {
		t.Fatal("missing browser proof read reviews")
	}

}
