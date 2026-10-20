package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type hermesQuestion struct {
	ID       string   `json:"qid"`
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
}
type hermesQuestionRequest struct {
	SessionID string           `json:"session_id"`
	RequestID string           `json:"request_id"`
	Question  string           `json:"question"`
	Choices   []string         `json:"choices"`
	Questions []hermesQuestion `json:"questions"`
}

func (r *hermesRun) request(f hermesFrame) {
	refuse := func() {
		_ = r.rpc.write(r.ctx, map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32601, "message": "This host does not support that native request"}})
	}
	if f.Method == "approval" {
		var p hermesApprovalRequest
		if json.Unmarshal(f.Params, &p) != nil || p.SessionID != r.sid {
			refuse()
			return
		}
		r.approve(p, string(f.ID), func(choice string) {
			_ = r.rpc.write(r.ctx, map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": map[string]any{"choice": choice}})
		})
		return
	}
	if f.Method != "clarify" {
		refuse()
		return
	}
	var p hermesQuestionRequest
	if json.Unmarshal(f.Params, &p) != nil || p.SessionID != r.sid {
		refuse()
		return
	}
	r.ask(p, string(f.ID), func(result map[string]any) {
		_ = r.rpc.write(r.ctx, map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": result})
	})
}
func (r *hermesRun) legacyQuestion(raw json.RawMessage) {
	var p hermesQuestionRequest
	if json.Unmarshal(raw, &p) != nil || p.RequestID == "" {
		return
	}
	r.ask(p, p.RequestID, func(result map[string]any) {
		if answers, ok := result["answers"].(map[string]string); ok {
			for _, q := range p.Questions {
				answer, present := answers[q.ID]
				if !present {
					continue
				}
				_, _ = r.rpc.call(r.ctx, "clarify.respond", map[string]any{"session_id": r.sid, "request_id": p.RequestID, "question_id": q.ID, "answer": answer})
			}
			return
		}
		answer, _ := result["answer"].(string)
		_, _ = r.rpc.call(r.ctx, "clarify.respond", map[string]any{"session_id": r.sid, "request_id": p.RequestID, "answer": answer})
	})
}
func (r *hermesRun) ask(p hermesQuestionRequest, key string, reply func(map[string]any)) {
	r.askWith(r.ctx, p, key, reply)
}
func (r *hermesRun) askWith(parent context.Context, p hermesQuestionRequest, key string, reply func(map[string]any)) {
	if r.d.AskUser == nil || !r.active || len(key) > 256 || len(p.Questions) > 10 {
		reply(map[string]any{})
		return
	}
	batch := p.Questions
	if len(batch) == 0 {
		batch = []hermesQuestion{{Question: p.Question, Choices: p.Choices}}
	}
	seen := map[string]bool{}
	for _, q := range batch {
		if len(q.ID) > 256 || len(q.Question) > 16000 || len(q.Choices) > 20 || (len(p.Questions) > 0 && (q.ID == "" || seen[q.ID])) {
			reply(map[string]any{})
			return
		}
		seen[q.ID] = true
		for _, c := range q.Choices {
			if len(c) > 4096 {
				reply(map[string]any{})
				return
			}
		}
	}
	ctx, cancel := context.WithCancel(parent)
	r.questionsMu.Lock()
	if len(r.questions) >= 32 {
		r.questionsMu.Unlock()
		cancel()
		reply(map[string]any{})
		return
	}
	if _, duplicate := r.questions[key]; duplicate {
		r.questionsMu.Unlock()
		cancel()
		return
	}
	token := ccUUID()
	if r.questionTokens == nil {
		r.questionTokens = map[string]string{}
	}
	r.questionTokens[key] = token
	r.questions[key] = cancel
	r.questionsMu.Unlock()
	r.d.emit("status_changed", agent.StatusPayload{Status: "waiting", Reason: "ask"})
	go func() {
		defer cancel()
		result := map[string]any{}
		answers := map[string]string{}
		for _, q := range batch {
			var b strings.Builder
			b.WriteString(q.Question)
			for i, c := range q.Choices {
				fmt.Fprintf(&b, "\n%d. %s", i+1, c)
			}
			answer, err := r.d.AskUser(ctx, b.String())
			if err != nil || ctx.Err() != nil {
				reply(map[string]any{})
				r.questionDone(key, token)
				return
			}
			answers[q.ID] = answer
		}
		if len(p.Questions) > 0 {
			result["answers"] = answers
		} else {
			result["answer"] = answers[""]
		}
		reply(result)
		r.questionDone(key, token)
	}()
}
func (r *hermesRun) questionDone(key, token string) {
	r.rpc.mu.Lock()
	if len(r.rpc.backlog) < 4096 {
		r.rpc.backlog = append(r.rpc.backlog, hermesFrame{questionDone: key, questionToken: token})
		select {
		case r.rpc.wake <- struct{}{}:
		default:
		}
	}
	r.rpc.mu.Unlock()
}
func (r *hermesRun) cancelQuestions() {
	r.questionsMu.Lock()
	defer r.questionsMu.Unlock()
	for id, cancel := range r.questions {
		cancel()
		delete(r.questions, id)
		delete(r.questionTokens, id)
	}
}
func (r *hermesRun) cancelRequest(raw json.RawMessage) {
	var p struct {
		ID        string `json:"id"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	r.questionsMu.Lock()
	defer r.questionsMu.Unlock()
	for _, id := range []string{p.ID, p.RequestID, `"` + p.ID + `"`} {
		if cancel, ok := r.questions[id]; ok {
			cancel()
			delete(r.questions, id)
			delete(r.questionTokens, id)
		}
	}
}

// Contract 6 used response methods rather than server-request errors. Empty
// secret values cancel the privileged prompt.
func (r *hermesRun) refuseLegacyRequest(kind string, raw json.RawMessage) {
	var p struct {
		ID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID == "" || len(p.ID) > 256 {
		return
	}
	params := map[string]any{"session_id": r.sid, "request_id": p.ID}
	key := map[string]string{"sudo.request": "password", "secret.request": "value", "vault.unlock.request": "password", "vault.save_login.request": "login", "vault.code.request": "code"}[kind]
	if key == "" {
		return
	}
	params[key] = ""
	_, _ = r.rpc.call(r.ctx, strings.TrimSuffix(kind, ".request")+".respond", params)
	r.d.emit("error", agent.ErrorPayload{Message: "Hermes requested an unsupported privileged interaction; it was refused", Retryable: true})
}

// Hermes asks before a command it flags as dangerous. Blerg agent sessions run
// Hermes in yolo mode (hermesGatewayBootstrap), so this only fires for what
// yolo does not cover (Hermes's protected-file writes, for one); it is then the
// session owner's decision, asked in the chat. It is not refused unanswered:
// Hermes treats a refused approval request as "the client cannot answer
// approvals" and withdraws it, which stops the agent without anyone deciding.
// Whatever cannot be asked, or is not answered with exactly one offered
// choice, is denied.
type hermesApprovalRequest struct {
	SessionID   string   `json:"session_id"`
	RequestID   string   `json:"request_id"`
	Command     string   `json:"command"`
	Description string   `json:"description"`
	Choices     []string `json:"choices"`
}

var hermesApprovalLabels = map[string]string{
	"once":    "once: allow this one time",
	"session": "session: allow for the rest of this session",
	"always":  "always: always allow commands like this (Hermes saves it to its profile)",
	"deny":    "deny: do not run it",
}

// hermesApprovalChoices keeps only the choices Hermes offered that Blerg
// knows, in Hermes's order. It never adds one.
func hermesApprovalChoices(offered []string) []string {
	choices := []string{}
	seen := map[string]bool{}
	for _, c := range offered {
		if _, known := hermesApprovalLabels[c]; known && !seen[c] {
			seen[c] = true
			choices = append(choices, c)
		}
	}
	return choices
}

// hermesApprovalChoice maps the person's answer to one offered choice. Only an
// exact single token counts (the choice word, its number, or its whole label,
// case-insensitive and trimmed); anything else is a deny, because approval is
// never inferred from prose such as "always deny".
func hermesApprovalChoice(answer string, choices []string) string {
	a := strings.ToLower(strings.TrimSpace(answer))
	for i, c := range choices {
		if a == c || a == strconv.Itoa(i+1) || a == strings.ToLower(hermesApprovalLabels[c]) {
			return c
		}
	}
	return "deny"
}

// hermesFence wraps command-controlled text in a Markdown code fence longer
// than any backtick run inside it, so the text cannot close the fence, format
// itself, or pose as Blerg's own choice list.
func hermesFence(text string) string {
	longest, run := 0, 0
	for _, c := range text {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "\n" + text + "\n" + fence
}

func (r *hermesRun) approve(p hermesApprovalRequest, key string, reply func(string)) {
	choices := hermesApprovalChoices(p.Choices)
	if len(choices) == 0 {
		reply("deny")
		return
	}
	var b strings.Builder
	b.WriteString("Hermes asks for approval to run this command:\n\n")
	b.WriteString(hermesFence(p.Command))
	if p.Description != "" {
		b.WriteString("\n\nHermes's reason:\n\n")
		b.WriteString(hermesFence(p.Description))
	}
	b.WriteString("\n\nOnly the session owner can answer. Reply with exactly one choice, by word or number; any other reply denies it:")
	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = hermesApprovalLabels[c]
	}
	r.askWith(withOwnerOnlyAsk(r.ctx), hermesQuestionRequest{Question: b.String(), Choices: labels}, key, func(result map[string]any) {
		answer, _ := result["answer"].(string)
		reply(hermesApprovalChoice(answer, choices))
	})
}
func (r *hermesRun) legacyApproval(raw json.RawMessage) {
	var p hermesApprovalRequest
	if json.Unmarshal(raw, &p) != nil || p.RequestID == "" || len(p.RequestID) > 256 {
		return
	}
	r.approve(p, p.RequestID, func(choice string) {
		_, _ = r.rpc.call(r.ctx, "approval.respond", map[string]any{"session_id": r.sid, "request_id": p.RequestID, "choice": choice})
	})
}
