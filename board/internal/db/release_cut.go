package db

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type cutQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func releaseCutPreview(ctx context.Context, q cutQuery, board, id string) (releasecut.Preview, error) {
	out := releasecut.Preview{Snapshot: []releasecut.SnapshotCard{}}
	rows, err := q.Query(ctx, `SELECT jsonb_build_object('id',c.id,'board_id',c.board_id,'number',c.number,'title',c.title,'version',c.version,'release_id',c.release_id,'column_id',c.column_id,'tags',(SELECT coalesce(jsonb_agg(ct.tag ORDER BY ct.tag),'[]'::jsonb) FROM card_tags ct WHERE ct.card_id=c.id),'type',c.type,'priority',c.priority,'size',c.size,'risk',c.risk,'complexity',c.complexity,'rank',c.rank,'archived_at',c.archived_at),coalesce(col.is_terminal,false),coalesce(col.name,'No column') FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id WHERE c.board_id=$1 AND c.release_id=$2 AND c.archived_at IS NULL ORDER BY c.id`, board, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	versions := []releasecut.CardVersion{}
	for rows.Next() {
		var v releasecut.SnapshotCard
		if err = rows.Scan(&v.Card, &v.Terminal, &v.ColumnName); err != nil {
			return out, err
		}
		var cv releasecut.CardVersion
		if err = json.Unmarshal(v.Card, &cv); err != nil {
			return out, err
		}
		cv.Terminal = v.Terminal
		cv.ColumnName = v.ColumnName
		versions = append(versions, cv)
		out.Snapshot = append(out.Snapshot, v)
	}
	out.Digest = releasecut.Digest(versions)
	return out, rows.Err()
}
func readCutReceipt(ctx context.Context, q cutQuery, board, id string) (*releasecut.Receipt, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT receipt FROM release_cut_receipts WHERE board_id=$1 AND release_id=$2`, board, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out releasecut.Receipt
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Release status and cut evidence are read from one consistent database snapshot.
func readReleaseCutEvidence(ctx context.Context, pool *pgxpool.Pool, v *ReleaseView) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	v.Release, err = scanRelease(tx.QueryRow(ctx, `SELECT `+releaseCols+` FROM board_releases WHERE board_id=$1 AND id=$2`, v.Release.BoardID, v.Release.ID))
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT name FROM boards WHERE id=$1`, v.Release.BoardID).Scan(&v.BoardName); err != nil {
		return err
	}
	v.CutReceipt, err = readCutReceipt(ctx, tx, v.Release.BoardID, v.Release.ID)
	if err != nil {
		return err
	}
	if v.Release.Status != "released" {
		preview, e := releaseCutPreview(ctx, tx, v.Release.BoardID, v.Release.ID)
		if e != nil {
			return e
		}
		v.CutPreview = &preview
	}
	return tx.Commit(ctx)
}

// ReadReleaseEvidence is the compact, consistent read used by cut authorization
// and reconciliation; descriptions and mutable burn calculations are not evidence.
func ReadReleaseEvidence(ctx context.Context, pool *pgxpool.Pool, board, id string) (ReleaseView, error) {
	view := ReleaseView{Release: Release{ID: id, BoardID: board}}
	err := readReleaseCutEvidence(ctx, pool, &view)
	return view, err
}
