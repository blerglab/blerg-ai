package protocol

const (
	VerbosityNarrated = "narrated"
	VerbosityStandard = "standard"
	VerbosityTerse    = "terse"
)

func ValidVerbosity(value string) bool {
	return value == VerbosityNarrated || value == VerbosityStandard || value == VerbosityTerse
}

// SetSessionVerbosity updates the guidance used for subsequent session input.
type SetSessionVerbosity struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Verbosity string `json:"verbosity"`
}
