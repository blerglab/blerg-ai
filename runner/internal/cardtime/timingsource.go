package cardtime

import "github.com/blerglab/blerg-ai/runner/internal/activity"

// activeKeys mirrors Insights' own ACTIVE_KEYS exactly (frontend/src/lib/insightsActivity.ts):
// every activity.Timing bucket except idle, waiting, waiting_on_person and unknown. Keeping this
// list here, not computed from activity.Timing's own field names, is deliberate: a new bucket
// BLG-424 adds later must be a conscious decision about whether it counts as "active," not an
// accidental inclusion.
var activeKeys = []string{"tests", "build_lint", "git", "read", "edit", "other", "model", "turn_end"}

// ActiveSecondsFromTiming is the adapter boundary onto BLG-424's activity.Timing (owner's decision
// 6): the only place in this package that names an activity.* type. A rename in that package
// before it merges is a one-line change here, not a ripple through cardtime's own logic.
// It returns the same "active seconds" figure Insights' own cardActiveTime sums, and the
// session's "unknown" bucket (any amount above zero disqualifies the whole card's total per
// CardTotal's rule, matching Insights).
func ActiveSecondsFromTiming(t activity.Timing) (active, unknownSeconds float64) {
	for _, k := range activeKeys {
		active += t.Seconds[k]
	}
	return active, t.Seconds["unknown"]
}
