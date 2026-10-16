package server

// Session insights: GET /api/sessions/{id}/insights, the figures of one session (time, work, subagents,
// tokens and estimated cost) for the Insights dialog on the session page.
//
// Who sees what: a signed-in person who may see the session (canSeeAccount, the rule the session list and
// the session page use). An agent token is refused, and a session that is unknown or private to someone
// else answers one uniform 404, like the session's files.
//
// Tokens and cost follow the Insights page's rule instead: they are for the session's owner (the account
// that started it) and for an administrator (insightsAdminCap). Anyone else who can see the session gets
// every other figure, with tokens null and tokens_hidden set.
//
// The figures follow the Insights page's rules: busy time from the agent's status changes (statusSeconds),
// tokens from turn_done and from subagent_done with usage (a subagent's tokens under its own model, not as
// turns), dollars from the same price table (costUSD). A terminal session has no agent transcript, so it
// reports its lifetime and little else.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

type sessionSubagents struct {
	Count   int        `json:"count"`
	ByType  []countRow `json:"by_type"`
	ByModel []countRow `json:"by_model"`
	// ToolCalls is the tool calls the subagents made (events with a parent_call_id); the session's
	// tool_calls leave them out.
	ToolCalls int `json:"tool_calls"`
}

type sessionTokens struct {
	Totals         tokenCounts `json:"totals"`
	Main           tokenCounts `json:"main"`
	Subagents      tokenCounts `json:"subagents"`
	ByModel        []modelRow  `json:"by_model"`
	CacheHitRate   float64     `json:"cache_hit_rate"`
	UnpricedModels []string    `json:"unpriced_models"`
}

// sessionProposals is the session's MCP proposals by what became of them: approved covers every
// proposal a person approved (executing, done, failed, unknown), declined the rejected ones; expired
// ones nobody decided.
type sessionProposals struct {
	Pending  int `json:"pending"`
	Approved int `json:"approved"`
	Declined int `json:"declined"`
	Expired  int `json:"expired"`
}

type sessionMCPCalls struct {
	Total     int        `json:"total"`
	ByOutcome []countRow `json:"by_outcome"`
}

type sessionInsightsResponse struct {
	SessionID          string           `json:"session_id"`
	Title              string           `json:"title"`
	Kind               string           `json:"kind"`
	Status             string           `json:"status"`
	Runtime            string           `json:"runtime"`
	Engine             string           `json:"engine"`
	Model              string           `json:"model"`
	StartedAt          time.Time        `json:"started_at"`
	EndedAt            *time.Time       `json:"ended_at"`
	GeneratedAt        time.Time        `json:"generated_at"`
	LifetimeSeconds    float64          `json:"lifetime_seconds"`
	RunningSeconds     float64          `json:"running_seconds"`
	WaitingSeconds     float64          `json:"waiting_seconds"`
	IdleSeconds        float64          `json:"idle_seconds"`
	Turns              int64            `json:"turns"`
	ToolCalls          int              `json:"tool_calls"`
	ToolCallsByTool    []countRow       `json:"tool_calls_by_tool"`
	MessagesFromPerson int              `json:"messages_from_person"`
	Subagents          sessionSubagents `json:"subagents"`
	Tokens             *sessionTokens   `json:"tokens"`
	TokensHidden       bool             `json:"tokens_hidden,omitempty"`
	Artifacts          int              `json:"artifacts"`
	Proposals          sessionProposals `json:"proposals"`
	MCPCalls           sessionMCPCalls  `json:"mcp_calls"`
}

// HandleGetSessionInsights is GET /api/sessions/{id}/insights.
func (a *API) HandleGetSessionInsights(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		writeError(w, http.StatusForbidden, "session insights are for a signed-in person, not an agent token")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "insights need the database")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		log.Printf("session insights GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if row == nil || !canSeeAccount(p.Sub, row) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	showTokens := ownsSession(p.Sub, row) || p.Has(insightsAdminCap)
	resp, err := a.buildSessionInsights(ctx, row, time.Now().UTC(), showTokens)
	if err != nil {
		log.Printf("session insights %s: %v", sid, err)
		writeError(w, http.StatusInternalServerError, "could not compute insights")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, resp)
}

// buildSessionInsights computes the report; showTokens false leaves tokens out (tokens_hidden).
func (a *API) buildSessionInsights(ctx context.Context, row *db.SessionRow, now time.Time, showTokens bool) (*sessionInsightsResponse, error) {
	tokenRows, err := db.ListSessionTokenUsage(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	activity, err := db.GetSessionActivity(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	timeline, err := db.ListSessionTimeline(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	side, err := db.GetSessionSideCounts(ctx, a.dbPool, row.ID)
	if err != nil {
		return nil, err
	}
	prices, err := db.ListModelPrices(ctx, a.dbPool)
	if err != nil {
		return nil, err
	}

	out := &sessionInsightsResponse{
		SessionID: row.ID, Title: derefOrEmpty(row.Title), Kind: row.Kind, Status: row.Status,
		Runtime: derefOrEmpty(row.Runtime), Engine: derefOrEmpty(row.Engine), Model: derefOrEmpty(row.Model),
		StartedAt: row.StartedAt, EndedAt: row.EndedAt, GeneratedAt: now,
		ToolCallsByTool: []countRow{},
	}
	end := now
	if row.EndedAt != nil {
		end = *row.EndedAt
	}
	if d := end.Sub(row.StartedAt).Seconds(); d > 0 {
		out.LifetimeSeconds = d
	}
	// The whole life of the session: statusSeconds clips to [from, to], and the last status lasts until
	// the session ended, or until now.
	session := db.InsightSession{ID: row.ID, StartedAt: row.StartedAt, EndedAt: row.EndedAt}
	out.RunningSeconds, out.WaitingSeconds, out.IdleSeconds = statusSeconds(timeline,
		map[string]db.InsightSession{row.ID: session}, time.Time{}, now.Add(time.Minute), now)

	tools := map[string]int{}
	for _, c := range activity.ToolCalls {
		if c.Subagent {
			out.Subagents.ToolCalls += c.Count
			continue
		}
		out.ToolCalls += c.Count
		tools[orUnknown(c.Tool)] += c.Count
	}
	out.ToolCallsByTool = sortedCounts(tools)
	out.MessagesFromPerson = activity.MessagesFromPerson

	sub := summariseSubagents(timeline)
	sub.ToolCalls = out.Subagents.ToolCalls
	out.Subagents = sub
	tokens := summariseSessionTokens(tokenRows, prices)
	out.Turns = tokens.Main.Turns // a count of turns, not of tokens: shown to everyone
	if showTokens {
		out.Tokens = &tokens
	} else {
		out.TokensHidden = true
	}

	out.Artifacts = side.Artifacts
	for state, n := range side.Proposals {
		switch state {
		case "pending":
			out.Proposals.Pending += n
		case "executing", "done", "failed", "unknown":
			out.Proposals.Approved += n
		case "rejected":
			out.Proposals.Declined += n
		case "expired":
			out.Proposals.Expired += n
		}
	}
	for _, n := range side.MCPOutcomes {
		out.MCPCalls.Total += n
	}
	out.MCPCalls.ByOutcome = sortedCounts(side.MCPOutcomes)
	return out, nil
}

// summariseSubagents counts a session's subagents (distinct child ids among subagent_started and
// subagent_done) by type and model. A later event's non-empty type or model wins: subagent_done carries
// the model the API reported, where subagent_started may carry the alias that was asked for.
func summariseSubagents(events []db.InsightEvent) sessionSubagents {
	type child struct{ agentType, model string }
	children := map[string]*child{}
	for _, e := range events {
		if e.Kind != "subagent_started" && e.Kind != "subagent_done" {
			continue
		}
		var p struct {
			ChildID   string `json:"child_id"`
			AgentType string `json:"agent_type"`
			Model     string `json:"model"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.ChildID == "" {
			continue
		}
		c := children[p.ChildID]
		if c == nil {
			c = &child{}
			children[p.ChildID] = c
		}
		if p.AgentType != "" {
			c.agentType = p.AgentType
		}
		if p.Model != "" {
			c.model = p.Model
		}
	}
	types, models := map[string]int{}, map[string]int{}
	for _, c := range children {
		types[orUnknown(c.agentType)]++
		models[orUnknown(c.model)]++
	}
	return sessionSubagents{Count: len(children), ByType: sortedCounts(types), ByModel: sortedCounts(models)}
}

// summariseSessionTokens is summariseTokens for one session (totals, by model, cache hit rate, unpriced
// models), plus the split between the main agent's turns and its subagents.
func summariseSessionTokens(rows []db.SessionTokenRow, prices []db.ModelPrice) sessionTokens {
	trs := make([]db.TokenRow, 0, len(rows))
	for _, r := range rows {
		trs = append(trs, db.TokenRow{Model: r.Model, Turns: r.Turns, Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite})
	}
	all := summariseTokens(trs, nil, prices, func(string) bool { return false })
	out := sessionTokens{Totals: all.Totals, ByModel: all.ByModel, CacheHitRate: all.CacheHitRate, UnpricedModels: all.UnpricedModels}
	pm := priceIndex(prices)
	for i, r := range rows {
		part := &out.Main
		if r.Subagent {
			part = &out.Subagents
		}
		addTokens(part, trs[i])
		if price, ok := pm[r.Model]; ok {
			addCost(part, costUSD(r.Input, r.Output, r.CacheRead, r.CacheWrite, price))
		}
	}
	for _, c := range []*tokenCounts{&out.Main, &out.Subagents} {
		if c.CostUSD != nil {
			v := roundUSD(*c.CostUSD)
			c.CostUSD = &v
		}
	}
	return out
}
