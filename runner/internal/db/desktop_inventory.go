package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DesktopListing is one desktop in Settings → Account → Your desktops.
type DesktopListing struct {
	ID            string
	Name          string // the person's name for it, else the daemon's host name
	HostName      string
	OwnerID       string
	Revision      int64
	Status        string
	StatusReason  string
	Compatibility bool
	LastSeenAt    *time.Time
}

// ListDesktopInventory lists account's own desktops and the unassigned ones they may enroll
// (recorded for them at the 0.5 upgrade). An administrator (all) sees every person's desktops
// too, and unassigned ones that are connected or still in upgrade compatibility.
func ListDesktopInventory(ctx context.Context, pool *pgxpool.Pool, account string, all bool) ([]DesktopListing, error) {
	rows, err := pool.Query(ctx, `SELECT id::text,COALESCE(NULLIF(display_name,''),name),name,COALESCE(desktop_owner_id,''),ownership_revision,status,status_reason,desktop_compatibility,last_seen_at FROM daemons
 WHERE mode<>'runner' AND (
  (desktop_owner_id=$1 AND $1<>'')
  OR ($2 AND desktop_owner_id IS NOT NULL)
  OR (desktop_owner_id IS NULL AND desktop_compatibility AND EXISTS(SELECT 1 FROM desktop_compatibility_accounts c WHERE c.desktop_id=daemons.id AND c.account_id=$1))
  OR ($2 AND desktop_owner_id IS NULL AND (desktop_compatibility OR status='connected')))
 ORDER BY 2,id`, account, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DesktopListing{}
	for rows.Next() {
		var d DesktopListing
		if err := rows.Scan(&d.ID, &d.Name, &d.HostName, &d.OwnerID, &d.Revision, &d.Status, &d.StatusReason, &d.Compatibility, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RenameDesktop sets the owner's own name for their desktop. False: not theirs.
func RenameDesktop(ctx context.Context, pool *pgxpool.Pool, id, owner, name string) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE daemons SET display_name=$3 WHERE id=$1 AND desktop_owner_id=$2 AND $2<>'' AND mode<>'runner'`, id, owner, name)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeDesktop takes a desktop away from its owner: it becomes unassigned at a new revision
// (so no earlier credential or connection can match it again), leaves upgrade compatibility,
// drops any pending enrollment and is audited. The caller holds WithDesktopFence and closes
// the live connection.
func RevokeDesktop(ctx context.Context, pool *pgxpool.Pool, id, actor string, revision int64) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var next int64
	err = tx.QueryRow(ctx, `UPDATE daemons SET desktop_owner_id=NULL,ownership_revision=GREATEST(ownership_revision,enrollment_revision)+1,connection_generation=NULL,status='disconnected',status_reason='Removed by its owner or an administrator.',desktop_compatibility=false,enrollment_owner_id=NULL,enrollment_token_id=NULL,enrollment_request_id=NULL,enrollment_generation=NULL
 WHERE id=$1 AND ownership_revision=$2 AND desktop_owner_id IS NOT NULL AND mode<>'runner' RETURNING ownership_revision`, id, revision).Scan(&next)
	if err != nil {
		return 0, ErrDesktopAuthority
	}
	if _, err := tx.Exec(ctx, `DELETE FROM desktop_compatibility_accounts WHERE desktop_id=$1`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_enrollment_audit(desktop_id,actor_id,action,ownership_revision) VALUES($1,$2,'revoked',$3)`, id, actor, next); err != nil {
		return 0, err
	}
	return next, tx.Commit(ctx)
}

// DesktopRemovedBy reports whether the desktop is unowned because of a removal the actor made
// (any administrator may finish an administrator's or owner's removal): its latest audit
// record is "revoked". A later enrollment or assignment writes a newer record.
func DesktopRemovedBy(ctx context.Context, pool *pgxpool.Pool, id, actor string, admin bool) bool {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daemons d WHERE d.id=$1 AND d.desktop_owner_id IS NULL AND mode<>'runner'
 AND (SELECT a.action='revoked' AND (a.actor_id=$2 OR $3) FROM desktop_enrollment_audit a WHERE a.desktop_id=d.id ORDER BY a.id DESC LIMIT 1))`, id, actor, admin).Scan(&ok)
	return err == nil && ok
}

// AssignDesktopAudited is AssignDesktop with its audit record in the same transaction: a desktop
// added with an enrollment code is either owned and audited, or neither.
func AssignDesktopAudited(ctx context.Context, pool *pgxpool.Pool, id, owner string, revision int64, action string) (*DesktopRow, error) {
	if owner == "" {
		return nil, ErrDesktopAuthority
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var next int64
	err = tx.QueryRow(ctx, `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=GREATEST(ownership_revision,enrollment_revision)+1,connection_generation=NULL,status='disconnected',desktop_compatibility=false,enrollment_token_id=NULL,enrollment_request_id=NULL,enrollment_generation=NULL
 WHERE id=$1 AND ownership_revision=$3 AND mode<>'runner' RETURNING ownership_revision`, id, owner, revision).Scan(&next)
	if err != nil {
		return nil, ErrDesktopAuthority
	}
	if _, err := tx.Exec(ctx, `INSERT INTO desktop_enrollment_audit(desktop_id,actor_id,action,ownership_revision) VALUES($1,$2,$3,$4)`, id, owner, action, next); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetDesktop(ctx, pool, id)
}
