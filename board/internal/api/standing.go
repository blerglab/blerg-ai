package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/jackc/pgx/v5"
)

// Standing column agents: bound to a column, triggered when a card enters it
// (see internal/db/standing.go for the enqueue side and the re-entrancy
// guard). This file is the admin CRUD surface and the drain worker.

func (a *API) standingAgentRoutes(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	mux.HandleFunc("GET /api/boards/{id}/standing-agents", authed(a.handleListStandingAgents))
	mux.HandleFunc("POST /api/boards/{id}/standing-agents", authed(a.handleCreateStandingAgent))
	mux.HandleFunc("PATCH /api/standing-agents/{id}", authed(a.handleUpdateStandingAgent))
	mux.HandleFunc("DELETE /api/standing-agents/{id}", authed(a.handleDeleteStandingAgent))
}

func (a *API) handleListStandingAgents(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	agents, err := db.ListStandingAgents(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

// handleCreateStandingAgent is board.admin-gated: a standing agent spawns
// sessions autonomously on every card entering its column, which is a
// structural, trust-bearing decision about the board — the same tier as
// field_schema and gate policy.
func (a *API) handleCreateStandingAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "board.admin"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		ColumnID    string `json:"column_id"`
		Name        string `json:"name"`
		Prompt      string `json:"prompt"`
		SessionMode string `json:"session_mode"`
		Runner      string `json:"runner"`
	}](r)
	if err != nil || req.ColumnID == "" || req.Name == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "column_id, name, and prompt are required")
		return
	}
	if req.SessionMode != "" && req.SessionMode != "per_card" && req.SessionMode != "persistent" {
		writeError(w, http.StatusBadRequest, "invalid session_mode (per_card|persistent)")
		return
	}
	var onBoard bool
	if err := a.Pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM board_columns WHERE id = $1 AND board_id = $2)`,
		req.ColumnID, boardID).Scan(&onBoard); err != nil {
		writeDBError(w, err)
		return
	}
	if !onBoard {
		writeError(w, http.StatusBadRequest, "column not on this board")
		return
	}
	runnerName := req.Runner
	if runnerName == "" && a.runner != nil {
		runnerName = a.runner.Driver.Name()
	}
	agent, err := db.CreateStandingAgent(r.Context(), a.Pool, boardID, req.ColumnID, req.Name, runnerName, req.Prompt, req.SessionMode)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(boardID, "board_changed")
	writeJSON(w, http.StatusCreated, agent)
}

func (a *API) handleUpdateStandingAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	agent, err := db.GetStandingAgent(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(agent.BoardID, "board.admin"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	params, err := decode[db.StandingAgentParams](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	updated, err := db.UpdateStandingAgent(r.Context(), a.Pool, agent.ID, params)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(agent.BoardID, "board_changed")
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) handleDeleteStandingAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	agent, err := db.GetStandingAgent(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(agent.BoardID, "board.admin"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := db.DeleteStandingAgent(r.Context(), a.Pool, agent.ID); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(agent.BoardID, "board_changed")
	w.WriteHeader(http.StatusNoContent)
}

// ── worker ───────────────────────────────────────────────────────────────────

const standingDrainInterval = 5 * time.Second

// standingBatchPerTick bounds how many queue items one tick drains, so a
// backlog cannot make a single tick run unboundedly (mirrors the board-run
// dispatcher's own guard).
const standingBatchPerTick = 10

// StartStandingAgents launches the queue drain worker. A no-op when no
// runner is configured — there is nothing to spawn sessions with.
func (a *API) StartStandingAgents(ctx context.Context) {
	go func() {
		t := time.NewTicker(standingDrainInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.standingTick(ctx)
			}
		}
	}()
}

func (a *API) standingTick(ctx context.Context) {
	if a.runner == nil {
		return
	}
	// Restart durability: a crash mid-item leaves it 'running' forever
	// unless something reclaims it once its lease expires.
	if err := db.ReapStaleStandingQueue(ctx, a.Pool); err != nil {
		log.Printf("standing agents: reap stale queue: %v", err)
	}
	for i := 0; i < standingBatchPerTick; i++ {
		item, ok, err := db.ClaimStandingQueueItem(ctx, a.Pool)
		if err != nil {
			log.Printf("standing agents: claim: %v", err)
			return
		}
		if !ok {
			return
		}
		a.processStandingQueueItem(ctx, item)
	}
}

// DrainStandingQueueOnce runs one full drain pass (tests and manual poking).
func (a *API) DrainStandingQueueOnce(ctx context.Context) { a.standingTick(ctx) }

func (a *API) processStandingQueueItem(ctx context.Context, item db.QueueItem) {
	agent, err := db.GetStandingAgent(ctx, a.Pool, item.StandingAgentID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			_ = db.CompleteStandingQueueItem(ctx, a.Pool, item.ID) // agent deleted since enqueue
			return
		}
		a.failStanding(ctx, item, "", err)
		return
	}
	if !agent.Enabled {
		_ = db.CompleteStandingQueueItem(ctx, a.Pool, item.ID)
		return
	}
	card, err := db.GetCard(ctx, a.Pool, item.CardID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			_ = db.CompleteStandingQueueItem(ctx, a.Pool, item.ID) // card gone since enqueue
			return
		}
		a.failStanding(ctx, item, agent.Name, err)
		return
	}
	board, err := db.GetBoard(ctx, a.Pool, agent.BoardID)
	if err != nil {
		a.failStanding(ctx, item, agent.Name, err)
		return
	}
	if err := a.runStandingAgent(ctx, agent, board, card); err != nil {
		a.failStanding(ctx, item, agent.Name, err)
		return
	}
	if err := db.CompleteStandingQueueItem(ctx, a.Pool, item.ID); err != nil {
		log.Printf("standing agents: complete queue item %d: %v", item.ID, err)
	}
}

func (a *API) failStanding(ctx context.Context, item db.QueueItem, agentName string, cause error) {
	permanent, err := db.FailStandingQueueItem(ctx, a.Pool, item.ID, item.Attempts, cause.Error())
	if err != nil {
		log.Printf("standing agents: record failure for queue item %d: %v", item.ID, err)
	}
	log.Printf("standing agents: queue item %d (agent %s, card %s) failed: %v",
		item.ID, item.StandingAgentID, item.CardID, cause)
	if !permanent {
		return
	}
	label := "Standing agent"
	if agentName != "" {
		label = fmt.Sprintf("Standing agent %q", agentName)
	}
	_ = db.AppendComment(ctx, a.Pool, item.CardID,
		fmt.Sprintf("%s failed to run on this card and gave up retrying after %d attempts: %v",
			label, item.Attempts+1, cause),
		db.EventMeta{Actor: "service"})
}

// runStandingAgent dispatches by session_mode: persistent reuses a live
// session (sending the card as a new turn) or falls through to starting one
// if none is live; per_card always starts a fresh session scoped to the
// card, resolving its working tree from the card's primary repo.
func (a *API) runStandingAgent(ctx context.Context, agent db.StandingAgent, board db.Board, card db.Card) error {
	if agent.SessionMode == "persistent" {
		ext, ok, err := a.livePersistentSessionExtID(ctx, agent.ID)
		if err != nil {
			return err
		}
		if ok {
			if err := a.runner.Driver.Message(ctx, ext, standingAgentCardMessage(card), "blerg-board"); err != nil {
				return fmt.Errorf("message persistent session: %w", err)
			}
			a.linkCardToStandingSession(ctx, card, ext, agent.Name)
			a.Hub.Broadcast(board.ID, "runner_changed")
			return nil
		}
	}
	if len(card.Repos) == 0 {
		return fmt.Errorf("card #%d has no repo — a standing agent session needs a working directory", card.Number)
	}
	var cardID *string
	if agent.SessionMode != "persistent" {
		id := card.ID
		cardID = &id
	}
	return a.spawnStandingSession(ctx, agent, board, card, cardID, buildStandingAgentPrompt(agent, board, card))
}

func (a *API) livePersistentSessionExtID(ctx context.Context, agentID string) (string, bool, error) {
	var ext string
	err := a.Pool.QueryRow(ctx, `
		SELECT external_session_id FROM runner_sessions
		WHERE standing_agent_id = $1 AND card_id IS NULL
		  AND lifecycle NOT IN ('stopped','error')
		ORDER BY created_at DESC LIMIT 1`, agentID).Scan(&ext)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ext, true, nil
}

// spawnStandingSession mints a token under the agent's own lineage (not a
// fresh one — the re-entrancy guard matches on lineage, so a session that
// isn't traceable back to it would trigger the agent's own column forever),
// starts the runner session, and records it. cardID is nil for a persistent
// session (it isn't tied to one card) and set for per_card.
func (a *API) spawnStandingSession(ctx context.Context, agent db.StandingAgent, board db.Board, triggerCard db.Card, cardID *string, prompt string) error {
	tok, rawTok, err := db.MintTokenForLineage(ctx, a.Pool, &board.ID, agent.TokenLineageID, "agent",
		fmt.Sprintf("standing agent %q session", agent.Name),
		[]string{"card.read", "card.write", "column.write"}, 24*time.Hour)
	if err != nil {
		return err
	}
	title := fmt.Sprintf("blerg-board standing agent %q: #%d %s", agent.Name, triggerCard.Number, triggerCard.Title)
	// standing agents have no role override of their own: the board default.
	// Routed through boardRoleModel so every spawn path reads its model from
	// one place.
	model := boardRoleModel(board, "standing")
	extID, err := a.startOnRunner(ctx, board.ID, runner.StartRequest{
		Repo:   triggerCard.Repos[0],
		Title:  title,
		Prompt: prompt,
		Model:  model,
		GitURL: boardGitURL(board, triggerCard.Repos[0]),
		Env: map[string]string{
			"BLERG_BOARD_URL":   a.runner.AgentURL,
			"BLERG_BOARD_TOKEN": rawTok,
			"BLERG_BOARD_BOARD": board.ID,
			"BLERG_BOARD_CARD":  triggerCard.ID,
		},
	})
	if err != nil {
		_ = db.RevokeToken(ctx, a.Pool, tok.ID)
		return fmt.Errorf("start session: %w", err)
	}
	var rsID string
	err = a.Pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, standing_agent_id, model)
		VALUES ($1,$2,$3,$4,'starting','standing',$5,$6) RETURNING id`,
		cardID, board.ID, a.runner.Driver.Name(), extID, agent.ID, model).Scan(&rsID)
	if err != nil {
		return err
	}
	// Remember which token belongs to this session so ingest can revoke it.
	_, _ = a.Pool.Exec(ctx,
		`UPDATE tokens SET label = label || ' [rs:' || $2 || ']' WHERE id = $1`, tok.ID, rsID)
	a.linkCardToStandingSession(ctx, triggerCard, extID, agent.Name)
	a.Hub.Broadcast(board.ID, "card_changed")
	return nil
}

func (a *API) linkCardToStandingSession(ctx context.Context, card db.Card, extID, agentName string) {
	url := a.sessionURL(extID)
	if url == "" {
		return
	}
	label := fmt.Sprintf("standing agent %q session (%s)", agentName, a.runner.Driver.Name())
	links := slices.Concat(card.Links, []db.Link{{Kind: "session", URL: url, Label: strPtrOf(label)}})
	if _, err := db.UpdateCard(ctx, a.Pool, card.ID, db.CardParams{Links: &links}, db.EventMeta{
		Actor: "service",
	}); err != nil {
		log.Printf("standing agent: link card: %v", err)
	}
}

func buildStandingAgentPrompt(agent db.StandingAgent, board db.Board, card db.Card) string {
	body := ""
	if card.Body != nil {
		body = *card.Body
	}
	return fmt.Sprintf(`You are the standing agent %q on the blerg-board board %q — triggered because card #%d entered your column.

Card title: %s

Card body:
%s

Your standing instructions:
%s

Your environment has BLERG_BOARD_URL, BLERG_BOARD_TOKEN, BLERG_BOARD_BOARD, and BLERG_BOARD_CARD set;
use the REST API via $BLERG_BOARD_URL. Read the onboarding doc first:
curl -s "$BLERG_BOARD_URL/onboard"   (WebFetch can't reach it; blerg-board is an
in-cluster service).`, agent.Name, board.Name, card.Number, card.Title, body, agent.Prompt)
}

func standingAgentCardMessage(card db.Card) string {
	body := ""
	if card.Body != nil {
		body = *card.Body
	}
	return fmt.Sprintf("Card #%d entered your column: %s\n\n%s", card.Number, card.Title, body)
}
