package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/board/prompts"
)

// Runner wiring: spawn-from-card, conversation endpoints, and the server-side
// ingest poller. The browser never talks to the runner — blerg-board brokers, and
// the UI reads runner_events over blerg-board's own API.

// maxErrorReasonChars bounds the runner-supplied error reason that goes onto a
// card. The reason is free text the runner composes (the engine preflight's
// embeds a whole PATH) and a card comment is permanent history, so it is
// capped rather than pasted in whole. Long enough for every reason the runner
// actually produces; the untruncated text stays on the session row.
const maxErrorReasonChars = 800

func truncateReason(s string) string {
	r := []rune(s)
	if len(r) <= maxErrorReasonChars {
		return s
	}
	return string(r[:maxErrorReasonChars]) + "… (truncated)"
}

type RunnerConfig struct {
	Driver    runner.Driver
	PublicURL string // blerg-board's public base URL, for links shown to humans
	AgentURL  string // blerg-board URL as reachable from spawned sessions (in-cluster)
	UIBase    string // base URL of the runner's session UI, e.g. https://runner.example.com/sessions; empty omits the link

	// InfraDocsURL/InfraDocsNote render the optional infra-reference
	// paragraph appended to every session prompt: InfraDocsNote is a
	// one-line description of the deployment environment, InfraDocsURL is
	// where the session should curl to read more. Both must be set for the
	// paragraph to appear — either empty omits it entirely, so vanilla
	// (self-hosted) deployments get clean prompts with no environment
	// assumptions baked in.
	InfraDocsURL  string
	InfraDocsNote string

	// GitCredentialEnv names the environment variable, already present in
	// spawned sessions, that holds a git/GitHub credential — used by the
	// bootstrap prompt to create and push the initial repo. Empty omits the
	// credential instructions and has the session ask the human instead.
	GitCredentialEnv string
}

// promptData seeds a prompts.Data with the RunnerConfig-sourced fields
// (infra-reference paragraph, git-credential env) shared by every prompt
// role — safe to call before a runner is configured.
func (a *API) promptData() prompts.Data {
	if a.runner == nil {
		return prompts.Data{}
	}
	return prompts.Data{
		InfraDocsURL:     a.runner.InfraDocsURL,
		InfraDocsNote:    a.runner.InfraDocsNote,
		GitCredentialEnv: a.runner.GitCredentialEnv,
	}
}

// sessionURL builds a human-facing link to a runner session, or "" if no
// UIBase is configured — callers must skip attaching the link in that case.
func (a *API) sessionURL(extID string) string {
	if a.runner == nil {
		return ""
	}
	base, err := url.Parse(a.runner.UIBase)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	return strings.TrimRight(base.String(), "/") + "/" + url.PathEscape(extID)
}

func (a *API) SetRunner(cfg RunnerConfig) { a.runner = &cfg }

func (a *API) runnerRoutes(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/cards/{id}/spawn", authed(a.handleSpawnFromCard))
	mux.HandleFunc("GET /api/cards/{id}/run-targets", authed(a.handleCardRunTargets))
	mux.HandleFunc("GET /api/boards/{id}/run-targets", authed(a.handleBoardRunTargets))
	mux.HandleFunc("GET /api/cards/{id}/runner-sessions", authed(a.handleCardRunnerSessions))
	mux.HandleFunc("GET /api/boards/{id}/active-sessions", authed(a.handleBoardActiveSessions))
	mux.HandleFunc("GET /api/runner-sessions/{id}/events", authed(a.handleRunnerSessionEvents))
	mux.HandleFunc("POST /api/runner-sessions/{id}/message", authed(a.handleRunnerSessionMessage))
	mux.HandleFunc("POST /api/runner-sessions/{id}/interrupt", authed(a.handleRunnerSessionInterrupt))
	// {id} accepts the literal "self": a running session re-models ITSELF
	// without having to learn its own runner-session id.
	mux.HandleFunc("POST /api/runner-sessions/{id}/model", authed(a.handleSetSessionModel))
	mux.HandleFunc("GET /api/runner-sessions/{id}/launch-options", authed(a.handleSessionLaunchOptions))
	mux.HandleFunc("POST /api/cards/{id}/accept", authed(a.handleAcceptCard))
	mux.HandleFunc("POST /api/deployments", authed(a.handleRecordDeployment))
	mux.HandleFunc("GET /api/boards/{id}/deployments", authed(a.handleBoardDeployments))
}

// handleSpawnFromCard: mint a board-scoped token, start a session on the
// card's primary repo, record it, link the card. The prompt points the agent
// at /onboard and the claim conventions.
func (a *API) handleSpawnFromCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if len(card.Repos) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "card has no repo — a session needs a working directory")
		return
	}
	var req struct {
		Prompt string `json:"prompt"` // optional extra instruction
		Model  string `json:"model"`
		Mode   string `json:"mode"` // "run" (default): execute the card | "discuss": talk about it
		// Runtime and DaemonID: where this run goes, when the person chose
		// (runTarget). Empty: the board's default, as always.
		Runtime  string `json:"runtime"`
		DaemonID string `json:"daemon_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "" {
		req.Mode = "run"
	}

	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	target := runTarget{Runtime: req.Runtime, DaemonID: req.DaemonID}
	if status, msg := a.checkRunTarget(p, board, target); status != 0 {
		writeError(w, status, msg)
		return
	}
	if target.Runtime == "daemon" && !hostAcknowledged(board, target.DaemonID) {
		writeError(w, http.StatusUnprocessableEntity, msgHostRunNeedsBoardAck)
		return
	}
	// Before the claim below: a board with no usable automation identity
	// must not move the card for a session that cannot start.
	if !a.checkStartIdentity(r.Context(), w, card.BoardID) {
		return
	}

	role := "worker"
	var prompt string
	if req.Mode == "discuss" {
		role = "discuss"
		prompt, err = a.buildDiscussPrompt(board, card, req.Prompt)
	} else {
		prompt, err = a.buildCardPrompt(board, card, req.Prompt)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "prompt render: "+err.Error())
		return
	}
	if req.Mode != "discuss" {
		// running a card claims it: move to the work column if it isn't there
		if cols, err := db.ListColumns(r.Context(), a.Pool, card.BoardID); err == nil {
			for _, c := range cols {
				if isWorkColumn(c.Name) {
					if card.ColumnID == nil || *card.ColumnID != c.ID {
						if moved, err := db.MoveCard(r.Context(), a.Pool, card.ID, c.ID, nil, nil,
							db.EventMeta{Actor: "service"}); err == nil {
							card = moved
							// Tell open boards now: if the start below fails,
							// nothing else would, and the card would sit in its
							// old column on every other screen.
							a.Hub.Broadcast(card.BoardID, "card_changed")
						}
					}
					break
				}
			}
		}
	}
	rsID, extID, err := a.spawnSessionAt(r.Context(), board, card, role, prompt, req.Model, target)
	if err != nil {
		writeStartError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"runner_session_id": rsID, "external_session_id": extID,
	})
}

// boardRoleModel resolves the board-level model for a session role: the
// role's own override when set, else the board default. Roles without an
// override (worker, standing) always land on board.Model.
//
//	reviewer → reviewer_model   discuss → discuss_model   board → chat_model
//
// Discussion and board-header chat are thinking work, not execution, so they
// get their own knobs: a board can run brainstorming on a stronger model
// without raising the model every worker session burns.
func boardRoleModel(board db.Board, role string) string {
	switch role {
	case "reviewer":
		if board.ReviewerModel != "" {
			return board.ReviewerModel
		}
	case "discuss":
		if board.DiscussModel != "" {
			return board.DiscussModel
		}
	case "board":
		if board.ChatModel != "" {
			return board.ChatModel
		}
	}
	return board.Model
}

// boardRoleEffort follows the role's model override. An override with no
// effort uses that model's native default, rather than inheriting an unrelated
// model's effort. A role effort may also override the board's model effort.
func boardRoleEffort(board db.Board, role string) string {
	switch role {
	case "reviewer":
		if board.ReviewerEffort != "" || board.ReviewerModel != "" {
			return board.ReviewerEffort
		}
	case "discuss":
		if board.DiscussEffort != "" || board.DiscussModel != "" {
			return board.DiscussEffort
		}
	case "board":
		if board.ChatEffort != "" || board.ChatModel != "" {
			return board.ChatEffort
		}
	}
	return board.Effort
}

// The two interaction modes of the runner contract (`interaction` on
// POST /api/runner/start).
const (
	interactionInteractive = "interactive"
	interactionUnattended  = "unattended"
)

// interactionForRole is the ONE rule for which mode a session the board
// starts runs in. A person opened a discussion or the board session to talk,
// and is reading the chat: interactive. Everything else is automation's — a
// card's Run, a review, a standing agent, anything the dispatcher starts —
// and nobody is watching it: unattended. An unknown role is unattended too,
// since an agent that waits for an answer nobody will give never finishes.
//
//	discuss, board → interactive   worker, reviewer, standing, … → unattended
func interactionForRole(role string) string {
	switch role {
	case "discuss", "board":
		return interactionInteractive
	}
	return interactionUnattended
}

// detached returns a context that survives its parent's cancellation, with a
// deadline of its own. For the writes that MUST land once an irreversible
// side effect has happened elsewhere — a session started on the runner, a
// card claimed for one — because the alternative is state blerg-board cannot see
// and therefore cannot heal. Same shape as the advisory-lock release in
// withBoardDispatch, and used sparingly for the same reason: it opts out of
// shutdown, so it only ever wraps a handful of quick statements.
func detached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// spawnSession mints a board-scoped token, starts a runner session on the
// card's primary repo in the given role, records it, and links the card.
//
// It runs on the board's default target with no per-spawn model: the card's
// own model, else the board's role override, else board.Model.
func (a *API) spawnSession(ctx context.Context, board db.Board, card db.Card, role, prompt string) (rsID, extID string, err error) {
	return a.spawnSessionAt(ctx, board, card, role, prompt, "", runTarget{})
}

// spawnSessionAt is spawnSession on a chosen execution target (runTarget; zero: the board's
// default). Only a person's own Run of a card chooses one (checkRunTarget). Model precedence:
// the explicit `model` argument (a per-spawn override) > the card's own model > the board's
// role override > board.Model.
func (a *API) spawnSessionAt(ctx context.Context, board db.Board, card db.Card, role, prompt, model string, target runTarget) (rsID, extID string, err error) {
	if model == "" {
		model = card.Model
	}
	if model == "" {
		model = boardRoleModel(board, role)
	}
	tok, rawTok, err := db.MintToken(ctx, a.Pool, &card.BoardID, "agent",
		fmt.Sprintf("card #%d %s session", card.Number, role),
		[]string{"card.read", "card.write", "column.write"}, 24*time.Hour)
	if err != nil {
		return "", "", err
	}
	title := fmt.Sprintf("blerg-board #%d: %s", card.Number, card.Title)
	switch role {
	case "reviewer":
		title = fmt.Sprintf("blerg-board #%d review: %s", card.Number, card.Title)
	case "discuss":
		title = fmt.Sprintf("blerg-board #%d discussion: %s", card.Number, card.Title)
	}
	effort := boardRoleEffort(board, role)
	if card.Model != "" {
		effort = card.Effort
	} else if card.Effort != "" {
		effort = card.Effort
	}
	var configured runner.StartRequest
	extID, configured, err = a.startOnRunnerConfigured(ctx, card.BoardID, runner.StartRequest{
		Repo:   card.Repos[0],
		Title:  title,
		Prompt: prompt,
		Model:  model,
		Effort: effort,
		GitURL: boardGitURL(board, card.Repos[0]),
		// By role: a discussion has its person in the chat, a run or a
		// review does not.
		Interaction:   interactionForRole(role),
		Runtime:       target.Runtime,
		DaemonID:      target.DaemonID,
		TargetAccount: targetAccount(board, target),
		Env: map[string]string{
			"BLERG_BOARD_URL":   a.runner.AgentURL,
			"BLERG_BOARD_TOKEN": rawTok,
			"BLERG_BOARD_BOARD": card.BoardID,
			"BLERG_BOARD_CARD":  card.ID,
		},
	})
	if err != nil {
		// Whether the runner had a slot is a fact about the runner, so record
		// it centrally: the next dispatch pass reads it instead of finding out
		// the same way, and the caller keeps deciding what a refusal means for
		// its own card (transient for everyone — never a stuck flag).
		if isCapacityErr(err) {
			a.noteCapacityRefusal(ctx, err)
		}
		_ = db.RevokeToken(ctx, a.Pool, tok.ID)
		return "", "", err
	}
	// From here the session EXISTS on the runner, and everything below is
	// blerg-board catching up to that fact. None of it may be abandoned half-done:
	// a live session with no runner_sessions row is invisible to ingest, to
	// the reaper and to the board — nobody can find it, stop it or resume it,
	// and the card it belongs to looks untouched. The contexts that get here
	// die routinely: the dispatcher's is the server's (a deploy cancels it
	// mid-pass) and a handler's dies when the client hangs up. So detach.
	ctx, done := detached(ctx, 15*time.Second)
	defer done()

	a.noteCapacityAvailable(ctx)
	err = a.Pool.QueryRow(ctx, `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, model, engine, effort,
		                             target_runtime, target_daemon_id, target_account)
		VALUES ($1,$2,$3,$4,'starting',$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		card.ID, card.BoardID, a.runner.Driver.Name(), extID, role, configured.Model, configured.Engine, configured.Effort,
		target.Runtime, target.DaemonID, targetAccount(board, target)).Scan(&rsID)
	if err != nil {
		return "", "", err
	}
	// Remember which token belongs to this session so ingest can revoke it.
	_, _ = a.Pool.Exec(ctx,
		`UPDATE tokens SET label = label || ' [rs:' || $2 || ']' WHERE id = $1`, tok.ID, rsID)

	// Link the card to the human-facing session view (blerg-runner UI).
	label := fmt.Sprintf("runner session (%s)", a.runner.Driver.Name())
	switch role {
	case "reviewer":
		label = fmt.Sprintf("review session (%s)", a.runner.Driver.Name())
	case "discuss":
		label = fmt.Sprintf("discussion session (%s)", a.runner.Driver.Name())
	}
	if url := a.sessionURL(extID); url != "" {
		links := slices.Concat(card.Links, []db.Link{{Kind: "session", URL: url, Label: strPtrOf(label)}})
		if _, err := db.UpdateCard(ctx, a.Pool, card.ID, db.CardParams{Links: &links}, db.EventMeta{
			Actor: "service",
		}); err != nil {
			log.Printf("spawn: link card: %v", err)
		}
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	return rsID, extID, nil
}

func strPtrOf(s string) *string { return &s }

func cardInfo(card db.Card) prompts.CardInfo {
	body := ""
	if card.Body != nil {
		body = *card.Body
	}
	return prompts.CardInfo{Number: card.Number, Title: card.Title, Body: body}
}

func (a *API) buildCardPrompt(board db.Board, card db.Card, extra string) (string, error) {
	d := a.promptData()
	d.Card, d.Board, d.Extra = cardInfo(card), prompts.BoardInfo{Name: board.Name}, extra
	return a.Prompts.Render("worker", d)
}

func (a *API) buildDiscussPrompt(board db.Board, card db.Card, extra string) (string, error) {
	d := a.promptData()
	d.Card, d.Board, d.Extra = cardInfo(card), prompts.BoardInfo{Name: board.Name}, extra
	return a.Prompts.Render("discuss", d)
}

func (a *API) handleCardRunnerSessions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.Pool.Query(r.Context(), `
		SELECT id, runner, external_session_id, lifecycle, runtime, resumable, role, model, engine, effort, resume_mode, resume_reason, steering_supported,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM runner_sessions WHERE card_id = $1 ORDER BY created_at`, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, rn, ext, lc, rt, role, model, engine, effort, resumeMode, resumeReason, created string
		var res bool
		var steering *bool
		if err := rows.Scan(&id, &rn, &ext, &lc, &rt, &res, &role, &model, &engine, &effort, &resumeMode, &resumeReason, &steering, &created); err != nil {
			writeDBError(w, err)
			return
		}
		entry := map[string]any{
			"id": id, "runner": rn, "external_session_id": ext, "role": role,
			"lifecycle": lc, "runtime": rt, "resumable": res, "created_at": created,
			"model": model, "engine": engine, "effort": effort, "resume_mode": resumeMode, "resume_reason": resumeReason, "board_id": card.BoardID, "session_url": a.sessionURL(ext),
		}
		if steering != nil {
			entry["steering_supported"] = *steering
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionBoardID resolves a runner-session id to the board it belongs to.
// board_id is NOT NULL on runner_sessions, so any row found always carries
// one — a session that cannot be resolved (unknown id) fails closed as
// db.ErrNotFound, which writeDBError maps to 404 for every principal,
// native service key included: there's no row to bypass a check on.
func (a *API) sessionBoardID(ctx context.Context, sessionID string) (string, error) {
	var boardID string
	err := a.Pool.QueryRow(ctx,
		`SELECT board_id FROM runner_sessions WHERE id = $1`, sessionID).Scan(&boardID)
	if err != nil {
		return "", db.ErrNotFound
	}
	return boardID, nil
}

func (a *API) handleRunnerSessionEvents(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID, err := a.sessionBoardID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	rows, err := a.Pool.Query(r.Context(), `
		SELECT seq, to_char(ts, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), kind, payload
		FROM runner_events WHERE runner_session_id = $1 AND seq > $2
		ORDER BY seq LIMIT 500`, r.PathValue("id"), after)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var ts, kind string
		var payload json.RawMessage
		if err := rows.Scan(&seq, &ts, &kind, &payload); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, map[string]any{"seq": seq, "ts": ts, "kind": kind, "payload": payload})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleRunnerSessionMessage(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	boardID, err := a.sessionBoardID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	var req struct {
		Text     string `json:"text"`
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		writeError(w, http.StatusBadRequest, "text required")
		return
	}
	if len(req.ClientID) > 256 {
		writeError(w, http.StatusUnprocessableEntity, "client_id must be at most 256 bytes")
		return
	}
	ext, err := a.runnerExternalID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	source := "blerg-board"
	if p.IsHuman() {
		source = "human"
	}
	if err := a.messageOnRunner(r.Context(), boardID, ext, req.Text, source, req.ClientID); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *API) handleRunnerSessionInterrupt(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	boardID, err := a.sessionBoardID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	ext, err := a.runnerExternalID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := a.runner.Driver.Interrupt(r.Context(), ext); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *API) runnerExternalID(ctx context.Context, id string) (string, error) {
	var ext string
	err := a.Pool.QueryRow(ctx,
		`SELECT external_session_id FROM runner_sessions WHERE id = $1`, id).Scan(&ext)
	if err != nil {
		return "", db.ErrNotFound
	}
	return ext, nil
}

// ── ingest ───────────────────────────────────────────────────────────────────

// StartRunnerIngest polls active runner sessions: pulls settled events into
// runner_events, mirrors lifecycle, and on a terminal lifecycle revokes the
// session's token (label-tagged at spawn) and stops watching. Poll cadence is
// per-session: 1s while running, 10s otherwise.
func (a *API) StartRunnerIngest(ctx context.Context) {
	if a.runner == nil {
		return
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		slow := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				slow++
				a.ingestTick(ctx, slow%10 == 0)
			}
		}
	}()
}

func (a *API) ingestTick(ctx context.Context, includeIdle bool) {
	rows, err := a.Pool.Query(ctx, `
		SELECT id, board_id, card_id, external_session_id, lifecycle, last_seq, runtime, resumable, resume_mode, resume_reason, engine, model, effort, steering_supported
		FROM runner_sessions
		WHERE lifecycle NOT IN ('stopped','error')`)
	if err != nil {
		return
	}
	type sess struct {
		id, boardID, ext, lifecycle       string
		cardID                            *string
		lastSeq                           int64
		runtime, resumeMode, resumeReason string
		engine, model, effort             string
		resumable                         bool
		steering                          *bool
	}
	var sessions []sess
	for rows.Next() {
		var s sess
		if err := rows.Scan(&s.id, &s.boardID, &s.cardID, &s.ext, &s.lifecycle, &s.lastSeq, &s.runtime, &s.resumable, &s.resumeMode, &s.resumeReason, &s.engine, &s.model, &s.effort, &s.steering); err == nil {
			sessions = append(sessions, s)
		}
	}
	rows.Close()

	for _, s := range sessions {
		active := s.lifecycle == "running" || s.lifecycle == "starting" || s.lifecycle == "waiting"
		if !active && !includeIdle {
			continue
		}
		changed := false
		for {
			events, hasMore, err := a.runner.Driver.Events(ctx, s.ext, s.lastSeq, 200)
			if err != nil {
				break
			}
			for _, ev := range events {
				_, err := a.Pool.Exec(ctx, `
					INSERT INTO runner_events (runner_session_id, seq, ts, kind, payload)
					VALUES ($1,$2,$3,$4,$5)
					ON CONFLICT (runner_session_id, seq) DO NOTHING`,
					s.id, ev.Seq, ev.Ts, ev.Kind, ev.Payload)
				if err == nil {
					s.lastSeq = ev.Seq
					changed = true
				}
				// A model change blerg-board did not make — someone re-modelled the
				// session in the runner's own UI. Follow it, so the model we
				// report is the model that is running. Events blerg-board has not
				// drained yet can be OLDER than a change blerg-board made through
				// the API, so a report only wins if it postdates that change
				// (both clocks are in-cluster; the window this has to be right
				// about is one re-model wide).
				if m := modelFromEvent(ev); m != "" {
					_, _ = a.Pool.Exec(ctx, `
						UPDATE runner_sessions SET model = $2
						WHERE id = $1 AND (model_set_at IS NULL OR model_set_at <= $3::timestamptz)`,
						s.id, m, ev.Ts)
				}
			}
			if !hasMore {
				break
			}
		}
		st, err := a.statusOnRunner(ctx, s.boardID, s.ext)
		switch {
		case err == nil && (st.Lifecycle != s.lifecycle || changed || st.Runtime != s.runtime || st.Resumable != s.resumable || st.ResumeMode != s.resumeMode || st.ResumeReason != s.resumeReason || (st.Engine != "" && st.Engine != s.engine) || (st.Model != "" && (st.Model != s.model || st.Effort != s.effort)) || (st.SteeringSupported != nil && !sameOptionalBool(st.SteeringSupported, s.steering))):
			_, _ = a.Pool.Exec(ctx, `
				UPDATE runner_sessions
				SET lifecycle = $2, runtime = $3, resumable = $4, last_seq = $5,
 resume_mode=$6, resume_reason=$7, engine=CASE WHEN $8<>'' THEN $8 ELSE engine END,
 model=CASE WHEN $9<>'' THEN $9 ELSE model END,
 effort=CASE WHEN $9<>'' THEN $10 ELSE effort END, steering_supported=coalesce($11,steering_supported),
				    last_activity_at = now()
				WHERE id = $1`, s.id, st.Lifecycle, st.Runtime, st.Resumable, s.lastSeq, st.ResumeMode, st.ResumeReason, st.Engine, st.Model, st.Effort, st.SteeringSupported)
			changed = true
			// A session that just died takes its reason with it: the runner's
			// error broadcast is one-shot, and the card would otherwise show a
			// bare "error". Write the runner's own reason onto the card, once,
			// on the transition — it is the only place a human will look.
			if st.Lifecycle == "error" && s.lifecycle != "error" && s.cardID != nil {
				note := "Session ended in error."
				if st.ErrorReason != "" {
					note = "Session ended in error: " + truncateReason(st.ErrorReason)
				}
				_ = db.AppendComment(ctx, a.Pool, *s.cardID, note, db.EventMeta{Actor: "service"})
			}
			if runner.TerminalLifecycle(st.Lifecycle) {
				a.revokeSessionToken(ctx, s.id)
			}
		case errors.Is(err, errRecoveryAuthorityUnavailable):
			// A privacy404 under the service key does not prove that the owner's
			// session is gone. Preserve its lifecycle, but stop advertising recovery.
			if s.resumable || s.resumeReason != st.ResumeReason || changed {
				_, _ = a.Pool.Exec(ctx, `UPDATE runner_sessions SET resumable=false,resume_mode='',resume_reason=$2,last_seq=$3 WHERE id=$1`, s.id, st.ResumeReason, s.lastSeq)
				changed = true
			}
		case errors.Is(err, runner.ErrSessionGone):
			// The runner has no record of this session — its Job finished and
			// was reaped, or it never existed. Settle it locally so ingest
			// stops polling a corpse and every session-derived signal (auto-
			// review's active-reviewer guard, findings relay, etc.) sees it
			// as done.
			_, _ = a.Pool.Exec(ctx, `
				UPDATE runner_sessions
				SET lifecycle = 'stopped', resumable=false, resume_mode='', resume_reason='The runner no longer has this session', last_seq = $2, last_activity_at = now()
				WHERE id = $1`, s.id, s.lastSeq)
			a.revokeSessionToken(ctx, s.id)
			changed = true
		case changed:
			_, _ = a.Pool.Exec(ctx,
				`UPDATE runner_sessions SET last_seq = $2, last_activity_at = now() WHERE id = $1`,
				s.id, s.lastSeq)
		}
		if changed {
			a.Hub.Broadcast(s.boardID, "runner_changed")
		}
	}
}

// modelFromEvent reads a runner-reported model change out of an ingested
// event. `model_changed` is not in blerg-board's closed event vocabulary, so the
// driver folds it to "status" and keeps the original kind and payload nested
// (see runner.kindMap) — hence the two shapes. Any other event yields "".
func modelFromEvent(ev runner.Event) string {
	var wrapped struct {
		SourceKind string `json:"source_kind"`
		Payload    struct {
			Model string `json:"model"`
		} `json:"payload"`
		Model string `json:"model"`
	}
	if json.Unmarshal(ev.Payload, &wrapped) != nil {
		return ""
	}
	if wrapped.SourceKind != "model_changed" {
		return ""
	}
	if wrapped.Payload.Model != "" {
		return wrapped.Payload.Model
	}
	return wrapped.Model
}

// revokeSessionToken finds the spawn-minted token by its label tag.
func (a *API) revokeSessionToken(ctx context.Context, runnerSessionID string) {
	_, err := a.Pool.Exec(ctx, `
		UPDATE tokens SET revoked_at = now()
		WHERE revoked_at IS NULL AND label LIKE '%[rs:' || $1 || ']'`, runnerSessionID)
	if err != nil {
		log.Printf("revoke session token: %v", err)
	}
}

// IngestOnce runs one full ingest pass (tests and manual poking).
func (a *API) IngestOnce(ctx context.Context) { a.ingestTick(ctx, true) }

// handleBoardActiveSessions: card_id → lifecycle for non-terminal runner
// sessions on a board — drives the board's "churning" spinners.
func (a *API) handleBoardActiveSessions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireBoard(r.PathValue("id"), "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.Pool.Query(r.Context(), `
		SELECT card_id, lifecycle,
		       to_char(COALESCE(last_activity_at, created_at), 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM runner_sessions
		WHERE board_id = $1 AND card_id IS NOT NULL
		  AND lifecycle NOT IN ('stopped','error')`, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	type sessInfo struct {
		Lifecycle string `json:"lifecycle"`
		Since     string `json:"since"`
	}
	out := map[string]sessInfo{}
	for rows.Next() {
		var cardID, lifecycle, since string
		if err := rows.Scan(&cardID, &lifecycle, &since); err != nil {
			writeDBError(w, err)
			return
		}
		out[cardID] = sessInfo{Lifecycle: lifecycle, Since: since}
	}
	writeJSON(w, http.StatusOK, out)
}

// boardGitURL resolves a repo short name against the board's git base.
// Empty base falls back to the runner's default (RUNNER_GIT_BASE).
func boardGitURL(board db.Board, repo string) string {
	if board.GitBase == "" {
		return ""
	}
	return runner.JoinRepoURL(strings.TrimRight(board.GitBase, "/"), repo)
}

func sameOptionalBool(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
