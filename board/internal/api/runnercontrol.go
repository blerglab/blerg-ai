package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/blerglab/blerg-ai/board/internal/runner"
)

var errRecoveryAuthorityUnavailable = errors.New("recovery authority unavailable")

// Board controls resolve today's automation authority. The runner still checks
// that its account owns the existing conversation; replacing a board's token
// with another owner's token cannot transfer a native conversation.
func (a *API) messageOnRunner(ctx context.Context, boardID, ext, text, source, clientID string) error {
	if c, ok := a.runner.Driver.(runner.IdentityController); ok {
		token, _, err := a.startIdentity(ctx, boardID)
		if err != nil {
			return err
		}
		return c.MessageAs(ctx, token, ext, text, source, clientID)
	}
	if clientID != "" {
		return fmt.Errorf("%w: durable input receipts", runner.ErrUnsupported)
	}
	return a.runner.Driver.Message(ctx, ext, text, source)
}

func (a *API) statusOnRunner(ctx context.Context, boardID, ext string) (runner.Status, error) {
	if c, ok := a.runner.Driver.(runner.IdentityController); ok {
		token, _, err := a.startIdentity(ctx, boardID)
		if err != nil {
			// Poll lifecycle even when recovery authority is unavailable, but never
			// advertise recovery that the board cannot authorize.
			st, statusErr := a.runner.Driver.Status(ctx, ext)
			st.Resumable = false
			st.ResumeMode = ""
			st.ResumeReason = "Update this board's automation identity to recover this session"
			if statusErr != nil {
				return st, errRecoveryAuthorityUnavailable
			}
			return st, nil
		}
		st, statusErr := c.StatusAs(ctx, token, ext)
		if errors.Is(statusErr, runner.ErrControlUnauthorized) {
			st, statusErr = a.runner.Driver.Status(ctx, ext)
			st.Resumable = false
			st.ResumeMode = ""
			st.ResumeReason = "The runner refused this board's current recovery authority"
			if statusErr != nil {
				return st, errRecoveryAuthorityUnavailable
			}
		}
		return st, statusErr
	}
	return a.runner.Driver.Status(ctx, ext)
}
