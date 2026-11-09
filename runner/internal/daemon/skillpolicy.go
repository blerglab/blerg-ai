package daemon

import (
	"strings"

	"github.com/blerglab/blerg-ai/runner/skills"
)

const SkillPolicyEnvVar = "BLERG_RUNNER_SKILL_POLICY"

func effectiveSkillPolicy(policy *skills.Policy) skills.Policy {
	if policy != nil {
		return *policy
	}
	// Legacy launches have no verified Main role.
	return skills.Resolve(false)
}

func ccSystemPromptForPolicy(mode string, policy skills.Policy) string {
	return filterSkillGuide(ccSystemPromptFor(mode), policy)
}

func filterSkillGuide(text string, policy skills.Policy) string {
	for _, item := range []struct {
		enabled bool
		body    string
	}{
		{policy.Coordinating, "When acting as Main, apply blerg:coordinating; workers keep their assigned scope. " + skillCatalogEntry("coordinating")},
		{policy.TDD, skillCatalogEntry("test-driven-development")},
		{policy.Planning, skillCatalogEntry("planning")},
		{policy.UX, skillCatalogEntry("ux-design")},
		{policy.Visualizing, skillCatalogEntry("visualizing")},
	} {
		if !item.enabled {
			text = strings.ReplaceAll(text, item.body, "")
		}
	}
	return text
}

func workflowSkillsPrompt(policy skills.Policy) string {
	return skills.AlwaysOn + " " + skills.Compact(policy)
}

func skillsCompact(policy skills.Policy) string { return skills.Compact(policy) }

// Compact-only engines need operation rules without an impossible instruction
// to load a private skill pack that their adapter does not provide.
func compactSessionGuide(mode string, policy skills.Policy) string {
	operations := ccSystemPromptForPolicy(mode, skills.Policy{})
	operations = strings.ReplaceAll(operations, skillCatalogEntry("managing-tickets"), "")
	operations = strings.ReplaceAll(operations, skillCatalogEntry("test-timing"), "")
	return operations + " " + skillsCompact(policy)
}
