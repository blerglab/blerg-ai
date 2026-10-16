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

var explicitResetTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

func codexUsageRefusal(ev codexEvent, turnID string) (agent.UsageLimitPayload, bool) {
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
	zone := strings.TrimSpace(m[6])
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
