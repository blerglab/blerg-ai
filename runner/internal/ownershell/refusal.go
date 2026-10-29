package ownershell

import "errors"

// Refusal reasons are fixed codes. Never carry upstream error text, paths,
// credentials or user input into an HTTP response, audit or launch receipt.
const (
	ReasonNotReady          Reason = "session_not_ready"
	ReasonBinding           Reason = "binding_refused"
	ReasonPolicyUnavailable Reason = "policy_unavailable"
	ReasonPolicyDisabled    Reason = "policy_disabled"
	ReasonAuthority         Reason = "authority_refused"
	ReasonOpenRuntime       Reason = "runtime_refused"
	ReasonRuntimeBinding    Reason = "runtime_binding_refused"
	ReasonRuntimeConsent    Reason = "runtime_consent_refused"
	ReasonRuntimeIdentity   Reason = "runtime_identity_refused"
	ReasonRuntimeCleanup    Reason = "runtime_cleanup_refused"
	ReasonRuntimeStart      Reason = "runtime_start_refused"
	ReasonRuntimeFenced     Reason = "runtime_fenced"
	ReasonRuntimeCapability Reason = "runtime_capability_refused"
	ReasonRuntimePolicy     Reason = "runtime_policy_refused"
	// ReasonRuntimeBusy is retryable: the desktop had no free slot to verify the binding.
	// It proves nothing about the binding, so it is never a fence or a revocation.
	ReasonRuntimeBusy Reason = "runtime_busy"
)

type refusal struct {
	reason Reason
	cause  string
}

func (r *refusal) Error() string { return ErrDenied.Error() }
func (r *refusal) Unwrap() error { return ErrDenied }

// Refuse preserves errors.Is(ErrDenied) and exposes only a known reason.
func Refuse(reason Reason) error {
	if !SafeRefusalReason(string(reason)) {
		reason = ReasonUnavailable
	}
	return &refusal{reason: reason}
}
func RefusalReason(err error) Reason {
	var r *refusal
	if errors.As(err, &r) && SafeRefusalReason(string(r.reason)) {
		return r.reason
	}
	return ReasonUnavailable
}
func SafeRefusalReason(reason string) bool {
	switch Reason(reason) {
	case ReasonRuntimeBusy, ReasonRuntimeBinding, ReasonRuntimeConsent, ReasonRuntimeIdentity, ReasonRuntimeCleanup, ReasonRuntimeStart, ReasonRuntimeFenced, ReasonRuntimeCapability, ReasonRuntimePolicy, ReasonNotReady, ReasonBinding, ReasonPolicyUnavailable, ReasonPolicyDisabled, ReasonAuthority, ReasonOpenRuntime, ReasonUnavailable:
		return true
	}
	return false
}

// Busy reports a retryable refusal: nothing was verified or refused.
func Busy(err error) bool { return err != nil && RefusalReason(err) == ReasonRuntimeBusy }

// RuntimeReason reads a refusal code from a runtime reply. Busy is created only by the
// server's own attachment backoff, so a runtime claiming it is an ordinary refusal.
func RuntimeReason(code string) Reason {
	if Reason(code) == ReasonRuntimeBusy {
		return ReasonOpenRuntime
	}
	return Reason(code)
}
