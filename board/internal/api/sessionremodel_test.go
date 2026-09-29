package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// spawnForRemodel runs a card and returns the runner session id blerg-board
// recorded for it — the thing a re-model targets.
func spawnForRemodel(t *testing.T, srv *httptest.Server, cookie string, cardID string) string {
	t.Helper()
	resp := request(t, srv, "POST", "/api/cards/"+cardID+"/spawn", cookie, nil, map[string]string{})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("spawn: HTTP %d", resp.StatusCode)
	}
	var out struct {
		RunnerSessionID string `json:"runner_session_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	if out.RunnerSessionID == "" {
		t.Fatal("spawn returned no runner_session_id")
	}
	return out.RunnerSessionID
}

// TestSessionRemodelSelfService is the path the card is about: a running
// session re-models ITSELF through blerg-board, keeps its session, and leaves the
// reason on the card's trail.
func TestSessionRemodelSelfService(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "remodel", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("hard card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	card := res.Card
	rsID := spawnForRemodel(t, srv, cookie, card.ID)

	// the spawn recorded the model it resolved
	var stored string
	if err := pool.QueryRow(ctx, `SELECT model FROM runner_sessions WHERE id = $1`, rsID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "board-default" {
		t.Fatalf("spawned model recorded as %q, want board-default", stored)
	}

	// the session's own token: board-scoped, tagged with its session id
	_, sessionTok, err := db.MintToken(ctx, pool, &board.ID, "agent",
		"card #1 worker session [rs:"+rsID+"]",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	resp := request(t, srv, "POST", "/api/runner-sessions/self/model", sessionTok, nil,
		map[string]string{"model": "claude-opus-5", "reason": "the refactor is deeper than the card implied"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("self re-model: HTTP %d", resp.StatusCode)
	}
	var change api.SessionModelChange
	_ = json.NewDecoder(resp.Body).Decode(&change)
	_ = resp.Body.Close()
	if change.From != "board-default" || change.To != "claude-opus-5" || change.AppliesTo != "next turn" {
		t.Fatalf("change = %+v", change)
	}

	// the driver was asked, against the session's EXTERNAL id
	if len(fake.modelSet) != 1 || fake.modelSet[0].SessionID != "ext-123" ||
		fake.modelSet[0].Model != "claude-opus-5" {
		t.Fatalf("driver calls = %+v", fake.modelSet)
	}
	// no respawn: the session survived the change
	if len(fake.started) != 1 {
		t.Fatalf("re-model must not spawn a session: %d starts", len(fake.started))
	}
	if err := pool.QueryRow(ctx, `SELECT model FROM runner_sessions WHERE id = $1`, rsID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "claude-opus-5" {
		t.Fatalf("stored model = %q after re-model", stored)
	}

	// and the card's trail says so, with the reason
	events, err := db.ListCardEvents(ctx, pool, card.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "comment" && strings.Contains(string(ev.Data), "deeper than the card implied") &&
			strings.Contains(string(ev.Data), "claude-opus-5") {
			found = true
		}
	}
	if !found {
		t.Fatal("no trail comment for the model change")
	}

	// re-setting the same model is a no-op, not another runner round trip
	resp = request(t, srv, "POST", "/api/runner-sessions/self/model", sessionTok, nil,
		map[string]string{"model": "claude-opus-5"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-op re-model: HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if len(fake.modelSet) != 1 {
		t.Fatalf("no-op re-model still called the runner: %+v", fake.modelSet)
	}
}

// A session may re-model itself and nothing else: the `[rs:<id>]` tag on its
// token is the whole authorisation story, so a sibling session on the same
// board (same capabilities!) is refused.
func TestSessionRemodelOtherSessionForbidden(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "neighbours", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	res, _ := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("someone else's card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	rsID := spawnForRemodel(t, srv, cookie, res.Card.ID)

	// a neighbour session's token: same board, same caps, different session
	_, neighbour, err := db.MintToken(ctx, pool, &board.ID, "agent",
		"card #2 worker session [rs:00000000-0000-0000-0000-000000000000]",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/api/runner-sessions/"+rsID+"/model", neighbour, nil,
		map[string]string{"model": "claude-haiku-4-5"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("neighbour re-model: HTTP %d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if len(fake.modelSet) != 0 {
		t.Fatalf("forbidden call still reached the runner: %+v", fake.modelSet)
	}

	// a token with no session tag has no "self" to resolve
	_, plain, err := db.MintToken(ctx, pool, &board.ID, "agent", "sweeper",
		[]string{"card.read", "card.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp = request(t, srv, "POST", "/api/runner-sessions/self/model", plain, nil,
		map[string]string{"model": "claude-haiku-4-5"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("self with no session tag: HTTP %d, want 422", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// a human curating the board may re-model any session on it
	resp = request(t, srv, "POST", "/api/runner-sessions/"+rsID+"/model", cookie, nil,
		map[string]string{"model": "claude-haiku-4-5", "reason": "cheap enough for the rest"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("human re-model: HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if len(fake.modelSet) != 1 {
		t.Fatalf("human re-model driver calls = %+v", fake.modelSet)
	}
}

// The failure modes a caller has to be able to tell apart: a runner that
// can't do it (501, and nothing recorded), a session that is over, and a
// model id that could not be one.
func TestSessionRemodelFailureModes(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "failures", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	res, _ := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	rsID := spawnForRemodel(t, srv, cookie, res.Card.ID)

	post := func(id string, body map[string]string) int {
		t.Helper()
		resp := request(t, srv, "POST", "/api/runner-sessions/"+id+"/model", cookie, nil, body)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if code := post(rsID, map[string]string{"model": "claude sonnet 5"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("model with spaces: HTTP %d, want 422", code)
	}
	if code := post(rsID, map[string]string{"model": ""}); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty model: HTTP %d, want 422", code)
	}
	if code := post("11111111-1111-1111-1111-111111111111",
		map[string]string{"model": "claude-sonnet-5"}); code != http.StatusNotFound {
		t.Fatalf("unknown session: HTTP %d, want 404", code)
	}
	// an id Postgres could not even compare is still "no such session", not a
	// server error
	if code := post("not-a-session", map[string]string{"model": "claude-sonnet-5"}); code != http.StatusNotFound {
		t.Fatalf("malformed session id: HTTP %d, want 404", code)
	}

	// a runner without the verb: 501, and blerg-board records nothing — the session
	// is still on the model it was spawned with
	fake.setModelErr = runner.ErrUnsupported
	if code := post(rsID, map[string]string{"model": "claude-sonnet-5"}); code != http.StatusNotImplemented {
		t.Fatalf("unsupported runner: HTTP %d, want 501", code)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT model FROM runner_sessions WHERE id = $1`, rsID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "board-default" {
		t.Fatalf("stored model = %q after a failed re-model, want the spawn model", stored)
	}
	fake.setModelErr = nil

	// a session that has ended cannot be re-modelled
	if _, err := pool.Exec(ctx,
		`UPDATE runner_sessions SET lifecycle = 'stopped' WHERE id = $1`, rsID); err != nil {
		t.Fatal(err)
	}
	if code := post(rsID, map[string]string{"model": "claude-sonnet-5"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("stopped session: HTTP %d, want 422", code)
	}
	if len(fake.modelSet) != 0 {
		t.Fatalf("no call should have reached the runner: %+v", fake.modelSet)
	}
}

// The MCP surface is the one agents actually use: same verb, self-resolved.
func TestSessionRemodelOverMCP(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "mcp remodel", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	res, _ := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	rsID := spawnForRemodel(t, srv, cookie, res.Card.ID)

	_, sessionTok, err := db.MintToken(ctx, pool, &board.ID, "agent",
		"card #1 worker session [rs:"+rsID+"]",
		[]string{"card.read", "card.write", "column.write"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, srv, "POST", "/mcp", sessionTok, nil, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "blerg_session_set_model",
			"arguments": map[string]any{
				"model": "claude-opus-5", "reason": "needs the bigger model",
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mcp call: HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	_ = resp.Body.Close()
	result, _ := out["result"].(map[string]any)
	if result == nil || result["isError"] == true {
		t.Fatalf("mcp result = %v", out)
	}
	if len(fake.modelSet) != 1 || fake.modelSet[0].Model != "claude-opus-5" {
		t.Fatalf("driver calls = %+v", fake.modelSet)
	}
}

// A model change made on the runner's side (a human flipping the model in the
// runner's own UI) arrives as an event; ingest follows it so the model blerg-board
// reports stays the model that is running.
func TestIngestFollowsRunnerModelChange(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "ingest remodel", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	res, _ := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	rsID := spawnForRemodel(t, srv, cookie, res.Card.ID)

	// the driver folds unmapped kinds to "status" and keeps the original kind
	// and payload nested — that is the shape ingest has to read
	fake.events = []runner.Event{{
		Seq: 1, Ts: "2026-01-01T00:00:00Z", Kind: "status",
		Payload: json.RawMessage(`{"source_kind":"model_changed","payload":{"model":"claude-haiku-4-5"}}`),
	}}
	srvAPI.IngestOnce(ctx)

	var stored string
	if err := pool.QueryRow(ctx, `SELECT model FROM runner_sessions WHERE id = $1`, rsID).
		Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "claude-haiku-4-5" {
		t.Fatalf("stored model = %q, want the runner-reported one", stored)
	}
}

// A model_changed event that predates blerg-board's own re-model must not undo it.
// Ingest can be behind: the backlog it drains after a re-model still reports
// the model the session was spawned with, and applying that would revert the
// change and leave it reverted until the runner happened to report again.
func TestIngestDoesNotUndoNewerReModel(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	cookie := humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	srvAPI.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc"})

	board, _ := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "backlog", Repos: []string{"blerg-board"}, Model: strPtr("board-default"),
	})
	res, _ := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("card"), Repos: &[]string{"blerg-board"},
	}, db.EventMeta{Actor: "human"})
	rsID := spawnForRemodel(t, srv, cookie, res.Card.ID)

	resp := request(t, srv, "POST", "/api/runner-sessions/"+rsID+"/model", cookie, nil,
		map[string]string{"model": "claude-opus-5", "reason": "escalating"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-model: HTTP %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	storedModel := func() string {
		t.Helper()
		var m string
		if err := pool.QueryRow(ctx, `SELECT model FROM runner_sessions WHERE id = $1`, rsID).
			Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// a stale report from before the change: ignored
	fake.events = []runner.Event{{
		Seq: 1, Ts: "2020-01-01T00:00:00Z", Kind: "status",
		Payload: json.RawMessage(`{"source_kind":"model_changed","payload":{"model":"board-default"}}`),
	}}
	srvAPI.IngestOnce(ctx)
	if got := storedModel(); got != "claude-opus-5" {
		t.Fatalf("stale event reverted the model to %q", got)
	}

	// a report from after it: followed
	fake.events = append(fake.events, runner.Event{
		Seq: 2, Ts: "2030-01-01T00:00:00Z", Kind: "status",
		Payload: json.RawMessage(`{"source_kind":"model_changed","payload":{"model":"claude-haiku-4-5"}}`),
	})
	srvAPI.IngestOnce(ctx)
	if got := storedModel(); got != "claude-haiku-4-5" {
		t.Fatalf("newer event not followed: model = %q", got)
	}
}
