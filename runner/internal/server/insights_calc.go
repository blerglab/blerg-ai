package server

// Insights: the pure calculations behind the Insights page and the Prometheus endpoint. Nothing here touches
// the database or the network, so they are unit-tested against the event sequences the runner really records
// (see insights_calc_test.go). The loaders live in internal/db/insights.go and the handlers in insights.go.

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ─── Start attempts ───────────────────────────────────────────────────────────

// startRecord is one start of a session (a fresh start or a resume), reconstructed from its start_stage events.
type startRecord struct {
	SessionID   string
	Runtime     string
	Resume      bool
	Start       time.Time
	End         time.Time // when the agent became ready, the attempt failed, or its last event
	Ok          bool
	FailedStage string
	// Steps is how long each stage took, by stage id. A stage that a later one jumped over lasted zero.
	// "ready" is the end of the attempt and has no entry.
	Steps map[string]float64
}

// Seconds is how long the attempt took.
func (a startRecord) Seconds() float64 { return a.End.Sub(a.Start).Seconds() }

// stageReached is an attempt under construction.
type recordBuilder struct {
	a       startRecord
	order   []string
	index   map[string]int
	reached []time.Time // when each stage became the current one
	cur     int
	open    bool
}

// analyzeAttempts rebuilds start attempts from start_stage events ordered by session and sequence. A plan
// event opens an attempt (the payload lists every stage in order); the updates that follow merge by stage id
// and move the attempt forward by the viewer's "furthest active/done stage" rule. Updates with no plan
// before them (their attempt began before the range) are ignored. runtimeOf names a session's runtime when the
// plan did not.
func analyzeAttempts(events []db.InsightEvent, runtimeOf func(sessionID string) string) []startRecord {
	var out []startRecord
	var b *recordBuilder
	session := ""
	plans := 0
	finish := func() {
		if b != nil && b.open {
			out = append(out, b.a)
		}
		b = nil
	}
	for _, e := range events {
		if e.Kind != "start_stage" {
			continue
		}
		if e.SessionID != session {
			finish()
			session, plans = e.SessionID, 0
		}
		var p protocol.StartStagePayload
		if json.Unmarshal([]byte(e.Payload), &p) != nil {
			continue
		}
		if p.Plan {
			finish()
			plans++
			b = newAttempt(e, p, plans > 1, runtimeOf)
			continue
		}
		if b == nil || !b.open {
			continue
		}
		b.apply(e.Ts, p.Stages)
	}
	finish()
	return out
}

func newAttempt(e db.InsightEvent, p protocol.StartStagePayload, resume bool, runtimeOf func(string) string) *recordBuilder {
	runtime := p.Runtime
	if runtime == "" && runtimeOf != nil {
		runtime = runtimeOf(e.SessionID)
	}
	b := &recordBuilder{
		a:     startRecord{SessionID: e.SessionID, Runtime: runtime, Resume: resume, Start: e.Ts, End: e.Ts, Steps: map[string]float64{}},
		index: map[string]int{},
		open:  true,
	}
	for i, s := range p.Stages {
		b.order = append(b.order, s.ID)
		b.index[s.ID] = i
		b.reached = append(b.reached, time.Time{})
	}
	b.cur = -1
	// the plan itself may already have the first stage active
	b.apply(e.Ts, p.Stages)
	return b
}

// apply merges one event's stages into the attempt.
func (b *recordBuilder) apply(ts time.Time, stages []protocol.StartStage) {
	b.a.End = ts
	furthest := b.cur
	for _, s := range stages {
		i, ok := b.index[s.ID]
		if !ok {
			continue
		}
		switch s.State {
		case protocol.StageStateFailed:
			if b.a.FailedStage == "" && !b.a.Ok {
				b.a.FailedStage = s.ID
			}
		case protocol.StageStateActive, protocol.StageStateDone, protocol.StageStateWarning:
			if i > furthest {
				furthest = i
			}
			if s.ID == "ready" && s.State == protocol.StageStateDone {
				b.a.Ok = true
			}
		}
	}
	if furthest > b.cur {
		// every stage before the new furthest one is over now; one that was jumped over lasted zero
		for k := b.cur; k < furthest; k++ {
			if k < 0 {
				continue
			}
			if b.reached[k].IsZero() {
				b.reached[k] = ts
			}
			if b.order[k] != "ready" {
				if d := ts.Sub(b.reached[k]).Seconds(); d >= 0 { // out-of-order timestamps
					b.a.Steps[b.order[k]] = d
				}
			}
		}
		for k := b.cur + 1; k <= furthest; k++ {
			if b.reached[k].IsZero() {
				b.reached[k] = ts
			}
		}
		b.cur = furthest
	}
}

// markResumes flags the attempts of a session that was already running when the attempt began: a fresh start's
// plan comes within moments of the session being created, a resume's much later. analyzeAttempts can only tell
// a second plan from a first one within the loaded range.
func markResumes(attempts []startRecord, sessions map[string]db.InsightSession) {
	for i := range attempts {
		if s, ok := sessions[attempts[i].SessionID]; ok && attempts[i].Start.Sub(s.StartedAt) > 60*time.Second {
			attempts[i].Resume = true
		}
	}
}

// slowestStarts returns the n longest successful starts the caller may see.
func slowestStarts(attempts []startRecord, n int, visible func(sessionID string) bool) []startRecord {
	var ok []startRecord
	for _, a := range attempts {
		if a.Ok && (visible == nil || visible(a.SessionID)) {
			ok = append(ok, a)
		}
	}
	sort.SliceStable(ok, func(i, j int) bool { return ok[i].Seconds() > ok[j].Seconds() })
	if len(ok) > n {
		ok = ok[:n]
	}
	return ok
}

// ─── Busy and idle time ───────────────────────────────────────────────────────

// statusSeconds adds up how long sessions spent running, waiting for a person, and idle inside [from, to].
// Time in any other status (starting, disconnected, stopped, error, ended) is not counted. The last status of
// a session lasts until the session ended, or until now when it is still alive.
func statusSeconds(events []db.InsightEvent, sessions map[string]db.InsightSession, from, to, now time.Time) (running, waiting, idle float64) {
	type step struct {
		ts     time.Time
		status string
	}
	bySession := map[string][]step{}
	for _, e := range events {
		if e.Kind != "status_changed" {
			continue
		}
		var p struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Status == "" {
			continue
		}
		bySession[e.SessionID] = append(bySession[e.SessionID], step{e.Ts, p.Status})
	}
	for id, steps := range bySession {
		end := now
		if s, ok := sessions[id]; ok && s.EndedAt != nil {
			end = *s.EndedAt
		}
		for i, st := range steps {
			stop := end
			if i+1 < len(steps) {
				stop = steps[i+1].ts
			}
			lo, hi := st.ts, stop
			if lo.Before(from) {
				lo = from
			}
			if hi.After(to) {
				hi = to
			}
			if !hi.After(lo) {
				continue
			}
			d := hi.Sub(lo).Seconds()
			switch st.status {
			case "running":
				running += d
			case "waiting":
				waiting += d
			case "idle":
				idle += d
			}
		}
	}
	return running, waiting, idle
}

// peakConcurrent is the most sessions of one runtime alive at once within [from, to].
func peakConcurrent(sessions []db.InsightSession, runtime string, from, to, now time.Time) int {
	type edge struct {
		ts    time.Time
		delta int
	}
	var edges []edge
	for _, s := range sessions {
		if s.Runtime != runtime || s.Kind != "agent" {
			continue
		}
		end := now
		if s.EndedAt != nil {
			end = *s.EndedAt
		}
		start := s.StartedAt
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		edges = append(edges, edge{start, +1}, edge{end, -1})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].ts.Equal(edges[j].ts) {
			return edges[i].delta < edges[j].delta // an ending frees its place before a start at the same instant
		}
		return edges[i].ts.Before(edges[j].ts)
	})
	cur, peak := 0, 0
	for _, e := range edges {
		cur += e.delta
		if cur > peak {
			peak = cur
		}
	}
	return peak
}

// ─── Numbers ──────────────────────────────────────────────────────────────────

type quantiles struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	Max   float64 `json:"max"`
}

// quantilesOf summarises values with linear interpolation between ranks.
func quantilesOf(v []float64) quantiles {
	if len(v) == 0 {
		return quantiles{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	at := func(p float64) float64 {
		pos := p * float64(len(s)-1)
		lo := int(math.Floor(pos))
		hi := int(math.Ceil(pos))
		return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
	}
	return quantiles{Count: len(s), P50: at(0.5), P90: at(0.9), Max: s[len(s)-1]}
}

// costUSD estimates what tokens cost at a model's per-million-token prices.
func costUSD(input, output, cacheRead, cacheWrite int64, p db.ModelPrice) float64 {
	const m = 1_000_000.0
	return float64(input)/m*p.InputPerMTok + float64(output)/m*p.OutputPerMTok +
		float64(cacheRead)/m*p.CacheReadPerMTok + float64(cacheWrite)/m*p.CacheWritePerMTok
}
