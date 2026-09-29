package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// These tests poke runner_sessions/runner_events directly — the stats and
// metrics queries read straight off those tables, so exercising them
// doesn't need a live runner or the ingest loop.

func TestCardStatsSurfacesModelPerRole(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}

	var workerID, reviewerID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1, $2, 'blerg-runner', 'ext-worker', 'stopped', 'worker') RETURNING id`,
		res.Card.ID, board.ID).Scan(&workerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1, $2, 'blerg-runner', 'ext-reviewer', 'stopped', 'reviewer') RETURNING id`,
		res.Card.ID, board.ID).Scan(&reviewerID); err != nil {
		t.Fatal(err)
	}

	// turn_done shape: flat payload, model alongside usage.
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_events (runner_session_id, seq, ts, kind, payload) VALUES
		($1, 1, now(), 'status', '{"model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":20},"stop_reason":"end_turn"}')`,
		workerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_events (runner_session_id, seq, ts, kind, payload) VALUES
		($1, 1, now(), 'status', '{"model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":8},"stop_reason":"end_turn"}')`,
		reviewerID); err != nil {
		t.Fatal(err)
	}

	resp := request(t, srv, "GET", "/api/cards/"+res.Card.ID+"/stats", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats: %d", resp.StatusCode)
	}
	var out struct {
		Models map[string]string `json:"models"`
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Models["worker"] != "claude-sonnet-5" || out.Models["reviewer"] != "claude-opus-5" {
		t.Fatalf("expected per-role models, got: %+v", out.Models)
	}
	if out.Tokens.Input != 15 || out.Tokens.Output != 28 {
		t.Fatalf("token aggregation broke: %+v", out.Tokens)
	}
}

func TestBoardMetricsTokensByModel(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("build the thing"), Repos: &[]string{"blerg-board"}}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}

	var sonnetID, opusID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1, $2, 'blerg-runner', 'ext-sonnet', 'stopped', 'worker') RETURNING id`,
		res.Card.ID, board.ID).Scan(&sonnetID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role)
		VALUES ($1, $2, 'blerg-runner', 'ext-opus', 'stopped', 'reviewer') RETURNING id`,
		res.Card.ID, board.ID).Scan(&opusID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_events (runner_session_id, seq, ts, kind, payload) VALUES
		($1, 1, now(), 'status', '{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":50}}')`,
		sonnetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_events (runner_session_id, seq, ts, kind, payload) VALUES
		($1, 1, now(), 'status', '{"model":"claude-opus-5","usage":{"input_tokens":7,"output_tokens":3}}')`,
		opusID); err != nil {
		t.Fatal(err)
	}

	resp := request(t, srv, "GET", "/api/boards/"+board.ID+"/metrics", cookie, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d", resp.StatusCode)
	}
	var out struct {
		TokensByModel []struct {
			Day       string `json:"day"`
			Model     string `json:"model"`
			TokensIn  int64  `json:"tokens_in"`
			TokensOut int64  `json:"tokens_out"`
		} `json:"tokens_by_model"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	byModel := map[string][2]int64{}
	for _, r := range out.TokensByModel {
		byModel[r.Model] = [2]int64{r.TokensIn, r.TokensOut}
	}
	if byModel["claude-sonnet-5"] != [2]int64{100, 50} {
		t.Fatalf("sonnet tokens: %+v", byModel["claude-sonnet-5"])
	}
	if byModel["claude-opus-5"] != [2]int64{7, 3} {
		t.Fatalf("opus tokens: %+v", byModel["claude-opus-5"])
	}
}
