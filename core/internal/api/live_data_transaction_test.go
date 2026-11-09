package api

import (
	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
	"net/http/httptest"
	"testing"
	"time"
)

// Model a token/session that becomes invalid after initial authentication but
// before the approval transaction acquires its session fence. No clock sleep or
// scheduling race is required to exercise this second authorization boundary.
func TestLiveDataHumanTransactionRechecksAuthority(t *testing.T) {
	pool := testsupport.Pool(t, db.Migrate)
	st := db.NewPgStore(pool)
	key, e := identity.LoadOrGenerateSigningKey(t.Context(), st)
	if e != nil {
		t.Fatal(e)
	}
	d := Deps{Store: st, Identity: identity.NewService(st, key)}
	var owner, otherOwner, sid string
	if e = pool.QueryRow(t.Context(), `INSERT INTO accounts(provider,provider_subject,email) VALUES('local','tx-guard','tx-guard@example.test') RETURNING id::text`).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	// A second healthy account makes removing the account/session join visible:
	// it must never supply healthy account state for the disabled owner's chain.
	if e = pool.QueryRow(t.Context(), `INSERT INTO accounts(provider,provider_subject,email) VALUES('local','tx-other','tx-other@example.test') RETURNING id::text`).Scan(&otherOwner); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(t.Context(), `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'transaction-guard-fixture',gen_random_uuid()) RETURNING chain_id::text`, owner).Scan(&sid); e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/internal/live-data/approve", nil).WithContext(t.Context())
	p := cid.Principal{}
	p.Sub = owner
	p.Sid = sid
	p.ExpiresAt = time.Now().Add(time.Hour).Unix()
	tx, e := d.liveHumanTx(request, p)
	if e != nil {
		t.Fatal("valid principal refused", e)
	}
	_ = tx.Rollback(t.Context())
	expired := p
	expired.ExpiresAt = time.Now().Add(-time.Second).Unix()
	if tx, e = d.liveHumanTx(request, expired); e == nil {
		_ = tx.Rollback(t.Context())
		t.Fatal("token expired before transaction accepted")
	}
	for name, tc := range map[string]struct {
		change, restore string
		mutate          func(*cid.Principal)
	}{
		"wrong owner":     {mutate: func(p *cid.Principal) { p.Sub = otherOwner }},
		"wrong chain":     {mutate: func(p *cid.Principal) { p.Sid = "00000000-0000-4000-8000-000000000999" }},
		"revoked session": {change: `UPDATE human_sessions SET revoked_at=clock_timestamp() WHERE account_id=$1`, restore: `UPDATE human_sessions SET revoked_at=NULL WHERE account_id=$1`},
		"expired session": {change: `UPDATE human_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, restore: `UPDATE human_sessions SET expires_at=clock_timestamp()+interval '30 days' WHERE account_id=$1`},
		"disabled owner":  {change: `UPDATE accounts SET disabled_at=clock_timestamp() WHERE id=$1`, restore: `UPDATE accounts SET disabled_at=NULL WHERE id=$1`},
		"password change": {change: `UPDATE accounts SET must_change_password=true WHERE id=$1`, restore: `UPDATE accounts SET must_change_password=false WHERE id=$1`},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.change != "" {
				if _, e := pool.Exec(t.Context(), tc.change, owner); e != nil {
					t.Fatal(e)
				}
				defer func() {
					if _, e := pool.Exec(t.Context(), tc.restore, owner); e != nil {
						t.Fatal(e)
					}
				}()
			}
			changed := p
			if tc.mutate != nil {
				tc.mutate(&changed)
			}
			if tx, e := d.liveHumanTx(request, changed); e == nil {
				_ = tx.Rollback(t.Context())
				t.Fatal("transaction accepted changed authority")
			}
		})
	}
}
