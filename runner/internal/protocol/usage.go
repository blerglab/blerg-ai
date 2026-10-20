package protocol

import (
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

const UsageUpdatedKind = "usage_updated"

// UsageUpdatedPayload replaces the reported usage of the CURRENT turn. It is
// display telemetry, never an additional billable turn. Nil Usage clears an
// earlier snapshot when an engine starts/resumes a turn whose usage is unknown.
// A turn_done supersedes it; all billing aggregates continue to read turn_done.
type UsageUpdatedPayload struct {
	Scope string         `json:"scope"`
	Model string         `json:"model,omitempty"`
	Usage *UsageCounters `json:"usage"`
}
type UsageCounters struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	CacheWrite int64 `json:"cache_write_tokens"`
}

func DecodeUsageUpdated(raw json.RawMessage) (UsageUpdatedPayload, bool) {
	var p UsageUpdatedPayload
	var fields map[string]json.RawMessage
	if len(raw) > 4096 || json.Unmarshal(raw, &fields) != nil || fields["usage"] == nil || json.Unmarshal(raw, &p) != nil || p.Scope != "turn" || len(p.Model) > 256 || !utf8.ValidString(p.Model) {
		return p, false
	}
	for _, r := range p.Model {
		if unicode.IsControl(r) {
			return p, false
		}
	}
	if p.Usage != nil {
		var counters map[string]*json.RawMessage
		if json.Unmarshal(fields["usage"], &counters) != nil {
			return p, false
		}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"} {
			if counters[key] == nil {
				return p, false
			}
		}
		for _, n := range []int64{p.Usage.Input, p.Usage.Output, p.Usage.CacheRead, p.Usage.CacheWrite} {
			if n < 0 || n > 1<<50 {
				return p, false
			}
		}
	}
	return p, true
}
