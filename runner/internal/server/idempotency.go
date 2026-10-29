package server

import (
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/idempotency"
)

const idempotencyKeyMessage = idempotency.Message

// CheckIdempotencyKey validates a supplied key for both REST and MCP callers.
// Only the transport knows whether a key was omitted; a supplied empty key is invalid.
func CheckIdempotencyKey(key string) *APIError {
	if !idempotency.Valid(key) {
		return apiErrorf(http.StatusBadRequest, "%s", idempotencyKeyMessage)
	}
	return nil
}

// requestIdempotencyKey enforces the same contract on every REST endpoint.
// Multiple values are invalid even when each value would be valid by itself.
func requestIdempotencyKey(r *http.Request, required bool) (string, *APIError) {
	values, present := r.Header[http.CanonicalHeaderKey("Idempotency-Key")]
	if !present && !required {
		return "", nil
	}
	if len(values) != 1 {
		return "", apiErrorf(http.StatusBadRequest, "%s", idempotencyKeyMessage)
	}
	if apiErr := CheckIdempotencyKey(values[0]); apiErr != nil {
		return "", apiErr
	}
	return values[0], nil
}
