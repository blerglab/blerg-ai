// Package cardtime computes a card's total worker time and review-round count from
// already-recorded sessions and marks (BLG-444). It never records anything itself: callers in
// internal/db and internal/server own persistence and the plan-drift delivery.
//
// Attribution rule (H1, docs/design/card-time.md §3): a session counts toward one card only when
// it is directly linked to that exact card, is not a root/coordinator session, and is linked to
// no other card. This mirrors Insights' own cardActiveTime (frontend/src/lib/insightsActivity.ts)
// exactly, including its all-or-nothing rule: if any linked session fails eligibility, the whole
// card's total is unknown, not a partial sum excluding just that session. A card we aren't sure
// about shows nothing, never a wrong number.
package cardtime

// SessionAttribution is one session's contribution to one card, already screened for direct
// linkage (the caller excludes root/coordinator sessions and sessions linked to more than one
// card's link set before building this list; the eligibility flags below are the ones Insights'
// own cardActiveTime also checks, reproduced here so the two can reconcile exactly).
type SessionAttribution struct {
	SessionID string
	// ActiveSeconds is exactly what Insights calls a session's card active time: the sum of the
	// 'tests','build_lint','git','read','edit','other','model','turn_end' buckets of its
	// activity.Timing (frontend/src/lib/insightsActivity.ts's ACTIVE_KEYS, ported in timingsource.go).
	ActiveSeconds float64
	// Shared is true when this session is linked to more than one card (Insights' shared_with_other_cards).
	Shared bool
	// CardAttributionUnknown mirrors Insights' card_attribution_unknown: the session's own card
	// links could not be resolved with confidence.
	CardAttributionUnknown bool
	// NativeActiveTimeUnknown mirrors Insights' native_active_time_unknown: a subagent-bearing or
	// native-timed session whose own active time the engine never reported.
	NativeActiveTimeUnknown bool
	// UnknownSeconds is the session's Timing.Seconds["unknown"] bucket: time Split could not
	// classify. Insights treats any unknown>0 as disqualifying the whole card's total.
	UnknownSeconds float64
}

// CardTotal sums the given sessions' active seconds for one card, reproducing Insights'
// cardActiveTime rule exactly: ok is false (no total, show nothing) when sessions is empty or any
// one of them is shared, has unknown card attribution, has unknown native active time, or carries
// any unclassified ("unknown") seconds. A duplicate SessionID (the caller is expected to pass one
// entry per distinct session) is deduplicated, never summed twice.
func CardTotal(sessions []SessionAttribution) (seconds float64, ok bool) {
	if len(sessions) == 0 {
		return 0, false
	}
	byID := make(map[string]SessionAttribution, len(sessions))
	for _, s := range sessions {
		byID[s.SessionID] = s
	}
	var total float64
	for _, s := range byID {
		if s.Shared || s.CardAttributionUnknown || s.NativeActiveTimeUnknown || s.UnknownSeconds > 0 {
			return 0, false
		}
		total += s.ActiveSeconds
	}
	return total, true
}
