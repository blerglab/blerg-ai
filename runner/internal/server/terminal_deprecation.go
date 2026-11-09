package server

// Terminal sessions retain readable history but cannot be started or resumed.
const terminalDeprecationMessage = "Terminal sessions have been removed. Start an agent session."

// kindRequiredMessage answers a start that names no kind: it used to mean a Terminal session.
const kindRequiredMessage = `kind "agent" is required`

func newSessionKindProblem(kind string) string {
	switch kind {
	case "agent":
		return ""
	case "":
		return kindRequiredMessage
	case "tmux":
		return terminalDeprecationMessage
	default:
		return "invalid session kind"
	}
}
