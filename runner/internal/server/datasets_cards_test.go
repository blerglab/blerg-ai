package server

import (
	"context"
	"encoding/json"

	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetCardTimeRequiresLiveBoardAndKnownTiming(t *testing.T) {
	fx := newTopicFx(t)
	api, board, _ := newLiveCardAPI(t)
	api.dbPool = fx.pool
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	seedCardTimeSession(t, fx.pool, fx.sess, "board-a", 7)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.cards.time", Parameters: map[string]string{"board": "board-a", "card": "7"}}}
	viewer, ok := fx.api.verifyBrowserToken(fx.token(topicOwner, "human"))
	if !ok {
		t.Fatal("viewer fixture")
	}
	ctx := context.WithValue(t.Context(), datasetViewerContextKey{}, viewer)
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || rows[0]["seconds"] != float64(14400) {
		t.Fatalf("card time=%+v %s", rows, cause)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET insight_pending=true WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "card_time_pending" || len(rows) != 0 {
		t.Fatalf("stale total=%+v %s", rows, cause)
	}
	board.mu.Lock()
	board.refuse = true
	board.mu.Unlock()
	rows, _, cause = resolveDataset(ctx, api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "access_removed" || len(rows) != 0 {
		t.Fatal("board revocation did not purge")
	}
	for _, name := range []string{"blerg.cards.time", "blerg.reviews.pairs"} {
		q.Data.Name = name
		id := newUUID()
		bc := &BrowserConn{AccountID: topicOwner, verified: viewer, datasetSubs: map[string]*datasetSubscription{id: {reference: q}}}
		raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "old", "seconds": 14400}}})
		safe, ok := api.hub.filterDatasetDelivery(ctx, bc, fx.pool, raw)
		var delta protocol.DatasetDelta
		if !ok || json.Unmarshal(safe, &delta) != nil || delta.Status != "access_removed" || !delta.Snapshot || len(delta.Upserts) != 0 {
			t.Fatalf("queued card source survived revocation: %s", safe)
		}
	}

}
