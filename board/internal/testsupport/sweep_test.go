package testsupport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/testfixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixtureAdminForTest(t *testing.T) *pgxpool.Config {
	t.Helper()
	env := "TEST_DATABASE_URL"
	if fixtureModule == "core" {
		env = "DATABASE_URL"
	}
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skip("test database configuration not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse test administrator configuration")
	}
	delete(cfg.ConnConfig.RuntimeParams, "search_path")
	delete(cfg.ConnConfig.RuntimeParams, "options")
	cfg.MaxConns = 2
	return cfg
}
func fixtureTestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("test identity failed")
	}
	return hex.EncodeToString(value[:])
}

func markedSweepFixture(t *testing.T, conn *pgx.Conn, scope testfixture.Scope, runID string, created time.Time) string {
	t.Helper()
	name := "cx_" + fixtureModule + "_" + fixtureTestID()
	if _, err := conn.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0 STRATEGY WAL_LOG"); err != nil {
		t.Fatal("create dedicated sweep fixture")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("remove dedicated sweep fixture")
		}
	})
	var oid uint32
	if err := conn.QueryRow(t.Context(), `SELECT oid FROM pg_database WHERE datname=$1`, name).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	comment, err := testfixture.Encode(testfixture.Metadata{Version: 1, Module: fixtureModule, Database: name, DatabaseOID: oid, RunID: runID, CreatedAt: created, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	var statement string
	if err := conn.QueryRow(t.Context(), `SELECT format('COMMENT ON DATABASE %I IS %L',$1::text,$2::text)`, name, comment).Scan(&statement); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
	return name
}
func fixtureExists(t *testing.T, conn *pgx.Conn, name string) bool {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestSweeperOnlyRemovesOldAbandonedMarkedFixtures(t *testing.T) {
	cfg := fixtureAdminForTest(t)
	conn, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal("open sweep test administrator")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	scope, err := readFixtureScope(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := markedSweepFixture(t, conn, scope, fixtureTestID(), now.Add(-2*time.Hour))
	fresh := markedSweepFixture(t, conn, scope, fixtureTestID(), now)
	unmarked := markedSweepFixture(t, conn, scope, fixtureTestID(), now.Add(-2*time.Hour))
	if _, err := conn.Exec(t.Context(), "COMMENT ON DATABASE "+pgx.Identifier{unmarked}.Sanitize()+" IS NULL"); err != nil {
		t.Fatal(err)
	}
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil {
		t.Fatal("sweep old fixtures failed")
	}
	if fixtureExists(t, conn, old) || !fixtureExists(t, conn, fresh) || !fixtureExists(t, conn, unmarked) {
		t.Fatal("sweep crossed marker or age boundary")
	}
}

func TestSweeperLeaseProtectsIdleTemplateAndActiveConnection(t *testing.T) {
	cfg := fixtureAdminForTest(t)
	conn, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal("open sweep test administrator")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	scope, err := readFixtureScope(t.Context(), conn)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	runID := fixtureTestID()
	leased := markedSweepFixture(t, conn, scope, runID, now.Add(-2*time.Hour))
	lease, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	var locked bool
	if err := lease.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, testfixture.LeaseKey(fixtureModule, runID, scope)).Scan(&locked); err != nil || !locked {
		t.Fatal("hold independent fixture lease")
	}
	active := markedSweepFixture(t, conn, scope, fixtureTestID(), now.Add(-2*time.Hour))
	activeCfg := cfg.ConnConfig.Copy()
	activeCfg.Database = active
	activeConn, err := pgx.ConnectConfig(t.Context(), activeCfg)
	if err != nil {
		t.Fatal("open active fixture")
	}
	t.Cleanup(func() { _ = activeConn.Close(context.Background()) })
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil {
		t.Fatal("sweep protected fixtures failed")
	}
	if !fixtureExists(t, conn, leased) || !fixtureExists(t, conn, active) {
		t.Fatal("sweeper deleted a live fixture")
	}
	if err := lease.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := activeConn.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := sweepFixtures(t.Context(), cfg, scope, now, time.Hour); err != nil {
		t.Fatal("sweep abandoned fixtures failed")
	}
	if fixtureExists(t, conn, leased) || fixtureExists(t, conn, active) {
		t.Fatal("abandoned fixture survived released lease and connection")
	}
}

func assertFixtureMarked(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var c testfixture.Candidate
	if err := pool.QueryRow(t.Context(), `SELECT d.datname,d.oid,d.datdba,d.datistemplate,coalesce(shobj_description(d.oid,'pg_database'),'') FROM pg_database d WHERE d.datname=current_database()`).Scan(&c.Database, &c.DatabaseOID, &c.OwnerOID, &c.Template, &c.Comment); err != nil {
		t.Fatal(err)
	}
	var meta testfixture.Metadata
	if json.Unmarshal([]byte(strings.TrimPrefix(c.Comment, testfixture.Prefix)), &meta) != nil {
		t.Fatal("fixture lacks metadata")
	}
	if _, ok := testfixture.Eligible(c, meta.Scope, fixtureModule, time.Now().UTC().Add(2*time.Hour), time.Hour); !ok {
		t.Fatal("fixture metadata does not match database identity")
	}
	var locked bool
	cfg := fixtureAdminForTest(t)
	admin, err := pgx.ConnectConfig(t.Context(), cfg.ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	if err := admin.QueryRow(t.Context(), `SELECT pg_try_advisory_lock($1)`, testfixture.LeaseKey(meta.Module, meta.RunID, meta.Scope)).Scan(&locked); err != nil || locked {
		t.Fatal("fixture run lease is not held in the administrator database")
	}
}

func TestFixtureSweepAgeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value   string
		age     time.Duration
		enabled bool
	}{{"", time.Hour, true}, {"1", time.Minute, true}, {"10080", 7 * 24 * time.Hour, true}, {"0", 0, false}, {"-1", 0, false}, {"broken", 0, false}, {"1.5", 0, false}, {"10081", 0, false}} {
		t.Setenv("BLERG_TEST_DB_MAX_AGE_MINUTES", tc.value)
		age, enabled := fixtureSweepAge()
		if age != tc.age || enabled != tc.enabled {
			t.Fatalf("age setting %q: %v %v", tc.value, age, enabled)
		}
	}
}
