package server

// Round-2 fix (G3b) MINOR 39 (a): a session that starts private never has a public row, even when
// the pre-created row could not be written or the token could not be minted.

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The token's insert fails after the row went in: the row must already carry its owner and its
// private flag (it is inserted with its origin, never public-then-marked).
func TestMintFailureLeavesThePrivateRowPrivate(t *testing.T) {
	fx := newMCPFx(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE FUNCTION g3b_fail_token() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'no tokens today'; END $$`,
		`CREATE TRIGGER g3b_fail_token BEFORE INSERT ON board_tokens FOR EACH ROW EXECUTE FUNCTION g3b_fail_token()`,
	} {
		if _, err := fx.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sid := newUUID()
	tok := mintSpawnSessionTokenAs(ctx, fx.pool, sid, fx.dc, "", "t", "", db.SessionOrigin{SpawningAccount: mcpAcct, Private: true})
	if tok != "" {
		t.Fatalf("a token was minted despite the failing insert: %q", tok)
	}
	row, err := db.GetSession(ctx, fx.pool, sid)
	if err != nil || row == nil {
		t.Fatalf("the pre-created row: %v, %v", row, err)
	}
	if !row.Private || row.SpawningAccountID == nil || *row.SpawningAccountID != mcpAcct {
		t.Errorf("row private=%v owner=%v, want private and owned from the insert", row.Private, row.SpawningAccountID)
	}
}

// The pre-create itself failed (so no row exists yet) but the hub was told the session is private:
// the daemon's session_started then inserts the row, and it must go in private and owned, not public
// until a later MarkPrivate.
func TestSessionStartedInsertsAKnownPrivateSessionPrivate(t *testing.T) {
	fx := newMCPFx(t)
	ctx := context.Background()
	sid := newUUID()
	fx.hub.notePrivate(sid, mcpAcct)
	HandleSessionStarted(ctx, fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{
		Type: "session_started", SessionID: sid, ProjectPath: "/repos/p", Repo: "p", Title: "T",
	})
	row, err := db.GetSession(ctx, fx.pool, sid)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v, %v", row, err)
	}
	if !row.Private || row.SpawningAccountID == nil || *row.SpawningAccountID != mcpAcct {
		t.Errorf("row private=%v owner=%v, want private and owned", row.Private, row.SpawningAccountID)
	}
	// A session the hub knows nothing about stays public, as before.
	other := newUUID()
	HandleSessionStarted(ctx, fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{
		Type: "session_started", SessionID: other, ProjectPath: "/repos/p", Repo: "p", Title: "T",
	})
	if row, _ := db.GetSession(ctx, fx.pool, other); row == nil || row.Private {
		t.Errorf("an ordinary session became private: %+v", row)
	}
}
