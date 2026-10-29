package api

import (
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// A typed runner refusal for lack of slots (503, 429) still reads as capacity: the dispatcher
// retries later instead of flagging the card.
func TestCapacityRefusalsAreRecognizedWhenTyped(t *testing.T) {
	for status, want := range map[int]bool{http.StatusServiceUnavailable: true, http.StatusTooManyRequests: true, http.StatusConflict: false} {
		if got := isCapacityErr(&runner.StartRefusedError{Status: status, Message: "busy"}); got != want {
			t.Errorf("status %d: capacity = %v, want %v", status, got, want)
		}
	}
}
