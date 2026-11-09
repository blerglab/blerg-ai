package server

import (
	"testing"
	"time"
)

func TestDatasetGateAllowsFiveSecondClockSkew(t *testing.T) {
	fx := newWorkFx(t)
	for _, offset := range []time.Duration{4 * time.Second, 30 * time.Second} {
		finish := time.Now().UTC().Add(offset)
		body := map[string]any{"run_id": newUUID(), "revision": 1, "commit": fixtureSHA, "started_at": finish.Add(-time.Minute).Format(time.RFC3339Nano), "finished_at": finish.Format(time.RFC3339Nano), "report": map[string]any{"seconds": 60, "complete": true, "suites": []any{}}}
		code, _ := fx.postWork("gate-reports", fx.sess, fx.sessTok, body)
		expected := 201
		if offset > 5*time.Second {
			expected = 400
		}
		if code != expected {
			t.Fatalf("finish offset %s: got %d, want %d", offset, code, expected)
		}
	}
}
