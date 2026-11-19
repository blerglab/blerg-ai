package blackboard

import (
	"context"
	"encoding/json"
)

const (
	PartitionCore                = "core"
	PartitionPlan                = "plan"
	PartitionProposal            = "proposal"
	PartitionEvent               = "event"
	PartitionObservation         = "observation"
	MaxPendingAgentProposals     = 20
	MaxPendingAgentProposalBytes = 256 << 10
	MaxPartitionEventBytes       = 1 << 20
	MaxWorkspacePendingProposals = 80
	MaxWorkspaceProposalBytes    = 1 << 20
	MaxWorkspaceEvents           = 32000
	MaxWorkspaceEventBytes       = 4 << 20
)

// Partition scopes are internal storage contracts, never caller-provided grants.
// Automatic SQL writers use one nonblocking write gate before admission and
// milestone state. Plan
// callbacks receive only the selected proposal and lifecycle evidence, not audit
// history. Full Load/Update remain available for exports and maintenance.
type Partition struct {
	Kind, MilestoneID, ProposalID, EventID, EventKind, ActorKind, ActorID string
	Selected                                                              []ItemID
	Revision                                                              int
	IncludeCut                                                            bool
	TryOnly                                                               bool
}
type PartitionUsage struct {
	Pending, ProposalBytes, Events, EventBytes                                     int
	WorkspacePending, WorkspaceProposalBytes, WorkspaceEvents, WorkspaceEventBytes int
}

// A blackboard's canonical workspace survives worker/session churn. Board-less
// records without a linked workspace use their own durable identity.
func WorkspaceKey(r Record) string {
	if r.WorkspaceID != "" {
		return r.WorkspaceID
	}
	if len(r.Links) > 0 && r.Links[0].WorkspaceID != "" {
		return r.Links[0].WorkspaceID
	}
	return r.ID
}

type PartitionStore interface {
	LoadPartition(context.Context, string, Partition) (Record, error)
	UpdatePartition(context.Context, string, Partition, func(*Record) error) error
}

func Core(r Record) Record {
	r.Events = nil
	r.Proposals = nil
	r.Revisions = nil
	r.Snapshots = nil
	r.Usage = nil
	return r
}

// Header excludes large cut observations as well as the audit partitions.
// Milestone rows own cut evidence; lifecycle guards need only stage/revision.
func Header(r Record) Record {
	r = Core(r)
	r.Milestones = append([]Milestone(nil), r.Milestones...)
	for i := range r.Milestones {
		r.Milestones[i].Frozen = nil
	}
	return r
}
func (s Service) loadPartition(ctx context.Context, p Partition) (Record, error) {
	if store, ok := s.Store.(PartitionStore); ok {
		return store.LoadPartition(ctx, s.BlackboardID, p)
	}
	return s.Store.Load(ctx, s.BlackboardID)
}
func (s Service) updatePartition(ctx context.Context, p Partition, fn func(*Record) error) error {
	if store, ok := s.Store.(PartitionStore); ok {
		return store.UpdatePartition(ctx, s.BlackboardID, p, fn)
	}
	return s.Store.Update(ctx, s.BlackboardID, fn)
}
func (s Service) planUpdate(ctx context.Context, mid string, fn func(*Record) error) error {
	return s.updatePartition(ctx, Partition{Kind: PartitionPlan, MilestoneID: mid}, fn)
}
func (s Service) planLoad(ctx context.Context, mid string) (Record, error) {
	return s.loadPartition(ctx, Partition{Kind: PartitionPlan, MilestoneID: mid})
}
func (s Service) cutUpdate(ctx context.Context, mid string, fn func(*Record) error) error {
	return s.updatePartition(ctx, Partition{Kind: PartitionPlan, MilestoneID: mid, IncludeCut: true}, fn)
}

func ProposalBudget(r Record, q Proposal) error {
	if q.Actor.Kind != "agent" {
		return nil
	}
	u := r.Usage
	if u == nil {
		u = &PartitionUsage{}
		for _, old := range r.Proposals {
			if old.Actor.Kind == "agent" && old.Status == "pending" && old.MilestoneID == q.MilestoneID {
				u.WorkspacePending++
				b, _ := json.Marshal(old)
				u.WorkspaceProposalBytes += len(b)
			}
			if old.Actor == q.Actor && old.Status == "pending" && old.MilestoneID == q.MilestoneID {
				u.Pending++
				b, _ := json.Marshal(old)
				u.ProposalBytes += len(b)
			}
		}
	}
	b, err := json.Marshal(q)
	if err != nil || u.Pending >= MaxPendingAgentProposals || u.ProposalBytes+len(b) > MaxPendingAgentProposalBytes || u.WorkspacePending >= MaxWorkspacePendingProposals || u.WorkspaceProposalBytes+len(b) > MaxWorkspaceProposalBytes {
		return ErrInvalid
	}
	return nil
}

// SelectPartition is also the provider fake's implementation of the bounded
// views used by the SQL provider; it never marshals unrelated history.
func SelectPartition(full Record, p Partition) Record {
	r := Header(full)
	if p.IncludeCut {
		for i, m := range full.Milestones {
			if m.ID == p.MilestoneID && (p.Kind == PartitionPlan || m.FrozenAt != nil) {
				r.Milestones[i].Frozen = m.Frozen
			}
		}
	}
	if p.Kind == PartitionCore {
		return r
	}
	r.Usage = &PartitionUsage{}
	for _, q := range full.Proposals {
		if q.MilestoneID != p.MilestoneID {
			continue
		}
		if p.Kind == PartitionProposal && q.Status == "pending" && q.Actor.Kind == "agent" {
			r.Usage.WorkspacePending++
			b, _ := json.Marshal(q)
			r.Usage.WorkspaceProposalBytes += len(b)
		}
		if p.Kind == PartitionProposal && q.Status == "pending" && q.Actor.Kind == "agent" && q.Actor.ID == p.ActorID {
			r.Usage.Pending++
			b, _ := json.Marshal(q)
			r.Usage.ProposalBytes += len(b)
		}
		if p.Kind == PartitionPlan && q.Status == "pending" && (len(p.Selected) == 0 || q.Revision == p.Revision) {
			match := string(q.ID) == p.ProposalID && p.ProposalID != ""
			if p.ProposalID == "" {
				for _, c := range q.Changes {
					for _, id := range p.Selected {
						match = match || c.ID == id
					}
				}
			}
			if match {
				r.Proposals = append(r.Proposals, q)
				if len(r.Proposals) == 2 {
					break
				}
			}
		}
	}
	source := "agent"
	if p.Kind == PartitionObservation {
		source = "board"
	}
	latest := map[cardKey]int{}
	lastNumber, lastIntent := -1, -1
	for i, e := range full.Events {
		if p.Kind == PartitionEvent && e.MilestoneID == p.MilestoneID && e.Source == "agent" {
			r.Usage.WorkspaceEvents++
			b, _ := json.Marshal(e)
			r.Usage.WorkspaceEventBytes += len(b)
		}
		if (p.Kind == PartitionEvent || p.Kind == PartitionObservation) && e.MilestoneID == p.MilestoneID && e.Source == source && (source == "board" || e.AuthenticatedActor.ID == p.ActorID) {
			r.Usage.Events++
			b, _ := json.Marshal(e)
			r.Usage.EventBytes += len(b)
		}
		if p.Kind == PartitionEvent && e.ID == p.EventID && p.EventID != "" {
			r.Events = append(r.Events, e)
		}
		if e.MilestoneID != p.MilestoneID {
			continue
		}
		if e.Source == "person" && e.Kind == "final_requested" {
			lastIntent = i
		}
		if e.Kind == p.EventKind && (e.Kind == "alpha" || e.Kind == "rc") {
			lastNumber = i
		}
		if e.Source == "board" {
			var item Item
			if json.Unmarshal(e.Payload, &item) == nil {
				latest[cardKey{item.BoardID, item.CardNumber}] = i
			}
		}
	}
	if p.Kind == PartitionPlan && lastIntent >= 0 {
		r.Events = append(r.Events, full.Events[lastIntent])
	}
	if p.Kind == PartitionEvent && lastNumber >= 0 && (len(r.Events) == 0 || r.Events[0].ID != full.Events[lastNumber].ID) {
		r.Events = append(r.Events, full.Events[lastNumber])
	}
	if p.Kind == PartitionObservation {
		for i, e := range full.Events {
			var item Item
			if e.Source == "board" && e.MilestoneID == p.MilestoneID && json.Unmarshal(e.Payload, &item) == nil && latest[cardKey{item.BoardID, item.CardNumber}] == i {
				r.Events = append(r.Events, e)
			}
		}
	}
	if p.Kind == PartitionPlan {
		for _, snap := range full.Snapshots {
			if snap.MilestoneID == p.MilestoneID {
				r.Snapshots = append(r.Snapshots, snap)
			}
		}
	}
	return r
}

func (s *MemoryStore) put(r Record) {
	s.rows[r.ID] = Header(r)
	s.history[r.ID] = Record{Events: r.Events, Proposals: r.Proposals, Revisions: r.Revisions, Snapshots: r.Snapshots, Milestones: r.Milestones}
}
func (s *MemoryStore) join(r Record) Record {
	h := s.history[r.ID]
	r.Milestones = append([]Milestone(nil), r.Milestones...)
	for i := range r.Milestones {
		for _, m := range h.Milestones {
			if m.ID == r.Milestones[i].ID {
				r.Milestones[i].Frozen = m.Frozen
				break
			}
		}
	}
	r.Events = h.Events
	r.Proposals = h.Proposals
	r.Revisions = h.Revisions
	r.Snapshots = h.Snapshots
	return r
}
func (s *MemoryStore) memoryPartition(r Record, p Partition) Record {
	view := SelectPartition(s.join(r), p)
	if view.Usage == nil {
		return view
	}
	for id, other := range s.rows {
		if id == r.ID || WorkspaceKey(other) != WorkspaceKey(r) {
			continue
		}
		v := SelectPartition(s.join(other), p)
		if v.Usage != nil {
			view.Usage.WorkspacePending += v.Usage.WorkspacePending
			view.Usage.WorkspaceProposalBytes += v.Usage.WorkspaceProposalBytes
			view.Usage.WorkspaceEvents += v.Usage.WorkspaceEvents
			view.Usage.WorkspaceEventBytes += v.Usage.WorkspaceEventBytes
		}
	}
	return view
}
func (s *MemoryStore) LoadPartition(_ context.Context, id string, p Partition) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return Clone(s.memoryPartition(r, p)), nil
}
func (s *MemoryStore) UpdatePartition(_ context.Context, id string, p Partition, fn func(*Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	core, ok := s.rows[id]
	if !ok {
		return ErrNotFound
	}
	old := Clone(s.memoryPartition(core, p))
	next := Clone(old)
	if err := fn(&next); err != nil {
		return err
	}
	if err := ValidateUpdate(old, next); err != nil {
		return err
	}
	if (p.Kind == PartitionProposal || p.Kind == PartitionObservation || p.Kind == PartitionEvent && p.ActorKind == "agent") && !sameJSON(Core(old), Core(next)) {
		return ErrImmutable
	}
	if p.Kind == PartitionProposal && p.ActorKind == "agent" {
		for _, q := range next.Proposals[len(old.Proposals):] {
			if err := ProposalBudget(old, q); err != nil {
				return err
			}
			raw, _ := json.Marshal(q)
			old.Usage.Pending++
			old.Usage.ProposalBytes += len(raw)
			old.Usage.WorkspacePending++
			old.Usage.WorkspaceProposalBytes += len(raw)
		}
	}
	if p.Kind == PartitionObservation || p.Kind == PartitionEvent && p.ActorKind == "agent" {
		added := next.Events[len(old.Events):]
		if len(added) > 0 {
			bytes := 0
			for _, e := range added {
				raw, _ := json.Marshal(e)
				bytes += len(raw)
			}
			if old.Usage.Events+len(added) > MaxAutomaticEvents || old.Usage.EventBytes+bytes > MaxPartitionEventBytes {
				return ErrInvalid
			}
			if p.Kind == PartitionEvent && (old.Usage.WorkspaceEvents+len(added) > MaxWorkspaceEvents || old.Usage.WorkspaceEventBytes+bytes > MaxWorkspaceEventBytes) {
				return ErrInvalid
			}
		}
	}
	h := s.history[id]
	for _, q := range next.Proposals {
		found := false
		for i, v := range h.Proposals {
			if v.ID == q.ID {
				h.Proposals[i] = q
				found = true
				break
			}
		}
		if !found {
			h.Proposals = append(h.Proposals, q)
		}
	}
	h.Events = append(h.Events, next.Events[len(old.Events):]...)
	h.Revisions = append(h.Revisions, next.Revisions...)
	for _, snap := range next.Snapshots {
		found := false
		for i, v := range h.Snapshots {
			if v.MilestoneID == snap.MilestoneID && v.Day == snap.Day {
				h.Snapshots[i] = snap
				found = true
				break
			}
		}
		if !found {
			h.Snapshots = append(h.Snapshots, snap)
		}
	}
	for i, m := range next.Milestones {
		if i >= len(old.Milestones) || !sameJSON(old.Milestones[i], m) {
			found := false
			for j, v := range h.Milestones {
				if v.ID == m.ID {
					h.Milestones[j] = m
					found = true
					break
				}
			}
			if !found {
				h.Milestones = append(h.Milestones, m)
			}
		}
	}
	s.rows[id] = Header(next)
	s.history[id] = h
	return nil
}
