package models

import (
	"regexp"
	"sort"
	"time"
)

// Report is one engine's model list as a daemon probed it from the engine's
// own CLI or endpoint (see the daemon's EngineSpec.ListModels), carried in
// the daemon's hello/heartbeat. An empty Models means "probed, nothing to
// offer" (no list): the launch sheet then shows no picker for that engine.
type Report struct {
	Models    []Model   `json:"models"`
	FetchedAt time.Time `json:"fetched_at"`
}

const (
	// maxReportedEngines caps how many engines one daemon report may carry.
	maxReportedEngines = 16
	// MaxReportedModels caps how many models one engine's report may carry.
	MaxReportedModels = maxModels
)

// effortKindPattern is the shape of an EffortKind label.
var effortKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// SanitizeReported checks one engine's reported list as untrusted input — a
// probe reads it from a CLI or a network endpoint, and the server receives it
// from a daemon — and returns only what may be shown and later launched: an
// id that passes the engine's own model rule (duplicates dropped), a name
// and description stripped of control and format characters and capped
// (the name falls back to the id), a section of main or overflow, efforts
// limited to the engine's allowlist in its order (never nil), a default
// effort that is one of them or "", and an effort kind that is a short
// lowercase token or "". At most MaxReportedModels entries survive, in the
// order given. An engine with no registered rules gets nothing.
func SanitizeReported(engine string, in []Model) []Model {
	out := []Model{}
	if !HasEngineRules(engine) {
		return out
	}
	allow := EffortsFor(engine)
	seen := map[string]bool{}
	for _, m := range in {
		if len(out) >= MaxReportedModels {
			break
		}
		if m.ID == "" || seen[m.ID] || !ValidModelFor(engine, m.ID) {
			continue
		}
		seen[m.ID] = true
		efforts := []string{}
		for _, e := range allow {
			if contains(m.Efforts, e) {
				efforts = append(efforts, e)
			}
		}
		c := Model{
			ID:          m.ID,
			Name:        clean(m.Name, maxNameLen),
			Description: clean(m.Description, maxDescriptionLen),
			Section:     "overflow",
			Efforts:     efforts,
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		if m.Section == "main" {
			c.Section = "main"
		}
		if contains(efforts, m.DefaultEffort) {
			c.DefaultEffort = m.DefaultEffort
		}
		if len(efforts) > 0 && effortKindPattern.MatchString(m.EffortKind) {
			c.EffortKind = m.EffortKind
		}
		out = append(out, c)
	}
	return out
}

// Bounds on a reported FetchedAt (see SanitizeReports).
var minFetchedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

const maxFetchedAtSkew = 5 * time.Minute

// SanitizeReports applies SanitizeReported to every engine in a daemon's
// report: engines without registered rules are dropped, at most
// maxReportedEngines are kept (lowest engine ids first, so the cut is
// deterministic), and a FetchedAt that is missing, before 2020 or more than
// five minutes in the future becomes now. nil in, nil out.
func SanitizeReports(in map[string]Report, now time.Time) map[string]Report {
	if in == nil {
		return nil
	}
	engines := make([]string, 0, len(in))
	for e := range in {
		if HasEngineRules(e) && e == NormalizeEngine(e) {
			engines = append(engines, e)
		}
	}
	sort.Strings(engines)
	if len(engines) > maxReportedEngines {
		engines = engines[:maxReportedEngines]
	}
	out := make(map[string]Report, len(engines))
	for _, e := range engines {
		r := in[e]
		t := r.FetchedAt
		// Missing, implausibly old, or in the future (beyond a little clock
		// skew): it is the daemon's claim, so the receipt time stands in.
		if t.Before(minFetchedAt) || t.After(now.Add(maxFetchedAtSkew)) {
			t = now
		}
		out[e] = Report{Models: SanitizeReported(e, r.Models), FetchedAt: t.UTC()}
	}
	return out
}

// Engines returns every engine id with registered rules, sorted.
func Engines() []string {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	out := make([]string, 0, len(rules))
	for e := range rules {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}
