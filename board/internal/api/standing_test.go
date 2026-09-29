package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/api"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestStandingWorkerPerCardSpawnsSession(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	_ = humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %v", err)
	}
	triggerCol, otherCol := cols[0], cols[1]

	agent, err := db.CreateStandingAgent(ctx, pool, board.ID, triggerCol.ID, "triager", "fake", "triage this card", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}

	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("needs triage"), Repos: &[]string{"blerg-board"}, ColumnID: &otherCol.ID,
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatalf("CreateCard: %v", err)
	}
	if _, err := db.MoveCard(ctx, pool, res.Card.ID, triggerCol.ID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatalf("MoveCard: %v", err)
	}

	apiSrv.DrainStandingQueueOnce(ctx)

	if len(fake.started) != 1 {
		t.Fatalf("expected 1 spawned session, got %d", len(fake.started))
	}
	st := fake.started[0]
	if st.Repo != "blerg-board" || st.Env["BLERG_BOARD_TOKEN"] == "" || st.Env["BLERG_BOARD_CARD"] != res.Card.ID || st.Env["BLERG_BOARD_BOARD"] != board.ID {
		t.Fatalf("start request wrong: %+v", st)
	}

	// The minted token shares the agent's lineage (the re-entrancy guard
	// matches on lineage, not on a specific token row).
	tok, err := db.ValidateToken(ctx, pool, st.Env["BLERG_BOARD_TOKEN"])
	if err != nil {
		t.Fatalf("minted token invalid: %v", err)
	}
	if tok.LineageID != agent.TokenLineageID {
		t.Fatalf("session token lineage = %s, want agent lineage %s", tok.LineageID, agent.TokenLineageID)
	}

	var role string
	var standingAgentID *string
	var cardID *string
	if err := pool.QueryRow(ctx,
		`SELECT role, standing_agent_id, card_id FROM runner_sessions WHERE external_session_id = 'ext-123'`).
		Scan(&role, &standingAgentID, &cardID); err != nil {
		t.Fatalf("runner_sessions row: %v", err)
	}
	if role != "standing" || standingAgentID == nil || *standingAgentID != agent.ID {
		t.Fatalf("runner_sessions row wrong: role=%s standing_agent_id=%v", role, standingAgentID)
	}
	if cardID == nil || *cardID != res.Card.ID {
		t.Fatalf("per_card session must carry the triggering card_id, got %v", cardID)
	}

	var state string
	if err := pool.QueryRow(ctx,
		`SELECT state FROM standing_agent_queue WHERE standing_agent_id = $1 AND card_id = $2`,
		agent.ID, res.Card.ID).Scan(&state); err != nil {
		t.Fatalf("queue row: %v", err)
	}
	if state != "done" {
		t.Fatalf("queue state = %q, want done", state)
	}

	// The card gained a link to the session (like a worker/reviewer spawn).
	card, _ := db.GetCard(ctx, pool, res.Card.ID)
	if len(card.Links) != 1 || card.Links[0].Kind != "session" ||
		card.Links[0].URL != "https://runner.test/sessions/ext-123" {
		t.Fatalf("card links: %+v", card.Links)
	}
}

func TestStandingWorkerPersistentReusesLiveSession(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	_ = humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running"}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil || len(cols) < 2 {
		t.Fatalf("columns: %v", err)
	}
	triggerCol, otherCol := cols[0], cols[1]

	agent, err := db.CreateStandingAgent(ctx, pool, board.ID, triggerCol.ID, "shepherd", "fake", "shepherd cards", "persistent")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}

	res1, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("c1"), Repos: &[]string{"blerg-board"}, ColumnID: &otherCol.ID,
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, res1.Card.ID, triggerCol.ID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	apiSrv.DrainStandingQueueOnce(ctx)
	if len(fake.started) != 1 {
		t.Fatalf("expected the first card to start a session, got %d starts", len(fake.started))
	}

	res2, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("c2"), Repos: &[]string{"blerg-board"}, ColumnID: &otherCol.ID,
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveCard(ctx, pool, res2.Card.ID, triggerCol.ID, nil, nil, db.EventMeta{Actor: "human"}); err != nil {
		t.Fatal(err)
	}
	apiSrv.DrainStandingQueueOnce(ctx)

	if len(fake.started) != 1 {
		t.Fatalf("expected no second Start (persistent session reused), got %d starts", len(fake.started))
	}
	if len(fake.messaged) != 1 || fake.messaged[0].SessionID != "ext-123" {
		t.Fatalf("expected the second card delivered as a message to the live session, got %+v", fake.messaged)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runner_sessions WHERE standing_agent_id = $1`, agent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 persistent runner_sessions row, got %d", count)
	}
	var cardID *string
	if err := pool.QueryRow(ctx,
		`SELECT card_id FROM runner_sessions WHERE standing_agent_id = $1`, agent.ID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	if cardID != nil {
		t.Fatalf("persistent session's card_id must stay NULL — it isn't tied to one card, got %v", *cardID)
	}
}

func TestStandingWorkerBacksOffOnStartFailure(t *testing.T) {
	srv, pool := testServer(t)
	ctx := context.Background()
	_ = humanToken(t, srv)

	fake := &fakeRunner{lifecycle: "running", startErr: errors.New("boom")}
	apiSrv := srvAPI
	apiSrv.SetRunner(api.RunnerConfig{Driver: fake, PublicURL: "https://blerg-board.test", AgentURL: "http://blerg-board.svc", UIBase: "https://runner.test/sessions"})

	board, err := db.CreateBoard(ctx, pool, db.BoardParams{Name: "work", Repos: []string{"blerg-board"}})
	if err != nil {
		t.Fatal(err)
	}
	cols, err := db.ListColumns(ctx, pool, board.ID)
	if err != nil || len(cols) < 1 {
		t.Fatalf("columns: %v", err)
	}
	col := cols[0]
	agent, err := db.CreateStandingAgent(ctx, pool, board.ID, col.ID, "flaky", "fake", "go", "per_card")
	if err != nil {
		t.Fatalf("CreateStandingAgent: %v", err)
	}
	res, err := db.CreateCard(ctx, pool, board.ID, db.CardParams{
		Title: strPtr("c"), Repos: &[]string{"blerg-board"}, ColumnID: &col.ID,
	}, db.EventMeta{Actor: "human"})
	if err != nil {
		t.Fatal(err)
	}

	apiSrv.DrainStandingQueueOnce(ctx)

	var state, lastErr string
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT state, attempts, last_error FROM standing_agent_queue WHERE standing_agent_id = $1 AND card_id = $2`,
		agent.ID, res.Card.ID).Scan(&state, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 1 || lastErr == "" {
		t.Fatalf("expected a backed-off pending retry, got state=%s attempts=%d last_error=%q", state, attempts, lastErr)
	}
	if len(fake.started) != 0 {
		t.Fatalf("no session should be recorded on a failed start, got %d", len(fake.started))
	}
}
