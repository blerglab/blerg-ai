package blackboard

import "testing"

func TestRound6WorkspaceKeyUsesLinkedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		r    Record
		want string
	}{
		{Record{ID: "record", WorkspaceID: "explicit", Links: []Link{{WorkspaceID: "linked"}}}, "explicit"},
		{Record{ID: "record", Links: []Link{{WorkspaceID: "linked"}}}, "linked"},
		{Record{ID: "record", Links: []Link{{}}}, "record"},
		{Record{ID: "record"}, "record"},
	} {
		if got := WorkspaceKey(tc.r); got != tc.want {
			t.Errorf("workspace key=%q, want %q", got, tc.want)
		}
	}
}
