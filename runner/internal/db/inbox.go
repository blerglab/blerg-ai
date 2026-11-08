package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// InboxItem is private to AccountID. All reads and mutations require that account.
type InboxItem struct {
	ClientID  string    `json:"-"`
	Preview   string    `json:"preview,omitempty"`
	ID        string    `json:"id"`
	AccountID string    `json:"-"`
	SessionID string    `json:"session_id"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body,omitempty"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
}

func InsertInboxItem(ctx context.Context, q DBTX, item InboxItem) (*InboxItem, bool, error) {
	proposedID := item.ID
	err := q.QueryRow(ctx, `INSERT INTO inbox_items (id,account_id,session_id,subject,body,kind,client_id)
 VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))
 ON CONFLICT (account_id,session_id,client_id) DO UPDATE SET client_id=inbox_items.client_id
 RETURNING id,session_id,subject,body,kind,created_at,is_read`, item.ID, item.AccountID, item.SessionID, item.Subject, item.Body, item.Kind, item.ClientID).Scan(&item.ID, &item.SessionID, &item.Subject, &item.Body, &item.Kind, &item.CreatedAt, &item.Read)
	return &item, item.ID == proposedID, err
}

func ListInboxItems(ctx context.Context, q DBTX, account, before string) ([]InboxItem, error) {
	rows, err := q.Query(ctx, `SELECT id,session_id,subject,kind,created_at,is_read,left(body,180) FROM inbox_items WHERE account_id=$1 AND ($2='' OR (created_at,id)<(SELECT created_at,id FROM inbox_items WHERE account_id=$1 AND id=$2)) ORDER BY created_at DESC,id DESC LIMIT 100`, account, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []InboxItem{}
	for rows.Next() {
		var item InboxItem
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Subject, &item.Kind, &item.CreatedAt, &item.Read, &item.Preview); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func GetInboxItem(ctx context.Context, q DBTX, account, id string) (*InboxItem, error) {
	var item InboxItem
	err := q.QueryRow(ctx, `SELECT id,session_id,subject,body,kind,created_at,is_read FROM inbox_items WHERE account_id=$1 AND id=$2`, account, id).Scan(&item.ID, &item.SessionID, &item.Subject, &item.Body, &item.Kind, &item.CreatedAt, &item.Read)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &item, err
}

func SetInboxRead(ctx context.Context, q DBTX, account, id string, read bool) (*InboxItem, error) {
	var item InboxItem
	err := q.QueryRow(ctx, `UPDATE inbox_items SET is_read=$3 WHERE account_id=$1 AND id=$2 RETURNING id,session_id,subject,kind,created_at,is_read`, account, id, read).Scan(&item.ID, &item.SessionID, &item.Subject, &item.Kind, &item.CreatedAt, &item.Read)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &item, err
}

func CountInboxUnread(ctx context.Context, q DBTX, account string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE account_id=$1 AND NOT is_read`, account).Scan(&count)
	return count, err
}

// DeleteInboxItem atomically scopes deletion to the recipient, even in shared workspaces.
func DeleteInboxItem(ctx context.Context, q DBTX, account, id string) (bool, error) {
	result, err := q.Exec(ctx, `DELETE FROM inbox_items WHERE account_id=$1 AND id=$2`, account, id)
	return result.RowsAffected() > 0, err
}
