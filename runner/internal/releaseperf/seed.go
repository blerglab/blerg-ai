package releaseperf

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	rdb "github.com/blerglab/blerg-ai/runner/internal/db"
)

// newSeedUUID generates an RFC 4122 v4 UUID string, avoiding a dependency
// on an external uuid package for this one use.
func newSeedUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random bytes: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// SeedVersion invalidates a baseline recorded under a different seed shape:
// see docs/design/release-performance.md's baseline file format.
const SeedVersion = 1

// SeedEpoch anchors every seeded timestamp. It is a fixed literal, never
// time.Now(), so the dataset is byte-identical across every run and every
// release: comparing "this release against last" only means something when
// both measured the same data.
var SeedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	seedDaemonID       = "00000000-0000-0000-0000-00000000da30"
	seedWorkspaceCount = 25
	longTranscriptN    = 8000
	insightsChildren   = 420
	insightsEventsPer  = 400 // 420 * 400 = 168,000, the scale TestInsights7dSeededLatency already proved
	insightsCardRefs   = 1000
)

// SeedSummary reports exactly what SeedRunnerDataset created, so a caller
// (or a test) can assert against the scale the design commits to rather
// than trusting the seeder silently.
type SeedSummary struct {
	Workspaces              int
	LongTranscriptSessionID string
	// SwitchTargetSessionID is a second, distinct seeded workspace: the
	// workspace-switch scenario alternates between it and
	// LongTranscriptSessionID so there is an actual switch to time, not a
	// no-op navigation to the same workspace.
	SwitchTargetSessionID  string
	LongTranscriptEvents   int
	InsightsOwnerSessionID string
	InsightsConversations  int
	InsightsEvents         int
	InsightsCardRefs       int
}

// SeedRunnerDataset creates the runner-side half of the release-perf
// dataset: ~25 workspaces (one holding a several-thousand-event long
// transcript) and a week of Insights-scale activity (420 conversations,
// 168,000 tool events, 1,000 card references) anchored to SeedEpoch. It is
// idempotent: re-running it against the same database clears this account's
// prior seeded rows first rather than duplicating them, so a second run (or
// a retried first run) never doubles the dataset.
func SeedRunnerDataset(ctx context.Context, pool *pgxpool.Pool, accountID string) (SeedSummary, error) {
	return seedRunnerDataset(ctx, pool, accountID, false)
}

func seedRunnerDataset(ctx context.Context, pool *pgxpool.Pool, accountID string, boot bool) (SeedSummary, error) {
	insertSession := rdb.InsertSession
	if boot {
		insertSession = insertBootSession
	}
	if err := rdb.UpsertDaemon(ctx, pool, seedDaemonID, "release-perf", "local", "/repos"); err != nil {
		return SeedSummary{}, fmt.Errorf("upsert seed daemon: %w", err)
	}
	if err := clearPriorSeed(ctx, pool, accountID, !boot); err != nil {
		return SeedSummary{}, fmt.Errorf("clear prior seed: %w", err)
	}

	summary := SeedSummary{Workspaces: seedWorkspaceCount}
	var longID, switchTargetID string
	for i := 0; i < seedWorkspaceCount; i++ {
		id := newSeedUUID()
		title := fmt.Sprintf("Release-perf workspace %d", i)
		// Workspace 0 is the "long, live workspace" the typing scenario
		// types into: a 'stopped' session's composer is disabled
		// (ChatView.tsx's inputLocked), so it must be a live status. Every
		// other workspace only needs to render in a switch/list, so it
		// stays 'stopped' like a real finished session.
		status := "stopped"
		if i == 0 {
			status = "idle"
		}
		if err := insertSession(ctx, pool, id, seedDaemonID, status, "/repos/release-perf", "release-perf", title, ""); err != nil {
			return SeedSummary{}, fmt.Errorf("insert workspace %d: %w", i, err)
		}
		if err := rdb.SetSessionSpawningAccount(ctx, pool, id, accountID); err != nil {
			return SeedSummary{}, fmt.Errorf("set spawning account for workspace %d: %w", i, err)
		}
		started := SeedEpoch.Add(-time.Duration(seedWorkspaceCount-i) * time.Hour)
		if _, err := pool.Exec(ctx, "UPDATE sessions SET started_at = $2 WHERE id = $1", id, started); err != nil {
			return SeedSummary{}, fmt.Errorf("anchor workspace %d timestamp: %w", i, err)
		}
		if i == 0 {
			longID = id
		}
		if i == 1 {
			switchTargetID = id
		}
	}
	summary.LongTranscriptSessionID = longID
	summary.SwitchTargetSessionID = switchTargetID

	// The long-transcript workspace: several thousand agent_events, the same
	// tool_call/tool_result shape TestInsights7dSeededLatency already uses.
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events(session_id, seq, client_event_id, kind, payload, ts)
		SELECT $1, n, gen_random_uuid(),
		       CASE WHEN n % 2 = 1 THEN 'tool_call' ELSE 'tool_result' END,
		       CASE WHEN n % 2 = 1
		            THEN jsonb_build_object('tool', 'Bash', 'call_id', ((n+1)/2)::text,
		                                     'input', jsonb_build_object('command', 'go test ./...'))
		            ELSE jsonb_build_object('call_id', (n/2)::text, 'is_error', false)
		       END,
		       $2::timestamptz + (n * interval '30 seconds')
		FROM generate_series(1, $3) n`, longID, SeedEpoch, longTranscriptN); err != nil {
		return SeedSummary{}, fmt.Errorf("seed long transcript events: %w", err)
	}
	summary.LongTranscriptEvents = longTranscriptN

	// Insights week: reuse the second workspace as the "topic owner" and
	// seed 420 completed/running children, 168,000 tool events and 1,000
	// card references under it — the exact scale
	// TestInsights7dSeededLatency already proved out, anchored to SeedEpoch
	// instead of now() so it never goes stale or drifts between runs.
	insightsOwner := newSeedUUID()
	if err := insertSession(ctx, pool, insightsOwner, seedDaemonID, "running", "/repos/release-perf", "release-perf", "Release-perf insights owner", ""); err != nil {
		return SeedSummary{}, fmt.Errorf("insert insights owner: %w", err)
	}
	if err := rdb.SetSessionSpawningAccount(ctx, pool, insightsOwner, accountID); err != nil {
		return SeedSummary{}, fmt.Errorf("set insights owner spawning account: %w", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE sessions SET started_at = $2 WHERE id = $1", insightsOwner, SeedEpoch); err != nil {
		return SeedSummary{}, fmt.Errorf("anchor insights owner: %w", err)
	}
	summary.InsightsOwnerSessionID = insightsOwner

	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions(daemon_id, project_path, repo, title, spawning_account_id,
		                      parent_session_id, started_at, ended_at, engine)
		SELECT s.daemon_id, s.project_path, s.repo, 'Seeded worker ' || n, s.spawning_account_id, s.id,
		       $2::timestamptz + (n * interval '20 minutes'),
		       CASE WHEN n <= 400 THEN $2::timestamptz + (n * interval '20 minutes') + interval '12100 seconds' END,
		       CASE WHEN n % 2 = 0 THEN 'codex' ELSE 'claude' END
		FROM sessions s CROSS JOIN generate_series(1, $3) n
		WHERE s.id = $1`, insightsOwner, SeedEpoch, insightsChildren); err != nil {
		return SeedSummary{}, fmt.Errorf("seed insights children: %w", err)
	}
	summary.InsightsConversations = insightsChildren

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_events(session_id, seq, client_event_id, kind, payload, ts)
		SELECT s.id, n, gen_random_uuid(),
		       CASE WHEN n % 2 = 1 THEN 'tool_call' ELSE 'tool_result' END,
		       CASE WHEN n % 2 = 1
		            THEN jsonb_build_object('tool', 'Bash', 'call_id', ((n+1)/2)::text,
		                                     'input', jsonb_build_object('command', 'go test ./...'))
		            ELSE jsonb_build_object('call_id', (n/2)::text, 'is_error', false)
		       END,
		       s.started_at + (n * interval '30 seconds')
		FROM sessions s CROSS JOIN generate_series(1, $2) n
		WHERE s.parent_session_id = $1`, insightsOwner, insightsEventsPer); err != nil {
		return SeedSummary{}, fmt.Errorf("seed insights events: %w", err)
	}
	summary.InsightsEvents = insightsChildren * insightsEventsPer

	if boot {
		return summary, nil
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO session_card_links(session_id, board_id, card_number, source, linked_at)
		SELECT s.id, 'release-perf-board', n, 'review', $2::timestamptz - (n * interval '1 minute')
		FROM generate_series(1, $3) n
		JOIN (SELECT id, row_number() OVER (ORDER BY id) AS rn FROM sessions WHERE parent_session_id = $1) s
		  ON s.rn = (n - 1) % $4 + 1`, insightsOwner, SeedEpoch, insightsCardRefs, insightsChildren); err != nil {
		return SeedSummary{}, fmt.Errorf("seed card references: %w", err)
	}
	summary.InsightsCardRefs = insightsCardRefs

	return summary, nil
}

// clearPriorSeed removes a prior seed run's rows for this account, in
// dependency order, so SeedRunnerDataset is safe to re-run.
func clearPriorSeed(ctx context.Context, pool *pgxpool.Pool, accountID string, cardLinks bool) error {
	const q = `
WITH owned AS (
	SELECT id FROM sessions WHERE spawning_account_id = $1
	UNION
	SELECT c.id FROM sessions c JOIN sessions p ON c.parent_session_id = p.id WHERE p.spawning_account_id = $1
)
DELETE FROM agent_events WHERE session_id IN (SELECT id FROM owned)`
	if _, err := pool.Exec(ctx, q, accountID); err != nil {
		return fmt.Errorf("clear agent_events: %w", err)
	}
	const q2 = `
WITH owned AS (
	SELECT id FROM sessions WHERE spawning_account_id = $1
	UNION
	SELECT c.id FROM sessions c JOIN sessions p ON c.parent_session_id = p.id WHERE p.spawning_account_id = $1
)
DELETE FROM session_card_links WHERE session_id IN (SELECT id FROM owned)`
	if cardLinks {
		if _, err := pool.Exec(ctx, q2, accountID); err != nil {
			return fmt.Errorf("clear session_card_links: %w", err)
		}
	}
	const q3 = `DELETE FROM sessions WHERE parent_session_id IN (SELECT id FROM sessions WHERE spawning_account_id = $1)`
	if _, err := pool.Exec(ctx, q3, accountID); err != nil {
		return fmt.Errorf("clear child sessions: %w", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM sessions WHERE spawning_account_id = $1", accountID); err != nil {
		return fmt.Errorf("clear sessions: %w", err)
	}
	return nil
}
