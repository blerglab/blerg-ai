package db

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateApprovedMetadata consumes the approval and applies the patch in one transaction.
// Replays are refused, including after subsequent human changes.
func UpdateApprovedMetadata(ctx context.Context, pool *pgxpool.Pool, d boardfiling.MetadataPatch, account, intent, digest string) error {
	if err := boardfiling.ValidateMetadataPatch(d); err != nil {
		return err
	}

	if d.FieldSchema != nil {
		if _, err := ParseFieldSchema(*d.FieldSchema); err != nil {
			return err
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit or original error is returned
	tag, err := tx.Exec(ctx, `INSERT INTO board_metadata_intents(intent,account_id,board_id,digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, intent, account, d.BoardID, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrBoardIntentUsed
	}
	if err = lockBoard(ctx, tx, d.BoardID); err != nil {
		return err
	}
	if d.Key != nil {
		if err = claimBoardKey(ctx, tx, d.BoardID, *d.Key); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE boards SET name=COALESCE($2,name),description=COALESCE($3,description),updated_at=now() WHERE id=$1`, d.BoardID, d.Name, d.Description)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `UPDATE boards SET require_repo=COALESCE($2,require_repo),field_schema=COALESCE($3,field_schema),deploy_url=COALESCE($4,deploy_url),include_component_version=COALESCE($5,include_component_version) WHERE id=$1`, d.BoardID, d.RequireRepo, d.FieldSchema, d.DeployURL, d.IncludeComponentVersion); err != nil {
		return err
	}
	if d.Repos != nil {
		if _, err = tx.Exec(ctx, `DELETE FROM board_repos WHERE board_id=$1`, d.BoardID); err != nil {
			return err
		}
		for i, repo := range *d.Repos {
			if _, err = tx.Exec(ctx, `INSERT INTO board_repos(board_id,repo,position) VALUES($1,$2,$3)`, d.BoardID, repo, i); err != nil {
				return err
			}
		}
	}
	for _, c := range d.Columns {
		if err = approvedColumn(ctx, tx, d.BoardID, c, account); err != nil {
			return err
		}
	}
	for _, t := range d.Tags {
		if err = approvedTag(ctx, tx, d.BoardID, t, account, intent); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func approvedColumn(ctx context.Context, tx pgx.Tx, boardID string, c boardfiling.ColumnPatch, account string) error {
	audit := []ColumnAudit{{Who: ColumnActor{Actor: "human", Account: account}}}
	if c.BeforeID != nil && *c.BeforeID != "" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM board_columns WHERE id=$1 AND board_id=$2`, *c.BeforeID, boardID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	var after Column
	if c.ID == "" {
		var last *string
		if err := tx.QueryRow(ctx, `SELECT max(rank) FROM board_columns WHERE board_id=$1`, boardID).Scan(&last); err != nil {
			return err
		}
		rank, err := RankBetween(deref(last), "")
		if err != nil {
			return err
		}
		terminal := c.IsTerminal != nil && *c.IsTerminal
		if err = tx.QueryRow(ctx, `INSERT INTO board_columns(board_id,rank,name,is_terminal) VALUES($1,$2,$3,$4) RETURNING id,name,is_terminal`, boardID, rank, *c.Name, terminal).Scan(&after.ID, &after.Name, &after.IsTerminal); err != nil {
			return err
		}
		c.ID = after.ID
		if err = recordColumnAudit(ctx, tx, boardID, c.ID, "created", audit, map[string]any{"name": after.Name, "is_terminal": after.IsTerminal}); err != nil {
			return err
		}
	} else {
		var before Column
		if err := tx.QueryRow(ctx, `SELECT id,name,is_terminal FROM board_columns WHERE id=$1 AND board_id=$2 FOR UPDATE`, c.ID, boardID).Scan(&before.ID, &before.Name, &before.IsTerminal); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if c.Delete {
			var populated bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cards WHERE column_id=$1)`, c.ID).Scan(&populated); err != nil {
				return err
			}
			if populated {
				return ErrColumnHasCards
			}
			if _, err := tx.Exec(ctx, `DELETE FROM board_columns WHERE id=$1`, c.ID); err != nil {
				return err
			}
			return recordColumnAudit(ctx, tx, boardID, c.ID, "deleted", audit, map[string]any{"name": before.Name})
		}
		after = before
		if c.Name != nil || c.IsTerminal != nil {
			if err := tx.QueryRow(ctx, `UPDATE board_columns SET name=COALESCE($2,name),is_terminal=COALESCE($3,is_terminal) WHERE id=$1 RETURNING name,is_terminal`, c.ID, c.Name, c.IsTerminal).Scan(&after.Name, &after.IsTerminal); err != nil {
				return err
			}
			if err := recordColumnAudit(ctx, tx, boardID, c.ID, "updated", audit, map[string]any{
				"from": map[string]any{"name": before.Name, "is_terminal": before.IsTerminal},
				"to":   map[string]any{"name": after.Name, "is_terminal": after.IsTerminal},
			}); err != nil {
				return err
			}
		}
	}
	if c.BeforeID != nil {
		if _, err := moveColumnTx(ctx, tx, c.ID, c.BeforeID); err != nil {
			return err
		}
		return recordColumnAudit(ctx, tx, boardID, c.ID, "moved", audit, map[string]any{"name": after.Name, "before_id": *c.BeforeID})
	}
	return nil
}

func approvedTag(ctx context.Context, tx pgx.Tx, boardID string, t boardfiling.TagPatch, account, intent string) error {
	// Lock affected cards before changing their tags or versions.
	rows, err := tx.Query(ctx, `SELECT c.id FROM cards c JOIN card_tags t ON t.card_id=c.id WHERE c.board_id=$1 AND t.tag=$2 ORDER BY c.id FOR UPDATE OF c`, boardID, t.From)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		deleted, deleteErr := tx.Exec(ctx, `DELETE FROM card_tags WHERE card_id=$1 AND tag=$2`, id, t.From)
		if deleteErr != nil {
			return deleteErr
		}
		if deleted.RowsAffected() == 0 {
			continue
		}
		if t.To != nil {
			if _, err = tx.Exec(ctx, `INSERT INTO card_tags(card_id,tag) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, *t.To); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE cards SET version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return err
		}
		if err = appendEvent(ctx, tx, id, "updated", EventMeta{Actor: "human"}, nil, nil, map[string]any{"fields": []string{"tags"}, "tag_patch": t, "approved_by": account, "approval_intent": intent}); err != nil {
			return err
		}
	}
	return nil
}
