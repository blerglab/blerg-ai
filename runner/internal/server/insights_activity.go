package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/skills"
)

// One live authorized exchange per board, with bounded concurrency. Metadata is
// not cached across requests, so a permission change takes effect on the next read.
func readInsightsBoards(ctx context.Context, caller sessionBoardCaller, proof mcpgw.Proof, refs []db.InsightCardRef, from, to time.Time) ([]insightActivityCard, []insightActivityMark, bool) {
	cards := []insightActivityCard{}
	events := []insightActivityMark{}
	if caller == nil || len(refs) == 0 {
		return cards, events, true
	}
	groups := map[string][]db.InsightCardRef{}
	for _, ref := range refs {
		groups[ref.Board] = append(groups[ref.Board], ref)
	}
	var mu sync.Mutex
	var workers sync.WaitGroup
	unavailable := false
	jobs := make(chan string)
	for range 6 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for board := range jobs {
				numbers := []int{}
				allowed := map[int]time.Time{}
				for _, ref := range groups[board] {
					numbers = append(numbers, ref.Number)
					allowed[ref.Number] = ref.Latest
				}
				args, _ := json.Marshal(map[string]any{"board_id": board, "card_numbers": numbers, "events_from": from.Format(time.RFC3339Nano), "events_to": to.Format(time.RFC3339Nano)})
				raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, Tool: "blerg_board_get", Arguments: args, ReadOnly: true})
				var result struct {
					Cards  *[]liveBoardCard `json:"insight_cards"`
					Events *[]struct {
						At    time.Time `json:"at"`
						Label string    `json:"label"`
					} `json:"events"`
				}
				failed := err != nil
				if failed {
					failurelog.Default.Event("insights_board_read", slog.LevelWarn, "operation", "insights_board_read", "cause", failurelog.Cause(err))
				} else if boardToolResult(raw, &result) != nil || result.Cards == nil || result.Events == nil {
					failed = true
					failurelog.Default.Event("insights_board_decode", slog.LevelWarn, "operation", "insights_board_decode", "cause", "invalid_response")
				}
				mu.Lock()
				if failed {
					unavailable = true
					mu.Unlock()
					continue
				}
				for _, card := range *result.Cards {
					latest, ok := allowed[card.Number]
					if !ok || card.BoardID != board {
						continue
					}
					if at, e := time.Parse(time.RFC3339Nano, card.UpdatedAt); e == nil && at.After(latest) && !at.After(to) {
						latest = at
					}
					cards = append(cards, insightActivityCard{Latest: latest, Reference: capText(card.Reference, 80), Title: capText(card.Title, 160), Key: board + "#" + strconv.Itoa(card.Number), Size: card.Size, Risk: card.Risk, Complexity: card.Complexity, SizeFirstSetAt: card.SizeFirstSetAt, Flow: json.RawMessage(`null`)})
				}
				for _, event := range *result.Events {
					if !event.At.Before(from) && event.At.Before(to) {
						events = append(events, insightActivityMark{Kind: "milestone", Label: capText(event.Label, 160), RecordedAt: event.At})
					}
				}
				mu.Unlock()
			}
		}()
	}
	for board := range groups {
		select {
		case jobs <- board:
		case <-ctx.Done():
		}
	}
	close(jobs)
	workers.Wait()
	return cards, events, unavailable
}

type insightActivitySession struct {
	Title              string     `json:"title"`
	NativeTimeUnknown  bool       `json:"native_active_time_unknown"`
	HasSubagents       bool       `json:"has_subagents"`
	Engine             string     `json:"engine"`
	AttributionUnknown bool       `json:"card_attribution_unknown,omitempty"`
	ID                 string     `json:"id"`
	Parent             *string    `json:"parent_session_id"`
	StartedAt          *time.Time `json:"started_at"`
	EndedAt            *time.Time `json:"ended_at"`
	Shared             bool       `json:"shared_with_other_cards"`
	Timing             *struct {
		Seconds         map[string]float64       `json:"seconds"`
		Phases          map[string]float64       `json:"phases"`
		Basis           string                   `json:"basis"`
		IncompleteTools int                      `json:"incomplete_tools"`
		Hours           []activity.TimingHour    `json:"hours,omitempty"`
		GateRuns        []activity.GateRunBucket `json:"gate_runs,omitempty"`
	} `json:"timing"`
	Cards []string `json:"cards"`
}
type insightActivityMark struct {
	Label       string     `json:"label,omitempty"`
	Kind        string     `json:"kind,omitempty"`
	CardLinks   []string   `json:"card_links,omitempty"`
	SessionID   string     `json:"session_id"`
	BoardID     string     `json:"board_id"`
	Card        int        `json:"card"`
	By          string     `json:"by,omitempty"`
	Target      string     `json:"target,omitempty"`
	Round       int        `json:"round,omitempty"`
	Verdict     string     `json:"verdict,omitempty"`
	Findings    []string   `json:"findings,omitempty"`
	Severity    string     `json:"severity,omitempty"`
	Type        string     `json:"type,omitempty"`
	Suite       string     `json:"suite,omitempty"`
	RerunPassed bool       `json:"rerun_passed,omitempty"`
	Source      string     `json:"source,omitempty"`
	MessageID   string     `json:"message_id,omitempty"`
	Occurrence  string     `json:"occurrence,omitempty"`
	TurnSeq     int        `json:"turn_seq,omitempty"`
	At          *time.Time `json:"at,omitempty"`
	RecordedAt  time.Time  `json:"recorded_at"`
}
type insightActivityCard struct {
	Latest         time.Time       `json:"-"`
	Key            string          `json:"key"`
	Reference      string          `json:"reference,omitempty"`
	Title          string          `json:"title,omitempty"`
	Size           *string         `json:"size"`
	Risk           *string         `json:"risk"`
	Complexity     *string         `json:"complexity"`
	SizeFirstSetAt *string         `json:"size_first_set_at"`
	Flow           json.RawMessage `json:"flow"`
}
type insightActivityReport struct {
	Skills            skills.Report              `json:"skills"`
	Scope             string                     `json:"scope"`
	GeneratedAt       time.Time                  `json:"generated_at"`
	From              time.Time                  `json:"from"`
	To                time.Time                  `json:"to"`
	Cards             []insightActivityCard      `json:"cards"`
	Sessions          []insightActivitySession   `json:"sessions"`
	Reviews           []insightActivityMark      `json:"reviews"`
	Failures          []insightActivityMark      `json:"failures"`
	Questions         []insightActivityQuestions `json:"questions"`
	Events            []insightActivityMark      `json:"events"`
	EventsUnavailable bool                       `json:"events_unavailable,omitempty"`
	History           *insightActivityHistory    `json:"history,omitempty"`
	OmittedCards      int                        `json:"omitted_cards"`
	PendingSessions   int                        `json:"pending_sessions"`
	Truncated         bool                       `json:"truncated"`
}

type insightActivityHistory struct {
	From     time.Time                `json:"from"`
	Sessions []insightActivitySession `json:"sessions"`
	Failures []insightActivityMark    `json:"failures"`
}

type insightActivityQuestions struct {
	SessionID          string   `json:"session_id"`
	Asked              int      `json:"asked"`
	ReachedPerson      int      `json:"reached_person"`
	Cards              []string `json:"cards"`
	AttributionUnknown bool     `json:"card_attribution_unknown,omitempty"`
}

// HandleInsightsActivity projects BLG-368 reads into an owner-only report.
// Live Board authorization precedes every card read. There is no admin scope
// override and no raw commands, outputs, review prose, tokens or costs returned.
func (a *API) HandleInsightsActivity(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if apiErr := checkGrantRequester(requesterOf(p)); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if p.Project != "" {
		writeError(w, 403, "Insights requires an account-wide sign-in.")
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	workspace := r.URL.Query().Get("workspace")
	if workspace != "" {
		root := a.insightsWorkspaceRoot(r.Context(), p.Sub, workspace)
		if root == nil {
			writeError(w, 404, "workspace not found")
			return
		}
		workspace = root.ID
	}
	label := r.URL.Query().Get("range")
	if label == "" {
		label = "7d"
	}
	span, ok := insightsRanges[label]
	allWorkspace := label == "all" && workspace != ""
	if !ok && !allWorkspace {
		writeError(w, 400, "range must be 24h, 7d, 30d or 90d")
		return
	}
	now := time.Now().UTC()
	from := now.Add(-span).Truncate(time.Hour)
	if allWorkspace {
		from = time.Time{}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out := insightActivityReport{Scope: "mine", GeneratedAt: now, From: from, To: now, Cards: []insightActivityCard{}, Sessions: []insightActivitySession{}, Reviews: []insightActivityMark{}, Failures: []insightActivityMark{}, Questions: []insightActivityQuestions{}}
	queryFrom := from
	if !allWorkspace {
		queryFrom = from.Add(-span)
		out.History = &insightActivityHistory{From: queryFrom, Sessions: []insightActivitySession{}, Failures: []insightActivityMark{}}
	}
	allMarks := []insightActivityMark{}
	reply := func() {
		sort.SliceStable(out.Cards, func(i, j int) bool {
			if out.Cards[i].Latest.Equal(out.Cards[j].Latest) {
				return out.Cards[i].Key < out.Cards[j].Key
			}
			return out.Cards[i].Latest.After(out.Cards[j].Latest)
		})
		if len(out.Cards) > 50 {
			out.Cards = out.Cards[:50]
			out.Truncated = true
		}
		allowed := map[string]bool{}
		for _, c := range out.Cards {
			allowed[c.Key] = true
		}
		for i := range out.Sessions {
			s := &out.Sessions[i]
			visible := []string{}
			for _, key := range s.Cards {
				if allowed[key] {
					visible = append(visible, key)
				} else {
					s.AttributionUnknown = true
				}
			}
			s.Cards = visible
		}
		if out.History != nil {
			for i := range out.History.Sessions {
				s := &out.History.Sessions[i]
				visible := []string{}
				for _, key := range s.Cards {
					if allowed[key] {
						visible = append(visible, key)
					} else {
						s.AttributionUnknown = true
					}
				}
				s.Cards = visible
			}
		}
		for i := range out.Questions {
			q := &out.Questions[i]
			visible := []string{}
			for _, key := range q.Cards {
				if allowed[key] {
					visible = append(visible, key)
				} else {
					q.AttributionUnknown = true
				}
			}
			q.Cards = visible
		}
		for _, m := range allMarks {
			if m.BoardID != "" {
				if !allowed[m.BoardID+"#"+strconv.Itoa(m.Card)] {
					continue
				}
			} else if len(m.CardLinks) > 0 {
				found := false
				for _, key := range m.CardLinks {
					if !allowed[key] {
						continue
					}
					if index := strings.LastIndexByte(key, '#'); index >= 0 {
						m.BoardID = key[:index]
						m.Card, _ = strconv.Atoi(key[index+1:])
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			m.CardLinks = nil
			at := m.RecordedAt
			if m.At != nil {
				at = *m.At
			}
			if at.Before(from) {
				if out.History != nil && m.Kind == "failure" {
					out.History.Failures = append(out.History.Failures, m)
				}
				continue
			}
			switch m.Kind {
			case "milestone":
				out.Events = append(out.Events, m)
			case "review":
				out.Reviews = append(out.Reviews, m)
			default:
				out.Failures = append(out.Failures, m)
			}
		}
		sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].ID < out.Sessions[j].ID })
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
	failed := func(err error, operation string) {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			out.Truncated = true
			reply()
			return
		}
		writeDatabaseFailure(w, operation, "insights", err)
	}
	selected, refs, truncated, exists, err := db.InsightActivityScope(ctx, a.dbPool, p.Sub, workspace, queryFrom, now)
	if err != nil {
		failed(err, "read Insights scope")
		return
	}
	if !exists {
		writeError(w, 404, "Workspace not found.")
		return
	}
	out.Truncated = truncated
	out.Skills = db.AvailableSkillTelemetry(ctx, a.dbPool, p.Sub, workspace, from, now)
	caller := a.sessionBoardCaller()
	out.Cards, out.Events, out.EventsUnavailable = readInsightsBoards(ctx, caller, mcpgw.Proof{AccountID: p.Sub, SessionID: p.Sid}, refs, from, now)
	out.OmittedCards = len(refs) - len(out.Cards)
	markRaw, err := db.InsightActivityMarks(ctx, a.dbPool, p.Sub, workspace, queryFrom, now)
	if err != nil {
		failed(err, "read Insights marks")
		return
	}
	if err = json.Unmarshal(markRaw, &allMarks); err != nil {
		failed(err, "decode Insights marks")
		return
	}
	questionRaw, err := db.InsightActivityQuestions(ctx, a.dbPool, p.Sub, workspace, from, now)
	if err != nil {
		failed(err, "read Insights questions")
		return
	}
	if err = json.Unmarshal(questionRaw, &out.Questions); err != nil {
		failed(err, "decode Insights questions")
		return
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	chartTo := now.Truncate(time.Hour)
	chartFrom := from.Truncate(time.Hour)
	if chartFrom.Before(from) {
		chartFrom = chartFrom.Add(time.Hour)
	}
	chartPrevious := chartFrom.Add(-chartTo.Sub(chartFrom))
	timingRaw, err := db.InsightActivityTimings(ctx, a.dbPool, p.Sub, ids, chartPrevious, chartFrom, chartTo)
	if err != nil {
		failed(err, "read Insights timing")
		return
	}
	var timings struct {
		Sessions []insightActivitySession `json:"sessions"`
		Partial  bool                     `json:"partial"`
		Pending  int                      `json:"pending_sessions"`
	}
	if err = json.Unmarshal(timingRaw, &timings); err != nil {
		failed(err, "decode Insights timing")
		return
	}
	out.Sessions = timings.Sessions
	if out.History != nil {
		// Apply the same live card redaction to both periods in reply().
		for _, s := range timings.Sessions {
			if s.StartedAt != nil && s.StartedAt.Before(from) {
				out.History.Sessions = append(out.History.Sessions, s)
			}
		}
		out.Sessions = []insightActivitySession{}
		for _, s := range timings.Sessions {
			if s.EndedAt == nil || !s.EndedAt.Before(from) {
				out.Sessions = append(out.Sessions, s)
			}
		}
	}
	out.PendingSessions = timings.Pending
	out.Truncated = out.Truncated || (timings.Partial && timings.Pending == 0)
	reply()
}
