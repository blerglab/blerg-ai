package daemon

import (
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const VerbosityEnvVar = "BLERG_RUNNER_VERBOSITY"

// Style never changes permission, interaction mode, or required reporting.
func verbosityParagraph(value string) string {
	const common = " Verbosity controls how much you say, independently of interactive or unattended mode. Keep required questions, errors, decisions, and final results visible. A later verbosity instruction supersedes this style. Default delegated workers to Terse unless explicitly asked otherwise. "
	switch value {
	case protocol.VerbosityNarrated:
		return common + "Verbosity: Narrated. Explain your approach before acting and give brief progress updates with findings and next steps. Explain the result and relevant reasoning."
	case protocol.VerbosityTerse:
		return common + "Verbosity: Terse. Keep routine progress quiet. Use short, direct replies and a compact final result with essential verification and blockers."
	default:
		return common + "Verbosity: Standard. Give concise updates at meaningful milestones and a clear final result with relevant verification. Avoid narrating routine tool calls."
	}
}

func (h *AgentHost) SetVerbosity(id, value string) {
	if !protocol.ValidVerbosity(value) {
		return
	}
	if sess := h.get(id); sess != nil {
		sess.verbosity.Store(value)
		sess.verbosityUpdated.Store(true)
		if sess.record != nil {
			sess.record.update(func(r *AgentRecord) bool {
				if r.Verbosity == value {
					return false
				}
				r.Verbosity = value
				return true
			})
		}
	}
}

func (sess *agentSession) withVerbosity(in agent.Input) agent.Input {
	if !sess.verbosityUpdated.Load() {
		return in
	}
	if value, ok := sess.verbosity.Load().(string); ok {
		if in.Shown == "" {
			in.Shown = in.Text
		}
		in.Text = verbosityParagraph(value) + "\n\n" + in.Text
	}
	return in
}
