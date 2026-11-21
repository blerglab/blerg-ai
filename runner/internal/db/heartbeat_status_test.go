package db_test

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
	"time"
)

func TestPinnedStatusCASAndAtomicRevoke(t *testing.T) {
	for _, scenario := range []string{"changed_status", "changed_owner", "changed_daemon", "changed_generation", "changed_revision", "rollback", "applied"} {
		t.Run(scenario, func(t *testing.T) {
			pool := reelTestPool(t)
			sid := reelSession(t, pool)
			const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
			const gen = "00000000-0000-4000-8000-00000000ab01"
			if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='owner',ownership_revision=3,connection_generation=$2 WHERE id=$1`, daemon, gen); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner',status='running' WHERE id=$1`, sid); err != nil {
				t.Fatal(err)
			}
			board, err := db.CreateBoard(t.Context(), pool, "fixture", nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, token, err := db.MintBoardToken(t.Context(), pool, board.ID, sid, []string{"read"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pool.Exec(t.Context(), `INSERT INTO session_mcp_grants(session_id,connection_id,name,account_id,tools,url_snapshot,proof_kind,proof_value,token_hash,call_budget) VALUES ($1,'00000000-0000-4000-8000-00000000ac01','fixture','owner','{}','fixture','token_id','fixture',decode('01','hex'),1)`, sid)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "changed_generation" {
				_, err = pool.Exec(t.Context(), `UPDATE daemons SET connection_generation='00000000-0000-4000-8000-00000000ab02' WHERE id=$1`, daemon)
			}
			if scenario == "changed_revision" {
				_, err = pool.Exec(t.Context(), `UPDATE daemons SET ownership_revision=4 WHERE id=$1`, daemon)
			}
			if scenario == "changed_status" {
				_, err = pool.Exec(t.Context(), `UPDATE sessions SET status='idle' WHERE id=$1`, sid)
			}
			if scenario == "changed_daemon" {
				const other = "00000000-0000-4000-8000-00000000ad01"
				if err := db.UpsertDaemon(t.Context(), pool, other, "other", "host", ""); err != nil {
					t.Fatal(err)
				}
				_, err = pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2 WHERE id=$1`, sid, other)
			}
			if scenario == "changed_owner" {
				_, err = pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='other' WHERE id=$1`, sid)
			}
			if scenario == "rollback" {
				_, err = pool.Exec(t.Context(), `CREATE FUNCTION reject_state_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_state_event BEFORE INSERT ON session_events FOR EACH ROW EXECUTE FUNCTION reject_state_event()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			pin := db.DaemonAuthority{DaemonID: daemon, OwnerID: "owner", Revision: 3, Generation: gen}
			now := time.Now()
			applied, err := db.ReconcileSessionStatus(t.Context(), pool, pin, sid, "running", nil, "stopped", &now, db.SessionEnd{Reason: db.EndReasonDaemonUnreported}, true)
			switch {
			case scenario == "rollback":
				if err == nil || applied {
					t.Fatalf("failure committed: %v %v", applied, err)
				}
			case scenario == "changed_generation" || scenario == "changed_revision":
				if !errors.Is(err, db.ErrDesktopAuthority) || applied {
					t.Fatalf("stale pin accepted: %v %v", applied, err)
				}
			case err != nil || applied != (scenario == "applied"):
				t.Fatalf("unexpected result: %v %v", applied, err)
			}
			var revoked bool
			if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM board_tokens WHERE id=$1`, token.ID).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			var grantCount int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM session_mcp_grants WHERE session_id=$1`, sid).Scan(&grantCount); err != nil {
				t.Fatal(err)
			}
			if (grantCount == 0) != (scenario == "applied") {
				t.Fatalf("grant count=%d scenario=%s", grantCount, scenario)
			}
			if revoked != (scenario == "applied") {
				t.Fatalf("revoked=%v scenario=%s", revoked, scenario)
			}
		})
	}
}

func TestPinnedPendingEndAndDiscard(t *testing.T) {
	for _, scenario := range []string{"pending", "discard"} {
		t.Run(scenario, func(t *testing.T) {
			pool := reelTestPool(t)
			sid := reelSession(t, pool)
			const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
			const gen = "00000000-0000-4000-8000-00000000ab01"
			if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='owner',ownership_revision=3,connection_generation=$2 WHERE id=$1`, daemon, gen); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner',status='running',end_reason='stopped_by_user',end_recorded_at=now()-interval '2 minutes',discard_requested_at=CASE WHEN $2 THEN now() ELSE NULL END WHERE id=$1`, sid, scenario == "discard"); err != nil {
				t.Fatal(err)
			}
			board, err := db.CreateBoard(t.Context(), pool, "fixture", nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, token, err := db.MintBoardToken(t.Context(), pool, board.ID, sid, []string{"read"}, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			pin := db.DaemonAuthority{DaemonID: daemon, OwnerID: "owner", Revision: 3, Generation: gen}
			reason := "stopped_by_user"
			applied, err := db.ReconcilePinnedPendingEnd(t.Context(), pool, pin, sid, "running", &reason, scenario == "discard")
			if err != nil || !applied {
				t.Fatalf("pending reconcile: %v %v", applied, err)
			}
			var revoked bool
			var current *string
			if err := pool.QueryRow(t.Context(), `SELECT b.revoked_at IS NOT NULL,s.end_reason FROM board_tokens b JOIN sessions s ON s.id=b.session_id WHERE b.id=$1`, token.ID).Scan(&revoked, &current); err != nil {
				t.Fatal(err)
			}
			if scenario == "pending" && (revoked || current != nil) {
				t.Fatalf("pending: revoked=%v end=%v", revoked, current)
			}
			if scenario == "discard" && (!revoked || current == nil) {
				t.Fatalf("discard: revoked=%v end=%v", revoked, current)
			}
		})
	}
}

func TestPinnedFrameUnreadAndErrorCommitWithState(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
	const gen = "00000000-0000-4000-8000-00000000ab01"
	if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='owner',ownership_revision=3,connection_generation=$2 WHERE id=$1`, daemon, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner',status='running',unread=false WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION reject_frame_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END $$; CREATE TRIGGER reject_frame_event BEFORE INSERT ON session_events FOR EACH ROW EXECUTE FUNCTION reject_frame_event()`); err != nil {
		t.Fatal(err)
	}
	pin := db.DaemonAuthority{DaemonID: daemon, OwnerID: "owner", Revision: 3, Generation: gen}
	effects := db.PinnedFrameEffects{SetUnread: true}
	applied, err := db.ApplyPinnedSessionFrame(t.Context(), pool, pin, sid, "running", nil, "idle", nil, db.SessionEnd{}, false, &effects)
	if err == nil || applied {
		t.Fatalf("failed event committed: %v %v", applied, err)
	}
	row, err := db.GetSession(t.Context(), pool, sid)
	if err != nil || row.Status != "running" || row.Unread {
		t.Fatalf("failed frame left effects: %+v %v", row, err)
	}
	if _, err := pool.Exec(t.Context(), `DROP TRIGGER reject_frame_event ON session_events`); err != nil {
		t.Fatal(err)
	}
	applied, err = db.ApplyPinnedSessionFrame(t.Context(), pool, pin, sid, "running", nil, "idle", nil, db.SessionEnd{}, false, &effects)
	if err != nil || !applied {
		t.Fatalf("idle frame: %v %v", applied, err)
	}
	row, err = db.GetSession(t.Context(), pool, sid)
	if err != nil || row.Status != "idle" || !row.Unread {
		t.Fatalf("idle frame effects: %+v %v", row, err)
	}
	reason := "fixture-error"
	effects = db.PinnedFrameEffects{ErrorReason: &reason}
	applied, err = db.ApplyPinnedSessionFrame(t.Context(), pool, pin, sid, "idle", nil, "error", nil, db.SessionEnd{}, false, &effects)
	if err != nil || !applied {
		t.Fatalf("error frame: %v %v", applied, err)
	}
	row, err = db.GetSession(t.Context(), pool, sid)
	if err != nil || row.ErrorReason == nil || *row.ErrorReason != reason {
		t.Fatalf("error reason missing: %+v %v", row, err)
	}
}
