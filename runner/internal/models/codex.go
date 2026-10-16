package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// codexLevel is one supported_reasoning_levels element, which Codex has
// spelled both as {"effort": "high", "description": …} and as a bare "high".
// Any other shape reads as "" (dropped later).
type codexLevel string

func (l *codexLevel) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = codexLevel(s)
		return nil
	}
	var o struct {
		Effort string `json:"effort"`
	}
	if json.Unmarshal(b, &o) == nil {
		*l = codexLevel(o.Effort)
	}
	return nil
}

type rawCodexModel struct {
	Slug                     string       `json:"slug"`
	DisplayName              string       `json:"display_name"`
	Description              string       `json:"description"`
	DefaultReasoningLevel    string       `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexLevel `json:"supported_reasoning_levels"`
	Visibility               string       `json:"visibility"`
	Priority                 *float64     `json:"priority"`
}

// ParseCodexModels reads `codex debug models` output: the models Codex's own
// picker lists (visibility "list"; hidden ones are internal or retired), in
// its priority order (lowest first; entries without one last, in document
// order). An entry that does not decode is skipped; a document that does not
// decode at all is an error.
func ParseCodexModels(data []byte) ([]Model, error) {
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("codex models: %w", err)
	}
	if doc.Models == nil {
		return nil, errors.New("codex models: no models array")
	}
	type ranked struct {
		m        rawCodexModel
		priority float64
	}
	var listed []ranked
	for _, raw := range doc.Models {
		var m rawCodexModel
		if json.Unmarshal(raw, &m) != nil || m.Visibility != "list" {
			continue
		}
		p := math.Inf(1)
		if m.Priority != nil {
			p = *m.Priority
		}
		listed = append(listed, ranked{m, p})
	}
	sort.SliceStable(listed, func(i, j int) bool { return listed[i].priority < listed[j].priority })
	out := make([]Model, 0, len(listed))
	for _, r := range listed {
		efforts := make([]string, 0, len(r.m.SupportedReasoningLevels))
		for _, l := range r.m.SupportedReasoningLevels {
			efforts = append(efforts, string(l))
		}
		out = append(out, Model{
			ID: r.m.Slug, Name: r.m.DisplayName, Description: r.m.Description,
			// Codex's own picker shows every listed model at once.
			Section: "main", Efforts: efforts, DefaultEffort: r.m.DefaultReasoningLevel,
			EffortKind: "reasoning",
		})
	}
	return SanitizeReported("codex", out), nil
}
