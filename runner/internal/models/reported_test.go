package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSanitizeReported(t *testing.T) {
	in := []Model{
		{ID: "-c", Name: "flag"},
		{ID: "gpt 5", Name: "space"},
		{ID: strings.Repeat("a", 129)},
		{ID: "gpt-a", Name: "  GPT\u200b-A\n ", Description: strings.Repeat("d", 500), Section: "main",
			Efforts: []string{"ultra", "low", "low", "none"}, DefaultEffort: "ultra", EffortKind: "reasoning"},
		{ID: "gpt-a", Name: "dup"},
		{ID: "gpt-b", Section: "top", Efforts: nil, DefaultEffort: "low", EffortKind: "reasoning"},
	}
	got := SanitizeReported("codex", in)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	a, b := got[0], got[1]
	if a.Name != "GPT-A" || len([]rune(a.Description)) != maxDescriptionLen || a.Section != "main" ||
		!slices.Equal(a.Efforts, []string{"none", "low", "ultra"}) || a.DefaultEffort != "ultra" || a.EffortKind != "reasoning" {
		t.Errorf("a = %+v", a)
	}
	// No efforts: no default, no kind, [] not nil; unknown section → overflow.
	if b.Name != "gpt-b" || b.Section != "overflow" || b.Efforts == nil || len(b.Efforts) != 0 || b.DefaultEffort != "" || b.EffortKind != "" {
		t.Errorf("b = %+v", b)
	}
	if got := SanitizeReported("no-such-engine", in); got == nil || len(got) != 0 {
		t.Errorf("unknown engine = %+v", got)
	}
	many := make([]Model, 200)
	for i := range many {
		many[i] = Model{ID: fmt.Sprintf("m-%d", i)}
	}
	if got := SanitizeReported("hermes", many); len(got) != MaxReportedModels {
		t.Errorf("cap: %d", len(got))
	}
}

func TestSanitizeReports(t *testing.T) {
	if SanitizeReports(nil, time.Now()) != nil {
		t.Error("nil in, nil out")
	}
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	got := SanitizeReports(map[string]Report{
		"codex":  {Models: []Model{{ID: "gpt-a"}}},
		"":       {Models: []Model{{ID: "claude-x"}}}, // not the canonical spelling
		"bogus":  {Models: []Model{{ID: "x"}}},
		"hermes": {Models: nil, FetchedAt: now.Add(-time.Hour)},
	}, now)
	if len(got) != 2 || !got["codex"].FetchedAt.Equal(now) || got["hermes"].Models == nil || !got["hermes"].FetchedAt.Equal(now.Add(-time.Hour)) {
		t.Errorf("got %+v", got)
	}
	// A future or garbage fetched_at is the daemon's claim: now stands in.
	got = SanitizeReports(map[string]Report{
		"codex":    {FetchedAt: now.Add(24 * time.Hour)},
		"hermes":   {FetchedAt: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)},
		"openclaw": {FetchedAt: now.Add(time.Minute)}, // within skew: kept
	}, now)
	if !got["codex"].FetchedAt.Equal(now) || !got["hermes"].FetchedAt.Equal(now) || !got["openclaw"].FetchedAt.Equal(now.Add(time.Minute)) {
		t.Errorf("fetched_at clamp: %+v", got)
	}
	if !slices.Contains(Engines(), "codex") || !slices.IsSorted(Engines()) {
		t.Errorf("Engines() = %v", Engines())
	}
}
