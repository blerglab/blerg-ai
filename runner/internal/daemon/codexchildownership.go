package daemon

import (
	"context"
	"encoding/json"
	"time"
)

type codexChildMetadata struct {
	ReasoningEffort                    *string `json:"reasoningEffort"`
	ID, ParentThreadID, Model, Preview string
	Source                             json.RawMessage
}

func (m codexChildMetadata) parent() string {
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		}
	}
	_ = json.Unmarshal(m.Source, &source)
	parent := source.Subagent.ThreadSpawn.Parent
	if m.ParentThreadID != "" {
		if parent != "" && parent != m.ParentThreadID {
			return ""
		}
		return m.ParentThreadID
	}
	return parent
}

type codexPendingChild struct {
	events []codexRPC
	bytes  int
}

// Root activity identifies a candidate, not ownership. Buffer only a bounded
// tail while native metadata proves ancestry; foreign/stale requests never
// reach a question callback. Replaying one child's FIFO preserves turn ordering.
func (a *codexApp) deferChildEvent(parent context.Context, child string, event codexRPC) {
	if !validCodexThreadID(child) {
		a.refuseChildEvents(parent, []codexRPC{event})
		return
	}
	a.mu.Lock()
	if a.pendingChildren == nil {
		a.pendingChildren = map[string]*codexPendingChild{}
	}
	pending := a.pendingChildren[child]
	if (pending == nil && len(a.pendingChildren) >= 16) || (pending != nil && len(pending.events) >= 128) || a.pendingChildBytes+len(event.Params) > 8<<20 {
		a.mu.Unlock()
		a.refuseChildEvents(parent, []codexRPC{event})
		return
	}
	fresh := pending == nil
	if fresh {
		pending = &codexPendingChild{}
		a.pendingChildren[child] = pending
	}
	pending.events = append(pending.events, event)
	pending.bytes += len(event.Params)
	a.pendingChildBytes += len(event.Params)
	a.mu.Unlock()
	if !fresh {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		path, proven := a.proveOwnedChild(ctx, child)
		proof := &codexChildProof{child: child, path: path, proven: proven}
		a.mu.Lock()
		if len(a.backlog) < 4096 {
			a.backlog = append([]codexRPC{{childProof: proof}}, a.backlog...)
			select {
			case a.events <- struct{}{}:
			default:
			}
			a.mu.Unlock()
			return
		}
		pending := a.pendingChildren[child]
		delete(a.pendingChildren, child)
		a.pendingChildBytes -= pending.bytes
		a.mu.Unlock()
		a.refuseChildEvents(ctx, pending.events)
	}()
}

func (a *codexApp) refuseChildEvents(ctx context.Context, events []codexRPC) {
	for _, event := range events {
		if len(event.ID) > 0 && event.Method != "" {
			_ = a.writeContext(ctx, map[string]any{"id": event.ID, "error": map[string]any{"code": -32602, "message": "Child request ownership could not be verified"}})
		}
	}
}

func (a *codexApp) proveOwnedChild(ctx context.Context, child string) ([]OpenSubagent, bool) {
	root := a.threadID()
	if !validCodexThreadID(child) || child == root {
		return nil, false
	}
	if a.d.childKnown(child) {
		return nil, true
	}
	path := []OpenSubagent{}
	seen := map[string]bool{}
	current := child
	for depth := 0; depth < 16; depth++ {
		if current == root || a.d.childKnown(current) {
			return path, true
		}
		if seen[current] || !validCodexThreadID(current) {
			return nil, false
		}
		seen[current] = true
		raw, err := a.callContext(ctx, "thread/read", map[string]any{"threadId": current, "includeTurns": false})
		if err != nil {
			return nil, false
		}
		var reply struct{ Thread codexChildMetadata }
		if json.Unmarshal(raw, &reply) != nil || reply.Thread.ID != current {
			return nil, false
		}
		parent := reply.Thread.parent()
		if !validCodexThreadID(parent) {
			return nil, false
		}
		path = append(path, OpenSubagent{AgentID: current, AgentType: "Codex", Model: reply.Thread.Model, Summary: reply.Thread.Preview})
		current = parent
	}
	return nil, false
}

type codexChildProof struct {
	child  string
	path   []OpenSubagent
	proven bool
}

func (a *codexApp) childNeedsDeferral(child string) bool {
	a.mu.Lock()
	pending := a.pendingChildren[child] != nil
	a.mu.Unlock()
	return pending || !a.d.childKnown(child)
}

// Admit ownership and replay on the app loop, so a newly-proven child cannot
// overtake its own buffered turn/start or question with a newer notification.
func (a *codexApp) acceptChildProof(ctx context.Context, proof *codexChildProof) {
	a.mu.Lock()
	pending := a.pendingChildren[proof.child]
	delete(a.pendingChildren, proof.child)
	if pending != nil {
		a.pendingChildBytes -= pending.bytes
	}
	a.mu.Unlock()
	if pending == nil {
		return
	}
	if proof.proven {
		a.d.seedKnownChildren(proof.path)
		a.d.updateOpenWork("subagent_update", nil)
	}
	owned := proof.proven && a.d.childKnown(proof.child)
	a.mu.Lock()
	accepted := owned && len(a.backlog)+len(pending.events) <= 4096 && a.backlogBytes+pending.bytes <= 64<<20
	if accepted {
		a.backlog = append(append([]codexRPC(nil), pending.events...), a.backlog...)
		a.backlogBytes += pending.bytes
		select {
		case a.events <- struct{}{}:
		default:
		}
	}
	a.mu.Unlock()
	if !accepted {
		a.refuseChildEvents(ctx, pending.events)
	}
}
