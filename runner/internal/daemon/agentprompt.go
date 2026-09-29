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
const harnessInstructions = `You are a blerg-runner agent session: an autonomous coding agent working in a
project workspace, communicating with your user through structured tools
rather than a terminal.

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
  headless chromium). Use them whenever showing beats describing.`

// BuildSystemPrompt assembles the full system prompt for an agent session:
// harness instructions → user CLAUDE.md → project CLAUDE.md → SessionStart
// hook context → skills list → agent types list.
func BuildSystemPrompt(workDir, homeDir string, skillList []skills.Skill, agentTypes []agent.AgentTypeDef) string {
	var b strings.Builder
	b.WriteString(harnessInstructions)

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
