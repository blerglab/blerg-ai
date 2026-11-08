package main

import (
	"fmt"
	"sort"
	"strings"
)

// declOrder is the declaration order the existing dark/light blocks use (brand/tokens.css), kept
// stable here so a diff against a re-generated block only ever shows value changes, never churn
// from map iteration order.
var declOrder = []string{
	"--fill-muted", "--button-fg",
	"",
	"color-scheme",
	"--terminal-black", "--terminal-red", "--terminal-green", "--terminal-yellow", "--terminal-blue",
	"--terminal-magenta", "--terminal-cyan", "--terminal-white",
	"--terminal-bright-black", "--terminal-bright-red", "--terminal-bright-green", "--terminal-bright-yellow",
	"--terminal-bright-blue", "--terminal-bright-magenta", "--terminal-bright-cyan", "--terminal-bright-white",
	"--code-keyword", "--code-string", "--code-number", "--code-comment", "--code-title", "--code-type",
	"--code-variable", "--code-builtin",
	"",
	"--basalt", "--scree", "--scree-2", "--stone", "--fog", "--fog-dim", "--chalk",
	"--blaze", "--blaze-soft", "--lichen", "--amber", "--danger", "--batch",
	"--selected-border", "--selected-bg", "--selected-text",
	"--diff-add", "--diff-del",
	"",
	"--primary-white-fg", "--runner-primary-fg", "--chat-primary-fg", "--knob-fg",
	"--board-diff-add", "--board-diff-del",
	"--si-series-1", "--si-series-2", "--si-series-3", "--series-fg",
	"--chart-tests", "--chart-build", "--chart-git", "--chart-read", "--chart-edit", "--chart-other",
	"--chart-model", "--chart-waiting", "--chart-idle",
}

// renderBlock renders tokens as a `:root[data-theme="id"] { ... }` block in the same declaration
// order and two-space indent as the existing hand-written blocks.
func renderBlock(id string, tokens map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, ":root[data-theme=\"%s\"] {\n", id)
	seen := map[string]bool{}
	for _, key := range declOrder {
		if key == "" {
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "  %s: %s;\n", key, tokens[key])
		seen[key] = true
	}
	// Any token the map defines but declOrder forgot still gets emitted (sorted, for determinism)
	// rather than silently dropped — a safety net, not the normal path.
	var extra []string
	for key := range tokens {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		fmt.Fprintf(&b, "  %s: %s;\n", key, tokens[key])
	}
	b.WriteString("}\n")
	return b.String()
}

// renderSessionBlock renders the twelve session-color-* tokens as their own
// `:root[data-theme="id"] { ... }` block — the "Session identity palette" section of
// brand/tokens.css declares these separately from the main palette block, same selector reused.
func renderSessionBlock(id string, colors map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, ":root[data-theme=\"%s\"] {\n", id)
	for _, name := range sessionColorNames {
		key := "--session-color-" + name
		fmt.Fprintf(&b, "  %s: %s;\n", key, colors[key])
	}
	b.WriteString("}\n")
	return b.String()
}
