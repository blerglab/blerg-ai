package cardtime

import "testing"

func TestThresholdsCrossedNoneUnderFirstInterval(t *testing.T) {
	got := ThresholdsCrossed(3*3600, 4, nil)
	if len(got) != 0 {
		t.Fatalf("3h under a 4h interval: got %v, want none", got)
	}
}

func TestThresholdsCrossedFirstThreshold(t *testing.T) {
	got := ThresholdsCrossed(4*3600, 4, nil)
	if len(got) != 1 || got[0] != 4*3600 {
		t.Fatalf("exactly 4h at a 4h interval: got %v, want [14400]", got)
	}
}

func TestThresholdsCrossedSkipsAlreadyFired(t *testing.T) {
	got := ThresholdsCrossed(9*3600, 4, map[int64]bool{4 * 3600: true})
	if len(got) != 1 || got[0] != 8*3600 {
		t.Fatalf("9h, 4h already fired: got %v, want [28800] (not 4h, not 4h&8h)", got)
	}
}

// A total that jumped past more than one threshold at once (e.g. several workers' turns landed
// close together) must still fire every un-fired threshold it has reached, in order — never skip
// straight to the highest one.
func TestThresholdsCrossedFiresEveryUnfiredThresholdInOrder(t *testing.T) {
	got := ThresholdsCrossed(13*3600, 4, nil)
	want := []int64{4 * 3600, 8 * 3600, 12 * 3600}
	if len(got) != len(want) {
		t.Fatalf("13h, nothing fired yet: got %v, want %v", got, want)
	}
	for i, m := range want {
		if got[i] != m {
			t.Fatalf("got %v, want %v (in order)", got, want)
		}
	}
}

func TestThresholdsCrossedNoneWhenAllAlreadyFired(t *testing.T) {
	got := ThresholdsCrossed(9*3600, 4, map[int64]bool{4 * 3600: true, 8 * 3600: true})
	if len(got) != 0 {
		t.Fatalf("got %v, want none (both reached thresholds already fired)", got)
	}
}

// Owner's correction: dedup keys on the threshold's own absolute seconds, not an
// interval-relative ordinal, so raising the interval after one firing can only add newly-reached
// thresholds under the new, larger interval — it can never re-announce the one already fired.
func TestThresholdsCrossedIntervalRaisedNeverReAnnouncesAFiredThreshold(t *testing.T) {
	// Fired the 4h threshold under a 4h interval.
	alreadyFired := map[int64]bool{4 * 3600: true}
	// Interval raised to 8h; total is now 9h (past one 8h multiple).
	got := ThresholdsCrossed(9*3600, 8, alreadyFired)
	if len(got) != 1 || got[0] != 8*3600 {
		t.Fatalf("interval raised after one firing: got %v, want [28800] (4h stays fired, never re-announced; 8h is new)", got)
	}
}

// Owner's correction, the lowering case: under the OLD design (an interval-relative ordinal
// "multiple"), lowering the interval after a firing would re-derive the already-announced time
// mark under a new ordinal number and fire it again as if it were new — a duplicate notification
// for a crossing already told. Keying on the threshold's absolute seconds means the already-fired
// 4h mark is skipped by its own value regardless of what ordinal a smaller interval would now
// assign it, and only a threshold whose absolute value was never recorded (here, 2h) is new.
func TestThresholdsCrossedIntervalLoweredNeverReAnnouncesAFiredThreshold(t *testing.T) {
	alreadyFired := map[int64]bool{4 * 3600: true}
	// Interval lowered to 2h; total is now 5h (past two 2h multiples: 2h and 4h).
	got := ThresholdsCrossed(5*3600, 2, alreadyFired)
	if len(got) != 1 || got[0] != 2*3600 {
		t.Fatalf("interval lowered after one firing: got %v, want [7200] (2h is newly reached and unfired; 4h stays fired, not re-announced)", got)
	}
}
