package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

func (a *API) discoverRunTargets(ctx context.Context, boardID, repo string) (runner.LaunchOptions, error) {
	token, err := a.startIdentity(ctx, boardID)
	if err != nil {
		return runner.LaunchOptions{}, err
	}
	if d, ok := a.runner.Driver.(runner.TargetDiscoverer); ok {
		return d.DiscoverTargets(ctx, token, repo)
	}
	if d, ok := a.runner.Driver.(runner.Discoverer); ok {
		return d.Discover(ctx, token, repo, false)
	}
	return runner.LaunchOptions{}, runner.ErrUnsupported
}

func targetAvailable(target runTarget, options runner.LaunchOptions) bool {
	for _, option := range options.Targets {
		if target.DaemonID != option.DaemonID {
			continue
		}
		for _, runtime := range option.Runtimes {
			if target.Runtime == runtime {
				return true
			}
		}
	}
	return false
}

// defaultDesktop is the owner rule for a board with no target of its own: on an install with no
// cluster runtime, or one whose configured default runtime is a desktop (preferred daemon or
// docker), a board runs on a desktop with no setting needed. One desktop is that desktop. With
// several, the one that has the board's repository is chosen (as the runner's own repo-aware choice
// did before boards had a target); none or more than one with it is refused with a message that
// says which. When the runner cannot tell (no repo, or an older runner that does not report it), the
// target names only the sandbox and leaves the desktop to the runner. ok is false when the cluster is
// the default, or no desktop is connected.
//
// It never chooses a desktop's host: a start lands on a host only where the person chose and
// acknowledged it (Where it runs). A desktop without a sandbox is refused with that instruction.
func defaultDesktop(options runner.LaunchOptions, preferred, repo string) (target runTarget, ok bool, err error) {
	preferDesktop := preferred == "daemon" || preferred == "docker"
	var desktops []*runner.LaunchTarget
	for i := range options.Targets {
		t := &options.Targets[i]
		if t.DaemonID == "" {
			if !preferDesktop && slices.Contains(t.Runtimes, "cluster") {
				return runTarget{}, false, nil
			}
			continue
		}
		desktops = append(desktops, t)
	}
	if len(desktops) == 0 {
		return runTarget{}, false, nil
	}
	desktop := desktops[0]
	if len(desktops) > 1 {
		var with []*runner.LaunchTarget
		for _, t := range desktops {
			if t.HasRepo == nil {
				return runTarget{Runtime: "docker"}, true, nil
			}
			if *t.HasRepo {
				with = append(with, t)
			}
		}
		switch len(with) {
		case 0:
			return runTarget{}, true, &automationError{fmt.Sprintf("none of the %d connected desktops has this board's repository %s; open it on one, or choose Where it runs in board settings", len(desktops), repo)}
		case 1:
			desktop = with[0]
		default:
			return runTarget{}, true, &automationError{fmt.Sprintf("%d connected desktops have this board's repository %s; choose which one in Where it runs in board settings", len(with), repo)}
		}
	}
	if !slices.Contains(desktop.Runtimes, "docker") {
		return runTarget{}, true, &automationError{msgNoSandbox(desktop.Name)}
	}
	return runTarget{Runtime: "docker", DaemonID: desktop.DaemonID}, true, nil
}

// msgNoInventory: the runner did not say where an unplaced board start would run.
const msgNoInventory = "this runner did not say where this board's sessions can run, and a board never runs on a desktop's host by itself: " +
	"choose Where it runs in board settings, or update the runner"

// msgNoSandbox: the desktop a board would run on has no sandbox, and a board never chooses a host.
func msgNoSandbox(desktop string) string {
	return fmt.Sprintf("%s has no sandbox, and a board never runs on a desktop's host by itself: install the sandbox on %s, "+
		"or choose its host in Where it runs in board settings", desktop, desktop)
}

func targetUnavailable(t runTarget) error {
	if t.Runtime == "cluster" {
		return &automationError{"this board selected the cluster, but the cluster runtime is unavailable; choose Where it runs in board settings"}
	}
	return &automationError{"the selected desktop is disconnected or does not offer this runtime; reconnect it or choose Where it runs in board settings"}
}
func (a *API) requireRunTarget(ctx context.Context, boardID string, target runTarget) error {
	options, err := a.discoverRunTargets(ctx, boardID, "")
	if err != nil {
		return &automationError{"could not check where this board runs; retry after the runner reconnects"}
	}
	if !targetAvailable(target, options) {
		return targetUnavailable(target)
	}
	return nil
}
func (a *API) resolveBoardTarget(ctx context.Context, boardID string, override runTarget, snapshot ...db.BoardAutomation) (runTarget, error) {
	board, err := db.GetBoard(ctx, a.Pool, boardID)
	if err != nil {
		return runTarget{}, err
	}
	if len(snapshot) > 0 {
		authority := snapshot[0]
		board.RunRuntime, board.RunDaemonID, board.RunTargetAccount = authority.RunRuntime, authority.RunDaemonID, authority.RunTargetAccount
		board.AutomationAccountID = &authority.AccountID
	}
	target := override
	if target == (runTarget{}) {
		target = runTarget{Runtime: board.RunRuntime, DaemonID: board.RunDaemonID}
		if target != (runTarget{}) && (board.AutomationAccountID == nil || board.RunTargetAccount != *board.AutomationAccountID) {
			return runTarget{}, &automationError{"the board's automation owner changed; its current owner must save Where it runs again"}
		}
	}
	// A host target (override or stored) runs only where the board's own acknowledged target is
	// that host; the operator's configured default runtime is not a person's choice and is exempt.
	if target.Runtime == "daemon" && !hostAcknowledged(board, target.DaemonID) {
		return runTarget{}, &automationError{msgHostRunNeedsBoardAck}
	}
	repo := ""
	if len(board.Repos) > 0 {
		repo = board.Repos[0]
	}
	options, err := a.discoverRunTargets(ctx, boardID, repo)
	if errors.Is(err, runner.ErrUnsupported) {
		return target, nil
	} // legacy drivers have no placement inventory
	if err != nil {
		return runTarget{}, &automationError{"could not check where this board runs; retry after the runner reconnects"}
	}
	if target != (runTarget{}) {
		if !targetAvailable(target, options) {
			return runTarget{}, targetUnavailable(target)
		}
		return target, nil
	}
	if desktop, ok, problem := defaultDesktop(options, a.defaultRunRuntime(), repo); ok {
		if problem != nil {
			return runTarget{}, problem
		}
		if desktop.DaemonID == "" {
			return desktop, nil // the runner picks the desktop that has the repo, in its sandbox
		}
		if !targetAvailable(desktop, options) {
			return runTarget{}, targetUnavailable(desktop)
		}
		return desktop, nil
	}
	for _, option := range options.Targets {
		for _, rt := range option.Runtimes {
			if rt == "cluster" {
				return runTarget{}, nil
			}
		}
	}
	// An older runner whose discovery lists no targets cannot say where an unplaced start would land.
	// It is never left to choose a desktop's host: the cluster default is sent as such, a desktop
	// default goes to the sandbox, and with no default the start is refused until the person chooses.
	if options.Targets == nil {
		switch a.defaultRunRuntime() {
		case "cluster":
			return runTarget{}, nil
		case "daemon", "docker":
			return runTarget{Runtime: "docker"}, nil
		}
		return runTarget{}, nil // a start refuses it (startOnRunnerConfigured); discovery shows it as unplaced
	}
	return runTarget{}, &automationError{"no single desktop is available for this board; connect its desktop or choose Where it runs in board settings"}
}

func (a *API) handleBoardRunTargets(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	board, err := db.GetBoard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err = p.RequireBoard(board.ID, "card.write"); err != nil {
		writeError(w, 403, err.Error())
		return
	}
	if a.runner == nil {
		writeError(w, 503, "no runner configured")
		return
	}
	repo := ""
	if len(board.Repos) > 0 {
		repo = board.Repos[0]
	}
	a.writeBoardRunTargets(w, r, p, board, repo, false)
}

func (a *API) defaultRunRuntime() string {
	if d, ok := a.runner.Driver.(interface{ DefaultRuntime() string }); ok {
		return d.DefaultRuntime()
	}
	return ""
}
