package db

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BrowserProjection is a short-lived read model, never mutation authority.
// It is rebuilt inside the delivery fence rather than reused from collection.
type BrowserProjection struct {
	Sessions   map[string]*SessionRow
	Desktops   map[string]*DesktopRow
	Accessible map[string]bool
	Workspaces map[string]*SessionWorkspace
	Operations map[string]*MembershipOperation
}

// LoadBrowserProjection uses a fixed number of set-based queries. A failed
// SQL component rejects the projection. A malformed optional workspace is
// omitted for that session, matching the established per-session mapper.
// Workspace and membership payloads are selected only for the launching owner.
func LoadBrowserProjection(ctx context.Context, pool *pgxpool.Pool, account string, sessions []SessionRow, metadata bool, extraDesktops ...string) (*BrowserProjection, error) {
	p := &BrowserProjection{
		Sessions: map[string]*SessionRow{}, Desktops: map[string]*DesktopRow{},
		Accessible: map[string]bool{}, Workspaces: map[string]*SessionWorkspace{},
		Operations: map[string]*MembershipOperation{},
	}
	ids := make([]string, 0, len(sessions))
	desktopIDs := append([]string{}, extraDesktops...)
	for _, row := range sessions {
		p.Sessions[row.ID] = &row
		ids = append(ids, row.ID)
		desktopIDs = append(desktopIDs, row.DaemonID)
	}
	rows, err := pool.Query(ctx, `SELECT id::text,name,COALESCE(desktop_owner_id,''),ownership_revision,COALESCE(connection_generation::text,''),status,status_reason,desktop_compatibility,
 ($2::text<>'' AND (COALESCE(desktop_owner_id=$2,false) OR (desktop_owner_id IS NULL AND desktop_compatibility AND EXISTS(SELECT 1 FROM desktop_compatibility_accounts c WHERE c.desktop_id=daemons.id AND c.account_id=$2))))
 FROM daemons WHERE mode<>'runner' AND id=ANY($1::uuid[])`, desktopIDs, account)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d DesktopRow
		var accessible bool
		if err = rows.Scan(&d.ID, &d.Name, &d.OwnerID, &d.Revision, &d.Generation, &d.Status, &d.StatusReason, &d.Compatibility, &accessible); err != nil {
			rows.Close()
			return nil, err
		}
		p.Desktops[d.ID] = &d
		p.Accessible[d.ID] = accessible
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !metadata {
		return p, nil
	}
	rows, err = pool.Query(ctx, `SELECT s.id::text,s.runtime_target_id,s.workspace_kind,COALESCE(r.manifest,w.manifest),w.results
 FROM sessions s LEFT JOIN session_workspaces w ON w.session_id=s.id
 LEFT JOIN session_workspace_revisions r ON r.session_id=w.session_id AND r.revision=w.membership_revision
 WHERE s.id=ANY($1::uuid[]) AND s.spawning_account_id=$2 AND $2::text<>''`, ids, account)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var w SessionWorkspace
		var manifest, results []byte
		if err = rows.Scan(&id, &w.RuntimeTargetID, &w.Kind, &manifest, &results); err != nil {
			rows.Close()
			return nil, err
		}
		if manifest != nil {
			if err = json.Unmarshal(manifest, &w.Manifest); err != nil {
				logBrowserWorkspaceDecode(id, "manifest")
				continue
			}
			if err = json.Unmarshal(results, &w.Results); err != nil {
				logBrowserWorkspaceDecode(id, "results")
				continue
			}
			if w.Manifest != nil && strings.HasPrefix(w.Manifest.LaunchGeneration, "legacy-") {
				w.Manifest = nil
				w.Results = nil
			}
		}
		p.Workspaces[id] = &w
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = pool.Query(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE session_id=ANY($1::uuid[]) AND phase IN ('requested','authorized','committed')
 AND EXISTS(SELECT 1 FROM sessions s WHERE s.id=session_id AND s.spawning_account_id=$2 AND $2::text<>'')`, ids, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		op, e := scanMembership(rows)
		if e != nil {
			return nil, e
		}
		p.Operations[op.SessionID] = op
	}
	return p, rows.Err()
}

func logBrowserWorkspaceDecode(session, field string) {
	failurelog.Default.Event("browser_workspace_decode:"+session, slog.LevelWarn, "component", "runner", "operation", "browser_initial_state", "session_id", session, "stage", "workspace_"+field, "cause", "decode")
}
