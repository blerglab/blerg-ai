package daemon

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// codex exec --json emits a flat error message and a turn.failed error object.
// Some releases only include the reset time in the error message.
type codexError struct {
	Message  string          `json:"message"`
	Code     string          `json:"code"`
	ResetsAt json.RawMessage `json:"resets_at"`
}

// Keep recognition in one place: these are provider refusals, never assistant prose.
var claudeHitLimit = regexp.MustCompile(`(?i)\b(?:hit|reached) your [\w' -]*limit\b`)

func claudeUsageRefusal(code, text string) bool {
	if code == "rate_limit" {
		return true
	}
	if code != "" {
		return false
	}
	lower := strings.ToLower(text)
	return claudeHitLimit.MatchString(text) || strings.Contains(lower, "usage limit") || strings.Contains(lower, "limit reached") && !strings.Contains(lower, "rate limit reached")
}

func claudeLimitWindow(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "session limit"), strings.Contains(lower, "5-hour"):
		return "5h"
	case strings.Contains(lower, "opus"):
		return "7d_opus"
	case strings.Contains(lower, "sonnet"):
		return "7d_sonnet"
	case strings.Contains(lower, "fable"):
		return "7d_fable"
	case strings.Contains(lower, "weekly"):
		return "7d"
	}
	return ""
}

var codexOrdinal = regexp.MustCompile(`(?i)(\d)(?:st|nd|rd|th)\b`)

var explicitResetTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

func codexUsageRefusal(ev codexEvent, turnID string) (agent.UsageLimitPayload, bool) {
	return codexUsageRefusalAt(ev, turnID, time.Now())
}

func codexUsageRefusalAt(ev codexEvent, turnID string, now time.Time) (agent.UsageLimitPayload, bool) {
	if ev.Type != "error" && ev.Type != "turn.failed" {
		return agent.UsageLimitPayload{}, false
	}
	text := ev.Message
	if ev.Error.Message != "" {
		text = ev.Error.Message
	}
	lower := strings.ToLower(text)
	if ev.Error.Code != "usage_limit_reached" && !strings.Contains(lower, "you've hit your usage limit") {
		return agent.UsageLimitPayload{}, false
	}
	p := agent.UsageLimitPayload{Provider: "codex", TurnID: turnID}
	var seconds int64
	if json.Unmarshal(ev.Error.ResetsAt, &seconds) == nil && seconds > 0 {
		reset := time.Unix(seconds, 0).UTC()
		p.ResetAt = &reset
	} else {
		var timestamp string
		_ = json.Unmarshal(ev.Error.ResetsAt, &timestamp)
		if timestamp == "" {
			if i := strings.Index(lower, "try again at "); i >= 0 {
				timestamp = explicitResetTimestamp.FindString(text[i:])
			}
		}
		if reset, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			reset = reset.UTC()
			p.ResetAt = &reset
		} else if i := strings.Index(lower, "try again at "); i >= 0 {
			retry := codexOrdinal.ReplaceAllString(text[i+len("try again at "):], "${1}")
			if reset, ok := claudeRateLimitReset("resets "+retry, now); ok {
				reset = reset.UTC()
				p.ResetAt = &reset
			}
		}
	}
	return p, true
}

func staleLimitResumeError() agent.ErrorPayload {
	return agent.ErrorPayload{Message: "Automatic continuation skipped because the interrupted turn is no longer idle and current.", Retryable: true, Code: "limit_resume_stale"}
}

// claudeResetZoneKnown is stricter than the account meter's display parser:
// a retry must never interpret a sandbox CLI's bare clock in the host's zone.
// A driver inside a cluster pod has no sandbox prefix; its CLI shares its zone.
func claudeResetZoneKnown(text string, sandbox bool) bool {
	if claudeResetUnixRE.MatchString(text) {
		return true
	}
	m := claudeResetTimeRE.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	zone := strings.TrimSpace(m[7])
	if zone == "" {
		// An empty or malformed explicit zone must not become a local fallback.
		return !sandbox && !strings.Contains(text, "(")
	}
	if zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}
