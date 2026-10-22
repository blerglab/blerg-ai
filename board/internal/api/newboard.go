package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/boardfiling"
)

// A session can propose a board (blerg-runner board create); the person approves it in Runner,
// which then creates it here with the person's own authority. That authority arrives as one
// Core-signed capability (boardfiling.BoardCapability): issued only to a live administrator, for
// one intent, bound to the digest of the exact approved draft, valid for 30 seconds, with no board
// scope and no other capability. Neither native Board tokens nor broader Core tokens reach this
// path, and its intent is consumed with the board it creates.

// approvedBoardReceipt is what the runner records: the new board's id, name and repositories.
type approvedBoardReceipt struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Repos []string `json:"repos"`
}

func (a *API) handleCreateApprovedBoard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	const refusal = "A signed one-board approval is required."
	if !p.FromCore || p.Token == nil || p.Token.Kind != "agent" || p.Token.BoardID != nil || !uuidRe.MatchString(p.CoreAccountID) || p.CoreAccountID != p.Token.LineageID {
		writeError(w, http.StatusForbidden, refusal)
		return
	}
	intent, digest, ok := boardfiling.ParseBoardCapability(p.Token.Capabilities)
	if !ok || p.Token.ID != "core:"+intent {
		writeError(w, http.StatusForbidden, refusal)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var draft boardfiling.BoardDraft
	if dec.Decode(&draft) != nil || dec.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "The approved board draft is invalid.")
		return
	}
	if err := boardfiling.ValidateBoardDraft(draft); err != nil {
		writeError(w, http.StatusBadRequest, "The approved board draft is invalid: "+err.Error())
		return
	}
	if actual := boardfiling.BoardDigest(draft); subtle.ConstantTimeCompare([]byte(actual), []byte(digest)) != 1 {
		writeError(w, http.StatusForbidden, "The board draft differs from the person's approval.")
		return
	}
	requireRepo := false // as a board made with New board: cards may name no repository
	params := db.BoardParams{Name: draft.Name, Repos: draft.Repos, Columns: draft.Columns, RequireRepo: &requireRepo}
	if draft.Description != "" {
		params.Description = &draft.Description
	}
	if draft.Template != "" {
		if err := applyTemplate(&params, draft.Template); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	b, replayed, err := db.CreateApprovedBoard(r.Context(), a.Pool, params, p.CoreAccountID, intent, digest)
	if errors.Is(err, db.ErrBoardIntentUsed) {
		writeError(w, http.StatusConflict, "This approval has already been used.")
		return
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	log.Printf("boards: created approved board %s for account %s (intent %s, replayed %t)", b.ID, p.CoreAccountID, intent, replayed)
	repos := b.Repos
	if repos == nil {
		repos = []string{}
	}
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, approvedBoardReceipt{ID: b.ID, Name: b.Name, Repos: repos})
}

// handleRepoChoices is GET /api/repo-choices: the repositories the connected desktops have checked
// out, for New board's repository picker. Only a signed-in person who may create boards asks.
func (a *API) handleRepoChoices(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.IsHuman() || p.RequireAdmin("") != nil {
		writeError(w, http.StatusForbidden, "only a signed-in person who can create boards lists repositories")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var lister runner.RepoChoiceLister
	if a.runner != nil {
		lister, _ = a.runner.Driver.(runner.RepoChoiceLister)
	}
	if lister == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "repos": []runner.RepoChoice{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	repos, err := lister.RepoChoices(ctx)
	if err != nil {
		log.Printf("repo choices: runner unavailable: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "repos": []runner.RepoChoice{}, "error": "Could not reach the runner to list your desktops' repositories; type one instead."})
		return
	}
	out := make([]runner.RepoChoice, 0, len(repos))
	for _, c := range repos {
		if boardfiling.ValidRepoName(c.Repo) {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "repos": out})
}
