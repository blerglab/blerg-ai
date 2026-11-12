package blackboard

import "fmt"

// Field and rule are fixed diagnostics; caller-provided values never enter them.
func invalidPlan(field, rule string) error {
	return fmt.Errorf("%s: %s: %w", field, rule, ErrInvalid)
}

type planField struct {
	name, value string
	max         int
}

func planLengths(fields ...planField) error {
	for _, f := range fields {
		if len(f.value) > f.max {
			return invalidPlan(f.name, fmt.Sprintf("at most %d bytes", f.max))
		}
	}
	return nil
}

func validatePlanLinkShape(changes int, c Change) error {
	if changes != 1 {
		return invalidPlan("changes", "link/unlink requires a separate proposal")
	}
	if c.Item.BoardID == "" {
		return invalidPlan("item.board_id", "required for link/unlink")
	}
	if c.Item.Reason == "" {
		return invalidPlan("item.reason", "required for link/unlink")
	}
	if err := planLengths(planField{"item.reason", c.Item.Reason, 1000}); err != nil {
		return err
	}
	if c.Item.CardNumber != 0 {
		return invalidPlan("item.card_number", "not allowed for link/unlink")
	}
	for _, f := range []planField{
		{"item.id", string(c.Item.ID), 0}, {"item.title", c.Item.Title, 0},
		{"item.destination", c.Item.Destination, 0}, {"item.thrust_id", c.Item.ThrustID, 0},
		{"item.goal_id", c.Item.GoalID, 0}, {"item.progress", c.Item.Progress, 0},
		{"item.state", c.Item.State, 0}, {"item.milestone_id", c.Item.MilestoneID, 0},
	} {
		if f.value != "" {
			return invalidPlan(f.name, "not allowed for link/unlink")
		}
	}
	return nil
}

func validatePlanItemShape(c Change) error {
	if err := planLengths(planField{"item.reason", c.Item.Reason, 1000}, planField{"item.title", c.Item.Title, 200}); err != nil {
		return err
	}
	if c.Item.BoardID == "" {
		if c.Item.CardNumber != 0 {
			return invalidPlan("item.card_number", "requires item.board_id")
		}
		if c.Operation == "add" && c.Item.Title == "" {
			return invalidPlan("item.title", "required for a plain item addition")
		}
	} else {
		if c.Item.CardNumber <= 0 {
			return invalidPlan("item.card_number", "must be positive for a board card")
		}
		if c.Item.Reason == "" {
			return invalidPlan("item.reason", "required for a board card")
		}
	}
	return nil
}
