package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
)

// Keep the caller's error fixed. The private reason is an allowlisted enum,
// never the Kubernetes message/details (which can echo the submitted manifest).
type jobSubmissionError struct {
	reason string
	cause  error
}

func (e *jobSubmissionError) Error() string { return e.Unwrap().Error() }
func (e *jobSubmissionError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return errSessionJobCreate
}

func launchCreateFailure(err, cause error) error {
	var failure *jobSubmissionError
	if errors.As(err, &failure) {
		return &jobSubmissionError{reason: failure.reason, cause: cause}
	}
	return cause
}

func safeJobSubmissionReason(reason string) bool {
	switch reason {
	case "AlreadyExists", "Forbidden", "Invalid", "Unauthorized", "BadRequest", "NotFound", "Conflict", "TooManyRequests", "ServiceUnavailable", "InternalError", "Timeout", "ServerTimeout", "UnknownAPIError", "SchedulingPolicyRefused", "TransportError":
		return true
	}
	return false
}

func jobSubmissionTransportError(err error) *jobSubmissionError {
	reason := "TransportError"
	if errors.Is(err, errJobScheduling) {
		reason = "SchedulingPolicyRefused"
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "Timeout"
	}
	return &jobSubmissionError{reason: reason}
}

func jobSubmissionRejection(resp *http.Response) error {
	var status struct {
		Reason string `json:"reason"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&status) == nil {
		switch status.Reason {
		case "AlreadyExists", "Forbidden", "Invalid", "Unauthorized", "BadRequest", "NotFound", "Conflict", "TooManyRequests", "ServiceUnavailable", "InternalError", "Timeout", "ServerTimeout":
			return &jobSubmissionError{reason: status.Reason}
		}
	}
	// Proxies/admission failures do not always return a Kubernetes Status body.
	reason := "UnknownAPIError"
	switch resp.StatusCode {
	case http.StatusBadRequest:
		reason = "BadRequest"
	case http.StatusUnauthorized:
		reason = "Unauthorized"
	case http.StatusForbidden:
		reason = "Forbidden"
	case http.StatusNotFound:
		reason = "NotFound"
	case http.StatusConflict:
		reason = "Conflict"
	case http.StatusUnprocessableEntity:
		reason = "Invalid"
	case http.StatusTooManyRequests:
		reason = "TooManyRequests"
	case http.StatusServiceUnavailable:
		reason = "ServiceUnavailable"
	case http.StatusInternalServerError:
		reason = "InternalError"
	case http.StatusGatewayTimeout:
		reason = "Timeout"
	}
	return &jobSubmissionError{reason: reason}
}

// Logging is a projection, never formatting of an upstream error. Both fields
// are fixed enums; object/session names, bodies and transport URLs are excluded.
func logLaunchFailure(stage string, err error) {
	if !knownLaunchStage(stage) {
		switch stage {
		case "secret_cleanup", "secret_adoption", "writer_cleanup":
		default:
			stage = "queued"
		}
	}
	reason := "UnknownAPIError"
	var failure *jobSubmissionError
	if errors.As(err, &failure) && safeJobSubmissionReason(failure.reason) {
		reason = failure.reason
	}
	log.Printf("cluster launch stage=%s reason=%s", stage, reason)
}
