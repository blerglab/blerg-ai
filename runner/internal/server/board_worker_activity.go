package server

import (
	"context"
	"slices"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func (a *API) boardCardLiveWorker(ctx context.Context, board string, number int, account, main string, now time.Time) (bool, error) {
	var live bool
	err := a.dbPool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM session_card_links l JOIN sessions s ON s.id=l.session_id
 WHERE l.board_id=$1 AND l.card_number=$2 AND s.spawning_account_id=$3 AND s.id<>$4
 AND s.ended_at IS NULL AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
 AND (s.status IN ('running','starting','waiting') OR (s.status='idle' AND (
 (SELECT e.ts FROM agent_events e WHERE e.session_id=s.id AND e.kind='turn_done' ORDER BY e.seq DESC LIMIT 1)>=$5
 OR (s.parent_session_id=$4 AND EXISTS(SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open' AND NOT m.person_required AND NOT m.child_escalated))
 ))))`, board, number, account, main, now.Add(-boardCodeStaleAfter)).Scan(&live)
	if err != nil || live {
		return live, err
	}
	// Full-key titles have no numeric SQL title link. Match them against the
	// current board key, retaining the same ownership, context and liveness rules.
	row, err := db.GetSession(ctx, a.dbPool, main)
	if err != nil || row == nil {
		return false, err
	}
	key := a.planBoardKey(ctx, row, board)
	rows, err := a.dbPool.Query(ctx, `SELECT coalesce(s.title,'') FROM sessions s
 WHERE s.spawning_account_id=$1 AND s.id<>$2 AND card_link_context(s.id)=$3
 AND s.ended_at IS NULL AND s.archived_at IS NULL AND s.discard_requested_at IS NULL
 AND (s.status IN ('running','starting','waiting') OR (s.status='idle' AND (
 (SELECT e.ts FROM agent_events e WHERE e.session_id=s.id AND e.kind='turn_done' ORDER BY e.seq DESC LIMIT 1)>=$4
 OR (s.parent_session_id=$2 AND EXISTS(SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open' AND NOT m.person_required AND NOT m.child_escalated))
 )))`, account, main, board, now.Add(-boardCodeStaleAfter))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return false, err
		}
		if slices.Contains(cardRefs(title, key), number) {
			return true, nil
		}
	}
	return false, rows.Err()
}
