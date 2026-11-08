package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetFenceMembershipDoesNotPurgeOrFlicker(t *testing.T) {
	for _, change := range []string{"added", "removed", "private", "owner", "busy_revision"} {
		t.Run(change, func(t *testing.T) {
			fx := newTopicFx(t)
			child := newUUID()
			insert := func() {
				t.Helper()
				if err := db.InsertSessionAs(t.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
					t.Fatal(err)
				}
			}
			if change != "added" {
				insert()
			}
			fence, err := datasetPrivacyFence(t.Context(), fx.pool, fx.sess)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "added":
				insert()
			case "removed":
				if _, err := fx.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, child); err != nil {
					t.Fatal(err)
				}
			case "private":
				if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, child); err != nil {
					t.Fatal(err)
				}
			case "owner":
				if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2 WHERE id=$1`, child, topicOther); err != nil {
					t.Fatal(err)
				}
			}
			id := newUUID()
			scope := &datasetScope{}
			scope.revision.Add(5)
			sub := &datasetSubscription{reference: protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Name: "blerg.workers.timeline"}}, scope: scope, deliveryFences: map[int64]datasetDeliveryFence{2: {privacy: fence}}}
			bc := &BrowserConn{AccountID: topicOwner, datasetSubs: map[string]*datasetSubscription{id: sub}}
			raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, BaseSequence: 1, Sequence: 2, Status: "ready", Upserts: []protocol.DatasetRow{{"key": "held"}}})
			safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
			if change == "added" || change == "removed" {
				if ok || len(safe) != 0 || !sub.forceSnapshot.Load() {
					t.Fatalf("membership flickered or purged: %s", safe)
				}
				return
			}
			var delta protocol.DatasetDelta
			if !ok || json.Unmarshal(safe, &delta) != nil {
				t.Fatal("no delivery")
			}
			if change == "busy_revision" {
				if delta.Status != "ready" || len(delta.Upserts) != 1 {
					t.Fatalf("busy timeline flickered: %s", safe)
				}
			} else if delta.Status != "access_removed" || !delta.Snapshot || len(delta.Upserts) != 0 {
				t.Fatalf("authority change not purged: %s", safe)
			}
		})
	}
}
