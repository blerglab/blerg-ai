package livedata

import (
	"context"
	"encoding/json"
	"errors"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	coreidentity "github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/signing"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExternalRevocationMarkSurvivesRestartAndFence(t *testing.T) {
	root := t.TempDir()
	state, e := StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	var writers sync.WaitGroup
	for i := int64(1); i <= 16; i++ {
		writers.Go(func() {
			if e := state.AdvanceRevocations(t.Context(), i); e != nil {
				t.Error(e)
			}
		})
	}
	writers.Wait()
	if e = state.AdvanceRevocations(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	for _, fence := range []bool{false, true} {
		var next *AuthorityState
		if fence {
			next, e = FenceAuthority(root)
		} else {
			next, e = StartAuthority(root, false)
		}
		if e != nil {
			t.Fatal(e)
		}
		if mark, e := next.RevocationMark(t.Context()); e != nil || mark != 16 {
			t.Fatal("lost or lowered external revocations", mark, e)
		}
	}
	if e = state.AdvanceRevocations(t.Context(), 17); e == nil {
		t.Fatal("old process wrote through an authority fence")
	}
}

func seedRevocationGrant(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	grant := approvedFixture(testScope(), time.Now().UTC())
	s := grant.Scope
	raw, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	for _, stmt := range []string{
		`INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES('` + s.Owner + `','local','checkpoint-fixture','checkpoint@example.test','member')`,
		`INSERT INTO mcp_connections(id,account_id,name,url,auth_kind) VALUES('` + s.Connection + `','` + s.Owner + `','fixture','https://market.example/mcp','none')`,
	} {
		if _, e = pool.Exec(t.Context(), stmt); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = pool.Exec(t.Context(), `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,approved_configuration,runner_committed) VALUES($1,1,$2,$3,$4,'{"symbol":"NVDA"}','fixture',clock_timestamp(),$5,$6,true)`, s.Binding, s.Owner, s.Connection, raw, grant.ExpiresAt, grant.Configuration); e != nil {
		t.Fatal(e)
	}
}
func TestRevocationCheckpointRollbackRecovers(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	root := t.TempDir()
	state, e := StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	external := &unavailableRevocationAuthority{RevocationAuthority: state}
	r := RevocationCheckpoint{Pool: pool, Authority: external, Epoch: 1}
	if e = r.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	seedRevocationGrant(t, pool)
	tx, e := pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, e = tx.Exec(t.Context(), `DELETE FROM mcp_connections`); e != nil {
		t.Fatal(e)
	}
	external.offline.Store(true)
	if e = r.Sync(t.Context()); e != nil {
		t.Fatal(e)
	}
	external.offline.Store(false)
	if e = tx.Rollback(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = r.Sync(t.Context()); e != nil {
		t.Fatal(e)
	}
	if mark, err := state.RevocationMark(t.Context()); err != nil || mark != 0 {
		t.Fatal("acknowledged rolled-back reservation", mark, err)
	}
	if e = r.Check(t.Context()); e != nil {
		t.Fatal("rollback stranded polling", e)
	}

}

type unavailableRevocationAuthority struct {
	RevocationAuthority
	offline atomic.Bool
}

func (a *unavailableRevocationAuthority) AdvanceRevocations(ctx context.Context, mark int64) error {
	if a.offline.Load() {
		return ErrUnavailable
	}
	return a.RevocationAuthority.AdvanceRevocations(ctx, mark)
}
func (a *unavailableRevocationAuthority) RevocationMark(ctx context.Context) (int64, error) {
	if a.offline.Load() {
		return 0, ErrUnavailable
	}
	return a.RevocationAuthority.RevocationMark(ctx)
}
func TestRevocationCheckpointOutageCommitsHumanActionsAndRetries(t *testing.T) {
	for _, action := range []string{"sign out everywhere", "password reset", "account disable", "policy off", "policy delete", "connection revoke", "binding delete"} {
		t.Run(action, func(t *testing.T) {
			pool := testsupport.Pool(t, db.Migrate)
			state, err := StartAuthority(t.TempDir(), true)
			if err != nil {
				t.Fatal(err)
			}
			external := &unavailableRevocationAuthority{RevocationAuthority: state}
			checkpoint := RevocationCheckpoint{Pool: pool, Authority: external, Epoch: 1}
			if err = checkpoint.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			seedRevocationGrant(t, pool)
			scope := approvedFixture(testScope(), time.Now().UTC()).Scope
			if _, err := pool.Exec(t.Context(), `INSERT INTO live_data_policy(connection_id,tool,mode,hash) VALUES($1,$2,'allow',$3)`, scope.Connection, scope.Tool, scope.Hash); err != nil {
				t.Fatal(err)
			}
			if _, err := (Postgres{pool}).Current(t.Context(), scope.Binding, 1); err != nil {
				t.Fatal("positive authority control", err)
			}
			external.offline.Store(true)
			service := coreidentity.NewService(db.NewPgStore(pool), signing.KeyPair{})
			if _, err = service.IssueRefreshToken(t.Context(), scope.Owner, "fixture", ""); err != nil {
				t.Fatal(err)
			}
			call, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			switch action {
			case "sign out everywhere":
				err = service.RevokeAccountEverywhere(call, scope.Owner)
			case "password reset":
				tx, e := pool.Begin(call)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = tx.Rollback(t.Context()) }()
				err = service.RevokeAccountEverywhereTx(call, tx, scope.Owner)
				if err == nil {
					_, err = tx.Exec(call, `UPDATE accounts SET password_hash='changed-fixture' WHERE id=$1`, scope.Owner)
				}
				if err == nil {
					err = tx.Commit(call)
				}
			case "account disable":
				_, err = pool.Exec(call, `UPDATE accounts SET disabled_at=clock_timestamp() WHERE id=$1`, scope.Owner)
			case "policy off":
				_, err = pool.Exec(call, `UPDATE live_data_policy SET mode='off'`)
			case "policy delete":
				_, err = pool.Exec(call, `DELETE FROM live_data_policy`)
			case "connection revoke":
				_, err = pool.Exec(call, `UPDATE mcp_connections SET status='needs_auth'`)
			case "binding delete":
				_, err = pool.Exec(call, `DELETE FROM live_data_grants`)
			}
			if err != nil {
				t.Fatal("human action blocked by unavailable polling authority", err)
			}
			if action == "sign out everywhere" || action == "password reset" {
				var live int
				if e := pool.QueryRow(t.Context(), `SELECT count(*) FROM human_sessions WHERE account_id=$1 AND revoked_at IS NULL`, scope.Owner).Scan(&live); e != nil || live != 0 {
					t.Fatal("human sessions survived", live, e)
				}
			}
			if action == "password reset" {
				var hash string
				if e := pool.QueryRow(t.Context(), `SELECT password_hash FROM accounts WHERE id=$1`, scope.Owner).Scan(&hash); e != nil || hash != "changed-fixture" {
					t.Fatal("password change did not commit", e)
				}
			}
			if _, err = (Postgres{pool}).Current(t.Context(), scope.Binding, 1); !errors.Is(err, ld.ErrDenied) {
				t.Fatal("revoked grant remains authorized", err)
			}
			if err = checkpoint.Sync(t.Context()); err == nil {
				t.Fatal("outage acknowledged checkpoint")
			}
			if err = checkpoint.Check(t.Context()); err == nil {
				t.Fatal("outage served authority")
			}
			external.offline.Store(false)
			// Pending durable work must refuse authority even when the external store recovers.
			if err = checkpoint.Check(t.Context()); err == nil {
				t.Fatal("pending revocation served authority before retry")
			}
			if err = checkpoint.Sync(t.Context()); err != nil {
				t.Fatal("checkpoint retry", err)
			}
			if err = checkpoint.Check(t.Context()); err != nil {
				t.Fatal("checkpoint did not recover", err)
			}
			if mark, e := state.RevocationMark(t.Context()); e != nil || mark != 1 {
				t.Fatal("lost durable revocation retry", mark, e)
			}
			if _, err = (Postgres{pool}).Current(t.Context(), scope.Binding, 1); !errors.Is(err, ld.ErrDenied) {
				t.Fatal("retry resurrected grant", err)
			}
		})
	}
}

// Block the second authority operation explicitly; human actions must still commit.
type blockingRevocationAuthority struct {
	RevocationAuthority
	entered chan struct{}
	release chan struct{}
}

func (a blockingRevocationAuthority) AdvanceRevocations(ctx context.Context, mark int64) error {
	close(a.entered)
	select {
	case <-a.release:
		return a.RevocationAuthority.AdvanceRevocations(ctx, mark)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestRevocationCheckpointStartupDoesNotLockHumanActions(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	seedRevocationGrant(t, pool)
	state, e := StartAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	a := blockingRevocationAuthority{state, make(chan struct{}), make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- (RevocationCheckpoint{pool, a, 1}).Start(t.Context()) }()
	<-a.entered
	call, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := pool.Exec(call, `UPDATE live_data_grants SET revoked_at=clock_timestamp()`)
	close(a.release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if err != nil {
		t.Fatal("startup authority call blocked human action", err)
	}
}
func TestRevocationCheckpointStartupInvalidatesUncheckpointedRestore(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	state, e := StartAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	external := &unavailableRevocationAuthority{RevocationAuthority: state}
	r := RevocationCheckpoint{pool, external, 1}
	if e = r.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	seedRevocationGrant(t, pool)
	if _, e = pool.Exec(t.Context(), `CREATE TABLE checkpoint_snapshot AS TABLE live_data_grants`); e != nil {
		t.Fatal(e)
	}
	external.offline.Store(true)
	if _, e = pool.Exec(t.Context(), `UPDATE live_data_grants SET revoked_at=clock_timestamp()`); e != nil {
		t.Fatal(e)
	}
	if e = r.Sync(t.Context()); e == nil {
		t.Fatal("outage acknowledged revocation")
	}
	// Restore the complete pre-revocation authority state, losing the local ticket.
	for _, sql := range []string{`DELETE FROM live_data_grants`, `INSERT INTO live_data_grants SELECT * FROM checkpoint_snapshot`, `UPDATE live_data_revocation_checkpoint SET high_water=0 WHERE singleton`, `SELECT setval('live_data_revocation_requested',1,false)`} {
		if _, e = pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	if e = r.Start(t.Context()); e == nil {
		t.Fatal("outage started restored authority")
	}
	external.offline.Store(false)
	if e = r.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = r.Sync(t.Context()); e != nil {
		t.Fatal(e)
	}
	var active bool
	if e = pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM live_data_grants WHERE revoked_at IS NULL)`).Scan(&active); e != nil || active {
		t.Fatal("startup revived pre-revocation approval", active, e)
	}
	if e = r.Check(t.Context()); e != nil {
		t.Fatal("startup fence did not settle", e)
	}
}

func TestRevocationCheckpointReservationFloor(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	state, e := StartAuthority(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(t.Context(), `SELECT setval('live_data_revocation_acknowledged',7,true)`); e != nil {
		t.Fatal(e)
	}
	if e = (RevocationCheckpoint{pool, state, 1}).Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	var requested int64
	if e = pool.QueryRow(t.Context(), `SELECT last_value FROM live_data_revocation_requested`).Scan(&requested); e != nil || requested < 7 {
		t.Fatal("requested below acknowledged", requested, e)
	}
	var next int64
	if e = pool.QueryRow(t.Context(), `SELECT nextval('live_data_revocation_requested')`).Scan(&next); e != nil || next <= 7 {
		t.Fatal("next reservation reused acknowledged ticket", next, e)
	}

}

func TestRevocationCheckpointOfflineBootRestoreRevokes(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	root := t.TempDir()
	first, e := StartAuthority(root, true)
	if e != nil {
		t.Fatal(e)
	}
	r1 := RevocationCheckpoint{pool, first, 1}
	if e = r1.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	seedRevocationGrant(t, pool)

	if _, e = pool.Exec(t.Context(), `CREATE TABLE offline_grants AS TABLE live_data_grants; CREATE TABLE offline_checkpoint AS TABLE live_data_revocation_checkpoint`); e != nil {
		t.Fatal(e)
	}
	// An optional-authority outage must not prevent Core serving human actions.
	// Its failed issuer boot cannot consume the old external clean certificate.
	offline := &unavailableRevocationAuthority{RevocationAuthority: first}
	offline.offline.Store(true)
	r2 := RevocationCheckpoint{pool, offline, 1}
	if e = r2.Start(t.Context()); e == nil {
		t.Fatal("offline boot started issuer")
	}
	if _, e = pool.Exec(t.Context(), `UPDATE live_data_grants SET revoked_at=clock_timestamp()`); e != nil {
		t.Fatal(e)
	}
	// Crash that Core and restore the complete earlier snapshot before recovery.
	for _, sql := range []string{`DELETE FROM live_data_grants`, `INSERT INTO live_data_grants SELECT * FROM offline_grants`, `DELETE FROM live_data_revocation_checkpoint`, `INSERT INTO live_data_revocation_checkpoint SELECT * FROM offline_checkpoint`, `SELECT setval('live_data_revocation_requested',1,false)`} {
		if _, e = pool.Exec(t.Context(), sql); e != nil {
			t.Fatal(e)
		}
	}
	next, e := StartAuthority(root, false)
	if e != nil {
		t.Fatal(e)
	}
	r3 := RevocationCheckpoint{pool, next, 1}
	if e = r3.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	var active bool
	if e = pool.QueryRow(t.Context(), `SELECT revoked_at IS NULL FROM live_data_grants`).Scan(&active); e != nil || active {
		t.Fatal("offline boot restored a revoked approval", active, e)
	}
}
