// Package livestate is what is live in a session right now — the agent's open questions, the
// subagents and background tasks still running — read from its whole event log, for the session
// panel's Live tab (GET /api/sessions/{id}/live). It applies the chat's rules (packages/chat:
// useLiveState, askStates, subagentStatus, runningTasks) to every event the session holds rather
// than to the part of the transcript a browser has loaded, so a refresh shows the whole list at once.
// It is pure: the server reads the events (Kinds, then each pending subagent's tail) and hands them
// to Build.
package livestate

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// Event is one transcript event (the reel's type). ID is its client_event_id.
type Event = topics.Event

// StaleRunning is how long after its newest sign of life a run with no end stops counting as
// running (the chat's STALE_RUNNING_MS): a daemon restart can cut one off before it says so.
const StaleRunning = 12 * time.Hour

// Kinds are the event kinds the live state reads. The first eight match migration 042
// in index order; MCP status is a latest-only snapshot with its own partial index (056).
// A subagent's own rows are read separately, only for the ones still running.
var Kinds = []string{"ask", "user_message", "turn_done", "subagent_started", "subagent_update", "subagent_done", "background_task_started", "background_task_done", protocol.MCPStatusKind}

// Options: Now is server time now; Status the session row's status; Closed the message ids of asks
// whose messages row is no longer open (answered, closed, expired); ServerAsk says whether an ask
// event is one the server recorded for `blerg-runner ask` (its client id derives from its message
// id) — such an ask stays open past its call and its turn; ShowTokens is the Insights rule's answer.
type Options struct {
	Now        time.Time
	Status     string
	Closed     map[string]bool
	ServerAsk  func(eventID, messageID string) bool
	ShowTokens bool
}

// State is GET /api/sessions/{id}/live's lists. Each is oldest first.
type State struct {
	MCP          *protocol.MCPStatusPayload `json:"mcp,omitempty"`
	MCPHold      *protocol.MCPHold          `json:"mcp_hold,omitempty"`
	Asks         []Ask                      `json:"asks"`
	Subagents    []Subagent                 `json:"subagents"`
	Tasks        []Task                     `json:"tasks"`
	TokensHidden bool                       `json:"tokens_hidden"`
}

// Ask is one of the agent's questions still waiting on an answer.
type Ask struct {
	EventID   string    `json:"event_id"`
	MessageID string    `json:"message_id"`
	Question  string    `json:"question"`
	AskedAt   time.Time `json:"asked_at"`
	Seq       int64     `json:"seq"`
}

// Subagent is a top-level subagent still at work. Key is its child_id (the Agent call's id, or for a
// resumed run the SendMessage call's); ActiveAt its newest sign of life (a client drops it
// StaleRunning after that). Resumed: a finished subagent the main agent resumed, running until its
// own end whatever the turn does; Background then says how its original ran.
type Subagent struct {
	Effort     string    `json:"effort"`
	Key        string    `json:"key"`
	Title      string    `json:"title"`
	Type       string    `json:"type"`
	Model      string    `json:"model"`
	Tokens     *int64    `json:"tokens"`
	Doing      string    `json:"doing"`
	StartedAt  time.Time `json:"started_at"`
	ActiveAt   time.Time `json:"active_at"`
	Background bool      `json:"background"`
	Resumed    bool      `json:"resumed,omitempty"`
	Seq        int64     `json:"seq"`
}

// Task is a background task still running (a background subagent's own task left out).
type Task struct {
	TaskID      string    `json:"task_id"`
	CallID      string    `json:"call_id"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	StartedAt   time.Time `json:"started_at"`
	Seq         int64     `json:"seq"`
}

// Tail names a subagent that may still be running, and the seq of its first event: the server
// reads its newest updates and its newest own row after that seq.
type Tail struct {
	Key string
	Seq int64
}

// live: the session can still be running something; inFlight: a turn runs or waits (a call can be
// in flight); ended: it is over, and nothing it asked is still open. As the chat reads them.
func live(status string) bool     { return status == "running" || status == "idle" || status == "waiting" }
func inFlight(status string) bool { return status == "running" || status == "waiting" }
func ended(status string) bool    { return status == "error" || status == "stopped" || status == "ended" }

func sorted(events []Event) []Event {
	out := append([]Event(nil), events...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

type subAcc struct {
	effort, model string
	key           string
	first         Event
	started       *agent.SubagentPayload
	update        *agent.SubagentPayload
	done          *agent.SubagentPayload // the latest subagent_done: a background launch, then its end
	background    bool
	resumed       bool // its start is a resumed run's: not tied to a call in flight
	final         bool // a subagent_done without Background: its end
	row           *Event
	active        time.Time
}

func (a *subAcc) touch(ts time.Time) {
	if ts.After(a.active) {
		a.active = ts
	}
}

// readSubagents folds the subagent events, in seq order, and the newest point a foreground call
// stops being in flight (a turn_done, or a message that is not the system's).
func readSubagents(events []Event) (order []*subAcc, lastTurnDone int64) {
	byKey := map[string]*subAcc{}
	get := func(key string, ev Event) *subAcc {
		a := byKey[key]
		if a == nil {
			a = &subAcc{key: key, first: ev}
			byKey[key] = a
			order = append(order, a)
		}
		return a
	}
	for _, ev := range events {
		switch ev.Kind {
		case "turn_done":
			lastTurnDone = ev.Seq
		case "user_message":
			// The chat's liveCalls: a message that is not the system's ends what the turn had in
			// flight (a person's mid-turn message, an answer), as a turn's end does.
			var p agent.UserMessagePayload
			if json.Unmarshal(ev.Payload, &p) == nil && p.Source != "system" {
				lastTurnDone = ev.Seq
			}
		case "subagent_started", "subagent_update", "subagent_done":
			var p agent.SubagentPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.ChildID == "" {
				continue
			}
			a := get(p.ChildID, ev)
			// Reusing a child ID starts a new incarnation. A server recovery completion
			// may end only the exact start it targeted, never this newer run.
			if ev.Kind == "subagent_started" {
				*a = subAcc{key: p.ChildID, first: ev}
			}
			if ev.Kind == "subagent_done" {
				var target struct {
					StartEventID string `json:"start_event_id"`
				}
				if json.Unmarshal(ev.Payload, &target) != nil || (target.StartEventID != "" && target.StartEventID != a.first.ID) {
					continue
				}
			}
			if p.Model != "" {
				if p.Model != a.model {
					a.effort = ""
				}
				a.model = p.Model
			}
			var observation struct {
				Effort json.RawMessage `json:"effort"`
			}
			_ = json.Unmarshal(ev.Payload, &observation)
			if len(observation.Effort) > 0 {
				a.effort = ""
				_ = json.Unmarshal(observation.Effort, &a.effort)
			}
			a.touch(ev.Ts)
			switch ev.Kind {
			case "subagent_started":
				if a.started == nil {
					a.started = &p
					a.resumed = p.Resumed
				}
			case "subagent_update":
				a.update = &p
			default:
				a.done = &p
				a.background = p.Background
				a.final = !p.Background
			}
		case "tool_call", "assistant_text":
			var p struct {
				Parent string `json:"parent_call_id"`
			}
			if json.Unmarshal(ev.Payload, &p) != nil || p.Parent == "" {
				continue
			}
			if a := byKey[p.Parent]; a != nil {
				e := ev
				a.row = &e
				a.touch(ev.Ts)
			}
		}
	}
	return order, lastTurnDone
}

// running: the subagent still runs at opt.Now — a background one or a resumed run in a live session
// (a resumed run's SendMessage call returns at once: only its own end ends it), a foreground one
// while its call is in flight (the session runs or waits on a turn, and no turn_done and no message
// but the system's came since it started: the chat's rule), none when its newest sign of life is
// older than StaleRunning.
func (a *subAcc) running(opt Options, lastTurnDone int64) bool {
	return a.unended(opt, lastTurnDone) && opt.Now.Sub(a.active) <= StaleRunning
}

// unended: running, staleness aside.
func (a *subAcc) unended(opt Options, lastTurnDone int64) bool {
	if !live(opt.Status) || a.final {
		return false
	}
	return a.background || a.resumed || (inFlight(opt.Status) && lastTurnDone < a.first.Seq)
}

// Pending: the subagents that may still be running (no end, in a live session), whose newest
// updates and own row the server reads next. Staleness is not judged here (a row may revive one),
// but it bounds the read: a row older than StaleRunning cannot, so a tail's Seq is never below the
// newest event held that is that old (a subagent that died silently costs 12 h of events at most,
// not the whole session since its start).
func Pending(events []Event, opt Options) []Tail {
	if !live(opt.Status) {
		return nil
	}
	events = sorted(events)
	var floor int64
	cutoff := opt.Now.Add(-StaleRunning)
	for _, ev := range events {
		if ev.Ts.Before(cutoff) && ev.Seq > floor {
			floor = ev.Seq
		}
	}
	order, lastTurnDone := readSubagents(events)
	var out []Tail
	for _, a := range order {
		if a.unended(opt, lastTurnDone) {
			out = append(out, Tail{Key: a.key, Seq: max(a.first.Seq, floor)})
		}
	}
	return out
}

// Build reads the live state from events (Kinds, plus the pending subagents' tails), in any order.
func Build(events []Event, opt Options) State {
	events = sorted(events)
	st := State{Asks: openAsks(events, opt), Subagents: []Subagent{}, Tasks: []Task{}, TokensHidden: !opt.ShowTokens}
	order, lastTurnDone := readSubagents(events)
	listed := map[string]bool{}
	for _, a := range order {
		if !a.running(opt, lastTurnDone) {
			continue
		}
		listed[a.key] = true
		st.Subagents = append(st.Subagents, a.view(opt.ShowTokens))
	}
	if live(opt.Status) {
		for _, t := range runningTasks(events, opt.Now) {
			if t.Kind == "agent" && listed[t.CallID] {
				continue // a background subagent's task: listed once, as the subagent
			}
			st.Tasks = append(st.Tasks, t)
		}
	}
	for _, e := range events {
		if e.Kind != protocol.MCPStatusKind {
			continue
		}
		var payload protocol.MCPStatusPayload
		if json.Unmarshal(e.Payload, &payload) != nil {
			continue
		}
		clean := protocol.SanitizeMCPStatus(payload)
		if clean.Engine == "" || clean.Since == "" {
			continue
		}
		st.MCP = &clean
	}
	if st.MCP != nil {
		st.MCPHold = st.MCP.Hold
		if ended(opt.Status) || opt.Status == "disconnected" {
			st.MCPHold = nil
		}
		if st.MCPHold != nil {
			until, _ := time.Parse(time.RFC3339Nano, st.MCPHold.Until)
			if !opt.Now.Before(until) {
				st.MCPHold = nil
			}
		}
		st.MCP.Hold = st.MCPHold
	}
	return st
}

func (a *subAcc) view(showTokens bool) Subagent {
	var started, update, done agent.SubagentPayload
	if a.started != nil {
		started = *a.started
	}
	if a.update != nil {
		update = *a.update
	}
	if a.done != nil {
		done = *a.done
	}
	s := Subagent{
		Key: a.key, Title: titleOf(started.Summary), Type: started.AgentType,
		Model:     a.model,
		StartedAt: a.first.Ts, ActiveAt: a.active, Background: a.background || a.resumed && started.Background,
		Resumed: a.resumed, Seq: a.first.Seq,
	}
	s.Effort = a.effort
	if showTokens {
		if n := usageTotal(done.Usage); n != nil {
			s.Tokens = n
		} else {
			s.Tokens = usageTotal(update.Usage)
		}
	}
	if a.row != nil {
		s.Doing = doingLine(*a.row)
	}
	return s
}

func usageTotal(u *agent.Usage) *int64 {
	if u == nil {
		return nil
	}
	n := int64(u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens)
	return &n
}

// runningTasks: the background tasks started and not ended, started within StaleRunning of now.
func runningTasks(events []Event, now time.Time) []Task {
	var out []Task
	for _, state := range OpenBackgroundTasks(events) {
		if now.Sub(state.Task.StartedAt) <= StaleRunning {
			out = append(out, state.Task)
		}
	}
	return out
}

type askAcc struct {
	ev     Event
	id     string // message id
	native bool   // closes with its turn (not one the server holds open)
	open   bool
	q      string
}

// openAsks is the chat's askStates read without the tool calls: an ask the server recorded for
// `blerg-runner ask` is open until it is answered, the server closes it or the session ends; any
// other (the native loop's, or one without a message id) also closes when its turn ends. A
// person's message closes the newest open ask without a message id.
func openAsks(events []Event, opt Options) []Ask {
	if ended(opt.Status) {
		return []Ask{}
	}
	var all []*askAcc
	newestOpen := func(pred func(a *askAcc) bool) *askAcc {
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].open && pred(all[i]) {
				return all[i]
			}
		}
		return nil
	}
	for _, ev := range events {
		switch ev.Kind {
		case "ask":
			var p agent.MessagingPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			server := p.MessageID != "" && opt.ServerAsk != nil && opt.ServerAsk(ev.ID, p.MessageID)
			all = append(all, &askAcc{ev: ev, id: p.MessageID, native: !server, open: true, q: p.Body})
		case "user_message":
			var p agent.UserMessagePayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if p.Source == "ask_answer" {
				var a *askAcc
				if p.MessageID != "" {
					for _, x := range all {
						if x.id == p.MessageID {
							a = x
							break
						}
					}
				} else {
					a = newestOpen(func(*askAcc) bool { return true })
				}
				if a != nil {
					a.open = false
				}
			} else if p.Source != "system" {
				if a := newestOpen(func(x *askAcc) bool { return x.id == "" }); a != nil {
					a.open = false
				}
			}
		case "turn_done":
			for _, a := range all {
				if a.native {
					a.open = false
				}
			}
		}
	}
	out := []Ask{}
	for _, a := range all {
		if !a.open || (a.id != "" && opt.Closed[a.id]) {
			continue
		}
		out = append(out, Ask{EventID: a.ev.ID, MessageID: a.id, Question: a.q, AskedAt: a.ev.Ts, Seq: a.ev.Seq})
	}
	return out
}

// titleOf: what the subagent is — its start's summary (the task's description, else the start of
// its prompt), first line.
func titleOf(summary string) string {
	if line := firstLine(summary); line != "" {
		return line
	}
	return "Subagent · started earlier"
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
