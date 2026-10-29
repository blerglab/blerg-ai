package daemon

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/pluginsinstall"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Native engines that must initialize a replacement delay their terminal
// result until that replacement proves it can load the conversation.
type sessionPluginLoader interface {
	stageSessionPlugins(apply func(), complete func(bool) bool)
}

// Each native driver owns its idle gate and snapshot format. Authorization and
// durable selection stay shared so live loading cannot widen plugin permissions.
type sessionPluginDriver interface {
	queuePluginUpdate(func(context.Context)) bool
	sessionPluginProblem() string
	prepareSessionPlugins(context.Context, *pluginWorkshop, string, []pluginspec.Entry, bool) (func(), pluginsinstall.Result)
}

func (d *claudeCodeDriver) sessionPluginProblem() string {
	if len(d.prefix) > 0 {
		return "This runtime loads plugins when a session starts — added for your next session."
	}
	if d.restrict {
		return "Restricted sessions cannot load plugins."
	}
	return ""
}
func (d *claudeCodeDriver) prepareSessionPlugins(ctx context.Context, w *pluginWorkshop, id string, entries []pluginspec.Entry, _ bool) (func(), pluginsinstall.Result) {
	dirs, res := w.prepare(ctx, id, entries)
	return func() { d.pluginDirs = dirs }, res
}

// queuePluginUpdate is deliberately not Interrupt/markStale: applying a plugin
// must neither cut off a foreground turn nor stop its owned background work.
func (d *claudeCodeDriver) queuePluginUpdate(apply func(context.Context)) bool {
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.steer.closed || len(d.pendingPlugins) >= pluginspec.MaxEntries {
		return false
	}
	d.pendingPlugins = append(d.pendingPlugins, apply)
	d.signal()
	return true
}

func (d *claudeCodeDriver) pluginUpdateWake() <-chan time.Time {
	d.sm.Lock()
	pending := len(d.pendingPlugins) > 0
	d.sm.Unlock()
	if pending {
		return time.After(250 * time.Millisecond)
	}
	return nil
}

// The run loop alone calls this. startMu also fences read-only MCP preparation
// from launching a process with old plugin dirs while a snapshot is installed.
func (d *claudeCodeDriver) applyPendingPlugins(ctx context.Context) bool {
	d.sm.Lock()
	pending := len(d.pendingPlugins) > 0
	d.sm.Unlock()
	if !pending || ctx.Err() != nil {
		return false
	}
	d.steer.startMu.Lock()
	defer d.steer.startMu.Unlock()
	d.subMu.Lock()
	d.sm.Lock()
	p := d.steer.proc
	if len(d.pendingPlugins) == 0 || d.steer.closed || d.steer.running || len(d.steer.awaiting) > 0 || d.backgroundRunningLocked() > 0 || len(d.subagents) > 0 || len(d.bgCalls) > 0 || (p != nil && p.inTurn) {
		d.sm.Unlock()
		d.subMu.Unlock()
		return false
	}
	apply := d.pendingPlugins[0]
	d.pendingPlugins = d.pendingPlugins[1:]
	closeNow := p != nil && d.markStdinClosedLocked(p)
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	d.subMu.Unlock()
	if closeNow {
		d.finishStdinClose(p)
	}
	if p != nil {
		select {
		case <-p.done:
		case <-ctx.Done():
			return true
		}
	}
	if ctx.Err() == nil {
		apply(ctx)
	}
	return true
}

func validPluginRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (h *AgentHost) pluginResult(result protocol.SessionPluginsResult) {
	result.Type = "session_plugins_result"
	if bounded, ok := h.sender.(interface {
		SendSessionPluginsResult(protocol.SessionPluginsResult) error
	}); ok {
		_ = bounded.SendSessionPluginsResult(result)
	} else {
		_ = h.sender.Send(result)
	}
}

// ApplySessionPlugins accepts only server-authorized approved additions. It never
// reads account settings and rechecks the operator allowlist before any install.
func (h *AgentHost) ApplySessionPlugins(msg protocol.ApplySessionPlugins) {
	result := protocol.SessionPluginsResult{RequestID: msg.RequestID, SessionID: msg.SessionID, State: "failed"}
	fail := func(message string) { result.Error = message; h.pluginResult(result) }
	if !validPluginRequestID(msg.RequestID) || msg.SessionID == "" {
		fail("invalid plugin request")
		return
	}
	h.mu.Lock()
	sess := h.sessions[msg.SessionID]
	if sess == nil {
		h.mu.Unlock()
		fail("session is no longer running")
		return
	}
	if previous, ok := sess.pluginRequests[msg.RequestID]; ok {
		h.mu.Unlock()
		h.pluginResult(previous)
		return
	}
	d, ok := sess.loop.(sessionPluginDriver)
	switch {
	case !ok || sess.sandboxed:
		result.State = "unsupported"
		result.Error = "This engine or runtime loads plugins when a session starts — added for your next session."
	case d.sessionPluginProblem() != "":
		result.State = "unsupported"
		result.Error = d.sessionPluginProblem()
	case h.cfg.Plugins == nil:
		result.State = "unsupported"
		result.Error = "this daemon has no plugin workshop"
	}
	if result.State == "unsupported" {
		h.mu.Unlock()
		h.pluginResult(result)
		return
	}
	if len(msg.Plugins)+len(msg.Remove) > pluginspec.MaxEntries {
		h.mu.Unlock()
		fail("too many plugins")
		return
	}
	engine := sess.engine
	if engine == "" {
		engine = pluginspec.EngineClaude
	}
	seen := map[string]bool{}
	for i, entry := range append(append([]pluginspec.Entry(nil), msg.Plugins...), msg.Remove...) {
		key := strings.ToLower(entry.Marketplace) + "/" + entry.Plugin
		if pluginspec.Validate(engine, entry) != nil || (i < len(msg.Plugins) && !h.cfg.Plugins.currentAllow().Allows(entry.Marketplace)) || seen[key] {
			h.mu.Unlock()
			fail("plugin is invalid, duplicated, or outside the operator allowlist")
			return
		}
		seen[key] = true
	}
	if len(sess.pluginRequests) >= 64 {
		h.mu.Unlock()
		fail("too many live plugin requests; start a new session")
		return
	}
	entries := append([]pluginspec.Entry(nil), msg.Plugins...)
	remove := append([]pluginspec.Entry(nil), msg.Remove...)
	result.State = "queued"
	if sess.pluginRequests == nil {
		sess.pluginRequests = map[string]protocol.SessionPluginsResult{}
	}
	sess.pluginRequests[msg.RequestID] = result
	ready := make(chan struct{})
	accepted := d.queuePluginUpdate(func(ctx context.Context) {
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		h.applySessionPluginUpdate(ctx, msg.SessionID, sess, d, msg.RequestID, entries, remove)
	})
	if !accepted {
		delete(sess.pluginRequests, msg.RequestID)
		h.mu.Unlock()
		result.State = "failed"
		fail("another plugin update is already queued")
		return
	}
	h.mu.Unlock()
	h.pluginResult(result)
	close(ready)
}

func (h *AgentHost) applySessionPluginUpdate(ctx context.Context, id string, sess *agentSession, d sessionPluginDriver, request string, entries []pluginspec.Entry, remove []pluginspec.Entry) {
	result := protocol.SessionPluginsResult{RequestID: request, SessionID: id, State: "failed"}
	h.mu.Lock()
	combined := append([]pluginspec.Entry(nil), sess.pluginSpecs...)
	h.mu.Unlock()
	for _, removal := range remove {
		kept := combined[:0]
		for _, loaded := range combined {
			if !strings.EqualFold(loaded.Marketplace, removal.Marketplace) || loaded.Plugin != removal.Plugin {
				kept = append(kept, loaded)
			}
		}
		combined = kept
	}
	for _, addition := range entries {
		found := false
		for _, loaded := range combined {
			if strings.EqualFold(loaded.Marketplace, addition.Marketplace) && loaded.Plugin == addition.Plugin {
				found = true
				break
			}
		}
		if !found {
			combined = append(combined, addition)
		}
	}
	if len(combined) > pluginspec.MaxEntries {
		result.Error = "too many session plugins"
		h.mu.Lock()
		sess.pluginRequests[request] = result
		_ = sess.record.setPluginSelection(sess.pluginSpecs, result)
		h.mu.Unlock()
		h.pluginResult(result)
		return
	}
	removalOnly := len(entries) == 0 && len(remove) > 0
	entries = combined
	generation := id + "-" + ccUUID()
	apply, res := d.prepareSessionPlugins(ctx, h.cfg.Plugins, generation, entries, removalOnly)
	if ctx.Err() != nil || len(res.Failed) > 0 || len(res.Skipped) > 0 || res.Installed != len(entries) {
		h.cfg.Plugins.release(generation)
		h.cfg.Plugins.releaseCodex(generation)
		result.Error = "plugin installation failed; the session keeps its previous plugins"
	} else {
		h.mu.Lock()
		live := h.sessions[id] == sess && ctx.Err() == nil
		if live {
			if loader, ok := d.(sessionPluginLoader); ok {
				result.State = "queued"
				if err := sess.record.setPluginSelection(sess.pluginSpecs, result); err == nil {
					loader.stageSessionPlugins(apply, func(loaded bool) bool {
						return h.completeSessionPluginLoad(ctx, id, sess, request, generation, entries, loaded)
					})
					h.mu.Unlock()
					return
				}
				result.State = "failed"
				result.Error = "could not persist the session plugin selection"
			} else {
				result.State = "applied"
				result.Plugins = entries
				if err := sess.record.setPluginSelection(entries, result); err != nil {
					result.State = "failed"
					result.Plugins = nil
					result.Error = "could not persist the session plugin selection"
				} else {
					apply()
					sess.pluginSpecs = entries
					sess.pluginGenerations = append(sess.pluginGenerations, generation)
					result.State = "applied"
					result.Plugins = entries
				}
			}
		} else {
			result.Error = "session ended before plugins could be applied"
		}
		h.mu.Unlock()
		if result.State != "applied" {
			h.cfg.Plugins.release(generation)
			h.cfg.Plugins.releaseCodex(generation)
		}
	}
	h.mu.Lock()
	sess.pluginRequests[request] = result
	if result.State != "applied" {
		_ = sess.record.setPluginSelection(sess.pluginSpecs, result)
	}
	h.mu.Unlock()
	h.pluginResult(result)
}

// The callback runs only after initialization/resume, or after the failed
// replacement has been reaped and the driver restored its previous settings.
func (h *AgentHost) completeSessionPluginLoad(ctx context.Context, id string, sess *agentSession, request, generation string, entries []pluginspec.Entry, loaded bool) bool {
	result := protocol.SessionPluginsResult{RequestID: request, SessionID: id, State: "failed", Error: "The plugin change failed. Your session is continuing with its previous plugins."}
	h.mu.Lock()
	if loaded && ctx.Err() == nil && h.sessions[id] == sess {
		result.State, result.Error, result.Plugins = "applied", "", entries
		if err := sess.record.setPluginSelection(entries, result); err != nil {
			result.State, result.Plugins = "failed", nil
			result.Error = "could not persist the session plugin selection; the session keeps its previous plugins"
		} else {
			sess.pluginSpecs = entries
			sess.pluginGenerations = append(sess.pluginGenerations, generation)
		}
	}
	sess.pluginRequests[request] = result
	if result.State != "applied" {
		_ = sess.record.setPluginSelection(sess.pluginSpecs, result)
	}
	h.mu.Unlock()
	if result.State != "applied" {
		h.cfg.Plugins.release(generation)
		h.cfg.Plugins.releaseCodex(generation)
	}
	h.pluginResult(result)
	return result.State == "applied"
}

// Unlike best-effort telemetry writes, a failed selection write must keep the
// live configuration unchanged so recovery cannot silently lose an applied spec.
func (k *recordKeeper) setPluginSelection(entries []pluginspec.Entry, result protocol.SessionPluginsResult) error {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.deleted || (k.frozen != nil && k.frozen.Load()) {
		return errors.New("record is stopping")
	}
	next := k.rec
	next.Plugins = append([]pluginspec.Entry(nil), entries...)
	next.PluginRequests = maps.Clone(k.rec.PluginRequests)
	if next.PluginRequests == nil {
		next.PluginRequests = map[string]protocol.SessionPluginsResult{}
	}
	next.PluginRequests[result.RequestID] = result
	if err := writeAgentRecord(k.dir, next); err != nil {
		return err
	}
	k.rec = next
	return nil
}
