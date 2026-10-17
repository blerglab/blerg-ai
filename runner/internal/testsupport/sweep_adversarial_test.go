package testsupport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/testfixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sweepReviewAdmin(t *testing.T) (*pgxpool.Config, *pgxpool.Pool, testfixture.Scope) {
	t.Helper()
	cfg := adminConfig(t, "TEST_DATABASE_URL")
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg.Copy())
	if err != nil {
		t.Fatal("open private sweep-test administrator failed")
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal("connect private sweep-test administrator failed")
	}
	scope, err := readFixtureScope(t.Context(), conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal("read private sweep-test scope failed")
	}
	return cfg, pool, scope
}

func sweepReviewCreate(t *testing.T, admin *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0 STRATEGY WAL_LOG"); err != nil {
		t.Fatal("create disposable sweeper review database failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("remove disposable sweeper review database failed")
		}
	})
}

func sweepReviewMark(t *testing.T, admin *pgxpool.Pool, name, runID string, scope testfixture.Scope, created time.Time) {
	t.Helper()
	var oid uint32
	if err := admin.QueryRow(t.Context(), `SELECT oid FROM pg_database WHERE datname=$1`, name).Scan(&oid); err != nil {
		t.Fatal("read disposable review database identity failed")
	}
	comment, err := testfixture.Encode(testfixture.Metadata{Version: 1, Module: fixtureModule, Database: name, DatabaseOID: oid, RunID: runID, CreatedAt: created, Scope: scope})
	if err != nil {
		t.Fatal("encode disposable review metadata failed")
	}
	var statement string
	if err := admin.QueryRow(t.Context(), `SELECT format('COMMENT ON DATABASE %I IS %L',$1::text,$2::text)`, name, comment).Scan(&statement); err != nil {
		t.Fatal("quote disposable review metadata failed")
	}
	if _, err := admin.Exec(t.Context(), statement); err != nil {
		t.Fatal("mark disposable review database failed")
	}
}

func sweepReviewExists(t *testing.T, admin *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		t.Fatal("check disposable review database failed")
	}
	return exists
}

func sweepReviewRunID() string { return strings.TrimPrefix(databaseName(), "cx_runner_") }

func sweepReviewWaitBackendGone(t *testing.T, admin *pgxpool.Pool, pid uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var exists bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1)`, pid).Scan(&exists); err != nil {
			t.Fatal("wait for owned test backend shutdown failed")
		}
		if !exists {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned test backend did not finish shutdown")
		case <-ticker.C:
		}
	}
}

func TestSweepReviewLiveLeaseProtectsIdleTemplateUntilSessionEnds(t *testing.T) {
	cfg, admin, scope := sweepReviewAdmin(t)
	name, runID := databaseName(), sweepReviewRunID()
	sweepReviewCreate(t, admin, name)
	now := time.Now().UTC()
	sweepReviewMark(t, admin, name, runID, scope, now.Add(-2*time.Hour))
	lease, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal("open owned run lease failed")
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	pid := lease.PgConn().PID()
	if _, err := lease.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, testfixture.LeaseKey(fixtureModule, runID, scope)); err != nil {
		t.Fatal("hold owned run lease failed")
	}
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil || !sweepReviewExists(t, admin, name) {
		t.Fatal("sweeper deleted a live run's template with no template connections")
	}
	if err := lease.Close(t.Context()); err != nil {
		t.Fatal("close owned run lease failed")
	}
	sweepReviewWaitBackendGone(t, admin, pid)
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil || sweepReviewExists(t, admin, name) {
		t.Fatal("abandoned marked fixture was not swept after its lease session ended")
	}
}

func TestSweepReviewActiveDatabaseConnectionIsNeverForcedClosed(t *testing.T) {
	cfg, admin, scope := sweepReviewAdmin(t)
	name := databaseName()
	sweepReviewCreate(t, admin, name)
	now := time.Now().UTC()
	sweepReviewMark(t, admin, name, sweepReviewRunID(), scope, now.Add(-2*time.Hour))
	activeCfg := cfg.ConnConfig.Copy()
	activeCfg.Database = name
	active, err := pgx.ConnectConfig(t.Context(), activeCfg)
	if err != nil {
		t.Fatal("open owned active fixture connection failed")
	}
	t.Cleanup(func() { _ = active.Close(context.Background()) })
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil || !sweepReviewExists(t, admin, name) {
		t.Fatal("sweeper removed an actively connected fixture")
	}
	if err := active.Ping(t.Context()); err != nil {
		t.Fatal("sweeper terminated the active fixture backend")
	}
}

func TestSweepReviewWrongAdminNamespaceCannotBypassLiveLease(t *testing.T) {
	cfg, admin, scope := sweepReviewAdmin(t)
	name, runID := databaseName(), sweepReviewRunID()
	sweepReviewCreate(t, admin, name)
	now := time.Now().UTC()
	sweepReviewMark(t, admin, name, runID, scope, now.Add(-2*time.Hour))
	lease, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal("open owned run lease failed")
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	if _, err := lease.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, testfixture.LeaseKey(fixtureModule, runID, scope)); err != nil {
		t.Fatal("hold owned run lease failed")
	}
	otherAdmin := "cx_hygiene_review_" + sweepReviewRunID()
	sweepReviewCreate(t, admin, otherAdmin)
	otherCfg := cfg.Copy()
	otherCfg.ConnConfig.Database = otherAdmin
	_ = sweepFixtures(t.Context(), otherCfg, scope, now, time.Hour)
	if !sweepReviewExists(t, admin, name) {
		t.Fatal("wrong admin database bypassed the live run's database-scoped advisory lease")
	}
	if err := lease.Ping(t.Context()); err != nil {
		t.Fatal("wrong-scope sweep disturbed the original lease session")
	}
}

func TestSweepReviewUnmarkedFreshAndWrongScopeSurvive(t *testing.T) {
	cfg, admin, scope := sweepReviewAdmin(t)
	now := time.Now().UTC()
	for _, kind := range []string{"unmarked legacy", "fresh", "wrong scope", "abandoned"} {
		t.Run(kind, func(t *testing.T) {
			name := databaseName()
			sweepReviewCreate(t, admin, name)
			if kind != "unmarked legacy" {
				created := now.Add(-2 * time.Hour)
				markedScope := scope
				if kind == "fresh" {
					created = now
				}
				if kind == "wrong scope" {
					markedScope.Database += "_wrong"
				}
				sweepReviewMark(t, admin, name, sweepReviewRunID(), markedScope, created)
			}
			if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil {
				t.Fatal("sweep disposable review fixtures failed")
			}
			if got := sweepReviewExists(t, admin, name); got != (kind != "abandoned") {
				t.Fatalf("unsafe cleanup eligibility for %s: exists=%v", kind, got)
			}
		})
	}
}
