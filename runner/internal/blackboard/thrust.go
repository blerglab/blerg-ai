package blackboard

import (
	"fmt"
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
	if t == nil || c.Item != (Item{}) || len(t.ID) > 100 || len(t.Name) > 200 || len(t.GoalID) > 200 || len(t.Destination) > 100 || strings.TrimSpace(t.Reason) == "" || len(t.Reason) > 1000 {
		return ErrInvalid
	}
	if c.Operation == "thrust_add" {
		if strings.TrimSpace(t.Name) == "" || t.GoalID == "" || t.Destination != "" {
			return ErrInvalid
		}
		for _, old := range r.Thrusts {
			if old.ID == t.ID && t.ID != "" {
				return ErrInvalid
			}
		}
		for _, g := range r.Goals {
			if g.ID == t.GoalID {
				return nil
			}
		}
		return ErrInvalid
	}
	idx := thrustIndex(r, mid, t.ID)
	if idx < 0 || t.GoalID != "" {
		return ErrInvalid
	}
	switch c.Operation {
	case "thrust_rename":
		if strings.TrimSpace(t.Name) == "" || t.Destination != "" {
			return ErrInvalid
		}
	case "thrust_merge":
		if t.Name != "" || t.Destination == t.ID || thrustIndex(r, mid, t.Destination) < 0 {
			return ErrInvalid
		}
	case "thrust_remove":
		if t.Name != "" || t.Destination != "" {
			return ErrInvalid
		}
		for _, item := range r.Items {
			if item.ThrustID == t.ID {
				return fmt.Errorf("cannot remove a thrust while cards remain: %w", ErrInvalid)
			}
		}
	default:
		return ErrInvalid
	}
	return nil
}
func prepareThrustChange(r Record, mid string, c *Change) error {
	if c.Operation == "thrust_add" && c.Thrust != nil && c.Thrust.ID != "" {
		return ErrInvalid
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
			return ErrInvalid
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
