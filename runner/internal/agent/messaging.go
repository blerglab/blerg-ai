package agent

import (
	"context"
	"encoding/json"
	"errors"
)

// Messenger delivers session→user messages. Phase 1b implements it against
// the server's messages table; tests use a fake.
type Messenger interface {
	SendUpdate(body string) error
	SendNote(body string) error
	// SendAsk registers the question and returns a message ID; the answer
	// arrives via Loop.Enqueue (any user message while blocked is treated as
	// the answer).
	SendAsk(body string) (msgID string, err error)
}

// installMessagingTools registers update/note/ask on the loop's registry.
// Requires cfg.Messenger. Called by the host at session setup.
func (l *Loop) installMessagingTools() {
	l.cfg.Registry = NewRegistry(append(
		[]Tool{updateTool{l}, noteTool{l}, askTool{l}},
		registryTools(l.cfg.Registry)...,
	)...)
}

// registryTools flattens a registry back to a slice in registration order.
func registryTools(r *Registry) []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

type updateTool struct{ l *Loop }

func (t updateTool) Def() ToolDef {
	return ToolDef{Name: "update", Description: "Send a brief progress update to the user. Non-blocking; use sparingly at milestones.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string"}},"required":["body"]}`)}
}
func (t updateTool) Mutating() bool { return false }
func (t updateTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	var args struct{ Body string }
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	t.l.emit("update", MessagingPayload{Body: args.Body})
	if err := t.l.cfg.Messenger.SendUpdate(args.Body); err != nil {
		return "", err
	}
	return "update sent", nil
}

type noteTool struct{ l *Loop }

func (t noteTool) Def() ToolDef {
	return ToolDef{Name: "note", Description: "Send a non-blocking note to the user; their reply (if any) arrives as a later message.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string"}},"required":["body"]}`)}
}
func (t noteTool) Mutating() bool { return false }
func (t noteTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	var args struct{ Body string }
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	t.l.emit("note", MessagingPayload{Body: args.Body})
	if err := t.l.cfg.Messenger.SendNote(args.Body); err != nil {
		return "", err
	}
	return "note sent", nil
}

type askTool struct{ l *Loop }

func (t askTool) Def() ToolDef {
	return ToolDef{Name: "ask", Description: "Ask the user a BLOCKING question. The turn waits until they answer. At most one ask at a time.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`)}
}
func (t askTool) Mutating() bool { return false }
func (t askTool) Execute(ctx context.Context, in json.RawMessage) (string, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	l := t.l
	l.mu.Lock()
	if l.askCh != nil {
		l.mu.Unlock()
		return "", errors.New("another ask is already waiting for an answer; ask one question at a time")
	}
	ch := make(chan string, 1)
	l.askCh = ch
	l.mu.Unlock()

	l.emit("ask", MessagingPayload{Body: args.Question})
	if _, err := l.cfg.Messenger.SendAsk(args.Question); err != nil {
		l.clearAsk()
		return "", err
	}
	l.emit("status_changed", StatusPayload{Status: "waiting", Reason: "blocked on ask"})
	defer l.emit("status_changed", StatusPayload{Status: "running", Reason: "ask answered"})
	select {
	case ans := <-ch:
		l.clearAsk()
		return ans, nil
	case <-ctx.Done():
		l.clearAsk()
		return "", errors.New("ask interrupted")
	}
}

func (l *Loop) clearAsk() {
	l.mu.Lock()
	ch := l.askCh
	l.askCh = nil
	if ch != nil {
		// A message may have raced into the buffer between the ask's receive
		// (or interrupt) and this teardown — preserve it as a queued message.
		select {
		case leftover := <-ch:
			l.queue = append(l.queue, queuedMsg{leftover, "chat"})
		default:
		}
	}
	l.mu.Unlock()
}

// InstallMessagingTools is the exported host entry point for registering the
// update/note/ask tools. Requires cfg.Messenger.
func (l *Loop) InstallMessagingTools() { l.installMessagingTools() }

// AddTool registers an additional tool on the loop's registry. Host-side
// setup only — not safe once Run has started processing turns.
func (l *Loop) AddTool(t Tool) {
	l.cfg.Registry = NewRegistry(append(registryTools(l.cfg.Registry), t)...)
}

// ToolDefs lists the loop's tools as the model sees them. Host-side setup
// only, like AddTool.
func (l *Loop) ToolDefs() []ToolDef { return l.cfg.Registry.Defs() }
