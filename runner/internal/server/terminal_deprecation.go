package server

// New Terminal (tmux) sessions are deprecated: a person or an agent can no longer start one from
// the launch sheet, REST, the browser socket or MCP, and children only ever start agent sessions
// (StartSession). Existing Terminal sessions keep working. Board Assist is exempt for now
// (HandlePostAssist): it still starts a Terminal session until card #103 moves it to an agent
// session in 0.5.0.

const terminalDeprecationMessage = "Terminal sessions are deprecated and will be removed in 0.5.0"

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
