package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type paletteSearchBoard struct {
	liveCardBoard
	searchJSON string
}

func (b *paletteSearchBoard) CallBoard(ctx context.Context, call mcpgw.BoardCall) (json.RawMessage, error) {
	if call.Tool != "blerg_card_search" && call.Tool != "blerg_column_list" {
		return b.liveCardBoard.CallBoard(ctx, call)
	}
	_, _ = b.fakeSessionBoard.CallBoard(ctx, call)
	body := b.searchJSON
	if call.Tool == "blerg_column_list" {
		body = `[{"id":"doing","name":"In progress"}]`
	}
	raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": body}}})
	return raw, nil
}
func TestPaletteCardSearchUsesPersonsReadOnlyBoardSearchAndOnlyReturnsSummaries(t *testing.T) {
	api, _, token := newLiveCardAPI(t)
	board := &paletteSearchBoard{searchJSON: `[{"id":"c218","board_id":"board-a","number":218,"title":"Split view","column_id":"doing","body":"private description","sensitive_body":"private details"}]`}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	mux := http.NewServeMux()
	api.RegisterSessionBoardRoutes(mux)
	req := httptest.NewRequest("GET", "/api/board-cards/board-a/search?q=split+view", nil)
	req.Header.Set("Authorization", "Bearer "+token("session.start"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"column_name":"In progress"`) || !strings.Contains(rec.Body.String(), `"number":218`) {
		t.Fatalf("search = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "body") {
		t.Fatal("search exposed card descriptions")
	}
	if rec.Header().Get("Cache-Control") != "no-store" || board.count() != 2 {
		t.Fatal("search was cached or performed extra reads")
	}
	for _, call := range board.calls {
		if call.BoardID != "board-a" || call.Proof.AccountID != "user-1" || call.Proof.SessionID != testSID || !call.ReadOnly || call.SessionRef != "" {
			t.Fatalf("wrong proof or grant: %+v", call)
		}
	}
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(board.calls[0].Arguments, &args); err != nil || args.Query != "split view" || args.Limit != 100 || board.calls[0].Tool != "blerg_card_search" {
		t.Fatalf("wrong search arguments: %s", board.calls[0].Arguments)
	}
	board.searchJSON = `[{"board_id":"another-board","number":218,"title":"wrong target"}]`
	rec = liveCardDo(api, api.HandlePaletteCards, "GET", "/x?q=split", "board-a", "", token("session.start"), "")
	if rec.Code != 502 {
		t.Fatalf("accepted mismatched reference: %d", rec.Code)
	}
}
func TestPaletteCardSearchNumberAndAccessRefusals(t *testing.T) {
	api, board, token := newLiveCardAPI(t)
	rec := liveCardDo(api, api.HandlePaletteCards, "GET", "/x?q=7", "board-a", "", token("session.start"), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"number":7`) || board.last().Tool != "blerg_card_get" {
		t.Fatalf("number lookup = %d %s", rec.Code, rec.Body.String())
	}
	board.refuse = true
	rec = liveCardDo(api, api.HandlePaletteCards, "GET", "/x?q=7", "board-a", "", token("session.start"), "")
	if rec.Code != 403 || strings.Contains(rec.Body.String(), "Fix the flaky") {
		t.Fatalf("access refusal = %d %s", rec.Code, rec.Body.String())
	}
	before := board.count()
	for _, tc := range []struct {
		query, board, token string
		status              int
	}{
		{"text", "board-a", "", 401}, {"", "board-a", token("session.start"), 400},
		{strings.Repeat("x", 257), "board-a", token("session.start"), 400}, {"text", "invalid board", token("session.start"), 400},
	} {
		rec := liveCardDo(api, api.HandlePaletteCards, "GET", "/x?q="+tc.query, tc.board, "", tc.token, "")
		if rec.Code != tc.status {
			t.Fatalf("refusal = %d, want %d", rec.Code, tc.status)
		}
	}
	if board.count() != before {
		t.Fatal("refused request reached Board")
	}
}
func TestPaletteCardSearchCannotEscapeProjectSignIn(t *testing.T) {
	fx := newMCPFx(t)
	bearer := mintRunnerToken(t, fx.priv, "core-1", identity.Claims{Sub: mcpAcct, Aud: coreAuthAudience, Kind: "human", Sid: testSID, Project: "board-7", Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	rec := liveCardDo(fx.api, fx.api.HandlePaletteCards, "GET", "/x?q=text", "other-board", "", bearer, "")
	if rec.Code != 404 {
		t.Fatalf("project escape = %d %s", rec.Code, rec.Body.String())
	}
}

type canceledPaletteBoard struct {
	fakeSessionBoard
	entered chan struct{}
}

func (b *canceledPaletteBoard) CallBoard(ctx context.Context, _ mcpgw.BoardCall) (json.RawMessage, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestPaletteCardSearchCancelsBoardRequest(t *testing.T) {
	api, _, token := newLiveCardAPI(t)
	board := &canceledPaletteBoard{entered: make(chan struct{})}
	api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest("GET", "/x?q=text", nil).WithContext(ctx)
	req.SetPathValue("board", "board-a")
	req.Header.Set("Authorization", "Bearer "+token("session.start"))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); api.HandlePaletteCards(rec, req) }()
	<-board.entered
	cancel()
	<-done
	if rec.Code == 200 || strings.Contains(rec.Body.String(), `"cards"`) {
		t.Fatal("canceled search returned cards")
	}
}
