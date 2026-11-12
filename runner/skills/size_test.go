package skills

import "testing"

// SKILL.md stays in the always-available on-demand budget; a linked examples
// file gets its own, separate cap so it can carry worked examples without
// crowding the instructions an agent loads for every turn.
const skillBodyBudget = 8 * 1024
const examplesBudget = 8 * 1024

func TestSkillBodyWithinBudget(t *testing.T) {
	for _, e := range Inventory(Resolve(true)) {
		if len(e.Body) > skillBodyBudget {
			t.Errorf("%s SKILL.md is %d bytes, over the %d byte budget", e.Name, len(e.Body), skillBodyBudget)
		}
	}
}

func TestExamplesWithinBudget(t *testing.T) {
	for _, e := range Inventory(Resolve(true)) {
		if len(e.Examples) > examplesBudget {
			t.Errorf("%s examples.md is %d bytes, over the %d byte budget", e.Name, len(e.Examples), examplesBudget)
		}
	}
}
