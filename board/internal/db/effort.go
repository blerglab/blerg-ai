package db

import (
	"regexp"
	"strings"
)

var effortID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Effort names are opaque engine metadata. Validate their wire shape without
// freezing today's engines or effort levels into Board's persistence layer.
func checkEffort(key string, value *string) error {
	if value == nil {
		return nil
	}
	*value = strings.TrimSpace(*value)
	if *value != "" && !effortID.MatchString(*value) {
		return &FieldError{Key: key, Msg: "must be a bare effort id or empty for the model default"}
	}
	return nil
}
func checkBoardEfforts(p *BoardParams) error {
	for _, f := range []struct {
		key   string
		value *string
	}{{"effort", p.Effort}, {"reviewer_effort", p.ReviewerEffort}, {"discuss_effort", p.DiscussEffort}, {"chat_effort", p.ChatEffort}} {
		if err := checkEffort(f.key, f.value); err != nil {
			return err
		}
	}
	return nil
}
