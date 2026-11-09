package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
)

// Board is a proof-bound gateway adapter. Call must preserve authorization failures
// and map only an actual missing card to ErrNotFound.
type Board interface {
	Call(context.Context, string, map[string]any, any) error
}
type Release struct {
	ID      string `json:"id"`
	BoardID string `json:"board_id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}
type Column struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Terminal bool   `json:"is_terminal"`
}
type Card struct {
	ID         string   `json:"id"`
	BoardID    string   `json:"board_id"`
	Number     int      `json:"number"`
	Reference  string   `json:"reference,omitempty"`
	Title      string   `json:"title"`
	ColumnID   *string  `json:"column_id"`
	ReleaseID  *string  `json:"release_id"`
	Tags       []string `json:"tags"`
	ArchivedAt *string  `json:"archived_at"`
	Version    int      `json:"version"`
	NeedsYou   bool     `json:"needs_you"`
	Type       string   `json:"type"`
	Priority   string   `json:"priority"`
	Size       *string  `json:"size"`
	Risk       *string  `json:"risk"`
	Complexity *string  `json:"complexity"`
	Rank       string   `json:"rank"`
}
type CardPage struct {
	Cards      []Card `json:"cards"`
	NextCursor string `json:"next_cursor"`
}
type SliceCard struct {
	Card        Card   `json:"card"`
	Membership  Item   `json:"membership"`
	Stage       string `json:"stage"`
	NeedsReason bool   `json:"needs_reason"`
	Drift       bool   `json:"drift"`
	Deleted     bool   `json:"deleted"`
	Live        *Card  `json:"live,omitempty"`
	Baseline    *Card  `json:"baseline,omitempty"`
}
type SliceView struct {
	RunnerRecord *Record             `json:"runner_record,omitempty"`
	BoardCut     *releasecut.Receipt `json:"board_cut,omitempty"`
	Milestone    Milestone           `json:"milestone"`
	Cards        []SliceCard         `json:"cards"`
	Counts       map[string]int      `json:"counts"`
	ObservedAt   time.Time           `json:"observed_at"`
	Complete     bool                `json:"complete"`
	Frozen       bool                `json:"frozen"`
}

func (m Mapping) Stage(c Card, columns []Column) string {
	if c.NeedsYou || slices.Contains(c.Tags, defaultString(m.NeedsTag, "needs-discussion")) {
		return "needs_you"
	}
	col := ""
	if c.ColumnID != nil {
		col = *c.ColumnID
	}
	// A finished card the person has not confirmed yet is still to test, whichever column holds it.
	if slices.Contains(c.Tags, defaultString(m.TestTag, "to-test")) {
		return "to_test"
	}
	if slices.Contains(m.DoneColumns, col) {
		return "done"
	}
	for _, v := range columns {
		if v.ID == col && (v.Terminal || strings.EqualFold(strings.TrimSpace(v.Name), "done")) {
			return "done"
		}
	}
	if slices.Contains(m.WorkColumns, col) {
		return "in_work"
	}
	if len(m.WorkColumns) == 0 {
		for _, v := range columns {
			if v.ID == col {
				switch strings.ToLower(v.Name) {
				case "in progress", "doing", "working":
					return "in_work"
				}
			}
		}
	}
	return "next"
}
func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
func (s Service) boardCall(ctx context.Context, m Milestone, tool string, args map[string]any, out any) error {
	if s.Board == nil {
		return ErrInvalid
	}
	args["board_id"] = m.BoardID
	return s.Board.Call(ctx, tool, args, out)
}
func (s Service) Releases(ctx context.Context, board string) ([]Release, error) {
	var out []Release
	if s.Board == nil {
		return nil, ErrInvalid
	}
	err := s.Board.Call(ctx, "blerg_release_list", map[string]any{"board_id": board}, &out)
	return out, err
}
func (s Service) readCards(ctx context.Context, m Milestone) ([]Card, []Column, error) {
	var columns []Column
	if err := s.boardCall(ctx, m, "blerg_column_list", map[string]any{}, &columns); err != nil {
		return nil, nil, err
	}
	cards := []Card{}
	seen := map[int]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for {
		var page CardPage
		err := s.boardCall(ctx, m, "blerg_card_search", map[string]any{"release_id": m.ReleaseID, "include_archived": true, "page": true, "summary": true, "limit": 100, "cursor": cursor}, &page)
		if err != nil {
			return cards, columns, err
		}
		if len(page.Cards) > 100 {
			return cards, columns, ErrInvalid
		}
		for _, c := range page.Cards {
			if c.Number <= 0 || c.BoardID != m.BoardID || c.ReleaseID == nil || *c.ReleaseID != m.ReleaseID || seen[c.Number] {
				return cards, columns, ErrInvalid
			}
			seen[c.Number] = true
			cards = append(cards, c)
		}
		if page.NextCursor == "" {
			break
		}
		if cursors[page.NextCursor] || len(cursors) >= 1000 {
			return cards, columns, ErrInvalid
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return cards, columns, nil
}

// lookupItems reads each unique card once, in batches of at most 100 per board.
// Results are indexed by identity so board search order never changes plan order.
func (s Service) lookupItems(ctx context.Context, items []Item, mid string, seen map[cardKey]bool) (map[cardKey]Card, map[cardKey]error) {
	cards := map[cardKey]Card{}
	failures := map[cardKey]error{}
	boards := map[string][]int{}
	for _, item := range items {
		key := cardKey{item.BoardID, item.CardNumber}
		if item.MilestoneID != mid || item.BoardID == "" || seen[key] {
			continue
		}
		seen[key] = true
		boards[item.BoardID] = append(boards[item.BoardID], item.CardNumber)
	}
	for board, numbers := range boards {
		for start := 0; start < len(numbers); start += 100 {
			batch := numbers[start:min(start+100, len(numbers))]
			wanted := map[int]bool{}
			for _, n := range batch {
				wanted[n] = true
			}
			var page CardPage
			err := s.boardCall(ctx, Milestone{BoardID: board}, "blerg_card_search", map[string]any{"numbers": batch, "include_archived": true, "page": true, "summary": true, "limit": 100}, &page)
			if err == nil {
				if page.NextCursor != "" || len(page.Cards) > len(batch) {
					err = ErrInvalid
				}
				found := map[int]bool{}
				for _, c := range page.Cards {
					if c.BoardID != board || !wanted[c.Number] || found[c.Number] {
						err = ErrInvalid
						break
					}
					found[c.Number] = true
				}
			}
			for _, n := range batch {
				if err != nil {
					failures[cardKey{board, n}] = err
				} else {
					failures[cardKey{board, n}] = ErrNotFound
				}
			}
			if err == nil {
				for _, c := range page.Cards {
					key := cardKey{board, c.Number}
					cards[key] = c
					delete(failures, key)
				}
			}
		}
	}
	return cards, failures
}

func (s Service) Slice(ctx context.Context, mid string) (SliceView, error) {
	return s.slice(ctx, mid, true)
}
func (s Service) slice(ctx context.Context, mid string, observe bool) (SliceView, error) {
	if s.ReadScope != nil {
		var finishRead func()
		ctx, finishRead = s.ReadScope(ctx)
		defer finishRead()
	}
	r, err := s.loadPartition(ctx, Partition{Kind: PartitionObservation, MilestoneID: mid, IncludeCut: true})
	if err != nil {
		return SliceView{}, err
	}
	m, err := milestone(&r, mid)
	if err != nil {
		return SliceView{}, err
	}
	if m.FrozenAt != nil || m.EvidenceIncomplete && len(m.Frozen) > 0 {
		return s.frozenSlice(ctx, *m)
	}
	v := SliceView{Milestone: *m, Cards: []SliceCard{}, Counts: map[string]int{}, ObservedAt: s.now()}
	if m.BoardID == "" || m.ReleaseID == "" {
		v.Complete = true
		cards, failures := s.lookupItems(ctx, r.Items, mid, map[cardKey]bool{})
		columnCache := map[string][]Column{}
		for _, item := range r.Items {
			if item.MilestoneID != mid {
				continue
			}
			if item.BoardID != "" {
				linked := *m
				linked.BoardID = item.BoardID
				key := cardKey{item.BoardID, item.CardNumber}
				card := cards[key]
				err := failures[key]
				if errors.Is(err, ErrNotFound) {
					v.Cards = append(v.Cards, SliceCard{Card: Card{BoardID: item.BoardID, Number: item.CardNumber}, Membership: item, Stage: "moved_out", Deleted: true, NeedsReason: item.State != "moved_out" || item.Reason == ""})
					continue
				}
				if err != nil {
					v.Complete = false
					countSlice(&v)
					return v, err
				}
				columns, ok := columnCache[item.BoardID]
				if !ok {
					if err := s.boardCall(ctx, linked, "blerg_column_list", map[string]any{}, &columns); err != nil {
						v.Complete = false
						countSlice(&v)
						return v, err
					}
					columnCache[item.BoardID] = columns
				}
				item = releaseTagMembership(item, card, *m)
				stage := r.Mapping.Stage(card, columns)
				if item.State == "moved_out" {
					stage = "moved_out"
				}
				v.Cards = append(v.Cards, SliceCard{Card: card, Membership: item, Stage: stage})
				continue
			}
			stage := defaultString(item.Progress, "next")
			if item.State == "moved_out" {
				stage = "moved_out"
			}
			v.Cards = append(v.Cards, SliceCard{Card: Card{ID: string(item.ID), Title: item.Title}, Membership: item, Stage: stage})
		}
		if err := s.taggedUnplanned(ctx, r, *m, &v, columnCache); err != nil {
			v.Complete = false
			countSlice(&v)
			return v, err
		}
		if observe {
			err := s.updatePartition(ctx, Partition{Kind: PartitionObservation, MilestoneID: mid}, func(next *Record) error {
				current, e := milestone(next, mid)
				if e != nil {
					return e
				}
				if current.FrozenAt != nil || current.Stage == "Finalizing" {
					return nil
				}
				if current.Revision != m.Revision {
					return ErrConflict
				}
				s.observeCards(next, mid, v.Cards)
				return nil
			})
			if err != nil {
				failurelog.Default.Event("blackboard_observation", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
			}
		}
		countSlice(&v)
		return v, nil
	}
	cards, columns, readErr := s.readCards(ctx, *m)
	v.Complete = readErr == nil
	seen := map[cardKey]bool{}
	for _, c := range cards {
		seen[cardKey{c.BoardID, c.Number}] = true
		item := Item{MilestoneID: mid, BoardID: m.BoardID, CardNumber: c.Number, State: "unplanned"}
		for _, old := range r.Items {
			if old.MilestoneID == mid && old.BoardID == c.BoardID && old.CardNumber == c.Number {
				item = old
				if item.State == "moved_out" {
					item.State = "unplanned"
				}
				break
			}
		}
		item = releaseTagMembership(item, c, *m)
		stage := r.Mapping.Stage(c, columns)
		if item.State == "moved_out" {
			stage = "moved_out"
		}
		v.Cards = append(v.Cards, SliceCard{Card: c, Membership: item, Stage: stage, NeedsReason: item.Reason == ""})
	}
	if readErr != nil {
		countSlice(&v)
		return v, readErr
	}
	lookupSeen := map[cardKey]bool{}
	for key, value := range seen {
		lookupSeen[key] = value
	}
	extra, failures := s.lookupItems(ctx, r.Items, mid, lookupSeen)
	columnCache := map[string][]Column{m.BoardID: columns}
	for _, item := range r.Items {
		if item.MilestoneID == mid && item.BoardID == "" {
			stage := defaultString(item.Progress, "next")
			if item.State == "moved_out" {
				stage = "moved_out"
			}
			v.Cards = append(v.Cards, SliceCard{Card: Card{ID: string(item.ID), Title: item.Title}, Membership: item, Stage: stage})
			continue
		}
		if item.MilestoneID != mid || seen[cardKey{item.BoardID, item.CardNumber}] {
			continue
		}
		c := extra[cardKey{item.BoardID, item.CardNumber}]
		linked := *m
		linked.BoardID = item.BoardID
		err := failures[cardKey{item.BoardID, item.CardNumber}]
		deleted := errors.Is(err, ErrNotFound)
		if err != nil && !deleted {
			v.Complete = false
			countSlice(&v)
			return v, err
		}
		if deleted {
			c = Card{BoardID: item.BoardID, Number: item.CardNumber}
		} else {
			item = releaseTagMembership(item, c, *m)
		}
		if item.BoardID != m.BoardID && !deleted && item.State != "moved_out" {
			columns, ok := columnCache[item.BoardID]
			if !ok {
				if err := s.boardCall(ctx, linked, "blerg_column_list", map[string]any{}, &columns); err != nil {
					v.Complete = false
					countSlice(&v)
					return v, err
				}
				columnCache[item.BoardID] = columns
			}
			v.Cards = append(v.Cards, SliceCard{Card: c, Membership: item, Stage: r.Mapping.Stage(c, columns)})
			continue
		}
		if item.State != "moved_out" {
			item.State = "moved_out"
			item.Reason = ""
			item.Destination = ""
			if c.ReleaseID != nil {
				item.Destination = *c.ReleaseID
			}
		}
		v.Cards = append(v.Cards, SliceCard{Card: c, Membership: item, Stage: "moved_out", NeedsReason: item.Reason == "", Deleted: deleted})
	}
	if err := s.taggedUnplanned(ctx, r, *m, &v, columnCache); err != nil {
		v.Complete = false
		countSlice(&v)
		return v, err
	}
	// Reconciliation records observations, never silently approves membership.
	before := len(r.Events)
	if observe {
		s.observeCards(&r, mid, v.Cards)
	}
	if len(r.Events) > before {
		err = s.updatePartition(ctx, Partition{Kind: PartitionObservation, MilestoneID: mid}, func(next *Record) error {
			current, e := milestone(next, mid)
			if e != nil {
				return e
			}
			if current.FrozenAt != nil || current.Stage == "Finalizing" {
				return nil
			}
			if current.Revision != m.Revision {
				return ErrConflict
			}
			s.observeCards(next, mid, v.Cards)
			return nil
		})
	}
	countSlice(&v)
	sort.Slice(v.Cards, func(i, j int) bool { return v.Cards[i].Card.Number < v.Cards[j].Card.Number })
	// Observations are best-effort evidence; persistence cannot invalidate a read.
	if err != nil {
		failurelog.Default.Event("blackboard_observation", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
	}
	return v, nil
}

// Index latest evidence once per batch. Each card compares only its own small
// payload; unrelated history and the plan header are never marshaled here.
func (s Service) observeCards(r *Record, mid string, cards []SliceCard) {
	latest := map[cardKey]Event{}
	if r.Usage == nil {
		r.Usage = &PartitionUsage{}
		for _, e := range r.Events {
			if e.Source == "board" && e.MilestoneID == mid {
				r.Usage.Events++
				raw, _ := json.Marshal(e)
				r.Usage.EventBytes += len(raw)
			}
		}
	}
	for _, e := range r.Events {
		if e.Source == "board" && e.MilestoneID == mid {
			var item Item
			if json.Unmarshal(e.Payload, &item) == nil {
				latest[cardKey{item.BoardID, item.CardNumber}] = e
			}
		}
	}
	for _, c := range cards {
		item := c.Membership
		if item.BoardID == "" {
			continue
		}
		key := cardKey{item.BoardID, item.CardNumber}
		one := Record{Usage: r.Usage}
		if e, ok := latest[key]; ok {
			one.Events = []Event{e}
		}
		n := len(one.Events)
		s.observation(&one, mid, item.State, item)
		if len(one.Events) > n {
			event := one.Events[n]
			r.Events = append(r.Events, event)
			latest[key] = event
		}
	}
}
func countSlice(v *SliceView) {
	for _, c := range v.Cards {
		v.Counts[c.Stage]++
		if c.Membership.State == "unplanned" {
			v.Counts["unplanned"]++
		}
		if c.Stage != "moved_out" {
			v.Counts["all"]++
		}
	}
}
func (s Service) observation(r *Record, mid, kind string, payload any) {
	raw, _ := json.Marshal(payload)
	// Deduplicate the latest observation of this card, while retaining transitions.
	var item Item
	_ = json.Unmarshal(raw, &item)
	for i := len(r.Events) - 1; i >= 0; i-- {
		e := r.Events[i]
		var previous Item
		if e.Source == "board" && e.MilestoneID == mid && json.Unmarshal(e.Payload, &previous) == nil && previous.BoardID == item.BoardID && previous.CardNumber == item.CardNumber {
			if e.Kind == kind && sameJSON(previous, item) {
				return
			}
			break
		}
	}
	if !automaticCapacity(*r) {
		failurelog.Default.Event("blackboard_observation_budget", slog.LevelWarn, "component", "runner", "stage", "observation_budget", "cause", failurelog.Cause(ErrInvalid))
		return
	}
	r.Events = append(r.Events, Event{ID: NewID(), MilestoneID: mid, AuthenticatedActor: Actor{ID: "runner", Kind: "service"}, RecordedAt: s.now(), Source: "board", Kind: kind, Payload: raw})
	if r.Usage != nil {
		r.Usage.Events++
		r.Usage.EventBytes += len(raw) + 512
	}
}

type cardKey struct {
	board  string
	number int
}

func (s Service) frozenSlice(ctx context.Context, m Milestone) (SliceView, error) {
	var v SliceView
	if err := json.Unmarshal(m.Frozen, &v); err != nil {
		return v, err
	}
	v.Frozen = true
	v.Milestone = m
	v.Milestone.Frozen = nil
	var current []Card
	var err error
	if m.BoardID != "" && m.ReleaseID != "" {
		current, _, err = s.readCards(ctx, m)
	}
	if err != nil {
		v.Complete = false
		return v, err
	}
	seen := map[cardKey]bool{}
	for i := range v.Cards {
		entry := &v.Cards[i]
		if entry.Membership.BoardID == "" {
			continue
		}
		linked := m
		linked.BoardID = entry.Membership.BoardID
		seen[cardKey{entry.Membership.BoardID, entry.Card.Number}] = true
		var live Card
		err := s.boardCall(ctx, linked, "blerg_card_get", map[string]any{"number": entry.Card.Number}, &live)
		if errors.Is(err, ErrNotFound) {
			entry.Drift = true
			entry.Deleted = true
			continue
		}
		if err != nil {
			v.Complete = false
			return v, err
		}
		entry.Live = &live
		baseline := entry.Card
		if entry.Baseline != nil {
			baseline = *entry.Baseline
		}
		entry.Drift = cardChanged(baseline, live)
	}
	for _, c := range current {
		if !seen[cardKey{c.BoardID, c.Number}] {
			v.Cards = append(v.Cards, SliceCard{Card: c, Membership: Item{BoardID: c.BoardID, CardNumber: c.Number, State: "drift"}, Stage: "drift", Drift: true})
		}
	}
	return v, nil
}
func cardChanged(a, b Card) bool {
	return a.Title != b.Title || !reflect.DeepEqual(a.ColumnID, b.ColumnID) || !reflect.DeepEqual(a.ReleaseID, b.ReleaseID) || !reflect.DeepEqual(a.ArchivedAt, b.ArchivedAt) || !reflect.DeepEqual(a.Tags, b.Tags)
}

// Final is retired. Only the server's verified board-receipt transaction may finalize.
func (s Service) Final(context.Context, Person, string, int, string) error { return ErrForbidden }

// AbortFinal is retired. Legacy transient locks are cleared automatically with an audit.
func (s Service) AbortFinal(context.Context, Person, string, int) error { return ErrForbidden }
