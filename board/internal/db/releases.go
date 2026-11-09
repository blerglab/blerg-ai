package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasecut"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Release struct {
	ReleasedAt *string `json:"released_at"`
	ReleasedBy *string `json:"released_by"`
	ID         string  `json:"id"`
	BoardID    string  `json:"board_id"`
	Name       string  `json:"name"`
	TargetDate *string `json:"target_date"`
	Status     string  `json:"status"`
}
type ReleaseParams struct {
	Name       string  `json:"name"`
	TargetDate *string `json:"target_date"`
	Status     string  `json:"status"`
}
type ReleaseView struct {
	BoardName       string              `json:"board_name"`
	CutReceipt      *releasecut.Receipt `json:"cut_receipt"`
	CutPreview      *releasecut.Preview `json:"cut_preview,omitempty"`
	Burn            BurnChart           `json:"burn"`
	RestoredCardIDs []string            `json:"restored_card_ids"`
	DoneCardIDs     []string            `json:"done_card_ids"`
	Release         Release             `json:"release"`
	Cards           []Card              `json:"cards"`
	Done            int                 `json:"done"`
	ByColumn        map[string]int      `json:"by_column"`
}

const releaseCols = `id,board_id,name,to_char(target_date,'YYYY-MM-DD'),status,released_at::text,released_by`

func scanRelease(row pgx.Row) (Release, error) {
	var r Release
	err := row.Scan(&r.ID, &r.BoardID, &r.Name, &r.TargetDate, &r.Status, &r.ReleasedAt, &r.ReleasedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}
func ListReleases(ctx context.Context, pool *pgxpool.Pool, board string) ([]Release, error) {
	rows, err := pool.Query(ctx, `SELECT `+releaseCols+` FROM board_releases WHERE board_id=$1 ORDER BY target_date NULLS LAST,name`, board)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, e := scanRelease(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func SaveRelease(ctx context.Context, pool *pgxpool.Pool, board, id string, p ReleaseParams) (Release, error) {
	if err := releaseUUID(id, true); err != nil {
		return Release{}, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 120 || (p.Status != "planned" && p.Status != "in progress") {
		return Release{}, &FieldError{Key: "release", Msg: "name and planned or in progress status required"}
	}
	if p.TargetDate != nil && *p.TargetDate != "" {
		if _, err := time.Parse("2006-01-02", *p.TargetDate); err != nil {
			return Release{}, &FieldError{Key: "target_date", Msg: "invalid target date"}
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBoard(ctx, tx, board); err != nil {
		return Release{}, err
	}
	var r Release
	if id == "" {
		r, err = scanRelease(tx.QueryRow(ctx, `INSERT INTO board_releases(board_id,name,target_date,status) VALUES($1,$2,$3::date,$4) RETURNING `+releaseCols, board, p.Name, emptyToNil(p.TargetDate), p.Status))
	} else {
		r, err = scanRelease(tx.QueryRow(ctx, `UPDATE board_releases SET name=$3,target_date=$4::date,status=$5 WHERE board_id=$1 AND id=$2 AND status<>'released' RETURNING `+releaseCols, board, id, p.Name, emptyToNil(p.TargetDate), p.Status))
	}
	if err != nil {
		if isUniqueViolation(err, "board_releases_board_id_name_key") {
			return r, &FieldError{Key: "name", Msg: "release name already exists"}
		}
		return r, err
	}
	return r, tx.Commit(ctx)
}
func validateRelease(ctx context.Context, tx pgx.Tx, board string, id *string) error {
	if id == nil || *id == "" {
		return nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM board_releases WHERE board_id=$1 AND id::text=$2 AND status<>'released')`, board, *id).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return &FieldError{Key: "release_id", Msg: "release must be active and on this board"}
	}
	return nil
}
func ReadRelease(ctx context.Context, pool *pgxpool.Pool, board, id string) (ReleaseView, error) {
	if err := releaseUUID(id, false); err != nil {
		return ReleaseView{}, err
	}
	v := ReleaseView{Cards: []Card{}, ByColumn: map[string]int{}}
	var err error
	v.Release, err = scanRelease(pool.QueryRow(ctx, `SELECT `+releaseCols+` FROM board_releases WHERE board_id=$1 AND id=$2`, board, id))
	if err != nil {
		return v, err
	}
	rows, err := pool.Query(ctx, `SELECT `+cardColsC+` FROM cards c WHERE c.board_id=$1 AND ((c.release_id=$2 AND c.archived_at IS NULL) OR EXISTS(SELECT 1 FROM release_done_cards d WHERE d.release_id=$2 AND d.card_id=c.id)) ORDER BY c.number`, board, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		c, e := scanCard(rows)
		if e != nil {
			rows.Close()
			return v, e
		}
		v.Cards = append(v.Cards, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	if err = attachExtras(ctx, pool, v.Cards); err != nil {
		return v, err
	}
	rows, err = pool.Query(ctx, `SELECT coalesce(d.column_name,col.name,'Archived'),count(*),count(*) FILTER(WHERE d.card_id IS NOT NULL OR col.is_terminal) FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id LEFT JOIN release_done_cards d ON d.card_id=c.id AND d.release_id=$2 WHERE c.board_id=$1 AND ((c.release_id=$2 AND c.archived_at IS NULL) OR d.card_id IS NOT NULL) AND d.restored_at IS NULL GROUP BY 1`, board, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n, done int
		if err = rows.Scan(&name, &n, &done); err != nil {
			return v, err
		}
		v.ByColumn[name] = n
		v.Done += done
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	doneRows, err := pool.Query(ctx, `SELECT c.id FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id LEFT JOIN release_done_cards d ON d.card_id=c.id AND d.release_id=$2 WHERE c.board_id=$1 AND ((c.release_id=$2 AND c.archived_at IS NULL) OR d.card_id IS NOT NULL) AND d.restored_at IS NULL AND (col.is_terminal OR d.card_id IS NOT NULL)`, board, id)
	if err != nil {
		return v, err
	}
	defer doneRows.Close()
	v.DoneCardIDs = []string{}
	for doneRows.Next() {
		var id string
		if err = doneRows.Scan(&id); err != nil {
			return v, err
		}
		v.DoneCardIDs = append(v.DoneCardIDs, id)
	}
	if err = doneRows.Err(); err != nil {
		return v, err
	}
	doneRows.Close()
	restored, err := pool.Query(ctx, `SELECT card_id FROM release_done_cards WHERE release_id=$1 AND restored_at IS NOT NULL`, id)
	if err != nil {
		return v, err
	}
	defer restored.Close()
	v.RestoredCardIDs = []string{}
	for restored.Next() {
		var cardID string
		if err = restored.Scan(&cardID); err != nil {
			return v, err
		}
		v.RestoredCardIDs = append(v.RestoredCardIDs, cardID)
	}
	if err = restored.Err(); err != nil {
		return v, err
	}
	restored.Close()
	if err = readReleaseCutEvidence(ctx, pool, &v); err != nil {
		return v, err
	}
	v.Burn, err = ReleaseBurn(ctx, pool, board, id)
	if err != nil {
		v.Burn = BurnChart{Unavailable: true, Count: []BurnPoint{}, Size: []BurnPoint{}}
	}
	return v, nil
}

// CloseRelease serializes with card assignment and moves. Every card change and its audit event commits together.
func CloseRelease(ctx context.Context, pool *pgxpool.Pool, board, id, next string, ev EventMeta) error {
	return CloseReleaseChecked(ctx, pool, board, id, next, ev, nil)
}

type ReleaseArchiveReview struct {
	Version int
	Meta    EventMeta
}

func CloseReleaseChecked(ctx context.Context, pool *pgxpool.Pool, board, id, next string, ev EventMeta, reviews map[string]ReleaseArchiveReview, intents ...releasecut.Intent) error {
	if err := releaseUUID(id, false); err != nil {
		return err
	}
	if err := releaseUUID(next, true); err != nil {
		return err
	}
	if id == next {
		return &FieldError{Key: "next_release_id", Msg: "choose a different next release"}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBoard(ctx, tx, board); err != nil {
		return err
	}
	r, err := scanRelease(tx.QueryRow(ctx, `SELECT `+releaseCols+` FROM board_releases WHERE board_id=$1 AND id=$2 FOR UPDATE`, board, id))
	if err != nil {
		return err
	}
	intent := releasecut.Intent{}
	if len(intents) > 1 {
		return &FieldError{Key: "cut", Msg: "one cut intent is required"}
	}
	if len(intents) == 1 {
		intent = intents[0]
	}
	if intent.AskID != "" {
		if err = releaseUUID(intent.AskID, false); err != nil {
			return err
		}
		if len(intent.CardDigest) != 64 || intent.OwnerID == "" {
			return &FieldError{Key: "cut", Msg: "cut proof is incomplete"}
		}
	}
	if r.Status == "released" {
		receipt, e := readCutReceipt(ctx, tx, board, id)
		if e != nil {
			return e
		}
		if receipt != nil && intent.AskID != "" && receipt.AskID == intent.AskID && receipt.OwnerID == intent.OwnerID && receipt.NextReleaseID == next && receipt.CardDigest == intent.CardDigest && receipt.ExecutionDeadline.Equal(intent.Deadline) {
			return nil
		}
		return &FieldError{Key: "release", Msg: "release already closed", CutCause: "release_already_closed"}
	}
	if err = checkCutDeadline(ctx, tx, intent); err != nil {
		return err
	}

	nextName := ""
	if next != "" {
		if err = validateRelease(ctx, tx, board, &next); err != nil {
			if intent.AskID != "" {
				return ErrStaleVersion
			}
			return err
		}
	}
	if next != "" {
		if err = tx.QueryRow(ctx, `SELECT name FROM board_releases WHERE board_id=$1 AND id=$2 FOR SHARE`, board, next).Scan(&nextName); err != nil {
			return err
		}
	}
	// Column names and terminal flags participate in the person's card digest.
	// Keep them stable across the item classification and receipt snapshot reads.
	columnRows, e := tx.Query(ctx, `SELECT id FROM board_columns WHERE board_id=$1 ORDER BY id FOR SHARE`, board)
	if e != nil {
		return e
	}
	for columnRows.Next() {
		var column string
		if e = columnRows.Scan(&column); e != nil {
			columnRows.Close()
			return e
		}
	}
	e = columnRows.Err()
	columnRows.Close()
	if e != nil {
		return e
	}
	rows, err := tx.Query(ctx, `SELECT c.id,coalesce(col.is_terminal,false),coalesce(col.name,'Archived') FROM cards c LEFT JOIN board_columns col ON col.id=c.column_id WHERE c.board_id=$1 AND c.release_id=$2 AND c.archived_at IS NULL ORDER BY c.id FOR UPDATE OF c`, board, id)
	if err != nil {
		return err
	}
	type item struct {
		id   string
		done bool
		col  string
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.done, &i.col); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var blocker string
	err = tx.QueryRow(ctx, `SELECT '#'||c.number||' '||c.title FROM runner_sessions rs JOIN cards c ON c.id=rs.card_id JOIN board_columns col ON col.id=c.column_id WHERE c.release_id=$1 AND c.archived_at IS NULL AND col.is_terminal AND rs.lifecycle IN ('starting','running','waiting') ORDER BY c.number LIMIT 1`, id).Scan(&blocker)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		return &FieldError{Key: "release", Msg: fmt.Sprintf("stop active session on %s before cutting the release", blocker), CutCause: "active_card_sessions"}
	}
	if reviews != nil {
		doneCount := 0
		for _, i := range items {
			if i.done {
				doneCount++
				review, ok := reviews[i.id]
				if !ok {
					return ErrStaleVersion
				}
				var version int
				if err = tx.QueryRow(ctx, `SELECT version FROM cards WHERE id=$1`, i.id).Scan(&version); err != nil {
					return err
				}
				if version != review.Version {
					return ErrStaleVersion
				}
			}
		}
		if doneCount != len(reviews) {
			return ErrStaleVersion
		}
	}

	preview, err := releaseCutPreview(ctx, tx, board, id)
	if err != nil {
		return err
	}
	if intent.AskID != "" && preview.Digest != intent.CardDigest {
		return &FieldError{Key: "card_digest", Msg: "Cards changed after authorization", CutCause: "card_digest_mismatch"}
	}
	// A lock wait must not extend the authorization's execution deadline.
	if err = checkCutDeadline(ctx, tx, intent); err != nil {
		return err
	}
	receipt := releasecut.Receipt{NextReleaseName: nextName, BoardID: board, ReleaseID: id, NextReleaseID: next, AskID: intent.AskID, OwnerID: intent.OwnerID, PersonAct: intent.PersonAct, ExecutionDeadline: intent.Deadline, CardDigest: preview.Digest, Snapshot: preview.Snapshot, Archived: []string{}, Moved: []string{}}
	if receipt.OwnerID == "" {
		receipt.OwnerID = ev.Actor
		if ev.ActorSessionID != nil {
			receipt.OwnerID = *ev.ActorSessionID
		}
	}
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text,now()`).Scan(&receipt.ID, &receipt.ReleasedAt); err != nil {
		return err
	}
	for _, i := range items {
		if i.done {
			receipt.Archived = append(receipt.Archived, i.id)
		} else {
			receipt.Moved = append(receipt.Moved, i.id)
		}
	}
	for _, i := range items {
		if i.done {
			if _, err = tx.Exec(ctx, `INSERT INTO release_done_cards(release_id,card_id,column_name) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, i.id, i.col); err != nil {
				return err
			}
			archiveEv := ev
			if checked, ok := reviews[i.id]; ok {
				archiveEv = checked.Meta
			}
			if _, err = archiveCardTx(ctx, tx, i.id, nil, archiveEv); err != nil {
				return err
			}
		} else {
			if next == "" {
				return &FieldError{Key: "next_release_id", Msg: "choose a next release for open cards"}
			}
			if _, err = tx.Exec(ctx, `UPDATE cards SET release_id=$2,version=version+1,updated_at=now() WHERE id=$1`, i.id, next); err != nil {
				return err
			}
			if err = appendEvent(ctx, tx, i.id, "updated", ev, nil, nil, map[string]any{"fields": []string{"release_id"}, "from_release": id, "to_release": next}); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE board_releases SET status='released',released_at=now(),released_by=$2 WHERE id=$1`, id, ev.Actor); err != nil {
		return err
	}
	rawReceipt, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO release_cut_receipts(id,board_id,release_id,ask_id,receipt) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5)`, receipt.ID, board, id, intent.AskID, rawReceipt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func releaseUUID(id string, optional bool) error {
	if id == "" && optional {
		return nil
	}
	var value pgtype.UUID
	if err := value.Scan(id); err != nil {
		return &FieldError{Key: "release_id", Msg: "invalid release UUID"}
	}
	return nil
}

// DeleteRelease removes a mistaken active release and clears its assignments atomically.
func DeleteRelease(ctx context.Context, pool *pgxpool.Pool, board, id string) error {
	if err := releaseUUID(id, false); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockBoard(ctx, tx, board); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM board_releases WHERE board_id=$1 AND id=$2 FOR UPDATE`, board, id).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if status == "released" {
		return &FieldError{Key: "release", Msg: "released records cannot be deleted"}
	}
	if _, err = tx.Exec(ctx, `UPDATE cards SET release_id=NULL,version=version+1,updated_at=now() WHERE release_id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM board_releases WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checkCutDeadline(ctx context.Context, tx pgx.Tx, intent releasecut.Intent) error {
	if intent.AskID == "" {
		return nil
	}
	var live bool
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp() < $1`, intent.Deadline).Scan(&live); err != nil {
		return err
	}
	if intent.Deadline.IsZero() || !live {
		return &FieldError{Key: "cut", Msg: "cut authorization expired", CutCause: "execution_deadline"}
	}
	return nil
}
