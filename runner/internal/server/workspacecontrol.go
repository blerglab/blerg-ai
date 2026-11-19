package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Called only inside the read pump's persisted desktop fence. A runtime token,
// legacy shared token or matching public generation is never this authority.
func (h *Hub) localWorkspaceControl(ctx context.Context, pool *pgxpool.Pool, dc *DaemonConn, req protocol.LocalWorkspaceControl) (reply protocol.LocalWorkspaceControlResult) {
	reply = protocol.LocalWorkspaceControlResult{Type: "local_workspace_control_result", RequestID: req.RequestID, SessionID: req.SessionID}
	// Every failed reply has a safe typed cause, including early exits. Refused
	// retains its stronger meaning: only definitive loss of a launch stops retries.
	defer func() {
		if reply.OK {
			return
		}
		if reply.Cause == "" {
			reply.Cause = protocol.WorkspaceControlUnavailable
		}
		if reply.Refused != "" {
			reply.Cause = workspaceControlRefusalCause(reply.Refused)
		}
		failurelog.Default.Event("workspace_control:"+string(reply.Cause), slog.LevelWarn,
			"component", "runner", "session_id", safeWorkspaceControlSession(req.SessionID), "daemon_id", dc.ID,
			"operation", safeWorkspaceControlOperation(req.Operation), "cause", string(reply.Cause))
	}()

	if (dc.DesktopBinding == nil && !dc.Compatibility) || dc.RuntimeTokenID != "" || h.GetDaemon(dc.ID) != dc || !validSessionID(req.SessionID) || len(req.RequestID) > 100 || req.RequestID == "" {
		reply.Cause = protocol.WorkspaceControlInvalid
		return reply
	}
	// A missing session cannot regain authority on the next heartbeat. Check only
	// after authenticating this desktop connection; DB failures remain retryable.
	if dc.DesktopBinding != nil {
		if !dc.DesktopBinding.ExpiresAt.After(time.Now()) {
			reply.Cause = protocol.WorkspaceControlDesktop
			return reply
		}
		if err := db.ValidateDesktopConnection(ctx, pool, *dc.DesktopBinding, dc.DesktopGeneration); err != nil {
			reply.Cause = workspaceControlErrorCause(err)
			return reply
		}
		row, err := db.GetSession(ctx, pool, req.SessionID)
		if err != nil {
			reply.Cause = workspaceControlErrorCause(err)
			return reply
		}
		if row == nil {
			reply.Refused = "the session is unknown"
			return reply
		}
		if row.DaemonID != dc.ID || row.SpawningAccountID == nil || *row.SpawningAccountID != dc.DesktopBinding.OwnerID {
			reply.Refused = "this desktop no longer owns the session"
			return reply
		}
	}
	if err := validateWorkspaceDesktop(ctx, pool, dc, req.SessionID); err != nil {
		reply.Cause = workspaceControlErrorCause(err)
		return reply
	}
	deny := func(cause protocol.WorkspaceControlCause) error { reply.Cause = cause; return errWorkspaceUnavailable }

	err := db.WithRuntimeLaunchFenceRetry(ctx, pool, req.SessionID, func(ctx context.Context) error {
		if req.Operation == "legacy_attach" {
			return h.attachLegacyWorkspace(ctx, pool, dc, req, &reply)
		}
		row, err := db.GetSession(ctx, pool, req.SessionID)
		if err != nil {
			return err
		}
		if row == nil {
			return workspaceRefused("the session is unknown")
		}
		ws, werr := db.GetSessionWorkspace(ctx, pool, req.SessionID)
		if werr != nil {
			return werr
		}
		if row.DaemonID != dc.ID || row.SpawningAccountID == nil || !h.ownsDesktopID(ctx, *row.SpawningAccountID, dc.ID) {
			reply.Cause = protocol.WorkspaceControlDesktop
			return errWorkspaceUnavailable
		}
		if ws == nil || ws.Manifest == nil || ws.Manifest.OwnerID != *row.SpawningAccountID || (ws.Manifest.Runtime != "sandbox" && ws.Manifest.Runtime != "host") {
			reply.Cause = protocol.WorkspaceControlManifest
			return errWorkspaceUnavailable
		}
		if row.ArchivedAt != nil || row.DiscardRequestedAt != nil && !localWorkspaceCleanupOperation(req.Operation) {
			return workspaceRefused("the session was archived or discarded")
		}
		m := *ws.Manifest
		if membershipControlEarly(req.Operation) {
			return h.membershipControl(ctx, pool, req, &reply)
		}
		if req.Operation == "manifest" {
			reply.Manifest = &m
			if row.ParentSessionID != nil {
				reply.ParentSessionID = *row.ParentSessionID
			}
			return nil
		}
		if req.Operation == "authorize_child" {
			if row.ParentSessionID == nil || *row.ParentSessionID != req.ParentSessionID || row.EndedAt != nil {
				return deny(protocol.WorkspaceControlChildAuthority)
			}
			parent, e := db.GetSessionWorkspace(ctx, pool, req.ParentSessionID)
			if e != nil {
				return e
			}
			if parent == nil || parent.Manifest == nil || protocol.ValidateWorkspaceChild(*parent.Manifest, m) != nil {
				return deny(protocol.WorkspaceControlChildAuthority)
			}
			return nil
		}
		if req.Manifest != nil && !reflect.DeepEqual(*req.Manifest, m) {
			return deny(protocol.WorkspaceControlManifestMismatch)
		}
		generation, binding, err := db.GetWorkspaceRuntime(ctx, pool, req.SessionID)
		if err != nil {
			return err
		}
		live := row.EndedAt == nil && row.Status != "stopped" && row.Status != "ended"
		launch := protocol.SupervisorLaunch{SessionID: req.SessionID, LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: generation, Runtime: m.Runtime, ConnectionID: dc.DesktopGeneration}
		if req.Operation == "launch" || req.Operation == "cleanup_launch" {
			cleanupLaunch := req.Operation == "cleanup_launch"
			if !live && !cleanupLaunch || req.Manifest == nil || !protocol.ValidChildBranchID(req.Token) {
				return deny(protocol.WorkspaceControlInvalid)
			}
			var oldGen int64
			var oldLaunch string
			var aborted bool
			e := pool.QueryRow(ctx, `SELECT runtime_generation,launch_generation,aborted FROM local_workspace_authority WHERE session_id=$1 AND token=$2`, req.SessionID, req.Token).Scan(&oldGen, &oldLaunch, &aborted)
			switch {
			case e == nil:
				if aborted || oldGen != generation || oldLaunch != m.LaunchGeneration {
					return deny(protocol.WorkspaceControlLaunchConflict)
				}
			case errors.Is(e, pgx.ErrNoRows):
				// Generation allocation and the idempotency journal must commit together.
				tx, e := pool.Begin(ctx)
				if e != nil {
					return e
				}
				defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
				var active bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM local_workspace_authority WHERE session_id=$1 AND NOT aborted)`, req.SessionID).Scan(&active); e != nil {
					return e
				}
				if active {
					return deny(protocol.WorkspaceControlLaunchConflict)
				}
				e = tx.QueryRow(ctx, `UPDATE session_workspaces SET runtime_generation=runtime_generation+1,runtime_binding=NULL WHERE session_id=$1 AND runtime_generation=$2 AND runtime_generation<9223372036854775807 RETURNING runtime_generation`, req.SessionID, generation).Scan(&generation)
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO local_workspace_authority(session_id,token,launch_generation,runtime_generation,connection_generation) VALUES($1,$2,$3,$4,$5)`, req.SessionID, req.Token, m.LaunchGeneration, generation, dc.DesktopGeneration); e != nil {
					return e
				}
				if e = tx.Commit(ctx); e != nil {
					return e
				}
			default:
				return e
			}
			launch.RuntimeGeneration = generation
			// A lost launch reply can be retried on a fresh authenticated connection.
			if _, err = pool.Exec(ctx, `UPDATE local_workspace_authority SET connection_generation=$3 WHERE session_id=$1 AND token=$2 AND NOT aborted`, req.SessionID, req.Token, dc.DesktopGeneration); err != nil {
				return err
			}
			if cleanupLaunch {
				if _, err := db.RequestLocalWorkspaceStop(ctx, pool, req.SessionID, systemEnd(db.EndReasonProcessFailed)); err != nil {
					return err
				}
			} else {
				dc.AuthorizeWorkspaceRuntime(req.SessionID, protocol.WorkspaceRuntimeRef{LaunchGeneration: m.LaunchGeneration, Generation: generation})
			}
			reply.Launch = launch
			return nil
		}
		var token, connection string
		var aborted bool
		var stopping, completed bool
		if req.Operation == "abort" {
			if req.Manifest == nil || req.Token == "" {
				return deny(protocol.WorkspaceControlInvalid)
			}
			e := pool.QueryRow(ctx, `SELECT token,connection_generation,aborted FROM local_workspace_authority WHERE session_id=$1 AND token=$2 AND launch_generation=$3`, req.SessionID, req.Token, m.LaunchGeneration).Scan(&token, &connection, &aborted)
			if errors.Is(e, pgx.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			// Fence even after a lost allocation/bind reply. Physical removal is P2's
			// separately journalled exact-container obligation before any next Launch.
			tag, err := pool.Exec(ctx, `WITH fenced AS (UPDATE local_workspace_authority SET aborted=true WHERE session_id=$1 AND token=$2 AND final_digest IS NULL RETURNING runtime_generation) UPDATE session_workspaces SET runtime_binding=NULL WHERE session_id=$1 AND runtime_generation IN (SELECT runtime_generation FROM fenced)`, req.SessionID, token)
			if err == nil && tag.RowsAffected() != 1 {
				return deny(protocol.WorkspaceControlLaunchConflict)
			}
			return err
		}
		if err = pool.QueryRow(ctx, `SELECT token,connection_generation,aborted,stop_requested,final_digest IS NOT NULL FROM local_workspace_authority WHERE session_id=$1 AND runtime_generation=$2 AND NOT aborted`, req.SessionID, generation).Scan(&token, &connection, &aborted, &stopping, &completed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return workspaceRefused("no launch holds this workspace any more")
			}
			return err
		}
		if req.Launch.SessionID != launch.SessionID || req.Launch.LaunchGeneration != launch.LaunchGeneration || req.Launch.RuntimeGeneration != launch.RuntimeGeneration || req.Launch.Runtime != launch.Runtime {
			return workspaceRefused("a newer launch replaced this one")
		}
		if req.Operation == "final" {
			// Completion reconciliation authenticates this new link independently of
			// ordinary live-session Current; exact old outcomes remain immutable.
			if req.Final == nil || req.Binding != req.Final.Binding {
				return deny(protocol.WorkspaceControlInvalid)
			}
			receipt, e := db.CommitLocalWorkspaceFinal(ctx, pool, req.SessionID, token, *req.Final)
			if e != nil {
				return e
			}
			reply.Receipt = receipt
			return nil
		}
		if completed {
			return workspaceRefused("the session's workspace was completed")
		}
		cleanup := localWorkspaceCleanupOperation(req.Operation)
		if (!live || stopping) && !cleanup {
			return workspaceRefused("the session ended or is stopping")
		}
		if req.Operation == "recover" || req.Operation == "cleanup_recover" {
			if binding == nil || *binding != req.Binding {
				return workspaceRefused("a newer runtime replaced this one")
			}
			if _, err = pool.Exec(ctx, `UPDATE local_workspace_authority SET connection_generation=$3 WHERE session_id=$1 AND token=$2`, req.SessionID, token, dc.DesktopGeneration); err != nil {
				return err
			}
			if !cleanup {
				dc.AuthorizeWorkspaceRuntime(req.SessionID, binding.WorkspaceRuntimeRef)
			}
			reply.Launch = launch
			return nil
		}
		if connection != dc.DesktopGeneration || req.Launch != launch {
			reply.Cause = protocol.WorkspaceControlConnectionRecovery
			return errWorkspaceUnavailable
		}
		ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: launch.LaunchGeneration, Generation: generation}
		authorized, ok := dc.authorizedWorkspaceRuntime(req.SessionID)
		if (!ok || authorized != ref) && !cleanup {
			reply.Cause = protocol.WorkspaceControlRuntimeUnauthorized
			return errWorkspaceUnavailable
		}
		switch req.Operation {
		case "membership_commit":
			if req.Membership == nil || binding == nil || *binding != req.Binding || dc.WorkspaceCapabilities.Membership != protocol.WorkspaceMembershipVersion {
				return workspaceRefused("the runtime cannot commit this repository change")
			}
			next, e := db.CommitMembershipOperation(ctx, pool, req.Membership.OperationID, req.Binding, req.Membership.Receipt)
			if errors.Is(e, db.ErrMembershipStale) || errors.Is(e, db.ErrMembershipConflict) {
				return workspaceRefused(e.Error())
			}
			if e == nil {
				reply.Binding = next
			}
			return e
		case "cleanup_bind":
			return db.BindWorkspaceCleanupRuntime(ctx, pool, req.SessionID, ref, req.Binding)
		case "bind":
			return db.BindWorkspaceRuntime(ctx, pool, req.SessionID, ref, req.Binding)
		case "current", "results", "cleanup_current", "cleanup_results":
			if binding == nil || *binding != req.Binding {
				return workspaceRefused("a newer runtime replaced this one")
			}
			if req.Operation == "results" || req.Operation == "cleanup_results" {
				if cleanup {
					return db.SetWorkspaceCleanupResults(ctx, pool, req.SessionID, ref, req.Results)
				}
				return db.SetWorkspaceRuntimeResults(ctx, pool, req.SessionID, ref, req.Results)
			}
			return nil
		default:
			return deny(protocol.WorkspaceControlInvalid)
		}
	})
	if err == nil && req.Operation == "final" {
		err = h.completeLocalWorkspaceFinal(ctx, pool, req.SessionID)
	}
	if err == nil && (req.Operation == "membership_commit" || req.Operation == "membership_installed" || req.Operation == "membership_report" && req.Membership != nil && req.Membership.Refusal != "") {
		h.membershipChanged(ctx, pool, req.SessionID)
	}
	reply.OK = err == nil
	if err != nil && reply.Cause == "" && !errors.Is(err, errWorkspaceUnavailable) {
		reply.Cause = workspaceControlErrorCause(err)
	}
	var refused *workspaceRefusal
	if errors.As(err, &refused) {
		reply.Refused = refused.reason
	}
	return reply
}

// workspaceRefusal is a definitive answer: this launch has no authority any more, and asking again
// will not change that. Only these reach the daemon as Refused; every other failure (a database
// error, a connection not yet recovered) may be transient. It is still errWorkspaceUnavailable.
type workspaceRefusal struct{ reason string }

func workspaceRefused(reason string) error { return &workspaceRefusal{reason: reason} }
func (r *workspaceRefusal) Error() string  { return errWorkspaceUnavailable.Error() + ": " + r.reason }
func (r *workspaceRefusal) Unwrap() error  { return errWorkspaceUnavailable }

type workspaceFinalCommittedKey struct{}

// A local v2 terminal frame is notification of an already committed immutable
// transaction. Missing or conflicting completion cannot end the row separately.
func localWorkspaceTerminalContext(ctx context.Context, pool *pgxpool.Pool, id string, msg protocol.SessionEnded) (context.Context, error) {
	var outcome []byte
	err := pool.QueryRow(ctx, `SELECT final_outcome FROM local_workspace_authority WHERE session_id=$1 AND NOT aborted`, id).Scan(&outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	var final protocol.WorkspaceFinalization
	if len(outcome) == 0 || json.Unmarshal(outcome, &final) != nil || !reflect.DeepEqual(final.Message, msg) {
		return ctx, errWorkspaceUnavailable
	}
	return context.WithValue(ctx, workspaceFinalCommittedKey{}, true), nil
}

func localWorkspaceCleanupOperation(op string) bool {
	switch op {
	case "cleanup_current", "cleanup_recover", "cleanup_results", "final", "abort", "manifest", "cleanup_launch", "cleanup_bind", "membership_status", "membership_installed", "membership_report":
		return true
	}
	return false
}

func safeWorkspaceControlSession(id string) string {
	if validSessionID(id) {
		return id
	}
	return ""
}

func workspaceControlErrorCause(err error) protocol.WorkspaceControlCause {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, db.ErrRuntimeFenceBusy):
		return protocol.WorkspaceControlDeadline
	case errors.Is(err, db.ErrDesktopAuthority):
		return protocol.WorkspaceControlDesktop
	case errors.Is(err, db.ErrWorkspaceRuntimeStale):
		return protocol.WorkspaceControlRuntimeUnauthorized
	default:
		return protocol.WorkspaceControlStorage
	}
}

func safeWorkspaceControlOperation(op string) string {
	switch op {
	case "legacy_attach", "manifest", "authorize_child", "launch", "cleanup_launch", "abort", "final", "recover", "cleanup_recover", "membership_commit", "cleanup_bind", "bind", "current", "results", "cleanup_current", "cleanup_results", "membership_status", "membership_installed", "membership_report":
		return op
	default:
		return "unknown"
	}
}

func workspaceControlRefusalCause(reason string) protocol.WorkspaceControlCause {
	switch reason {
	case "the session is unknown":
		return protocol.WorkspaceControlSessionUnknown
	case "this desktop no longer owns the session":
		return protocol.WorkspaceControlOwnerChanged
	case "the session was archived or discarded":
		return protocol.WorkspaceControlSessionDiscarded
	case "no launch holds this workspace any more":
		return protocol.WorkspaceControlLaunchMissing
	case "a newer launch replaced this one":
		return protocol.WorkspaceControlLaunchReplaced
	case "a newer runtime replaced this one":
		return protocol.WorkspaceControlRuntimeReplaced
	case "the session's workspace was completed":
		return protocol.WorkspaceControlSessionCompleted
	case "the session ended or is stopping":
		return protocol.WorkspaceControlSessionStopping
	default:
		return protocol.WorkspaceControlRefused
	}
}
