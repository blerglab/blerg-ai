package cardtime

// ThresholdsCrossed is the ordered list of absolute budget thresholds (whole seconds, each a
// multiple of the current interval) that totalSeconds has reached and are not already present in
// alreadyFired (owner's decision 3: one account-level interval, firing once at every multiple —
// 4h, 8h, 12h... for a 4-hour interval). Dedup is keyed on the threshold's own absolute second
// value, not an interval-relative ordinal (owner's correction): a later change to the interval
// can only ever add newly-reached thresholds under the new granularity, never re-fire or skip one
// already recorded — because the already-fired set is checked by the threshold's own value, which
// never changes meaning when the interval does. Callers persist alreadyFired permanently
// (card_time_threshold_alerts), never prune it.
func ThresholdsCrossed(totalSeconds float64, intervalHours int, alreadyFired map[int64]bool) []int64 {
	if intervalHours <= 0 || totalSeconds <= 0 {
		return nil
	}
	intervalSeconds := int64(intervalHours) * 3600
	reached := int64(totalSeconds) / intervalSeconds
	var out []int64
	for m := int64(1); m <= reached; m++ {
		threshold := m * intervalSeconds
		if !alreadyFired[threshold] {
			out = append(out, threshold)
		}
	}
	return out
}
