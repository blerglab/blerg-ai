package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CommitLocalWorkspaceFinal is called under both the desktop ownership and
// runtime launch fences. The transaction commits exact results, terminal state
// and receipt together. It accepts identical retries after end, never a new
// outcome on an ended row. The caller must authenticate the current desktop.
func CommitLocalWorkspaceFinal(ctx context.Context, pool *pgxpool.Pool, id, token string, f protocol.WorkspaceFinalization) (protocol.WorkspaceFinalReceipt, error) {
	refuse := errors.New("workspace completion refused")
	tx, err := pool.Begin(ctx)
	if err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	var rawManifest, rawBinding, saved []byte
	var gen int64
	var digest *string
	err = tx.QueryRow(ctx, `SELECT w.manifest,w.runtime_binding,w.runtime_generation,a.final_digest,a.final_outcome FROM sessions s JOIN session_workspaces w ON w.session_id=s.id JOIN local_workspace_authority a ON a.session_id=s.id AND a.token=$2 AND NOT a.aborted AND a.runtime_generation=w.runtime_generation AND a.launch_generation=w.launch_generation WHERE s.id=$1 FOR UPDATE OF s,w,a`, id, token).Scan(&rawManifest, &rawBinding, &gen, &digest, &saved)
	if err != nil {
		return protocol.WorkspaceFinalReceipt{}, refuse
	}
	var m protocol.WorkspaceManifest
	var binding protocol.WorkspaceRuntimeBinding
	if json.Unmarshal(rawManifest, &m) != nil || json.Unmarshal(rawBinding, &binding) != nil || binding != f.Binding || gen != binding.Generation || binding.Validate(m) != nil || m.ValidateResults(f.Results) != nil || len(f.Results) != len(m.Members) || f.Message.Type != "session_ended" || f.Message.SessionID != id || f.Message.WorkspaceResults != nil {
		return protocol.WorkspaceFinalReceipt{}, refuse
	}
	receipt := protocol.WorkspaceFinalReceipt{Digest: f.Digest()}
	if digest != nil {
		var old protocol.WorkspaceFinalization
		if *digest != receipt.Digest || json.Unmarshal(saved, &old) != nil || !reflect.DeepEqual(old, f) {
			return protocol.WorkspaceFinalReceipt{}, refuse
		}
		return receipt, tx.Commit(ctx)
	}

	results, _ := json.Marshal(f.Results)
	outcome, _ := json.Marshal(f)
	if _, err = tx.Exec(ctx, `UPDATE session_workspaces SET results=$2 WHERE session_id=$1`, id, results); err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	status, reason := "stopped", EndReasonProcessExited
	if f.Message.ExitCode != 0 {
		status, reason = "error", EndReasonProcessFailed
	}
	now := time.Now()
	if err = endSessionStatus(ctx, tx, id, status, &now, SessionEnd{Reason: reason}); err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE board_tokens SET revoked_at=now() WHERE session_id=$1 AND revoked_at IS NULL`, id); err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	if f.Message.ExitCode != 0 && f.Message.Message != "" {
		if _, err = tx.Exec(ctx, `UPDATE sessions SET error_reason=$2 WHERE id=$1`, id, f.Message.Message); err != nil {
			return protocol.WorkspaceFinalReceipt{}, err
		}
	}
	event, _ := json.Marshal(map[string]any{"status": status, "exit_code": f.Message.ExitCode})
	if _, err = tx.Exec(ctx, `INSERT INTO session_events(session_id,type,data,seq) VALUES($1,'state_change',$2,0)`, id, string(event)); err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE local_workspace_authority SET final_digest=$3,final_outcome=$4 WHERE session_id=$1 AND token=$2`, id, token, receipt.Digest, outcome); err != nil {
		return protocol.WorkspaceFinalReceipt{}, err
	}
	return receipt, tx.Commit(ctx)
}
