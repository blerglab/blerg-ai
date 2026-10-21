package daemon

import (
	"context"
	"encoding/json"
	"errors"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type hermesUsage struct {
	Model      string `json:"model"`
	Input      int    `json:"input"`
	Output     int    `json:"output"`
	CacheRead  int    `json:"cache_read"`
	CacheWrite int    `json:"cache_write"`
}

func (u hermesUsage) delta(base hermesUsage) agent.Usage {
	return agent.Usage{InputTokens: max(0, u.Input-base.Input), OutputTokens: max(0, u.Output-base.Output), CacheReadTokens: max(0, u.CacheRead-base.CacheRead), CacheWriteTokens: max(0, u.CacheWrite-base.CacheWrite)}
}

type hermesRun struct {
	carried agent.Usage
	tasks   map[string]agent.BackgroundTaskStartedPayload

	childDone      map[string]bool
	childStarted   map[string]bool
	usageSeen      bool
	usagePublished bool
	lastUsage      protocol.UsageUpdatedPayload

	d                                    *hermesDriver
	rpc                                  *hermesRPC
	ctx                                  context.Context
	sid                                  string
	active                               bool
	actualModel, actualEffort, turnModel string
	binding                              hermesModelRoute
	turnBinding                          hermesModelRoute
	actualProvider                       string
	routeConfirmed                       bool
	ambiguous                            bool
	total, baseline                      hermesUsage
	thought                              strings.Builder
	thinking                             bool
	reasoning                            hermesReasoning
	childReasoningState                  map[string]*hermesReasoning
	tools                                map[string]bool
	children                             map[string]agent.SubagentPayload
	questionsMu                          sync.Mutex
	questionTokens                       map[string]string
	questions                            map[string]context.CancelFunc
	contract                             int
	agentReady                           bool
}

func newHermesRun(ctx context.Context, d *hermesDriver, rpc *hermesRPC) *hermesRun {
	return &hermesRun{d: d, rpc: rpc, ctx: ctx, tools: map[string]bool{}, children: map[string]agent.SubagentPayload{}, childDone: map[string]bool{}, childStarted: map[string]bool{}, questions: map[string]context.CancelFunc{}, questionTokens: map[string]string{}}
}
func (r *hermesRun) begin() {
	r.usagePublished = false
	r.d.emit(protocol.UsageUpdatedKind, protocol.UsageUpdatedPayload{Scope: "turn"})
	r.active = true
	r.baseline = r.total
	r.turnModel = r.actualModel
	r.turnBinding = r.binding
	r.ambiguous = false
	r.usageSeen = false
	r.carried = agent.Usage{}
	r.thought.Reset()
	r.endThinking()
}
func (r *hermesRun) info(raw json.RawMessage) {
	var p struct {
		Model    string              `json:"model"`
		Provider string              `json:"provider"`
		Effort   string              `json:"reasoning_effort"`
		Version  string              `json:"version"`
		Contract int                 `json:"desktop_contract"`
		Tools    map[string][]string `json:"tools"`
		Skills   map[string][]string `json:"skills"`
		Usage    json.RawMessage     `json:"usage"`
		Lazy     bool                `json:"lazy"`
		StoredID string              `json:"stored_session_id"`
		MCP      json.RawMessage     `json:"mcp_servers"`
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &p) != nil {
		return
	}
	if p.StoredID != "" && nativeSessionIDPattern.MatchString(p.StoredID) {
		r.d.mu.Lock()
		changed := r.d.nativeID != p.StoredID
		r.d.nativeID = p.StoredID
		r.d.mu.Unlock()
		if changed && r.d.onSessionID != nil {
			r.d.onSessionID(p.StoredID)
		}
	}
	if p.Contract != 0 {
		r.contract = p.Contract
	}
	if len(p.Model) > 256 || len(p.Provider) > 128 || len(p.Effort) > 32 || len(p.Version) > 128 {
		return
	}
	if r.binding.Provider != "" {
		r.routeConfirmed = !p.Lazy && p.Provider == r.binding.Provider && p.Model == r.binding.Model
		if p.Provider != r.binding.Provider || p.Model != r.binding.Model {
			return
		}
		p.Model = r.binding.Selector
		if r.routeConfirmed {
			r.d.mu.Lock()
			changed := r.d.routeFingerprint != r.binding.fingerprint
			r.d.routeFingerprint = r.binding.fingerprint
			id := r.d.nativeID
			r.d.mu.Unlock()
			if changed && id != "" && r.d.onSessionID != nil {
				r.d.onSessionID(id)
			}
		}
	}
	r.actualProvider = p.Provider
	if p.Model != "" {
		if r.active && (p.Model != r.turnModel || (r.binding.Provider != "" && (r.binding.Provider != r.turnBinding.Provider || r.binding.fingerprint != r.turnBinding.fingerprint))) {
			r.ambiguous = true
		}
		r.actualModel = p.Model
		r.d.mu.Lock()
		if r.d.model == "" {
			r.d.model = p.Model
		}
		if r.d.effort == "" && !p.Lazy {
			r.d.effort = p.Effort
		}
		requestedModel := r.d.model
		r.d.mu.Unlock()
		r.actualEffort = p.Effort
		if requestedModel == p.Model {
			r.d.emit("model_changed", agent.ModelChangedPayload{Model: p.Model, Effort: p.Effort, Source: "start"})
		}
	}
	r.reportUsage(p.Usage)
	if !p.Lazy {
		r.agentReady = true
		caps := protocol.CapabilitiesPayload{Engine: "hermes", EngineName: "Hermes", Model: p.Model, Version: p.Version, PermissionMode: "bypassPermissions", Note: "Observed native gateway tools and skills. Command approval prompts are bypassed (Hermes yolo mode); any approval Hermes still raises is asked of the session owner. Restricted MCP/cron execution and exact cache-token counts are not advertised."}
		for _, group := range []struct {
			kind   string
			values map[string][]string
		}{{"tools", p.Tools}, {"skills", p.Skills}} {
			if group.values == nil {
				continue
			}
			g := protocol.CapabilityGroup{ID: group.kind, Label: group.kind, Items: []protocol.CapabilityItem{}}
			keys := []string{}
			for key := range group.values {
				if len(key) > 256 {
					continue
				}
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				for _, name := range group.values[key] {
					if len(name) > 512 {
						continue
					}
					if len(g.Items) >= 500 {
						break
					}
					g.Items = append(g.Items, protocol.CapabilityItem{Name: name, Source: key})
				}
			}
			caps.Groups = append(caps.Groups, g)
		}
		if len(p.MCP) > 0 {
			raw, _ := json.Marshal(map[string]json.RawMessage{"servers": p.MCP})
			if probe, err := hermesMCPRows(raw, 0, true); err == nil {
				group := protocol.CapabilityGroup{ID: "mcp", Label: "MCP servers", Items: []protocol.CapabilityItem{}}
				for _, server := range probe.Servers {
					group.Items = append(group.Items, protocol.CapabilityItem{Name: server.Name, Status: server.Status, Source: server.Source})
				}
				caps.Groups = append(caps.Groups, group)
			}
		}
		r.d.emit("capabilities", caps)
	}
}
func (r *hermesRun) observeUsage(u hermesUsage) {
	if r.active && r.turnBinding.Provider != "" && u.Model == r.turnBinding.Model {
		u.Model = r.turnBinding.Selector
	}
	if !hermesCounter(u.Input) || !hermesCounter(u.Output) || !hermesCounter(u.CacheRead) || !hermesCounter(u.CacheWrite) {
		return
	}
	if r.active {
		r.usageSeen = true
	}
	if len(u.Model) > 256 {
		return
	}
	if r.active && u.Model != "" && r.turnModel != "" && u.Model != r.turnModel {
		r.ambiguous = true
	}
	// Lifetime counters can reset after a native agent rebuild. Never turn a
	// reset into a negative bill or invent cache counts from a rounded percent.
	if u.Input < r.total.Input || u.Output < r.total.Output {
		if r.active {
			r.carried = hermesAddUsage(r.carried, r.total.delta(r.baseline))
		}
		r.baseline = hermesUsage{}
		if r.active {
			r.ambiguous = true
		}
	}
	r.total = u
}
func (r *hermesRun) applySettings() bool {
	r.d.mu.Lock()
	model, effort := r.d.model, r.d.effort
	r.d.mu.Unlock()
	if model != "" && model != r.actualModel {
		route, err := r.d.resolveModel(r.ctx, model)
		if err != nil {
			r.d.emit("error", agent.ErrorPayload{Message: err.Error(), Retryable: true})
			return false
		}
		switchValue := "--session " + route.Model
		if route.Provider != "" {
			switchValue = "--session --provider " + route.Provider + " " + route.Model
		}
		previous := r.binding
		previousConfirmed := r.routeConfirmed
		r.binding = route
		r.routeConfirmed = false
		raw, err := r.rpc.call(r.ctx, "config.set", map[string]any{"session_id": r.sid, "key": "model", "value": switchValue, "scope": "session"})
		var ack struct {
			Confirm bool `json:"confirm_required"`
		}
		_ = json.Unmarshal(raw, &ack)
		if err != nil || ack.Confirm {
			r.binding = previous
			r.routeConfirmed = previousConfirmed
			r.d.emit("error", agent.ErrorPayload{Message: "Hermes did not apply the model change; native confirmation or a supported model is required", Retryable: true})
			r.d.mu.Lock()
			if r.d.model == model {
				r.d.model = r.actualModel
			}
			r.d.mu.Unlock()
			r.d.emit("model_changed", agent.ModelChangedPayload{Model: r.actualModel, Effort: r.actualEffort, Source: "confirmation"})
			if err != nil {
				var rejection hermesRejection
				if !errors.As(err, &rejection) {
					return false
				}
			}
		}
	}

	if effort != "" && effort != r.actualEffort {
		if _, err := r.rpc.call(r.ctx, "config.set", map[string]any{"session_id": r.sid, "key": "reasoning", "value": effort, "scope": "session"}); err != nil {
			r.d.emit("error", agent.ErrorPayload{Message: "Hermes effort change was not confirmed", Retryable: true})
			return false
		}
	}
	return true
}
func (r *hermesRun) finish(stop, text string) {
	if !r.active {
		return
	}
	r.active = false
	r.cancelQuestions()
	r.d.mu.Lock()
	r.d.active = false
	r.d.interruptRequested = false
	r.d.mu.Unlock()
	if r.thought.Len() > 0 {
		r.d.emit("assistant_thinking", agent.AssistantThinkingPayload{Text: r.thought.String()})
		r.thought.Reset()
	}
	if r.thinking {
		r.d.emit(agent.KindThinkingDone, agent.ThinkingPayload{})
	}
	r.endThinking()
	if text != "" {
		r.d.emit("assistant_text", agent.AssistantTextPayload{Text: text, Done: true})
	}
	for call := range r.tools {
		r.d.emit("tool_result", agent.ToolResultPayload{CallID: call, IsError: true, Output: "Native tool did not report completion before the turn ended"})
		delete(r.tools, call)
	}
	model := r.turnModel
	if r.ambiguous {
		model = ""
	}
	usage := hermesAddUsage(r.carried, r.total.delta(r.baseline))
	reported := r.usageSeen
	r.d.emit("turn_done", agent.TurnDonePayload{UsageReported: &reported, StopReason: stop, Model: model, Usage: usage, UsageByModel: func() map[string]agent.Usage {
		if !r.usageSeen {
			return nil
		}
		return map[string]agent.Usage{model: usage}
	}()})
	r.d.emit("status_changed", agent.StatusPayload{Status: "idle", Reason: "turn_done"})
}
func (r *hermesRun) handle(f hermesFrame) {
	if f.questionDone != "" {
		r.questionsMu.Lock()
		_, exists := r.questions[f.questionDone]
		exists = exists && r.questionTokens[f.questionDone] == f.questionToken
		if exists {
			delete(r.questions, f.questionDone)
			delete(r.questionTokens, f.questionDone)
		}
		remaining := len(r.questions)
		r.questionsMu.Unlock()
		if exists && remaining == 0 && r.active {
			r.d.emit("status_changed", agent.StatusPayload{Status: "running", Reason: "ask_answered"})
		}
		return
	}

	if f.Method != "event" {
		if f.Method != "" && len(f.ID) > 0 {
			r.request(f)
		}
		return
	}
	var event struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(f.Params, &event) != nil {
		return
	}
	if event.SessionID != r.sid {
		return
	}
	switch event.Type {
	case "session.info":
		r.info(event.Payload)
	case "message.start":
		if !r.active {
			r.begin()
			r.d.mu.Lock()
			r.d.active = true
			r.d.mu.Unlock()
			r.d.emit("status_changed", agent.StatusPayload{Status: "running", Reason: "native_turn"})
		}
	case "message.delta":
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && r.active {
			r.endThinking()
			r.d.emit("assistant_text", agent.AssistantTextPayload{Text: p.Text})
		}
	case "message.interim":
		var p struct {
			Text    string `json:"text"`
			Already bool   `json:"already_streamed"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && p.Text != "" && !p.Already {
			r.d.emit("assistant_text", agent.AssistantTextPayload{Text: p.Text, Done: true})
		}
	case "thinking.delta", "tool.generating":
		if r.active {
			r.startThinking()
		}
	case "reasoning.delta", "reasoning.available":
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && r.active {
			r.startThinking()
			if event.Type == "reasoning.available" && strings.Contains(r.thought.String(), p.Text) {
				break
			}
			if r.thought.Len()+len(p.Text) < 1<<20 {
				r.thought.WriteString(p.Text)
			}
		}
	case "session.usage":
		var p struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(event.Payload, &p) != nil {
			break
		}
		r.reportUsage(p.Usage)
	case "message.complete":
		var p struct {
			Text   string          `json:"text"`
			Status string          `json:"status"`
			Usage  json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(event.Payload, &p) != nil {
			return
		}
		if u, ok := hermesReportedUsage(p.Usage); ok {
			r.observeUsage(u)
		}
		stop := "end_turn"
		if p.Status == "error" {
			stop = "error"
			p.Text = ""
			r.d.emit("error", agent.ErrorPayload{Message: "Hermes reported a provider or tool failure; its private diagnostics were not forwarded", Retryable: true})
		}
		if p.Status == "interrupted" || p.Status == "cancelled" {
			stop = "interrupted"
		}
		r.finish(stop, p.Text)
	case "tool.start":
		var p struct {
			ID   string          `json:"tool_id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal(event.Payload, &p) != nil || p.ID == "" || len(p.ID) > 256 || p.Name == "" || len(p.Name) > 256 || r.tools[p.ID] {
			return
		}
		if len(r.tools) >= 1024 {
			r.rpc.stop()
			r.d.emit("error", agent.ErrorPayload{Message: "Hermes tool lifecycle limit exceeded", Retryable: true})
			return
		}
		r.tools[p.ID] = true
		if !json.Valid(p.Args) {
			p.Args = json.RawMessage(`{}`)
		}
		// A tool call ends the thinking phase (the browser and the server close it on tool_call);
		// reasoning after it is a new phase with its own count.
		r.endThinking()
		r.d.emit("tool_call", agent.ToolCallPayload{Tool: p.Name, CallID: p.ID, Input: p.Args})
	case "tool.complete":
		var p struct {
			ID       string          `json:"tool_id"`
			Result   json.RawMessage `json:"result"`
			Text     string          `json:"result_text"`
			Duration float64         `json:"duration_s"`
		}
		if json.Unmarshal(event.Payload, &p) != nil || !r.tools[p.ID] {
			return
		}
		delete(r.tools, p.ID)
		text := p.Text
		if text == "" {
			var s string
			if json.Unmarshal(p.Result, &s) == nil {
				text = s
			} else {
				text = string(p.Result)
			}
		}
		if len(text) > 1<<20 {
			text = text[:1<<20] + "\n[tool result truncated]"
		}
		r.d.emit("tool_result", agent.ToolResultPayload{CallID: p.ID, Output: text, DurationMs: int64(p.Duration * 1000)})
	case "todo.updated":
		r.plan(event.Payload)
	case "subagent.spawn_requested", "subagent.start", "subagent.progress", "subagent.thinking", "subagent.tool", "subagent.complete":
		r.child(event.Type, event.Payload)
		r.childReasoning(event.Type, event.Payload)
	case "sudo.request", "secret.request", "vault.unlock.request", "vault.save_login.request", "vault.code.request":
		r.refuseLegacyRequest(event.Type, event.Payload)
	case "approval.request":
		r.legacyApproval(event.Payload)
	case "clarify.request":
		r.legacyQuestion(event.Payload)
	case "request.cancel", "clarify.expire":
		r.cancelRequest(event.Payload)
	case "error":
		r.d.emit("error", agent.ErrorPayload{Message: "Hermes reported a native gateway error", Retryable: true})
	case "status.update":
		var p struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && p.Kind == "compacting" {
			r.d.emit("compaction", agent.CompactionPayload{})
		}
	}
}
func (r *hermesRun) plan(raw json.RawMessage) {
	if len(raw) > protocol.MaxPlanUpdatedBytes {
		return
	}
	var p struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Todos == nil {
		return
	}
	plan := protocol.PlanUpdatedPayload{Items: []protocol.PlanItem{}}
	for _, t := range p.Todos {
		status := t.Status
		if status == "cancelled" {
			status = "pending"
			t.Content = "[cancelled] " + t.Content
		}
		plan.Items = append(plan.Items, protocol.PlanItem{Text: t.Content, Status: status})
	}
	if clean, ok := protocol.SanitizePlanUpdated(plan); ok {
		r.d.emit(protocol.PlanUpdatedKind, clean)
	}
}
func (r *hermesRun) child(kind string, raw json.RawMessage) {
	var p struct {
		ID       string   `json:"subagent_id"`
		Parent   string   `json:"parent_id"`
		Model    string   `json:"model"`
		Goal     string   `json:"goal"`
		Summary  string   `json:"summary"`
		Status   string   `json:"status"`
		Input    *int     `json:"input_tokens"`
		Output   *int     `json:"output_tokens"`
		Tools    *int     `json:"tool_count"`
		Duration *float64 `json:"duration_seconds"`
	}
	if json.Unmarshal(raw, &p) != nil || p.ID == "" || len(p.ID) > 256 || len(p.Parent) > 256 || len(p.Model) > 256 {
		return
	}
	p.Goal = hermesBoundedText(p.Goal, 4000)
	p.Summary = hermesBoundedText(p.Summary, 4000)
	old, known := r.children[p.ID]
	if r.childDone[p.ID] {
		if kind != "subagent.start" && kind != "subagent.spawn_requested" {
			return
		}
		previous := old.ChildID
		// Display lifetimes stay bounded independently of the native ownership key.
		old = agent.SubagentPayload{ChildID: ccUUID(), AgentType: "Hermes", Resumed: true, ResumedFrom: previous}
		r.childDone[p.ID] = false
		r.childStarted[p.ID] = false
	}

	if !known && kind != "subagent.start" && kind != "subagent.spawn_requested" {
		return
	}
	if !known && p.Parent != "" && p.Parent != r.sid && p.Parent != r.d.nativeID {
		if _, ok := r.children[p.Parent]; !ok {
			return
		}
	}
	if !known {
		if len(r.children) >= 256 {
			r.rpc.stop()
			r.d.emit("error", agent.ErrorPayload{Message: "Hermes child lifecycle limit exceeded", Retryable: true})
			return
		}
		old = agent.SubagentPayload{ChildID: p.ID, AgentType: "Hermes", Summary: p.Goal}
	}
	priorModel := old.Model
	if p.Model != "" {
		old.Model = p.Model
		if priorModel != "" && priorModel != old.Model && old.Usage != nil {
			old.UsageByModel = map[string]agent.Usage{"": *old.Usage}
		}
	}
	if p.Summary != "" {
		old.Summary = p.Summary
	}
	if p.Tools != nil {
		old.ToolUses = *p.Tools
	}
	if (p.Input != nil && !hermesCounter(*p.Input)) || (p.Output != nil && !hermesCounter(*p.Output)) || (p.Tools != nil && !hermesCounter(*p.Tools)) {
		return
	}
	if p.Input != nil || p.Output != nil {
		u := agent.Usage{}
		if old.Usage != nil {
			u = *old.Usage
		}
		if p.Input != nil {
			u.InputTokens = *p.Input
		}
		if p.Output != nil {
			u.OutputTokens = *p.Output
		}
		old.Usage = &u
		attribution := old.Model
		if priorModel != "" && priorModel != old.Model {
			attribution = ""
		}
		if _, unknown := old.UsageByModel[""]; unknown {
			attribution = ""
		}
		old.UsageByModel = map[string]agent.Usage{attribution: u}
	}
	if p.Duration != nil && *p.Duration >= 0 && *p.Duration <= 366*24*3600 {
		old.DurationMs = int64(*p.Duration * float64(time.Second/time.Millisecond))
	}
	r.children[p.ID] = old
	if kind == "subagent.spawn_requested" {
		return
	}
	out := "subagent_update"
	if !r.childStarted[p.ID] {
		r.childStarted[p.ID] = true
		out = "subagent_started"
	}
	if kind == "subagent.complete" {
		out = "subagent_done"
		r.childDone[p.ID] = true
		old.IsError = p.Status == "failed" || p.Status == "error"
	}
	r.d.emit(out, old)
	r.reportOpenWork()
}

func hermesBoundedText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func (r *hermesRun) closeChildren() {
	for id, p := range r.children {
		if r.childDone[id] || !r.childStarted[id] {
			continue
		}
		p.IsError = true
		p.Summary = "Hermes gateway stopped before this child reported completion"
		r.d.emit("subagent_done", p)
		r.childDone[id] = true
	}
}

func hermesCounter(n int) bool { return n >= 0 && int64(n) <= 1<<50 }
func hermesAddUsage(a, b agent.Usage) agent.Usage {
	sum := func(x, y int) int {
		const limit = 1 << 60
		if x > limit-y {
			return limit
		}
		return x + y
	}
	return agent.Usage{InputTokens: sum(a.InputTokens, b.InputTokens), OutputTokens: sum(a.OutputTokens, b.OutputTokens), CacheReadTokens: sum(a.CacheReadTokens, b.CacheReadTokens), CacheWriteTokens: sum(a.CacheWriteTokens, b.CacheWriteTokens)}
}

func mustHermesJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }

func hermesReportedUsage(raw json.RawMessage) (hermesUsage, bool) {
	var fields map[string]*json.RawMessage
	var u hermesUsage
	if len(raw) > 64<<10 || json.Unmarshal(raw, &fields) != nil || fields["input"] == nil || fields["output"] == nil || json.Unmarshal(raw, &u) != nil || !hermesCounter(u.Input) || !hermesCounter(u.Output) || !hermesCounter(u.CacheRead) || !hermesCounter(u.CacheWrite) {
		return u, false
	}
	_, valid := protocol.DecodeUsageUpdated(mustHermesJSON(protocol.UsageUpdatedPayload{Scope: "turn", Model: u.Model, Usage: &protocol.UsageCounters{Input: int64(u.Input), Output: int64(u.Output), CacheRead: int64(u.CacheRead), CacheWrite: int64(u.CacheWrite)}}))
	return u, valid
}

func (r *hermesRun) reportUsage(raw json.RawMessage) {
	r.reasoningProgress(raw)
	u, valid := hermesReportedUsage(raw)
	if !valid {
		return
	}
	// Native counters are lifetime totals. Idle reports can establish the
	// next turn's baseline, but never publish resurrected live progress.
	r.observeUsage(u)
	if r.active {
		delta := hermesAddUsage(r.carried, r.total.delta(r.baseline))
		model := r.turnModel
		if r.ambiguous {
			model = ""
		}
		snapshot := protocol.UsageUpdatedPayload{Scope: "turn", Model: model, Usage: &protocol.UsageCounters{Input: int64(delta.InputTokens), Output: int64(delta.OutputTokens), CacheRead: int64(delta.CacheReadTokens), CacheWrite: int64(delta.CacheWriteTokens)}}
		if _, valid := protocol.DecodeUsageUpdated(mustHermesJSON(snapshot)); valid && (!r.usagePublished || snapshot.Model != r.lastUsage.Model || *snapshot.Usage != *r.lastUsage.Usage) {
			r.d.emit(protocol.UsageUpdatedKind, snapshot)
			r.lastUsage = snapshot
			r.usagePublished = true
		}
	}
}
