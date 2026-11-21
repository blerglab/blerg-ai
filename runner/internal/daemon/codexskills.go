package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	workflow "github.com/blerglab/blerg-ai/runner/skills"
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
	if strings.HasPrefix(name, "blerg:") {
		if a.d.skillPack != nil {
			for _, entry := range a.d.skillPack.entries {
				if name != "blerg:"+entry.Name {
					continue
				}
				pack := a.d.skillPack
				body, err := pack.read(a.ctx, entry.Name)
				if err != nil {
					failurelog.Default.Event("skill_explicit_read", slog.LevelWarn, "skill", entry.Name, "cause", failurelog.Cause(err))
					a.d.emit("skill_reporting", workflow.Load{Engine: "codex", Skill: entry.Name, Version: entry.Version(), Mode: "on_demand", Basis: "file_unavailable"})
					return []any{plain("Blerg skill unavailable; do not substitute a repository skill.\n" + text)}
				}
				if string(body) != entry.Body {
					failurelog.Default.Event("skill_explicit_content", slog.LevelWarn, "skill", entry.Name, "cause", "hash_mismatch")
					a.d.emit("skill_reporting", workflow.Load{Engine: "codex", Skill: entry.Name, Version: fmt.Sprintf("%x", sha256.Sum256(body)), Mode: "on_demand", Bytes: len(body), Basis: "content_mismatch"})
					return []any{plain("Blerg skill content mismatch; do not use the replacement.\n" + text)}
				}
				return []any{plain("Blerg skill loaded from " + pack.path(entry.Name) + "\n" + string(body)), plain("The requested Blerg skill is already loaded above. Apply it without reading it again.\n\n" + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0])))}
			}
		}
		return []any{plain(text)}
	}
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

// A verified text input acknowledged by turn/start or turn/steer is an explicit
// context load. Match the submitted private path and exact body, not skills/list.
func (d *codexDriver) recordAcceptedSkillInputs(inputs []any) {
	if d.skillPack == nil {
		return
	}
	for _, input := range inputs {
		item, ok := input.(map[string]any)
		if !ok || item["type"] != "text" {
			continue
		}
		for _, entry := range d.skillPack.entries {
			if item["text"] == "Blerg skill loaded from "+d.skillPack.path(entry.Name)+"\n"+entry.Body {
				d.emit("skill_load", workflow.Load{Engine: "codex", Skill: entry.Name, Version: entry.Version(), Mode: "on_demand", Bytes: len(entry.Body), Basis: "explicit_skill_input_accepted"})
			}
		}
	}
}
