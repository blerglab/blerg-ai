package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestRecordAndListUsageWindowSample(t *testing.T) {
	pool := migratedPool(t, 4)
	ctx := context.Background()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root,desktop_owner_id) VALUES('d','host','/r','owner-1') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	resets := start.Add(7 * 24 * time.Hour)
	sampled := start.Add(time.Hour)
	if err := db.RecordUsageWindowSample(ctx, pool, daemon, "claude", "7d", 12.5, start, resets, sampled); err != nil {
		t.Fatal(err)
	}
	// A second report in the same minute updates the row rather than adding one.
	if err := db.RecordUsageWindowSample(ctx, pool, daemon, "claude", "7d", 13.0, start, resets, sampled); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListOwnerUsageWindowSamples(ctx, pool, "owner-1", "claude", "7d", start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (same-minute reports collapse)", len(got))
	}
	if got[0].UsedPercent != 13.0 {
		t.Fatalf("used_percent = %v, want the latest report (13.0)", got[0].UsedPercent)
	}
	if !got[0].WindowStart.Equal(start) || !got[0].ResetsAt.Equal(resets) {
		t.Fatalf("window_start/resets_at = %v/%v, want %v/%v", got[0].WindowStart, got[0].ResetsAt, start, resets)
	}

	// A different provider, or a request scoped to another owner, is excluded.
	if err := db.RecordUsageWindowSample(ctx, pool, daemon, "codex", "7d", 40, start, resets, sampled); err != nil {
		t.Fatal(err)
	}
	stillClaude, err := db.ListOwnerUsageWindowSamples(ctx, pool, "owner-1", "claude", "7d", start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stillClaude) != 1 {
		t.Fatalf("provider filter: len = %d, want 1", len(stillClaude))
	}
	foreign, err := db.ListOwnerUsageWindowSamples(ctx, pool, "owner-2", "claude", "7d", start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Fatalf("owner filter: len = %d, want 0 for a daemon owned by someone else", len(foreign))
	}
}

func TestListOwnerUsageWindowSamplesOrdersBySampledAtAndRespectsSince(t *testing.T) {
	pool := migratedPool(t, 4)
	ctx := context.Background()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root,desktop_owner_id) VALUES('d','host','/r','owner-1') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	resets := start.Add(7 * 24 * time.Hour)
	// Two samples, three hours apart, plus one from a week earlier (the previous window) that
	// a "since" cutoff just after it should exclude.
	old := start.Add(-7 * 24 * time.Hour)
	for _, s := range []struct {
		at      time.Time
		percent float64
	}{
		{old, 90},
		{start.Add(3 * time.Hour), 10},
		{start.Add(6 * time.Hour), 20},
	} {
		if err := db.RecordUsageWindowSample(ctx, pool, daemon, "claude", "7d", s.percent, start, resets, s.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListOwnerUsageWindowSamples(ctx, pool, "owner-1", "claude", "7d", start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (the old sample is before 'since')", len(got))
	}
	if got[0].UsedPercent != 10 || got[1].UsedPercent != 20 {
		t.Fatalf("not ordered oldest-first: %v, %v", got[0].UsedPercent, got[1].UsedPercent)
	}
}

func TestPruneUsageWindowSamples(t *testing.T) {
	pool := migratedPool(t, 4)
	ctx := context.Background()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons(name,mode,repos_root,desktop_owner_id) VALUES('d','host','/r','owner-1') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-20 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	if err := db.RecordUsageWindowSample(ctx, pool, daemon, "claude", "7d", 50, old.Add(-7*24*time.Hour), old, old); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsageWindowSample(ctx, pool, daemon, "claude", "7d", 60, recent.Add(-7*24*time.Hour), recent.Add(7*24*time.Hour), recent); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneUsageWindowSamples(ctx, pool, daemon, 15*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListOwnerUsageWindowSamples(ctx, pool, "owner-1", "claude", "7d", now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UsedPercent != 60 {
		t.Fatalf("got %v, want only the recent sample to survive pruning", got)
	}
}
