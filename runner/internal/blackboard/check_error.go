package blackboard

import (
	"fmt"
	"log/slog"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// CheckError names a failed check without retaining record contents or raw errors.
type CheckError struct{ code, field, rule string }

func (e *CheckError) Error() string { return fmt.Sprintf("%s: %s: %v", e.field, e.rule, ErrInvalid) }
func (e *CheckError) Unwrap() error { return ErrInvalid }
func (e *CheckError) Code() string  { return e.code }

// CheckFailed accepts only fixed, developer-owned labels, never caller data.
// Log at the check so direct store callers get the same safe cause as HTTP callers.
func CheckFailed(code, field, rule string) error {
	failurelog.Default.Event("blackboard_check:"+code, slog.LevelWarn, "component", "runner", "cause", code)
	return &CheckError{code: code, field: field, rule: rule}
}
