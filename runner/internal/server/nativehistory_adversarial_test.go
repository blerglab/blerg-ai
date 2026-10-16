package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestNativeHistoryRefusesReclaimedBindingBeforeVolumeAccess(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	h := db.SessionNativeHistory{SessionID: spec.SessionID, AccountID: spec.SpawningAccountID, Engine: "codex", ClaimName: historyClaimName(spec.SessionID), ClaimUID: "fixture-deleting-claim"}
	if err := db.BindSessionNativeHistory(ctx, f.pool, h); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE session_native_history SET reclaimed_at=now() WHERE session_id=$1`, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	j := &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client(), historyPool: f.pool}
	if _, err := j.prepareNativeHistory(ctx, spec); err == nil || calls != 0 {
		t.Fatalf("reclaimed tombstone accessed storage: err=%v calls=%d", err, calls)
	}
}
