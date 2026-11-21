package livedata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func TestMain(m *testing.M) { os.Exit(testsupport.Run(m)) }
func TestPostgresDurableAuthority(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	ctx := context.Background()
	grant := approvedFixture(testScope(), time.Now().UTC())
	s := grant.Scope
	raw, _ := json.Marshal(s)
	for _, statement := range []string{
		`INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES('` + s.Owner + `','local','live-test','live@example.com','member')`,
		`INSERT INTO mcp_connections(id,account_id,name,url,auth_kind) VALUES('` + s.Connection + `','` + s.Owner + `','market','https://market.example/mcp','none')`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO live_data_policy(connection_id,tool,mode,hash) VALUES($1,$2,'allow',$3)`, s.Connection, s.Tool, s.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,approved_configuration,runner_committed) VALUES($1,1,$2,$3,$4,'{"symbol":"NVDA"}','approved-fixture',clock_timestamp(),$5,$6,true)`, s.Binding, s.Owner, s.Connection, raw, grant.ExpiresAt, grant.Configuration); err != nil {
		t.Fatal(err)
	}
	store := Postgres{pool}
	if _, err := store.Current(ctx, s.Binding, 1); err != nil {
		t.Fatal("approved grant unavailable", err)
	}
	for name, change := range map[string]string{
		"disabled owner":      `UPDATE accounts SET disabled_at=clock_timestamp() WHERE id='` + s.Owner + `'`,
		"policy propose":      `UPDATE live_data_policy SET mode='propose'`,
		"policy off":          `UPDATE live_data_policy SET mode='off'`,
		"hash changed":        `UPDATE live_data_policy SET hash=repeat('c',64)`,
		"OAuth needs sign-in": `UPDATE mcp_connections SET status='needs_auth'`,
		"owner epoch":         `UPDATE accounts SET live_data_epoch=live_data_epoch+1 WHERE id='` + s.Owner + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err = tx.Exec(ctx, change); err != nil {
				t.Fatal(err)
			}
			// Inspect within the transaction: triggers must revoke before commit and
			// resetting the changed field must not resurrect the grant.
			var revoked bool
			if err := tx.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM live_data_grants WHERE binding_id=$1`, s.Binding).Scan(&revoked); err != nil || !revoked {
				t.Fatal("authority change did not revoke", err)
			}
		})
	}
	if err := store.Audit(ctx, "not-a-uuid", 1, "mint", "denied", AuditMetadata{}); err != nil {
		t.Fatal("malformed identifier broke refusal audit", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM live_data_credential_audit WHERE binding_id IS NULL AND outcome='denied'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("missing refusal audit")
	}
	keys, err := LoadKeyring("postgres-key", keyFile(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	issuer := Issuer{Store: store, Keys: keys, Installation: s.Installation, Runner: s.Runner, Posture: func(context.Context) (bool, error) { return true, nil }}
	token, err := issuer.Mint(ctx, s.Binding, 1, false)
	if err != nil || token == "" {
		t.Fatal("database-backed issuer failed", err)
	}
	if err := store.Revoke(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Current(ctx, s); err == nil {
		t.Fatal("issuer served revoked grant from cache")
	}
	if _, err := issuer.Mint(ctx, s.Binding, 1, true); err == nil {
		t.Fatal("issuer renewed revoked grant")
	}
}

func TestLiveAuditRetention(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	for _, table := range []string{"live_data_credential_audit", "live_data_approval_audit"} {
		op := "approve"
		if table == "live_data_credential_audit" {
			op = "check"
		}
		if _, err := pool.Exec(t.Context(), "INSERT INTO "+table+"(operation,outcome,created_at)VALUES($1,'denied',clock_timestamp()-interval '181 days'),($1,'denied',clock_timestamp())", op); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Postgres{Pool: pool}).Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"live_data_credential_audit", "live_data_approval_audit"} {
		var count int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Fatal("audit retention failed", err, count)
		}
	}
}

func TestPostgresRefusesSessionScopedConnection(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	ctx := context.Background()
	grant := approvedFixture(testScope(), time.Now().UTC())
	s := grant.Scope
	raw, _ := json.Marshal(s)
	if _, err := pool.Exec(ctx, `INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES($1,'local','scoped-live','scoped-live@example.com','member')`, s.Owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO mcp_connections(id,account_id,name,url,auth_kind,secret_ciphertext,key_id,runner_installation_id,runner_session_id,bound_sid,credential_generation,lifecycle,human_deadline,suspended_at,cleanup_after,refresh_fence)
 VALUES($1,$2,'scoped','https://market.example/mcp','oauth',decode('01','hex'),'synthetic','runner',$3,$4,1,'suspended',now()+interval '1 day',now(),now()+interval '168 hours',0)`, s.Connection, s.Owner, s.Binding, "00000000-0000-4000-8000-000000000003"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO live_data_policy(connection_id,tool,mode,hash) VALUES($1,$2,'allow',$3)`, s.Connection, s.Tool, s.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,approved_configuration,runner_committed) VALUES($1,1,$2,$3,$4,'{"symbol":"NVDA"}','approved-fixture',clock_timestamp(),$5,$6,true)`, s.Binding, s.Owner, s.Connection, raw, grant.ExpiresAt, grant.Configuration); err != nil {
		t.Fatal(err)
	}
	if _, err := (Postgres{pool}).Current(ctx, s.Binding, 1); !errors.Is(err, ld.ErrDenied) {
		t.Fatal("account live-data authority accepted session-scoped connection")
	}
}
