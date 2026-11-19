package mcpconn

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"testing"
	"time"
)

func sessionIdentity(t *testing.T, e *sessionFlowEnv) (*identity.Service, string) {
	t.Helper()
	ctx := context.Background()
	st := db.NewPgStore(e.pool)
	keys, err := identity.LoadOrGenerateSigningKey(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	svc := identity.NewService(st, keys)
	raw, err := svc.IssueRefreshToken(ctx, e.auth.Binding.AccountID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var sid string
	if err = e.pool.QueryRow(ctx, `SELECT chain_id::text FROM human_sessions WHERE account_id=$1 AND token_hash<>'synthetic-session' ORDER BY created_at DESC LIMIT 1`, e.auth.Binding.AccountID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET bound_sid=$2 WHERE account_id=$1 AND runner_session_id IS NOT NULL`, e.auth.Binding.AccountID, sid); err != nil {
		t.Fatal(err)
	}
	e.auth.Binding.HumanSessionID = sid
	encoded, err := json.Marshal(e.auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET oauth_meta=jsonb_set(oauth_meta,'{session_authorization}',$2::jsonb) WHERE account_id=$1 AND runner_session_id IS NOT NULL`, e.auth.Binding.AccountID, encoded); err != nil {
		t.Fatal(err)
	}
	return svc, raw
}

func TestLogoutDisableAndExpirySuspendScopedCredentials(t *testing.T) {
	for _, mode := range []string{"logout", "logout all", "all sessions", "chain revoke", "disable", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			e, in := sessionDispatchReady(t)
			ctx := context.Background()
			svc, raw := sessionIdentity(t, e)
			var sid string
			if err := e.pool.QueryRow(ctx, `SELECT bound_sid::text FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "logout":
				err = svc.RevokeHumanSession(ctx, raw)
			case "logout all":
				err = svc.RevokeAccountEverywhere(ctx, in.AccountID)
			case "all sessions":
				err = svc.RevokeAllSessions(ctx, in.AccountID)
			case "chain revoke":
				err = svc.RevokeChain(ctx, sid, "synthetic")
			case "disable":
				var admin, adminSID string
				if err = e.pool.QueryRow(ctx, `INSERT INTO accounts(provider,provider_subject,role) VALUES('local','scope-admin','admin') RETURNING id::text`).Scan(&admin); err != nil {
					t.Fatal(err)
				}
				if err = e.pool.QueryRow(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'synthetic-admin',gen_random_uuid()) RETURNING chain_id::text`, admin).Scan(&adminSID); err != nil {
					t.Fatal(err)
				}
				err = svc.DisablePerson(ctx, admin, adminSID, in.AccountID)
			case "expiry":
				_, err = e.pool.Exec(ctx, `UPDATE human_sessions SET expires_at=now()-interval '1 second' WHERE chain_id=$1`, sid)
				if err == nil {
					err = e.svc.SweepSessionCredentials(ctx)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
			if err != nil || c.Lifecycle != "suspended" || c.Generation != in.CredentialGeneration+1 {
				t.Fatal("revocation did not fence and suspend", c, err)
			}
			var retained bool
			if err = e.pool.QueryRow(ctx, `SELECT secret_ciphertext IS NOT NULL FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&retained); err != nil || !retained {
				t.Fatal("logout erased encrypted credential", err)
			}
		})
	}
}

func TestLogoutCommitsWhenBindingSuspensionFails(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	svc, raw := sessionIdentity(t, e)
	in.HumanSessionID = e.auth.Binding.HumanSessionID
	if err := e.svc.CheckSessionDispatch(ctx, in); err != nil {
		t.Fatal("live warm admission fixture refused", err)
	}
	if _, err := e.pool.Exec(ctx, `CREATE FUNCTION reject_binding_suspend() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic binding suspension failure'; END $$;
 CREATE TRIGGER reject_binding_suspend BEFORE UPDATE ON mcp_connections FOR EACH ROW EXECUTE FUNCTION reject_binding_suspend()`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeHumanSession(ctx, raw); err != nil {
		t.Fatal("binding suspension blocked logout", err)
	}
	var sid string
	if err := e.pool.QueryRow(ctx, `SELECT bound_sid::text FROM mcp_connections WHERE id=$1`, in.ConnectionID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	in.HumanSessionID = sid
	if err := e.svc.CheckSessionDispatch(ctx, in); !errors.Is(err, ErrNotFound) {
		t.Fatal("warm dispatch survived identity revoke", err)
	}
	if _, err := e.pool.Exec(ctx, `DROP TRIGGER reject_binding_suspend ON mcp_connections`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SweepSessionCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.GetSessionCredential(ctx, in.SessionScope, in.ConnectionID)
	if err != nil || c.Lifecycle != "suspended" || c.Generation != in.CredentialGeneration+1 {
		t.Fatal("sweeper did not recover suspension", c, err)
	}
}

func TestLogoutSuspendsOnlyInitiatingLoginBindings(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	svc, raw := sessionIdentity(t, e)
	var otherSID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO human_sessions(account_id,token_hash,chain_id) VALUES($1,'other-synthetic-session',gen_random_uuid()) RETURNING chain_id::text`, in.AccountID).Scan(&otherSID); err != nil {
		t.Fatal(err)
	}
	auth := e.auth
	auth.Binding.HumanSessionID = otherSID
	auth.Binding.RunnerSessionID = "00000000-0000-4000-8000-000000000888"
	other, err := e.svc.StoreSessionOAuth(ctx, auth.Binding, SessionOAuthCredential{Authorization: &auth, Name: "other-device", URL: e.provider.MCPURL(), Issuer: e.provider.Issuer(), TokenEndpoint: e.provider.Issuer() + "/token", ClientID: "synthetic-client", AccessToken: "other-synthetic-access", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pool.Exec(ctx, `UPDATE mcp_connections SET lifecycle='active',suspended_at=NULL,cleanup_after=NULL,binding_lease_until=now()+interval '1 minute' WHERE id=$1`, other.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RevokeHumanSession(ctx, raw); err != nil {
		t.Fatal(err)
	}
	other, err = e.svc.GetSessionCredential(ctx, auth.Binding.SessionScope, other.ID)
	if err != nil || other.Lifecycle != "active" || other.Generation != 1 {
		t.Fatal("logout crossed browser login boundary", other, err)
	}
}

func TestLogoutDoesNotWaitForBindingRowLock(t *testing.T) {
	e, in := sessionDispatchReady(t)
	ctx := context.Background()
	svc, raw := sessionIdentity(t, e)
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM mcp_connections WHERE id=$1 FOR UPDATE`, in.ConnectionID); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = svc.RevokeHumanSession(bounded, raw); err != nil {
		t.Fatal("binding row lock blocked identity revocation", err)
	}
}
