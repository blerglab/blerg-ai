package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/idempotency"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// delegateProbe mounts delegateParent alone on GET /test/{id}: 200 {"parent","daemon"} when it
// admits the request. Task 4 re-runs the same table against the real children route.
func (fx *delegFx) delegateProbe(opts delegateOpts) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test/{id}", func(w http.ResponseWriter, r *http.Request) {
		parent, dc, ok := fx.api.delegateParent(w, r, opts)
		if !ok {
			return
		}
		daemon := ""
		if dc != nil {
			daemon = dc.ID
		}
		writeJSON(w, http.StatusOK, map[string]string{"parent": parent.ID, "daemon": daemon})
	})
	return mux
}

func probe(t *testing.T, mux *http.ServeMux, sid, tok string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/test/"+sid, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestDelegateCredential(t *testing.T) {
	fx := newDelegFx(t)
	mux := fx.delegateProbe(delegateOpts{})
	_, otherTok := fx.session(sessOpts{owner: "owner-1"})
	child, childTok := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	ownerless, ownerlessTok := fx.session(sessOpts{owner: ""})
	revoked, revokedTok := fx.session(sessOpts{owner: "owner-1"})
	if err := db.RevokeBoardTokensForSession(context.Background(), fx.pool, revoked); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		sid, tok string
		want     int
		msg      string
	}{
		"own session token":       {fx.parent, fx.parentTok, 200, ""},
		"no credential":           {fx.parent, "", 401, ""},
		"another session's token": {fx.parent, otherTok, 403, "only for its own session"},
		"daemon master token":     {fx.parent, delegDaemonToken, 403, "use the session's own token"},
		"assist board token":      {fx.parent, fx.assistToken(fx.parent), 403, "a board token cannot start sessions"},
		"agent token":             {fx.parent, fx.agentToken("owner-1"), 401, ""},
		"browser login":           {fx.parent, fx.human("owner-1"), 401, ""},
		"runner key":              {fx.parent, runnerTestKey, 401, ""},
		"a child's own token":     {child, childTok, 403, "a started session cannot start sessions"},
		"owner-less parent":       {ownerless, ownerlessTok, 403, "delegation needs a session owned by a person"},
		"revoked session token":   {revoked, revokedTok, 401, "revoked or expired"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := probe(t, mux, c.sid, c.tok)
			if code != c.want {
				t.Fatalf("status = %d (%v), want %d", code, out, c.want)
			}
			if c.msg != "" && !strings.Contains(asString(out["error"]), c.msg) {
				t.Fatalf("error = %q, want it to say %q", out["error"], c.msg)
			}
			if c.want == 200 && (out["parent"] != fx.parent || out["daemon"] != fx.dc.ID) {
				t.Fatalf("admitted = %v", out)
			}
		})
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func TestDelegateParentStateChecks(t *testing.T) {
	fx := newDelegFx(t)
	mux := fx.delegateProbe(delegateOpts{})
	ctx := context.Background()

	ended, endedTok := fx.session(sessOpts{owner: "owner-1", status: "stopped"})
	cluster, clusterTok := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	const offline = "00000000-0000-4000-8000-0000000000d2"
	if err := db.UpsertDaemon(ctx, fx.pool, offline, "desk", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	gone, goneTok := fx.session(sessOpts{owner: "owner-1", daemonID: offline})

	for name, c := range map[string]struct {
		sid, tok string
		want     int
		msg      string
	}{
		"ended parent":        {ended, endedTok, 409, "the session has ended"},
		"cluster parent":      {cluster, clusterTok, 422, msgClusterNotConfigured}, // no cluster runtime in this fixture
		"daemon disconnected": {gone, goneTok, 409, "the session's daemon is not connected"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out := probe(t, mux, c.sid, c.tok)
			if code != c.want || !strings.Contains(asString(out["error"]), c.msg) {
				t.Fatalf("status = %d %v, want %d %q", code, out, c.want, c.msg)
			}
		})
	}

	// The child routes that only read (Task 7) still answer with the daemon offline.
	lenient := fx.delegateProbe(delegateOpts{AllowDisconnected: true})
	if code, out := probe(t, lenient, gone, goneTok); code != 200 || out["parent"] != gone || out["daemon"] != "" {
		t.Fatalf("AllowDisconnected: %d %v", code, out)
	}
	// It relaxes nothing else.
	if code, _ := probe(t, lenient, ended, endedTok); code != 409 {
		t.Fatalf("AllowDisconnected admitted an ended parent: %d", code)
	}
	if code, _ := probe(t, lenient, cluster, clusterTok); code != 422 {
		t.Fatalf("AllowDisconnected admitted a cluster parent: %d", code)
	}

	// An unknown session id with a valid token of another session: 403, never an oracle.
	if code, _ := probe(t, mux, newUUID(), fx.parentTok); code != 403 {
		t.Fatalf("unknown id = %d, want 403", code)
	}
}

// A session's own token is never a runner-contract credential: not on the REST contract, not for
// /mcp (AuthorizeRunner), so no route outside the children routes can act as the delegate.
func TestDelegateKindNeverReachesTheContract(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/runner/sessions/" + child, nil},
		{"GET", "/api/runner/sessions/" + fx.parent, nil},
		{"POST", "/api/runner/start", map[string]any{"repo": "app", "prompt": "x", "runtime": "docker"}},
		{"GET", "/api/runner/preferences", nil},
		{"POST", "/api/runner/sessions/" + child + "/stop", nil},
	} {
		if code, out, _ := fx.do(c.method, c.path, fx.parentTok, c.body, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s with a session token = %d %v, want 401", c.method, c.path, code, out)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+fx.parentTok)
	rec := httptest.NewRecorder()
	if _, ok := fx.api.AuthorizeRunner(rec, req); ok || rec.Code != http.StatusUnauthorized {
		t.Fatalf("AuthorizeRunner(session token) = %v %d, want refused 401", ok, rec.Code)
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("a refused call reached the daemon: %s", raw)
	default:
	}
}

// The delegate principal is built in exactly one place: only delegation*.go may call
// delegatePrincipal or set a principal's Kind to sessionDelegateKind. Any other file doing so
// could hand the kind to a contract or MCP core that does not scope it.
func TestDelegatePrincipalBuiltOnlyInDelegationFiles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// RunnerPrincipal is exported: the MCP server could build one too.
	mcpFiles, err := filepath.Glob("../mcp/*.go")
	if err != nil || len(mcpFiles) == 0 {
		t.Fatalf("mcp sources not found: %v", err)
	}
	files = append(files, mcpFiles...)
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		allowed := strings.HasPrefix(filepath.Base(name), "delegation") && filepath.Dir(name) == "."

		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		isKind := func(e ast.Expr) bool {
			id, ok := e.(*ast.Ident)
			return ok && id.Name == "sessionDelegateKind"
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "delegatePrincipal" && !allowed {
					t.Errorf("%s: delegatePrincipal called outside delegation*.go", fset.Position(x.Pos()))
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Trim(x.Value, "`\"") == sessionDelegateKind && !allowed {
					t.Errorf("%s: the literal %q outside delegation*.go", fset.Position(x.Pos()), sessionDelegateKind)
				}
			case *ast.KeyValueExpr:
				if isKind(x.Value) && !allowed {
					t.Errorf("%s: a principal of kind sessionDelegateKind built outside delegation*.go", fset.Position(x.Pos()))
				}
			case *ast.AssignStmt:
				for _, rhs := range x.Rhs {
					if isKind(rhs) && !allowed {
						t.Errorf("%s: sessionDelegateKind assigned outside delegation*.go", fset.Position(x.Pos()))
					}
				}
			}
			return true
		})
	}
	if checked < 50 {
		t.Fatalf("only %d files parsed: the guard is not looking at the package", checked)
	}
}

func TestDelegatePrincipalSeesOnlyItsChildren(t *testing.T) {
	fx := newDelegFx(t)
	sibling, _ := fx.session(sessOpts{owner: "owner-1"})
	ownChild := fx.childOf(fx.parent)
	ownPrivateChild, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, private: true})
	siblingChild := fx.childOf(sibling)
	ownersOther, _ := fx.session(sessOpts{owner: "owner-1"})
	ownersPrivate, _ := fx.session(sessOpts{owner: "owner-1", private: true})
	someoneElses, _ := fx.session(sessOpts{owner: "owner-2"})
	noOwner, _ := fx.session(sessOpts{})

	p := delegatePrincipal(fx.row(fx.parent))
	if p.Kind != sessionDelegateKind || p.Sub != fx.parent || p.OnBehalfOf != "owner-1" {
		t.Fatalf("principal = %+v", p)
	}
	for id, want := range map[string]bool{
		ownChild: true, ownPrivateChild: true,
		fx.parent: false, sibling: false, siblingChild: false, ownersOther: false, ownersPrivate: false,
		someoneElses: false, noOwner: false,
	} {
		if got := canSee(p, fx.row(id)); got != want {
			t.Errorf("canSee(delegate, %s) = %v, want %v", id, got, want)
		}
		if _, apiErr := fx.api.requireSessionAccess(context.Background(), p, id); (apiErr == nil) != want {
			t.Errorf("requireSessionAccess(%s) = %v, want access %v", id, apiErr, want)
		}
	}
	if canSee(p, nil) {
		t.Fatal("canSee(nil row) = true")
	}
	if p.seesAllSessions() {
		t.Fatal("seesAllSessions() = true for a delegate")
	}
	if got := p.startScope(); got != idempotency.Scope(sessionDelegateKind, fx.parent) {
		t.Fatalf("startScope() = %q", got)
	}
	if p.spawningAccountID() != "owner-1" || p.tokenID() != "" || p.proof().valid() {
		t.Fatalf("account %q token %q proof %+v", p.spawningAccountID(), p.tokenID(), p.proof())
	}
}

func TestStartChildRecordsAttributionAndInheritsPrivacy(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	peek := &BrowserConn{ID: "peek", AccountID: "owner-2", send: make(chan []byte, 64)}
	fx.hub.RegisterBrowser(peek)
	drain := func() string {
		var all strings.Builder
		for len(peek.send) > 0 {
			all.Write(<-peek.send)
		}
		return all.String()
	}

	resp, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), fx.childStart(), "rule:r1", "k1")
	if apiErr != nil {
		t.Fatalf("startChild: %d %s", apiErr.Status, apiErr.Message)
	}
	fx.spawn()
	row := fx.row(resp.SessionID)
	if row.ParentSessionID == nil || *row.ParentSessionID != fx.parent || row.DelegationVia != "rule:r1" || row.KeepRunning ||
		row.Interaction != protocol.InteractionUnattended || row.StartedByKind != "session" || row.StartedByName != "Parent" ||
		row.SpawningAccountID == nil || *row.SpawningAccountID != "owner-1" || row.Private || row.TokenID != nil ||
		row.CallbackURL != nil || row.AutoStop || row.CronID != nil {
		t.Fatalf("child row = %+v", row)
	}
	// The positive control: a public child is announced to everyone, so the peek sees it.
	if !strings.Contains(drain(), resp.SessionID) {
		t.Fatal("the public child was never announced: the privacy half below proves nothing")
	}
	// The same key replays the same child, in the delegate's own scope.
	again, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), fx.childStart(), "rule:r1", "k1")
	if apiErr != nil || again.SessionID != resp.SessionID || !again.Replayed {
		t.Fatalf("replay = %+v %v", again, apiErr)
	}
	idem, err := db.GetIdempotencyKey(ctx, fx.pool, idempotency.Scope(sessionDelegateKind, fx.parent), "k1")
	if err != nil || idem == nil || idem.SessionID != resp.SessionID {
		t.Fatalf("idempotency row = %+v %v", idem, err)
	}

	// keep_running travels; an untitled parent names itself by its label.
	cs := fx.childStart()
	cs.KeepRunning = true
	resp, apiErr = fx.api.startChild(ctx, fx.row(fx.parent), cs, "grant:g1", "")
	if apiErr != nil {
		t.Fatal(apiErr.Message)
	}
	fx.spawn()
	if r := fx.row(resp.SessionID); !r.KeepRunning || r.DelegationVia != "grant:g1" {
		t.Fatalf("keep_running child = %+v", r)
	}

	// A private parent's child is private from its insert: the hub knows before the first
	// broadcast, and the other account's socket never hears of it.
	priv, _ := fx.session(sessOpts{owner: "owner-1", private: true, title: "Secret"})
	drain()
	resp, apiErr = fx.api.startChild(ctx, fx.row(priv), fx.childStart(), "request:q1", "")
	if apiErr != nil {
		t.Fatal(apiErr.Message)
	}
	fx.spawn()
	if owners, ok := fx.hub.privateOwners([]string{resp.SessionID}); !ok || owners[resp.SessionID] != "owner-1" {
		t.Fatalf("hub private owners = %v %v", owners, ok)
	}
	if r := fx.row(resp.SessionID); !r.Private || r.StartedByName != "Secret" {
		t.Fatalf("private child row = %+v", r)
	}
	if got := drain(); strings.Contains(got, resp.SessionID) {
		t.Fatalf("another account's socket heard of a private parent's child: %s", got)
	}
}

func TestDelegatedSpawnSkipsPluginsAndCarriesNoEnv(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.SetPlugins(true)
	fx.api.SetCoreCredentials("http://core.test", "internal-key")
	fetched := 0
	fx.api.fetchPlugins = func(context.Context, string, coreProof) ([]pluginspec.Entry, error) {
		fetched++
		return nil, nil
	}
	resp, apiErr := fx.api.startChild(context.Background(), fx.row(fx.parent), fx.childStart(), "rule:r1", "")
	if apiErr != nil {
		t.Fatalf("startChild: %d %s", apiErr.Status, apiErr.Message)
	}
	msg := fx.spawn()
	if msg.SessionID != resp.SessionID || len(msg.Plugins) != 0 || len(msg.ExtraEnv) != 0 || msg.BoardID != "" ||
		msg.BoardToken != "" || msg.Interaction != protocol.InteractionUnattended || !msg.Sandbox || msg.MCPGateway != nil ||
		msg.RestrictTools || msg.DangerouslySkipPermissions || msg.InitialPrompt != "do the thing" || msg.Repo != "app" ||
		msg.Kind != "agent" || msg.SessionToken == "" {
		t.Fatalf("spawn = %+v", msg)
	}
	if fetched != 0 {
		t.Fatalf("core was asked for plugins %d times", fetched)
	}
	att := fx.hub.starts.get(resp.SessionID)
	if att == nil {
		t.Fatal("no start attempt")
	}
	found := false
	for _, s := range att.stages {
		if s.ID == protocol.StagePlugins {
			found = s.Detail == pluginsNoteDelegated && s.State == protocol.StageStateWarning
		}
	}
	if !found || pluginsNoteDelegated != "skipped: started by another session" {
		t.Fatalf("plugin stage missing or wrong: %+v", att.stages)
	}

	// The daemon path refuses a delegate request that carries anything the allowlist does not.
	p := delegatePrincipal(fx.row(fx.parent))
	base := func() runnerStartRequest {
		return runnerStartRequest{Repo: "app", Prompt: "x", Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID, ParentSessionID: fx.parent}
	}
	for name, mutate := range map[string]func(*runnerStartRequest){
		"env":                  func(r *runnerStartRequest) { r.Env = map[string]string{"X": "1"} },
		"callback_url":         func(r *runnerStartRequest) { r.CallbackURL = "https://example.test/cb" },
		"callback_secret":      func(r *runnerStartRequest) { r.CallbackSecret = "s" },
		"git_url":              func(r *runnerStartRequest) { r.GitURL = "https://example.test/x.git" },
		"provider":             func(r *runnerStartRequest) { r.Provider = "github" },
		"auto_stop":            func(r *runnerStartRequest) { r.AutoStop = true },
		"daemon_id":            func(r *runnerStartRequest) { r.TargetDaemonID = fx.dc.ID },
		"grant":                func(r *runnerStartRequest) { r.Grant = &ResolvedGrant{} },
		"cron":                 func(r *runnerStartRequest) { r.CronID = newUUID() },
		"no daemon pin":        func(r *runnerStartRequest) { r.DaemonID = "" },
		"other parent":         func(r *runnerStartRequest) { r.ParentSessionID = newUUID() },
		"cluster":              func(r *runnerStartRequest) { r.Runtime = runnerRuntimeCluster },
		"default runtime":      func(r *runnerStartRequest) { r.Runtime = "" },
		"no operator fallback": func(r *runnerStartRequest) { r.NoOperatorFallback = true },
		"idempotency scope":    func(r *runnerStartRequest) { r.IdemScope = idempotency.Scope(sessionDelegateKind, fx.parent) },
	} {
		req := base()
		mutate(&req)
		if apiErr := fx.api.startRunnerSession(context.Background(), req, p, newUUID()); apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: startRunnerSession = %v, want 422", name, apiErr)
		}
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("a refused delegate start reached the daemon: %s", raw)
	default:
	}
}

// On a cluster install the delegate is refused before anything is recorded, whatever it asks.
func TestDelegateStartRefusedOnTheClusterPath(t *testing.T) {
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	if err := db.UpsertDaemon(ctx, pool, "00000000-0000-4000-8000-0000000000d1", "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	parent := newUUID()
	if err := db.InsertSessionAs(ctx, pool, parent, "00000000-0000-4000-8000-0000000000d1", "running", "/repos/app", "app", "Parent", "",
		db.SessionOrigin{SpawningAccount: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetSession(ctx, pool, parent)
	id := newUUID()
	apiErr := api.startRunnerSession(ctx, runnerStartRequest{Repo: "app", Prompt: "x", ParentSessionID: parent,
		DaemonID: row.DaemonID}, delegatePrincipal(row), id)
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity || apiErr.Message != msgChildrenOnDaemon {
		t.Fatalf("cluster start = %v", apiErr)
	}
	if got, _ := db.GetSession(ctx, pool, id); got != nil {
		t.Fatalf("a refused start left a row: %+v", got)
	}
}

func TestDelegateStopIsAttributedToAnAgent(t *testing.T) {
	end := stopEndByRunner(runnerPrincipal{Kind: sessionDelegateKind, Sub: "parent-1", OnBehalfOf: "owner-1"})
	if end != (db.SessionEnd{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByAgent, ByAccount: "owner-1"}) {
		t.Fatalf("end = %+v", end)
	}
}

func TestAnswersAskFromSession(t *testing.T) {
	if !answersAsk("session") {
		t.Fatal(`answersAsk("session") = false`)
	}
	if answersAsk("board") {
		t.Fatal(`answersAsk("board") = true`)
	}
}

func TestSessionStartedChildIsHeadless(t *testing.T) {
	row := &db.SessionRow{StartedByKind: "session", Interaction: protocol.InteractionInteractive}
	if !headlessSession(row) {
		t.Fatal("a session-started child is not headless")
	}
	if got := startedByOf(&db.SessionRow{StartedByKind: "session", StartedByName: "Parent"}); got == nil ||
		got.Kind != "session" || got.Name != "Parent" {
		t.Fatalf("startedByOf = %+v", got)
	}
}

// HandleRunnerGetPreferences refuses the delegate kind (defense in depth: authRunner never yields it).
func TestPreferencesRefuseTheDelegateKind(t *testing.T) {
	if !preferencesRefused(runnerPrincipal{Kind: sessionDelegateKind}) || !preferencesRefused(runnerPrincipal{Kind: agentPrincipalKind}) ||
		preferencesRefused(runnerPrincipal{Kind: "human"}) {
		t.Fatal("preferencesRefused is wrong")
	}
}

// The shared helpers later tasks build on do what they say.
func TestDelegFixtureHelpers(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.startChildViaRule()
	if r := fx.row(child); r.ParentSessionID == nil || *r.ParentSessionID != fx.parent || !strings.HasPrefix(r.DelegationVia, "rule:") {
		t.Fatalf("startChildViaRule row = %+v", r)
	}
	other, _ := fx.session(sessOpts{owner: "owner-1"})
	if r := fx.row(fx.childOf(other)); r.ParentSessionID == nil || *r.ParentSessionID != other {
		t.Fatalf("childOf row = %+v", r)
	}
	if r := fx.row(fx.parent); derefOrEmpty(r.Title) != "Parent" || derefOrEmpty(r.Runtime) != runnerRuntimeDocker ||
		derefOrEmpty(r.SpawningAccountID) != "owner-1" || r.Kind != "agent" {
		t.Fatalf("parent row = %+v", r)
	}
	board, _ := fx.session(sessOpts{owner: "owner-1", boardID: "new", startedByKind: agentPrincipalKind})
	if r := fx.row(board); r.BoardID == nil || r.StartedByKind != agentPrincipalKind {
		t.Fatalf("board session row = %+v", r)
	}
}

// Depth 1 survives the parent's deletion: parent_session_id is SET NULL then, but the child is
// still known by delegation_via and started_by, so its own token never becomes a parent's.
func TestDeletedParentLeavesItsChildRefused(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	cs := fx.childStart()
	cs.KeepRunning = true
	resp, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), cs, "rule:r1", "")
	if apiErr != nil {
		t.Fatal(apiErr.Message)
	}
	childTok := fx.spawn().SessionToken
	if childTok == "" {
		t.Fatal("the child has no token")
	}
	if err := db.DeleteSession(ctx, fx.pool, fx.parent); err != nil {
		t.Fatal(err)
	}
	child := fx.row(resp.SessionID)
	if child.ParentSessionID != nil {
		t.Fatalf("parent link survived the delete: %v", *child.ParentSessionID)
	}
	mux := fx.delegateProbe(delegateOpts{})
	if code, out := probe(t, mux, resp.SessionID, childTok); code != http.StatusForbidden || asString(out["error"]) != msgDelegateDepth {
		t.Fatalf("orphaned child's token = %d %v, want 403 %q", code, out, msgDelegateDepth)
	}
	if _, apiErr := fx.api.startChild(ctx, child, fx.childStart(), "rule:r1", ""); apiErr == nil || apiErr.Status != http.StatusForbidden {
		t.Fatalf("startChild(orphaned child) = %v, want 403", apiErr)
	}
	for name, row := range map[string]*db.SessionRow{
		"via only":        {DelegationVia: "request:x"},
		"started_by only": {StartedByKind: "session"},
		"parent only":     {ParentSessionID: &fx.parent},
	} {
		if !isChildSession(row) {
			t.Errorf("%s: isChildSession = false", name)
		}
	}
	if isChildSession(&db.SessionRow{StartedByKind: agentPrincipalKind}) || isChildSession(nil) {
		t.Fatal("isChildSession is true for a non-child")
	}
}

// A child whose row cannot be pre-created (with its parent link) is refused, never left for the
// daemon's session_started to insert without one.
func TestDelegateStartRefusedWhenTheChildCannotBeRecorded(t *testing.T) {
	fx := newDelegFx(t)
	req := runnerStartRequest{Repo: "app", Prompt: "x", Runtime: runnerRuntimeDocker, DaemonID: fx.dc.ID,
		ParentSessionID: fx.parent, DelegationVia: "rule:r1", Interaction: protocol.InteractionUnattended}
	p := delegatePrincipal(fx.row(fx.parent))
	ctx := context.Background()
	// Not a uuid: the pre-create insert fails.
	if apiErr := fx.api.startRunnerSession(ctx, req, p, "not-a-uuid"); apiErr == nil || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("start (insert fails) = %v, want 503", apiErr)
	}
	// The row goes in but its token cannot be minted (the pre-create returns ""): refused too, and
	// the row is removed again. This database is this test's own copy.
	if _, err := fx.pool.Exec(ctx, `
		CREATE FUNCTION refuse_token() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'no tokens'; END $$;
		CREATE TRIGGER refuse_token BEFORE INSERT ON board_tokens FOR EACH ROW EXECUTE FUNCTION refuse_token();`); err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	if apiErr := fx.api.startRunnerSession(ctx, req, p, id); apiErr == nil || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("start (token fails) = %v, want 503", apiErr)
	}
	if row, _ := db.GetSession(ctx, fx.pool, id); row != nil {
		t.Fatalf("the refused child's row was left: %+v", row)
	}
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("an unrecorded child reached the daemon: %s", raw)
	default:
	}
}

// The idempotency hash covers a child's in-process fields, and only a child's.
func TestChildIdempotencyHashCoversKeepRunning(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	if _, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), fx.childStart(), "rule:r1", "k1"); apiErr != nil {
		t.Fatal(apiErr.Message)
	}
	fx.spawn()
	cs := fx.childStart()
	cs.KeepRunning = true
	if _, apiErr := fx.api.startChild(ctx, fx.row(fx.parent), cs, "rule:r1", "k1"); apiErr == nil || apiErr.Status != http.StatusConflict {
		t.Fatalf("same key, different keep_running = %v, want 409", apiErr)
	}
	plain := runnerStartRequest{Repo: "app", Prompt: "x"}
	raw, _ := json.Marshal(plain)
	sum := sha256.Sum256(raw)
	if got, _ := startRequestHash(plain); got != hex.EncodeToString(sum[:]) {
		t.Fatal("the hash of a non-child request changed")
	}
}

// A runtime token (a cluster pod's) is accepted as a credential and then refused as a parent: its
// own id → 422 (no cluster runtime configured in this fixture), another id → 403. A connected
// daemon that is not a host → 422.
func TestDelegateRuntimeTokenAndNonHostDaemon(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	mux := fx.delegateProbe(delegateOpts{AllowDisconnected: true})
	pod, _ := fx.session(sessOpts{owner: "owner-1", runtime: runnerRuntimeCluster})
	runtimeTok, _, err := db.MintRuntimeLaunch(ctx, fx.pool, pod, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := probe(t, mux, pod, runtimeTok); code != http.StatusUnprocessableEntity || asString(out["error"]) != msgClusterNotConfigured {
		t.Fatalf("runtime token, own id = %d %v", code, out)
	}
	if code, _ := probe(t, mux, fx.parent, runtimeTok); code != http.StatusForbidden {
		t.Fatalf("runtime token, another id = %d, want 403", code)
	}

	podDaemon := registerDaemon(t, fx.api, fx.hub, "00000000-0000-4000-8000-0000000000d3", "pod-daemon")
	podDaemon.Mode = "runner"
	onPod, onPodTok := fx.session(sessOpts{owner: "owner-1", daemonID: podDaemon.ID})
	if code, out := probe(t, mux, onPod, onPodTok); code != http.StatusUnprocessableEntity || asString(out["error"]) != msgChildrenOnDaemon {
		t.Fatalf("connected non-host daemon = %d %v", code, out)
	}
}
