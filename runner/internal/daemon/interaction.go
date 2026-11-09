package daemon

import "github.com/blerglab/blerg-ai/runner/internal/protocol"

// A session's interaction mode (protocol.SpawnSession.Interaction), stated in the engine's
// system prompt. An engine run headless assumes nobody is watching: it treats a remark as a
// work order and says nothing between tool calls. In Blerg everything the model writes,
// including the text between tool calls, appears in the person's chat as it is written, so an
// interactive session is told so; an unattended one (a board card run, a cron, a tool's job) is
// told the opposite. The server decides which; the wording lives here.

// interactiveParagraph is what an interactive session is told.
const interactiveParagraph = "This session is interactive: a person is reading this chat as you work, and answers here. " +
	"Your words appear live, including what you say between tool calls. " +
	"On a remark or question, answer it and talk it through first; change things only on request or agreement. " +
	"When intent is unclear or the decision is theirs, ask and end your turn: their reply arrives as your next message. " +
	"For their decisions you can work around, ask, not just prose: `blerg-runner ask --no-wait \"<question>\"` keeps it in their Needs you until they answer, and the answer reaches you as a message. " +

	"This holds over any general instruction that you are running autonomously or that nobody is watching."

// unattendedParagraph is what an unattended session is told.
const unattendedParagraph = "This session is unattended: nobody is reading this chat as you work. " +
	"Do not stop to ask and wait for an answer in the chat. " +
	"Make the reasonable decision, write down the assumption you made, and finish the task; end with a summary that stands on its own, since it may be all anyone reads. " +
	"If you are blocked on a decision only a person can make, `blerg-runner ask \"<question>\"` reaches them and waits for the answer."

// interactionParagraph is the paragraph for a mode. Only "unattended" is unattended: an empty
// mode (a server older than the field) and anything unknown are interactive.
func interactionParagraph(mode string) string {
	if mode == protocol.InteractionUnattended {
		return unattendedParagraph + " " + thinkingVoiceParagraph
	}
	return interactiveParagraph + " " + thinkingVoiceParagraph
}

// Reasoning is passed through verbatim; keep visible progress in the speaker's voice.
const thinkingVoiceParagraph = "Thinking and progress use first person: your own words, shortened, or a present tense action ('Restructuring the question'). No third-person meta summary ('the narration', 'the user', 'Claude will')."
