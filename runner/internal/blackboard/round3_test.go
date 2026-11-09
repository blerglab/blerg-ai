package blackboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

func TestRound3PendingAgentBudgetsAndPersonIsolation(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	agent := Actor{"worker", "agent"}
	ids := []ProposalID{}
	for n := 0; n < MaxPendingAgentProposals; n++ {
		id, err := s.Propose(ctx, agent, "m", []Change{{ID: ItemID(fmt.Sprint(n)), Operation: "add", Item: Item{Title: "idea"}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := s.Propose(ctx, agent, "m", []Change{{Operation: "add", Item: Item{Title: "overflow"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pending count: %v", err)
	}
	if err := s.Apply(ctx, p, "m", 0, []Change{{Operation: "add", Item: Item{Title: "person"}}}); err != nil {
		t.Fatalf("Apply blocked: %v", err)
	}
	if err := s.Decide(ctx, p, "m", ids[0], 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, agent, "m", []Change{{Operation: "add", Item: Item{Title: "room after rejection"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		// Upgrade-shaped history exceeds the former aggregate 16 MiB bound.
		for n := 0; n < 165; n++ {
			r.Proposals = append(r.Proposals, Proposal{ID: ProposalID(NewID()), MilestoneID: "m", Actor: agent, Status: "rejected", Changes: []Change{{Item: Item{Title: strings.Repeat("x", 100<<10)}}}})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, p, "m", 1, []Change{{Operation: "add", Item: Item{Title: "after history"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(ctx, p, "m", ids[1], 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := seedLegacyFrozenView(ctx, s, "m", ""); err != nil {
		t.Fatal(err)
	}
}
func TestRound3AgentProposalByteBudget(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	agent := Actor{"worker", "agent"}
	change := Change{Operation: "add", Item: Item{Title: strings.Repeat("t", 200), Reason: strings.Repeat("r", 1000)}}
	changes := make([]Change, 100)
	for n := range changes {
		changes[n] = change
	}
	accepted := 0
	for n := 0; n < MaxPendingAgentProposals; n++ {
		if _, err := s.Propose(ctx, agent, "m", append([]Change(nil), changes...)); errors.Is(err, ErrInvalid) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted == 0 || accepted >= MaxPendingAgentProposals {
		t.Fatalf("byte cap not independent: %d", accepted)
	}
	r, _ := s.Store.Load(ctx, "b")
	total := 0
	for _, q := range r.Proposals {
		data, _ := json.Marshal(q)
		total += len(data)
	}
	if total > MaxPendingAgentProposalBytes {
		t.Fatalf("pending bytes %d", total)
	}
}
func TestRound3OnlyPendingProposalsCount(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		for n := 0; n < 1000; n++ {
			r.Proposals = append(r.Proposals, Proposal{ID: ProposalID(NewID()), Actor: Actor{"worker", "agent"}, MilestoneID: "m", Status: "rejected"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{{Operation: "add", Item: Item{Title: "new"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, p, "m", 0, []Change{{Operation: "add", Item: Item{Title: "person"}}}); err != nil {
		t.Fatal(err)
	}
}

type partitionSpy struct {
	Store
	PartitionStore
	observations int
}

func (s *partitionSpy) UpdatePartition(c context.Context, id string, p Partition, fn func(*Record) error) error {
	if p.Kind == PartitionObservation {
		s.observations++
	}
	return s.PartitionStore.UpdatePartition(c, id, p, fn)
}
func TestRound3UnchangedSliceDoesNotLockOrWrite(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil }); err != nil {
		t.Fatal(err)
	}
	spy := &partitionSpy{Store: s.Store, PartitionStore: s.Store.(PartitionStore)}
	s.Store = spy
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "work"), 2: boardCard(2, "next")}}
	if _, err := s.Slice(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	first := spy.observations
	if first != 1 {
		t.Fatalf("initial batch writes %d", first)
	}
	for n := 0; n < 3; n++ {
		if _, err := s.Slice(ctx, "m"); err != nil {
			t.Fatal(err)
		}
	}
	if spy.observations != first {
		t.Fatalf("unchanged reads locked/wrote: %d", spy.observations)
	}
}
func TestRound3ReleaselessMovedOutIsNotLive(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	if err := s.Apply(ctx, p, "m", 0, []Change{addChange("item", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(ctx, p, "m", 1, []Change{{Operation: "remove", Item: Item{BoardID: "board", CardNumber: 1, Reason: "Too risky", Destination: "later"}}}); err != nil {
		t.Fatal(err)
	}
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "work")}}
	v, err := s.Slice(ctx, "m")
	if err != nil || v.Counts["all"] != 0 || v.Counts["moved_out"] != 1 {
		t.Fatalf("release-less counts %+v %v", v, err)
	}
}
func TestRound3ObservationBothDedupeClauses(t *testing.T) {
	s, _ := planFixture(t)
	item := Item{BoardID: "board", CardNumber: 1, State: "planned", Reason: "why"}
	raw, _ := json.Marshal(item)
	for _, field := range []string{"same", "kind", "payload"} {
		t.Run(field, func(t *testing.T) {
			r := Record{Events: []Event{{Source: "board", MilestoneID: "m", Kind: "planned", Payload: raw}}}
			kind := "planned"
			next := item
			if field == "kind" {
				kind = "different"
			}
			if field == "payload" {
				next.Reason = "new reason"
			}
			s.observation(&r, "m", kind, next)
			want := 2
			if field == "same" {
				want = 1
			}
			if len(r.Events) != want {
				t.Fatalf("dedupe %s: %d", field, len(r.Events))
			}
		})
	}
}
func captureRound3Failures(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { failurelog.Default = old })
	return buf
}
func TestRound3ObservationBudgetSkipLogged(t *testing.T) {
	s, _ := planFixture(t)
	buf := captureRound3Failures(t)
	r := Record{Usage: &PartitionUsage{Events: MaxAutomaticEvents}}
	s.observation(&r, "m", "planned", Item{BoardID: "board", CardNumber: 1})
	if len(r.Events) != 0 || !strings.Contains(buf.String(), `"stage":"observation_budget"`) {
		t.Fatalf("unlogged budget skip: %s", buf.String())
	}
}
func TestRound3ObservationWriteFailureLogged(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil }); err != nil {
		t.Fatal(err)
	}
	buf := captureRound3Failures(t)
	s.Store = failedObservationStore{s.Store}
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
	if v, err := s.Slice(ctx, "m"); err != nil || !v.Complete {
		t.Fatalf("read %+v %v", v, err)
	}
	if !strings.Contains(buf.String(), `"cause":"*errors.errorString"`) {
		t.Fatalf("missing failure log: %s", buf.String())
	}
}
func TestRound3PartitionEventCapacityBothBounds(t *testing.T) {
	for _, u := range []PartitionUsage{{Events: MaxAutomaticEvents}, {EventBytes: MaxPartitionEventBytes}, {Events: MaxAutomaticEvents - 1, EventBytes: MaxPartitionEventBytes - (32 << 10) - 1}} {
		r := Record{Usage: &u}
		want := u.Events < MaxAutomaticEvents && u.EventBytes < MaxPartitionEventBytes-(32<<10)
		if automaticCapacity(r) != want {
			t.Fatalf("capacity %+v", u)
		}
	}
}
func TestRound3FinalizingBlocksCommitAllowsReject(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("item", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].Stage = "Finalizing"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"item"}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("Finalizing commit: %v", err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, nil); err != nil {
		t.Fatalf("Finalizing reject: %v", err)
	}
}

func TestRound3ForeignDeletedMovedOutAndPartialCounts(t *testing.T) {
	for _, state := range []string{"deleted", "moved_out", "column_error"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, _ := planFixture(t)
			if err := s.Store.Update(ctx, "b", func(r *Record) error {
				r.Milestones[0].ReleaseID = "release"
				r.Items = []Item{{ID: "foreign", BoardID: "foreign", CardNumber: 2, MilestoneID: "m", State: "planned", Reason: "Too risky", Destination: "r2"}}
				if state == "moved_out" {
					r.Items[0].State = "moved_out"
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "work"), 2: boardCard(2, "next")}}
			s.Board = boardFunc(func(c context.Context, tool string, a map[string]any, o any) error {
				if a["board_id"] == "foreign" {
					if tool == "blerg_card_search" {
						page := o.(*CardPage)
						if state != "deleted" {
							card := boardCard(2, "next")
							card.BoardID = "foreign"
							page.Cards = []Card{card}
						}
						return nil
					}
					if tool == "blerg_card_get" && state == "deleted" {
						return ErrNotFound
					}
					if tool == "blerg_column_list" {
						return errors.New("foreign columns unavailable")
					}
				}
				return b.Call(c, tool, a, o)
			})
			v, err := s.Slice(ctx, "m")
			if state == "column_error" {
				if err == nil || v.Complete || v.Counts["all"] != 2 {
					t.Fatalf("partial counts %+v %v", v, err)
				}
				return
			}
			if err != nil || !v.Complete || v.Counts["moved_out"] != 1 {
				t.Fatalf("foreign guard %+v %v", v, err)
			}
		})
	}
}

func TestRound3CapacityDoesNotMarshalHeader(t *testing.T) {
	r := Record{Vision: strings.Repeat("x", MaxRecordBytes-4096), Usage: &PartitionUsage{Events: 1, EventBytes: 100}}
	got := false
	allocs := testing.AllocsPerRun(10, func() { got = automaticCapacity(r) })
	if !got || allocs != 0 {
		t.Fatalf("partition capacity inspected/marshaled plan header: allowed=%v allocations=%v", got, allocs)
	}
}

func TestRound3LargeCutDoesNotBurdenRejectOrAbort(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("item", 1)})
	if err != nil {
		t.Fatal(err)
	}
	cut := json.RawMessage(`{"cards":[],"evidence":"` + strings.Repeat("x", MaxRecordBytes+1024) + `"}`)
	if err = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].Stage = "Finalizing"; r.Milestones[0].Frozen = cut; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, nil); err != nil {
		t.Fatalf("cut blocked rejection: %v", err)
	}
	full, err := s.Store.Load(ctx, "b")
	if err != nil || !sameJSON(full.Milestones[0].Frozen, cut) {
		t.Fatalf("rejection rewrote cut: %v", err)
	}
	if err = seedClearedLegacyLock(ctx, s, "m"); err != nil {
		t.Fatalf("cut blocked abort: %v", err)
	}
	core, err := s.Store.(PartitionStore).LoadPartition(ctx, "b", Partition{Kind: PartitionCore})
	if err != nil || len(core.Milestones[0].Frozen) != 0 || core.Milestones[0].Stage != "Evidence incomplete" {
		t.Fatalf("cut in header or failed abort: %+v %v", core.Milestones, err)
	}
}

func TestRound3HeaderOmitsCutAndAuditRows(t *testing.T) {
	large := strings.Repeat("x", 1<<20)
	r := Record{Milestones: []Milestone{{ID: "m", Frozen: json.RawMessage(`{"evidence":"` + large + `"}`)}}, Proposals: []Proposal{{Changes: []Change{{Item: Item{Title: large}}}}}, Events: []Event{{Payload: json.RawMessage(`{"text":"` + large + `"}`)}}}
	data, err := json.Marshal(Header(r))
	if err != nil || len(data) > 4096 {
		t.Fatalf("history/cut entered header: bytes=%d err=%v", len(data), err)
	}
}

func TestRound3InvalidPlanHeader(t *testing.T) {
	for _, kind := range []string{"json", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			r := Record{ID: "invalid", Owner: Actor{ID: "owner", Kind: "human"}, Version: 1}
			if kind == "json" {
				r.Style = json.RawMessage(`{`)
			} else {
				r.Vision = strings.Repeat("x", MaxRecordBytes)
			}
			if err := NewMemoryStore().Create(context.Background(), r); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid header %s accepted: %v", kind, err)
			}
		})
	}
}
