package server

// Live reasoning is display metadata, never billing or session lifecycle state.
// Keep it across browser refreshes without persisting per-token events.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

const maxReasoningSessions = 256
const maxReasoningScopes = 256

type reasoningSession struct {
	scopes  map[string]livestate.Reasoning
	touched uint64
}
type reasoningCache struct {
	mu       sync.Mutex
	sessions map[string]*reasoningSession
	clock    uint64
}

var thinkingKindPrefix = []byte(`"thinking_`)

func (c *reasoningCache) observe(sessionID string, data []byte) bool {
	// Every agent event passes through here (tool results included). Only a thinking event can
	// open a scope, and any other event matters only to a session with one open: decode nothing
	// else.
	if !bytes.Contains(data, thinkingKindPrefix) {
		c.mu.Lock()
		_, open := c.sessions[sessionID]
		c.mu.Unlock()
		if !open {
			return false
		}
	}
	var msg struct {
		SessionID string `json:"session_id"`
		Kind      string `json:"kind"`
		Ts        string `json:"ts"`
		Transient bool   `json:"transient"`
		Payload   struct {
			Parent     string `json:"parent_call_id"`
			Child      string `json:"child_id"`
			Tokens     *int   `json:"tokens"`
			Status     string `json:"status"`
			Source     string `json:"source"`
			Background bool   `json:"background"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &msg) != nil || msg.SessionID != sessionID {
		return false
	}
	switch msg.Kind {
	case "thinking_started", "thinking_progress":
		if !msg.Transient {
			return false
		}
	case "thinking_done", "assistant_thinking", "assistant_text", "tool_call", "turn_done", "status_changed", "user_message", "error", "subagent_done":
	default:
		return false
	}
	p := msg.Payload
	if len(p.Parent) > 256 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	session := c.sessions[sessionID]
	key := p.Parent
	opening := msg.Transient && (msg.Kind == "thinking_started" || msg.Kind == "thinking_progress")
	if opening {
		if p.Tokens != nil && (*p.Tokens < 0 || int64(*p.Tokens) > 1<<40) {
			return false
		}
		if msg.Kind == "thinking_progress" && p.Tokens == nil {
			return false
		}
		at, err := time.Parse(time.RFC3339Nano, msg.Ts)
		if err != nil {
			return false
		}
		if session == nil {
			if c.sessions == nil {
				c.sessions = map[string]*reasoningSession{}
			}
			if len(c.sessions) >= maxReasoningSessions {
				oldest := ""
				age := ^uint64(0)
				for id, s := range c.sessions {
					if s.touched < age {
						oldest, age = id, s.touched
					}
				}
				delete(c.sessions, oldest)
			}
			session = &reasoningSession{scopes: map[string]livestate.Reasoning{}}
			c.sessions[sessionID] = session
		}
		old, known := session.scopes[key]
		next := livestate.Reasoning{Since: at.UnixMilli(), Tokens: p.Tokens}
		if msg.Kind == "thinking_progress" && known {
			next.Since = old.Since
			if old.Tokens != nil && *old.Tokens >= *p.Tokens {
				return false
			}
		}
		if !known && len(session.scopes) >= maxReasoningScopes {
			return false
		}
		session.scopes[key] = next
		c.clock++
		session.touched = c.clock
		return true
	}
	if session == nil {
		return false
	}
	if msg.Kind == "status_changed" && p.Status == "running" || msg.Kind == "user_message" && p.Source == "system" {
		return false
	}
	if msg.Kind == "subagent_done" {
		if p.Background {
			return false
		}
		key = p.Child
	}
	if _, ok := session.scopes[key]; !ok {
		return false
	}
	delete(session.scopes, key)
	if len(session.scopes) == 0 {
		delete(c.sessions, sessionID)
	}
	return true
}

// forget drops the open scopes of these sessions (their daemon restarted: whatever they were
// thinking about is gone, and a resumed turn opens a fresh scope).
func (c *reasoningCache) forget(sessionIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range sessionIDs {
		delete(c.sessions, id)
	}
}

func (h *Hub) observeReasoning(sessionID string, data []byte) {
	if h.reasoning.observe(sessionID, data) {
		h.ChildActivity(sessionID)
	}
}

// A snapshot uses exactly the rows the existing visibility check returned. Hide
// counts with the same per-owner rule as usage; labels/clocks remain available.
func (h *Hub) liveReasoning(sid string, rows []db.ChildLiveRow, account string, showTokens, admin bool) map[string]map[string]livestate.Reasoning {
	h.reasoning.mu.Lock()
	defer h.reasoning.mu.Unlock()
	out := map[string]map[string]livestate.Reasoning{}
	copySession := func(id string, counts bool) {
		s := h.reasoning.sessions[id]
		if s == nil {
			return
		}
		scopes := map[string]livestate.Reasoning{}
		for key, p := range s.scopes {
			if !counts {
				p.Tokens = nil
			} else if p.Tokens != nil {
				n := *p.Tokens
				p.Tokens = &n
			}
			scopes[key] = p
		}
		out[id] = scopes
	}
	copySession(sid, showTokens)
	for _, row := range rows {
		if row.Status == "running" && row.EndedAt == nil {
			copySession(row.ID, showTokens && (admin || account != "" && account == row.Owner))
		}
	}
	return out
}

func applyReasoning(st *livestate.State, children []liveChild, sid string, snapshot map[string]map[string]livestate.Reasoning) {
	for i := range st.Subagents {
		sub := &st.Subagents[i]
		if p, ok := snapshot[sid][sub.Key]; ok {
			sub.Reasoning = &p
		}
	}
	for i := range children {
		child := &children[i]
		// A child with an open question says so; its reasoning clock does not cover it.
		if child.Status != "running" || child.EndedAt != nil || child.Asking {
			continue
		}
		p, ok := snapshot[child.ID][""]
		if !ok {
			continue
		}
		child.Activity = "Thinking…"
		at := time.UnixMilli(p.Since)
		child.ActivitySince = &at
		if p.Tokens != nil {
			child.Activity = fmt.Sprintf("Thinking… %d tokens", *p.Tokens)
			child.ActivitySince = nil
		}
	}
}
