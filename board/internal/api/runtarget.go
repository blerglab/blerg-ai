package api

import (
	"context"
	"net/http"
	"regexp"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// Run targets: where a person's Run of a card goes.
//
// By default a card runs where the board's runner is configured to start sessions
// (BLERG_BOARD_RUNNER_RUNTIME, or the runner's own choice). The person who runs a card may
// instead pick the cluster or one of the connected desktop daemons' sandboxes. The bare host
// (This machine) is not offered: a card's run is unattended and its prompt is card text any board
// writer can edit, which the runner's launch sheet only allows on the host after an explicit
// acknowledgement. The start still goes through the one start path (spawnSessionAt →
// startOnRunnerConfigured) under the board's automation token, so the session runs as that
// token's owner with their credentials, exactly as a launch from the runner would. That is why
// only the owner may choose: a desktop is a person's own machine, and a session placed there
// must be theirs. Automated runs never choose; a respawn of the card's worker keeps the last
// choice (cardRunTarget) while the board's automation still runs as the account that made it.

// runTarget is a chosen execution target; the zero value is the board's default.
type runTarget struct {
	Runtime  string // "cluster" | "docker" | "" (the default)
	DaemonID string // the connected daemon, for docker
}

// validDaemonID bounds a daemon id from a request before it reaches the runner.
var validDaemonID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const msgRunTargetOwnerOnly = "only the owner of this board's automation token can choose where a card runs: the session runs as them"

// runTargetChooser reports whether p may choose where the board's cards run: a signed-in
// person who owns the board's automation token.
func runTargetChooser(p auth.Principal, board db.Board) bool {
	account := p.HumanAccountID()
	return account != "" && board.AutomationAccountID != nil && *board.AutomationAccountID == account
}

// checkRunTarget refuses a chosen target that is malformed or not p's to choose, as (status,
// message); (0, "") accepts it. The zero target is always accepted.
func (a *API) checkRunTarget(p auth.Principal, board db.Board, t runTarget) (int, string) {
	if t == (runTarget{}) {
		return 0, ""
	}
	switch t.Runtime {
	case "cluster":
		if t.DaemonID != "" {
			return http.StatusUnprocessableEntity, "daemon_id does not apply to the cluster"
		}
	case "docker":
		if !validDaemonID.MatchString(t.DaemonID) {
			return http.StatusUnprocessableEntity, "daemon_id is required for a desktop sandbox, and must be a daemon id"
		}
	case "daemon":
		return http.StatusUnprocessableEntity, "a card does not run on This machine from the board: choose a desktop's sandbox or the cluster"
	case "":
		return http.StatusUnprocessableEntity, "daemon_id needs a runtime (docker)"
	default:
		return http.StatusUnprocessableEntity, `runtime must be "cluster" or "docker"`
	}
	if !runTargetChooser(p, board) {
		return http.StatusForbidden, msgRunTargetOwnerOnly
	}
	return 0, ""
}

// runTargetOption is one entry of a card's run menu.
type runTargetOption struct {
	Runtime  string `json:"runtime"`
	DaemonID string `json:"daemon_id,omitempty"`
	Label    string `json:"label"`
}

// runtimeLabel names a runtime the way the runner's launch sheet does.
func runtimeLabel(rt string) string {
	switch rt {
	case "docker":
		return "Local sandbox"
	case "daemon":
		return "This machine"
	}
	return "Cluster"
}

// handleCardRunTargets answers GET /api/cards/{id}/run-targets: the board's default runtime,
// whether the caller may choose (runTargetChooser), and, when they may, every target the
// runner offers. No credential is returned.
func (a *API) handleCardRunTargets(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	out := map[string]any{"default_runtime": "", "can_choose": false, "targets": []runTargetOption{}}
	if d, ok := a.runner.Driver.(interface{ DefaultRuntime() string }); ok {
		out["default_runtime"] = d.DefaultRuntime()
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// A board that cannot start sessions says so here, and offers "Run as me" to a person who
	// may fix it (a board admin, on an install that can mint) beside the card's Run button.
	out["board_id"] = board.ID
	out["can_run_as_me"] = false
	if _, _, err := a.startIdentity(r.Context(), board.ID); err != nil {
		if !isAutomationErr(err) {
			writeDBError(w, err)
			return
		}
		out["automation_problem"] = automationErrText(err)
		out["can_run_as_me"] = a.canRunAsMe(p, board.ID)
	}
	if !runTargetChooser(p, board) {
		out["reason"] = msgRunTargetOwnerOnly
		writeJSON(w, http.StatusOK, out)
		return
	}
	discover, ok := a.runner.Driver.(runner.Discoverer)
	if !ok {
		out["reason"] = "this runner cannot list where sessions run"
		writeJSON(w, http.StatusOK, out)
		return
	}
	token, _, err := a.startIdentity(r.Context(), board.ID)
	if err != nil {
		out["reason"] = automationErrText(err)
		writeJSON(w, http.StatusOK, out)
		return
	}
	repo := ""
	if len(card.Repos) > 0 {
		repo = card.Repos[0]
	}
	options, err := discover.Discover(r.Context(), token, repo, false)
	if err != nil {
		out["reason"] = "the runner could not list where sessions run; retry"
		writeJSON(w, http.StatusOK, out)
		return
	}
	targets := []runTargetOption{}
	for _, t := range options.Targets {
		if len(targets) >= 64 || len(t.Name) > 128 {
			continue
		}
		for _, rt := range t.Runtimes {
			switch {
			case rt == "cluster" && t.DaemonID == "":
				targets = append(targets, runTargetOption{Runtime: rt, Label: runtimeLabel(rt)})
			case rt == "docker" && validDaemonID.MatchString(t.DaemonID):
				targets = append(targets, runTargetOption{Runtime: rt, DaemonID: t.DaemonID, Label: t.Name + " · " + runtimeLabel(rt)})
			}
		}
	}
	out["can_choose"] = true
	out["targets"] = targets
	writeJSON(w, http.StatusOK, out)
}

// targetAccount is the automation account a chosen target is recorded against ("" for none).
func targetAccount(board db.Board, t runTarget) string {
	if t == (runTarget{}) || board.AutomationAccountID == nil {
		return ""
	}
	return *board.AutomationAccountID
}

// cardRunTarget is where the card's last chosen run went, for a respawn of its worker: the zero
// target unless that choice was recorded against the account the board's automation runs as now,
// and is still one the board offers.
func (a *API) cardRunTarget(ctx context.Context, board db.Board, cardID string) runTarget {
	if board.AutomationAccountID == nil || *board.AutomationAccountID == "" {
		return runTarget{}
	}
	var t runTarget
	var account string
	err := a.Pool.QueryRow(ctx, `
		SELECT target_runtime, target_daemon_id, target_account FROM runner_sessions
		 WHERE card_id = $1 AND role = 'worker' ORDER BY created_at DESC, id DESC LIMIT 1`, cardID).
		Scan(&t.Runtime, &t.DaemonID, &account)
	if err != nil || account != *board.AutomationAccountID {
		return runTarget{}
	}
	switch {
	case t.Runtime == "cluster" && t.DaemonID == "":
		return t
	case t.Runtime == "docker" && validDaemonID.MatchString(t.DaemonID):
		return t
	}
	return runTarget{}
}
