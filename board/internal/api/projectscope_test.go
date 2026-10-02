package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ghostID is a well-formed uuid that names nothing: the baseline a foreign
// object's answer must be indistinguishable from.
const ghostID = "00000000-0000-4000-8000-00000000dead"

var boardAgentCaps = []string{"card.read", "card.write", "column.write"}

// projectToken mints a fake core-issued AGENT token (aud blerg-board) carrying
// the given Project claim, signed against wireFakeCore's key.
func projectToken(t *testing.T, project string, caps []string) string {
	t.Helper()
	return projectTokenKind(t, "agent", project, caps)
}

// projectTokenKind is projectToken for any core token kind.
func projectTokenKind(t *testing.T, kind, project string, caps []string) string {
	t.Helper()
	if corePriv == nil {
		t.Fatal("projectToken: no fake core wired")
	}
	claims := identity.Claims{
		Sub: kind + "-" + project, Aud: "blerg-board", Kind: kind, Project: project,
		OnBehalfOf: "acct-1", Caps: caps, ExpiresAt: time.Now().Unix() + 3600,
	}
	hb, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": coreTestKID})
	pb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	return si + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(corePriv, []byte(si)))
}

// scopeFixture: two boards, each with columns and cards, plus a review, a
// standing agent and a runner session on board B.
type scopeFixture struct {
	srv             *httptest.Server
	pool            *pgxpool.Pool
	A, B            db.Board
	colA, colA2     db.Column
	colB            db.Column
	cardA, cardA2   db.Card
	cardB           db.Card
	reviewB         string
	standingB       string
	sessionB        string
	scoped, service string
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	srv, pool := testServer(t)
	ctx := context.Background()
	f := false
	mk := func(name string) (db.Board, []db.Column) {
		b, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: name, RequireRepo: &f})
		if err != nil {
			t.Fatal(err)
		}
		var cols []db.Column
		for _, n := range []string{"Inbox", "Done"} {
			c, err := db.CreateColumn(ctx, pool, b.ID, n, n == "Done")
			if err != nil {
				t.Fatal(err)
			}
			cols = append(cols, c)
		}
		return b, cols
	}
	fx := scopeFixture{srv: srv, pool: pool}
	var colsA, colsB []db.Column
	fx.A, colsA = mk("board-a")
	fx.B, colsB = mk("board-b")
	fx.colA, fx.colA2, fx.colB = colsA[0], colsA[1], colsB[0]
	card := func(b db.Board, col db.Column, title string) db.Card {
		res, err := db.CreateCard(ctx, pool, b.ID, db.CardParams{Title: &title, ColumnID: &col.ID}, db.EventMeta{Actor: "service"})
		if err != nil {
			t.Fatal(err)
		}
		return res.Card
	}
	fx.cardA = card(fx.A, fx.colA, "zebra in A")
	fx.cardA2 = card(fx.A, fx.colA, "second in A")
	fx.cardB = card(fx.B, fx.colB, "zebra in B")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO admission_reviews (board_id, operation, payload_hash, payload, policy_applied)
		VALUES ($1,'create','\x00','{}','ungated') RETURNING id`, fx.B.ID).Scan(&fx.reviewB))
	sa, err := db.CreateStandingAgent(ctx, pool, fx.B.ID, fx.colB.ID, "sa", "r", "p", "per_card")
	must(err)
	fx.standingB = sa.ID
	must(pool.QueryRow(ctx, `INSERT INTO runner_sessions (board_id, runner, external_session_id)
		VALUES ($1,'r','ext-b') RETURNING id`, fx.B.ID).Scan(&fx.sessionB))
	fx.scoped = projectToken(t, fx.A.ID, boardAgentCaps)
	fx.service = "svc-key-123"
	return fx
}

// do returns status and trimmed body.
func (fx scopeFixture) do(t *testing.T, method, path, token string, body any) (int, string) {
	t.Helper()
	resp := request(t, fx.srv, method, path, token, nil, body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// A project-scoped token asking for ANOTHER board's object, by any route that
// addresses it by id, gets exactly the answer a non-existent id gets: 404 and
// the same body. No existence oracle.
func TestProjectScoped_ForeignObjectsIndistinguishableFromMissing(t *testing.T) {
	fx := newScopeFixture(t)
	type c struct {
		method, path string // {X} placeholders
		body         any
	}
	cases := []c{
		// board-addressed
		{"GET", "/api/boards/{board}", nil},
		{"PATCH", "/api/boards/{board}", map[string]any{"name": "x"}},
		{"DELETE", "/api/boards/{board}", nil},
		{"GET", "/api/boards/{board}/columns", nil},
		{"POST", "/api/boards/{board}/columns", map[string]any{"name": "new"}},
		{"GET", "/api/boards/{board}/cards", nil},
		{"POST", "/api/boards/{board}/cards", map[string]any{"title": "sneak"}},
		{"GET", "/api/boards/{board}/cards/search?q=zebra", nil},
		{"GET", "/api/boards/{board}/schema", nil},
		{"GET", "/api/boards/{board}/metrics", nil},
		{"GET", "/api/boards/{board}/icon", nil},
		{"GET", "/api/boards/{board}/sessions", nil},
		{"POST", "/api/boards/{board}/sessions", map[string]any{}},
		{"GET", "/api/boards/{board}/active-sessions", nil},
		{"GET", "/api/boards/{board}/deployments", nil},
		{"GET", "/api/boards/{board}/run", nil},
		{"POST", "/api/boards/{board}/run", map[string]any{}},
		{"POST", "/api/boards/{board}/run/stop", map[string]any{}},
		{"GET", "/api/boards/{board}/standing-agents", nil},
		{"POST", "/api/boards/{board}/standing-agents", map[string]any{"name": "n"}},
		{"GET", "/api/reviews?board={board}", nil},
		// card-addressed
		{"GET", "/api/cards/{card}", nil},
		{"PATCH", "/api/cards/{card}", map[string]any{"title": "hijack"}},
		{"DELETE", "/api/cards/{card}", nil},
		{"POST", "/api/cards/{card}/move", map[string]any{"column_id": "{col}"}},
		{"POST", "/api/cards/{card}/archive", map[string]any{}},
		{"POST", "/api/cards/{card}/dependencies", map[string]any{"depends_on": "{cardA}"}},
		{"DELETE", "/api/cards/{card}/dependencies/{cardA}", nil},
		{"GET", "/api/cards/{card}/events", nil},
		{"POST", "/api/cards/{card}/comments", map[string]any{"text": "hi"}},
		{"GET", "/api/cards/{card}/diff", nil},
		{"GET", "/api/cards/{card}/git", nil},
		{"GET", "/api/cards/{card}/stats", nil},
		{"GET", "/api/cards/{card}/doc", nil},
		{"POST", "/api/cards/{card}/spawn", map[string]any{}},
		{"GET", "/api/cards/{card}/runner-sessions", nil},
		{"POST", "/api/cards/{card}/accept", map[string]any{}},
		// column-addressed
		{"PATCH", "/api/columns/{col}", map[string]any{"name": "renamed"}},
		{"DELETE", "/api/columns/{col}", nil},
		{"POST", "/api/columns/{col}/move", map[string]any{}},
		// review / standing agent / runner session
		{"GET", "/api/reviews/{review}", nil},
		{"POST", "/api/reviews/{review}/resolve", map[string]any{"decision": "reject"}},
		{"PATCH", "/api/standing-agents/{standing}", map[string]any{"name": "x"}},
		{"DELETE", "/api/standing-agents/{standing}", nil},
		{"GET", "/api/runner-sessions/{session}/events", nil},
		{"POST", "/api/runner-sessions/{session}/message", map[string]any{"text": "x"}},
		{"POST", "/api/runner-sessions/{session}/interrupt", map[string]any{}},
		{"POST", "/api/runner-sessions/{session}/model", map[string]any{"model": "claude-sonnet-5"}},
		{"POST", "/api/runner-sessions/{session}/close", map[string]any{}},
	}
	foreign := strings.NewReplacer(
		"{board}", fx.B.ID, "{card}", fx.cardB.ID, "{col}", fx.colB.ID, "{cardA}", fx.cardA.ID,
		"{review}", fx.reviewB, "{standing}", fx.standingB, "{session}", fx.sessionB)
	ghost := strings.NewReplacer(
		"{board}", ghostID, "{card}", ghostID, "{col}", ghostID, "{cardA}", fx.cardA.ID,
		"{review}", ghostID, "{standing}", ghostID, "{session}", ghostID)
	sub := func(r *strings.Replacer, v any) any {
		if v == nil {
			return nil
		}
		b, _ := json.Marshal(v)
		var out any
		_ = json.Unmarshal([]byte(r.Replace(string(b))), &out)
		return out
	}
	for _, tc := range cases {
		fs, fb := fx.do(t, tc.method, foreign.Replace(tc.path), fx.scoped, sub(foreign, tc.body))
		gs, gb := fx.do(t, tc.method, ghost.Replace(tc.path), fx.scoped, sub(ghost, tc.body))
		if fs != http.StatusNotFound {
			t.Errorf("%s %s: foreign object status %d (%s), want 404", tc.method, tc.path, fs, fb)
		}
		if fs != gs || fb != gb {
			t.Errorf("%s %s: foreign answers %d %q but missing answers %d %q (existence leak)",
				tc.method, tc.path, fs, fb, gs, gb)
		}
	}
	// Nothing above changed board B.
	var n int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM cards WHERE board_id = $1`, fx.B.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("board B cards = %d (%v), want 1", n, err)
	}
	var title string
	_ = fx.pool.QueryRow(context.Background(), `SELECT title FROM cards WHERE id = $1`, fx.cardB.ID).Scan(&title)
	if title != "zebra in B" {
		t.Errorf("board B card title = %q", title)
	}
}

// Instance-wide routes have no board the token could be scoped to: refused
// with 403 (never reachable), and a board-list / search only ever show the
// token's own board.
func TestProjectScoped_InstanceWideRoutes(t *testing.T) {
	fx := newScopeFixture(t)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/boards", map[string]any{"name": "nope"}},
		{"GET", "/api/tokens", nil},
		{"POST", "/api/tokens", map[string]any{"label": "x"}},
		{"POST", "/api/tokens/refresh", map[string]any{}},
		{"POST", "/api/tokens/" + ghostID + "/revoke", nil},
		{"GET", "/api/overview", nil},
		{"GET", "/api/sessions", nil},
		{"GET", "/api/reviews", nil},
		{"POST", "/api/deployments", map[string]any{"board_id": fx.A.ID, "env": "prod"}},
		{"GET", "/ws?board=" + fx.B.ID, nil},
	} {
		st, body := fx.do(t, tc.method, tc.path, fx.scoped, tc.body)
		if st != http.StatusForbidden {
			t.Errorf("%s %s = %d (%s), want 403", tc.method, tc.path, st, body)
		}
	}
	var n int
	_ = fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM boards`).Scan(&n)
	if n != 2 {
		t.Errorf("boards = %d, a scoped token created one", n)
	}

	// List: only its own board.
	st, body := fx.do(t, "GET", "/api/boards", fx.scoped, nil)
	var boards []db.Board
	_ = json.Unmarshal([]byte(body), &boards)
	if st != 200 || len(boards) != 1 || boards[0].ID != fx.A.ID {
		t.Errorf("GET /api/boards = %d %s, want only board A", st, body)
	}
	// Global search: only its own board, even when it names the other one.
	for _, q := range []string{"/api/search?q=zebra", "/api/search?q=zebra&board=" + fx.B.ID} {
		st, body = fx.do(t, "GET", q, fx.scoped, nil)
		var cards []db.Card
		_ = json.Unmarshal([]byte(body), &cards)
		if st != 200 || len(cards) != 1 || cards[0].ID != fx.cardA.ID {
			t.Errorf("GET %s = %d %s, want only board A's card", q, st, body)
		}
	}
}

// The token still works on its own board, end to end.
func TestProjectScoped_SameBoardSucceeds(t *testing.T) {
	fx := newScopeFixture(t)
	ok := func(method, path string, body any, want int) string {
		t.Helper()
		st, b := fx.do(t, method, path, fx.scoped, body)
		if st != want {
			t.Fatalf("%s %s = %d (%s), want %d", method, path, st, b, want)
		}
		return b
	}
	ok("GET", "/api/boards/"+fx.A.ID, nil, 200)
	ok("GET", "/api/boards/"+fx.A.ID+"/schema", nil, 200)
	ok("GET", "/api/boards/"+fx.A.ID+"/columns", nil, 200)
	ok("GET", "/api/boards/"+fx.A.ID+"/cards", nil, 200)
	ok("GET", "/api/boards/"+fx.A.ID+"/cards/search?q=zebra", nil, 200)
	ok("GET", "/api/reviews?board="+fx.A.ID, nil, 200)
	ok("GET", "/api/cards/"+fx.cardA.ID, nil, 200)
	ok("GET", "/api/cards/"+fx.cardA.ID+"/events", nil, 200)
	created := ok("POST", "/api/boards/"+fx.A.ID+"/cards", map[string]any{"title": "new one"}, 201)
	var nc db.Card
	_ = json.Unmarshal([]byte(created), &nc)
	ok("PATCH", "/api/cards/"+nc.ID, map[string]any{"title": "renamed"}, 200)
	ok("POST", "/api/cards/"+nc.ID+"/move", map[string]any{"column_id": fx.colA2.ID}, 200)
	ok("POST", "/api/cards/"+nc.ID+"/comments", map[string]any{"text": "note"}, 201)
	ok("POST", "/api/cards/"+nc.ID+"/dependencies", map[string]any{"depends_on": fx.cardA.ID}, 200)
	ok("POST", "/api/boards/"+fx.A.ID+"/columns", map[string]any{"name": "extra"}, 201)
	ok("POST", "/api/columns/"+fx.colA.ID+"/move", map[string]any{}, 200)
	ok("POST", "/api/cards/"+nc.ID+"/archive", map[string]any{}, 200)
}

// A card on the token's own board cannot be pointed at another board's
// column or card: the token cannot smuggle data across the boundary through a
// body id either.
func TestProjectScoped_BodyIDsCannotCrossBoards(t *testing.T) {
	fx := newScopeFixture(t)
	ctx := context.Background()

	// Move into a foreign column: refused, card stays put.
	st, _ := fx.do(t, "POST", "/api/cards/"+fx.cardA.ID+"/move", fx.scoped, map[string]any{"column_id": fx.colB.ID})
	if st < 400 {
		t.Errorf("move into foreign column = %d, want an error", st)
	}
	// A dependency on a foreign card answers like a missing one.
	fs, fb := fx.do(t, "POST", "/api/cards/"+fx.cardA.ID+"/dependencies", fx.scoped, map[string]any{"depends_on": fx.cardB.ID})
	gs, gb := fx.do(t, "POST", "/api/cards/"+fx.cardA.ID+"/dependencies", fx.scoped, map[string]any{"depends_on": ghostID})
	if fs != http.StatusNotFound || fs != gs || fb != gb {
		t.Errorf("foreign dependency = %d %q, missing = %d %q; want identical 404", fs, fb, gs, gb)
	}
	// Create with a foreign column_id never lands a card there.
	fx.do(t, "POST", "/api/boards/"+fx.A.ID+"/cards", fx.scoped,
		map[string]any{"title": "x", "column_id": fx.colB.ID})
	// External-id refresh path must not move the card into a foreign column.
	ext := "ext-1"
	title := "ext card"
	res, err := db.CreateCard(ctx, fx.pool, fx.A.ID, db.CardParams{Title: &title, ExternalID: &ext}, db.EventMeta{Actor: "service"})
	if err != nil {
		t.Fatal(err)
	}
	fx.do(t, "POST", "/api/boards/"+fx.A.ID+"/cards", fx.scoped,
		map[string]any{"title": "ext card", "external_id": ext, "column_id": fx.colB.ID})

	var stray int
	_ = fx.pool.QueryRow(ctx, `SELECT count(*) FROM cards c JOIN board_columns bc ON bc.id = c.column_id
		WHERE c.board_id <> bc.board_id`).Scan(&stray)
	if stray != 0 {
		t.Errorf("%d card(s) sit in a column of another board", stray)
	}
	_ = res
}

// A token with NO Project claim behaves exactly as before: instance-wide,
// capability-gated. Humans and the service key are unaffected.
func TestProjectScoped_UnscopedTokensUnchanged(t *testing.T) {
	fx := newScopeFixture(t)
	noProject := projectToken(t, "", boardAgentCaps)
	for name, tok := range map[string]string{"core agent, no project": noProject, "member": memberToken(t), "service key": fx.service} {
		for _, path := range []string{
			"/api/boards/" + fx.A.ID, "/api/boards/" + fx.B.ID,
			"/api/cards/" + fx.cardA.ID, "/api/cards/" + fx.cardB.ID,
			"/api/boards/" + fx.B.ID + "/columns", "/api/reviews/" + fx.reviewB,
			"/api/reviews?board=" + fx.B.ID,
		} {
			if st, b := fx.do(t, "GET", path, tok, nil); st != 200 {
				t.Errorf("%s: GET %s = %d (%s), want 200", name, path, st, b)
			}
		}
		st, b := fx.do(t, "GET", "/api/boards", tok, nil)
		var boards []db.Board
		_ = json.Unmarshal([]byte(b), &boards)
		if st != 200 || len(boards) != 2 {
			t.Errorf("%s: GET /api/boards = %d, %d boards, want 2", name, st, len(boards))
		}
		st, b = fx.do(t, "GET", "/api/search?q=zebra", tok, nil)
		var cards []db.Card
		_ = json.Unmarshal([]byte(b), &cards)
		if st != 200 || len(cards) != 2 {
			t.Errorf("%s: global search = %d, %d cards, want 2", name, st, len(cards))
		}
	}
	// A human core token WITHOUT a Project claim is not narrowed either.
	human := testCoreToken(t, "human", boardAgentCaps)
	if st, _ := fx.do(t, "GET", "/api/boards/"+fx.B.ID, human, nil); st != 200 {
		t.Errorf("human token GET other board = %d, want 200", st)
	}
	// Native board-scoped agent tokens keep their existing behaviour (403 on
	// a foreign board).
	_, raw, err := db.MintToken(context.Background(), fx.pool, &fx.A.ID, "agent", "native", boardAgentCaps, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := fx.do(t, "GET", "/api/boards/"+fx.B.ID, raw, nil); st != http.StatusForbidden {
		t.Errorf("native scoped token GET other board = %d, want 403 (unchanged)", st)
	}
}

// ── MCP ──────────────────────────────────────────────────────────────────────

type mcpResult struct {
	IsError bool
	Text    string
}

func (fx scopeFixture) mcpCall(t *testing.T, token, tool string, args map[string]any) mcpResult {
	t.Helper()
	resp := request(t, fx.srv, "POST", "/mcp", token, nil, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	var out struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	decodeBody(t, resp, &out)
	r := mcpResult{IsError: out.Result.IsError}
	if len(out.Result.Content) > 0 {
		r.Text = out.Result.Content[0].Text
	}
	return r
}

func TestProjectScoped_MCP(t *testing.T) {
	fx := newScopeFixture(t)
	type c struct {
		tool string
		args func(board, card, col, review string) map[string]any
	}
	cases := []c{
		{"blerg_board_get", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b} }},
		{"blerg_board_schema", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b} }},
		{"blerg_board_update", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b, "name": "x"} }},
		{"blerg_column_list", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b} }},
		{"blerg_column_create", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b, "name": "n"} }},
		{"blerg_column_move", func(_, _, col, _ string) map[string]any { return map[string]any{"column_id": col} }},
		{"blerg_card_search", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b, "query": "zebra"} }},
		{"blerg_card_get", func(_, cd, _, _ string) map[string]any { return map[string]any{"card_id": cd} }},
		{"blerg_card_create", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b, "title": "sneak"} }},
		{"blerg_card_update", func(_, cd, _, _ string) map[string]any { return map[string]any{"card_id": cd, "title": "hijack"} }},
		{"blerg_card_move", func(_, cd, col, _ string) map[string]any { return map[string]any{"card_id": cd, "column_id": col} }},
		{"blerg_card_archive", func(_, cd, _, _ string) map[string]any { return map[string]any{"card_id": cd} }},
		{"blerg_card_link", func(_, cd, _, _ string) map[string]any {
			return map[string]any{"card_id": cd, "kind": "url", "url": "https://x.example"}
		}},
		{"blerg_card_comment", func(_, cd, _, _ string) map[string]any { return map[string]any{"card_id": cd, "text": "hi"} }},
		{"blerg_review_get", func(_, _, _, rv string) map[string]any { return map[string]any{"review_id": rv} }},
		// by board + number instead of card id
		{"blerg_card_get", func(b, _, _, _ string) map[string]any { return map[string]any{"board_id": b, "number": 1} }},
		{"blerg_card_comment", func(b, _, _, _ string) map[string]any {
			return map[string]any{"board_id": b, "number": 1, "text": "hi"}
		}},
		{"blerg_session_set_model", func(_, _, _, _ string) map[string]any {
			return map[string]any{"model": "claude-sonnet-5", "runner_session_id": fx.sessionB}
		}},
	}
	for _, tc := range cases {
		fr := fx.mcpCall(t, fx.scoped, tc.tool, tc.args(fx.B.ID, fx.cardB.ID, fx.colB.ID, fx.reviewB))
		gr := fx.mcpCall(t, fx.scoped, tc.tool, tc.args(ghostID, ghostID, ghostID, ghostID))
		if !fr.IsError {
			t.Errorf("%s on board B succeeded: %s", tc.tool, fr.Text)
		}
		if fr != gr {
			t.Errorf("%s: foreign -> %+v, missing -> %+v (existence leak)", tc.tool, fr, gr)
		}
	}
	// board_create / board_list / session_set_model(self)
	if r := fx.mcpCall(t, fx.scoped, "blerg_board_create", map[string]any{"name": "nope"}); !r.IsError {
		t.Errorf("blerg_board_create by a scoped token succeeded: %s", r.Text)
	}
	r := fx.mcpCall(t, fx.scoped, "blerg_board_list", nil)
	var boards []db.Board
	_ = json.Unmarshal([]byte(r.Text), &boards)
	if r.IsError || len(boards) != 1 || boards[0].ID != fx.A.ID {
		t.Errorf("blerg_board_list = %+v, want only board A", r)
	}
	var n int
	_ = fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM boards`).Scan(&n)
	if n != 2 {
		t.Errorf("boards = %d after scoped board_create", n)
	}

	// Same board: works.
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"blerg_board_get", map[string]any{"board_id": fx.A.ID}},
		{"blerg_board_schema", map[string]any{"board_id": fx.A.ID}},
		{"blerg_column_list", map[string]any{"board_id": fx.A.ID}},
		{"blerg_card_search", map[string]any{"board_id": fx.A.ID, "query": "zebra"}},
		{"blerg_card_get", map[string]any{"card_id": fx.cardA.ID}},
		{"blerg_card_get", map[string]any{"board_id": fx.A.ID, "number": fx.cardA.Number}},
		{"blerg_card_create", map[string]any{"board_id": fx.A.ID, "title": "mcp new"}},
		{"blerg_card_update", map[string]any{"card_id": fx.cardA.ID, "title": "zebra in A v2"}},
		{"blerg_card_move", map[string]any{"card_id": fx.cardA.ID, "column_id": fx.colA2.ID}},
		{"blerg_card_comment", map[string]any{"card_id": fx.cardA.ID, "text": "hello"}},
		{"blerg_card_link", map[string]any{"card_id": fx.cardA.ID, "kind": "url", "url": "https://x.example"}},
		{"blerg_column_create", map[string]any{"board_id": fx.A.ID, "name": "mcp col"}},
		{"blerg_column_move", map[string]any{"column_id": fx.colA.ID}},
		{"blerg_card_archive", map[string]any{"card_id": fx.cardA2.ID}},
	} {
		if r := fx.mcpCall(t, fx.scoped, tc.tool, tc.args); r.IsError {
			t.Errorf("same-board %s failed: %s", tc.tool, r.Text)
		}
	}

	// Unscoped core agent token: unchanged (can reach board B).
	noProject := projectToken(t, "", boardAgentCaps)
	if r := fx.mcpCall(t, noProject, "blerg_card_get", map[string]any{"card_id": fx.cardB.ID}); r.IsError {
		t.Errorf("unscoped token blerg_card_get on board B failed: %s", r.Text)
	}
	if r := fx.mcpCall(t, noProject, "blerg_review_get", map[string]any{"review_id": fx.reviewB}); r.IsError {
		t.Errorf("unscoped token blerg_review_get failed: %s", r.Text)
	}
	r = fx.mcpCall(t, noProject, "blerg_board_list", nil)
	_ = json.Unmarshal([]byte(r.Text), &boards)
	if len(boards) != 2 {
		t.Errorf("unscoped blerg_board_list = %d boards, want 2", len(boards))
	}

	// tools/list hides tools a scoped token can never use.
	resp := request(t, fx.srv, "POST", "/mcp", fx.scoped, nil,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	var tl struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	decodeBody(t, resp, &tl)
	names := map[string]bool{}
	for _, tool := range tl.Result.Tools {
		names[tool.Name] = true
	}
	if names["blerg_board_create"] || names["blerg_session_set_model"] || !names["blerg_card_get"] {
		t.Errorf("scoped tools/list = %v", fmt.Sprint(names))
	}
}

// A Project claim scopes ANY core-issued token kind over REST and MCP, not
// only "agent": a human- or service-kind token minted with a Project is
// confined to that board like an agent token is.
func TestProjectScoped_EveryTokenKindIsConfined(t *testing.T) {
	fx := newScopeFixture(t)
	for _, kind := range []string{"agent", "human", "service", "cron"} {
		tok := projectTokenKind(t, kind, fx.A.ID, boardAgentCaps)
		if st, _ := fx.do(t, "GET", "/api/boards/"+fx.B.ID, tok, nil); st != http.StatusNotFound {
			t.Errorf("%s: GET other board = %d, want 404", kind, st)
		}
		if st, _ := fx.do(t, "GET", "/api/cards/"+fx.cardB.ID, tok, nil); st != http.StatusNotFound {
			t.Errorf("%s: GET other board's card = %d, want 404", kind, st)
		}
		if st, _ := fx.do(t, "GET", "/api/boards/"+fx.A.ID, tok, nil); st != http.StatusOK {
			t.Errorf("%s: GET own board = %d, want 200", kind, st)
		}
		st, body := fx.do(t, "GET", "/api/boards", tok, nil)
		var boards []db.Board
		_ = json.Unmarshal([]byte(body), &boards)
		if st != 200 || len(boards) != 1 || boards[0].ID != fx.A.ID {
			t.Errorf("%s: GET /api/boards = %d %s, want only board A", kind, st, body)
		}
		st, body = fx.do(t, "GET", "/api/search?q=zebra&board="+fx.B.ID, tok, nil)
		var cards []db.Card
		_ = json.Unmarshal([]byte(body), &cards)
		if st != 200 || len(cards) != 1 || cards[0].ID != fx.cardA.ID {
			t.Errorf("%s: search = %d %s, want only board A's card", kind, st, body)
		}
		if r := fx.mcpCall(t, tok, "blerg_card_get", map[string]any{"card_id": fx.cardB.ID}); !r.IsError {
			t.Errorf("%s: MCP card_get on another board succeeded: %s", kind, r.Text)
		}
	}
}

// A malformed Project claim fails closed over REST: scoped to nothing.
func TestProjectScoped_MalformedProjectSeesNothing(t *testing.T) {
	fx := newScopeFixture(t)
	for _, project := range []string{"bad\nproject", "bad\x00project", strings.Repeat("x", 65)} {
		tok := projectToken(t, project, boardAgentCaps)
		for _, path := range []string{
			"/api/boards/" + fx.A.ID, "/api/boards/" + fx.B.ID, "/api/cards/" + fx.cardA.ID,
			"/api/boards/" + fx.A.ID + "/columns", "/api/reviews/" + fx.reviewB,
		} {
			if st, b := fx.do(t, "GET", path, tok, nil); st == http.StatusOK {
				t.Errorf("project %q: GET %s = 200 (%s), want refusal", project, path, b)
			}
		}
		st, body := fx.do(t, "GET", "/api/boards", tok, nil)
		var boards []db.Board
		_ = json.Unmarshal([]byte(body), &boards)
		if st == http.StatusOK && len(boards) != 0 {
			t.Errorf("project %q: GET /api/boards = %d boards, want none", project, len(boards))
		}
	}
}

// A Project-scoped core token exists for card work. It must not start runner
// sessions or runs (those mint native 12-hour board tokens), on its OWN board
// (403) and on a foreign one (404, same as a missing id). Nothing is minted.
func TestProjectScoped_CannotStartSessionsOrRuns(t *testing.T) {
	fx := newScopeFixture(t)
	var tokensBefore int
	_ = fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM tokens`).Scan(&tokensBefore)
	for _, kind := range []string{"agent", "human"} {
		tok := projectTokenKind(t, kind, fx.A.ID, append([]string{"board.admin"}, boardAgentCaps...))
		for _, tc := range []struct{ path string }{
			{"/api/boards/" + fx.A.ID + "/sessions"},
			{"/api/boards/" + fx.A.ID + "/run"},
			{"/api/cards/" + fx.cardA.ID + "/spawn"},
		} {
			if st, b := fx.do(t, "POST", tc.path, tok, map[string]any{}); st != http.StatusForbidden {
				t.Errorf("%s: POST %s = %d (%s), want 403", kind, tc.path, st, b)
			}
		}
		for _, path := range []string{
			"/api/boards/" + fx.B.ID + "/sessions", "/api/boards/" + fx.B.ID + "/run",
			"/api/cards/" + fx.cardB.ID + "/spawn",
		} {
			if st, b := fx.do(t, "POST", path, tok, map[string]any{}); st != http.StatusNotFound {
				t.Errorf("%s: POST foreign %s = %d (%s), want 404", kind, path, st, b)
			}
		}
	}
	var tokensAfter int
	_ = fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM tokens`).Scan(&tokensAfter)
	if tokensAfter != tokensBefore {
		t.Errorf("tokens %d -> %d: a scoped token caused a token to be minted", tokensBefore, tokensAfter)
	}
	// Stopping a run and reading state stay available on its own board.
	tok := projectToken(t, fx.A.ID, boardAgentCaps)
	if st, _ := fx.do(t, "GET", "/api/boards/"+fx.A.ID+"/run", tok, nil); st != http.StatusOK {
		t.Errorf("GET own run state = %d, want 200", st)
	}
}

// rawMCP posts an arbitrary JSON-RPC body (so a test can send duplicate keys,
// which a Go map cannot express) and returns the decoded envelope.
func (fx scopeFixture) rawMCP(t *testing.T, token, body string) (errCode int, res mcpResult) {
	t.Helper()
	resp := request(t, fx.srv, "POST", "/mcp", token, nil, json.RawMessage(body))
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	decodeBody(t, resp, &out)
	if out.Error != nil {
		errCode = out.Error.Code
	}
	res.IsError = out.Result.IsError
	if len(out.Result.Content) > 0 {
		res.Text = out.Result.Content[0].Text
	}
	return errCode, res
}

// reviewOn inserts a review on the board and returns its id.
func (fx scopeFixture) reviewOn(t *testing.T, boardID string) string {
	t.Helper()
	var id string
	if err := fx.pool.QueryRow(context.Background(), `INSERT INTO admission_reviews (board_id, operation, payload_hash, payload, policy_applied)
		VALUES ($1,'create','\x00','{}','ungated') RETURNING id`, boardID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The scope check and dispatch once decoded the arguments differently: a
// case-variant duplicate key passed the check on one value (review A) and
// dispatched another (review B). Two boards, two real reviews: the exploit
// encodings must never return B's review, in either key order, and are
// refused as invalid params.
func TestMCP_CaseVariantDuplicateKeysCannotReadForeignReview(t *testing.T) {
	fx := newScopeFixture(t)
	reviewA := fx.reviewOn(t, fx.A.ID)
	if reviewA == fx.reviewB {
		t.Fatal("fixture: same review")
	}
	call := func(args string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"blerg_review_get","arguments":` + args + `}}`
	}
	for _, args := range []string{
		`{"review_id":"` + fx.reviewB + `","REVIEW_ID":"` + reviewA + `","fields":"{}"}`,
		`{"REVIEW_ID":"` + reviewA + `","review_id":"` + fx.reviewB + `"}`,
		`{"review_id":"` + reviewA + `","Review_Id":"` + fx.reviewB + `"}`,
		`{"review_id":"` + reviewA + `","review_id":"` + fx.reviewB + `"}`,
		`{"review_id":"` + fx.reviewB + `","review_id":"` + reviewA + `"}`,
	} {
		code, res := fx.rawMCP(t, fx.scoped, call(args))
		if strings.Contains(res.Text, fx.B.ID) || strings.Contains(res.Text, fx.reviewB) {
			t.Errorf("args %s returned board B's review: %s", args, res.Text)
		}
		if code != -32602 {
			t.Errorf("args %s: rpc error code %d (%+v), want -32602 invalid params", args, code, res)
		}
	}
	// Sanity: the honest call on its own review still works.
	if code, res := fx.rawMCP(t, fx.scoped, call(`{"review_id":"`+reviewA+`"}`)); code != 0 || res.IsError || !strings.Contains(res.Text, fx.A.ID) {
		t.Errorf("own review: code %d %+v", code, res)
	}
}

// blerg_review_get authorizes on the loaded review's board, like REST does: a
// native board-scoped agent token (no Project claim) cannot read another
// board's review over MCP either.
func TestMCP_ReviewGetChecksReviewBoard(t *testing.T) {
	fx := newScopeFixture(t)
	reviewA := fx.reviewOn(t, fx.A.ID)
	_, raw, err := db.MintToken(context.Background(), fx.pool, &fx.A.ID, "agent", "native", boardAgentCaps, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r := fx.mcpCall(t, raw, "blerg_review_get", map[string]any{"review_id": fx.reviewB}); !r.IsError || strings.Contains(r.Text, fx.B.ID) {
		t.Errorf("native board-A token read board B's review: %+v", r)
	}
	if r := fx.mcpCall(t, raw, "blerg_review_get", map[string]any{"review_id": reviewA}); r.IsError {
		t.Errorf("native board-A token own review failed: %+v", r)
	}
}
