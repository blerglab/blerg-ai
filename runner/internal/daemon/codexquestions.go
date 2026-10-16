package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type codexUserQuestion struct {
	ID       string                                `json:"id"`
	Header   string                                `json:"header"`
	Question string                                `json:"question"`
	IsSecret bool                                  `json:"isSecret"`
	Options  []struct{ Label, Description string } `json:"options"`
}

func (a *codexApp) userQuestions(parent context.Context, ev codexRPC) {
	var request struct {
		ThreadID   string              `json:"threadId"`
		TurnID     string              `json:"turnId"`
		Questions  []codexUserQuestion `json:"questions"`
		IsBlocking *bool               `json:"isBlocking"`
	}
	defer func() { a.forgetPendingQuestion(request.ThreadID, ev.ID) }()
	refuse := func(message string) {
		if a.questionRequestResolved(request.ThreadID, ev.ID) {
			return
		}
		_ = a.writeContext(parent, map[string]any{"id": ev.ID, "error": map[string]any{"code": -32602, "message": message}})
	}
	if json.Unmarshal(ev.Params, &request) != nil || len(request.Questions) == 0 || len(request.Questions) > 10 || len(ev.ID) > 256 || a.d.AskUser == nil {
		refuse("User questions are not available")
		return
	}
	if a.questionRequestResolved(request.ThreadID, ev.ID) {
		return
	}
	seen := map[string]bool{}
	for _, q := range request.Questions {
		if seen[q.ID] {
			refuse("Duplicate question identifier")
			return
		}
		seen[q.ID] = true
		if q.IsSecret {
			refuse("Secret questions must use a credential setup flow")
			return
		}
		if q.ID == "" || len(q.ID) > 128 || len(q.Question) > 16000 || len(q.Options) > 20 {
			refuse("Invalid user question")
			return
		}
		for _, option := range q.Options {
			if len(option.Label) > 512 || len(option.Description) > 4096 {
				refuse("Invalid question option")
				return
			}
		}
	}
	root := a.threadID()
	child := request.ThreadID != root
	if child && !a.d.childKnown(request.ThreadID) {
		refuse("User questions are not available")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.mu.Lock()
	blocking := request.IsBlocking == nil || *request.IsBlocking
	valid := request.TurnID != "" && !a.interruptRequested && !a.pendingQuestions[questionRequestKey(request.ThreadID, ev.ID)]
	if child {
		valid = valid && a.childTurns[request.ThreadID] == request.TurnID && a.interruptedChildren[request.ThreadID] != request.TurnID
	} else {
		valid = valid && request.TurnID == a.turn
	}
	key := string(ev.ID)
	if previous, exists := a.dynamicCalls[key]; exists {
		a.mu.Unlock()
		previous.cancel()
		refuse("Duplicate user question request")
		return
	}
	if len(a.dynamicCalls) >= 32 {
		valid = false
	}
	scope := request.TurnID
	if child {
		scope = request.ThreadID + "\x00" + request.TurnID
	}
	if valid {
		if a.dynamicCalls == nil {
			a.dynamicCalls = map[string]codexDynamicCall{}
		}
		a.dynamicCalls[key] = codexDynamicCall{thread: request.ThreadID, turn: request.TurnID, cancel: cancel}
		if blocking {
			if a.blockingQuestions == nil {
				a.blockingQuestions = map[string]int{}
			}
			a.blockingQuestions[scope]++
			a.queueQuestionStatusLocked(a.turn, "waiting")
		}
	}
	a.mu.Unlock()
	if !valid {
		refuse("The question's turn is no longer active")
		return
	}
	defer func() { a.mu.Lock(); delete(a.dynamicCalls, key); a.mu.Unlock() }()
	defer func() {
		if !blocking {
			return
		}
		a.mu.Lock()
		a.blockingQuestions[scope]--
		remaining := a.blockingQuestions[scope]
		if remaining == 0 {
			delete(a.blockingQuestions, scope)
		}
		active := a.ctx.Err() == nil && a.blockingQuestionCountLocked() == 0 && !a.interruptRequested
		if active {
			status := "running"
			if a.turn == "" {
				status = "idle"
			}
			a.queueQuestionStatusLocked(a.turn, status)
		}
		a.mu.Unlock()
	}()
	if child {
		callID := a.d.childCallID(request.ThreadID)
		ctx = context.WithValue(ctx, codexQuestionOriginKey{}, callID)
	}
	answers := map[string]any{}
	for _, q := range request.Questions {
		var body strings.Builder
		if child {
			fmt.Fprintf(&body, "Codex subagent %.8s asks:\n\n", a.d.childCallID(request.ThreadID))
		}
		body.WriteString(q.Question)
		for i, option := range q.Options {
			fmt.Fprintf(&body, "\n%d. %s", i+1, option.Label)
			if option.Description != "" {
				body.WriteString(" — " + option.Description)
			}
		}
		answer, err := a.d.AskUser(ctx, body.String())
		if err != nil || ctx.Err() != nil {
			refuse("The user question was canceled")
			return
		}
		answers[q.ID] = map[string]any{"answers": []string{answer}}
	}
	_ = a.writeContext(ctx, map[string]any{"id": ev.ID, "result": map[string]any{"answers": answers}})
}

// Queue display transitions through the app loop so a blocked websocket can
// never hold the RPC/lifecycle mutex and prevent interruption or responses.
func (a *codexApp) queueQuestionStatusLocked(turn, status string) {
	if len(a.backlog) >= 4096 {
		return
	} // Transient status is safe to coalesce at capacity.
	a.backlog = append(a.backlog, codexRPC{questionStatus: &codexQuestionStatus{turn: turn, status: status}})
	select {
	case a.events <- struct{}{}:
	default:
	}
}

// All blocking questions contribute to the session's waiting state, including
// owned children that continue after their parent's turn ends.
func (a *codexApp) blockingQuestionCountLocked() int {
	total := 0
	for _, n := range a.blockingQuestions {
		total += n
	}
	return total
}

type codexQuestionOriginKey struct{}

func (a *codexApp) startChildTurn(thread, turn string) {
	if turn == "" {
		return
	}
	a.mu.Lock()
	if a.childTurns == nil {
		a.childTurns = map[string]string{}
	}
	old := a.childTurns[thread]
	a.childTurns[thread] = turn
	if old != turn {
		delete(a.interruptedChildren, thread)
	}
	a.mu.Unlock()
	if old != "" && old != turn {
		a.cancelDynamic(thread, old)
	}
}

func (a *codexApp) endChildThread(thread string) {
	a.mu.Lock()
	delete(a.childTurns, thread)
	delete(a.interruptedChildren, thread)
	a.mu.Unlock()
	a.cancelDynamic(thread, "")
}

// The pinned CLI exposes this tool only to its root thread. Child questions
// therefore use the parent as their user-facing broker; the RPC handler also
// supports owned child requests from runtimes that expose them in the future.
const codexQuestionHandoffInstructions = "Native request_user_input is available only to the root thread in this Codex runtime. A subagent that needs a person's input must send the question to its parent through collaboration messaging and wait for the answer. The parent asks through request_user_input, identifies the requesting subagent in the question, and forwards the answer back to that child. Do not ask for credentials or secrets through ordinary chat questions."

func questionRequestKey(thread string, id json.RawMessage) string {
	return thread + "\x00" + string(id)
}

// Dispatch records pending registration before starting a callback goroutine.
// A native resolution that wins that race must never open a stale user card.
func (a *codexApp) markQuestionPending(thread string, id json.RawMessage) bool {
	if !validCodexThreadID(thread) || len(id) == 0 || len(id) > 256 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingQuestions == nil {
		a.pendingQuestions = map[string]bool{}
	}
	key := questionRequestKey(thread, id)
	if _, exists := a.pendingQuestions[key]; exists || len(a.pendingQuestions) >= 32 {
		return false
	}
	a.pendingQuestions[key] = false
	return true
}

func (a *codexApp) forgetPendingQuestion(thread string, id json.RawMessage) {
	a.mu.Lock()
	delete(a.pendingQuestions, questionRequestKey(thread, id))
	a.mu.Unlock()
}

func (a *codexApp) questionRequestResolved(thread string, id json.RawMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	call := a.dynamicCalls[string(id)]
	return a.pendingQuestions[questionRequestKey(thread, id)] || (call.thread == thread && call.resolved)
}

func (a *codexApp) resolveQuestionRequest(thread string, id json.RawMessage) {
	if !validCodexThreadID(thread) || len(id) == 0 || len(id) > 256 {
		return
	}
	a.mu.Lock()
	key := questionRequestKey(thread, id)
	if _, pending := a.pendingQuestions[key]; pending {
		a.pendingQuestions[key] = true
	}
	call, exists := a.dynamicCalls[string(id)]
	if exists && call.thread == thread {
		call.resolved = true
		a.dynamicCalls[string(id)] = call
	}
	a.mu.Unlock()
	if exists && call.thread == thread {
		call.cancel()
	}
}
