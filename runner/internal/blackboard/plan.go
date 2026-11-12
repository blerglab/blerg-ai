package blackboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type Service struct {
	Store        Store
	Board        Board
	BlackboardID string
	Now          func() time.Time
	ReadScope    func(context.Context) (context.Context, func())
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func milestone(r *Record, id string) (*Milestone, error) {
	if id == "" {
		return &Milestone{Revision: r.PlanRevision}, nil
	}
	for i := range r.Milestones {
		if r.Milestones[i].ID == id {
			return &r.Milestones[i], nil
		}
	}
	return nil, ErrNotFound
}
func (s Service) Propose(ctx context.Context, actor Actor, mid string, changes []Change) (ProposalID, error) {
	id := ProposalID(NewID())
	if actor.ID == "" {
		return "", invalidPlan("actor.id", "required")
	}
	if err := planLengths(planField{"actor.id", actor.ID, 200}, planField{"milestone_id", mid, 200}); err != nil {
		return "", err
	}
	if len(changes) == 0 || len(changes) > 100 {
		return "", invalidPlan("changes", "requires 1–100 changes")
	}
	// Milestone creation belongs to the workspace, including after its last cut.
	workspaceOnly := true
	for _, c := range changes {
		workspaceOnly = workspaceOnly && c.Operation == "milestone_add"
	}
	if workspaceOnly {
		mid = ""
	}
	// Own the proposal payload before assigning IDs or resolving name snapshots.
	changes = append([]Change(nil), changes...)
	for i := range changes {
		if changes[i].Milestone != nil {
			m := *changes[i].Milestone
			changes[i].Milestone = &m
		}
		if changes[i].Thrust != nil {
			thrust := *changes[i].Thrust
			changes[i].Thrust = &thrust
		}
	}
	for _, c := range changes {
		if c.Operation == "milestone_add" {
			record, err := s.loadPartition(ctx, Partition{Kind: PartitionCore})
			if err != nil {
				return "", err
			}
			var errPreflight error
			s, errPreflight = s.PreflightMilestones(ctx, record, changes)
			if errPreflight != nil {
				return "", errPreflight
			}
			break
		}
	}
	err := s.updatePartition(ctx, Partition{Kind: PartitionProposal, MilestoneID: mid, ActorKind: actor.Kind, ActorID: actor.ID}, func(r *Record) error {
		m, err := milestone(r, mid)
		if err != nil {
			return fmt.Errorf("milestone_id: must identify an existing milestone: %w: %w", ErrInvalid, err)
		}
		if m.FrozenAt != nil || m.Stage == "Finalizing" {
			return fmt.Errorf("milestone_id: must be open: %w: %w", ErrInvalid, ErrFrozen)
		}
		seen := map[ItemID]bool{}
		for i := range changes {
			c := &changes[i]
			if err := planLengths(
				planField{"item.destination", c.Item.Destination, 200}, planField{"item.board_id", c.Item.BoardID, 200},
				planField{"item.milestone_id", c.Item.MilestoneID, 200}, planField{"item.goal_id", c.Item.GoalID, 200},
				planField{"item.thrust_id", c.Item.ThrustID, 200}, planField{"item.id", string(c.Item.ID), 100}, planField{"changes.id", string(c.ID), 100},
			); err != nil {
				return err
			}
			if c.ID == "" {
				c.ID = ItemID(NewID())
			}
			if seen[c.ID] {
				return invalidPlan("changes.id", "must be unique within a proposal")
			}
			seen[c.ID] = true
			if c.Operation == "milestone_add" {
				if err := s.prepareMilestoneChange(ctx, *r, c); err != nil {
					return err
				}
				continue
			}
			if c.Milestone != nil {
				return fmt.Errorf("milestone: only allowed for milestone_add: %w", ErrInvalid)
			}
			if isThrustOperation(c.Operation) {
				if err := prepareThrustChange(*r, mid, c); err != nil {
					return err
				}
				continue
			}
			if c.Thrust != nil {
				return invalidPlan("thrust", "only allowed for thrust operations")
			}
			if c.Operation == "link" || c.Operation == "unlink" {
				if err := validatePlanLinkShape(len(changes), *c); err != nil {
					return err
				}
				if err := validateBoardLink(*r, *c); err != nil {
					return err
				}
				c.Item.RaisedBy = actor
				continue
			}
			if c.Operation != "add" && c.Operation != "remove" && c.Operation != "move" {
				return invalidPlan("operation", "use add, remove, move, link, unlink, thrust_* or milestone_add")
			}
			if err := validatePlanItemShape(*c); err != nil {
				return err
			}
			c.Item.MilestoneID = mid
			if c.Item.ID != "" {
				owned := false
				for _, old := range r.Items {
					if old.ID == c.Item.ID && old.MilestoneID == mid && old.BoardID == c.Item.BoardID && old.CardNumber == c.Item.CardNumber {
						owned = true
					}
				}
				if !owned {
					return invalidPlan("item.id", "must identify this card or plain item in the selected milestone")
				}
			}
			if c.Item.ID == "" && c.Operation == "add" {
				c.Item.ID = ItemID(NewID())
			}
			if c.Item.BoardID != "" {
				if !boardLinked(*r, m.BoardID, c.Item.BoardID) {
					return &PlanLinkError{BoardID: c.Item.BoardID, Cause: "board_not_linked"}
				}
			}
			c.Item.RaisedBy = actor
			if c.Item.GoalID != "" {
				found := false
				for _, g := range r.Goals {
					if g.ID == c.Item.GoalID {
						found = true
					}
				}
				if !found {
					return invalidPlan("item.goal_id", "must identify an existing goal")
				}
			}
			if c.Item.Progress != "" && c.Item.Progress != "next" && c.Item.Progress != "in_work" && c.Item.Progress != "to_test" && c.Item.Progress != "done" && c.Item.Progress != "needs_you" {
				return invalidPlan("item.progress", "use next, in_work, to_test, done or needs_you")
			}
			if c.Operation == "add" && c.Item.ThrustID != "" {
				found := false
				for _, t := range r.Thrusts {
					if t.ID == c.Item.ThrustID && t.MilestoneID == mid {
						found = true
					}
				}
				if !found {
					return invalidPlan("item.thrust_id", "must identify a thrust in the selected milestone")
				}
			}
			if c.Operation == "move" && c.Item.BoardID != "" && c.Item.Destination == "" {
				return invalidPlan("item.destination", "required for move")
			}
		}
		q := Proposal{ID: id, MilestoneID: mid, Revision: m.Revision, Actor: actor, CreatedAt: s.now(), Changes: changes, Status: "pending"}
		if err := ProposalBudget(*r, q); err != nil {
			return invalidPlan("changes", "pending proposal count or byte budget exceeded")
		}
		r.Proposals = append(r.Proposals, q)
		return nil
	})
	// Unknown bare refusals must not be mislabeled as proven budget failures.
	if err == ErrInvalid { //nolint:errorlint // wrapped check errors already have their own diagnostic
		err = CheckFailed("store_unclassified_refusal", "store", "rejected without a check diagnostic; inspect runner logs")

	}
	return id, err
}

// Commit selects immutable change IDs from exactly one pending proposal. Unselected
// items are rejected. No caller-provided actor labels or standing grants confer authority.
func (s Service) Commit(ctx context.Context, p Person, mid string, revision int, selected []ItemID) error {
	return s.Decide(ctx, p, mid, "", revision, selected)
}
func (s Service) Decide(ctx context.Context, p Person, mid string, pid ProposalID, revision int, selected []ItemID) error {
	return s.updatePartition(ctx, Partition{Kind: PartitionPlan, MilestoneID: mid, ProposalID: string(pid), Selected: selected, Revision: revision}, func(r *Record) error {
		if p.Kind != "human" || p.ID != r.Owner.ID || p.LoginID == "" {
			return ErrForbidden
		}
		m, err := milestone(r, mid)
		if err != nil {
			return err
		}
		if (m.FrozenAt != nil || m.Stage == "Finalizing") && len(selected) > 0 {
			return ErrFrozen
		}
		if m.Revision != revision && len(selected) > 0 {
			return ErrConflict
		}
		ids := map[ItemID]bool{}
		for _, id := range selected {
			if ids[id] {
				return ErrInvalid
			}
			ids[id] = true
		}
		var proposal *Proposal
		for i := range r.Proposals {
			q := &r.Proposals[i]
			if q.MilestoneID != mid || q.Status != "pending" || (len(selected) > 0 && q.Revision != revision) || (pid != "" && q.ID != pid) {
				continue
			}
			match := len(selected) == 0 && pid != ""
			for _, c := range q.Changes {
				if ids[c.ID] {
					match = true
				}
			}
			if match {
				if proposal != nil {
					return ErrInvalid
				}
				proposal = q
			}
		}
		if proposal == nil {
			return ErrConflict
		}
		for id := range ids {
			found := false
			for _, c := range proposal.Changes {
				if c.ID == id {
					found = true
				}
			}
			if !found {
				return ErrInvalid
			}
		}
		for _, c := range proposal.Changes {
			if !ids[c.ID] {
				continue
			}
			if c.Operation == "milestone_add" {
				if err := s.applyMilestoneChange(ctx, r, c); err != nil {
					return err
				}
				continue
			}
			if isThrustOperation(c.Operation) {
				if err := applyThrustChange(r, mid, c); err != nil {
					return err
				}
				continue
			}
			if c.Operation == "link" || c.Operation == "unlink" {
				if err := validateBoardLink(*r, c); err != nil {
					return err
				}
				if c.Operation == "link" {
					r.Links = append(r.Links, Link{WorkspaceID: WorkspaceKey(*r), BoardID: c.Item.BoardID})
				} else {
					links := []Link{}
					for _, l := range r.Links {
						if l.BoardID == c.Item.BoardID {
							if l.WorkspaceID == "" {
								continue
							}
							l.BoardID = ""
						}
						// A separate workspace-only row may already be present.
						duplicate := false
						for _, existing := range links {
							if existing == l {
								duplicate = true
								break
							}
						}
						if !duplicate {
							links = append(links, l)
						}
					}
					r.Links = links
				}
				continue
			}
			idx := -1
			for i, v := range r.Items {
				if v.MilestoneID == mid && ((c.Item.BoardID != "" && v.BoardID == c.Item.BoardID && v.CardNumber == c.Item.CardNumber) || (c.Item.BoardID == "" && v.ID == c.Item.ID)) {
					idx = i
				}
			}
			if c.Operation == "add" {
				if c.Item.BoardID != "" && !boardLinked(*r, m.BoardID, c.Item.BoardID) {
					return &PlanLinkError{c.Item.BoardID, "board_not_linked"}
				}
				if c.Item.ThrustID != "" && thrustIndex(*r, mid, c.Item.ThrustID) < 0 {
					return ErrInvalid
				}
				item := c.Item
				item.State = "planned"
				if idx < 0 {
					if item.ID == "" {
						item.ID = ItemID(NewID())
					}
					r.Items = append(r.Items, item)
				} else {
					item.ID = r.Items[idx].ID
					r.Items[idx] = item
				}
			} else {
				if idx < 0 {
					return ErrInvalid
				}
				r.Items[idx].State = "moved_out"
				r.Items[idx].Reason = c.Item.Reason
				r.Items[idx].Destination = c.Item.Destination
			}
		}
		proposal.Status = "decided"
		proposal.Selected = append([]ItemID{}, selected...)
		if len(selected) == 0 {
			proposal.Status = "rejected"
			e := Event{ID: NewID(), MilestoneID: mid, AuthenticatedActor: p.Actor, RecordedAt: s.now(), Source: "person", Kind: "proposal_rejected"}
			e.Payload, _ = json.Marshal(map[string]any{"proposal_id": proposal.ID})
			r.Events = append(r.Events, e)
			r.Version++
			return nil
		}
		// Appending milestones may reallocate the slice.
		m, err = milestone(r, mid)
		if err != nil {
			return err
		}
		m.Revision++
		if mid == "" {
			r.PlanRevision = m.Revision
		}
		m.Version++
		r.Version++
		e := Event{ID: NewID(), MilestoneID: mid, AuthenticatedActor: p.Actor, RecordedAt: s.now(), Source: "person", Kind: "plan_set"}
		e.Payload, _ = json.Marshal(map[string]any{"proposal_id": proposal.ID, "selected": selected, "revision": m.Revision})
		r.Events = append(r.Events, e)
		items := []Item{}
		for _, item := range r.Items {
			if item.MilestoneID == mid {
				items = append(items, item)
			}
		}
		thrusts := []Thrust{}
		for _, t := range r.Thrusts {
			if t.MilestoneID == mid {
				thrusts = append(thrusts, t)
			}
		}
		r.Revisions = append(r.Revisions, Revision{Thrusts: thrusts, MilestoneID: mid, Number: m.Revision, Items: items, EventID: e.ID})
		return nil
	})
}

// Apply lets a person propose and approve their exact batch in one atomic action.
// Agents have no path to this method: Decide still checks verified owner and login.
func (s Service) Apply(ctx context.Context, p Person, mid string, revision int, changes []Change) error {
	return s.planUpdate(ctx, mid, func(r *Record) error {
		if p.Kind != "human" || p.ID != r.Owner.ID || p.LoginID == "" {
			return ErrForbidden
		}
		m, e := milestone(r, mid)
		if e != nil {
			return e
		}
		if m.Revision != revision {
			return ErrConflict
		}
		next := s
		next.Store = stagedStore{r}
		id, e := next.Propose(ctx, p.Actor, mid, changes)
		if e != nil {
			return e
		}
		q := r.Proposals[len(r.Proposals)-1]
		ids := make([]ItemID, len(q.Changes))
		for i, c := range q.Changes {
			ids[i] = c.ID
		}
		if e = next.Decide(ctx, p, mid, id, revision, ids); e != nil {
			return e
		}
		return nil
	})
}

type stagedStore struct{ record *Record }

func (s stagedStore) Create(context.Context, Record) error { return ErrInvalid }
func (s stagedStore) Load(_ context.Context, id string) (Record, error) {
	if s.record.ID != id {
		return Record{}, ErrNotFound
	}
	return Clone(*s.record), nil
}
func (s stagedStore) Update(_ context.Context, id string, fn func(*Record) error) error {
	if s.record.ID != id {
		return ErrNotFound
	}
	// This adapter is only used inside an owning Store.Update transaction. That
	// boundary supplies rollback, isolation and validation once for the whole batch.
	return fn(s.record)
}

// PlanLinkError exposes a useful refusal while keeping diagnostic causes free of board data.
type PlanLinkError struct {
	BoardID string
	Cause   string
}

func (e *PlanLinkError) Unwrap() error { return ErrInvalid }
func (e *PlanLinkError) Error() string {
	switch e.Cause {
	case "board_not_linked":
		return fmt.Sprintf("item.board_id: link board %s first: propose a link and ask the owner to approve it", e.BoardID)
	case "board_in_use":
		return fmt.Sprintf("item.board_id: cannot unlink board %s while plan items or milestones reference it", e.BoardID)
	case "board_already_linked":
		return fmt.Sprintf("item.board_id: board %s is already linked", e.BoardID)
	}
	return "item.board_id: invalid board link"
}
func validateBoardLink(r Record, c Change) error {
	linked := false
	for _, l := range r.Links {
		if l.BoardID == c.Item.BoardID {
			linked = true
		}
	}
	if c.Operation == "link" {
		if linked {
			return &PlanLinkError{c.Item.BoardID, "board_already_linked"}
		}
		return nil
	}
	if !linked {
		return &PlanLinkError{c.Item.BoardID, "board_not_linked"}
	}
	for _, i := range r.Items {
		if i.BoardID == c.Item.BoardID {
			return &PlanLinkError{c.Item.BoardID, "board_in_use"}
		}
	}
	for _, m := range r.Milestones {
		if m.BoardID == c.Item.BoardID {
			return &PlanLinkError{c.Item.BoardID, "board_in_use"}
		}
	}
	return nil
}

func boardLinked(r Record, milestoneBoard, board string) bool {
	if milestoneBoard == board {
		return true
	}
	for _, l := range r.Links {
		if l.BoardID == board {
			return true
		}
	}
	return false
}

// Withdraw removes only the proposing agent's pending proposal, without changing scope.
// The transport additionally requires the workspace Main's live credential.
func (s Service) Withdraw(ctx context.Context, actor Actor, mid string, id ProposalID) error {
	if actor.Kind != "agent" || actor.ID == "" {
		return ErrForbidden
	}
	return s.updatePartition(ctx, Partition{Kind: PartitionPlan, MilestoneID: mid, ProposalID: string(id)}, func(r *Record) error {
		for i := range r.Proposals {
			q := &r.Proposals[i]
			if q.ID != id || q.MilestoneID != mid {
				continue
			}
			if q.Actor != actor {
				return ErrForbidden
			}
			if q.Status != "pending" {
				return ErrConflict
			}
			q.Status = "withdrawn"
			e := Event{ID: NewID(), MilestoneID: mid, AuthenticatedActor: actor, RecordedAt: s.now(), Source: "agent", Kind: "proposal_withdrawn"}
			e.Payload, _ = json.Marshal(map[string]any{"proposal_id": id})
			r.Events = append(r.Events, e)
			return nil
		}
		return ErrConflict
	})
}
