package cardtime

import "time"

// ReviewMark is one accepted review mark for a card (agent_events kind='mark',
// payload->>'kind'='review'), reduced to what this package needs.
type ReviewMark struct {
	At time.Time
	// Round is the mark's own round field, carried for display/debugging only. Rounds (below)
	// deliberately never reads it — see its doc comment.
	Round    int
	Verdict  string
	Findings []string
}

// Rounds counts review rounds as the number of distinct accepted marks (L8), never the maximum
// of their "round" field: a retried or imported post can leave round numbers that repeat or skip,
// so the row count is the only figure that matches "how many review passes actually happened."
func Rounds(marks []ReviewMark) int { return len(marks) }

// LatestDetail is the latest mark's verdict and its open high/medium finding counts (owner's
// decision 2: the latest round only, not a cumulative count across every round). ok is false when
// there are no marks.
func LatestDetail(marks []ReviewMark) (verdict string, high, medium int, ok bool) {
	if len(marks) == 0 {
		return "", 0, 0, false
	}
	latest := marks[0]
	for _, m := range marks[1:] {
		if m.At.After(latest.At) {
			latest = m
		}
	}
	for _, f := range latest.Findings {
		switch f {
		case "high":
			high++
		case "medium":
			medium++
		}
	}
	return latest.Verdict, high, medium, true
}
