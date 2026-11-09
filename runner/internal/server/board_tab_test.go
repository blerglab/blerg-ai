package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestNativeBoardRequiresSignIn(t *testing.T) {
	api, _, _ := newLiveCardAPI(t)
	mux := http.NewServeMux()
	api.RegisterSessionBoardRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/board-cards/board-a/view", nil))
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "columns") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

type nativeBoardFixture struct{ paletteSearchBoard }

func (b *nativeBoardFixture) CallBoard(ctx context.Context, call mcpgw.BoardCall) (json.RawMessage, error) {
	if call.Tool == "blerg_card_get" || call.Tool == "blerg_card_move" {
		return (&liveCardBoard{column: "col-inbox"}).CallBoard(ctx, call)
	}
	if call.Tool == "blerg_release_list" {
		_, _ = b.fakeSessionBoard.CallBoard(ctx, call)
		return json.RawMessage(`{"content":[{"type":"text","text":"[]"}]}`), nil
	}
	return b.paletteSearchBoard.CallBoard(ctx, call)
}
func TestNativeBoardPagesAndSanitizesCards(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	api := fx.api
	token := func(...string) string { return fx.token(topicOwner, "human") }
	if code, body := setLevel(fx, token(), "board-a", "full"); code != 200 {
		t.Fatalf("grant %d %s", code, body)
	}
	board := &nativeBoardFixture{paletteSearchBoard: paletteSearchBoard{searchJSON: `{"cards":[{"id":"c1","board_id":"board-a","number":1,"version":0,"rank":"h","title":"Card","column_id":"doing","type":"bug","priority":"urgent","size":"M","risk":"low","complexity":"high","stale_at":"2026-01-01T00:00:00Z","tags":["to-test"],"body":"private description"}],"next_cursor":"next"}`}}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	// Agent bursts exhaust only the session-token limiter; browser reads use human proof.
	for i := 0; i <= boardCallsPerMinute; i++ {
		boardCallRetryAfter(fx.sess, time.Now(), boardCallsPerMinute)
	}
	rec := liveCardDo(api, api.HandleLiveBoardView, "GET", "/x?cursor=previous&tag=to-test&release=r1&session="+fx.sess, "board-a", "", token("session.start"), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"next_cursor":"next"`) || !strings.Contains(rec.Body.String(), "to-test") || strings.Contains(rec.Body.String(), "private") || !strings.Contains(rec.Body.String(), `"version":0`) || !strings.Contains(rec.Body.String(), `"rank":"h"`) {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"type":"bug"`, `"priority":"urgent"`, `"size":"M"`, `"risk":"low"`, `"complexity":"high"`, `"stale_at":"2026-01-01T00:00:00Z"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("missing presentation metadata %s: %s", field, rec.Body.String())
		}
	}
	for _, call := range board.calls {
		if !call.ReadOnly || call.Proof.AccountID != topicOwner || call.BoardID != "board-a" {
			t.Fatalf("wrong proof: %+v", call)
		}
		if call.Tool == "blerg_card_search" {
			var args map[string]any
			_ = json.Unmarshal(call.Arguments, &args)
			if args["cursor"] != "previous" || args["tag"] != "to-test" || args["release_id"] != "r1" || args["page"] != true || args["summary"] != true {
				t.Fatalf("args: %v", args)
			}
		}
	}
	before := board.count()
	rec = liveCardDo(api, api.HandleLiveBoardView, "GET", "/x?metadata=0&session="+fx.sess, "board-a", "", token(), "")
	if rec.Code != 200 || board.count() != before+1 || board.last().Tool != "blerg_card_search" {
		t.Fatalf("cached metadata: %d %s calls %d", rec.Code, rec.Body.String(), board.count()-before)
	}
	board.searchJSON = `{"cards":[{"board_id":"other","number":1}]}`
	rec = liveCardDo(api, api.HandleLiveBoardView, "GET", "/x?session="+fx.sess, "board-a", "", token("session.start"), "")
	if rec.Code != 502 {
		t.Fatalf("cross-board response: %d", rec.Code)
	}
}

func TestNativeBoardSessionScopeFailsClosed(t *testing.T) {
	api, board, token := newLiveCardAPI(t)
	rec := liveCardDo(api, api.HandleLiveBoardView, "GET", "/x?session=missing", "board-a", "", token("session.start"), "")
	if rec.Code != 403 || board.count() != 0 {
		t.Fatalf("response = %d %s; calls %d", rec.Code, rec.Body.String(), board.count())
	}
	rec = liveCardDo(api, api.HandleMoveLiveBoardCard, "POST", "/x?tab=1&session=missing", "board-a", "7", token("session.start"), `{"column_id":"done"}`)
	if rec.Code != 403 || board.count() != 0 {
		t.Fatalf("move = %d %s; calls %d", rec.Code, rec.Body.String(), board.count())
	}
}

// Exercise routing and browser authentication, rather than calling the handlers directly.
func TestBoardTabAccessThroughMux(t *testing.T) {
	fx, _, _ := newBoardLevelFx(t)
	board := &nativeBoardFixture{paletteSearchBoard: paletteSearchBoard{searchJSON: `{"cards":[]}`}}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	human := fx.token(topicOwner, "human")
	request := func(path, token string, move bool, want int) {
		t.Helper()
		method := "GET"
		var body any
		if move {
			method = "POST"
			body = map[string]any{"column_id": "done", "if_match": 0}
		}
		rec := filingRequest(fx, method, path, token, body)
		if rec.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body.String(), want)
		}
	}
	for _, level := range []string{"propose", "read", "file", "full", "read"} {
		if code, body := setLevel(fx, human, "board-a", level); code != 200 {
			t.Fatalf("grant: %d %s", code, body)
		}
		read, move := 200, 403
		if level == "propose" {
			read = 403
		}
		if level == "full" {
			move = 200
		}
		request("/api/board-cards/board-a/7?session="+fx.sess, human, false, 200)
		request("/api/board-cards/board-a/view?session="+fx.sess, human, false, read)
		request("/api/board-cards/board-a/7?tab=1&session="+fx.sess, human, false, read)
		request("/api/board-cards/board-a/7/move?tab=1&session="+fx.sess, human, true, move)
		if level == "full" {
			for _, body := range []map[string]any{{"column_id": "done"}, {"column_id": "done", "if_match": -1}} {
				rec := filingRequest(fx, "POST", "/api/board-cards/board-a/7/move?tab=1&session="+fx.sess, human, body)
				if rec.Code != 400 {
					t.Fatalf("invalid tab version: %d %s", rec.Code, rec.Body.String())
				}
			}
		}
	}
	// Pinned Status retains the person's own board role, even at workspace read.
	request("/api/board-cards/board-a/7/move?session="+fx.sess, human, true, 200)
	request("/api/board-cards/board-a/view", human, false, 403)
	request("/api/board-cards/board-a/7?tab=1", human, false, 403)
	request("/api/board-cards/board-a/7/move?tab=1", human, true, 403)
	if code, body := setLevel(fx, human, "board-a", "full"); code != 200 {
		t.Fatalf("grant: %d %s", code, body)
	}
	child := newChildOf(t, fx)
	passDown(t, fx, child.id, "board-a", "A", "full")
	request("/api/board-cards/board-a/7/move?tab=1&session="+child.id, human, true, 200)
	if code, body := setLevel(fx, human, "board-a", "read"); code != 200 {
		t.Fatalf("lower: %d %s", code, body)
	}
	request("/api/board-cards/board-a/7?tab=1&session="+child.id, human, false, 200)
	request("/api/board-cards/board-a/7/move?tab=1&session="+child.id, human, true, 403)
	if err := fx.api.MarkPrivate(t.Context(), fx.sess); err != nil {
		t.Fatal(err)
	}
	request("/api/board-cards/board-a/view?session="+fx.sess, fx.token("other-person", "human"), false, 404)
	request("/api/board-cards/board-a/7?tab=1&session="+fx.sess, fx.token("other-person", "human"), false, 404)
	request("/api/board-cards/board-a/7/move?tab=1&session="+fx.sess, fx.token("other-person", "human"), true, 404)
}

func TestBoardMoveCarriesVersionThroughMux(t *testing.T) {
	api, board, token := newLiveCardAPI(t)
	mux := http.NewServeMux()
	api.RegisterSessionBoardRoutes(mux)
	req := httptest.NewRequest("POST", "/api/board-cards/board-a/7/move?session=audit-only", strings.NewReader(`{"column_id":"done","if_match":0}`))
	req.Header.Set("Authorization", "Bearer "+token("session.start"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	var args map[string]any
	_ = json.Unmarshal(board.last().Arguments, &args)
	if args["if_match"] != float64(0) {
		t.Fatalf("lost version: %v", args)
	}
}

func TestStaleBoardMoveDoesNotUndoMoveOrArchive(t *testing.T) {
	for _, archived := range []bool{false, true} {
		api, board, token := newLiveCardAPI(t)
		board.version = 5
		board.archived = archived
		board.column = "concurrent-column"
		mux := http.NewServeMux()
		api.RegisterSessionBoardRoutes(mux)
		req := httptest.NewRequest("POST", "/api/board-cards/board-a/7/move", strings.NewReader(`{"column_id":"done","if_match":0}`))
		req.Header.Set("Authorization", "Bearer "+token("session.start"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "stale version") || board.column != "concurrent-column" || board.archived != archived {
			t.Fatalf("stale move: %d %s, column %s archived %v", rec.Code, rec.Body.String(), board.column, board.archived)
		}
	}
}

func TestBoardCallTransientExchangeIsNotAccessDenial(t *testing.T) {
	gateway := mcpgw.New(mcpgw.Config{Board: failingBoardExchange{}, BoardMCPURL: "https://board.example.com/mcp"})
	_, callErr := gateway.CallBoard(context.Background(), mcpgw.BoardCall{
		Proof: mcpgw.Proof{AccountID: "person", SessionID: "sign-in"}, BoardID: "board",
		Tool: "blerg_card_search", ReadOnly: true,
	})
	if callErr == nil {
		t.Fatal("expected credential exchange failure")
	}
	err := boardCallError(callErr)
	if err.Status != http.StatusBadGateway {
		t.Fatalf("transient exchange status = %d, want 502", err.Status)
	}
	if strings.Contains(err.Message, "access") {
		t.Fatalf("transient exchange blames access: %s", err.Message)
	}
}

type failingBoardExchange struct{}

func (failingBoardExchange) Exchange(context.Context, mcpgw.Proof, string, string) (mcpgw.BoardToken, error) {
	return mcpgw.BoardToken{}, context.DeadlineExceeded
}
func (f failingBoardExchange) ExchangeReadOnly(ctx context.Context, proof mcpgw.Proof, board, session string) (mcpgw.BoardToken, error) {
	return f.Exchange(ctx, proof, board, session)
}
func (failingBoardExchange) Revoke(context.Context, string, string) error { return nil }
