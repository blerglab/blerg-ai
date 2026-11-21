package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func datasetRowKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
func datasetSince(p map[string]string, now time.Time, defaultAge, maxAge time.Duration) time.Time {
	since := now.Add(-defaultAge)
	if parsed, err := time.Parse(time.RFC3339, p["since"]); err == nil {
		since = parsed
	}
	if since.Before(now.Add(-maxAge)) {
		since = now.Add(-maxAge)
	}
	return since
}
func descendantDataset(name string) bool {
	return name == "blerg.gates.durations" || name == "blerg.gates.bins" || name == "blerg.workers.timeline"
}

// Only the authorization metadata is read at delivery, never rows or aggregates.
// Includes hidden descendants so either direction of a privacy change fences
// every queued frame. Collection captures this before reading payload records.
func datasetPrivacyFence(ctx context.Context, pool *pgxpool.Pool, sid string) (map[string]string, error) {
	if pool == nil {
		return nil, errors.New("database_unavailable")
	}
	var fence map[string]string
	err := pool.QueryRow(ctx, `WITH RECURSIVE ancestors AS (
 SELECT id,parent_session_id,ARRAY[id] AS path FROM sessions WHERE id=$1
 UNION ALL SELECT s.id,s.parent_session_id,a.path||s.id FROM sessions s JOIN ancestors a ON s.id=a.parent_session_id WHERE NOT s.id=ANY(a.path) AND cardinality(a.path)<64
 ), family AS (
 SELECT id,ARRAY[id] AS path FROM ancestors WHERE parent_session_id IS NULL
 UNION ALL SELECT s.id,f.path||s.id FROM sessions s JOIN family f ON s.parent_session_id=f.id WHERE NOT s.id=ANY(f.path) AND cardinality(f.path)<64)
 SELECT coalesce(jsonb_object_agg(s.id::text,md5(s.private::text||':'||coalesce(s.spawning_account_id,''))),'{}'::jsonb) FROM family f JOIN sessions s ON s.id=f.id`, sid).Scan(&fence)
	return fence, err
}

// Membership changes require a fresh sample, but are not revocation. Only
// changed authority on an identity present in both snapshots purges rows.
func datasetFenceChange(before, after map[string]string) string {
	changed := len(before) != len(after)
	for id, authority := range before {
		current, exists := after[id]
		if !exists {
			changed = true
			continue
		}
		if current != authority {
			return "access_removed"
		}
	}
	if changed {
		return "source_changed"
	}
	// A replacement can have equal cardinality.
	for id := range after {
		if _, exists := before[id]; !exists {
			return "source_changed"
		}
	}
	return ""
}

type datasetDeliveryFence struct {
	liveScope  ld.Scope
	validUntil time.Time
	privacy    map[string]string
	revision   uint64
}
type datasetDeliveryContextKey struct{}

func pruneGateReports(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM live_gate_reports WHERE observed_at<clock_timestamp()-interval '30 days'`)
	return err
}

// Coverage metadata is separate from resolver failures: bounded ready rows
// remain usable and carry an explicit truncated cause on the socket frame.
type datasetSampleMetadata struct {
	truncated                               bool
	liveScope                               ld.Scope
	authorityUntil, validUntil, nextAttempt time.Time
	intervalSeconds                         int
}
type datasetSampleMetadataContextKey struct{}

func markDatasetTruncated(ctx context.Context) {
	if metadata, ok := ctx.Value(datasetSampleMetadataContextKey{}).(*datasetSampleMetadata); ok {
		metadata.truncated = true
	}
}

func pruneTestRuns(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM test_runs WHERE finished_at<clock_timestamp()-interval '180 days'`)
	return err
}
