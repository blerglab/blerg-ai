package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestJobSubmissionReasonIsBoundedAndAllowlisted(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"status_reason", `{"reason":"AlreadyExists","message":"fixture-private"}`, "AlreadyExists", 409},
		{"unknown_reason", `{"reason":"fixture-private"}`, "Conflict", 409},
		{"malformed", `fixture-private`, "Forbidden", 403},
		{"validation", `{"reason":"Invalid","details":{"causes":[{"message":"fixture-private"}]}}`, "Invalid", 422},
		{"oversized", `{"message":"` + strings.Repeat("x", 16<<10) + `","reason":"fixture-private"}`, "BadRequest", 400},
		{"unknown_status", `{}`, "UnknownAPIError", 418},
		{"local_reason_is_not_api_authority", `{"reason":"SchedulingPolicyRefused"}`, "InternalError", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(tc.body)
			err := jobSubmissionRejection(&http.Response{StatusCode: tc.status, Body: io.NopCloser(body)})
			var failure *jobSubmissionError
			if !errors.As(err, &failure) || failure.reason != tc.want || !errors.Is(err, errSessionJobCreate) || err.Error() != errSessionJobCreate.Error() {
				t.Fatal("unsafe or incorrect refusal", err)
			}
			if len(tc.body)-body.Len() > 16<<10 {
				t.Fatal("unbounded API body read")
			}
			got := launchDiagnosticReason(stage(protocol.StageQueued, "", protocol.StageStateFailed, "Job submission refused: "+failure.reason))
			if got != tc.want {
				t.Fatal("reason not retained in stage", got)
			}
		})
	}
	// Even persisted arbitrary text cannot become log/receipt authority.
	if got := launchDiagnosticReason(stage(protocol.StageQueued, "", protocol.StageStateFailed, "Job submission refused: fixture-private")); got != "stage_refused" {
		t.Fatal("untrusted reason accepted", got)
	}
}

func TestJobSubmissionLocalFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errJobScheduling, "SchedulingPolicyRefused"},
		{context.DeadlineExceeded, "Timeout"},
		{errors.New("fixture-private-transport-url"), "TransportError"},
	} {
		err := jobSubmissionTransportError(tc.err)
		var failure *jobSubmissionError
		if !errors.As(err, &failure) || failure.reason != tc.want || err.Error() != errSessionJobCreate.Error() {
			t.Fatal("unsafe local submission error", err)
		}
	}
}
