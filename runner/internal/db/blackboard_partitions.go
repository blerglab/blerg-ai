package db

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ blackboard.PartitionStore = BlackboardStore{}

type blackboardReader interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadBlackboardCore(ctx context.Context, q blackboardReader, id, lock string) (blackboard.Record, error) {
	var data []byte
	err := q.QueryRow(ctx, `SELECT record FROM blackboards WHERE id=$1 `+lock, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return blackboard.Record{}, blackboard.ErrNotFound
	}
	if err != nil {
		return blackboard.Record{}, err
	}
	var r blackboard.Record
	err = json.Unmarshal(data, &r)
	return r, err
}
func readBlackboardRows[T any](ctx context.Context, q blackboardReader, query string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var v T
		if err = json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func loadBlackboardHistory(ctx context.Context, q blackboardReader, r *blackboard.Record) error {
	var err error
	if err = loadBlackboardCuts(ctx, q, r, nil); err != nil {
		return err
	}
	if r.Events, err = readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM plan_events WHERE blackboard_id=$1 ORDER BY ordinal`, r.ID); err != nil {
		return err
	}
	if r.Proposals, err = readBlackboardRows[blackboard.Proposal](ctx, q, `SELECT data FROM plan_proposals WHERE blackboard_id=$1 ORDER BY ordinal`, r.ID); err != nil {
		return err
	}
	if r.Revisions, err = readBlackboardRows[blackboard.Revision](ctx, q, `SELECT data FROM plan_revisions WHERE blackboard_id=$1 ORDER BY scope_id,revision`, r.ID); err != nil {
		return err
	}
	r.Snapshots, err = readBlackboardRows[blackboard.Snapshot](ctx, q, `SELECT data FROM milestone_snapshots WHERE blackboard_id=$1 ORDER BY milestone_id,day`, r.ID)
	return err
}
func (s BlackboardStore) LoadPartition(ctx context.Context, id string, p blackboard.Partition) (blackboard.Record, error) {
	tx, err := s.Tx, error(nil)
	owned := tx == nil
	if owned {
		tx, err = s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	}
	if err != nil {
		return blackboard.Record{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	r, err := loadBlackboardCore(ctx, tx, id, "")
	if err == nil {
		err = loadBlackboardPartition(ctx, tx, &r, p)
	}
	if err == nil && owned {
		err = tx.Commit(ctx)
	}
	return r, err
}
func loadBlackboardPartition(ctx context.Context, q blackboardReader, r *blackboard.Record, p blackboard.Partition) error {
	if p.Kind == blackboard.PartitionCore {
		return nil
	}
	r.Usage = &blackboard.PartitionUsage{}
	var err error
	if p.IncludeCut {
		for _, m := range r.Milestones {
			if m.ID == p.MilestoneID && (p.Kind == blackboard.PartitionPlan || m.FrozenAt != nil) {
				if err = loadBlackboardCuts(ctx, q, r, &p.MilestoneID); err != nil {
					return err
				}
				break
			}
		}
	}
	switch p.Kind {
	case blackboard.PartitionPlan:
		ids := make([]string, len(p.Selected))
		for i, v := range p.Selected {
			ids[i] = string(v)
		}
		if p.ProposalID != "" || len(ids) > 0 {
			r.Proposals, err = readBlackboardRows[blackboard.Proposal](ctx, q, `SELECT data FROM plan_proposals WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')=$2 AND status='pending' AND (($3<>'' AND id=$3) OR ($3='' AND EXISTS(SELECT 1 FROM jsonb_array_elements(data->'changes') c WHERE c->>'id'=ANY($4::text[])))) AND (cardinality($4::text[])=0 OR revision=$5) ORDER BY ordinal LIMIT 2`, r.ID, p.MilestoneID, p.ProposalID, ids, p.Revision)
			if err != nil {
				return err
			}
		}
		r.Events, err = readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM plan_events WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')=$2 AND source='person' AND kind='final_requested' ORDER BY ordinal DESC LIMIT 1`, r.ID, p.MilestoneID)
		if err != nil {
			return err
		}
		r.Snapshots, err = readBlackboardRows[blackboard.Snapshot](ctx, q, `SELECT data FROM milestone_snapshots WHERE blackboard_id=$1 AND milestone_id=$2 ORDER BY day`, r.ID, p.MilestoneID)
	case blackboard.PartitionProposal:
		err = q.QueryRow(ctx, `SELECT count(*) FILTER(WHERE p.blackboard_id=$1 AND p.data->'actor'->>'id'=$3),COALESCE(sum(octet_length(p.data::text)) FILTER(WHERE p.blackboard_id=$1 AND p.data->'actor'->>'id'=$3),0),count(*),COALESCE(sum(octet_length(p.data::text)),0) FROM plan_proposals p JOIN blackboards b ON b.id=p.blackboard_id WHERE COALESCE(p.milestone_id,'')=$2 AND p.status='pending' AND p.data->'actor'->>'kind'='agent' AND COALESCE(NULLIF(b.record->>'workspace_id',''),NULLIF(b.record->'links'->0->>'workspace_id',''),b.id)=$4`, r.ID, p.MilestoneID, p.ActorID, blackboard.WorkspaceKey(*r)).Scan(&r.Usage.Pending, &r.Usage.ProposalBytes, &r.Usage.WorkspacePending, &r.Usage.WorkspaceProposalBytes)
	case blackboard.PartitionEvent:
		r.Events, err = readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM plan_events WHERE blackboard_id=$1 AND id=$2 UNION ALL SELECT data FROM (SELECT data FROM plan_events WHERE blackboard_id=$1 AND COALESCE(milestone_id,'')=$3 AND kind=$4 AND kind IN ('alpha','rc') AND id<>$2 ORDER BY ordinal DESC LIMIT 1) numbered`, r.ID, p.EventID, p.MilestoneID, p.EventKind)
	case blackboard.PartitionObservation:
		r.Events, err = readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM blackboard_observations WHERE blackboard_id=$1 AND scope_id=$2 ORDER BY board_id,card_number`, r.ID, p.MilestoneID)
	}
	if err != nil {
		return err
	}
	if p.Kind == blackboard.PartitionEvent && p.ActorKind != "human" || p.Kind == blackboard.PartitionObservation {
		err = q.QueryRow(ctx, `SELECT event_count,event_bytes FROM blackboard_partition_budgets WHERE blackboard_id=$1 AND scope_id=$2 AND kind=$3 AND actor_id=$4`, r.ID, p.MilestoneID, p.Kind, p.ActorID).Scan(&r.Usage.Events, &r.Usage.EventBytes)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	if err == nil && p.Kind == blackboard.PartitionEvent && p.ActorKind != "human" {
		err = q.QueryRow(ctx, `SELECT event_count,event_bytes FROM blackboard_workspace_budgets WHERE workspace_id=$1 AND scope_id=$2`, blackboard.WorkspaceKey(*r), p.MilestoneID).Scan(&r.Usage.WorkspaceEvents, &r.Usage.WorkspaceEventBytes)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	return err
}
func (s BlackboardStore) UpdatePartition(ctx context.Context, id string, p blackboard.Partition, fn func(*blackboard.Record) error) error {
	tryOnly := p.TryOnly || p.Kind == blackboard.PartitionObservation
	if tryOnly {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, BlackboardAutomaticWait)
		defer cancel()
	}
	if p.ActorKind == "agent" || p.Kind == blackboard.PartitionObservation {
		leave, err := s.admitAutomaticWrite(ctx, id, tryOnly)
		if err != nil {
			return err
		}
		defer leave()
		var release func()
		ctx, release, err = s.AdmitAgentWrite(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	// Best-effort evidence skips an exhausted pool. A concurrent checkout can
	// race this hint, so its complete operation also has the deadline above.
	if tryOnly && s.Pool.Stat().IdleConns() == 0 {
		return blackboard.ErrConflict
	}
	tx, err := s.Tx, error(nil)
	owned := tx == nil
	if owned {
		tx, err = s.Pool.Begin(ctx)
	}
	if err != nil {
		return err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	person := p.Kind == blackboard.PartitionPlan || p.ActorKind == "human"
	if err = lockBlackboardWriter(ctx, tx, id, person); err != nil {
		return err
	}
	lock := ""
	if person {
		lock = "FOR NO KEY UPDATE"
	} else {
		lock = "FOR KEY SHARE NOWAIT"
	}
	r, err := loadBlackboardCore(ctx, tx, id, lock)
	if err != nil {
		return blackboardLockError(err)
	}
	// Automatic writers never queue with row locks held. One exclusive try-lock
	// covers every write to this blackboard; a waiting person cannot be extended
	// by overlapping generations of automatic milestone share lockers.
	if !person {
		var workspaceAllowed bool
		if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "blackboard-workspace:"+blackboard.WorkspaceKey(r)+":"+p.MilestoneID).Scan(&workspaceAllowed); err != nil {
			return err
		}
		if !workspaceAllowed {
			return blackboard.ErrConflict
		}
		if err = loadBlackboardPartition(ctx, tx, &r, p); err != nil {
			return err
		}
		// Existing IDs must reach the callback's exact replay/conflict checks even
		// when that event filled admission. New deltas still pay all byte/count caps.
		replay := p.Kind == blackboard.PartitionEvent && slices.ContainsFunc(r.Events, func(e blackboard.Event) bool { return e.ID == p.EventID })
		if !replay && (p.Kind == blackboard.PartitionProposal && (r.Usage.Pending >= blackboard.MaxPendingAgentProposals || r.Usage.ProposalBytes >= blackboard.MaxPendingAgentProposalBytes || r.Usage.WorkspacePending >= blackboard.MaxWorkspacePendingProposals || r.Usage.WorkspaceProposalBytes >= blackboard.MaxWorkspaceProposalBytes) || (p.Kind == blackboard.PartitionEvent || p.Kind == blackboard.PartitionObservation) && (r.Usage.Events >= blackboard.MaxAutomaticEvents || r.Usage.EventBytes >= blackboard.MaxPartitionEventBytes) || p.Kind == blackboard.PartitionEvent && (r.Usage.WorkspaceEvents >= blackboard.MaxWorkspaceEvents || r.Usage.WorkspaceEventBytes >= blackboard.MaxWorkspaceEventBytes)) {
			return blackboard.ErrInvalid
		}
		if _, err = tx.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, p.MilestoneID, p.Kind, p.ActorID); err != nil {
			return err
		}
		var n int
		if err = tx.QueryRow(ctx, `SELECT event_count FROM blackboard_partition_budgets WHERE blackboard_id=$1 AND scope_id=$2 AND kind=$3 AND actor_id=$4 FOR UPDATE NOWAIT`, id, p.MilestoneID, p.Kind, p.ActorID).Scan(&n); err != nil {
			return blackboardLockError(err)
		}
		if p.Kind == blackboard.PartitionEvent {
			if _, err = tx.Exec(ctx, `INSERT INTO blackboard_workspace_budgets(workspace_id,scope_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, blackboard.WorkspaceKey(r), p.MilestoneID); err != nil {
				return err
			}
			if err = tx.QueryRow(ctx, `SELECT event_count FROM blackboard_workspace_budgets WHERE workspace_id=$1 AND scope_id=$2 FOR UPDATE NOWAIT`, blackboard.WorkspaceKey(r), p.MilestoneID).Scan(&n); err != nil {
				return blackboardLockError(err)
			}
		}
	}
	if p.MilestoneID != "" {
		mode := "FOR SHARE NOWAIT"
		if person {
			mode = "FOR NO KEY UPDATE"
		}
		var found string
		err = tx.QueryRow(ctx, `SELECT id FROM milestones WHERE blackboard_id=$1 AND id=$2 `+mode, id, p.MilestoneID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return blackboard.ErrNotFound
		}
		if err != nil {
			return blackboardLockError(err)
		}
	}
	if !person {
		r, err = loadBlackboardCore(ctx, tx, id, "")
		if err != nil {
			return err
		}
	}
	if err = loadBlackboardPartition(ctx, tx, &r, p); err != nil {
		return err
	}
	baseline := captureBlackboard(r)
	beforeCore := blackboardHeaderJSON(r)
	if err = fn(&r); err != nil {
		return err
	}
	if err = baseline.validate(r); err != nil {
		return err
	}
	afterCore := blackboardHeaderJSON(r)
	if !person && (blackboardFingerprint(json.RawMessage(beforeCore)) != blackboardFingerprint(json.RawMessage(afterCore)) || !slices.Equal(baseline.milestones, blackboardFingerprints(r.Milestones))) {
		return blackboard.ErrImmutable
	}
	delta := baseline.delta(r)
	if p.Kind == blackboard.PartitionProposal && p.ActorKind == "agent" {
		for _, proposal := range delta.Proposals {
			var bytes int
			if err = tx.QueryRow(ctx, `SELECT octet_length(($1::jsonb)::text)`, asJSON(proposal)).Scan(&bytes); err != nil {
				return err
			}
			if r.Usage.Pending+1 > blackboard.MaxPendingAgentProposals || r.Usage.ProposalBytes+bytes > blackboard.MaxPendingAgentProposalBytes || r.Usage.WorkspacePending+1 > blackboard.MaxWorkspacePendingProposals || r.Usage.WorkspaceProposalBytes+bytes > blackboard.MaxWorkspaceProposalBytes {
				return blackboard.ErrInvalid
			}
			r.Usage.Pending++
			r.Usage.ProposalBytes += bytes
			r.Usage.WorkspacePending++
			r.Usage.WorkspaceProposalBytes += bytes
		}
	}
	if (p.Kind == blackboard.PartitionEvent && p.ActorKind == "agent") || p.Kind == blackboard.PartitionObservation {
		var count, bytes int
		// Admission charges only new rows, never the reconstructed header/history.
		for _, event := range delta.Events {
			var size int
			if err = tx.QueryRow(ctx, `SELECT octet_length(($1::jsonb)::text)`, asJSON(event)).Scan(&size); err != nil {
				return err
			}
			count++
			bytes += size
		}
		if count > 0 {
			if p.Kind == blackboard.PartitionEvent && (r.Usage.WorkspaceEvents+count > blackboard.MaxWorkspaceEvents || r.Usage.WorkspaceEventBytes+bytes > blackboard.MaxWorkspaceEventBytes) {
				return blackboard.ErrInvalid
			}
			var allowed bool
			e := tx.QueryRow(ctx, `SELECT event_count+$4<=$6 AND event_bytes+$5<=$7 FROM blackboard_partition_budgets WHERE blackboard_id=$1 AND scope_id=$2 AND kind=$3 AND actor_id=$8`, id, p.MilestoneID, p.Kind, count, bytes, blackboard.MaxAutomaticEvents, blackboard.MaxPartitionEventBytes, p.ActorID).Scan(&allowed)
			if e != nil {
				return e
			}
			if !allowed {
				return blackboard.ErrInvalid
			}
		}
	}
	if err = writeBlackboard(ctx, tx, delta); err != nil {
		return err
	}
	if person && string(beforeCore) != string(afterCore) {
		if err = syncBlackboardLinks(ctx, tx, r); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE blackboards SET record=$2,style=$3,version=$4 WHERE id=$1`, id, afterCore, jsonOrEmpty(r.Style), r.Version); err != nil {
			return err
		}
	}
	if owned {
		return tx.Commit(ctx)
	}
	return nil
}

func lockBlackboardWriter(ctx context.Context, tx pgx.Tx, id string, person bool) error {
	key := "blackboard-write:" + id
	if person {
		_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key)
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return blackboard.ErrConflict
	}
	return nil
}
func blackboardLockError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
		return blackboard.ErrConflict
	}
	return err
}

// Omit partition keys without changing the public Record JSON shape.
func blackboardHeaderJSON(r blackboard.Record) []byte {
	type header struct {
		blackboard.Record
		Events    json.RawMessage `json:"events,omitempty"`
		Proposals json.RawMessage `json:"proposals,omitempty"`
		Revisions json.RawMessage `json:"revisions,omitempty"`
		Snapshots json.RawMessage `json:"snapshots,omitempty"`
	}
	return asJSON(header{Record: blackboard.Header(r)})
}

func loadBlackboardCuts(ctx context.Context, q blackboardReader, r *blackboard.Record, mid *string) error {
	query := `SELECT id,data->'frozen' FROM milestones WHERE blackboard_id=$1`
	args := []any{r.ID}
	if mid != nil {
		query += ` AND id=$2`
		args = append(args, *mid)
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var frozen []byte
		if err = rows.Scan(&id, &frozen); err != nil {
			return err
		}
		if string(frozen) == "null" {
			frozen = nil
		}
		for i := range r.Milestones {
			if r.Milestones[i].ID == id {
				r.Milestones[i].Frozen = frozen
				break
			}
		}
	}
	return rows.Err()
}

// LockBlackboardDecision orders ask decisions with all plan writers.
func LockBlackboardDecision(ctx context.Context, tx pgx.Tx, id string) (blackboard.Record, error) {
	if err := lockBlackboardWriter(ctx, tx, id, true); err != nil {
		return blackboard.Record{}, err
	}
	return loadBlackboardCore(ctx, tx, id, "FOR NO KEY UPDATE")
}
