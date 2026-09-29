package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// The two runner-side admin routes (handlers in review.go, registered in
// runnerapi.go).
func acceptCardPath(id string) string { return "/api/cards/" + id + "/accept" }
func recordDeploymentPath() string    { return "/api/deployments" }

// A core "member" must be refused on every admin-tier route. Before this test
// existed, corePrincipal's KindService mapping let members through every
// `p.Kind == KindAgent`-only gate (audit-board-runner C1).
func TestMemberRefusedOnAdminRoutes(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)
	member := memberToken(t)

	var board db.Board
	resp := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "authz"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create board = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &board)

	var minted struct {
		Token db.Token `json:"token"`
	}
	resp = request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "victim"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin mint = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &minted)

	cases := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/boards", map[string]any{"name": "nope"}},
		{"PATCH", "/api/boards/" + board.ID, map[string]any{"name": "renamed"}},
		{"DELETE", "/api/boards/" + board.ID, nil},
		{"GET", "/api/tokens", nil},
		{"POST", "/api/tokens", map[string]any{"label": "x"}},
		{"POST", "/api/tokens/" + minted.Token.ID + "/revoke", nil},
		{"POST", "/api/reviews/00000000-0000-0000-0000-000000000000/resolve", map[string]any{"decision": "approve"}},
		{"POST", acceptCardPath("00000000-0000-0000-0000-000000000000"), nil},
		{"POST", recordDeploymentPath(), map[string]any{"board_id": board.ID, "env": "prod"}},
	}
	for _, tc := range cases {
		resp := request(t, srv, tc.method, tc.path, member, nil, tc.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("member %s %s = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
	}

	// The same gates over MCP: board_create/board_update used to be
	// `Kind == KindAgent`-only there too.
	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"blerg_board_create", map[string]any{"name": "nope-mcp"}},
		{"blerg_board_update", map[string]any{"board_id": board.ID, "name": "renamed-mcp"}},
	} {
		resp := request(t, srv, "POST", "/mcp", member, nil, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool.name, "arguments": tool.args},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("mcp %s: HTTP %d", tool.name, resp.StatusCode)
		}
		var out struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		decodeBody(t, resp, &out)
		if !out.Result.IsError {
			t.Errorf("member mcp %s succeeded, want tool error", tool.name)
		}
	}

	// Nothing above changed state: the board still has its original name.
	var after db.Board
	resp = request(t, srv, "GET", "/api/boards/"+board.ID, admin, nil, nil)
	decodeBody(t, resp, &after)
	if after.Name != "authz" {
		t.Errorf("board renamed to %q by a member", after.Name)
	}

	// And the things a member IS allowed to do still work.
	resp = request(t, srv, "GET", "/api/boards/"+board.ID, member, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("member GET board = %d, want 200", resp.StatusCode)
	}
	resp = request(t, srv, "POST", "/api/boards/"+board.ID+"/columns", member, nil, map[string]any{"name": "col"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("member create column = %d, want 201 (column.write is a member cap)", resp.StatusCode)
	}
	for _, path := range []string{
		"/api/boards/" + board.ID + "/columns",
		"/api/boards/" + board.ID + "/schema",
		"/api/reviews?board=" + board.ID,
		"/api/reviews",
		"/api/overview",
		"/api/sessions",
	} {
		resp := request(t, srv, "GET", path, member, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("member GET %s = %d, want 200 (card.read, unscoped)", path, resp.StatusCode)
		}
	}
}

// A core "admin" (board.admin in its caps) is admin on every admin-tier route
// — the gates are capability checks, not a ban on core-issued principals.
func TestAdminAllowedOnAdminRoutes(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)

	var board db.Board
	resp := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "adm"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create board = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &board)

	resp = request(t, srv, "PATCH", "/api/boards/"+board.ID, admin, nil, map[string]any{"name": "renamed"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin PATCH board = %d, want 200", resp.StatusCode)
	}
	resp = request(t, srv, "GET", "/api/tokens", admin, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin GET tokens = %d, want 200", resp.StatusCode)
	}
	resp = request(t, srv, "POST", recordDeploymentPath(), admin, nil, map[string]any{"board_id": board.ID, "env": "prod"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("admin record deployment = %d, want 202", resp.StatusCode)
	}
	// Board deletion: a human with board.admin. Last, since it removes the board.
	resp = request(t, srv, "DELETE", "/api/boards/"+board.ID, admin, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("admin DELETE board = %d, want 204", resp.StatusCode)
	}
	// ...but the native service key is not a human, so it may not delete.
	resp = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "svc-del"})
	decodeBody(t, resp, &board)
	resp = request(t, srv, "DELETE", "/api/boards/"+board.ID, "svc-key-123", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("service key DELETE board = %d, want 403 (deletion is human-only)", resp.StatusCode)
	}
}

// Revocation is admin-only and never upward: an admin can revoke a token it
// could have minted, but not one more privileged than itself (the env
// service key's row carries runner.report, which no platform role holds).
func TestRevokeTokenNeverUpward(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)

	var minted struct {
		Token db.Token `json:"token"`
	}
	resp := request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "victim"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin mint = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &minted)

	var tokens []db.Token
	resp = request(t, srv, "GET", "/api/tokens", "svc-key-123", nil, nil)
	decodeBody(t, resp, &tokens)
	var svcRow string
	for _, tok := range tokens {
		if tok.Kind == "service" {
			svcRow = tok.ID
		}
	}
	if svcRow == "" {
		t.Fatal("env service key row not registered")
	}

	resp = request(t, srv, "POST", "/api/tokens/"+svcRow+"/revoke", admin, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("admin revoking the service key row = %d, want 403", resp.StatusCode)
	}
	resp = request(t, srv, "POST", "/api/tokens/"+minted.Token.ID+"/revoke", admin, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin revoking its own mint = %d, want 200", resp.StatusCode)
	}
	resp = request(t, srv, "POST", "/api/tokens/00000000-0000-0000-0000-000000000000/revoke", admin, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoking an unknown token = %d, want 404", resp.StatusCode)
	}
}

// A board-scoped agent token must not read other boards' columns/schema/
// reviews or the global overview (audit M5).
func TestBoardScopedTokenCannotReadAcrossBoards(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)
	var a, b db.Board
	r := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "a"})
	decodeBody(t, r, &a)
	r = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "b"})
	decodeBody(t, r, &b)
	var minted struct {
		Secret string `json:"secret"`
	}
	r = request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "a-only", "board_id": a.ID, "capabilities": []string{"card.read"}})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("mint scoped = %d", r.StatusCode)
	}
	decodeBody(t, r, &minted)
	for _, path := range []string{
		"/api/boards/" + b.ID + "/columns",
		"/api/boards/" + b.ID + "/schema",
		"/api/reviews?board=" + b.ID,
		"/api/reviews",
		"/api/overview",
		"/api/sessions",
	} {
		resp := request(t, srv, "GET", path, minted.Secret, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("scoped token GET %s = %d, want 403", path, resp.StatusCode)
		}
	}
	// Its own board stays readable.
	for _, path := range []string{
		"/api/boards/" + a.ID + "/columns",
		"/api/boards/" + a.ID + "/schema",
		"/api/reviews?board=" + a.ID,
	} {
		resp := request(t, srv, "GET", path, minted.Secret, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("scoped token GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
	// Same over MCP: board_get/schema/column_list on the other board error.
	for _, tool := range []string{"blerg_board_get", "blerg_board_schema", "blerg_column_list"} {
		resp := request(t, srv, "POST", "/mcp", minted.Secret, nil, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": map[string]any{"board_id": b.ID}},
		})
		var out struct {
			Result struct {
				IsError bool            `json:"isError"`
				Content json.RawMessage `json:"content"`
			} `json:"result"`
		}
		decodeBody(t, resp, &out)
		if !out.Result.IsError {
			t.Errorf("scoped token mcp %s on another board succeeded, want tool error", tool)
		}
	}
}

// Board-chat sessions mint a token with requested ∩ caller's caps, and
// bootstrapping an empty board (which wires repos/git_base — board.admin
// work) is refused to anyone without board.admin.
func TestBoardSessionCapsFollowCaller(t *testing.T) {
	srv, _ := testServer(t)
	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})
	admin := adminToken(t)
	member := memberToken(t)

	var empty, wired db.Board
	r := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "empty"})
	decodeBody(t, r, &empty)
	r = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "wired", "repos": []string{"blerg-board"}})
	decodeBody(t, r, &wired)
	if len(empty.Repos) != 0 || len(wired.Repos) != 1 {
		t.Fatalf("fixture: empty.Repos=%v wired.Repos=%v", empty.Repos, wired.Repos)
	}

	// Member on the repo-less board: bootstrap needs board.admin → 403, and
	// no session token is minted.
	resp := request(t, srv, "POST", "/api/boards/"+empty.ID+"/sessions", member, nil, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("member bootstrap session = %d, want 403", resp.StatusCode)
	}
	if n := len(fake.started); n != 0 {
		t.Errorf("member bootstrap started %d runner sessions, want 0", n)
	}
	if got := sessionTokens(t, srv); len(got) != 0 {
		t.Errorf("member bootstrap minted %d board-session tokens, want 0", len(got))
	}

	// Admin bootstraps: 202, and the session token carries board.admin.
	resp = request(t, srv, "POST", "/api/boards/"+empty.ID+"/sessions", admin, nil, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("admin bootstrap session = %d, want 202", resp.StatusCode)
	}
	toks := sessionTokens(t, srv)
	if len(toks) != 1 {
		t.Fatalf("admin bootstrap minted %d board-session tokens, want 1", len(toks))
	}
	for _, c := range []string{"card.read", "card.write", "column.write", "board.admin"} {
		if !toks[0].HasCap(c) {
			t.Errorf("admin's bootstrap token lacks %s: %v", c, toks[0].Capabilities)
		}
	}
	if toks[0].BoardID == nil || *toks[0].BoardID != empty.ID {
		t.Errorf("bootstrap token board = %v, want %s", toks[0].BoardID, empty.ID)
	}

	// Member on a wired board: allowed (card.write), but the chat is the
	// member's proxy — its token must not carry board.admin.
	resp = request(t, srv, "POST", "/api/boards/"+wired.ID+"/sessions", member, nil, map[string]any{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("member board session on a wired board = %d, want 202", resp.StatusCode)
	}
	toks = sessionTokens(t, srv)
	if len(toks) != 2 {
		t.Fatalf("expected 2 board-session tokens, got %d", len(toks))
	}
	var memberTok *db.Token
	for i := range toks {
		if toks[i].BoardID != nil && *toks[i].BoardID == wired.ID {
			memberTok = &toks[i]
		}
	}
	if memberTok == nil {
		t.Fatal("no session token minted for the wired board")
	}
	if memberTok.HasCap("board.admin") {
		t.Errorf("member's board-session token carries board.admin: %v", memberTok.Capabilities)
	}
	for _, c := range []string{"card.read", "card.write", "column.write"} {
		if !memberTok.HasCap(c) {
			t.Errorf("member's board-session token lacks %s: %v", c, memberTok.Capabilities)
		}
	}
}

// sessionTokens lists the live board-session tokens, as the native key.
func sessionTokens(t *testing.T, srv *httptest.Server) []db.Token {
	t.Helper()
	var all []db.Token
	resp := request(t, srv, "GET", "/api/tokens", "svc-key-123", nil, nil)
	decodeBody(t, resp, &all)
	var out []db.Token
	for _, tok := range all {
		if strings.HasPrefix(tok.Label, "board session") && tok.RevokedAt == nil {
			out = append(out, tok)
		}
	}
	return out
}

// GET /api/reviews/{id} is board-scoped: a token scoped to another board is
// refused; an unscoped member with card.read may read any review.
func TestGetReviewIsBoardScoped(t *testing.T) {
	srv, _ := testServer(t) // denyAll gate: every gated agent write yields a review
	admin := adminToken(t)
	member := memberToken(t)

	var gated, other db.Board
	r := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "gated", "gate_enabled": true, "require_repo": false})
	decodeBody(t, r, &gated)
	r = request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "other"})
	decodeBody(t, r, &other)

	mint := func(boardID string, caps []string) string {
		t.Helper()
		var m struct {
			Secret string `json:"secret"`
		}
		r := request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "t", "board_id": boardID, "capabilities": caps})
		if r.StatusCode != http.StatusCreated {
			t.Fatalf("mint = %d", r.StatusCode)
		}
		decodeBody(t, r, &m)
		return m.Secret
	}
	writer := mint(gated.ID, []string{"card.read", "card.write"})
	outsider := mint(other.ID, []string{"card.read"})

	// An agent write through the deny-all gate leaves a review row.
	resp := request(t, srv, "POST", "/api/boards/"+gated.ID+"/cards", writer, nil, map[string]any{"title": "denied"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("gated agent create = %d, want 409", resp.StatusCode)
	}
	var verdict struct {
		ReviewID string `json:"review_id"`
	}
	decodeBody(t, resp, &verdict)
	if verdict.ReviewID == "" {
		t.Fatal("denial carried no review_id")
	}
	path := "/api/reviews/" + verdict.ReviewID

	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"scoped token on another board", outsider, http.StatusForbidden},
		{"scoped token on the review's board", writer, http.StatusOK},
		{"unscoped member (card.read)", member, http.StatusOK},
		{"native key", "svc-key-123", http.StatusOK},
	} {
		resp := request(t, srv, "GET", path, tc.token, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s GET %s = %d, want %d", tc.name, path, resp.StatusCode, tc.want)
		}
	}
}

// Token refresh mints a real board tokens row from the caller's token; a
// core-issued agent token is not a board row and must not be able to
// materialise one that way.
func TestRefreshRejectsCoreTokens(t *testing.T) {
	srv, _ := testServer(t)
	coreAgent := testCoreToken(t, "agent", []string{"card.read", "card.write"})
	resp := request(t, srv, "POST", "/api/tokens/refresh", coreAgent, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("core agent refresh = %d, want 400", resp.StatusCode)
	}
}

// A password.change-only core token (R7: the must-change-password state)
// authenticates but must be refused on the instance-wide read routes — list
// boards and global search used to run for any verified principal, letting
// such a token enumerate every board and search every card.
func TestPasswordChangeOnlyTokenRefusedOnReadRoutes(t *testing.T) {
	srv, _ := testServer(t)
	admin := adminToken(t)
	member := memberToken(t)
	pcOnly := testHumanToken(t, []string{"password.change"})

	var board db.Board
	resp := request(t, srv, "POST", "/api/boards", admin, nil, map[string]any{"name": "pc-only"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin create board = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &board)

	for _, path := range []string{"/api/boards", "/api/search?q=anything"} {
		resp := request(t, srv, "GET", path, pcOnly, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("password.change-only GET %s = %d, want 403", path, resp.StatusCode)
		}
		resp = request(t, srv, "GET", path, member, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("member GET %s = %d, want 200", path, resp.StatusCode)
		}
	}

	// A board-scoped agent token with card.read keeps its narrowed view of both routes.
	var minted struct {
		Secret string `json:"secret"`
	}
	resp = request(t, srv, "POST", "/api/tokens", admin, nil, map[string]any{"label": "scoped", "board_id": board.ID, "capabilities": []string{"card.read"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mint scoped = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &minted)
	var boards []db.Board
	resp = request(t, srv, "GET", "/api/boards", minted.Secret, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scoped agent GET /api/boards = %d, want 200", resp.StatusCode)
	}
	decodeBody(t, resp, &boards)
	if len(boards) != 1 || boards[0].ID != board.ID {
		t.Errorf("scoped agent sees %d boards, want only its own", len(boards))
	}
	resp = request(t, srv, "GET", "/api/search?q=x", minted.Secret, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("scoped agent GET /api/search = %d, want 200", resp.StatusCode)
	}
}

// TestMeReportsAdminFlag pins /api/me's is_admin to the same capability
// RequireAdmin enforces (board.admin), not a guess from the token kind — a
// member token (no board.admin) must see is_admin:false even though it can
// call plenty of other routes; an admin token (board.admin) must see true.
func TestMeReportsAdminFlag(t *testing.T) {
	srv, _ := testServer(t)
	for _, c := range []struct {
		tok   string
		admin bool
	}{{memberToken(t), false}, {adminToken(t), true}} {
		resp := request(t, srv, "GET", "/api/me", c.tok, nil, nil)
		var me struct {
			IsAdmin bool `json:"is_admin"`
			IsHuman bool `json:"is_human"`
		}
		decodeBody(t, resp, &me)
		if me.IsAdmin != c.admin || !me.IsHuman {
			t.Errorf("token admin=%v: /api/me = %+v", c.admin, me)
		}
	}
}
