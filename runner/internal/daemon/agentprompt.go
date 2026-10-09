package daemon

import (
	"os"
	"path/filepath"
	"strings"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
)

// harnessInstructions is the blerg-runner harness core system prompt: the
// messaging protocol and check-in rules the loop structurally enforces, plus
// workspace/git policy.
const harnessInstructions = `You are a blerg-runner agent session: a coding agent working in a project
workspace, talking to your user through this session's chat rather than a
terminal. The user reads the chat and answers there; you are not unattended
(only a cron or a board-started run is, and is told so). A reply that ends with
a question is a question to the user, and their answer arrives as your next
message: brainstorming, design review and any decision that is theirs happen
that way. Never replace their answer with an assumption because nobody answered
inside the turn.

## Communication protocol (enforced)
- check_in(phase="task_start", summary=<brief plan>) BEFORE your first
  mutating tool call each turn. Mutating calls are rejected until you do.
- check_in(phase="pre_risky", summary=<intent>) immediately before risky
  commands (git push, deploys, publishes, migrations).
- check_in(phase="completion", summary=<what you did + verification status>)
  before ending any turn in which you changed things.
- update(body) for brief progress notes at milestones (sparingly).
- ask(question) BLOCKS until the user answers — use it for decisions that
  need the user; batch questions rather than asking serially.
- note(body) for non-blocking ideas; replies arrive as later messages.

## Working style
- Work on a session branch; never commit directly to main. Push the branch
  before declaring completion.
- Verify before claiming: run the tests, show the output in your summary.
- Use the skill tool to load any relevant skill BEFORE doing work it covers.
- Subagents: agent(prompt, agent_type, model) — multiple calls in one
  message run in parallel.
- Visual work: push_mockup(dir) publishes static HTML/CSS/JS the user can
  open on any device; push_screenshot(file) publishes an image (capture with
  headless chromium). Use them whenever showing beats describing. The user
  can review a published markdown or PDF file, or mark up an image, from the
  chat: the review arrives as a message listing requests with ids. Make the
  changes, publish the file again, and answer each request with
  blerg-runner review reply <id> done|declined "<one line>".`

// BuildSystemPrompt assembles the full system prompt for an agent session:
// harness instructions → the interaction mode's paragraph → user CLAUDE.md →
// project CLAUDE.md → SessionStart hook context → skills list → agent types
// list. interaction is the session's mode ("interactive" or "unattended";
// "" = interactive), the same paragraph a Claude Code session gets.
func BuildSystemPrompt(workDir, homeDir string, skillList []skills.Skill, agentTypes []agent.AgentTypeDef, interaction string) string {
	var b strings.Builder
	b.WriteString(harnessInstructions)
	b.WriteString("\n\n## Who is reading\n\n")
	b.WriteString(interactionParagraph(interaction))

	appendFile := func(title, path string) {
		data, err := os.ReadFile(path) //nolint:gosec // path is CLAUDE.md under the session's work dir or home dir, built by BuildSystemPrompt
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return
		}
		b.WriteString("\n\n## " + title + "\n\n")
		b.Write(data)
	}
	appendFile("User instructions (~/.claude/CLAUDE.md)", filepath.Join(homeDir, ".claude", "CLAUDE.md"))
	appendFile("Project instructions (CLAUDE.md)", filepath.Join(workDir, "CLAUDE.md"))

	if hookCtx := skills.SessionStartContext(workDir, homeDir); strings.TrimSpace(hookCtx) != "" {
		b.WriteString("\n\n## Session context\n\n")
		b.WriteString(hookCtx)
	}
	if len(skillList) > 0 {
		b.WriteString("\n\n## Available skills (load with the skill tool)\n\n")
		b.WriteString(skills.PromptList(skillList))
	}
	if len(agentTypes) > 0 {
		b.WriteString("\n\n## Available agent types\n\n")
		for _, t := range agentTypes {
			b.WriteString("- " + t.Name + ": " + t.Description + "\n")
		}
	}
	return b.String()
}
