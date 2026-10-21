package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// BoardLaunchOptions runs discovery under this board's stored automation
// identity and configured runtime. Callers cannot substitute another owner,
// token or execution host.
func (a *API) BoardLaunchOptions(ctx context.Context, p auth.Principal, id string, refresh bool) (runner.LaunchOptions, error) {
	var out runner.LaunchOptions
	if err := p.RequireBoard(id, "card.write"); err != nil {
		return out, fmt.Errorf("%w: %w", errModelForbidden, err)
	}
	if a.runner == nil {
		return out, fmt.Errorf("%w: no runner configured", runner.ErrUnsupported)
	}
	board, err := db.GetBoard(ctx, a.Pool, id)
	if err != nil {
		return out, err
	}
	discover, ok := a.runner.Driver.(runner.Discoverer)
	if !ok {
		return out, fmt.Errorf("%w: launch option discovery", runner.ErrUnsupported)
	}
	token, err := a.startIdentity(ctx, id)
	if err != nil {
		return out, err
	}
	repo := ""
	if len(board.Repos) > 0 {
		repo = board.Repos[0]
	}
	target, targetErr := a.resolveBoardTarget(ctx, id, runTarget{})
	if targetErr != nil {
		return out, targetErr
	}
	if placed, ok := a.runner.Driver.(runner.PlacementDiscoverer); ok && target != (runTarget{}) {
		out, err = placed.DiscoverAt(ctx, token, repo, target.Runtime, target.DaemonID, refresh)
	} else {
		out, err = discover.Discover(ctx, token, repo, refresh)
	}
	if err != nil {
		return runner.LaunchOptions{}, fmt.Errorf("runner discovery unavailable")
	}
	return out, nil
}
func (a *API) handleBoardLaunchOptions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	options, err := a.BoardLaunchOptions(r.Context(), p, r.PathValue("id"), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		if isAutomationErr(err) {
			writeStartError(w, err)
		} else {
			writeError(w, sessionModelStatus(err), err.Error())
		}
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, options)
}

func (a *API) handleSessionLaunchOptions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.IsHuman() {
		writeError(w, http.StatusForbidden, "only a signed-in person discovers chat model settings")
		return
	}
	id := r.PathValue("id")
	boardID, err := a.sessionBoardID(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	discover, ok := a.runner.Driver.(runner.SessionDiscoverer)
	if !ok {
		writeError(w, http.StatusNotImplemented, "this runner cannot discover session settings")
		return
	}
	ext, err := a.runnerExternalID(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	token, err := a.startIdentity(r.Context(), boardID)
	if err != nil {
		writeStartError(w, err)
		return
	}
	options, err := discover.DiscoverSession(r.Context(), token, ext, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, http.StatusBadGateway, "runner session discovery unavailable")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, options)
}
