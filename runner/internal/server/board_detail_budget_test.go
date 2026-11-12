package server

import "testing"

func TestBoardDetailBudgetRotates(t *testing.T) {
	var p boardDetailProgress
	if p.start("workspace") != 0 {
		t.Fatal("initial offset")
	}
	for _, tc := range []struct{ total, want int }{{25, 10}, {25, 20}, {25, 0}} {
		start := p.start("workspace")
		p.finish("workspace", start, tc.total)
		if got := p.start("workspace"); got != tc.want {
			t.Fatalf("offset %d want %d", got, tc.want)
		}
	}
}
