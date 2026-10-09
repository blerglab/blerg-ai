package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/board/prompts"
)

// Ephemeral board sessions: a chat spawned from the board header, not tied to
// any card. The human talks; the agent triages — search, create, update, and
// move cards through the same gated API as everyone else. Closed explicitly
// from the UI (interrupt + stopped + token revoked).

func (a *API) boardSessionRoutes(mux *http.ServeMux, authed func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	mux.HandleFunc("POST /api/boards/{id}/sessions", authed(a.handleSpawnBoardSession))
	mux.HandleFunc("GET /api/boards/{id}/sessions", authed(a.handleListBoardSessions))
	mux.HandleFunc("POST /api/runner-sessions/{id}/close", authed(a.handleCloseSession))
}

func (a *API) handleSpawnBoardSession(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if !a.checkStartIdentity(r.Context(), w, boardID) {
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	// an empty board gets a BOOTSTRAP session: it runs in a scratch repo,
	// creates the project's real repo, and wires the board itself — so a
	// brand-new project starts entirely from blerg-board
	bootstrap := len(board.Repos) == 0
	repo, gitURL, prompt := "", "", ""
	var perr error
	// Caps are requested ∩ caller's: a human-opened board chat is the human's
	// proxy and may administer the board (fix repos/git_base/deploy_url/model)
	// only if that human may. An agent-spawned one must not self-escalate.
	// The native service key holds everything.
	wanted := []string{"card.read", "card.write", "column.write", "board.admin"}
	caps := make([]string, 0, len(wanted))
	for _, c := range wanted {
		if p.IsNativeService() || (p.Token != nil && p.Token.HasCap(c)) {
			caps = append(caps, c)
		}
	}
	if bootstrap && !slices.Contains(caps, "board.admin") {
		writeError(w, http.StatusForbidden, "bootstrapping a board requires board.admin")
		return
	}
	if bootstrap {
		repo = os.Getenv("RUNNER_BOOTSTRAP_REPO")
		if repo == "" {
			repo = "blerg-board" // any clonable repo works as a scratch workspace
		}
		gitURL = ""
		prompt, perr = a.buildBootstrapPrompt(board, req.Prompt)
	} else {
		repo = board.Repos[0]
		gitURL = boardGitURL(board, repo)
		prompt, perr = a.buildBoardSessionPrompt(board, a.boardSnapshot(r.Context(), board), req.Prompt)
	}
	if perr != nil {
		writeError(w, http.StatusInternalServerError, "prompt render: "+perr.Error())
		return
	}

	tok, rawTok, err := db.MintToken(r.Context(), a.Pool, &boardID, "agent",
		"board session", caps, 12*time.Hour)
	if err != nil {
		writeDBError(w, err)
		return
	}
	title := fmt.Sprintf("blerg-board board: %s", board.Name)
	if bootstrap {
		title = fmt.Sprintf("blerg-board bootstrap: %s", board.Name)
	}
	model := boardRoleModel(board, "board")
	extID, err := a.startOnRunner(r.Context(), boardID, runner.StartRequest{
		Repo:   repo,
		Title:  title,
		Prompt: prompt,
		Model:  model,
		GitURL: gitURL,
		// The person who opened the drawer is in the chat.
		Interaction: interactionForRole("board"),
		Env: map[string]string{
			"BLERG_BOARD_URL":   a.runner.AgentURL,
			"BLERG_BOARD_TOKEN": rawTok,
			"BLERG_BOARD_BOARD": boardID,
		},
	})
	if err != nil {
		_ = db.RevokeToken(r.Context(), a.Pool, tok.ID)
		writeStartError(w, err)
		return
	}
	var rsID string
	err = a.Pool.QueryRow(r.Context(), `
		INSERT INTO runner_sessions (card_id, board_id, runner, external_session_id, lifecycle, role, model)
		VALUES (NULL,$1,$2,$3,'starting','board',$4) RETURNING id`,
		boardID, a.runner.Driver.Name(), extID, model).Scan(&rsID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	_, _ = a.Pool.Exec(r.Context(),
		`UPDATE tokens SET label = label || ' [rs:' || $2 || ']' WHERE id = $1`, tok.ID, rsID)
	writeJSON(w, http.StatusAccepted, map[string]string{
		"runner_session_id": rsID, "external_session_id": extID,
	})
}

func (a *API) handleListBoardSessions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	rows, err := a.Pool.Query(r.Context(), `
		SELECT id, runner, external_session_id, lifecycle, role, model,
		       to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM runner_sessions
		WHERE board_id = $1 AND card_id IS NULL AND role = 'board'
		  AND lifecycle NOT IN ('stopped','error')
		ORDER BY created_at DESC`, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, rn, ext, lc, role, model, created string
		if rows.Scan(&id, &rn, &ext, &lc, &role, &model, &created) == nil {
			out = append(out, map[string]any{
				"id": id, "runner": rn, "external_session_id": ext,
				"lifecycle": lc, "role": role, "resumable": false, "created_at": created,
				"model": model,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCloseSession ends a session for good: interrupt the runner side, mark
// it stopped locally, revoke its token.
func (a *API) handleCloseSession(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	id := r.PathValue("id")
	var boardID, ext string
	if err := a.Pool.QueryRow(r.Context(), `
		SELECT board_id, external_session_id FROM runner_sessions WHERE id = $1`, id).
		Scan(&boardID, &ext); err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if a.runner != nil {
		if err := a.runner.Driver.Stop(r.Context(), ext); err != nil {
			_ = a.runner.Driver.Interrupt(r.Context(), ext)
		}
	}
	tag, err := a.Pool.Exec(r.Context(),
		`UPDATE runner_sessions SET lifecycle = 'stopped'
		 WHERE id = $1 AND lifecycle NOT IN ('stopped','error')`, id)
	if err == nil && tag.RowsAffected() > 0 {
		// one slot back on the runner — let dispatch use it on the next tick
		// instead of sitting out an older refusal
		a.noteCapacityAvailable(r.Context())
	}
	_, _ = a.Pool.Exec(r.Context(), `
		UPDATE tokens SET revoked_at = now()
		WHERE revoked_at IS NULL AND label LIKE '%% [rs:' || $1 || ']'`, id)
	a.Hub.Broadcast(boardID, "card_changed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

// buildBoardSessionPrompt renders the board-chat brief. The board-state
// snapshot is rendered by the caller (a.boardSnapshot) and passed in, so this
// stays a pure function of its arguments — no pool, no queries.
func (a *API) buildBoardSessionPrompt(board db.Board, snapshot, extra string) (string, error) {
	d := a.promptData()
	d.Board, d.Extra = prompts.BoardInfo{Name: board.Name, Snapshot: snapshot}, extra
	return a.Prompts.Render("board", d)
}

func (a *API) buildBootstrapPrompt(board db.Board, extra string) (string, error) {
	d := a.promptData()
	d.Board, d.Extra = prompts.BoardInfo{Name: board.Name}, extra
	return a.Prompts.Render("bootstrap", d)
}
