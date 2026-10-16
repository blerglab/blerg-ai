package server

import (
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// sessionInputError exposes only fixed validation failures, never database or input contents.
func sessionInputError(err error) string {
	for _, known := range []error{db.ErrSessionInputTooLarge, db.ErrSessionInputID, db.ErrSessionInputConflict, db.ErrSessionInputQueue, db.ErrSessionInputEnded} {
		if errors.Is(err, known) {
			return known.Error() + "."
		}
	}
	return "Message could not be saved; retry shortly."
}
