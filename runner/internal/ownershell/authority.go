package ownershell

import (
	"context"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"time"
)

// ErrUnknown refuses access without proving a terminal denial. It preserves the
// ordinary refusal contract for callers that do not support retained custody.
var ErrUnknown = fmt.Errorf("%w: verification unavailable", ErrDenied)

func Unknown(err error) bool { return errors.Is(err, ErrUnknown) || Busy(err) }

// PersistentAuthority requires fresh typed approval and the original login's
// deadline. An ordinary legacy policy response cannot authorize retained custody.
type PersistentAuthority interface {
	AuthorizePersistent(context.Context, Actor) (protocol.ShellPolicy, time.Time, error)
}
