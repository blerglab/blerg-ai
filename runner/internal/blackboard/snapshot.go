package blackboard

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// SnapshotDay records today's actual observation only. Incomplete observations
// can be repaired by a later complete read; retries never create two samples.
func (s Service) SnapshotDay(ctx context.Context, mid string) (Snapshot, error) {
	day := s.now().Format("2006-01-02")
	r, err := s.planLoad(ctx, mid)
	if err != nil {
		return Snapshot{}, err
	}
	m, err := milestone(&r, mid)
	if err != nil {
		return Snapshot{}, err
	}
	if mid == "" {
		return Snapshot{}, ErrInvalid
	}
	if m.FrozenAt != nil || m.Stage == "Finalizing" {
		return Snapshot{}, ErrFrozen
	}
	for _, v := range r.Snapshots {
		if v.MilestoneID == mid && v.Day == day && v.Complete {
			return v, nil
		}
	}
	view, readErr := s.Slice(ctx, mid)
	snap := Snapshot{MilestoneID: mid, Day: day, ObservedAt: view.ObservedAt, Complete: view.Complete && readErr == nil, Counts: view.Counts}
	if snap.ObservedAt.IsZero() {
		snap.ObservedAt = s.now()
	}
	day = snap.ObservedAt.UTC().Format("2006-01-02")
	snap.Day = day
	if snap.Counts == nil {
		snap.Counts = map[string]int{}
	}
	err = s.planUpdate(ctx, mid, func(r *Record) error {
		m, e := milestone(r, mid)
		if e != nil {
			return e
		}
		if m.FrozenAt != nil || m.Stage == "Finalizing" {
			return ErrFrozen
		}
		for i, v := range r.Snapshots {
			if v.MilestoneID == mid && v.Day == day {
				if !v.Complete && snap.Complete {
					r.Snapshots[i] = snap
					return nil
				}
				snap = v
				return nil
			}
		}
		r.Snapshots = append(r.Snapshots, snap)
		return nil
	})
	if err != nil {
		return snap, err
	}
	return snap, readErr
}
func (s Service) Timeline(ctx context.Context, mid string) ([]Event, []Snapshot, error) {
	r, err := s.Store.Load(ctx, s.BlackboardID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := milestone(&r, mid); err != nil {
		return nil, nil, err
	}
	events := []Event{}
	snapshots := []Snapshot{}
	for _, e := range r.Events {
		if e.MilestoneID == mid {
			events = append(events, e)
		}
	}
	for _, v := range r.Snapshots {
		if v.MilestoneID == mid {
			snapshots = append(snapshots, v)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].RecordedAt.Before(events[j].RecordedAt) })
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Day < snapshots[j].Day })
	return events, snapshots, nil
}

type EventInput struct {
	ID          string          `json:"id,omitempty"`
	MilestoneID string          `json:"milestone_id"`
	Kind        string          `json:"kind"`
	ClaimedAt   *time.Time      `json:"claimed_at"`
	Payload     json.RawMessage `json:"payload"`
}

// RecordEvent never establishes plan authority, even when payload claims a person
// approved something. Special lifecycle events have dedicated, person-only methods.
func (s Service) RecordEvent(ctx context.Context, actor Actor, input EventInput) (Event, error) {
	return s.recordEvent(ctx, actor, input, false)
}

// RecordCommitAssertion is an internal audit path, never a client event grant.
// The operation and outcome are fixed here rather than accepted from a body.
func (s Service) RecordCommitAssertion(ctx context.Context, actor Actor, mid string) error {
	if actor.Kind != "agent" {
		return ErrForbidden
	}
	_, err := s.recordEvent(ctx, actor, EventInput{MilestoneID: mid, Kind: "assertion", Payload: json.RawMessage(`{"operation":"plan_commit","accepted":false}`)}, true)
	return err
}

func (s Service) recordEvent(ctx context.Context, actor Actor, input EventInput, internalAssertion bool) (Event, error) {
	if actor.ID == "" || len(actor.ID) > 200 || (actor.Kind != "human" && actor.Kind != "agent") || len(input.MilestoneID) > 200 || len(input.ID) > 100 || len(input.Payload) > 16<<10 {
		return Event{}, ErrInvalid
	}
	custom := false
	switch input.Kind {
	case "assertion":
		if !internalAssertion {
			return Event{}, ErrInvalid
		}
	case "opened", "alpha", "rc", "date_moved", "decision", "correction", "shipped":
	case "final", "plan_set", "plan_committed", "release_imported":
		return Event{}, ErrInvalid
	default:
		custom = true
	}
	var payload map[string]any
	if len(input.Payload) > 0 {
		if json.Unmarshal(input.Payload, &payload) != nil || payload == nil {
			return Event{}, ErrInvalid
		}
	} else {
		payload = map[string]any{}
	}
	if input.Kind == "alpha" || input.Kind == "rc" {
		delete(payload, "number")
	}
	if input.ID == "" {
		input.ID = NewID()
	}
	source := "agent"
	if actor.Kind == "human" {
		source = "person"
	}
	event := Event{ID: input.ID, MilestoneID: input.MilestoneID, Kind: input.Kind, AuthenticatedActor: actor, RecordedAt: s.now(), ClaimedAt: input.ClaimedAt, Source: source}
	err := s.updatePartition(ctx, Partition{Kind: PartitionEvent, MilestoneID: input.MilestoneID, EventID: input.ID, EventKind: input.Kind, ActorKind: actor.Kind, ActorID: actor.ID, TryOnly: internalAssertion}, func(r *Record) error {
		if actor.Kind == "human" && actor != r.Owner {
			return ErrForbidden
		}
		var style ReleaseStyle
		if len(r.Style) > 0 && json.Unmarshal(r.Style, &style) != nil {
			return ErrInvalid
		}
		lifecycle := input.Kind == "opened" || input.Kind == "alpha" || input.Kind == "rc" || input.Kind == "shipped"
		if custom || style.Name != "" && lifecycle {
			if style.Validate() != nil {
				return ErrInvalid
			}
			allowed := false
			for _, stage := range style.Stages {
				if stage.Key == input.Kind {
					allowed = true
				}
			}
			if !allowed {
				return ErrInvalid
			}
		}
		m, e := milestone(r, input.MilestoneID)
		if e != nil {
			return e
		}
		for _, old := range r.Events {
			if old.ID == event.ID {
				oldPayload := map[string]any{}
				_ = json.Unmarshal(old.Payload, &oldPayload)
				if input.Kind == "alpha" || input.Kind == "rc" {
					delete(oldPayload, "number")
				}
				if old.Kind != event.Kind || old.MilestoneID != event.MilestoneID || old.AuthenticatedActor != actor || !sameJSON(oldPayload, payload) || !sameJSON(old.ClaimedAt, input.ClaimedAt) {
					return ErrConflict
				}
				event = old
				return nil
			}
		}
		if actor.Kind == "agent" && !automaticCapacity(*r) {
			return ErrInvalid
		}
		if m.FrozenAt != nil && input.Kind != "correction" && input.Kind != "assertion" {
			return ErrFrozen
		}
		if m.Stage == "Finalizing" && input.Kind != "assertion" {
			return ErrFrozen
		}
		if input.Kind == "alpha" || input.Kind == "rc" {
			if input.MilestoneID == "" {
				return ErrInvalid
			}
			if input.Kind == "alpha" {
				commit, ok := payload["commit"].(string)
				if !ok || commit == "" || len(commit) > 200 {
					return ErrInvalid
				}
			}
			n := 1
			for _, old := range r.Events {
				if old.MilestoneID == input.MilestoneID && old.Kind == input.Kind {
					var previous struct {
						Number int `json:"number"`
					}
					_ = json.Unmarshal(old.Payload, &previous)
					if previous.Number >= n {
						n = previous.Number + 1
					}
				}
			}
			payload["number"] = n
		}
		if input.Kind == "date_moved" && input.MilestoneID == "" {
			return ErrInvalid
		}
		if input.Kind == "date_moved" {
			if actor.Kind != "human" || actor.ID != r.Owner.ID {
				return ErrForbidden
			}
			target, ok := payload["target"].(string)
			if !ok {
				return ErrInvalid
			}
			at, e := time.Parse("2006-01-02", target)
			if e != nil {
				return ErrInvalid
			}
			m.Target = &at
			m.Version++
		}
		event.Payload, _ = json.Marshal(payload)
		r.Events = append(r.Events, event)
		return nil
	})
	return event, err
}
