package daemon

import "github.com/blerglab/blerg-ai/runner/internal/agent"

// Native engines report the same disjoint input/output/cache buckets. These
// helpers combine usage snapshots without letting an older counter erase spend.
func addNativeUsage(a, b agent.Usage) agent.Usage {
	return agent.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens, CacheReadTokens: a.CacheReadTokens + b.CacheReadTokens, CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens}
}
func nonnegativeUsageDelta(a, b agent.Usage) agent.Usage {
	return agent.Usage{InputTokens: max(0, a.InputTokens-b.InputTokens), OutputTokens: max(0, a.OutputTokens-b.OutputTokens), CacheReadTokens: max(0, a.CacheReadTokens-b.CacheReadTokens), CacheWriteTokens: max(0, a.CacheWriteTokens-b.CacheWriteTokens)}
}
