package blackboard

import (
	"regexp"
	"strings"
)

// ReleaseStyle controls presentation names, never plan authority or card state.
type ReleaseStyle struct {
	Name   string         `json:"name"`
	Stages []ReleaseStage `json:"stages"`
}
type ReleaseStage struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}
type StyleSuggestion struct {
	Style      ReleaseStyle `json:"style"`
	HasHistory bool         `json:"has_history"`
	Matched    bool         `json:"matched"`
}

func DefaultStyle() ReleaseStyle {
	return ReleaseStyle{Name: "Versioned releases", Stages: []ReleaseStage{{"opened", "Opened"}, {"alpha", "Alpha"}, {"rc", "RC"}, {"final", "Final"}}}
}
func ContinuousStyle() ReleaseStyle {
	return ReleaseStyle{Name: "Continuous delivery", Stages: []ReleaseStage{{"final", "Shipped"}}}
}
func (s ReleaseStyle) Validate() error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 100 || len(s.Stages) > 12 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, stage := range s.Stages {
		if !stageKey.MatchString(stage.Key) || stage.Key == "plan_set" || stage.Key == "plan_committed" || seen[stage.Key] || strings.TrimSpace(stage.Label) == "" || len(stage.Label) > 80 {
			return ErrInvalid
		}
		seen[stage.Key] = true
	}
	return nil
}
func (s ReleaseStyle) Label(key string) string {
	for _, stage := range s.Stages {
		if stage.Key == key {
			return stage.Label
		}
	}
	return key
}

var stageKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
var versionTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

// Detection is a suggestion; callers must not apply it over an existing choice.
func DetectStyle(tags []string) StyleSuggestion {
	result := StyleSuggestion{Style: DefaultStyle(), HasHistory: len(tags) > 0}
	for _, tag := range tags {
		if versionTag.MatchString(tag) {
			result.Matched = true
			return result
		}
	}
	return result
}
