package daemon

import (
	"encoding/json"
	"strings"
)

// Resolve explicit slash skills against this runtime's enabled inventory,
// including project and plugin roots. Paths stay inside RPC input, never in
// user-message history or capabilities.
func (a *codexApp) skillInput(text string) []any {
	plain := func(s string) any { return map[string]any{"type": "text", "text": s, "text_elements": []any{}} }
	if a.d.codexToolsRestricted() {
		return []any{plain(text)}
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return []any{plain(text)}
	}
	name := strings.TrimPrefix(fields[0], "/")
	raw, err := a.call("skills/list", map[string]any{"cwds": []string{a.d.runtimeWorkDir()}})
	if err != nil {
		return []any{plain(text)}
	}
	var r struct {
		Data []struct {
			Skills []struct {
				Name, Path string
				Enabled    bool
			}
		}
	}
	if json.Unmarshal(raw, &r) != nil {
		return []any{plain(text)}
	}
	for _, e := range r.Data {
		for _, s := range e.Skills {
			if s.Name == name && s.Enabled && s.Path != "" {
				args := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
				return []any{map[string]any{"type": "skill", "name": name, "path": s.Path}, plain("Use the " + name + " skill.\n\n" + args)}
			}
		}
	}
	return []any{plain(text)}
}
