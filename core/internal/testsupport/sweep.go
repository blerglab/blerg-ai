package testsupport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/testfixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fixtureModule = "core"

type fixtureRun struct {
	scope testfixture.Scope
	runID string
	lease *pgx.Conn
}

var fixtureRuns = struct {
	sync.Mutex
	values map[string]*fixtureRun
}{values: make(map[string]*fixtureRun)}

func fixtureSweepAge() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv("BLERG_TEST_DB_MAX_AGE_MINUTES"))
	if raw == "" {
		return time.Hour, true
	}
	minutes, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || minutes < 1 || minutes > 10080 {
		return 0, false
	}
	return time.Duration(minutes) * time.Minute, true
}

// startFixtureRun pins an administrator connection for the entire process run. It is deliberately
// outside the copy pool, and stays on the admin database so templates remain copyable.
func startFixtureRun(ctx context.Context, cfg *pgxpool.Config) (*fixtureRun, error) {
	fixtureRuns.Lock()
	defer fixtureRuns.Unlock()
	key := cfg.ConnString()
	if r := fixtureRuns.values[key]; r != nil {
		return r, nil
	}
	conn, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	r := &fixtureRun{lease: conn}
	ok := false
	defer func() {
		if !ok {
			cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer done()
			_ = conn.Close(cleanup)
		}
	}()
	r.scope, err = readFixtureScope(ctx, conn)
	if err != nil {
		return nil, err
	}
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return nil, err
	}
	r.runID = hex.EncodeToString(value[:])
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, testfixture.LeaseKey(fixtureModule, r.runID, r.scope)).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("test fixture lease unavailable")
	}
	if age, enabled := fixtureSweepAge(); enabled { // Cleanup is best effort; fixture creation still requires its own live lease.
		_ = sweepFixtures(ctx, cfg, r.scope, time.Now().UTC(), age)
	}
	fixtureRuns.values[key] = r
	ok = true
	return r, nil
}

func (r *fixtureRun) mark(ctx context.Context, admin *pgxpool.Pool, name string) error {
	if err := r.lease.Ping(ctx); err != nil {
		return err
	}
	pinned, err := admin.Acquire(ctx)
	if err != nil {
		return err
	}
	defer pinned.Release()
	actual, err := readFixtureScope(ctx, pinned.Conn())
	if err != nil {
		return err
	}
	if actual != r.scope {
		return errors.New("test fixture administrator scope changed")
	}
	var oid, owner uint32
	if err := pinned.QueryRow(ctx, `SELECT oid,datdba FROM pg_database WHERE datname=$1`, name).Scan(&oid, &owner); err != nil {
		return err
	}
	if owner != r.scope.RoleOID {
		return errors.New("test fixture owner mismatch")
	}
	comment, err := testfixture.Encode(testfixture.Metadata{Version: 1, Module: fixtureModule, Database: name, DatabaseOID: oid, RunID: r.runID, CreatedAt: time.Now().UTC(), Scope: r.scope})
	if err != nil {
		return err
	}
	// PostgreSQL quotes both identifiers and literals; no connection string enters metadata.
	var statement string
	if err := pinned.QueryRow(ctx, `SELECT format('COMMENT ON DATABASE %I IS %L',$1::text,$2::text)`, name, comment).Scan(&statement); err != nil {
		return err
	}
	_, err = pinned.Exec(ctx, statement)
	return err
}

func closeFixtureRuns() error {
	fixtureRuns.Lock()
	defer fixtureRuns.Unlock()
	var first error
	for key, r := range fixtureRuns.values {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.lease.Close(ctx)
		cancel()
		if err != nil && first == nil {
			first = err
		}
		delete(fixtureRuns.values, key)
	}
	return first
}

func fixtureCandidate(ctx context.Context, conn *pgx.Conn, oid uint32) (testfixture.Candidate, int, error) {
	var c testfixture.Candidate
	var connections int
	err := conn.QueryRow(ctx, `SELECT d.datname,d.oid,d.datdba,d.datistemplate,coalesce(shobj_description(d.oid,'pg_database'),''),(SELECT count(*) FROM pg_stat_activity a WHERE a.datid=d.oid) FROM pg_database d WHERE d.oid=$1`, oid).Scan(&c.Database, &c.DatabaseOID, &c.OwnerOID, &c.Template, &c.Comment, &connections)
	return c, connections, err
}

// sweepFixtures only removes explicitly marked old databases in this exact admin/owner scope.
// A run lease protects even a template with no live database connections. DROP is never FORCE:
// a connection arriving after the final check makes PostgreSQL refuse deletion.
func sweepFixtures(parent context.Context, cfg *pgxpool.Config, scope testfixture.Scope, now time.Time, minAge time.Duration) error {
	if minAge < time.Minute {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg.ConnConfig.Copy())
	if err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = conn.Close(cleanup)
	}()
	actual, err := readFixtureScope(ctx, conn)
	if err != nil {
		return err
	}
	if actual != scope {
		return errors.New("test fixture administrator scope changed")
	}
	rows, err := conn.Query(ctx, `SELECT d.datname,d.oid,d.datdba,d.datistemplate,coalesce(shobj_description(d.oid,'pg_database'),'') FROM pg_database d WHERE d.datname LIKE $1`, "cx_"+fixtureModule+"_%")
	if err != nil {
		return err
	}
	var candidates []testfixture.Candidate
	for rows.Next() {
		var c testfixture.Candidate
		if err := rows.Scan(&c.Database, &c.DatabaseOID, &c.OwnerOID, &c.Template, &c.Comment); err != nil {
			rows.Close()
			return err
		}
		if _, ok := testfixture.Eligible(c, scope, fixtureModule, now, minAge); ok {
			candidates = append(candidates, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		meta, ok := testfixture.Eligible(candidate, scope, fixtureModule, now, minAge)
		if !ok {
			continue
		}
		lock := testfixture.LeaseKey(meta.Module, meta.RunID, meta.Scope)
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lock).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			continue
		}
		// Each candidate's lock is released before another candidate is considered, including errors.
		err = func() error {
			defer func() {
				cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer done()
				var unlocked bool
				_ = conn.QueryRow(cleanup, `SELECT pg_advisory_unlock($1)`, lock).Scan(&unlocked)
			}()
			current, connections, err := fixtureCandidate(ctx, conn, candidate.DatabaseOID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			checked, eligible := testfixture.Eligible(current, scope, fixtureModule, now, minAge)
			if !eligible || connections != 0 || current.Database != candidate.Database || current.Comment != candidate.Comment || checked.RunID != meta.RunID {
				return nil
			}
			_, err = conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{current.Database}.Sanitize())
			// DROP may legitimately lose a race to a new connection. Never force it or terminate backends.
			var pgerr interface{ SQLState() string }
			if errors.As(err, &pgerr) && (pgerr.SQLState() == "55006" || pgerr.SQLState() == "3D000") {
				return nil
			}
			return err
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func readFixtureScope(ctx context.Context, conn *pgx.Conn) (testfixture.Scope, error) {
	var scope testfixture.Scope
	err := conn.QueryRow(ctx, `SELECT current_database(),d.oid,current_user,r.oid FROM pg_database d CROSS JOIN pg_roles r WHERE d.datname=current_database() AND r.rolname=current_user`).Scan(&scope.Database, &scope.DatabaseOID, &scope.Role, &scope.RoleOID)
	return scope, err
}
