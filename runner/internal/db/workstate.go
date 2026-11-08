package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
	"github.com/jackc/pgx/v5"
)

func appendWorkState(ctx context.Context, tx pgx.Tx, sid, eid, kind, payload string, ts time.Time) (int64, bool, error) {
	if kind == "ticket" {
		return appendPinState(ctx, tx, sid, eid, kind, payload, ts)
	}
	if kind != "checklist" {
		return appendAgentEvent(ctx, tx, sid, eid, kind, payload, ts)
	}
	// All checklist writers share this lock, including automatic plan synchronization.
	if err := LockSessionWork(ctx, tx, "checklist", sid); err != nil {
		return 0, false, err
	}
	full, ok := checklist.ReadWithSeed([]byte(payload), eid)
	if !ok {
		return appendAgentEvent(ctx, tx, sid, eid, kind, payload, ts)
	}
	prev, err := LatestChecklist(ctx, tx, sid, 1<<62)
	if err != nil {
		return 0, false, err
	}
	var before []checklist.Item
	if prev != nil {
		p, _ := checklist.ReadWithSeed([]byte(prev.Payload), prev.ClientEventID)
		before = p.Items
	}
	delta := checklist.Compact(before, full)
	raw, err := json.Marshal(delta)
	if err != nil {
		return 0, false, err
	}
	seq, inserted, err := appendAgentEvent(ctx, tx, sid, eid, kind, string(raw), ts)
	if err != nil || !inserted {
		return seq, inserted, err
	}
	state, _ := json.Marshal(full)
	var revision int64
	err = tx.QueryRow(ctx, `INSERT INTO session_work_state(session_id,kind,seq,revision,payload) VALUES($1,'checklist',$2,1,$3) ON CONFLICT(session_id,kind) DO UPDATE SET seq=excluded.seq,revision=session_work_state.revision+1,payload=excluded.payload RETURNING revision`, sid, seq, string(state)).Scan(&revision)
	if err == nil && (revision%100 == 1 || full.Op == "set" || full.Op == "clear") {
		_, err = tx.Exec(ctx, `INSERT INTO checklist_checkpoints(session_id,seq,payload) VALUES($1,$2,$3)`, sid, seq, string(state))
	}
	return seq, true, err
}

// LatestChecklist uses the current row, or a checkpoint plus changes for historical reads.
func LatestChecklist(ctx context.Context, q Querier, sid string, through int64) (*AgentEventRow, error) {
	latest, err := latestChecklistEvent(ctx, q, sid, through)
	if err != nil || latest == nil {
		return latest, err
	}
	if p, ok := checklist.ReadWithSeed([]byte(latest.Payload), latest.ClientEventID); ok && !p.Delta {
		return latest, nil
	}
	var base int64
	var raw string
	err = q.QueryRow(ctx, `SELECT seq,payload::text FROM session_work_state WHERE session_id=$1 AND kind='checklist' AND seq=$2`, sid, latest.Seq).Scan(&base, &raw)
	if err == nil {
		latest.Payload = raw
		return latest, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	err = q.QueryRow(ctx, `SELECT seq,payload::text FROM checklist_checkpoints WHERE session_id=$1 AND seq<=$2 ORDER BY seq DESC LIMIT 1`, sid, through).Scan(&base, &raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var state checklist.Payload
	if raw != "" {
		state, _ = checklist.Read([]byte(raw))
	}
	rows, err := q.Query(ctx, `SELECT client_event_id,payload::text FROM agent_events WHERE session_id=$1 AND kind='checklist' AND seq>$2 AND seq<=$3 ORDER BY seq`, sid, base, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var eid, payload string
		if err = rows.Scan(&eid, &payload); err != nil {
			return nil, err
		}
		p, ok := checklist.ReadWithSeed([]byte(payload), eid)
		if ok {
			state = checklist.Replay(state.Items, p)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(state)
	latest.Payload = string(encoded)
	return latest, err
}

// CountRecentWork applies a database-clock rate window rather than a lifetime cap.
func CountRecentWork(ctx context.Context, q Querier, sid, kind string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind IN ('checklist','topic','ticket') AND kind=$2 AND ts>now()-interval '1 minute'`, sid, kind).Scan(&n)
	return n, err
}

func appendPinState(ctx context.Context, tx pgx.Tx, sid, eid, kind, payload string, ts time.Time) (int64, bool, error) {
	if err := LockSessionWork(ctx, tx, "tickets", sid); err != nil {
		return 0, false, err
	}
	rows, err := ListTicketRows(ctx, tx, sid, 1<<62)
	if err != nil {
		return 0, false, err
	}
	seq, inserted, err := appendAgentEvent(ctx, tx, sid, eid, kind, payload, ts)
	if err != nil || !inserted {
		return seq, inserted, err
	}
	var p ticket.Payload
	err = json.Unmarshal([]byte(payload), &p)
	rows = append(rows, ticket.Row{Seq: seq, EventID: eid, Ts: ts, Action: p.Action, Source: p.Ticket.Source, Key: p.Ticket.Key, BoardID: p.Ticket.BoardID, Title: p.Ticket.Title, Status: p.Ticket.Status, URL: p.Ticket.URL})
	if err != nil {
		return 0, false, err
	}
	pins := ticket.Pinned(rows)
	active := []ticket.Row{}
	for _, p := range pins {
		active = append(active, ticket.Row{Seq: p.Seq, EventID: p.EventID, Ts: p.UpdatedAt, Action: "pin", Source: p.Source, Key: p.Key, BoardID: p.BoardID, Title: p.Title, Status: p.Status, URL: p.URL})
	}
	raw, _ := json.Marshal(active)
	_, err = tx.Exec(ctx, `INSERT INTO session_work_state(session_id,kind,seq,revision,payload) VALUES($1,'ticket',$2,1,$3) ON CONFLICT(session_id,kind) DO UPDATE SET seq=excluded.seq,revision=session_work_state.revision+1,payload=excluded.payload`, sid, seq, string(raw))
	return seq, true, err
}

// A replay page can begin between checkpoints. Hydrate its first checklist once;
// subsequent events stay compact, so pagination never depends on an earlier page.
func seedChecklistPage(ctx context.Context, q Querier, events []AgentEventRow) error {
	for i, e := range events {
		if e.Kind != "checklist" {
			continue
		}
		p, ok := checklist.Read([]byte(e.Payload))
		if ok && p.Delta {
			full, err := LatestChecklist(ctx, q, e.SessionID, e.Seq)
			if err != nil {
				return err
			}
			if full != nil {
				events[i].Payload = full.Payload
			}
		}
		break
	}
	return nil
}
