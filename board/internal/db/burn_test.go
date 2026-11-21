package db

import (
	"testing"
	"time"
)

func TestBurnScopeAndConfirmation(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []burnChange{
		{At: start, Card: "a", After: burnState{Release: "r", Size: "M"}},
		{At: start.AddDate(0, 0, 1), Card: "a", After: burnState{Release: "r", Size: "M", Done: true, ToTest: true}},
		{At: start.AddDate(0, 0, 2), Card: "b", After: burnState{Release: "r", Size: "S"}},
		{At: start.AddDate(0, 0, 3), Card: "a", After: burnState{Release: "r", Size: "M", Done: true}},
		{At: start.AddDate(0, 0, 4), Card: "a", After: burnState{Release: "r", Size: "M"}},
	}
	got := buildBurn("r", events, start.AddDate(0, 0, 4))
	if len(got.Count) != 5 || got.Count[1].Done != 1 || got.Count[1].Confirmed != 0 || got.Count[2].Scope != 2 || got.Count[3].Confirmed != 1 || got.Count[4].Done != 0 {
		t.Fatalf("count %+v", got.Count)
	}
	if got.Size[2].Scope != 3 || got.Size[1].Done != 2 {
		t.Fatalf("size %+v", got.Size)
	}
}
