package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetGateIngestionCorrectionsAndVisibility(t *testing.T) {
	fx := newWorkFx(t)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	body := map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Minute).Format(time.RFC3339), "report": map[string]any{"seconds": 60, "complete": true, "host": "private-canary", "suites": []any{map[string]any{"suite": "runner-unit", "seconds": 90, "outcome": "pass", "log": "private-canary"}}}}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 201 {
		t.Fatalf("ingest=%d", code)
	}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 200 {
		t.Fatalf("idempotent=%d", code)
	}
	if code, _ := fx.postWork("gate-reports", fx.privSess, fx.sessTok, body); code != 403 {
		t.Fatalf("cross session=%d", code)
	}
	var raw string
	if err := fx.pool.QueryRow(t.Context(), `SELECT report::text FROM live_gate_reports WHERE session_id=$1`, fx.sess).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "private-canary") {
		t.Fatal("raw report persisted")
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.gates.durations", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 2 || rows[0]["seconds"] != float64(60) {
		t.Fatalf("durations=%+v %s", rows, cause)
	}
	q.Data.Parameters = map[string]string{"suite": "total"}
	totals, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(totals) != 1 || totals[0]["seconds"] != float64(60) {
		t.Fatalf("suite selection=%+v %s", totals, cause)
	}
	q.Data.Name = "blerg.gates.bins"
	q.Data.Parameters = map[string]string{"width": "60"}
	bins, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(bins) != 1 || bins[0]["start"] != 60 || bins[0]["count"] != 1 {
		t.Fatalf("resolved bins=%+v %s", bins, cause)
	}
	q.Data.Name = "blerg.gates.durations"
	q.Data.Parameters = map[string]string{}
	body["revision"] = 2
	body["finished_at"] = start.Add(45 * time.Second).Format(time.RFC3339)
	body["report"].(map[string]any)["seconds"] = 45
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 201 {
		t.Fatalf("correction=%d", code)
	}
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 2 || rows[0]["seconds"] != float64(45) {
		t.Fatalf("corrected=%+v %s", rows, cause)
	}
	body["revision"] = 1
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 409 {
		t.Fatalf("stale correction=%d", code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, false)
	if cause != "access_removed" || len(rows) != 0 {
		t.Fatal("foreign gate read")
	}
}

func TestDatasetGateRejectsInvalidAndConflictingSamples(t *testing.T) {
	fx := newWorkFx(t)
	const valid = `{"run_id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","revision":1,"commit":"0123456789012345678901234567890123456789","started_at":"2026-11-01T00:00:00Z","finished_at":"2026-11-01T00:01:00Z","report":{"seconds":60,"complete":true,"suites":[{"suite":"runner-unit","seconds":90,"outcome":"pass"}]}}`
	for _, bad := range []string{strings.Replace(valid, `"seconds":60`, `"seconds":-1`, 1), strings.Replace(valid, `"seconds":60`, `"seconds":null`, 1), strings.Replace(valid, `"runner-unit"`, `"/private-canary"`, 1), strings.Replace(valid, `"runner-unit"`, `"total"`, 1), strings.Replace(valid, `"revision":1`, `"revision":0`, 1)} {
		if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, bad); code != 400 {
			t.Fatalf("invalid report=%d", code)
		}
	}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, valid); code != 201 {
		t.Fatalf("valid=%d", code)
	}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, strings.Replace(valid, `"seconds":60`, `"seconds":61`, 1)); code != 409 {
		t.Fatalf("conflict=%d", code)
	}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.token(topicOwner, "human"), valid); code != 401 && code != 403 {
		t.Fatalf("human ingestion=%d", code)
	}
}

func TestDatasetGateQueuedDeliveryRechecksDescendants(t *testing.T) {
	fx := newWorkFx(t)
	child := newUUID()
	if err := db.InsertSessionAs(t.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "Worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	report, err := json.Marshal(map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Minute).Format(time.RFC3339), "report": map[string]any{"seconds": 60, "complete": true, "suites": []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report) VALUES($1,$2,1,$3)`, child, newUUID(), string(report)); err != nil {
		t.Fatal(err)
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.gates.durations", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, false)
	if cause != "" || len(rows) != 1 {
		t.Fatalf("snapshot=%+v %s", rows, cause)
	}
	fence, err := datasetPrivacyFence(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET private=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	bc := &BrowserConn{AccountID: topicOther, datasetSubs: map[string]*datasetSubscription{id: {reference: q, deliveryFences: map[int64]datasetDeliveryFence{0: {privacy: fence}}}}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Status: "ready", Upserts: rows})
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	var delta protocol.DatasetDelta
	if !ok || json.Unmarshal(safe, &delta) != nil || !delta.Snapshot || len(delta.Upserts) != 0 || !bc.datasetSubs[id].forceSnapshot.Load() {
		t.Fatalf("queued private descendant survived: %s", safe)
	}
}
