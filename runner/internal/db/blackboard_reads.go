package db

import (
	"context"
	"fmt"
	"strconv"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5"
)

const BlackboardReadPartitionBytes = 512 << 10

// Every public history read pages in SQL before transferring JSON. Oversized
// legacy rows retain bounded identity metadata and an explicit omission marker.
// Full Store.Load remains a trusted maintenance/export interface only.
func readBlackboardPage[T any](ctx context.Context, q blackboardReader, from, order, fallback string, offset, limit int, args ...any) ([]T, error) {
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return nil, blackboard.ErrInvalid
	}
	n := len(args)
	args = append(args, limit, offset)
	query := fmt.Sprintf(`WITH page AS (SELECT data,row_number() OVER(ORDER BY %s) pos FROM %s ORDER BY %s LIMIT $%d OFFSET $%d), sized AS (SELECT data,pos,sum(octet_length(data::text)) OVER(ORDER BY pos) bytes FROM page) SELECT CASE WHEN bytes<=%d THEN data ELSE %s END FROM sized ORDER BY pos`, order, from, order, n+1, n+2, BlackboardReadPartitionBytes, fallback)
	return readBlackboardRows[T](ctx, q, query, args...)
}

const eventOmission = `jsonb_build_object('id',left(data->>'id',100),'milestone_id',left(data->>'milestone_id',200),'kind',left(data->>'kind',100),'source',left(data->>'source',20),'recorded_at',data->'recorded_at','authenticated_actor',jsonb_build_object('id',left(data->'authenticated_actor'->>'id',200),'kind',left(data->'authenticated_actor'->>'kind',20)),'payload',jsonb_build_object('omitted',true),'metadata_omitted',true)`
const proposalOmission = `jsonb_build_object('id',left(data->>'id',100),'milestone_id',left(data->>'milestone_id',200),'revision',data->'revision','status',left(data->>'status',20),'actor',jsonb_build_object('id',left(data->'actor'->>'id',200),'kind',left(data->'actor'->>'kind',20)),'changes','[]'::jsonb,'metadata_omitted',true)`
const revisionOmission = `jsonb_build_object('milestone_id',left(data->>'milestone_id',200),'number',data->'number','event_id',left(data->>'event_id',100),'items','[]'::jsonb,'metadata_omitted',true)`
const snapshotOmission = `jsonb_build_object('milestone_id',left(data->>'milestone_id',200),'day',left(data->>'day',10),'observed_at',data->'observed_at','complete',data->'complete','counts','{}'::jsonb,'metadata_omitted',true)`

func (s BlackboardStore) PendingProposals(ctx context.Context, id string) (int, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM plan_proposals WHERE blackboard_id=$1 AND status='pending'`, id).Scan(&count)
	return count, err
}

func (s BlackboardStore) LoadReadPage(ctx context.Context, id string, offset, limit int, owner bool) (blackboard.Record, int, int, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return blackboard.Record{}, 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, err := loadBlackboardCore(ctx, tx, id, "")
	if err != nil {
		return r, 0, 0, err
	}
	total := max(len(r.Items), len(r.Goals), len(r.Thrusts), len(r.Milestones), len(r.Links))
	pending := 0
	var counts [4]int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM plan_events WHERE blackboard_id=$1),(SELECT count(*) FROM plan_revisions WHERE blackboard_id=$1),(SELECT count(*) FROM milestone_snapshots WHERE blackboard_id=$1),(SELECT count(*) FROM plan_proposals WHERE blackboard_id=$1 AND status='pending')`, id).Scan(&counts[0], &counts[1], &counts[2], &pending); err != nil {
		return r, 0, 0, err
	}
	for _, c := range counts[:3] {
		total = max(total, c)
	}
	r.Events, err = readBlackboardPage[blackboard.Event](ctx, tx, "plan_events WHERE blackboard_id=$1", "ordinal", eventOmission, offset, limit, id)
	if err != nil {
		return r, 0, 0, err
	}
	r.Revisions, err = readBlackboardPage[blackboard.Revision](ctx, tx, "plan_revisions WHERE blackboard_id=$1", "scope_id,revision", revisionOmission, offset, limit, id)
	if err != nil {
		return r, 0, 0, err
	}
	r.Snapshots, err = readBlackboardPage[blackboard.Snapshot](ctx, tx, "milestone_snapshots WHERE blackboard_id=$1", "milestone_id,day", snapshotOmission, offset, limit, id)
	if err != nil {
		return r, 0, 0, err
	}
	if owner {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM plan_proposals WHERE blackboard_id=$1`, id).Scan(&n); err != nil {
			return r, 0, 0, err
		}
		total = max(total, n)
		r.Proposals, err = readBlackboardPage[blackboard.Proposal](ctx, tx, "plan_proposals WHERE blackboard_id=$1", "ordinal", proposalOmission, offset, limit, id)
		if err != nil {
			return r, 0, 0, err
		}
	}
	err = tx.Commit(ctx)
	return r, total, pending, err
}

type BlackboardHistoryPage struct {
	Events    []blackboard.Event
	Proposals []blackboard.Proposal
	Snapshots []blackboard.Snapshot
	Total     int
}

func (s BlackboardStore) LoadHistoryPage(ctx context.Context, id, mid, board string, number, offset, limit int) (BlackboardHistoryPage, error) {
	var result BlackboardHistoryPage
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return result, blackboard.ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = checkBlackboardReadScope(ctx, tx, id, mid); err != nil {
		return result, err
	}
	from := `plan_events e WHERE e.blackboard_id=$1 AND (($3='' AND COALESCE(e.milestone_id,'')=$2) OR ($3<>'' AND ((e.payload->>'board_id'=$3 AND e.payload->>'card_number'=$4) OR EXISTS(SELECT 1 FROM plan_proposals p,jsonb_array_elements(p.data->'changes') c WHERE p.blackboard_id=e.blackboard_id AND p.id=e.payload->>'proposal_id' AND p.status='decided' AND p.data->'selected' ? (c->>'id') AND c->'item'->>'board_id'=$3 AND c->'item'->>'card_number'=$4))))`
	args := []any{id, mid, board, strconv.Itoa(number)}
	var events, snaps int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM `+from, args...).Scan(&events); err != nil {
		return result, err
	}
	if board == "" {
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM milestone_snapshots WHERE blackboard_id=$1 AND milestone_id=$2`, id, mid).Scan(&snaps); err != nil {
			return result, err
		}
	}
	result.Total = events + snaps
	if offset < events {
		result.Events, err = readBlackboardPage[blackboard.Event](ctx, tx, from, "recorded_at,ordinal", eventOmission, offset, min(limit, events-offset), args...)
		if err != nil {
			return result, err
		}
	}
	remaining := limit - len(result.Events)
	if remaining > 0 && board == "" {
		result.Snapshots, err = readBlackboardPage[blackboard.Snapshot](ctx, tx, "milestone_snapshots WHERE blackboard_id=$1 AND milestone_id=$2", "day", snapshotOmission, max(0, offset-events), remaining, id, mid)
		if err != nil {
			return result, err
		}
	}
	ids := []string{}
	for _, e := range result.Events {
		if e.Kind == "plan_set" {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) > 0 {
		result.Proposals, err = readBlackboardPage[blackboard.Proposal](ctx, tx, `plan_proposals p WHERE p.blackboard_id=$1 AND p.id IN(SELECT payload->>'proposal_id' FROM plan_events WHERE blackboard_id=$1 AND id=ANY($2::text[]))`, "ordinal", proposalOmission, 0, min(100, len(ids)), id, ids)
		if err != nil {
			return result, err
		}
	}
	err = tx.Commit(ctx)
	return result, err
}

func (s BlackboardStore) LoadTimelinePage(ctx context.Context, id, mid string, offset, limit int) (BlackboardHistoryPage, error) {
	var result BlackboardHistoryPage
	if offset < 0 || offset > 1000000 || limit < 1 || limit > 100 {
		return result, blackboard.ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = checkBlackboardReadScope(ctx, tx, id, mid); err != nil {
		return result, err
	}
	var events, snaps int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM plan_events WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')=$2),(SELECT count(*) FROM milestone_snapshots WHERE blackboard_id=$1 AND milestone_id=$2)`, id, mid).Scan(&events, &snaps); err != nil {
		return result, err
	}
	result.Total = max(events, snaps)
	result.Events, err = readBlackboardPage[blackboard.Event](ctx, tx, "plan_events WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')=$2", "recorded_at,ordinal", eventOmission, offset, limit, id, mid)
	if err != nil {
		return result, err
	}
	result.Snapshots, err = readBlackboardPage[blackboard.Snapshot](ctx, tx, "milestone_snapshots WHERE blackboard_id=$1 AND milestone_id=$2", "day", snapshotOmission, offset, limit, id, mid)
	if err != nil {
		return result, err
	}
	err = tx.Commit(ctx)
	return result, err
}

func checkBlackboardReadScope(ctx context.Context, q blackboardReader, id, mid string) error {
	if mid == "" {
		return nil
	}
	var found bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM milestones WHERE blackboard_id=$1 AND id=$2)`, id, mid).Scan(&found); err != nil {
		return err
	}
	if !found {
		return blackboard.ErrNotFound
	}
	return nil
}
