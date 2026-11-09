package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestDatasetGateTruncationCauseAndRecovery(t *testing.T) {
	fx := newWorkFx(t)
	if _, err := fx.pool.Exec(t.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report)
 SELECT $1,run,1,jsonb_build_object('run_id',run::text,'revision',1,'commit',$2::text,'started_at',$3::timestamptz-interval '60 seconds','finished_at',$3::timestamptz,'report',jsonb_build_object('seconds',60,'complete',true,'suites','[]'::jsonb))
 FROM (SELECT gen_random_uuid() AS run FROM generate_series(1,80)) r`, fx.sess, fixtureSHA, time.Now().UTC().Add(-time.Minute).Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}
	fx.mux.HandleFunc("/ws/browser", fx.api.hub.ServeBrowser(fx.pool, allowWSForTest))
	server := httptest.NewServer(fx.mux)
	defer server.Close()
	socket := dialBrowserTestServer(t, server)
	defer func() { _ = socket.Close() }()
	readMessageWithDeadline(t, socket)
	q := protocol.DatasetSubscribe{Type: "subscribe_dataset", SubscriptionID: newUUID(), SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.gates.durations", Parameters: map[string]string{}}}
	if err := socket.WriteJSON(q); err != nil {
		t.Fatal(err)
	}
	next := func() protocol.DatasetDelta {
		t.Helper()
		for {
			var d protocol.DatasetDelta
			if err := json.Unmarshal(readMessageWithDeadline(t, socket), &d); err != nil {
				t.Fatal(err)
			}
			if d.Type == "dataset_delta" {
				return d
			}
		}
	}
	first := next()
	fx.api.hub.mu.RLock()
	var sub *datasetSubscription
	for _, browser := range fx.api.hub.browsers {
		browser.subMu.Lock()
		if candidate := browser.datasetSubs[q.SubscriptionID]; candidate != nil {
			sub = candidate
		}
		browser.subMu.Unlock()
	}
	fx.api.hub.mu.RUnlock()
	if sub != nil {
		t.Cleanup(func() {
			_ = socket.Close()
			select {
			case <-sub.done:
			case <-time.After(3 * time.Second):
				t.Error("dataset subscription did not close")
			}
		})
	}
	if first.Status != "ready" || first.Cause != "truncated" || len(first.Upserts) != 77 {
		t.Fatalf("silent gate cut: status=%s cause=%s rows=%d", first.Status, first.Cause, len(first.Upserts))
	}
	// Remove only the three runs outside the response. Rows stay identical, but
	// the truncation metadata must still emit its recovery to full coverage.
	if _, err := fx.pool.Exec(t.Context(), `DELETE FROM live_gate_reports WHERE session_id=$1 AND run_id IN (SELECT run_id FROM live_gate_reports WHERE session_id=$1 ORDER BY run_id DESC LIMIT 3)`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.api.hub.datasets.invalidate("blerg.gates.durations", fx.sess)
	second := next()
	if second.Status != "ready" || second.Cause != "" || second.Sequence <= first.Sequence {
		t.Fatalf("truncation did not recover: %+v", second)
	}
}
