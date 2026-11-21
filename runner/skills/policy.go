package skills

// Overrides contains off switches. An omitted switch retains the default.
// Coordinating has no switch: only Main receives it. Planning is required and ships on.
type Overrides struct {
	TDD         *bool `json:"test-driven-development,omitempty"`
	UX          *bool `json:"ux-design,omitempty"`
	Visualizing *bool `json:"visualizing,omitempty"`
}

// Policy is the resolved, session-specific skill inventory.
type Policy struct {
	Planning     bool `json:"planning"`
	TDD          bool `json:"test-driven-development"`
	UX           bool `json:"ux-design"`
	Coordinating bool `json:"coordinating"`
	Visualizing  bool `json:"visualizing"`
}

// Resolve applies every off switch; no narrower scope can undo a disable.
func Resolve(main bool, scopes ...Overrides) Policy {
	p := Policy{Planning: true, TDD: true, UX: true, Coordinating: main, Visualizing: true}
	for _, scope := range scopes {
		if scope.TDD != nil && !*scope.TDD {
			p.TDD = false
		}
		if scope.UX != nil && !*scope.UX {
			p.UX = false
		}
		if scope.Visualizing != nil && !*scope.Visualizing {
			p.Visualizing = false
		}
	}
	return p
}
