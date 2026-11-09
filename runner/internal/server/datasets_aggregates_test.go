package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetGateBinsBoundariesCorrectionsAndEmpty(t *testing.T) {
	rows := []protocol.DatasetRow{{"suite": "total", "seconds": float64(0)}, {"suite": "total", "seconds": float64(60)}, {"suite": "runner-unit", "seconds": float64(90)}}
	bins, cause := gateBinRows(rows, map[string]string{"width": "60"})
	if cause != "" || len(bins) != 2 || bins[0]["start"] != 0 || bins[0]["end"] != 60 || bins[1]["count"] != 1 {
		t.Fatalf("bins=%+v %s", bins, cause)
	}
	rows[1]["seconds"] = float64(59)
	bins, cause = gateBinRows(rows, map[string]string{"width": "60"})
	if cause != "" || len(bins) != 1 || bins[0]["count"] != 2 {
		t.Fatalf("corrected bins=%+v %s", bins, cause)
	}
	bins, cause = gateBinRows(nil, map[string]string{"width": "60"})
	if cause != "" || len(bins) != 0 {
		t.Fatal("absent coverage became zero")
	}
}

func TestDatasetMergeHeatmapCombinesRepeatedDSTHour(t *testing.T) {
	h := &protocol.MergeHistory{ObservedAt: time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), Commits: []protocol.GraphCommit{{SHA: fixtureSHA, Date: "2026-11-01T01:30:00-04:00"}, {SHA: "abcdef0123456789abcdef0123456789abcdef01", Date: "2026-11-01T01:30:00-05:00"}}}
	rows, cause := mergeHeatmapRows(h, map[string]string{"timezone": "America/New_York", "bucket": "hour"})
	if cause != "" || len(rows) != 1 || rows[0]["count"] != 2 || rows[0]["hour"] != 1 || rows[0]["bucket"] != "2026-11-01T05:00:00Z" || rows[0]["end"] != "2026-11-01T07:00:00Z" {
		t.Fatalf("heatmap=%+v %s", rows, cause)
	}
	h.Commits = h.Commits[:1]
	rows, cause = mergeHeatmapRows(h, map[string]string{"timezone": "America/New_York", "bucket": "hour"})
	if cause != "" || len(rows) != 1 || rows[0]["count"] != 1 {
		t.Fatalf("corrected heatmap=%+v %s", rows, cause)
	}
}

func TestDatasetWorkerTimelineCorrectsSameTimestamp(t *testing.T) {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	points := []workerTimelinePoint{{At: start.Add(time.Second), Sequence: 1, Status: "running"}, {At: start.Add(time.Second), Sequence: 2, Status: "idle"}, {At: start.Add(3 * time.Second), Sequence: 3, Status: "waiting"}}
	rows := workerTimelineRows("worker", start, start.Add(5*time.Second), points)
	if len(rows) != 3 || rows[0]["status"] != "unknown" || rows[1]["status"] != "idle" || rows[1]["end"] != start.Add(3*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("timeline=%+v", rows)
	}
	key := rows[1]["key"]
	points[1].Status = "running"
	rows = workerTimelineRows("worker", start, start.Add(5*time.Second), points)
	if rows[1]["key"] != key || rows[1]["status"] != "running" {
		t.Fatal("correction lost interval identity")
	}
}

func TestDatasetWorkerTimelineVisibilityAndWorkspace(t *testing.T) {
	fx := newTopicFx(t)
	start := time.Now().UTC().Add(-time.Minute)
	visible, hidden := newUUID(), newUUID()
	for _, spec := range []struct {
		id      string
		private bool
	}{{visible, false}, {hidden, true}} {
		if err := db.InsertSessionAs(t.Context(), fx.pool, spec.id, topicDaemonID, "running", "/fixture", "example/repo", "private-canary", "", db.SessionOrigin{SpawningAccount: topicOwner, ParentSessionID: fx.sess, Private: spec.private}); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=$2 WHERE id=$1`, spec.id, start); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.AppendAgentEventAt(t.Context(), fx.pool, spec.id, newUUID(), "status_changed", `{"status":"running","reason":"private-canary"}`, start.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.workers.timeline", Parameters: map[string]string{}}}
	var history *protocol.MergeHistory
	rows, _, cause := resolveDataset(t.Context(), fx.api.hub, fx.pool, topicOther, q, &history, false)
	if cause != "" || len(rows) != 2 {
		t.Fatalf("visible timeline=%+v %s", rows, cause)
	}
	for _, r := range rows {
		if r["worker"] != visible {
			t.Fatal("hidden worker in timeline")
		}
	}
	raw, _ := json.Marshal(rows)
	if containsCanary(raw) {
		t.Fatal("timeline exposed prose")
	}
}
func containsCanary(raw []byte) bool { return strings.Contains(string(raw), "private-canary") }
