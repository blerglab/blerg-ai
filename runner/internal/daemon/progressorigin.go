package daemon

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const progressHookSettings = `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"blerg-runner _progress_scope_hook","timeout":2}]}]}}`

// Only active Bash calls are retained. Results and process exit remove their
// identities, so a leftover background process cannot impersonate a later run.
type progressCalls struct {
	mu    sync.Mutex
	calls map[string]string
}

func validProgressOriginID(id string) bool {
	return id != "" && len(id) <= 256 && utf8.ValidString(id) && strings.IndexFunc(id, unicode.IsControl) < 0
}
func (p *progressCalls) start(call, parent string) {
	if !validProgressOriginID(call) || (parent != "" && !validProgressOriginID(parent)) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]string{}
	}
	if _, exists := p.calls[call]; !exists && len(p.calls) < 256 {
		p.calls[call] = parent
	}
}
func (p *progressCalls) end(call string) { p.mu.Lock(); delete(p.calls, call); p.mu.Unlock() }
func (p *progressCalls) clear()          { p.mu.Lock(); p.calls = nil; p.mu.Unlock() }
func (d *claudeCodeDriver) resolveProgressOrigin(call, thread string) (string, string, bool) {
	if !validProgressOriginID(call) || thread != "" {
		return "", "", false
	}
	d.progress.mu.Lock()
	defer d.progress.mu.Unlock()
	parent, ok := d.progress.calls[call]
	return parent, "", ok
}

//nolint:unparam // Implements progressOriginResolver; Codex identifies children with ChildID, while Claude uses ParentCallID.
func (d *codexDriver) resolveProgressOrigin(call, thread string) (string, string, bool) {
	if call != "" || !validProgressOriginID(thread) {
		return "", "", false
	}
	d.mu.Lock()
	app := d.app
	root := thread == d.threadID && d.threadID != "" && d.activeTurn
	child := ""
	if c := d.children[thread]; c != nil && !c.done {
		child = c.callID
	}
	allowed := !d.codexToolsRestricted()
	d.mu.Unlock()
	if !allowed || app == nil || app.ctx == nil || app.ctx.Err() != nil {
		return "", "", false
	}
	select {
	case <-app.done:
		return "", "", false
	default:
	}
	app.mu.Lock()
	active := root && app.turn != "" || child != "" && app.childTurns[thread] != ""
	app.mu.Unlock()
	d.mu.Lock()
	same := d.app == app
	if child != "" {
		c := d.children[thread]
		same = same && c != nil && !c.done && c.callID == child
	}
	d.mu.Unlock()
	if !active || !same {
		return "", "", false
	}
	return "", child, true
}

type progressOriginResolver interface {
	resolveProgressOrigin(call, thread string) (string, string, bool)
}

// ResolveProgressOrigin uses an already-running driver; it never prepares or
// starts an engine just to service a CLI request.
func (h *AgentHost) ResolveProgressOrigin(req protocol.ResolveProgressOrigin) {
	if !validProgressOriginID(req.RequestID) || !validProgressOriginID(req.SessionID) {
		return
	}
	res := protocol.ProgressOriginResult{Type: "progress_origin_result", RequestID: req.RequestID, SessionID: req.SessionID}
	sess := h.get(req.SessionID)
	if sess != nil && sess.loop != nil && sess.runDone != nil {
		select {
		case <-sess.runDone:
		default:
			if resolver, ok := sess.loop.(progressOriginResolver); ok {
				res.ParentCallID, res.ChildID, res.OK = resolver.resolveProgressOrigin(req.CallID, req.NativeThreadID)
				if h.get(req.SessionID) != sess {
					res.ParentCallID, res.ChildID, res.OK = "", "", false
				}
			}
		}
	}
	if bounded, ok := h.sender.(interface {
		SendProgressOriginResult(protocol.ProgressOriginResult) error
	}); ok {
		_ = bounded.SendProgressOriginResult(res)
	} else {
		_ = h.sender.Send(res)
	}
}
func (m *Manager) handleResolveProgressOrigin(raw []byte) {
	var req protocol.ResolveProgressOrigin
	if len(raw) > 4096 || json.Unmarshal(raw, &req) != nil {
		return
	}
	if m.agents != nil {
		m.agents.ResolveProgressOrigin(req)
	}
}

// Tiny attribution replies use the same bounded write path as workspace
// replies: no waiting indefinitely behind an unrelated blocked event write.
func (c *WSClient) SendProgressOriginResult(result protocol.ProgressOriginResult) error {
	return c.sendBoundedWorkspaceReply(result, 5*time.Second)
}
