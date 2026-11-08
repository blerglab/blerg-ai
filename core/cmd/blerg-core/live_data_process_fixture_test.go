package main

// These fixtures are compiled only into the test binary. Control travels over
// inherited stdio and a private config file, never a production HTTP route.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
	coreidentity "github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type liveProcessConfig struct {
	Root, CoreDB, RunnerDB, CoreAddress, RunnerAddress, MarketURL string
}
type liveProcessCommand struct {
	Action, Mode string
	Revision     ld.Revision
}
type liveProcessReply struct {
	Ready, Available     bool
	Error, Token         string
	Connection           string
	Revision             ld.Revision
	Audits, SecretAudits int
}

func liveProcessClient(i *livetls.Identity, c liveProcessConfig) *http.Client {
	client := i.Client()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, e := net.SplitHostPort(address)
		if e != nil {
			return nil, livetls.ErrIdentity
		}
		switch host {
		case "blerg-core-live-data":
			address = c.CoreAddress
		case "blerg-runner-live-data":
			address = c.RunnerAddress
		default:
			return nil, livetls.ErrIdentity
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return client
}
func TestLiveDataCoreProcessFixture(t *testing.T) {
	path := os.Getenv("BLERG_LIVE_PROCESS_CONFIG")
	if path == "" {
		t.Skip("process fixture only")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("private process config")
	}
	var config liveProcessConfig
	if json.Unmarshal(raw, &config) != nil {
		t.Fatal("process config")
	}
	cfg, e := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal("database config")
	}
	cfg.ConnConfig.Database = config.CoreDB
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal("process database")
	}
	defer pool.Close()
	if db.Migrate(t.Context(), pool) != nil {
		t.Fatal("Core process migrations")
	}
	identity, e := livetls.Load(filepath.Join(config.Root, "tls", "core"), "core")
	if e != nil {
		t.Fatal("Core process identity")
	}
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND", "")
	t.Setenv("BLERG_LIVE_DATA_AUTHORITY_DIR", filepath.Join(config.Root, "authority"))
	t.Setenv("BLERG_LIVE_DATA_DESKTOP", "true")
	deps := api.Deps{InternalKey: "process-internal-canary", LiveDataDesktop: true, LiveDataRunnerURL: "https://blerg-runner-live-data:8443", LiveDataRunnerClient: liveProcessClient(identity, config)}
	backend, e := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal("fixture vault")
	}
	policy := netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}
	deps.MCPConnections = mcpconn.NewService(db.NewPgStore(pool), backend, policy)
	signer, err := coreidentity.LoadOrGenerateSigningKey(t.Context(), db.NewPgStore(pool))
	if err != nil {
		t.Fatal("fixture human signer")
	}
	humans := coreidentity.NewService(db.NewPgStore(pool), signer)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issuer, bootstrapErr := configureLiveDataIssuer(ctx, &deps, identity, pool)
	fd := os.NewFile(3, "live-data-listener")
	listener, e := net.FileListener(fd)
	_ = fd.Close()
	if e != nil {
		t.Fatal("fixture listener")
	}
	mux := http.NewServeMux()
	livedata.Transport{Issuer: issuer, Enabled: true, Verify: identity.VerifyRequest, Provider: liveDataProvider(deps)}.Register(mux)
	server := &http.Server{Handler: identity.Handler(mux), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, identity.ServerTLS())) }()
	defer func() { _ = server.Close(); <-done }()
	out := json.NewEncoder(os.Stdout)
	in := json.NewDecoder(os.Stdin)
	emit := func(r liveProcessReply) {
		if out.Encode(r) != nil {
			t.Fatal("fixture response")
		}
	}
	emit(liveProcessReply{Ready: true, Available: bootstrapErr == nil && issuer != nil})
	for {
		var command liveProcessCommand
		if e = in.Decode(&command); errors.Is(e, io.EOF) {
			return
		}
		if e != nil {
			t.Fatal("fixture input")
		}
		reply := liveProcessReply{}
		e = nil
		switch command.Action {
		case "rotate state key":
			// Simulate an operator replacing only this fixture's persisted
			// signing key, preserving its epoch and revocation checkpoint.
			statePath := filepath.Join(config.Root, "authority", "authority.json")
			previous, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal("rotation fixture state")
			}
			var state map[string]json.RawMessage
			if json.Unmarshal(previous, &state) != nil {
				t.Fatal("rotation fixture shape")
			}
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal("rotation fixture key")
			}
			state["private"], _ = json.Marshal([]byte(key))
			state["key_id"], _ = json.Marshal("proof-rotated-key")
			next, _ := json.Marshal(state)
			temporary := filepath.Join(config.Root, "authority", "rotation.json")
			if os.WriteFile(temporary, next, 0600) != nil || os.Rename(temporary, statePath) != nil {
				t.Fatal("rotation fixture write")
			}
		case "capability":
			// Deliberately sign invalid variants with this fixture's private key.
			// No production route exposes this control or the key.
			reply.Token = liveProcessTestCapability(t, config, command.Revision, command.Mode)
		case "prepare", "prepare expired":
			r := command.Revision
			if _, e = pool.Exec(ctx, `INSERT INTO accounts(id,provider,provider_subject,email,role) VALUES($1,'local','proof-owner','proof@example.test','member') ON CONFLICT(id) DO UPDATE SET disabled_at=NULL,must_change_password=false`, r.Scope.Owner); e != nil {
				t.Fatal("fixture owner")
			}
			var connection string
			if e = pool.QueryRow(ctx, `SELECT id::text FROM mcp_connections WHERE account_id=$1 AND name='simulated-market'`, r.Scope.Owner).Scan(&connection); e != nil {
				c, err := deps.MCPConnections.Create(ctx, r.Scope.Owner, mcpconn.CreateInput{Name: "simulated-market", URL: config.MarketURL, AuthKind: "static", Secret: "Bearer process-provider-canary"})
				if err != nil {
					t.Fatal("fixture market connection")
				}
				connection = c.ID
			}
			if _, e = pool.Exec(ctx, `UPDATE mcp_connections SET status='ok' WHERE id=$1`, connection); e != nil {
				t.Fatal("fixture connection reset")
			}
			r.Scope.Connection = connection
			if deps.LiveDataAuthorityEpoch == nil || deps.LiveDataAuthorityEpoch() == 0 {
				reply.Error = "authority_unavailable"
				break
			}
			r.Scope.AuthorityEpoch = deps.LiveDataAuthorityEpoch()
			if e = pool.QueryRow(ctx, `SELECT a.live_data_epoch,c.live_data_generation FROM accounts a JOIN mcp_connections c ON c.account_id=a.id WHERE c.id=$1`, connection).Scan(&r.Scope.OwnerEpoch, &r.Scope.ConnectionGeneration); e != nil {
				t.Fatal("fixture epochs")
			}
			approvalClock := time.Now()
			if command.Action == "prepare expired" {
				r.ExpiresAt = approvalClock.Add(-time.Minute)
				approvalClock = approvalClock.Add(-time.Hour)
			}
			r, configuration, err := r.Canonical(approvalClock)
			if err != nil {
				t.Fatal("fixture canonical revision")
			}
			scope, _ := json.Marshal(r.Scope)
			if _, e = pool.Exec(ctx, `INSERT INTO live_data_owner_settings(account_id,unsafe_host_opt_in,consent_epoch) VALUES($1,true,$2) ON CONFLICT(account_id) DO UPDATE SET unsafe_host_opt_in=true,consent_epoch=EXCLUDED.consent_epoch`, r.Scope.Owner, r.Scope.AuthorityEpoch); e != nil {
				t.Fatal("fixture machine consent")
			}
			if _, e = pool.Exec(ctx, `INSERT INTO live_data_policy(connection_id,tool,mode,hash) VALUES($1,$2,'allow',$3) ON CONFLICT(connection_id,tool) DO UPDATE SET mode='allow',hash=EXCLUDED.hash`, connection, r.Scope.Tool, r.Scope.Hash); e != nil {
				t.Fatal("fixture policy")
			}
			if _, e = pool.Exec(ctx, `INSERT INTO live_data_grants(binding_id,revision,owner_id,connection_id,scope,arguments,approved_sid,approved_at,expires_at,unsafe_host_opt_in,approved_configuration,runner_committed) VALUES($1,$2,$3,$4,$5,$6,'fixture-approved-human',$9,$7,true,$8,true)`, r.Scope.Binding, r.Scope.Revision, r.Scope.Owner, connection, scope, r.Arguments, r.ExpiresAt, configuration, approvalClock); e != nil {
				t.Fatal("fixture approved grant")
			}
			reply.Revision = r
			reply.Connection = connection
		case "revoke":
			e = (livedata.Postgres{Pool: pool}).Revoke(ctx, command.Revision.Scope)
		case "disable owner":
			_, e = pool.Exec(ctx, `UPDATE accounts SET disabled_at=clock_timestamp() WHERE id=$1`, command.Revision.Scope.Owner)
		case "password reset":
			_, e = authprovider.NewLocal(db.NewPgStore(pool)).ResetPassword(ctx, "proof-owner", humans.RevokeAccountEverywhere)
		case "secret audit unavailable":
			_, e = pool.Exec(ctx, `CREATE FUNCTION proof_secret_audit_unavailable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture_secret_audit_unavailable'; END $$; CREATE TRIGGER proof_secret_audit_unavailable BEFORE INSERT ON mcp_secret_access_log FOR EACH ROW EXECUTE FUNCTION proof_secret_audit_unavailable()`)
		case "secret audit restored":
			_, e = pool.Exec(ctx, `DROP TRIGGER proof_secret_audit_unavailable ON mcp_secret_access_log; DROP FUNCTION proof_secret_audit_unavailable()`)
		case "sign out everywhere":
			e = humans.RevokeAccountEverywhere(ctx, command.Revision.Scope.Owner)
		case "connection revoked":
			e = deps.MCPConnections.Delete(ctx, command.Revision.Scope.Owner, command.Revision.Scope.Connection)
		case "needs sign in":
			_, e = pool.Exec(ctx, `UPDATE mcp_connections SET status='needs_auth' WHERE id=$1`, command.Revision.Scope.Connection)
		case "policy off", "policy propose":
			mode := "off"
			if command.Action == "policy propose" {
				mode = "propose"
			}
			_, e = pool.Exec(ctx, `UPDATE live_data_policy SET mode=$1 WHERE connection_id=$2 AND tool=$3`, mode, command.Revision.Scope.Connection, command.Revision.Scope.Tool)
		case "binding deleted":
			_, e = pool.Exec(ctx, `DELETE FROM live_data_grants WHERE binding_id=$1`, command.Revision.Scope.Binding)
		case "block revocation":
			_, e = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION proof_refuse_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture_revocation_unavailable'; END $$;
CREATE TRIGGER aaa_proof_revoke BEFORE UPDATE ON live_data_grants FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL) EXECUTE FUNCTION proof_refuse_revoke()`)
		case "unblock revocation":
			_, e = pool.Exec(ctx, `DROP TRIGGER IF EXISTS aaa_proof_revoke ON live_data_grants; DROP FUNCTION IF EXISTS proof_refuse_revoke()`)
		case "auto accept":
			_, e = pool.Exec(ctx, `UPDATE mcp_connections SET pin_policy='auto_accept' WHERE id=$1`, command.Revision.Scope.Connection)
		case "no machine consent":
			_, e = pool.Exec(ctx, `UPDATE live_data_owner_settings SET unsafe_host_opt_in=false WHERE account_id=$1`, command.Revision.Scope.Owner)
		case "fence":
			e = runLiveDataFence([]string{"--reason", "restore"})
		case "audit unavailable":
			_, e = pool.Exec(ctx, `CREATE FUNCTION proof_audit_unavailable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture_audit_unavailable'; END $$; CREATE TRIGGER proof_audit_unavailable BEFORE INSERT ON live_data_credential_audit FOR EACH ROW EXECUTE FUNCTION proof_audit_unavailable()`)
		case "audit restored":
			_, e = pool.Exec(ctx, `DROP TRIGGER proof_audit_unavailable ON live_data_credential_audit; DROP FUNCTION proof_audit_unavailable()`)
		case "audit counts":
			if e = pool.QueryRow(ctx, `SELECT count(*) FROM live_data_credential_audit`).Scan(&reply.Audits); e == nil {
				e = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log`).Scan(&reply.SecretAudits)
			}
		default:
			reply.Error = "unknown_fixture_action"
		}
		// These process cases assert the settled, checkpointed transition. Human
		// commit-before-ack and authority outage are proved separately in Core.
		// Await an explicit Sync completion rather than racing the retry ticker.
		if e == nil {
			switch command.Action {
			case "prepare", "prepare expired", "revoke", "disable owner", "password reset", "sign out everywhere", "connection revoked", "needs sign in", "policy off", "policy propose", "binding deleted", "no machine consent", "fence":
				state, stateErr := livedata.StartAuthority(filepath.Join(config.Root, "authority"), false)
				if stateErr != nil {
					e = stateErr
				} else {
					e = (livedata.RevocationCheckpoint{Pool: pool, Authority: state, Epoch: state.Epoch()}).Sync(ctx)
				}
			}
		}
		if e != nil {
			reply.Error = "fixture_operation_refused"
		}
		emit(reply)
	}
}

func liveProcessTestCapability(t *testing.T, c liveProcessConfig, r ld.Revision, mode string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.Root, "authority", "authority.json"))
	if err != nil {
		t.Fatal("fixture signing state")
	}
	var key struct {
		KeyID   string `json:"key_id"`
		Private []byte `json:"private"`
	}
	if json.Unmarshal(raw, &key) != nil || len(key.Private) != ed25519.PrivateKeySize {
		t.Fatal("fixture signing key")
	}
	now := time.Now().Unix()
	claims := ld.Claims{Scope: r.Scope, Issuer: "blerg-core-live-data", Operation: "poll", Audience: ld.Audience(r.Scope.Installation, r.Scope.Runner), IssuedAt: now, NotBefore: now, ExpiresAt: now + 300}
	header := struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
		KeyID     string `json:"kid"`
	}{"EdDSA", ld.Type, key.KeyID}
	switch mode {
	case "valid":
	case "expired":
		claims.IssuedAt -= 600
		claims.NotBefore -= 600
		claims.ExpiresAt -= 600
	case "wrong type":
		header.Type = "blerg-session+jwt"
	case "wrong algorithm":
		header.Algorithm = "HS256"
	case "unknown key":
		header.KeyID = "unknown-proof-key"
	case "wrong audience":
		claims.Audience = "another-installation"
	case "wrong owner":
		claims.Scope.Owner = "00000000-0000-4000-8000-000000000099"
	case "wrong installation":
		claims.Scope.Installation = "00000000-0000-4000-8000-000000000099"
		claims.Audience = ld.Audience(claims.Scope.Installation, claims.Scope.Runner)
	case "wrong runner":
		claims.Scope.Runner = "00000000-0000-4000-8000-000000000099"
		claims.Audience = ld.Audience(claims.Scope.Installation, claims.Scope.Runner)
	default:
		t.Fatal("unknown capability variant")
	}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key.Private), []byte(input)))
}
