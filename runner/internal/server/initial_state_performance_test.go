package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type initialQueryCounter struct{ queries atomic.Int64 }

func (c *initialQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.queries.Add(1)
	return ctx
}
func (*initialQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func seedInitialState(t *testing.T) (*Hub, func(*http.ServeMux), []db.SessionRow, *initialQueryCounter) {
	t.Helper()
	fixturePool := connectSrvTestDB(t)
	counter := &initialQueryCounter{}
	cfg := fixturePool.Config()
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	const daemon = "00000000-0000-0000-0000-000000000091"
	if err := db.UpsertDaemon(ctx, pool, daemon, "fixture", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE daemons SET desktop_owner_id='owner' WHERE id=$1`, daemon)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO sessions(id,daemon_id,status,project_path,repo,title,spawning_account_id,private,unread,starred,runtime,started_at,archived_at)
 SELECT md5(i::text)::uuid,$1,CASE WHEN i%3=0 THEN 'stopped' ELSE 'idle' END,'/repos/app','app','Fixture '||i,
 CASE WHEN i%4=0 THEN 'other' ELSE 'owner' END,i%2=0,i%3=0,i%5=0,
 CASE WHEN i%7=0 THEN 'cluster' ELSE 'daemon' END,
 now()-i*interval '1 day',CASE WHEN i%11=0 THEN now() ELSE NULL END
 FROM generate_series(1,1000) i`, daemon)
	if err != nil {
		t.Fatal(err)
	}

	// Include inaccessible foreign ownership and historical compatibility grants.
	for _, desktop := range []string{"00000000-0000-0000-0000-000000000095", "00000000-0000-0000-0000-000000000096"} {
		if err := db.UpsertDaemon(ctx, pool, desktop, "fixture variant", "local", "/repos"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE daemons SET desktop_owner_id='other' WHERE id='00000000-0000-0000-0000-000000000095'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=true WHERE id='00000000-0000-0000-0000-000000000096'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO desktop_compatibility_accounts(desktop_id,account_id) VALUES('00000000-0000-0000-0000-000000000096','owner')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET daemon_id='00000000-0000-0000-0000-000000000095' WHERE id=md5('1')::uuid`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET daemon_id='00000000-0000-0000-0000-000000000096' WHERE id=md5('3')::uuid`); err != nil {
		t.Fatal(err)
	}
	// Mixed nonempty workspace/membership state, including current revisions and
	// legacy reader attachments, must match the established scalar mapper.
	for _, i := range []string{"1", "3", "5", "7", "9"} {
		manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "fixture", Digest: "launch", Members: []protocol.WorkspaceMember{{ID: "one", Name: "app", Path: "app", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "app"}}}
		if i == "7" {
			manifest.Runtime = "cluster"
		}
		if i == "9" {
			manifest.LaunchGeneration = "legacy-fixture"
		}
		raw, e := json.Marshal(manifest)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `INSERT INTO session_workspaces(session_id,launch_generation,manifest,results) VALUES(md5($1::text)::uuid,'fixture',$2,'[{"member_id":"one","pending":true}]')`, i, raw); e != nil {
			t.Fatal(e)
		}
		manifest.Digest = "proposal"
		manifest.Revision = 1
		proposed, e := json.Marshal(manifest)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `INSERT INTO workspace_membership_operations(id,session_id,kind,phase,requested_by,principal,expected_revision,old_digest,proposed_digest,proposed,member_id,member_name,request_digest) VALUES(md5('op'||$1::text)::uuid,md5($1::text)::uuid,'add','requested','main','owner',0,'launch','proposal',$2,'two','second','request')`, i, proposed); e != nil {
			t.Fatal(e)
		}
		if i == "3" {
			if _, e = pool.Exec(ctx, `INSERT INTO session_workspace_revisions(session_id,revision,manifest,operation_id) VALUES(md5($1::text)::uuid,1,$2,md5('op'||$1::text)::uuid)`, i, proposed); e != nil {
				t.Fatal(e)
			}
			if _, e = pool.Exec(ctx, `UPDATE session_workspaces SET membership_revision=1 WHERE session_id=md5($1::text)::uuid`, i); e != nil {
				t.Fatal(e)
			}
		}
	}

	if _, err = pool.Exec(ctx, `UPDATE sessions SET parent_session_id=md5('5')::uuid WHERE id IN (SELECT md5(i::text)::uuid FROM generate_series(21,1000,20) i)`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO messages(id,session_id,kind,body,no_wait,person_required,child_escalated) SELECT md5('msg'||i::text)::uuid,md5(i::text)::uuid,'ask','Fixture question',true,i%3=0,i%7=0 FROM generate_series(1,1000,10) i`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO session_failure_alerts(session_id,occurrence,turn_seq,message_id,parent_session_id) VALUES(md5('21')::uuid,'fixture',1,md5('msg21')::uuid,md5('5')::uuid)`); err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	hub.desktopPool = pool
	rows, err := db.ListSessions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	return hub, func(mux *http.ServeMux) {
		mux.HandleFunc("/ws/browser", hub.ServeBrowser(pool, func(*http.Request) (identity.Principal, bool) {
			return identity.Principal{Claims: identity.Claims{Sub: "owner", Kind: "human"}}, true
		}))
	}, rows, counter
}

// A real socket, including both delivery-time privacy projections. Retain the
// measurement as a reproducible fixture rather than a shared-machine time gate.
func TestInitialStateSeededConnect(t *testing.T) {
	hub, mount, rows, counter := seedInitialState(t)
	mux := http.NewServeMux()
	mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	expected := map[string]protocol.SessionInfo{}
	for _, row := range rows {
		if canSeeAccount("owner", &row) {
			expected[row.ID] = hub.sessionInfoFor(context.Background(), row, "owner")
		}
	}

	for _, row := range rows {
		if row.Title != nil && *row.Title == "Fixture 3" {
			info := expected[row.ID]
			if !info.DesktopCompatibility || info.Workspace == nil {
				t.Fatal("compatibility-grant fixture did not expose its owner's workspace")
			}
		}
	}
	messageRows, err := db.ListMessagesFor(t.Context(), hub.desktopPool, defaultMessageLimit, "owner")
	if err != nil {
		t.Fatal(err)
	}
	expectedMessages := messageInfosFor(t.Context(), hub.desktopPool, messageRows, "owner")
	wantMessages, _ := json.Marshal(expectedMessages)
	for sample := 0; sample < 3; sample++ {
		before := counter.queries.Load()
		started := time.Now()
		conn := dialBrowserTestServer(t, srv)
		raw := readMessageWithDeadline(t, conn)
		elapsed := time.Since(started)
		conn.Close()
		var got protocol.InitialState
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}

		gotMessages, _ := json.Marshal(got.Messages)
		if string(gotMessages) != string(wantMessages) {
			t.Fatal("initial message answerability differs from established mapper")
		}
		if len(got.Sessions) != len(expected) {
			t.Fatalf("sessions=%d want %d", len(got.Sessions), len(expected))
		}
		for _, info := range got.Sessions {
			want, ok := expected[info.ID]
			a, _ := json.Marshal(info)
			b, _ := json.Marshal(want)
			if !ok || string(a) != string(b) {
				t.Fatalf("projection differs for %s: got %s want %s", info.ID, a, b)
			}
		}
		queries := counter.queries.Load() - before
		t.Logf("1000 seeded, %d visible, websocket connect to initial_state sample %d: %s; data_queries=%d", len(got.Sessions), sample+1, elapsed, queries)
		if queries > 40 {
			t.Fatalf("connect performed %d data queries; per-session queries returned", queries)
		}
	}
	// A second account receives every public conversation, but no private owner
	// conversation or desktop/workspace authority borrowed from its author.
	otherMux := http.NewServeMux()
	otherMux.HandleFunc("/ws/browser", hub.ServeBrowser(hub.desktopPool, func(*http.Request) (identity.Principal, bool) {
		return identity.Principal{Claims: identity.Claims{Sub: "other", Kind: "human"}}, true
	}))
	otherServer := httptest.NewServer(otherMux)
	defer otherServer.Close()
	otherExpected := map[string]protocol.SessionInfo{}
	byID := map[string]db.SessionRow{}
	for _, row := range rows {
		byID[row.ID] = row
		if canSeeAccount("other", &row) {
			otherExpected[row.ID] = hub.sessionInfoFor(t.Context(), row, "other")
		}
	}
	conn := dialBrowserTestServer(t, otherServer)
	defer conn.Close()
	var otherState protocol.InitialState
	if err := json.Unmarshal(readMessageWithDeadline(t, conn), &otherState); err != nil {
		t.Fatal(err)
	}
	if len(otherState.Sessions) != len(otherExpected) {
		t.Fatalf("other viewer sessions=%d want %d", len(otherState.Sessions), len(otherExpected))
	}
	for _, info := range otherState.Sessions {
		want, ok := otherExpected[info.ID]
		if !ok {
			t.Fatal("other viewer received owner's private session")
		}
		a, _ := json.Marshal(info)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Fatalf("other viewer parity differs for %s: got %s want %s", info.ID, a, b)
		}
		row := byID[info.ID]
		if ownsSession("owner", &row) {
			if row.Private {
				t.Fatal("owner private session leaked")
			}
			if info.Workspace != nil || len(info.WorkspaceResults) != 0 || info.RepositoryRequest != nil {
				t.Fatal("owner workspace authority leaked")
			}
			// Cluster conversation scalars are shared by the existing mapper; desktop
			// source fields are withheld for every public desktop conversation.
			if derefOrEmpty(row.Runtime) != "cluster" && (info.DaemonID != "" || info.ProjectPath != "" || info.Repo != "" || info.DesktopCompatibility || info.DesktopStatus != "" || info.DesktopStatusReason != "") {
				t.Fatal("owner desktop fields leaked")
			}
		}
	}

}

func TestInitialStateQueryFailureClosesSocket(t *testing.T) {
	pool := connectSrvTestDB(t)
	pool.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bc := &BrowserConn{ctx: ctx, cancel: cancel, send: make(chan []byte, 1)}
	sendInitialState(ctx, bc, NewHub(), pool)
	if ctx.Err() == nil {
		t.Fatal("failed initial query left socket alive with an incomplete initial state")
	}
	if len(bc.send) != 0 {
		t.Fatal("failed query published incomplete history")
	}
}

func TestInitialStateSlowQueryClosesSocket(t *testing.T) {
	for _, internal := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller_deadline", true: "internal_deadline"}[internal], func(t *testing.T) {
			pool := connectSrvTestDB(t)
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if _, err = tx.Exec(t.Context(), "LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			bc := &BrowserConn{ctx: ctx, cancel: cancel, send: make(chan []byte, 1)}
			// The caller's shorter deadline also applies to blocked SQL. No sleeps or
			// scheduler timing assertions: cancellation and no partial payload are proof.
			budget := 50 * time.Millisecond
			if internal {
				budget = 2 * initialStateTimeout
			}
			queryCtx, queryCancel := context.WithTimeout(t.Context(), budget)
			defer queryCancel()
			sendInitialState(queryCtx, bc, NewHub(), pool)
			if internal && queryCtx.Err() != nil {
				t.Fatal("collection depended on caller timeout instead of its own deadline")
			}
			if ctx.Err() == nil {
				t.Fatal("query timeout did not close socket")
			}
			if len(bc.send) != 0 {
				t.Fatal("query timeout published incomplete history")
			}
		})
	}
}

func TestInitialStateFailureLogsSafeCause(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewTextHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	logInitialStateFailure("sessions", &pgconn.PgError{Code: "42501", Message: "canary-secret", Detail: "canary-secret"})
	logInitialStateFailure("sessions", context.DeadlineExceeded)
	logInitialStateFailure("sessions", context.Canceled)
	logInitialStateFailure("sessions", context.Canceled)
	text := out.String()
	if strings.Count(text, "cause=canceled") != 1 || !strings.Contains(text, "suppressed=0") {
		t.Fatal("repeated failure diagnostics are not bounded")
	}
	for _, want := range []string{"stage=sessions", "postgres_42501", "deadline_exceeded", "canceled"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "canary-secret") {
		t.Fatal("raw database detail leaked")
	}
}

func TestInitialStateDebugTiming(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bc := &BrowserConn{ctx: ctx, cancel: cancel, send: make(chan []byte, 1)}
	sendInitialState(ctx, bc, NewHub(), nil)
	if !strings.Contains(out.String(), "duration_ms=") || !strings.Contains(out.String(), "level=DEBUG") {
		t.Fatal("initial state collection has no debug timing")
	}
}

func TestInitialStateUnownedDesktop(t *testing.T) {
	for _, compatibility := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "compatibility_without_grant"}[compatibility], func(t *testing.T) {
			pool := connectSrvTestDB(t)
			const desktop = "00000000-0000-0000-0000-000000000093"
			const session = "00000000-0000-0000-0000-000000000094"
			if err := db.UpsertDaemon(t.Context(), pool, desktop, "fixture", "local", "/repos"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id=NULL,desktop_compatibility=$2 WHERE id=$1`, desktop, compatibility); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertSession(t.Context(), pool, session, desktop, "idle", "/repos/app", "app", "Fixture", ""); err != nil {
				t.Fatal(err)
			}
			rows, err := db.ListBrowserSessions(t.Context(), pool, "viewer", nil)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := db.LoadBrowserProjection(t.Context(), pool, "viewer", rows, true)
			if err != nil {
				t.Fatalf("unknown desktop authority must project as inaccessible, not abort history: %v", err)
			}
			if projection.Accessible[desktop] {
				t.Fatal("unowned desktop without a grant is accessible")
			}
		})
	}
}
