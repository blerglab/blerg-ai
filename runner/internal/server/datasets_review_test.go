package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetReviewBoundaryKeys(t *testing.T) {
	fx := newWorkFx(t)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	suite := strings.Repeat("s", 64)
	body := map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Minute).Format(time.RFC3339), "report": map[string]any{"seconds": 60, "complete": true, "suites": []any{map[string]any{"suite": suite, "seconds": 60, "outcome": "pass"}}}}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 201 {
		t.Fatalf("ingest %d", code)
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.gates.durations", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 2 {
		t.Fatalf("rows %v %s", rows, cause)
	}
	for _, row := range rows {
		key, ok := row["key"].(string)
		if !ok || len(key) > 128 {
			t.Fatal("gate key exceeds client limit")
		}
	}
	board := strings.Repeat("b", 128)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$2 WHERE id=$1`, fx.sess, fx.ended); err != nil {
		t.Fatal(err)
	}
	seedCardTimeSession(t, fx.pool, fx.sess, board, 7)
	rows, _, cause = cardTimeDatasetRows(t.Context(), fx.pool, topicOwner, map[string]string{"board": board, "card": "7"})
	key := ""
	if len(rows) > 0 {
		key, _ = rows[0]["key"].(string)
	}
	if cause != "" || len(rows) != 1 || len(key) > 128 {
		t.Fatalf("card key exceeds client limit: %v %s", rows, cause)
	}
}

func TestDatasetGateRejectsFutureAndContradictoryTime(t *testing.T) {
	fx := newWorkFx(t)
	for _, end := range []string{time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "2026-11-01T00:02:00Z"} {
		body := map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": "2026-11-01T00:00:00Z", "finished_at": end, "report": map[string]any{"seconds": 60, "complete": true, "suites": []any{}}}
		if endTime, err := time.Parse(time.RFC3339, end); err == nil && endTime.After(time.Now()) {
			body["started_at"] = endTime.Add(-time.Minute).Format(time.RFC3339)
		}
		if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 400 {
			t.Fatalf("invalid time accepted: %d", code)
		}
	}
}

func TestDatasetGateLimitsWindowFreshnessAndRetention(t *testing.T) {
	fx := newWorkFx(t)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	body := map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Second).Format(time.RFC3339), "report": map[string]any{"seconds": 1, "complete": true, "suites": []any{}}}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 201 {
		t.Fatalf("ingest %d", code)
	}
	observed := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_gate_reports SET observed_at=$2 WHERE session_id=$1`, fx.sess, observed); err != nil {
		t.Fatal(err)
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.gates.durations", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, at, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 1 || !at.Equal(observed) {
		t.Fatalf("freshness %v %s", at, cause)
	}
	q.Data.Parameters["since"] = time.Now().UTC().Format(time.RFC3339)
	rows, _, cause = resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOwner, q, &history, false)
	if cause != "" || len(rows) != 0 {
		t.Fatal("since did not filter reports")
	}

	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_gate_ingest_limits SET requests=29 WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	for range 2 {
		go func() {
			payload := map[string]any{}
			for k, v := range body {
				payload[k] = v
			}
			payload["run_id"] = newUUID()
			code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, payload)
			codes <- code
		}()
	}
	first, second := <-codes, <-codes
	seen := map[int]int{}
	seen[first]++
	seen[second]++
	if seen[201] != 1 || seen[429] != 1 {
		t.Fatalf("concurrent rate admission %d %d", first, second)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_gate_ingest_limits SET requests=30 WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 429 {
		t.Fatalf("rate cap %d", code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_gate_ingest_limits SET requests=1,window_started=now()-interval '2 minutes' WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report) SELECT $1,gen_random_uuid(),1,'{}' FROM generate_series(1,255)`, fx.sess); err != nil {
		t.Fatal(err)
	}
	body["run_id"] = newUUID()
	if code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body); code != 429 {
		t.Fatalf("count cap %d", code)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE live_gate_reports SET observed_at=now()-interval '31 days' WHERE session_id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	if err := pruneGateReports(t.Context(), fx.pool); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_gate_reports WHERE session_id=$1`, fx.sess).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention %d %v", count, err)
	}
}

func TestDatasetTimelineWindowClampsBaseline(t *testing.T) {
	fx := newTopicFx(t)
	child := newUUID()
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-time.Hour)
	if err := db.InsertSessionAs(t.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2 WHERE id=$1`, child, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.AppendAgentEventAt(t.Context(), fx.pool, child, newUUID(), "status_changed", `{"status":"running"}`, since.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, _, cause := timelineDatasetRows(t.Context(), fx.pool, topicOwner, fx.sess, map[string]string{"since": since.Format(time.RFC3339)})
	if cause != "" || len(rows) != 1 || rows[0]["start"] != since.Format(time.RFC3339Nano) || rows[0]["status"] != "running" {
		t.Fatalf("clamped baseline %+v %s", rows, cause)
	}
}

func TestDatasetDeliveryDoesNotCollectAndRetainsOnUnavailable(t *testing.T) {
	fx := newTopicFx(t)
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Name: "blerg.gates.durations", Kind: "dataset", Parameters: map[string]string{}}}
	fence, err := datasetPrivacyFence(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	sub := &datasetSubscription{reference: q, deliveryFences: map[int64]datasetDeliveryFence{2: {privacy: fence}}}
	bc := &BrowserConn{AccountID: topicOwner, datasetSubs: map[string]*datasetSubscription{id: sub}}
	raw, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Status: "ready", BaseSequence: 1, Sequence: 2, Upserts: []protocol.DatasetRow{{"key": "held", "seconds": 5}}})
	// Full aggregate collection would fail here. The authority check reads only sessions.
	if _, err := fx.pool.Exec(t.Context(), `ALTER TABLE live_gate_reports RENAME TO held_gate_reports`); err != nil {
		t.Fatal(err)
	}
	safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, fx.pool, raw)
	var delta protocol.DatasetDelta
	if !ok || json.Unmarshal(safe, &delta) != nil || delta.Status != "ready" || len(delta.Upserts) != 1 {
		t.Fatalf("delivery collected payload %s", safe)
	}
	safe, ok = fx.api.hub.filterDatasetDelivery(t.Context(), bc, nil, raw)
	if !ok || json.Unmarshal(safe, &delta) != nil || delta.Status != "unavailable" || delta.Snapshot || len(delta.Upserts) != 0 || !sub.forceSnapshot.Load() {
		t.Fatalf("transient delivery purged rows %s", safe)
	}
}

func TestDatasetGateCLIProducer(t *testing.T) {
	fx := newWorkFx(t)
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	raw, _ := json.Marshal(map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": start.Format(time.RFC3339), "finished_at": start.Add(time.Second).Format(time.RFC3339), "report": map[string]any{"seconds": 1, "complete": true, "suites": []any{}}})
	path := filepath.Join(t.TempDir(), "gate.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "python3", "../../.claude/skills/session-messaging/blerg-runner", "gate", "report", path)
	cmd.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+server.URL, "BLERG_RUNNER_SESSION_ID="+fx.sess, "BLERG_RUNNER_SESSION_TOKEN="+fx.sessTok, "BLERG_RUNNER_DAEMON_STATE_DIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CLI %v %s", err, output)
	}
	var count int
	if err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_gate_reports WHERE session_id=$1`, fx.sess).Scan(&count); err != nil || count != 1 {
		t.Fatalf("producer persisted %d %v", count, err)
	}
}

func TestDatasetBoardCacheAndUnavailable(t *testing.T) {
	fx := newTopicFx(t)
	api, board, _ := newLiveCardAPI(t)
	viewer, ok := fx.api.verifyBrowserToken(fx.token(topicOwner, "human"))
	if !ok {
		t.Fatal("viewer")
	}
	ctx := context.WithValue(t.Context(), datasetViewerContextKey{}, viewer)
	p := map[string]string{"board": "board-a", "card": "7"}
	if cause := api.datasetCardAccess(ctx, topicOwner, fx.sess, p); cause != "" {
		t.Fatal(cause)
	}
	count := board.count()
	delivery := context.WithValue(ctx, datasetDeliveryContextKey{}, true)
	if cause := api.datasetCardAccess(delivery, topicOwner, fx.sess, p); cause != "" || board.count() != count {
		t.Fatal("delivery did not reuse its brief proof")
	}
	// A configured but malformed/transient provider response is unavailable, not denial.
	api.hub.mcpStart.set(&mcpStartConfig{Core: &liveKeyBoardList{}, Board: &fakeSessionBoard{}})
	if cause := api.datasetCardAccess(ctx, topicOwner, fx.sess, p); cause != "board_unavailable" {
		t.Fatalf("transient mapped to %s", cause)
	}
	if cause := api.datasetCardAccess(delivery, topicOwner, fx.sess, p); cause != "board_unavailable" {
		t.Fatal("failed check left successful cache")
	}
}

func TestDatasetMarkInvalidatesCardSources(t *testing.T) {
	fx := newWorkFx(t)
	first, release := fx.api.hub.datasets.watch(datasetKey{"blerg.cards.time", fx.sess})
	defer release()
	second, releaseSecond := fx.api.hub.datasets.watch(datasetKey{"blerg.reviews.pairs", fx.ended})
	defer releaseSecond()
	if code, _ := fx.postWork("marks", fx.sess, fx.sessTok, map[string]any{"kind": "finding", "text": "fixture finding"}); code != 201 && code != 200 {
		t.Fatalf("mark %d", code)
	}
	if first.revision.Load() != 1 || second.revision.Load() != 1 {
		t.Fatal("mark did not invalidate card sources")
	}
}

func TestDatasetWindowsKeepNewestBoundedRows(t *testing.T) {
	fx := newWorkFx(t)
	now := time.Now().UTC().Truncate(time.Second)
	suites := []any{}
	for i := range 128 {
		suites = append(suites, map[string]any{"suite": fmt.Sprintf("suite%03d", i), "seconds": 60, "outcome": "pass"})
	}
	raw, _ := json.Marshal(map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "report": map[string]any{"seconds": 60, "complete": true, "suites": suites}})
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report)
 SELECT $1,run,1,$2::jsonb||jsonb_build_object('run_id',run::text,'started_at',$3::timestamptz-(n+60)*interval '1 second','finished_at',$3::timestamptz-n*interval '1 second')
 FROM (SELECT gen_random_uuid() AS run,n FROM generate_series(1,80) n) r`, fx.sess, raw, now); err != nil {
		t.Fatal(err)
	}
	metadata := &datasetSampleMetadata{}
	ctx := context.WithValue(t.Context(), datasetSampleMetadataContextKey{}, metadata)
	rows, _, cause := gateDatasetRows(ctx, fx.pool, topicOwner, fx.sess, map[string]string{})
	if cause != "" || len(rows) != 77*129 || !metadata.truncated || rows[0]["end"] != now.Add(-time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("bounded newest gate rows=%d cause=%s", len(rows), cause)
	}
	child := newUUID()
	if err := db.InsertSessionAs(t.Context(), fx.pool, child, topicDaemonID, "running", "/fixture", "example/repo", "worker", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2 WHERE id=$1`, child, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	base := now.Add(-4 * time.Hour)
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload,ts)
 SELECT $1,n,gen_random_uuid(),'status_changed','{"status":"running"}'::jsonb,$2::timestamptz+n*interval '1 second' FROM generate_series(1,10005) n`, child, base); err != nil {
		t.Fatal(err)
	}
	metadata.truncated = false
	rows, _, cause = timelineDatasetRows(ctx, fx.pool, topicOwner, fx.sess, map[string]string{})
	if cause != "" || len(rows) != 10000 || !metadata.truncated || rows[0]["start"] != base.Add(10005*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("bounded newest timeline rows=%d cause=%s", len(rows), cause)
	}
}
