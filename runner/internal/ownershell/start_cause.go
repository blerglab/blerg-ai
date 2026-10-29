package ownershell

import "errors"

// Fixed diagnostics only; no runtime-supplied error text crosses the HTTP boundary.
const (
	StartCauseMissing     = "helper_missing"
	StartCausePermission  = "permission_denied"
	StartCauseTimeout     = "start_timeout"
	StartCauseBootstrap   = "bootstrap_refused"
	StartCauseUnavailable = "start_unavailable"
)

func SafeStartCause(cause string) string {
	if _, ok := startStages[cause]; ok {
		return cause
	}
	switch cause {
	case StartCauseMissing, StartCausePermission, StartCauseTimeout, StartCauseBootstrap, StartCauseUnavailable:
		return cause
	}
	return ""
}
func RefuseStart(reason Reason, cause string) error {
	return &refusal{reason: RefusalReason(Refuse(reason)), cause: SafeStartCause(cause)}
}
func StartCause(err error) string {
	var r *refusal
	if errors.As(err, &r) {
		return SafeStartCause(r.cause)
	}
	return ""
}

// startStages is the fixed vocabulary shared by runtime diagnostics and owner UI.
// Never add paths, account names, command text or environment values here.
var startStages = map[string]string{
	"helper_start":        "The shell helper could not be launched. Check the service process permissions and restrictions.",
	"workspace_admission": "The session workspace could not authorize this shell launch. Reconnect the desktop.",
	"bootstrap_read":      "The shell helper could not read its launch request.",
	"bootstrap_validate":  "The shell helper rejected its launch request.",
	"resolve_root":        "The shell project directory could not be resolved.",
	"resolve_identity":    "The shell account could not be resolved.",
	"verify_identity":     "The shell account changed. Review host shell consent again.",
	"verify_cwd":          "The shell working directory changed.",
	"verify_root":         "The shell project directory changed.",
	"verify_grant":        "Shell authorization could not be verified. Reconnect the desktop.",
	"open_root":           "The shell project directory could not be opened.",
	"open_cwd":            "The shell working directory could not be opened.",
	"verify_uid":          "The shell account ID is unsupported.",
	"pty_open":            "The shell terminal could not be opened. Check the service's terminal access.",
	"exec_shell":          "The account shell could not be executed. Check its permissions and service restrictions.",
	"pty_configure":       "The shell terminal could not be configured.",
	"containment_setup":   "Shell containment could not be established. Check the service's tracing and seccomp restrictions.",
	"lease_create":        "The shell authorization expired during startup. Try opening the shell again.",
	"helper_ready":        "The shell helper could not confirm startup.",
	"helper_stopped":      "The shell helper stopped.",
	"helper_input_pipe":   "The shell helper's input channel could not be opened.",
	"helper_output_pipe":  "The shell helper's output channel could not be opened.",
	"helper_record":       "The shell helper's ownership could not be recorded.",
	"helper_write":        "The shell helper's request could not be sent.",
	"helper_wait":         "The shell helper did not acknowledge the request before it was canceled or timed out.",
	"helper_reply":        "The shell helper rejected a control request.",
	"admit_shell":         "The shell launch is paused or awaiting recovery.",
	"verify_binding":      "The shell launch changed. Reconnect the desktop and review consent again.",
	"verify_reservation":  "The shell launch reservation is no longer valid.",
	"verify_policy":       "Shell authorization changed. Reconnect the desktop.",
	"grant_clock":         "This desktop's clock is off by more than a few seconds. Sync it (on WSL: sudo hwclock -s), then try again.",
	"cleanup_shell":       "Shell cleanup could not be verified. Check the desktop daemon log.",
	"control_read":        "The shell helper's control channel closed.",
	"control_write":       "The shell helper could not acknowledge a control request.",
	"control_grant":       "The shell helper rejected control authorization.",
	"lease_check":         "The shell authorization expired or was revoked.",
	"input_sequence":      "The shell helper rejected the input sequence.",
	"resize_sequence":     "The shell helper rejected the terminal resize sequence.",
	"control_operation":   "The shell helper rejected a control operation.",
	"control_apply":       "The shell helper could not apply a control operation.",
}

func SafeStartStage(stage string) string {
	if _, ok := startStages[stage]; ok {
		return stage
	}
	return ""
}
func StartStageMessage(stage string) string { return startStages[stage] }
