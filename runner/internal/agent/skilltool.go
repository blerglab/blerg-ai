package agent

import (
	"context"
	"encoding/json"

	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
)

// SkillTool returns the "skill" tool: loads a skill's full instructions into
// the turn. The result leads with the skill directory so the model can read
// referenced sibling files (references/*.md, prompt templates) via read_file.
func SkillTool(list []skills.Skill) Tool {
	return skillTool{list}
}

type skillTool struct{ list []skills.Skill }

func (s skillTool) Def() ToolDef {
	return ToolDef{Name: "skill", Description: "Load a skill's full instructions. Invoke a skill BEFORE doing work it covers.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)}
}
func (s skillTool) Mutating() bool { return false }
func (s skillTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	var args struct{ Name string }
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	content, dir, err := skills.Load(s.list, args.Name)
	if err != nil {
		return "", err
	}
	return "Skill directory: " + dir + "\n\n" + content, nil
}
