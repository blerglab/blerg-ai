package daemon

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

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
	d, ok := sess.loop.(*claudeCodeDriver)
	switch {
	case sess.engine != "" && sess.engine != "claude":
		result.State = "unsupported"
		result.Error = "this engine supports plugin changes for new sessions only"
	case !ok:
		result.State = "unsupported"
		result.Error = "live plugins require the Claude Code engine"
	case sess.sandboxed || len(d.prefix) > 0:
		result.State = "unsupported"
		result.Error = "live plugins are not supported in Docker or cluster sessions"
	case d.restrict:
		result.State = "unsupported"
		result.Error = "restricted sessions cannot load plugins"
	case h.cfg.Plugins == nil:
		result.State = "unsupported"
		result.Error = "this daemon has no plugin workshop"
	}
	if result.State == "unsupported" {
		h.mu.Unlock()
		h.pluginResult(result)
		return
	}
	if len(msg.Plugins) > pluginspec.MaxEntries {
		h.mu.Unlock()
		fail("too many plugins")
		return
	}
	seen := map[string]bool{}
	for _, entry := range msg.Plugins {
		key := strings.ToLower(entry.Marketplace) + "/" + entry.Plugin
		if pluginspec.ValidateClaude(entry) != nil || !h.cfg.Plugins.allow.Allows(entry.Marketplace) || seen[key] {
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
		h.applySessionPluginUpdate(ctx, msg.SessionID, sess, d, msg.RequestID, entries)
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

func (h *AgentHost) applySessionPluginUpdate(ctx context.Context, id string, sess *agentSession, d *claudeCodeDriver, request string, entries []pluginspec.Entry) {
	result := protocol.SessionPluginsResult{RequestID: request, SessionID: id, State: "failed"}
	h.mu.Lock()
	combined := append([]pluginspec.Entry(nil), sess.pluginSpecs...)
	h.mu.Unlock()
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
	entries = combined
	generation := id + "-" + ccUUID()
	dirs, res := h.cfg.Plugins.prepare(ctx, generation, entries)
	if ctx.Err() != nil || len(res.Failed) > 0 || len(res.Skipped) > 0 || res.Installed != len(entries) {
		h.cfg.Plugins.release(generation)
		result.Error = "plugin installation failed; the session keeps its previous plugins"
	} else {
		h.mu.Lock()
		live := h.sessions[id] == sess && ctx.Err() == nil
		if live {
			result.State = "applied"
			result.Plugins = entries
			if err := sess.record.setPluginSelection(entries, result); err != nil {
				result.State = "failed"
				result.Plugins = nil
				result.Error = "could not persist the session plugin selection"
			} else {
				d.pluginDirs = dirs
				sess.pluginSpecs = entries
				sess.pluginGenerations = append(sess.pluginGenerations, generation)
				result.State = "applied"
				result.Plugins = entries
			}
		} else {
			result.Error = "session ended before plugins could be applied"
		}
		h.mu.Unlock()
		if result.State != "applied" {
			h.cfg.Plugins.release(generation)
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
