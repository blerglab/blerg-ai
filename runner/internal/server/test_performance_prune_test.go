package server

import (
	"context"
	"testing"
	"time"
)

func TestTestPerformancePruneFailureDoesNotSkipGatePrune(t *testing.T) {
	f := newWorkFx(t)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO live_gate_reports(session_id,run_id,revision,report,observed_at) VALUES($1,$2,1,'{}',$3)`, f.sess, newUUID(), time.Now().AddDate(0, 0, -31)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE test_runs RENAME TO held_prune_test_runs`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.pool.Exec(context.Background(), `ALTER TABLE held_prune_test_runs RENAME TO test_runs`)
	}()
	f.api.pruneDatasetSources(t.Context())
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM live_gate_reports WHERE session_id=$1`, f.sess).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("test history failure suppressed gate cleanup")
	}
}
