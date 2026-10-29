package server

// The token chip on every Running now row (the Main row and each started session, Claude, Codex or
// any engine): what that session has used so far, from the recorded turn usage Insights reads
// (turn_done, subagent_done), plus what a running turn has reported so far (usage_updated). The
// visibility is Insights': only the session's owner or an administrator gets it at all.
//
// The context meter beside it (card #191) reads the same way: how full the main agent's context
// window was at the end of its latest turn that reported it, as the engine said (agent.ContextUsage on
// turn_done). Nothing is estimated: a part the engine did not report is left out.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// liveTurnUsage is one turn's usage: the running turn's so far (InFlight) or the last finished one.
type liveTurnUsage struct {
	Tokens     int64 `json:"tokens"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	InFlight   bool  `json:"in_flight"`
}

// liveUsage is a session's usage so far, for its Running now row.
type liveUsage struct {
	// Total is every token counted (input, output, cache read and cache write) across its turns
	// and its subagents.
	Total      int64 `json:"total"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	// Subagents is the part of Total its subagents used.
	Subagents int64          `json:"subagents"`
	Turns     int64          `json:"turns"`
	Turn      *liveTurnUsage `json:"turn,omitempty"`
	// CostUSD is the estimate at the configured prices, absent when a model has no price.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	// Context is the context meter's reading; absent when no turn reported one.
	Context *liveContext `json:"context,omitempty"`
}

// liveContext is the main agent's context at the end of its latest turn that reported it. A zero
// count is one the engine did not report (omitted).
type liveContext struct {
	// Tokens is the conversation's size then; absent when a compaction came after the last call.
	Tokens int64 `json:"tokens,omitempty"`
	// Window is the model's context window; CompactAt the size at which the engine compacts.
	Window    int64 `json:"window,omitempty"`
	CompactAt int64 `json:"compact_at,omitempty"`
	// Compacted says the engine compacted the conversation in that turn, from CompactedFrom tokens.
	Compacted     bool      `json:"compacted,omitempty"`
	CompactedFrom int64     `json:"compacted_from,omitempty"`
	At            time.Time `json:"at"`
}

// maxContextTokens bounds a reported count: anything larger is not a context size.
const maxContextTokens = 1 << 40

// decodeLiveContext reads a turn_done's context report; nil when it says nothing usable.
func decodeLiveContext(e db.AgentEventRow) *liveContext {
	var p struct {
		Context *struct {
			Tokens        int64 `json:"tokens"`
			Window        int64 `json:"window"`
			CompactAt     int64 `json:"compact_at"`
			Compacted     bool  `json:"compacted"`
			CompactedFrom int64 `json:"compacted_from"`
		} `json:"context"`
	}
	if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Context == nil {
		return nil
	}
	c := p.Context
	for _, n := range []int64{c.Tokens, c.Window, c.CompactAt, c.CompactedFrom} {
		if n < 0 || n > maxContextTokens {
			return nil
		}
	}
	if c.Tokens == 0 && !c.Compacted {
		return nil
	}
	out := &liveContext{Tokens: c.Tokens, Window: c.Window, CompactAt: c.CompactAt, Compacted: c.Compacted, At: e.Ts.UTC()}
	if c.Compacted {
		out.CompactedFrom = c.CompactedFrom
	}
	if out.Window > 0 && out.CompactAt > out.Window {
		out.CompactAt = 0 // a threshold past the window is not one
	}
	return out
}

// readLiveContext reads a session's context reading through the boundary; nil when there is none.
func (a *API) readLiveContext(ctx context.Context, sid string, through int64) (*liveContext, error) {
	row, compaction, ok, err := db.ReadContextTurn(ctx, a.dbPool, sid, through)
	if err != nil || !ok {
		return nil, err
	}
	return compactedSince(decodeLiveContext(row), compaction), nil
}

// compactedSince applies a compaction recorded after the reading's turn: its count no longer
// describes the conversation, so it is dropped (the meter shows the compacted marker until a turn
// reports a new one); the window stays the reading's model's. Nil compaction: the reading as it is.
func compactedSince(c *liveContext, compaction *db.AgentEventRow) *liveContext {
	if compaction == nil || c == nil {
		return c
	}
	var p struct {
		PreTokens int64 `json:"pre_tokens"`
	}
	_ = json.Unmarshal([]byte(compaction.Payload), &p)
	out := &liveContext{Window: c.Window, CompactAt: c.CompactAt, Compacted: true, At: compaction.Ts.UTC()}
	if p.PreTokens > 0 && p.PreTokens <= maxContextTokens {
		out.CompactedFrom = p.PreTokens
	}
	return out
}

func turnCounters(u protocol.UsageCounters, inFlight bool) *liveTurnUsage {
	return &liveTurnUsage{Tokens: u.Input + u.Output + u.CacheRead + u.CacheWrite, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, InFlight: inFlight}
}

// turnUsage picks the turn the chip's tooltip names from the newest turn_done and usage_updated
// (by seq): a running turn's own report when it is newer than the last finished turn, otherwise the
// last finished turn's usage. Nil when neither carries usage.
func turnUsage(events []db.AgentEventRow, active bool) (turn *liveTurnUsage, inFlight *protocol.UsageUpdatedPayload) {
	var done int64
	for _, e := range events {
		switch e.Kind {
		case "turn_done":
			done = e.Seq
			var p struct {
				Reported *bool                   `json:"usage_reported"`
				Usage    *protocol.UsageCounters `json:"usage"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) == nil && p.Usage != nil && (p.Reported == nil || *p.Reported) {
				turn = turnCounters(*p.Usage, false)
			}
		case protocol.UsageUpdatedKind:
			if !active || e.Seq <= done {
				continue
			}
			if p, ok := protocol.DecodeUsageUpdated([]byte(e.Payload)); ok && p.Usage != nil {
				turn, inFlight = turnCounters(*p.Usage, true), &p
			}
		}
	}
	return turn, inFlight
}

// summariseLiveUsage adds a running turn's report to the recorded usage and totals it the way
// Insights does (summariseSessionTokens). Nil when nothing was recorded.
func summariseLiveUsage(rows []db.SessionTokenRow, inFlight *protocol.UsageUpdatedPayload, turn *liveTurnUsage, prices []db.ModelPrice) *liveUsage {
	all := append([]db.SessionTokenRow(nil), rows...)
	if inFlight != nil && inFlight.Usage != nil {
		u := inFlight.Usage
		all = append(all, db.SessionTokenRow{Model: inFlight.Model, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
	}
	if len(all) == 0 && turn == nil {
		return nil
	}
	s := summariseSessionTokens(all, prices)
	t := s.Totals
	out := &liveUsage{
		Total: t.Input + t.Output + t.CacheRead + t.CacheWrite, Input: t.Input, Output: t.Output, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite,
		Subagents: s.Subagents.Input + s.Subagents.Output + s.Subagents.CacheRead + s.Subagents.CacheWrite,
		Turns:     s.Main.Turns, Turn: turn,
	}
	if len(s.UnpricedModels) == 0 && out.Total > 0 {
		out.CostUSD = t.CostUSD
	}
	return out
}

// sessionLiveUsage reads a session's usage through the boundary. active says a turn may be running
// (the session runs or waits on one), so a newer usage_updated is the running turn's.
func (a *API) sessionLiveUsage(ctx context.Context, sid string, through int64, active bool, prices []db.ModelPrice) (*liveUsage, error) {
	rows, err := db.ListSessionTokenUsageThrough(ctx, a.dbPool, sid, through)
	if err != nil {
		return nil, err
	}
	events, err := db.ReadTurnUsageEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		return nil, err
	}
	turn, inFlight := turnUsage(events, active)
	out := summariseLiveUsage(rows, inFlight, turn, prices)
	if out != nil {
		if out.Context, err = a.readLiveContext(ctx, sid, through); err != nil {
			return nil, err
		}
	}
	return out, nil
}
