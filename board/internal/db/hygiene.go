package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// retentionEligible is shared by the preview and sweep. Names never confer
// terminal status, and comments do not reset a column's entry clock.
func retentionEligible(days, tags string) string {
	return `c.archived_at IS NULL AND c.stuck_at IS NULL AND col.is_terminal
 AND coalesce(b.driven_by,'')='' AND c.column_entered_at < now()-make_interval(days=>` + days + `)
 AND NOT EXISTS (SELECT 1 FROM card_tags t WHERE t.card_id=c.id AND t.tag=ANY(` + tags + `))
 AND NOT EXISTS (SELECT 1 FROM runner_sessions rs WHERE rs.card_id=c.id AND rs.lifecycle NOT IN ('stopped','error'))`
}

func PreviewAutoArchive(ctx context.Context, pool *pgxpool.Pool, id string, days int, tags []string) (int, error) {
	if err := checkArchiveDays(&days); err != nil {
		return 0, err
	}
	if err := checkKeepTags(&tags); err != nil {
		return 0, err
	}
	b, err := GetBoard(ctx, pool, id)
	if err != nil {
		return 0, err
	}
	if b.DrivenBy != nil && *b.DrivenBy != "" {
		return 0, mirrorRetentionError()
	}
	if days == 0 {
		return 0, nil
	}
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM cards c JOIN boards b ON b.id=c.board_id JOIN board_columns col ON col.id=c.column_id
 WHERE c.board_id=$1 AND `+retentionEligible("$2", "$3::text[]"), id, days, tags).Scan(&n)
	return n, err
}

// AutoArchiveDone gives each eligible board its own oldest-first batch, so a
// busy board cannot consume another board's retention allowance.
func AutoArchiveDone(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM boards WHERE auto_archive_done_days>0 AND coalesce(driven_by,'')='' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for _, id := range ids {
		n, err := autoArchiveBoard(ctx, pool, id)
		if err != nil {
			return changed, err
		}
		if n > 0 {
			changed = append(changed, id)
		}
	}
	return changed, nil
}

func autoArchiveBoard(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	rows, err := tx.Query(ctx, `SELECT c.id FROM cards c JOIN boards b ON b.id=c.board_id JOIN board_columns col ON col.id=c.column_id
 WHERE c.board_id=$1 AND b.auto_archive_done_days>0 AND `+retentionEligible("b.auto_archive_done_days", "b.auto_archive_keep_tags")+`
 ORDER BY c.column_entered_at,c.id LIMIT 100 FOR UPDATE OF c,b,col SKIP LOCKED`, id)
	if err != nil {
		return 0, err
	}
	var found []string
	for rows.Next() {
		var card string
		if err = rows.Scan(&card); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, card)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, card := range found {
		if _, err = archiveCardTx(ctx, tx, card, nil, EventMeta{Actor: "service"}); err != nil {
			return 0, err
		}
	}
	return len(found), tx.Commit(ctx)
}
