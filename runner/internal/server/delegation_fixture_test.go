package server

// The shared fixture of the child-session tests (Tasks 2–8): one desktop daemon "laptop" with a
// sandbox and three engines, a direct parent session on it with its own session token, and the
// credentials the refusal tables need. Every helper a later task uses is defined here, so the
// tasks do not grow divergent copies.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// delegDaemonToken is the fixture's daemon master token.
const delegDaemonToken = "daemon-tok-1234567890"

type delegFx struct {
	t    *testing.T
	api  *API
	hub  *Hub
	pool *pgxpool.Pool
	mux  *http.ServeMux
	priv ed25519.PrivateKey

	dc *DaemonConn // "laptop": sandbox available, engines claude/codex/hermes, repo "app"

	// parent is a direct parent: kind agent, runtime docker, owner "owner-1", title "Parent".
	parent, parentTok string
}

// sessOpts describes one fixture session. Zero values: owner "" (none), runtime "docker",
// status "running", on fx.dc, title "Session".
type sessOpts struct {
	owner         string
	runtime       string // "docker" (default) | "daemon" | "cluster"
	private       bool
	boardID       string // a board row is created when set to "new"
	startedByKind string
	parent        string
	status        string
	title         string
	daemonID      string // default fx.dc.ID
}

func newDelegFx(t *testing.T) *delegFx {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pool := runnerContractPool(t)
	hub := NewHub()
	api := NewAPI(hub, pool, delegDaemonToken, nil, "")
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	api.SetRunnerKey(runnerTestKey)
	dc := registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000d1", "laptop", "app")
	dc.SetSandboxAvailable(true)
	dc.RecoveryReadyCapable = true
	dc.SetAvailableEngines([]string{"claude", "codex", "hermes"})
	mux := http.NewServeMux()
	api.RegisterDelegationRoutes(mux)
	RegisterRunnerContractRoutes(mux, api)
	fx := &delegFx{t: t, api: api, hub: hub, pool: pool, mux: mux, priv: priv, dc: dc}
	fx.parent, fx.parentTok = fx.session(sessOpts{owner: "owner-1", title: "Parent"})
	return fx
}

// session inserts a session row as described and mints its own session token (cap "message").
func (fx *delegFx) session(o sessOpts) (id, token string) {
	fx.t.Helper()
	ctx := context.Background()
	if o.runtime == "" {
		o.runtime = runnerRuntimeDocker
	}
	if o.status == "" {
		o.status = "running"
	}
	if o.title == "" {
		o.title = "Session"
	}
	daemonID := firstNonEmpty(o.daemonID, fx.dc.ID)
	if o.runtime == runnerRuntimeCluster {
		daemonID = clusterDaemonID
		if err := db.UpsertDaemon(ctx, fx.pool, clusterDaemonID, "cluster", "runner", ""); err != nil {
			fx.t.Fatal(err)
		}
	}
	id = newUUID()
	origin := db.SessionOrigin{SpawningAccount: o.owner, Private: o.private, ParentSessionID: o.parent}
	if o.parent != "" {
		origin.DelegationVia = "request:" + newUUID()
	}
	if err := db.InsertSessionAs(ctx, fx.pool, id, daemonID, o.status, "/repos/app", "app", o.title, "", origin); err != nil {
		fx.t.Fatal(err)
	}
	if err := db.SetSessionPosture(ctx, fx.pool, id, o.runtime, false); err != nil {
		fx.t.Fatal(err)
	}
	if err := db.SetSessionKind(ctx, fx.pool, id, "agent"); err != nil {
		fx.t.Fatal(err)
	}
	if o.startedByKind != "" {
		if err := db.SetSessionStartedBy(ctx, fx.pool, id, o.startedByKind, ""); err != nil {
			fx.t.Fatal(err)
		}
	}
	if o.boardID != "" {
		board := o.boardID
		if board == "new" {
			b, err := db.CreateBoard(ctx, fx.pool, "Board "+id[:8], nil, nil, nil)
			if err != nil {
				fx.t.Fatal(err)
			}
			board = b.ID
		}
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET board_id = $2::uuid WHERE id = $1::uuid`, id, board); err != nil {
			fx.t.Fatal(err)
		}
	}
	if o.status == "stopped" || o.status == "ended" || o.status == "error" {
		if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET ended_at = now(), end_reason = 'stopped_by_user' WHERE id = $1::uuid`, id); err != nil {
			fx.t.Fatal(err)
		}
	}
	tok, err := db.MintSessionToken(ctx, fx.pool, id, []string{"message"}, time.Hour)
	if err != nil {
		fx.t.Fatal(err)
	}
	if o.private {
		fx.hub.notePrivate(id, o.owner)
	}
	return id, tok
}

// row reads a session row back.
func (fx *delegFx) row(id string) *db.SessionRow {
	fx.t.Helper()
	row, err := db.GetSession(context.Background(), fx.pool, id)
	if err != nil || row == nil {
		fx.t.Fatalf("GetSession %s: %v", id, err)
	}
	return row
}

func (fx *delegFx) coreToken(c identity.Claims) string {
	fx.t.Helper()
	c.Aud = coreAuthAudience
	if c.ExpiresAt == 0 {
		c.ExpiresAt = time.Now().Add(time.Hour).Unix()
	}
	return mintRunnerToken(fx.t, fx.priv, "core-1", c)
}

// human is a browser login's runner token for account, stamped with a browser session.
func (fx *delegFx) human(account string) string {
	return fx.coreToken(identity.Claims{Kind: "human", Sub: account, Sid: testSID,
		Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}})
}

// agentToken is an agent token acting for account.
func (fx *delegFx) agentToken(account string) string {
	return fx.coreToken(identity.Claims{Kind: agentPrincipalKind, Sub: "tok-" + account, OnBehalfOf: account,
		Caps: []string{coreAuthRunnerCap}})
}

// assistToken is a board-scoped (assist) token of session: a board row and a token with
// caps board+message bound to it.
func (fx *delegFx) assistToken(session string) string {
	fx.t.Helper()
	ctx := context.Background()
	b, err := db.CreateBoard(ctx, fx.pool, "Assist "+newUUID()[:8], nil, nil, nil)
	if err != nil {
		fx.t.Fatal(err)
	}
	raw, _, err := db.MintBoardToken(ctx, fx.pool, b.ID, session, []string{"board", "message"}, time.Hour)
	if err != nil {
		fx.t.Fatal(err)
	}
	return raw
}

// do sends one request through fx.mux. body is JSON-encoded unless it is a string or []byte (sent
// raw). The decoded JSON object is returned when the response is one (nil otherwise).
func (fx *delegFx) do(method, path, tok string, body any, hdr map[string]string) (int, map[string]any, http.Header) {
	fx.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			fx.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	fx.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

// spawn reads one spawn_session frame from fx.dc.send, skipping other frames; fails at the readiness deadline.
func (fx *delegFx) spawn() protocol.SpawnSession {
	fx.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.SpawnSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				fx.t.Fatal(err)
			}
			if msg.Type == "spawn_session" {
				return msg
			}
		case <-deadline:
			fx.t.Fatal("no spawn_session reached the daemon within 2 s")
			return protocol.SpawnSession{}
		}
	}
}

// childStart is the fixture's default allowlisted start: Claude in the sandbox on repo "app".
func (fx *delegFx) childStart() childStart {
	return childStart{Engine: "claude", Repo: "app", Runtime: runnerRuntimeDocker, Title: "Child", Prompt: "do the thing"}
}

// startChildViaRule stores a standing rule for the parent's owner (claude, docker, fx.dc) and
// starts a child of fx.parent under it, draining its spawn frame; it returns the child id. Until
// Task 4's POST …/children route exists it calls startChild directly; Task 4 may route it through
// the POST, which then matches the same rule.
func (fx *delegFx) startChildViaRule() string {
	fx.t.Helper()
	ctx := context.Background()
	rule, err := db.InsertDelegationRule(ctx, fx.pool, db.DelegationRule{AccountID: "owner-1", Engine: "claude",
		Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID, CreatedVia: "settings"})
	if err != nil {
		fx.t.Fatal(err)
	}
	resp, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), fx.childStart(), "rule:"+rule.ID, newUUID())
	if apiErr != nil {
		fx.t.Fatalf("startChild: %d %s", apiErr.Status, apiErr.Message)
	}
	fx.spawn()
	return resp.SessionID
}

// childOf inserts a running child row of parent (owner "owner-1") and returns its id.
func (fx *delegFx) childOf(parent string) string {
	fx.t.Helper()
	id, _ := fx.session(sessOpts{owner: "owner-1", parent: parent, title: "Child"})
	return id
}
