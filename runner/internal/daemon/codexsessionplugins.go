package daemon

import (
	"context"
	"encoding/json"
	"maps"
	"time"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
)

func (d *codexDriver) sessionPluginProblem() string {
	if d.prefix.enabled() {
		return "This runtime loads plugins when a session starts — added for your next session."
	}
	if d.codexToolsRestricted() {
		return "Restricted sessions cannot load plugins."
	}
	return ""
}

func (d *codexDriver) queuePluginUpdate(apply func(context.Context)) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || len(d.pendingPlugins) >= pluginspec.MaxEntries {
		return false
	}
	d.pendingPlugins = append(d.pendingPlugins, apply)
	select {
	case d.pluginWake <- struct{}{}:
	default:
	}
	return true
}
func (d *codexDriver) pluginUpdateWake() <-chan time.Time {
	d.mu.Lock()
	pending := len(d.pendingPlugins) > 0
	d.mu.Unlock()
	if pending {
		return time.After(250 * time.Millisecond)
	}
	return nil
}

// Called only by the native event loop, before taking the next input. Unknown
// terminal inventory or unsettled child work keeps the current process alive.
func (a *codexApp) restartForPlugins() bool {
	d := a.d
	d.mu.Lock()
	recoveryReady := d.recoveryInputReady
	ready := len(d.pendingPlugins) > 0 && !d.stopped && !d.activeTurn && len(d.bgTasks) == 0
	for _, c := range d.children {
		if !c.done {
			ready = false
		}
	}
	d.mu.Unlock()
	if !ready || (recoveryReady != nil && !recoveryReady()) {
		return false
	}
	a.mu.Lock()
	backlog := a.pluginWorkQueuedLocked()
	a.mu.Unlock()
	if backlog {
		return false
	}
	// thread/read is implemented by the installed app-server. A process id
	// without an exit code can still own a background PTY after turn completion.
	raw, err := a.call("thread/read", map[string]any{"threadId": a.threadID(), "includeTurns": true})
	var state struct {
		Thread struct {
			ID     string
			Status struct{ Type string }
			Turns  []struct {
				Status string
				Items  []struct {
					Type, Status, ProcessID string
					ExitCode                *int
				}
			}
		}
	}
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Thread.ID != a.threadID() || state.Thread.Status.Type != "idle" || state.Thread.Turns == nil {
		return false
	}
	for _, turn := range state.Thread.Turns {
		if turn.Status == "inProgress" {
			return false
		}
		for _, item := range turn.Items {
			if item.Type == "commandExecution" && (item.Status == "inProgress" || (item.ProcessID != "" && item.ExitCode == nil)) {
				return false
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turn != "" || a.starting || a.pluginWorkQueuedLocked() || len(a.dynamicCalls) > 0 || len(a.pendingQuestions) > 0 || len(a.childTurns) > 0 || len(a.pendingChildren) > 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.activeTurn || len(d.bgTasks) > 0 {
		return false
	}
	for _, c := range d.children {
		if !c.done {
			return false
		}
	}
	// Confirm each replacement before computing the next selection. A queued
	// addition must not inherit an earlier request's failed configuration.
	d.restartPlugins = d.pendingPlugins[:1]
	d.pendingPlugins = d.pendingPlugins[1:]
	return len(d.restartPlugins) > 0
}

// runApp has fully stopped and reaped the old process before this runs. The
// next runApp uses thread/resume with the unchanged durable native thread ID.
func (d *codexDriver) applySessionPlugins(ctx context.Context) bool {
	d.mu.Lock()
	pending := d.restartPlugins
	d.restartPlugins = nil
	d.mu.Unlock()
	for _, apply := range pending {
		if ctx.Err() == nil {
			apply(ctx)
		}
	}
	return len(pending) > 0
}

func (d *codexDriver) prepareSessionPlugins(ctx context.Context, w *pluginWorkshop, id string, entries []pluginspec.Entry, removalOnly bool) (func(), pluginsinstall.Result) {
	d.mu.Lock()
	previousHome := d.CodexHome
	d.mu.Unlock()
	var home string
	var config map[string]any
	var res pluginsinstall.Result
	if removalOnly && previousHome != "" {
		home, config, res = w.prepareCodexRemoval(ctx, id, entries, previousHome, codexUserHome(d.env, w.home))
	} else {
		home, config, res = w.prepareCodex(ctx, id, entries, codexUserHome(d.env, w.home))
	}
	if home != "" && d.RestrictedRuntime != nil {
		source := codexUserHome(d.env, w.home)
		if err := sanitizeCodexInteractiveHome(ctx, home, source, d.workDir); err != nil {
			return func() {}, failedCodexPlugins(entries)
		}
	}
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		next := maps.Clone(d.RuntimeConfig)
		if next == nil {
			next = map[string]any{}
		}
		next["plugins"] = config["plugins"]
		next["marketplaces"] = config["marketplaces"]
		d.CodexHome = home
		d.RuntimeConfig = next
	}, res
}

// Reading extended history can itself emit deprecationNotice on newer CLIs.
// That informational notification cannot start work or block a safe restart.
func (a *codexApp) pluginWorkQueuedLocked() bool {
	for _, ev := range a.backlog {
		if len(ev.ID) > 0 || ev.Method != "deprecationNotice" {
			return true
		}
	}
	return false
}

// Only the run loop owns an attempt. The durable selection remains the previous
// one until the replacement has initialized and resumed the original thread.
type codexPluginAttempt struct {
	home     string
	config   map[string]any
	complete func(bool) bool
}

func (d *codexDriver) stageSessionPlugins(apply func(), complete func(bool) bool) {
	d.mu.Lock()
	d.pluginAttempt = &codexPluginAttempt{home: d.CodexHome, config: d.RuntimeConfig, complete: complete}
	d.mu.Unlock()
	apply()
}

func (d *codexDriver) confirmSessionPlugins() bool {
	attempt := d.pluginAttempt
	if attempt == nil {
		return true
	}
	if !attempt.complete(true) {
		// Persistence refused the new selection; runApp must stop it before rollback.
		attempt.complete = nil
		return false
	}
	d.pluginAttempt = nil
	return true
}

func (d *codexDriver) rollbackSessionPlugins(ctx context.Context) bool {
	attempt := d.pluginAttempt
	if attempt == nil {
		return false
	}
	d.mu.Lock()
	d.CodexHome, d.RuntimeConfig = attempt.home, attempt.config
	d.pluginAttempt = nil
	d.mu.Unlock()
	if attempt.complete != nil {
		attempt.complete(false)
	}
	if ctx.Err() == nil {
		d.emit("error", agent.ErrorPayload{Message: "The plugin change failed. Your session is continuing with its previous plugins.", Retryable: true})
	}
	return true
}
