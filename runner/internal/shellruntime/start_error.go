package shellruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

// A helper reports one allowlisted failure cause, never its stderr or environment.
type helperStartError struct{ cause, stage string }

func (e *helperStartError) Error() string { return e.cause }
func (e *helperStartError) Unwrap() error { return ErrClosed }

// Preserve the original error chain in memory. Logs expose only trusted stages and OS error
// descriptions: paths, argv, arbitrary wrapped messages and environment values are redacted.
type startError struct {
	stage string
	err   error
}

func (e *startError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *startError) Unwrap() error { return e.err }
func startDiagnostic(err error) (string, string) {
	stage := "open"
	// Bootstrap context must not hide a more specific trusted transport stage.
	for current := err; current != nil; current = errors.Unwrap(current) {
		var start *startError
		if errors.As(current, &start) {
			if safe := ownershell.SafeStartStage(start.stage); safe != "" {
				stage = safe
			}
		}
	}
	detail := "[redacted]"
	cause := ownershell.StartCauseUnavailable
	var errno syscall.Errno
	var exit *exec.ExitError
	var helper *helperStartError
	switch {
	case errors.As(err, &helper):
		cause = ownershell.SafeStartCause(helper.cause)
		if safe := ownershell.SafeStartStage(helper.stage); safe != "" {
			stage = helper.stage
		}
	case errors.Is(err, os.ErrNotExist):
		cause = ownershell.StartCauseMissing
	case errors.Is(err, os.ErrPermission):
		cause = ownershell.StartCausePermission
	case errors.Is(err, context.DeadlineExceeded):
		cause = ownershell.StartCauseTimeout
	case errors.Is(err, ErrClosed):
		cause = ownershell.StartCauseBootstrap
	}
	switch {
	case errors.As(err, &errno):
		detail = errno.Error()
	case errors.As(err, &exit):
		detail = fmt.Sprintf("exit status %d", exit.ExitCode())
	case errors.Is(err, context.DeadlineExceeded):
		detail = "deadline exceeded"
	case errors.As(err, &helper):
		switch helper.cause {
		case ownershell.StartCausePermission:
			detail = "permission denied starting shell"
		case ownershell.StartCauseMissing:
			detail = "shell executable or directory missing"
		case ownershell.StartCauseTimeout:
			detail = "shell start deadline exceeded"
		default:
			detail = ownershell.StartStageMessage(stage)
			if detail == "" {
				detail = "shell containment refused"
			}
		}
	case errors.Is(err, ErrClosed):
		detail = ownershell.StartStageMessage(stage)
		if detail == "" {
			detail = ErrClosed.Error()
		}
	}
	if safe := ownershell.SafeStartStage(stage); safe != "" {
		cause = stage
		sentence := ownershell.StartStageMessage(stage)
		if !strings.Contains(detail, sentence) {
			detail = sentence + " " + detail
		}
	}
	return fmt.Sprintf("%s: %s (paths and details redacted)", stage, detail), cause
}

func stageError(stage string, err error) error {
	if err == nil {
		err = ErrClosed
	}
	return &startError{stage: stage, err: err}
}
