package db

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DesktopRow is the persisted authority P1 uses before collecting any metadata.
type DesktopRow struct {
	Compatibility bool   `json:"compatibility"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	OwnerID       string `json:"owner_id"`
	Revision      int64  `json:"ownership_revision"`
	Generation    string `json:"connection_generation"`
	Status        string `json:"status"`
	StatusReason  string `json:"status_reason"`
}

var ErrDesktopAuthority = errors.New("desktop ownership changed or unknown")

func GetDesktop(ctx context.Context, pool *pgxpool.Pool, id string) (*DesktopRow, error) {
	var d DesktopRow
	err := pool.QueryRow(ctx, `SELECT id::text,name,COALESCE(desktop_owner_id,''),ownership_revision,COALESCE(connection_generation::text,''),status,status_reason,desktop_compatibility FROM daemons WHERE id=$1 AND mode<>'runner'`, id).Scan(&d.ID, &d.Name, &d.OwnerID, &d.Revision, &d.Generation, &d.Status, &d.StatusReason, &d.Compatibility)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

func ListDesktops(ctx context.Context, pool *pgxpool.Pool, owner string, unknownAdmin bool) ([]DesktopRow, error) {
	rows, err := pool.Query(ctx, `SELECT id::text,name,COALESCE(desktop_owner_id,''),ownership_revision,COALESCE(connection_generation::text,''),status,status_reason,desktop_compatibility FROM daemons
 WHERE mode<>'runner' AND ((desktop_owner_id=$1 AND $1<>'') OR (desktop_owner_id IS NULL AND ($2 OR (desktop_compatibility AND EXISTS(SELECT 1 FROM desktop_compatibility_accounts c WHERE c.desktop_id=daemons.id AND c.account_id=$1))))) ORDER BY name,id`, owner, unknownAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DesktopRow{}
	for rows.Next() {
		var d DesktopRow
		if err := rows.Scan(&d.ID, &d.Name, &d.OwnerID, &d.Revision, &d.Generation, &d.Status, &d.StatusReason, &d.Compatibility); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Desktop fences share the existing bounded advisory-lock pool, with a distinct
// lock namespace. The callback owns the entire frame, delivery or owner change.
func WithDesktopFence(ctx context.Context, pool *pgxpool.Pool, id string, fn func(context.Context) error) error {
	return withAuthorityFence(ctx, pool, id, 876434, fn)
}

// BindDesktopConnection must run under WithDesktopFence, including registration.
// A new credential may create its own UUID; it cannot adopt an existing unknown
// desktop or change a recorded owner. Unknown adoption is an admin/install action.
func BindDesktopConnection(ctx context.Context, pool *pgxpool.Pool, b desktopauth.Binding, name, mode, root, generation string) error {
	if b.DesktopID == "" || b.OwnerID == "" || b.Revision < 1 || generation == "" || mode == "runner" {
		return ErrDesktopAuthority
	}
	_, err := pool.Exec(ctx, `INSERT INTO daemons(id,name,mode,repos_root,desktop_owner_id,ownership_revision) SELECT $1,$2,$3,$4,$5,$6 WHERE $6::bigint=1 ON CONFLICT(id) DO NOTHING`, b.DesktopID, name, mode, root, b.OwnerID, b.Revision)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE daemons SET name=$2,mode=$3,repos_root=$4,connection_generation=$7,status='connected',status_reason='',last_seen_at=now()
 WHERE id=$1 AND desktop_owner_id=$5 AND ownership_revision=$6 AND mode<>'runner'`, b.DesktopID, name, mode, root, b.OwnerID, b.Revision, generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDesktopAuthority
	}
	return nil
}

func ValidateDesktopConnection(ctx context.Context, pool *pgxpool.Pool, b desktopauth.Binding, generation string) error {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daemons WHERE id=$1 AND desktop_owner_id=$2 AND ownership_revision=$3 AND connection_generation=$4 AND mode<>'runner')`, b.DesktopID, b.OwnerID, b.Revision, generation).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok || b.OwnerID == "" || generation == "" {
		return ErrDesktopAuthority
	}
	return nil
}

// AssignDesktop invalidates every old connection before a new owner can use it.
// Caller holds WithDesktopFence. Expected revision prevents competing assignments.
func AssignDesktop(ctx context.Context, pool *pgxpool.Pool, id, owner string, revision int64) (*DesktopRow, error) {
	if owner == "" {
		return nil, ErrDesktopAuthority
	}
	tag, err := pool.Exec(ctx, `UPDATE daemons SET desktop_owner_id=$2,ownership_revision=GREATEST(ownership_revision,enrollment_revision)+1,connection_generation=NULL,status='disconnected',desktop_compatibility=false,enrollment_token_id=NULL,enrollment_request_id=NULL,enrollment_generation=NULL
 WHERE id=$1 AND ownership_revision=$3 AND mode<>'runner'`, id, owner, revision)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrDesktopAuthority
	}
	return GetDesktop(ctx, pool, id)
}

// DesktopOwnsSession prevents token healing, recovery and delivery from carrying
// an earlier owner's session authority across a desktop reassignment.
func DesktopOwnsSession(ctx context.Context, pool *pgxpool.Pool, desktop, owner, session string) bool {
	if pool == nil || owner == "" || session == "" {
		return false
	}
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id::text=$3 AND daemon_id=$1 AND spawning_account_id=$2)`, desktop, owner, session).Scan(&ok)
	return err == nil && ok
}
func DesktopSessionIDs(ctx context.Context, pool *pgxpool.Pool, desktop, owner string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM sessions WHERE daemon_id=$1 AND spawning_account_id=$2`, desktop, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func BindLegacyDesktop(ctx context.Context, pool *pgxpool.Pool, id, generation string) (int64, error) {
	var revision int64
	err := pool.QueryRow(ctx, `UPDATE daemons SET connection_generation=$2,enrollment_token_id=NULL,enrollment_request_id=NULL,enrollment_generation=NULL,status='connected',status_reason='',last_seen_at=now() WHERE id=$1 AND desktop_owner_id IS NULL AND mode<>'runner' RETURNING ownership_revision`, id, generation).Scan(&revision)
	if err != nil {
		return 0, ErrDesktopAuthority
	}
	return revision, nil
}
func ValidateLegacyDesktop(ctx context.Context, pool *pgxpool.Pool, id, generation string, revision int64) error {
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daemons WHERE id=$1 AND desktop_owner_id IS NULL AND connection_generation=$2 AND ownership_revision=$3 AND mode<>'runner')`, id, generation, revision).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDesktopAuthority
	}
	return nil
}

// FindUnownedDesktopByName is only an upgrade bridge. Ambiguous legacy names
// require explicit admin assignment by ID; never guess which person's old row.
func FindUnownedDesktopByName(ctx context.Context, pool *pgxpool.Pool, name string) (*DesktopRow, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM daemons WHERE name=$1 AND desktop_owner_id IS NULL AND mode<>'runner' ORDER BY id LIMIT 2`, name)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
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
	if len(ids) > 1 {
		return nil, ErrDesktopAuthority
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return GetDesktop(ctx, pool, ids[0])
}

// Historical compatibility grants are not ownership. Assignment retires them.
func DesktopAccessible(ctx context.Context, pool *pgxpool.Pool, d DesktopRow, account string) bool {
	if account == "" {
		return false
	}
	if d.OwnerID != "" {
		return d.OwnerID == account
	}
	if !d.Compatibility {
		return false
	}
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM desktop_compatibility_accounts WHERE desktop_id=$1 AND account_id=$2)`, d.ID, account).Scan(&ok)
	return err == nil && ok
}
