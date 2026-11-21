package skills

import "time"

// Evidence is a bounded rule assessment. Agent declarations are kept distinct
// from a person's corrections and never promoted to observed proof.
type Evidence struct {
	SessionID string    `json:"session_id"`
	Skill     string    `json:"skill"`
	Version   string    `json:"version"`
	Rule      string    `json:"rule"`
	Result    string    `json:"result"`
	Source    string    `json:"source"`
	At        time.Time `json:"at"`
}

func ValidRule(skill, rule string) bool {
	rules := map[string][]string{
		"test-driven-development": {"red-before-fix", "region-once"},
		"ux-design":               {"screenshots", "reuse-patterns", "plain-words", "say-once", "natural-width"},
		"managing-tickets":        {"estimate-first", "record-reviews", "record-failures"},
		"coordinating":            {"review-before-merge", "bounded-scope"},
	}
	for _, r := range rules[skill] {
		if r == rule {
			return true
		}
	}
	return false
}
