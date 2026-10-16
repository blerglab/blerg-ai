package daemon

import (
	"encoding/json"
	"slices"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Read model constraints from this app-server, rather than duplicating a list
// of model names or assuming every model supports every engine effort.
func (a *codexApp) loadModelCatalog() {
	catalog := map[string]models.Model{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"limit": 100, "includeHidden": true}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := a.call("model/list", params)
		if err != nil {
			return
		}
		var r struct {
			Data []struct {
				ID, Model, DefaultReasoningEffort string
				SupportedReasoningEfforts         []struct{ ReasoningEffort string }
			}
			NextCursor string
		}
		if json.Unmarshal(raw, &r) != nil || r.Data == nil {
			return
		}
		for _, m := range r.Data {
			id := m.Model
			if id == "" {
				id = m.ID
			}
			efforts := []string{}
			for _, effort := range m.SupportedReasoningEfforts {
				efforts = append(efforts, effort.ReasoningEffort)
			}
			clean := models.SanitizeReported("codex", []models.Model{{ID: id, Efforts: efforts, DefaultEffort: m.DefaultReasoningEffort, EffortKind: "reasoning"}})
			if len(clean) == 1 {
				catalog[id] = clean[0]
			}
		}
		if r.NextCursor == "" {
			a.d.mu.Lock()
			a.d.modelCatalog = catalog
			a.d.mu.Unlock()
			return
		}
		if r.NextCursor == cursor {
			return
		}
		cursor = r.NextCursor
	}
}

func (d *codexDriver) reconcileEffortLocked(model, effort string) string {
	if m, ok := d.modelCatalog[model]; ok && len(m.Efforts) > 0 && !slices.Contains(m.Efforts, effort) {
		if m.DefaultEffort != "" {
			return m.DefaultEffort
		}
		return m.Efforts[0]
	}
	return effort
}
