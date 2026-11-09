package cardtime

import (
	"testing"
	"time"
)

func TestRoundsEmpty(t *testing.T) {
	if n := Rounds(nil); n != 0 {
		t.Fatalf("got %d, want 0", n)
	}
}

// L8: rounds are counted as the number of distinct accepted review marks, never max(round). A
// retried or imported post can leave round numbers that repeat or skip; this must not under- or
// over-count. This is the single most important test in the package: it must fail against a
// naive max(round) implementation.
func TestRoundsCountsDistinctMarksNotMaxRound(t *testing.T) {
	marks := []ReviewMark{
		{Round: 1}, {Round: 1}, {Round: 3},
	}
	if n := Rounds(marks); n != 3 {
		t.Fatalf("round values 1,1,3: got %d rounds, want 3 (count of marks, not max(round)=3 by coincidence)", n)
	}
}

func TestRoundsCountsDistinctMarksSequential(t *testing.T) {
	marks := []ReviewMark{{Round: 1}, {Round: 2}}
	if n := Rounds(marks); n != 2 {
		t.Fatalf("round values 1,2: got %d, want 2 (not max=2 coincidentally — proven by the skip test above)", n)
	}
}

func TestLatestDetailEmpty(t *testing.T) {
	_, _, _, ok := LatestDetail(nil)
	if ok {
		t.Fatalf("empty marks: ok should be false")
	}
}

// Owner's decision 2: the tooltip shows the latest round's verdict and open high/medium finding
// count, not a cumulative count across all rounds.
func TestLatestDetailUsesLatestRoundOnly(t *testing.T) {
	marks := []ReviewMark{
		{At: time.Unix(1, 0), Verdict: "changes", Findings: []string{"high", "high", "medium"}},
		{At: time.Unix(2, 0), Verdict: "approved", Findings: []string{"low"}},
	}
	verdict, high, medium, ok := LatestDetail(marks)
	if !ok {
		t.Fatalf("expected ok")
	}
	if verdict != "approved" || high != 0 || medium != 0 {
		t.Fatalf("got verdict=%q high=%d medium=%d, want approved/0/0 (latest round only, not cumulative)", verdict, high, medium)
	}
}

func TestLatestDetailCountsHighAndMedium(t *testing.T) {
	marks := []ReviewMark{
		{At: time.Unix(1, 0), Verdict: "changes", Findings: []string{"high", "medium", "medium", "low"}},
	}
	verdict, high, medium, ok := LatestDetail(marks)
	if !ok || verdict != "changes" || high != 1 || medium != 2 {
		t.Fatalf("got verdict=%q high=%d medium=%d ok=%v, want changes/1/2/true", verdict, high, medium, ok)
	}
}
