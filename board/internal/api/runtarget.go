package api

import (
	"context"
	"net/http"
	"regexp"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// Run targets: where a person's Run of a card goes.
//
// Board placement is selected by the automation owner. A single Run can override it; worker
// respawns keep that override. An unavailable chosen target never falls back to another machine.

// runTarget is a chosen execution target; the zero value is the board's default.
type runTarget struct {
	Runtime  string // "cluster" | "docker" | "daemon" | "" (the default)
	DaemonID string // the selected desktop
}

// validDaemonID bounds a daemon id from a request before it reaches the runner.
var validDaemonID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// msgHostAckRequired: a host target is saved only with the owner's explicit acknowledgement.
const msgHostAckRequired = "running a board on This machine needs your acknowledgement: it runs card writers' prompts unattended and without a sandbox, as the person who runs that desktop"

// msgHostRunNeedsBoardAck: a single run may use a desktop's host only where the board's own,
// acknowledged target is that host.
const msgHostRunNeedsBoardAck = "a card runs on This machine only when the board's Where it runs is that machine, acknowledged in board settings"

// hostAcknowledged reports whether the board's stored target is the host of daemonID, acknowledged
// by the account it is recorded against, which is the board's current automation owner (a
// previous owner's acknowledgement never lets the next owner's runs onto that host).
func hostAcknowledged(board db.Board, daemonID string) bool {
	return board.RunRuntime == "daemon" && board.RunDaemonID == daemonID && board.RunHostAckAt != nil &&
		board.RunHostAckBy != "" && board.RunHostAckBy == board.RunTargetAccount &&
		board.AutomationAccountID != nil && board.RunTargetAccount == *board.AutomationAccountID
}

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
	case "docker", "daemon":
		if !validDaemonID.MatchString(t.DaemonID) {
			return http.StatusUnprocessableEntity, "daemon_id is required for a desktop runtime, and must be a daemon id"
		}
	case "":
		return http.StatusUnprocessableEntity, "daemon_id needs a runtime (docker)"
	default:
		return http.StatusUnprocessableEntity, `runtime must be "cluster", "docker", or "daemon"`
	}
	if !runTargetChooser(p, board) {
		return http.StatusForbidden, msgRunTargetOwnerOnly
	}
	return 0, ""
}

// runTargetOption is one entry of a card's run menu.
type runTargetOption struct {
	Runtime     string `json:"runtime"`
	DaemonID    string `json:"daemon_id,omitempty"`
	Label       string `json:"label"`
	Unavailable string `json:"unavailable,omitempty"`
	// DesktopName names a desktop target's machine, for the host acknowledgement's wording.
	DesktopName string `json:"desktop_name,omitempty"`
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
	repo := ""
	if len(card.Repos) > 0 {
		repo = card.Repos[0]
	}
	a.writeBoardRunTargets(w, r, p, board, repo, true)
}

// writeBoardRunTargets lists where the board (or, forCard, a single run of one of its cards) may
// run. A single run is offered a desktop's host only where the board's own acknowledged target is
// that host.
func (a *API) writeBoardRunTargets(w http.ResponseWriter, r *http.Request, p auth.Principal, board db.Board, repo string, forCard bool) {
	out := map[string]any{"default_runtime": board.RunRuntime, "default_daemon_id": board.RunDaemonID, "can_choose": false, "targets": []runTargetOption{}}
	if d, ok := a.runner.Driver.(interface{ DefaultRuntime() string }); ok && board.RunRuntime == "" {
		out["default_runtime"] = d.DefaultRuntime()
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// A board that cannot start sessions says so here, and offers "Run as me" to a person who
	// may fix it (a board admin, on an install that can mint) beside the card's Run button.
	out["board_id"] = board.ID
	out["can_run_as_me"] = false
	if _, err := a.startIdentity(r.Context(), board.ID); err != nil {
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
	options, err := a.discoverRunTargets(r.Context(), board.ID, repo)
	if err != nil {
		out["reason"] = "the runner could not list where sessions run; retry"
		writeJSON(w, 200, out)
		return
	}
	if board.RunRuntime == "" {
		if target, ok, problem := defaultDesktop(options, a.defaultRunRuntime(), repo); ok && problem == nil && target != (runTarget{}) {
			out["default_runtime"], out["default_daemon_id"] = target.Runtime, target.DaemonID
		}
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
			case rt == "daemon" && forCard && !hostAcknowledged(board, t.DaemonID):
				continue
			case (rt == "docker" || rt == "daemon") && validDaemonID.MatchString(t.DaemonID):
				targets = append(targets, runTargetOption{Runtime: rt, DaemonID: t.DaemonID, DesktopName: t.Name, Label: t.Name + " · " + runtimeLabel(rt)})
			}
		}
	}
	if board.RunRuntime != "" && !targetAvailable(runTarget{Runtime: board.RunRuntime, DaemonID: board.RunDaemonID}, options) {
		label, reason := runtimeLabel(board.RunRuntime), "Desktop is disconnected or this runtime is unavailable."
		if board.RunDaemonID != "" {
			label = board.RunDaemonID + " · " + label
		} else {
			reason = "Cluster runtime is not configured."
		}
		targets = append(targets, runTargetOption{Runtime: board.RunRuntime, DaemonID: board.RunDaemonID, Label: label, Unavailable: reason})
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
	case t.Runtime == "daemon" && validDaemonID.MatchString(t.DaemonID) && hostAcknowledged(board, t.DaemonID):
		return t
	}
	return runTarget{}
}
