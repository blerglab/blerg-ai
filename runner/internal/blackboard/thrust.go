package blackboard

import (
	"strings"
)

func isThrustOperation(op string) bool {
	switch op {
	case "thrust_add", "thrust_rename", "thrust_merge", "thrust_remove":
		return true
	}
	return false
}
func thrustIndex(r Record, mid, id string) int {
	for i, t := range r.Thrusts {
		if t.ID == id && t.MilestoneID == mid {
			return i
		}
	}
	return -1
}
func validateThrustChange(r Record, mid string, c Change) error {
	t := c.Thrust
	if t == nil {
		return invalidPlan("thrust", "required for thrust operations")
	}
	if c.Item != (Item{}) {
		return invalidPlan("item", "not allowed for thrust operations")
	}
	if err := planLengths(planField{"thrust.id", t.ID, 100}, planField{"thrust.name", t.Name, 200}, planField{"thrust.goal_id", t.GoalID, 200}, planField{"thrust.destination", t.Destination, 100}, planField{"thrust.reason", t.Reason, 1000}); err != nil {
		return err
	}
	if strings.TrimSpace(t.Reason) == "" {
		return invalidPlan("thrust.reason", "required")
	}
	if c.Operation == "thrust_add" {
		if strings.TrimSpace(t.Name) == "" {
			return invalidPlan("thrust.name", "required for thrust_add")
		}
		if t.GoalID == "" {
			return invalidPlan("thrust.goal_id", "required for thrust_add")
		}
		if t.Destination != "" {
			return invalidPlan("thrust.destination", "not allowed for thrust_add")
		}
		for _, old := range r.Thrusts {
			if old.ID == t.ID && t.ID != "" {
				return invalidPlan("thrust.id", "must be unique")
			}
		}
		for _, g := range r.Goals {
			if g.ID == t.GoalID {
				return nil
			}
		}
		return invalidPlan("thrust.goal_id", "must identify an existing goal")
	}
	idx := thrustIndex(r, mid, t.ID)
	if idx < 0 {
		return invalidPlan("thrust.id", "must identify a thrust in the selected milestone")
	}
	if t.GoalID != "" {
		return invalidPlan("thrust.goal_id", "only allowed for thrust_add")
	}
	switch c.Operation {
	case "thrust_rename":
		if strings.TrimSpace(t.Name) == "" {
			return invalidPlan("thrust.name", "required for thrust_rename")
		}
		if t.Destination != "" {
			return invalidPlan("thrust.destination", "not allowed for thrust_rename")
		}
	case "thrust_merge":
		if t.Name != "" {
			return invalidPlan("thrust.name", "not allowed for thrust_merge")
		}
		if t.Destination == t.ID || thrustIndex(r, mid, t.Destination) < 0 {
			return invalidPlan("thrust.destination", "must identify a different thrust in the selected milestone")
		}
	case "thrust_remove":
		if t.Name != "" {
			return invalidPlan("thrust.name", "not allowed for thrust_remove")
		}
		if t.Destination != "" {
			return invalidPlan("thrust.destination", "not allowed for thrust_remove")
		}
		for _, item := range r.Items {
			if item.ThrustID == t.ID {
				return invalidPlan("thrust.id", "cannot remove a thrust while cards remain")
			}
		}
	default:
		return invalidPlan("operation", "use a supported thrust operation")
	}
	return nil
}
func prepareThrustChange(r Record, mid string, c *Change) error {
	if c.Operation == "thrust_add" && c.Thrust != nil && c.Thrust.ID != "" {
		return invalidPlan("thrust.id", "assigned by server")
	}
	if err := validateThrustChange(r, mid, *c); err != nil {
		return err
	}
	t := c.Thrust
	// Ignore claimed snapshot names: only the server's current record supplies them.
	t.PreviousName = ""
	t.DestinationName = ""
	t.GoalName = ""
	if c.Operation == "thrust_add" {
		t.ID = NewID()
		for _, g := range r.Goals {
			if g.ID == t.GoalID {
				t.GoalName = g.Name
			}
		}
	} else {
		t.PreviousName = r.Thrusts[thrustIndex(r, mid, t.ID)].Name
		if c.Operation == "thrust_merge" {
			t.DestinationName = r.Thrusts[thrustIndex(r, mid, t.Destination)].Name
		}
	}
	return nil
}
func applyThrustChange(r *Record, mid string, c Change) error {
	if err := validateThrustChange(*r, mid, c); err != nil {
		return err
	}
	t := c.Thrust
	if c.Operation == "thrust_add" {
		if t.ID == "" {
			return invalidPlan("thrust.id", "assigned by server")
		}
		r.Thrusts = append(r.Thrusts, Thrust{ID: t.ID, Owner: r.Owner, MilestoneID: mid, GoalID: t.GoalID, Name: t.Name, Version: 1})
		return nil
	}
	idx := thrustIndex(*r, mid, t.ID)
	if c.Operation == "thrust_rename" {
		r.Thrusts[idx].Name = t.Name
		r.Thrusts[idx].Version++
		return nil
	}
	if c.Operation == "thrust_merge" {
		for i := range r.Items {
			if r.Items[i].ThrustID == t.ID {
				r.Items[i].ThrustID = t.Destination
			}
		}
	}
	r.Thrusts = append(r.Thrusts[:idx], r.Thrusts[idx+1:]...)
	return nil
}
