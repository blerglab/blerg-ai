package daemon

// Native usage partitions replace, rather than supplement, the legacy root
// aggregate. An empty model key is explicitly unknown, never a model alias.
import (
	"regexp"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

var usageModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`)

const rootUsageModelCap = 32
const rootUsageMessageCap = 4096
const rootUsageCountCap = 999999999999999

func validRootUsage(u agent.Usage) bool {
	return u.InputTokens >= 0 && u.InputTokens <= rootUsageCountCap && u.OutputTokens >= 0 && u.OutputTokens <= rootUsageCountCap && u.CacheReadTokens >= 0 && u.CacheReadTokens <= rootUsageCountCap && u.CacheWriteTokens >= 0 && u.CacheWriteTokens <= rootUsageCountCap
}
func rootUsageWithin(a, b agent.Usage) bool {
	return a.InputTokens <= b.InputTokens && a.OutputTokens <= b.OutputTokens && a.CacheReadTokens <= b.CacheReadTokens && a.CacheWriteTokens <= b.CacheWriteTokens
}
func rootUsageModel(model string) string {
	if len(model) > 128 || !usageModelID.MatchString(model) {
		return ""
	}
	return model
}

// reconcileRootUsage keeps identified native usage only when it fits every
// component of the authoritative root aggregate. Unidentified remainder stays
// unknown; inconsistent or oversized partitions fall back to that aggregate.
func reconcileRootUsage(total agent.Usage, parts map[string]agent.Usage) map[string]agent.Usage {
	if len(parts) == 0 {
		return nil
	}
	unknown := func() map[string]agent.Usage { return map[string]agent.Usage{"": total} }
	if !validRootUsage(total) || len(parts) > rootUsageModelCap {
		return unknown()
	}
	out := make(map[string]agent.Usage, len(parts)+1)
	var sum agent.Usage
	for model, u := range parts {
		if len(model) > 128 || model != "" && !usageModelID.MatchString(model) || !validRootUsage(u) {
			return unknown()
		}
		sum = addNativeUsage(sum, u)
		if !rootUsageWithin(sum, total) {
			return unknown()
		}
		out[model] = u
	}
	remainder := nonnegativeUsageDelta(total, sum)
	if remainder != (agent.Usage{}) {
		out[""] = addNativeUsage(out[""], remainder)
	}
	if len(out) > rootUsageModelCap {
		return unknown()
	}
	return out
}

type codexRootUsage struct {
	model     string
	ambiguous bool
	seen      bool
	base      agent.Usage
	usage     agent.Usage
	parts     map[string]agent.Usage
}

func newCodexRootUsage(model string) *codexRootUsage {
	return &codexRootUsage{model: rootUsageModel(model), parts: map[string]agent.Usage{}}
}
func (u *codexRootUsage) changeModel(model string) {
	model = rootUsageModel(model)
	if model != u.model {
		u.ambiguous = true
		u.model = model
	}
}
func (u *codexRootUsage) observe(last agent.Usage, total *agent.Usage) {
	if !validRootUsage(last) || (total != nil && !validRootUsage(*total)) {
		return
	}
	candidate := last
	if total != nil {
		if !u.seen {
			u.base = nonnegativeUsageDelta(*total, last)
			u.seen = true
		}
		candidate = nonnegativeUsageDelta(*total, u.base)
	}
	// Native thread totals may repeat or arrive out of order. A decrease is
	// not evidence of a new accounting epoch and must not erase/re-add tokens.
	delta := nonnegativeUsageDelta(candidate, u.usage)
	u.usage = addNativeUsage(u.usage, delta)
	model := u.model
	if u.ambiguous {
		model = ""
	}
	if delta != (agent.Usage{}) {
		u.parts[model] = addNativeUsage(u.parts[model], delta)
	}
}
func (u *codexRootUsage) snapshot() (agent.Usage, map[string]agent.Usage) {
	return u.usage, reconcileRootUsage(u.usage, u.parts)
}

type rootMessageUsage struct {
	model string
	usage agent.Usage
}
type claudeRootUsage struct {
	messages map[string]rootMessageUsage
	overflow bool
	// The context meter (agent.ContextUsage): the latest main-agent call's prompt and its model,
	// and whether the CLI compacted the conversation since the turn began.
	context agent.ContextUsage
	model   string
}

func (u *claudeRootUsage) observe(ev *ccLine) {
	if ev.ParentToolUseID != "" || ev.Message.ID == "" || len(ev.Message.ID) > 256 || ev.Message.Model == "" || !validRootUsage(ev.Message.Usage.usage()) {
		return
	}
	// The prompt the model read on this call is the conversation's size (Claude Code's own
	// reading): what was not cached, what was read from the cache and what was written to it.
	if t := ev.Message.Usage; t.InputTokens+t.CacheReadInputTokens+t.CacheCreationInputTokens > 0 {
		u.context.Tokens = t.InputTokens + t.CacheReadInputTokens + t.CacheCreationInputTokens
		u.model = ev.Message.Model
	}
	if u.messages == nil {
		u.messages = map[string]rootMessageUsage{}
	}
	id := ev.Message.ID
	model := rootUsageModel(ev.Message.Model)
	old, exists := u.messages[id]
	if !exists && len(u.messages) >= rootUsageMessageCap {
		u.overflow = true
		return
	}
	current := ev.Message.Usage.usage()
	if exists {
		if old.model != model {
			model = ""
		} // one identity with contradictory model reports
		current = addNativeUsage(old.usage, nonnegativeUsageDelta(current, old.usage))
	}
	u.messages[id] = rootMessageUsage{model: model, usage: current}
}
func (u *claudeRootUsage) partition(total agent.Usage) map[string]agent.Usage {
	if len(u.messages) == 0 && !u.overflow {
		return nil
	}
	parts := map[string]agent.Usage{}
	for _, m := range u.messages {
		if _, exists := parts[m.model]; !exists && len(parts) >= rootUsageModelCap {
			return map[string]agent.Usage{"": total}
		}
		parts[m.model] = addNativeUsage(parts[m.model], m.usage)
	}
	return reconcileRootUsage(total, parts)
}

// compacted records the CLI's compaction (its compact_boundary line): the reading before it no
// longer describes the conversation; the next call's does.
func (u *claudeRootUsage) compacted(preTokens int) {
	u.context.Tokens = 0
	u.context.Compacted = true
	if preTokens > 0 && preTokens <= rootUsageCountCap {
		u.context.CompactedFrom = preTokens
	}
}

// compactionOnly is what a turn that ended without a result line says about its context: that the
// CLI compacted, when it did (the reading before is gone; without a result line no window is known
// for a count). Nil otherwise: the previous turn's reading still holds.
func (u *claudeRootUsage) compactionOnly() *agent.ContextUsage {
	if !u.context.Compacted {
		return nil
	}
	return &agent.ContextUsage{Compacted: true, CompactedFrom: u.context.CompactedFrom}
}

// contextUsage is the turn's context reading for its turn_done, with the window the result line
// reports for the model the reading came from. Nil when the turn said nothing about it.
func (u *claudeRootUsage) contextUsage(result *ccLine) *agent.ContextUsage {
	c := u.context
	if c.Tokens > 0 {
		c.Window = result.contextWindow(u.model)
	}
	return c.Reported()
}
