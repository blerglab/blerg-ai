package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// newAuthzMux registers the board / column / ticket / reply / assist / rule
// routes exactly as cmd/server/main.go does, so the table below exercises the
// real method+pattern matching and PathValue plumbing rather than handlers
// called by hand.
func newAuthzMux(api *API) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agent/rules", api.HandlePostRule)
	mux.HandleFunc("GET /api/agent/rules", api.HandleGetRules)
	mux.HandleFunc("PATCH /api/agent/rules/{id}", api.HandlePatchRule)
	mux.HandleFunc("DELETE /api/agent/rules/{id}", api.HandleDeleteRule)
	mux.HandleFunc("POST /api/messages/{id}/reply", api.HandlePostMessageReply)
	mux.HandleFunc("GET /api/boards", api.HandleGetBoards)
	mux.HandleFunc("POST /api/boards", api.HandlePostBoards)
	mux.HandleFunc("DELETE /api/boards/{id}", api.HandleDeleteBoard)
	mux.HandleFunc("GET /api/boards/{id}", api.HandleGetBoard)
	mux.HandleFunc("GET /api/boards/{id}/order", api.HandleGetBoardOrder)
	mux.HandleFunc("POST /api/boards/{id}/columns", api.HandlePostColumn)
	mux.HandleFunc("PATCH /api/columns/{id}", api.HandlePatchColumn)
	mux.HandleFunc("DELETE /api/columns/{id}", api.HandleDeleteColumn)
	mux.HandleFunc("POST /api/boards/{id}/tickets", api.HandlePostTicket)
	mux.HandleFunc("GET /api/boards/{id}/tickets", api.HandleListTickets)
	mux.HandleFunc("GET /api/tickets/{id}", api.HandleGetTicket)
	mux.HandleFunc("PATCH /api/tickets/{id}", api.HandlePatchTicket)
	mux.HandleFunc("POST /api/tickets/{id}/split", api.HandleSplitTicket)
	mux.HandleFunc("POST /api/tickets/{id}/archive", api.HandleArchiveTicket)
	mux.HandleFunc("POST /api/tickets/{id}/dependencies", api.HandleAddDependency)
	mux.HandleFunc("DELETE /api/tickets/{id}/dependencies/{depId}", api.HandleRemoveDependency)
	mux.HandleFunc("GET /api/tickets/{id}/events", api.HandleGetTicketEvents)
	mux.HandleFunc("GET /api/boards/{id}/archive", api.HandleGetBoardArchive)
	mux.HandleFunc("POST /api/boards/{id}/assist", api.HandlePostAssist)
	return mux
}

// authzFixture is one board's worth of resources; every credential round gets
// a fresh pair (A and B) so mutations in one round cannot skew the next.
type authzFixture struct {
	board, column, ticket, ticket2 string
	repo                           string // the board's one repo (ticket bodies must name it)
	msg                            string // message on a session bound to this board
}

type authzWorld struct {
	a, b       authzFixture
	noBoardMsg string // message on a session with no board
	rule       string
	boardTokA  string // board token scoped to a.board with {board}
}

// authzHubDaemonID is the daemon registered on the hub so POST .../assist can
// actually spawn (202) instead of 503-ing for want of a daemon.
const authzHubDaemonID = "00000000-0000-0000-0000-00000000a0d1"

func seedAuthzWorld(ctx context.Context, t *testing.T, api *API, tag string) authzWorld {
	t.Helper()
	pool := api.dbPool
	daemonID := newUUID()
	if err := db.UpsertDaemon(ctx, pool, daemonID, "authz-"+tag, "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	mk := func(name string) authzFixture {
		board, err := db.CreateBoard(ctx, pool, name+"-"+tag, nil, []string{"repo-" + tag}, nil)
		if err != nil {
			t.Fatalf("CreateBoard: %v", err)
		}
		col, err := db.AddColumn(ctx, pool, board.ID, "Extra", nil, false)
		if err != nil {
			t.Fatalf("AddColumn: %v", err)
		}
		t1, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "t1", Priority: "medium", Repos: []string{"repo-" + tag}})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		t2, err := db.CreateTicket(ctx, pool, db.TicketInput{BoardID: board.ID, Title: "t2", Priority: "medium", Repos: []string{"repo-" + tag}})
		if err != nil {
			t.Fatalf("CreateTicket: %v", err)
		}
		sess := newUUID()
		if err := db.CreateAssistSession(ctx, pool, sess, daemonID, board.ID, nil, "repo-"+tag, "assist"); err != nil {
			t.Fatalf("CreateAssistSession: %v", err)
		}
		msg, err := db.CreateMessage(ctx, pool, sess, "ask", "q?")
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		return authzFixture{board: board.ID, column: col.ID, ticket: t1.ID, ticket2: t2.ID, repo: "repo-" + tag, msg: msg}
	}
	w := authzWorld{a: mk("A"), b: mk("B")}

	plainSess := newUUID()
	if err := db.InsertSession(ctx, pool, plainSess, daemonID, "running", "/repos/app", "app", "plain", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	msg, err := db.CreateMessage(ctx, pool, plainSess, "ask", "q?")
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	w.noBoardMsg = msg

	rule, err := db.InsertAgentRule(ctx, pool, "proj-"+tag, "be nice")
	if err != nil {
		t.Fatalf("InsertAgentRule: %v", err)
	}
	w.rule = rule

	raw, _, err := db.MintBoardToken(ctx, pool, w.a.board, plainSess, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken: %v", err)
	}
	w.boardTokA = raw
	return w
}

// authzRoute is one in-scope route. cap is the core capability the ruling
// (task brief, R9) assigns it; scope says which gate the route sits behind:
//
//	"board"   — authBrowserOrBoard(boardID): a board token for the fixture's
//	            board passes; another board's → 403
//	"none"    — authBrowserOrBoard + requireCoreOrDaemon: board tokens → 403
//	            (board create/delete, reply on a no-board session)
//	"rules"   — authDaemonOrCoreCap: daemon or core token only; board token → 401
//	"daemon"  — checkBearerToken: the daemon token only, everything else 401
//	"browser" — authBrowser: core token with session.start only; daemon → 401
type authzRoute struct {
	name   string
	method string
	path   func(f authzFixture, w authzWorld) string
	body   func(f authzFixture) string
	cap    string
	scope  string
}

// authzRoutes is ordered so that, when a credential class is allowed
// through, every route genuinely succeeds: reads before writes, dependency
// add/remove before ticket2 is archived, split (which archives ticket) after
// everything else that touches ticket, and DELETE /api/boards last.
var authzRoutes = []authzRoute{
	{"GET /api/boards/x", "GET", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board }, nil, "card.read", "board"},
	{"GET /api/boards/x/order", "GET", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/order" }, nil, "card.read", "board"},
	{"GET /api/boards/x/archive", "GET", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/archive" }, nil, "card.read", "board"},
	{"GET /api/boards/x/tickets", "GET", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/tickets" }, nil, "card.read", "board"},
	{"GET /api/tickets/x", "GET", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket }, nil, "card.read", "board"},
	{"GET /api/tickets/x/events", "GET", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket + "/events" }, nil, "card.read", "board"},
	{"POST /api/boards/x/columns", "POST", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/columns" },
		func(authzFixture) string { return `{"name":"Col"}` }, "card.write", "board"},
	{"PATCH /api/columns/x", "PATCH", func(f authzFixture, _ authzWorld) string { return "/api/columns/" + f.column },
		func(authzFixture) string { return `{"name":"Renamed"}` }, "card.write", "board"},
	{"DELETE /api/columns/x", "DELETE", func(f authzFixture, _ authzWorld) string { return "/api/columns/" + f.column }, nil, "card.write", "board"},
	{"POST /api/boards/x/tickets", "POST", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/tickets" },
		func(f authzFixture) string { return `{"title":"New","repos":["` + f.repo + `"]}` }, "card.write", "board"},
	{"PATCH /api/tickets/x", "PATCH", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket },
		func(authzFixture) string { return `{"title":"Renamed"}` }, "card.write", "board"},
	{"POST /api/tickets/x/dependencies", "POST", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket + "/dependencies" },
		func(f authzFixture) string { return `{"depends_on_ticket_id":"` + f.ticket2 + `"}` }, "card.write", "board"},
	{"DELETE /api/tickets/x/dependencies/y", "DELETE", func(f authzFixture, _ authzWorld) string {
		return "/api/tickets/" + f.ticket + "/dependencies/" + f.ticket2
	}, nil, "card.write", "board"},
	{"POST /api/tickets/x/archive", "POST", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket2 + "/archive" }, nil, "card.write", "board"},
	{"POST /api/tickets/x/split", "POST", func(f authzFixture, _ authzWorld) string { return "/api/tickets/" + f.ticket + "/split" },
		func(authzFixture) string { return `{"titles":["a","b"]}` }, "card.write", "board"},
	{"POST /api/messages/x/reply (board session)", "POST", func(f authzFixture, _ authzWorld) string { return "/api/messages/" + f.msg + "/reply" },
		func(authzFixture) string { return `{"answer":"ok"}` }, "session.start", "board"},
	{"POST /api/messages/x/reply (no board)", "POST", func(_ authzFixture, w authzWorld) string { return "/api/messages/" + w.noBoardMsg + "/reply" },
		func(authzFixture) string { return `{"answer":"ok"}` }, "session.start", "none"},
	{"POST /api/boards/x/assist", "POST", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board + "/assist" },
		func(authzFixture) string { return `{}` }, "session.start", "board"},
	{"GET /api/boards", "GET", func(authzFixture, authzWorld) string { return "/api/boards" }, nil, "session.start", "browser"},
	{"POST /api/agent/rules", "POST", func(authzFixture, authzWorld) string { return "/api/agent/rules" },
		func(authzFixture) string { return `{"project":"p","content":"c"}` }, "", "daemon"},
	{"GET /api/agent/rules", "GET", func(authzFixture, authzWorld) string { return "/api/agent/rules" }, nil, "board.admin", "rules"},
	{"PATCH /api/agent/rules/x", "PATCH", func(_ authzFixture, w authzWorld) string { return "/api/agent/rules/" + w.rule },
		func(authzFixture) string { return `{"enabled":true}` }, "board.admin", "rules"},
	{"DELETE /api/agent/rules/x", "DELETE", func(_ authzFixture, w authzWorld) string { return "/api/agent/rules/" + w.rule }, nil, "board.admin", "rules"},
	{"POST /api/boards", "POST", func(authzFixture, authzWorld) string { return "/api/boards" },
		func(authzFixture) string { return `{"name":"New","repos":["r"]}` }, "card.write", "none"},
	// Last on purpose: when a round is allowed through, the board is gone
	// afterwards, so every other route must have been exercised first.
	{"DELETE /api/boards/x", "DELETE", func(f authzFixture, _ authzWorld) string { return "/api/boards/" + f.board }, nil, "board.admin", "none"},
}

func routeByName(t *testing.T, name string) authzRoute {
	t.Helper()
	for _, rt := range authzRoutes {
		if rt.name == name {
			return rt
		}
	}
	t.Fatalf("no route named %q", name)
	return authzRoute{}
}

var (
	memberCaps = []string{"card.read", "card.write", "column.write", "session.start"}
	adminCaps  = append(append([]string{}, memberCaps...), "board.admin", "gate.bypass", "membership.write", "secrets.read", "account.manage")
)

func hasCap(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// wantSuccess is the sentinel meaning "any 2xx" — the allowed paths must
// genuinely succeed, not merely avoid 401/403.
const wantSuccess = 0

// coreClassCaps are the core-token credential classes and the caps they mint.
var coreClassCaps = map[string][]string{
	"cardread": {"card.read"},
	"member":   memberCaps,
	"admin":    adminCaps,
}

// expectFor is the ruling as a function: what status each credential class
// must get on each route. Classes: none, garbage, cardread, member, admin,
// boardA (board token scoped to A, used on A), boardB (same token used on B),
// daemon.
func expectFor(rt authzRoute, class string) int {
	switch class {
	case "none", "garbage":
		return http.StatusUnauthorized
	}
	if caps, isCore := coreClassCaps[class]; isCore {
		switch rt.scope {
		case "daemon":
			return http.StatusUnauthorized
		case "browser":
			if hasCap(caps, "session.start") {
				return wantSuccess
			}
			return http.StatusUnauthorized // authBrowser has no 403 tier
		default: // board, none, rules
			if hasCap(caps, rt.cap) {
				return wantSuccess
			}
			return http.StatusForbidden
		}
	}
	switch class {
	case "daemon":
		if rt.scope == "browser" {
			return http.StatusUnauthorized // authBrowser accepts core tokens only
		}
		return wantSuccess
	case "boardA":
		switch rt.scope {
		case "board":
			return wantSuccess
		case "none":
			return http.StatusForbidden
		default: // rules, daemon, browser: a board token is not a credential there
			return http.StatusUnauthorized
		}
	case "boardB":
		return http.StatusForbidden
	}
	panic("unknown class " + class)
}

// TestAuthzRoutes is the regression guard for the anonymous tier: every
// board/column/ticket/reply/assist/rule route now demands a verified
// credential, and each credential class is held to the capability ruling.
// DB-gated.
func TestAuthzRoutes(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	const daemonTok = "daemon-secret"
	hub := NewHub()
	if err := db.UpsertDaemon(ctx, pool, authzHubDaemonID, "authz-hub-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	hub.Register(&DaemonConn{ID: authzHubDaemonID, Name: "authz-hub-daemon", send: make(chan []byte, 256)})
	api := NewAPI(hub, pool, daemonTok, nil, "")
	mint := enableBrowserAuth(t, api)
	mux := newAuthzMux(api)

	do := func(rt authzRoute, f authzFixture, w authzWorld, auth string) *httptest.ResponseRecorder {
		var body string
		if rt.body != nil {
			body = rt.body(f)
		}
		req := httptest.NewRequest(rt.method, rt.path(f, w), strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	check := func(t *testing.T, rt authzRoute, class string, rec *httptest.ResponseRecorder) {
		t.Helper()
		want := expectFor(rt, class)
		if want == wantSuccess {
			if rec.Code/100 != 2 {
				t.Errorf("%s [%s]: got %d, want 2xx; body=%s", rt.name, class, rec.Code, rec.Body.String())
			}
			return
		}
		if rec.Code != want {
			t.Errorf("%s [%s]: got %d, want %d; body=%s", rt.name, class, rec.Code, want, rec.Body.String())
		}
	}

	classes := []string{"none", "garbage", "cardread", "member", "admin", "boardA", "boardB", "daemon"}
	for _, class := range classes {
		t.Run(class, func(t *testing.T) {
			w := seedAuthzWorld(ctx, t, api, class)
			var auth string
			fixture := w.a
			switch class {
			case "none":
			case "garbage":
				auth = "not-a-token"
			case "boardA":
				auth = w.boardTokA
			case "boardB":
				auth = w.boardTokA
				fixture = w.b
			case "daemon":
				auth = daemonTok
			default:
				auth = mint(coreClassCaps[class]...)
			}
			for _, rt := range authzRoutes {
				if class == "boardB" && rt.scope != "board" {
					continue // no board-B variant of a boardless route
				}
				check(t, rt, class, do(rt, fixture, w, auth))
			}
		})
	}

	t.Run("member creates board, column, ticket; only admin deletes the board", func(t *testing.T) {
		w := seedAuthzWorld(ctx, t, api, "flow")
		member := mint(memberCaps...)
		admin := mint(adminCaps...)

		post := func(path, body, auth string) (int, map[string]any) {
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+auth)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			return rec.Code, out
		}
		code, board := post("/api/boards", `{"name":"Flow","repos":["flow"]}`, member)
		if code != http.StatusCreated {
			t.Fatalf("member POST /api/boards → %d, want 201", code)
		}
		boardID, _ := board["id"].(string)
		if code, _ := post("/api/boards/"+boardID+"/columns", `{"name":"Col"}`, member); code != http.StatusCreated {
			t.Fatalf("member POST column → %d, want 201", code)
		}
		if code, _ := post("/api/boards/"+boardID+"/tickets", `{"title":"T","repos":["flow"]}`, member); code != http.StatusCreated {
			t.Fatalf("member POST ticket → %d, want 201", code)
		}
		del := routeByName(t, "DELETE /api/boards/x")
		f := authzFixture{board: boardID}
		if got := do(del, f, w, member).Code; got != http.StatusForbidden {
			t.Fatalf("member DELETE board → %d, want 403", got)
		}
		if got := do(del, f, w, admin).Code; got != http.StatusNoContent {
			t.Fatalf("admin DELETE board → %d, want 204", got)
		}
	})

	t.Run("exact codes on the brief's spot checks", func(t *testing.T) {
		w := seedAuthzWorld(ctx, t, api, "spot")
		if got := do(routeByName(t, "GET /api/boards/x"), w.a, w, mint("card.read")).Code; got != http.StatusOK {
			t.Errorf("GET /api/boards/<real> with card.read → %d, want 200", got)
		}
		get := routeByName(t, "GET /api/agent/rules")
		if got := do(get, w.a, w, daemonTok).Code; got != http.StatusOK {
			t.Errorf("GET /api/agent/rules with daemon token → %d, want 200", got)
		}
		if got := do(get, w.a, w, mint(memberCaps...)).Code; got != http.StatusForbidden {
			t.Errorf("GET /api/agent/rules with member token → %d, want 403", got)
		}
		if got := do(get, w.a, w, mint(adminCaps...)).Code; got != http.StatusOK {
			t.Errorf("GET /api/agent/rules with admin token → %d, want 200", got)
		}
	})
}
