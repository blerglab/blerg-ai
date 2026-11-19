package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type liveProcessConfig struct{ Root, CoreDB, RunnerDB, CoreAddress, RunnerAddress, MarketURL string }
type liveProcessCommand struct {
	Action, Token, Attempt, Mode string
	Revision                     ld.Revision
}
type liveProcessReply struct {
	GatewayURL                                                      string
	Interrupted, Pending                                            int
	Ready, Available, Enabled, Retained                             bool
	Error, Cause, Connection, Workspace, BrowserURL, Attempt, Token string
	Tokens                                                          map[string]string
	Revision                                                        ld.Revision
	Rows                                                            json.RawMessage
	Calls, Lists, Audits, SecretAudits                              int
	RetrySeconds                                                    float64
	PayloadAt                                                       string
}

func liveProcessClient(identity *livetls.Identity, c liveProcessConfig) *http.Client {
	client := identity.Client()
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
func TestLiveDataRunnerProcessFixture(t *testing.T) {
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
	cfg.ConnConfig.Database = config.RunnerDB
	pool, e := pgxpool.NewWithConfig(t.Context(), cfg)
	if e != nil {
		t.Fatal("process database")
	}
	defer pool.Close()
	if db.RunMigrations(t.Context(), pool) != nil {
		t.Fatal("Runner process migrations")
	}
	fx := newTopicFxWithPool(t, pool)
	identity, e := livetls.Load(filepath.Join(config.Root, "tls", "runner"), "runner")
	if e != nil {
		t.Fatal("Runner process identity")
	}
	fx.api.coreInternalKey = "process-internal-canary"
	fx.api.SetLiveDataDesktop(true)
	fx.api.SetLiveDataDefinitionIdentity(identity.PeerURI)
	fx.api.SetLiveDataPeerVerifier(identity.VerifyRequest)
	authority := &ld.ServiceClient{URL: "https://blerg-core-live-data:8443", HTTP: liveProcessClient(identity, config)}
	fx.api.SetLiveDataAuthority(authority)
	fx.api.liveDataPollingEnabled = true
	fx.api.hub.liveDataPollingEnabled.Store(true)
	mux := http.NewServeMux()
	fx.api.RegisterMCPRoutes(mux)
	fd := os.NewFile(3, "live-data-listener")
	listener, e := net.FileListener(fd)
	_ = fd.Close()
	if e != nil {
		t.Fatal("fixture listener")
	}
	server := &http.Server{Handler: identity.Handler(mux), ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(tls.NewListener(listener, identity.ServerTLS())) }()
	defer func() { _ = server.Close(); <-done }()
	fx.mux.HandleFunc("/ws/browser", fx.api.hub.ServeBrowser(pool, fx.api.AuthorizeBrowserWS))
	browser := httptest.NewServer(fx.mux)
	defer browser.Close()
	gateway := mcpgw.New(mcpgw.Config{DB: pool, Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}}})
	sandbox := httptest.NewServer(gateway.Handler())
	defer sandbox.Close()
	fx.api.SetMCPGateway(&MCPGatewayHandle{Gateway: gateway}, sandbox.URL, "https://blerg-core-live-data:8443", fx.api.coreInternalKey)
	fx.api.RegisterMCPGatewayRoutes(fx.mux, true)
	attempts := livepoll.Attempts{Pool: pool}
	verifier := &livepoll.Verifier{Enabled: true, Desktop: true, Installation: identity.Installation, Runner: identity.Runner, Authority: authority, Attempts: attempts, Provider: gateway.LiveProvider(authority)}
	scheduler := &livepoll.Scheduler{Enabled: true, Store: attempts, Minter: authority, Executor: verifier}
	out := json.NewEncoder(os.Stdout)
	in := json.NewDecoder(os.Stdin)
	emit := func(r liveProcessReply) {
		if out.Encode(r) != nil {
			t.Fatal("fixture response")
		}
	}
	emit(liveProcessReply{Ready: true, GatewayURL: sandbox.URL, Workspace: fx.sess, BrowserURL: browser.URL, Tokens: map[string]string{"owner": fx.token(mcpFixtureOwner, "human"), "other": fx.token(topicOther, "human"), "admin": fx.token("proof-admin", "human", "runner.admin", "board.admin", "card.read", "card.write")}})
	var queuedRows json.RawMessage
	var active ld.Revision
	var lease livepoll.Lease
	var running <-chan liveProcessReply
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
			<-running
		}
	}()
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
		case "load existing":
			active = command.Revision
			e = pool.QueryRow(t.Context(), `SELECT workspace_id::text FROM live_mcp_binding_state WHERE binding_id=$1`, active.Scope.Binding).Scan(&fx.sess)
			reply.Workspace = fx.sess
		case "audit unavailable":
			_, e = pool.Exec(t.Context(), `CREATE FUNCTION proof_attempt_unavailable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture_attempt_unavailable'; END $$; CREATE TRIGGER proof_attempt_unavailable BEFORE INSERT ON live_mcp_attempts FOR EACH ROW EXECUTE FUNCTION proof_attempt_unavailable()`)
		case "audit restored":
			_, e = pool.Exec(t.Context(), `DROP TRIGGER proof_attempt_unavailable ON live_mcp_attempts; DROP FUNCTION proof_attempt_unavailable()`)
		case "activate":
			active = command.Revision
			queuedRows = nil
			if _, err := pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET enabled=false,demand_until=clock_timestamp() WHERE binding_id<>$1`, active.Scope.Binding); err != nil {
				t.Fatal("fixture previous viewer cleanup")
			}
			e = attempts.Activate(t.Context(), active, fx.sess)
		case "demand":
			e = attempts.Demand(t.Context(), active.Scope.Owner, fx.sess, active.Scope.Binding)
		case "due":
			_, e = pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp(),next_due_at=clock_timestamp()-interval '1 day' WHERE binding_id=$1`, active.Scope.Binding)
		case "advance cadence":
			_, e = pool.Exec(t.Context(), `UPDATE live_mcp_attempts SET dispatched_at=clock_timestamp()-interval '2 minutes' WHERE binding_id=$1 AND dispatched_at IS NOT NULL`, active.Scope.Binding)
			if e == nil {
				_, e = pool.Exec(t.Context(), `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp(),next_due_at=clock_timestamp() WHERE binding_id=$1`, active.Scope.Binding)
			}
		case "seed expired":
			active = command.Revision
			raw, _ := json.Marshal(active.Scope)
			_, e = pool.Exec(t.Context(), `INSERT INTO live_mcp_binding_state(binding_id,revision,scope,enabled,fence,lease_until,demand_until,workspace_id,expires_at,last_payload,payload_at) VALUES($1,$2,$3,true,1,clock_timestamp(),clock_timestamp(),$4,$5,'[{"key":"quote","value":185.25}]',clock_timestamp()-interval '1 hour')`, active.Scope.Binding, active.Scope.Revision, raw, fx.sess, active.ExpiresAt)
		case "mint revision":
			reply.Token, e = authority.Mint(t.Context(), command.Revision.Scope.Binding, command.Revision.Scope.Revision, false)
		case "quota":
			e = liveProcessQuota(t.Context(), pool, active, fx.sess, command.Mode)
		case "reconcile":
			e = attempts.Reconcile(t.Context())
		case "attempt counts":
			e = pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE outcome='interrupted'),count(*) FILTER(WHERE outcome='undispatched') FROM live_mcp_attempts`).Scan(&reply.Interrupted, &reply.Pending)
		case "age interrupted":
			_, e = pool.Exec(t.Context(), `UPDATE live_mcp_attempts SET dispatched_at=clock_timestamp()-interval '31 seconds' WHERE binding_id=$1 AND outcome='dispatched'`, active.Scope.Binding)
		case "mint":
			reply.Token, e = authority.Mint(t.Context(), active.Scope.Binding, active.Scope.Revision, false)
		case "attempt":
			var leases []livepoll.Lease
			leases, e = attempts.Claim(t.Context())
			if e == nil && len(leases) == 1 {
				lease = leases[0]
				lease.Attempt, e = attempts.Create(t.Context(), lease.Scope, lease.Fence)
				reply.Attempt = lease.Attempt
				if e == nil {
					reply.Token, e = authority.Mint(t.Context(), lease.Scope.Binding, lease.Scope.Revision, false)
					if e == nil {
						snapshot, err := authority.Current(t.Context(), lease.Scope)
						if err != nil {
							e = err
						} else {
							e = attempts.ObserveEpoch(t.Context(), snapshot.Grant.Scope)
						}
					}

				}
			} else {
				reply.Error = "fixture_no_lease"
			}
		case "start", "tick":
			if running != nil {
				t.Fatal("fixture work already active")
			}
			ctx, stop := context.WithCancel(t.Context())
			cancel = stop
			result := make(chan liveProcessReply, 1)
			running = result
			if command.Action == "tick" {
				go func() {
					err := scheduler.Tick(ctx)
					r := liveProcessReply{}
					if err != nil {
						r.Cause = livepoll.FailureCause(err)
					}
					result <- r
				}()
			} else {
				token, id := command.Token, command.Attempt
				go func() {
					rows, err := verifier.Execute(ctx, token, id)
					r := liveProcessReply{Rows: rows}
					if err != nil {
						r.Cause = livepoll.FailureCause(err)
					}
					result <- r
				}()
			}
		case "cancel":
			if cancel != nil {
				cancel()
			}
		case "finish":
			if running == nil {
				t.Fatal("fixture work missing")
			}
			reply = <-running
			cancel()
			running = nil
			cancel = nil
		case "receipt":
			_, e = authority.ProviderCredential(t.Context(), ld.ProviderRequest{Token: command.Token, Attempt: command.Attempt, Stage: command.Mode})
		case "capture queued":
			e = pool.QueryRow(t.Context(), `SELECT last_payload FROM live_mcp_binding_state WHERE binding_id=$1`, active.Scope.Binding).Scan(&queuedRows)
		case "lifecycle":
			statements := map[string]string{
				"demand expired":  `UPDATE live_mcp_binding_state SET demand_until=clock_timestamp()-interval '1 second' WHERE binding_id=$1`,
				"lease expired":   `UPDATE live_mcp_binding_state SET lease_until=clock_timestamp()-interval '1 second' WHERE binding_id=$1`,
				"new fence":       `UPDATE live_mcp_binding_state SET fence=fence+1 WHERE binding_id=$1`,
				"attempt expired": `UPDATE live_mcp_attempts SET due_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE binding_id=$1 AND outcome='undispatched'`,
				"idle week":       `UPDATE live_mcp_binding_state SET last_view_at=clock_timestamp()-interval '8 days' WHERE binding_id=$1`,
				"sample expired":  `UPDATE live_mcp_binding_state SET payload_at=clock_timestamp()-interval '25 hours' WHERE binding_id=$1`,
			}
			statement, ok := statements[command.Mode]
			if !ok {
				t.Fatal("unknown lifecycle fixture")
			}
			_, e = pool.Exec(t.Context(), statement, active.Scope.Binding)
		case "clock offset":
			switch command.Mode {
			case "expired":
				verifier.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
			case "skew":
				verifier.Now = func() time.Time { return time.Now().Add(31 * time.Second) }
			default:
				verifier.Now = nil
			}
		case "scope", "queued":
			principal := active.Scope.Owner
			if command.Mode == "other" {
				principal = topicOther
			}
			if command.Mode == "admin" {
				principal = "proof-admin"
			}
			q := protocol.DatasetSubscribe{SessionID: fx.sess, Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": active.Scope.Binding}}}
			if command.Action == "scope" {
				scope, cause := fx.api.hub.mcpDatasetAuthority(t.Context(), pool, principal, fx.sess, active.Scope.Binding)
				reply.Available = scope.Valid() && cause == ""
				reply.Cause = cause
			} else {
				id, epoch := newUUID(), newUUID()
				bc := &BrowserConn{AccountID: principal, datasetSubs: map[string]*datasetSubscription{id: {reference: q, epoch: epoch, deliveryFences: map[int64]datasetDeliveryFence{1: {liveScope: active.Scope, validUntil: time.Now().Add(time.Hour)}}}}}
				var rows []protocol.DatasetRow
				raw := queuedRows
				if raw == nil {
					if err := pool.QueryRow(t.Context(), `SELECT last_payload FROM live_mcp_binding_state WHERE binding_id=$1`, active.Scope.Binding).Scan(&raw); err != nil {
						t.Fatal("fixture queued sample")
					}
				}
				if json.Unmarshal(raw, &rows) != nil {
					t.Fatal("fixture queued rows")
				}

				frame, _ := json.Marshal(protocol.DatasetDelta{Type: "dataset_delta", SessionID: fx.sess, SubscriptionID: id, Epoch: epoch, Sequence: 1, Snapshot: true, Status: "ready", Upserts: rows})
				safe, ok := fx.api.hub.filterDatasetDelivery(t.Context(), bc, pool, frame)
				reply.Available = ok
				reply.Rows = safe
			}
		case "sample":
			e = pool.QueryRow(t.Context(), `SELECT cause,enabled,last_payload IS NOT NULL,coalesce(last_payload,'null'::jsonb),EXTRACT(EPOCH FROM next_due_at-clock_timestamp()),coalesce(payload_at::text,'') FROM live_mcp_binding_state WHERE binding_id=$1`, active.Scope.Binding).Scan(&reply.Cause, &reply.Enabled, &reply.Retained, &reply.Rows, &reply.RetrySeconds, &reply.PayloadAt)
		case "maintain":
			e = attempts.Maintain(t.Context())
		default:
			reply.Error = "unknown_fixture_action"
		}
		if e != nil {
			reply.Error = "fixture_operation_refused"
			reply.Cause = livepoll.FailureCause(e)
		}
		emit(reply)
	}
}
