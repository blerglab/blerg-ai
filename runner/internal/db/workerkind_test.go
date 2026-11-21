package db_test

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestWorkerKindPersistsAcrossRevival(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		t.Run(map[bool]string{false: "daemon", true: "cluster"}[cluster], func(t *testing.T) {
			pool := connect(t)
			ctx := t.Context()
			const daemon = "00000000-0000-4000-8000-000000000001"
			const session = "00000000-0000-4000-8000-000000000002"
			if err := db.UpsertDaemon(ctx, pool, daemon, "worker", "local", "/repos"); err != nil {
				t.Fatal(err)
			}
			insert := db.InsertSessionAs
			if cluster {
				insert = db.InsertClusterSessionAs
			}
			if err := insert(ctx, pool, session, daemon, "starting", "/repos/app", "app", "BLG-123", "", db.SessionOrigin{WorkerKind: "role"}); err != nil {
				t.Fatal(err)
			}
			// A runtime announces itself without purpose metadata after reconnecting.
			if err := insert(ctx, pool, session, daemon, "running", "/repos/app", "app", "BLG-123", "", db.SessionOrigin{}); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetSession(ctx, pool, session)
			if err != nil || row.WorkerKind != "role" {
				t.Fatalf("revived kind: %+v, %v", row, err)
			}
			rows, err := db.ListSessions(ctx, pool)
			if err != nil || len(rows) != 1 || rows[0].WorkerKind != "role" {
				t.Fatalf("listed kind: %+v, %v", rows, err)
			}
		})
	}
}
