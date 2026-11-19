package skills

import "testing"

func TestPolicyPrecedence(t *testing.T) {
	on, off := true, false
	for _, main := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			scopes  []Overrides
			tdd, ux bool
		}{
			{"defaults", nil, true, true},
			{"install wins", []Overrides{{TDD: &off}, {TDD: &on}, {TDD: &on}}, false, true},
			{"account wins", []Overrides{{}, {UX: &off}, {UX: &on}}, true, false},
			{"workspace off", []Overrides{{}, {}, {TDD: &off, UX: &off}}, false, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := Resolve(main, tc.scopes...)
				if got.TDD != tc.tdd || got.UX != tc.ux || got.Coordinating != main {
					t.Fatalf("policy = %+v", got)
				}
			})
		}
	}
}

func TestPlanningDefaults(t *testing.T) {
	for _, main := range []bool{false, true} {
		if !Resolve(main).Planning {
			t.Fatal("planning must ship on by default for every session")
		}
	}
}
