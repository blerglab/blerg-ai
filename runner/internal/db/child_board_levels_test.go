package db_test

// Migration 073: a board level passed down by a parent (created_via 'parent') may carry no sign-in
// of its own; every other permission still needs one, and created_via stays a closed set.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestMigration073AllowsOnlyParentBoardRowsWithoutASignIn(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	insert := func(board, proof, via string) error {
		_, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, board_name, proof_session_id, created_via, level)
			VALUES ($1::uuid, 'acct-1', 'board_file', $2, 'B', $3, $4, 'read')`, sid, board, proof, via)
		return err
	}
	// Re-running the migration is harmless.
	sql, err := os.ReadFile("migrations/073_child_board_levels.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("073 again: %v", err)
	}
	if err := insert("board-a", "", "parent"); err != nil {
		t.Fatalf("a parent row without a sign-in: %v", err)
	}
	if err := insert("board-b", "", "panel"); err == nil {
		t.Fatal("a panel row without a sign-in was accepted")
	}
	if err := insert("board-c", "login-1", "elsewhere"); err == nil {
		t.Fatal("an unknown created_via was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, connection_id, connection_name, tool, tool_hash, proof_session_id, created_via)
		VALUES ($1::uuid, 'acct-1', 'mcp_tool', '11111111-1111-4111-8111-111111111111', 'mail', 'send', 'h', '', 'parent')`, sid); err == nil {
		t.Fatal("a tool permission without a sign-in was accepted")
	}
	// The stored row is an ordinary live level of the session.
	g, err := db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
		BoardID: "board-d", Level: db.BoardLevelFull, CreatedVia: "parent"})
	if err != nil || g.ProofSessionID != "" {
		t.Fatalf("InsertProposalGrant parent row: %+v %v", g, err)
	}
	st, err := db.LatestBoardGrant(ctx, pool, sid, "board-d")
	if err != nil || st == nil || st.Level() != db.BoardLevelFull {
		t.Fatalf("latest %+v %v", st, err)
	}
}

// The CHANGELOG's rollback SQL for builds without the child cap ends every board level a started
// session holds, an orphaned child's (its parent row deleted) included, and nothing else.
func TestChangelogChildRollbackSQLEndsEveryStartedSessionsLevel(t *testing.T) {
	raw, err := os.ReadFile("../../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	at := strings.Index(text, "#### Rolling back to a build before children inherit board access")
	if at < 0 {
		t.Fatal("CHANGELOG has no child rollback section")
	}
	start := strings.Index(text[at:], "```sql\n")
	end := strings.Index(text[at+start+7:], "```")
	if start < 0 || end < 0 {
		t.Fatal("child rollback section has no sql block")
	}
	rollback := text[at+start+7 : at+start+7+end]
	pool := reelTestPool(t)
	ctx := context.Background()
	parent, orphan, lowered, plain := reelSession(t, pool), reelSession(t, pool), reelSession(t, pool), reelSession(t, pool)
	for _, sid := range []string{parent, orphan, lowered, plain} {
		if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range []string{orphan, lowered} {
		if _, err := pool.Exec(ctx, `UPDATE sessions SET parent_session_id = $2::uuid, delegation_via = 'request:x', started_by_kind = 'session' WHERE id = $1`, child, parent); err != nil {
			t.Fatal(err)
		}
	}
	give := func(sid, level, via, proof string) {
		t.Helper()
		if _, err := db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
			BoardID: "b-1", Level: level, CreatedVia: via, ProofSessionID: proof}); err != nil {
			t.Fatal(err)
		}
	}
	// Parent Full, passed down Full, parent lowered to Read; a second child the person lowered to Read.
	give(parent, db.BoardLevelFull, "panel", "login-1")
	give(orphan, db.BoardLevelFull, "parent", "login-1")
	give(lowered, db.BoardLevelFull, "parent", "login-1")
	give(lowered, db.BoardLevelRead, "panel", "login-2")
	give(parent, db.BoardLevelRead, "panel", "login-1")
	give(plain, db.BoardLevelFile, "panel", "login-1")
	// The parent row is deleted: the FK clears the orphan's parent_session_id.
	if _, err := pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, parent); err != nil {
		t.Fatal(err)
	}
	var pid *string
	if err := pool.QueryRow(ctx, `SELECT parent_session_id::text FROM sessions WHERE id = $1`, orphan).Scan(&pid); err != nil || pid != nil {
		t.Fatalf("orphan's parent %v %v", pid, err)
	}
	for range 2 { // idempotent
		if _, err := pool.Exec(ctx, rollback); err != nil {
			t.Fatalf("rollback sql: %v", err)
		}
	}
	// A build before the cap reads the child's own latest row (and 0.4.3 any live board_file row).
	for name, sid := range map[string]string{"orphaned child": orphan, "lowered child": lowered} {
		st, err := db.LatestBoardGrant(ctx, pool, sid, "b-1")
		if err != nil || st.Level() != db.BoardLevelPropose {
			t.Fatalf("%s keeps %s after the rollback SQL (%v)", name, st.Level(), err)
		}
		var live int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_proposal_grants WHERE session_id = $1::uuid AND kind = 'board_file' AND revoked_at IS NULL`, sid).Scan(&live); err != nil || live != 0 {
			t.Fatalf("%s: %d live rows (%v)", name, live, err)
		}
	}
	// A session nobody started keeps its level.
	if st, _ := db.LatestBoardGrant(ctx, pool, plain, "b-1"); st.Level() != db.BoardLevelFile {
		t.Fatalf("a direct session lost its level: %s", st.Level())
	}
}
