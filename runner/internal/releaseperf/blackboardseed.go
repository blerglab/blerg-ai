package releaseperf

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedBlackboardDataset populates one account-owned blackboard, linked to
// workspaceID, with a goal, a plan item and a recorded event, so opening it
// for the blackboard scenario is a real render rather than an empty state.
// HandleListBlackboards (runner/internal/server/blackboard_plan.go) finds a
// workspace's blackboard by record->>'workspace_id', not by owner, so the
// record must carry the real workspace id — the blackboard-rendered mark
// never fired with the account id there instead, since BlackboardTabs.tsx's
// per-session lookup found no match and rendered the empty state. plan_events
// is append-only by design (an immutable-history trigger refuses
// UPDATE/DELETE on it — see migrations/132_blackboard.sql), so unlike this
// package's other seeders, re-running this one is a no-op once the fixed
// blackboard id already exists, rather than delete-and-recreate.
func SeedBlackboardDataset(ctx context.Context, pool *pgxpool.Pool, accountID, workspaceID string) error {
	const blackboardID = "release-perf-blackboard"
	const goalID = "release-perf-goal"
	const planItemID = "release-perf-plan-item"
	const eventID = "release-perf-event"
	owner := fmt.Sprintf(`{"kind":"account","id":%q}`, accountID)

	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM blackboards WHERE id = $1)", blackboardID).Scan(&exists); err != nil {
		return fmt.Errorf("check for prior seeded blackboard: %w", err)
	}
	if exists {
		return nil
	}

	// The record column is read back by unmarshaling it directly into
	// blackboard.Record (loadBlackboardCore): it must be fully
	// self-describing — "id", "owner", "name", "vision", "version" and
	// "plan_revision" included — not just the table row's own columns.
	// LoadReadPage then re-derives r.ID from that unmarshaled blob and
	// re-queries `WHERE id=$1` with it; a blob missing "id" silently became
	// "" there, so that second query found nothing and the fetch failed
	// with "not found" — which is why BlackboardTabs.tsx always showed the
	// empty/error state and 'blackboard-rendered' never fired. Goal/Item
	// field names must also match blackboard.Record's own types (Goal.Name,
	// not "text"; Record.Items, not "plan_items").
	recordedAt := SeedEpoch.Format("2006-01-02T15:04:05Z")
	record := fmt.Sprintf(`{
		"id": %q,
		"owner": %s,
		"workspace_id": %q,
		"name": "Release-perf project",
		"vision": "Seeded for BLG-453",
		"version": 1,
		"plan_revision": 1,
		"goals": [{"id": %q, "owner": %s, "name": "Ship the release-perf scenario matrix", "version": 1}],
		"items": [{"id": %q, "goal_id": %q, "reason": "Seeded for BLG-453", "raised_by": %s, "state": "open"}],
		"milestones": [],
		"thrusts": [],
		"links": [],
		"events": [{"id": %q, "authenticated_actor": %s, "kind": "milestone", "source": "seed", "recorded_at": %q, "payload": {"text": "Seeded for the release performance check"}}]
	}`, blackboardID, owner, workspaceID, goalID, owner, planItemID, goalID, owner, eventID, owner, recordedAt)

	if _, err := pool.Exec(ctx, `
		INSERT INTO blackboards (id, owner_kind, owner_id, version, record)
		VALUES ($1, 'account', $2, 1, $3::jsonb)`,
		blackboardID, accountID, record); err != nil {
		return fmt.Errorf("create blackboard: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO goals (id, blackboard_id, owner, version, data)
		VALUES ($1, $2, $3::jsonb, 1, '{"text":"Ship the release-perf scenario matrix"}'::jsonb)`,
		goalID, blackboardID, owner); err != nil {
		return fmt.Errorf("create goal: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO plan_items (id, blackboard_id, reason, raised_by, state, data)
		VALUES ($1, $2, 'Seeded for BLG-453', $3::jsonb, 'open', '{}'::jsonb)`,
		planItemID, blackboardID, owner); err != nil {
		return fmt.Errorf("create plan item: %w", err)
	}
	// LoadReadPage re-derives r.Events from this table's own `data` column,
	// discarding the blob's "events" array entirely — it must independently
	// be a well-formed blackboard.Event, not an empty object, or the
	// rendered event list comes back blank.
	eventData := fmt.Sprintf(`{"id": %q, "authenticated_actor": %s, "kind": "milestone", "source": "seed", "recorded_at": %q, "payload": {"text": "Seeded for the release performance check"}}`,
		eventID, owner, recordedAt)
	if _, err := pool.Exec(ctx, `
		INSERT INTO plan_events (id, blackboard_id, authenticated_actor, recorded_at, source, kind, payload, data)
		VALUES ($1, $2, $3::jsonb, $4, 'seed', 'milestone',
		        '{"text":"Seeded for the release performance check"}'::jsonb, $5::jsonb)`,
		eventID, blackboardID, owner, SeedEpoch, eventData); err != nil {
		return fmt.Errorf("create plan event: %w", err)
	}
	return nil
}
