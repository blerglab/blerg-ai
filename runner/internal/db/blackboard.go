package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BlackboardStore keeps the person's plan header separate from authoritative
// proposal, event, observation, revision and snapshot rows.
type BlackboardStore struct {
	Pool *pgxpool.Pool
	// Tx joins a decision to an owning ask-answer transaction. The caller owns its lifetime.
	Tx pgx.Tx
}

var _ blackboard.Store = BlackboardStore{}

func (s BlackboardStore) Create(ctx context.Context, r blackboard.Record) error {
	if err := blackboard.ValidateCreate(r); err != nil {
		return err
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
	style := r.Style
	if len(style) == 0 {
		style = json.RawMessage(`{}`)
	}
	_, err = tx.Exec(ctx, `INSERT INTO blackboards(id,owner_kind,owner_id,parent_id,style,version,record) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.ID, r.Owner.Kind, r.Owner.ID, r.ParentID, style, r.Version, blackboardHeaderJSON(r))
	if err != nil {
		return err
	}
	if err = writeBlackboard(ctx, tx, r); err != nil {
		return err
	}
	if owned {
		return tx.Commit(ctx)
	}
	return nil
}
func (s BlackboardStore) Load(ctx context.Context, id string) (blackboard.Record, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return blackboard.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	r, err := loadBlackboardCore(ctx, tx, id, "")
	if err == nil {
		err = loadBlackboardHistory(ctx, tx, &r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return r, err
}
func (s BlackboardStore) Update(ctx context.Context, id string, fn func(*blackboard.Record) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBlackboardWriter(ctx, tx, id, true); err != nil {
		return err
	}
	var data []byte
	err = tx.QueryRow(ctx, `SELECT record FROM blackboards WHERE id=$1 FOR UPDATE`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return blackboard.ErrNotFound
	}
	if err != nil {
		return err
	}
	var next blackboard.Record
	if err = json.Unmarshal(data, &next); err != nil {
		return err
	}
	if err = loadBlackboardHistory(ctx, tx, &next); err != nil {
		return err
	}
	baseline := captureBlackboard(next)
	if err = fn(&next); err != nil {
		return err
	}
	if err = baseline.validate(next); err != nil {
		return err
	}
	if err = writeBlackboard(ctx, tx, baseline.delta(next)); err != nil {
		return err
	}
	if err = syncBlackboardLinks(ctx, tx, next); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE blackboards SET record=$2,style=$3,version=$4 WHERE id=$1`, id, blackboardHeaderJSON(next), jsonOrEmpty(next.Style), next.Version)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func asJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func jsonOrEmpty(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage(`{}`)
	}
	return v
}
func writeBlackboard(ctx context.Context, tx pgx.Tx, r blackboard.Record) error {
	exec := func(q string, args ...any) error {
		tag, err := tx.Exec(ctx, q, args...)
		if err == nil && strings.Contains(q, "DO UPDATE") && tag.RowsAffected() == 0 {
			return blackboard.ErrConflict
		}
		return err
	}
	for _, v := range r.Links {
		if err := exec(`INSERT INTO blackboard_links VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.ID, v.WorkspaceID, v.BoardID); err != nil {
			return err
		}
	}
	for _, v := range r.Goals {
		if err := exec(`INSERT INTO goals VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET version=EXCLUDED.version,data=EXCLUDED.data WHERE goals.blackboard_id=EXCLUDED.blackboard_id`, v.ID, r.ID, asJSON(v.Owner), v.Version, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Milestones {
		if err := exec(`INSERT INTO milestones VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET board_id=EXCLUDED.board_id,release_id=EXCLUDED.release_id,target=EXCLUDED.target,stage=EXCLUDED.stage,frozen_at=EXCLUDED.frozen_at,version=EXCLUDED.version,data=EXCLUDED.data || CASE WHEN NOT(EXCLUDED.data ? 'frozen') AND milestones.data ? 'frozen' THEN jsonb_build_object('frozen',milestones.data->'frozen') ELSE '{}'::jsonb END WHERE milestones.blackboard_id=EXCLUDED.blackboard_id`, v.ID, r.ID, asJSON(v.Owner), nullableBlackboardRef(v.BoardID), nullableBlackboardRef(v.ReleaseID), v.Target, v.Stage, v.FrozenAt, v.Version, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Thrusts {
		if err := exec(`INSERT INTO thrusts VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET version=EXCLUDED.version,data=EXCLUDED.data WHERE thrusts.blackboard_id=EXCLUDED.blackboard_id`, v.ID, r.ID, nullableBlackboardRef(v.MilestoneID), v.GoalID, asJSON(v.Owner), v.Version, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Items {
		if err := exec(`INSERT INTO plan_items VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET thrust_id=EXCLUDED.thrust_id,reason=EXCLUDED.reason,state=EXCLUDED.state,data=EXCLUDED.data WHERE plan_items.blackboard_id=EXCLUDED.blackboard_id`, v.ID, r.ID, nullableBlackboardRef(v.MilestoneID), nullableBlackboardRef(v.BoardID), nullableBlackboardNumber(v.CardNumber), v.ThrustID, v.Reason, asJSON(v.RaisedBy), v.State, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Revisions {
		if err := exec(`INSERT INTO plan_revisions(blackboard_id,milestone_id,scope_id,revision,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.ID, nullableBlackboardRef(v.MilestoneID), v.MilestoneID, v.Number, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Events {
		tag, err := tx.Exec(ctx, `INSERT INTO plan_events(id,blackboard_id,milestone_id,authenticated_actor,recorded_at,claimed_at,source,kind,payload,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, v.ID, r.ID, v.MilestoneID, asJSON(v.AuthenticatedActor), v.RecordedAt, v.ClaimedAt, v.Source, v.Kind, jsonOrEmpty(v.Payload), asJSON(v))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if v.Source == "agent" || v.Source == "board" {
			kind := blackboard.PartitionEvent
			actorID := v.AuthenticatedActor.ID
			if v.Source == "board" {
				kind = blackboard.PartitionObservation
				actorID = ""
			}
			if err := exec(`INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,event_count,event_bytes,actor_id) VALUES($1,$2,$3,1,octet_length(($4::jsonb)::text),$5) ON CONFLICT(blackboard_id,scope_id,kind,actor_id) DO UPDATE SET event_count=blackboard_partition_budgets.event_count+1,event_bytes=blackboard_partition_budgets.event_bytes+EXCLUDED.event_bytes`, r.ID, v.MilestoneID, kind, asJSON(v), actorID); err != nil {
				return err
			}
		}
		if v.Source == "agent" {
			if err := exec(`INSERT INTO blackboard_workspace_budgets(workspace_id,scope_id,event_count,event_bytes) VALUES($1,$2,1,octet_length(($3::jsonb)::text)) ON CONFLICT(workspace_id,scope_id) DO UPDATE SET event_count=blackboard_workspace_budgets.event_count+1,event_bytes=blackboard_workspace_budgets.event_bytes+EXCLUDED.event_bytes`, blackboard.WorkspaceKey(r), v.MilestoneID, asJSON(v)); err != nil {
				return err
			}
		}
		if v.Source == "board" {
			var item blackboard.Item
			if json.Unmarshal(v.Payload, &item) == nil && item.BoardID != "" && item.CardNumber > 0 {
				if err := exec(`INSERT INTO blackboard_observations VALUES($1,$2,$3,$4,$5) ON CONFLICT(blackboard_id,scope_id,board_id,card_number) DO UPDATE SET data=EXCLUDED.data`, r.ID, v.MilestoneID, item.BoardID, item.CardNumber, asJSON(v)); err != nil {
					return err
				}
			}
		}
	}
	for _, v := range r.Proposals {
		if err := exec(`INSERT INTO plan_proposals(id,blackboard_id,milestone_id,revision,status,data) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,data=EXCLUDED.data WHERE plan_proposals.blackboard_id=EXCLUDED.blackboard_id`, v.ID, r.ID, nullableBlackboardRef(v.MilestoneID), v.Revision, v.Status, asJSON(v)); err != nil {
			return err
		}
	}
	for _, v := range r.Snapshots {
		if err := exec(`INSERT INTO milestone_snapshots VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(milestone_id,day) DO UPDATE SET observed_at=EXCLUDED.observed_at,complete=EXCLUDED.complete,data=EXCLUDED.data WHERE NOT milestone_snapshots.complete AND EXCLUDED.complete`, r.ID, v.MilestoneID, v.Day, v.ObservedAt, v.Complete, asJSON(v)); err != nil {
			return err
		}
	}
	return nil
}

// Capture fingerprints rather than cloning the entire record and its audit
// payloads. The transaction owns its decoded record; immutable history is checked
// against these pre-callback fingerprints, and only changed SQL rows are written.
type blackboardBaseline struct {
	immutable               blackboard.Record
	events, revisions       int
	eventHash, revisionHash [32]byte
	links                   [][32]byte
	goals                   [][32]byte
	milestones              [][32]byte
	thrusts                 [][32]byte
	items                   [][32]byte
	proposals               [][32]byte
	snapshots               [][32]byte
}

func captureBlackboard(r blackboard.Record) blackboardBaseline {
	b := blackboardBaseline{
		immutable: blackboard.Record{ID: r.ID, Owner: r.Owner},
		events:    len(r.Events), revisions: len(r.Revisions),
		eventHash: blackboardFingerprint(r.Events), revisionHash: blackboardFingerprint(r.Revisions),
		links: blackboardFingerprints(r.Links), goals: blackboardFingerprints(r.Goals),
		milestones: blackboardFingerprints(r.Milestones), thrusts: blackboardFingerprints(r.Thrusts),
		items: blackboardFingerprints(r.Items), proposals: blackboardFingerprints(r.Proposals),
		snapshots: blackboardFingerprints(r.Snapshots),
	}
	if r.ParentID != nil {
		parent := *r.ParentID
		b.immutable.ParentID = &parent
	}
	// Incomplete snapshots are the one repairable history type. Keep their small
	// value metadata and counts isolated for the shared repair validator.
	b.immutable.Snapshots = make([]blackboard.Snapshot, len(r.Snapshots))
	for i, s := range r.Snapshots {
		s.Counts = maps.Clone(s.Counts)
		b.immutable.Snapshots[i] = s
	}
	return b
}
func (b blackboardBaseline) validate(next blackboard.Record) error {
	if err := blackboard.ValidateUpdate(b.immutable, next); err != nil {
		return err
	}
	if len(next.Events) < b.events || len(next.Revisions) < b.revisions {
		return blackboard.ErrImmutable
	}
	if b.events > 0 && blackboardFingerprint(next.Events[:b.events]) != b.eventHash {
		return blackboard.ErrImmutable
	}
	if b.revisions > 0 && blackboardFingerprint(next.Revisions[:b.revisions]) != b.revisionHash {
		return blackboard.ErrImmutable
	}
	return nil
}
func (b blackboardBaseline) delta(next blackboard.Record) blackboard.Record {
	r := next
	r.Links = changedBlackboardRows(b.links, next.Links)
	r.Goals = changedBlackboardRows(b.goals, next.Goals)
	r.Milestones = changedBlackboardRows(b.milestones, next.Milestones)
	r.Thrusts = changedBlackboardRows(b.thrusts, next.Thrusts)
	r.Items = changedBlackboardRows(b.items, next.Items)
	r.Proposals = changedBlackboardRows(b.proposals, next.Proposals)
	r.Snapshots = changedBlackboardRows(b.snapshots, next.Snapshots)
	r.Events = next.Events[b.events:]
	r.Revisions = next.Revisions[b.revisions:]
	return r
}
func blackboardFingerprint(v any) [32]byte {
	// Match the shared validator's JSON value equality, including jsonb payload
	// key ordering and insignificant whitespace.
	var canonical any
	_ = json.Unmarshal(asJSON(v), &canonical)
	return sha256.Sum256(asJSON(canonical))
}
func blackboardFingerprints[T any](rows []T) [][32]byte {
	out := make([][32]byte, len(rows))
	for i, v := range rows {
		out[i] = blackboardFingerprint(v)
	}
	return out
}
func changedBlackboardRows[T any](old [][32]byte, next []T) []T {
	out := []T{}
	for i, v := range next {
		if i >= len(old) || old[i] != blackboardFingerprint(v) {
			out = append(out, v)
		}
	}
	return out
}
func nullableBlackboardRef(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullableBlackboardNumber(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// Keep the lookup index aligned with the authoritative header, including unlink.
func syncBlackboardLinks(ctx context.Context, tx pgx.Tx, r blackboard.Record) error {
	raw := asJSON(r.Links)
	if len(r.Links) == 0 {
		raw = []byte("[]")
	}
	_, err := tx.Exec(ctx, `DELETE FROM blackboard_links l WHERE blackboard_id=$1 AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements($2::jsonb) v WHERE v->>'workspace_id'=l.workspace_id AND v->>'board_id'=l.board_id)`, r.ID, raw)
	return err
}
