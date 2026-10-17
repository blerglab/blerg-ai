package daemon

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

const codexMaxKnownChildren = 512

func validCodexThreadID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// A recovery record proves ownership, never current execution. Its previous
// display identity is retained only to link the next, independently identified run.
func (d *codexDriver) seedKnownChildren(children []OpenSubagent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.children == nil {
		d.children = map[string]*codexChild{}
	}
	for _, child := range children {
		if len(d.children) >= codexMaxKnownChildren {
			break
		}
		if !validCodexThreadID(child.AgentID) || child.AgentID == d.threadID || d.children[child.AgentID] != nil {
			continue
		}
		id := child.ChildID
		if !validCodexThreadID(id) {
			id = ccUUID()
		}
		total := agent.Usage{}
		if child.CumulativeUsage != nil {
			total = *child.CumulativeUsage
		}
		effort := child.Effort
		if !models.ValidEffortFor("codex", effort) {
			effort = ""
		}
		d.children[child.AgentID] = &codexChild{callID: id, effort: effort, model: cutRunes(child.Model, 128), summary: cutRunes(strings.TrimSpace(child.Summary), 200), done: true, started: time.Now(), lastTotal: total}
	}
}

// Metadata from the native database is admitted only through an ancestry chain
// rooted in this conversation (or an already-proven child). A same-account
// sibling's history is never enough to authorize its notifications or questions.
func (a *codexApp) discoverOwnedChildren() {
	root := a.threadID()
	if !validCodexThreadID(root) {
		return
	}
	type candidate = codexChildMetadata
	candidates := map[string]candidate{}
	cursor := ""
	for page := 0; page < 10; page++ {
		p := map[string]any{"limit": 100, "sourceKinds": []string{"subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther"}, "useStateDbOnly": true}
		if cursor != "" {
			p["cursor"] = cursor
		}
		raw, err := a.call("thread/list", p)
		if err != nil {
			break
		}
		var reply struct {
			Data       []candidate
			NextCursor string
		}
		if json.Unmarshal(raw, &reply) != nil || reply.Data == nil {
			break
		}
		for _, child := range reply.Data {
			if len(candidates) >= 1024 {
				break
			}
			child.ParentThreadID = child.parent()
			if child.ID != root && validCodexThreadID(child.ID) && validCodexThreadID(child.ParentThreadID) {
				child.Model = cutRunes(child.Model, 128)
				child.Preview = cutRunes(child.Preview, 200)
				candidates[child.ID] = child
			}
		}
		if reply.NextCursor == "" || reply.NextCursor == cursor {
			break
		}
		cursor = reply.NextCursor
	}
	owned := map[string]bool{root: true}
	a.d.mu.Lock()
	for id := range a.d.children {
		owned[id] = true
	}
	a.d.mu.Unlock()
	additions := []OpenSubagent{}
	for changed := true; changed && len(additions) < codexMaxKnownChildren; {
		changed = false
		ids := make([]string, 0, len(candidates))
		for id := range candidates {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			child := candidates[id]
			if owned[id] || !owned[child.ParentThreadID] {
				continue
			}
			owned[id] = true
			changed = true
			effort := ""
			if child.ReasoningEffort != nil {
				effort = *child.ReasoningEffort
			}
			additions = append(additions, OpenSubagent{Effort: effort, AgentID: id, AgentType: "Codex", Model: child.Model, Summary: child.Preview})
			if len(additions) >= codexMaxKnownChildren {
				break
			}
		}
	}
	a.d.seedKnownChildren(additions)
	a.d.updateOpenWork("subagent_update", nil)
}
