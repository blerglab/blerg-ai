package db_test

// Board access levels (migration 070): 0.4.3 filing permissions become level file, a level change
// is a new row, the latest row of the session's current lifetime is the level, and only file and
// full approve a filing.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestMigration070KeepsEveryFilingPermissionAtFile(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	// A 0.4.3 database: no level column, and a filing permission and a tool permission.
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS session_proposal_grants_board_latest_idx;
		ALTER TABLE session_proposal_grants DROP COLUMN level`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, board_name, proof_session_id, created_via)
		VALUES ($1::uuid, 'acct-1', 'board_file', 'board-a', 'A', 'login-1', 'panel'),
		       ($1::uuid, 'acct-1', 'mcp_tool', '', '', 'login-1', 'card')`, sid); err == nil {
		t.Fatal("a tool permission without its connection was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, board_name, proof_session_id, created_via)
		VALUES ($1::uuid, 'acct-1', 'board_file', 'board-a', 'A', 'login-1', 'panel')`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, connection_id, connection_name, tool, tool_hash, proof_session_id, created_via)
		VALUES ($1::uuid, 'acct-1', 'mcp_tool', '11111111-1111-4111-8111-111111111111', 'mail', 'send', 'h', 'login-1', 'card')`, sid); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("migrations/070_session_board_levels.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("070: %v", err)
	}
	var board, tool string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT level FROM session_proposal_grants WHERE session_id = $1::uuid AND kind = 'board_file'),
		(SELECT level FROM session_proposal_grants WHERE session_id = $1::uuid AND kind = 'mcp_tool')`, sid).Scan(&board, &tool); err != nil {
		t.Fatal(err)
	}
	if board != db.BoardLevelFile || tool != db.BoardLevelFile {
		t.Fatalf("after 070: board=%q tool=%q, want file", board, tool)
	}
	// It is the live level the gateway reads, and it still approves filings.
	st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a")
	if err != nil || st == nil || st.Level() != db.BoardLevelFile {
		t.Fatalf("latest after 070: %+v %v", st, err)
	}
	if g, err := db.LiveProposalGrantFor(ctx, pool, sid, "acct-1", db.ProposalGrantBoard, "board-a", "", "", ""); err != nil || g == nil {
		t.Fatalf("a migrated filing permission no longer approves: %v", err)
	}
	// The constraint holds: no other level, and a tool permission has none of its own.
	if _, err := pool.Exec(ctx, `UPDATE session_proposal_grants SET level = 'admin' WHERE kind = 'board_file'`); err == nil {
		t.Fatal("an unknown level was stored")
	}
	if _, err := pool.Exec(ctx, `UPDATE session_proposal_grants SET level = 'full' WHERE kind = 'mcp_tool'`); err == nil {
		t.Fatal("a tool permission took a board level")
	}
	// Running it again changes nothing.
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("070 again: %v", err)
	}
}

func TestBoardLevelsAreRowsAndOnlyFileAndFullApprove(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	set := func(level string) *db.ProposalGrant {
		t.Helper()
		g, err := db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
			BoardID: "board-a", BoardName: "A", ProofSessionID: "login-1", CreatedVia: "panel", Level: level})
		if err != nil {
			t.Fatalf("level %q: %v", level, err)
		}
		return g
	}
	if st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a"); err != nil || st != nil {
		t.Fatalf("no row: %+v %v", st, err)
	}
	approves := func() bool {
		g, err := db.LiveProposalGrantFor(ctx, pool, sid, "acct-1", db.ProposalGrantBoard, "board-a", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		return g != nil
	}
	for _, c := range []struct {
		level   string
		approve bool
	}{{db.BoardLevelRead, false}, {db.BoardLevelFile, true}, {db.BoardLevelFull, true}, {db.BoardLevelPropose, false}} {
		g := set(c.level)
		st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a")
		if err != nil || st == nil || st.Grant.ID != g.ID || st.Level() != c.level {
			t.Fatalf("%s: latest %+v %v", c.level, st, err)
		}
		if approves() != c.approve {
			t.Fatalf("%s: approves=%v", c.level, !c.approve)
		}
	}
	// Setting the same level again keeps the row; every change is its own row, the old ones revoked.
	again := set(db.BoardLevelPropose)
	if st, _ := db.LatestBoardGrant(ctx, pool, sid, "board-a"); st.Grant.ID != again.ID {
		t.Fatal("same level made a new row")
	}
	var rows, live int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE revoked_at IS NULL) FROM session_proposal_grants WHERE session_id = $1::uuid`, sid).Scan(&rows, &live); err != nil {
		t.Fatal(err)
	}
	if rows != 4 || live != 1 {
		t.Fatalf("rows=%d live=%d, want 4 and 1", rows, live)
	}
	var reasons int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_proposal_grants WHERE session_id = $1::uuid AND revoked_reason = 'level_changed'`, sid).Scan(&reasons); err != nil || reasons != 3 {
		t.Fatalf("level_changed rows = %d %v", reasons, err)
	}
	// Revoked: the board is at propose, not at no-row (which the caller may read as a launch board's full).
	full := set(db.BoardLevelFull)
	if _, err := db.RevokeProposalGrant(ctx, pool, full.ID, "acct-1", "revoked"); err != nil {
		t.Fatal(err)
	}
	st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a")
	if err != nil || st == nil || st.Live || st.Level() != db.BoardLevelPropose {
		t.Fatalf("revoked: %+v %v", st, err)
	}
	list, err := db.ListLatestBoardGrants(ctx, pool, sid)
	if err != nil || len(list) != 1 || list[0].Level() != db.BoardLevelPropose {
		t.Fatalf("list: %+v %v", list, err)
	}
	// A new lifetime (stopped for good, then resumed) starts from no row again.
	set(db.BoardLevelRead)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET delegation_ended_at = clock_timestamp() WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a"); err != nil || st != nil {
		t.Fatalf("new lifetime: %+v %v", st, err)
	}
	// Bad levels are refused before the database.
	if _, err := db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
		BoardID: "board-a", ProofSessionID: "login-1", CreatedVia: "panel", Level: "admin"}); err == nil {
		t.Fatal("level admin accepted")
	}
}

// Minor 1: two racing level changes can leave the live row with an older created_at (its
// transaction began first) than the row it revoked. The live row is the level.
func TestLatestBoardGrantPrefersTheLiveRowOverANewerRevokedOne(t *testing.T) {
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	// The end state of the race: T2's read row (newer, revoked by T1) and T1's full row (older, live).
	var full string
	if err := pool.QueryRow(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, proof_session_id, created_via, level, created_at)
		VALUES ($1::uuid, 'acct-1', 'board_file', 'board-a', 'login-1', 'panel', 'full', now() - interval '1 second') RETURNING id::text`, sid).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_proposal_grants (session_id, account_id, kind, board_id, proof_session_id, created_via, level, created_at, revoked_at, revoked_reason)
		VALUES ($1::uuid, 'acct-1', 'board_file', 'board-a', 'login-1', 'panel', 'read', now(), now(), 'level_changed')`, sid); err != nil {
		t.Fatal(err)
	}
	st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a")
	if err != nil || st == nil || st.Grant.ID != full || st.Level() != db.BoardLevelFull {
		t.Fatalf("latest = %+v %v, want the live full row", st, err)
	}
	list, err := db.ListLatestBoardGrants(ctx, pool, sid)
	if err != nil || len(list) != 1 || list[0].Grant.ID != full {
		t.Fatalf("list = %+v %v", list, err)
	}
	// And under real concurrent changes, the level read always names the one live row.
	var wg sync.WaitGroup
	for i := range 8 {
		level := []string{db.BoardLevelRead, db.BoardLevelFull}[i%2]
		wg.Go(func() {
			_, _ = db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
				BoardID: "board-a", ProofSessionID: "login-1", CreatedVia: "panel", Level: level})
		})
	}
	wg.Wait()
	var live string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM session_proposal_grants WHERE session_id = $1::uuid AND revoked_at IS NULL`, sid).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if st, err := db.LatestBoardGrant(ctx, pool, sid, "board-a"); err != nil || st == nil || st.Grant.ID != live || !st.Live {
		t.Fatalf("after the race: %+v %v, live row %s", st, err, live)
	}
}

// I3: the CHANGELOG's pre-rollback SQL, run on a migrated database, leaves 0.4.3's reading (every
// live board_file row files without asking) with exactly the boards at file or full.
func TestChangelogRollbackSQLLeavesOnlyFileAndFullFor043(t *testing.T) {
	raw, err := os.ReadFile("../../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	at := strings.Index(text, "#### Rolling back to 0.4.3")
	if at < 0 {
		t.Fatal("CHANGELOG has no rollback section")
	}
	start := strings.Index(text[at:], "```sql\n")
	end := strings.Index(text[at+start+7:], "```")
	if start < 0 || end < 0 {
		t.Fatal("rollback section has no sql block")
	}
	rollback := text[at+start+7 : at+start+7+end]
	pool := reelTestPool(t)
	ctx := context.Background()
	sid := reelSession(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET spawning_account_id = 'acct-1' WHERE id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	for board, level := range map[string]string{"b-propose": db.BoardLevelPropose, "b-read": db.BoardLevelRead, "b-file": db.BoardLevelFile, "b-full": db.BoardLevelFull} {
		if _, err := db.InsertProposalGrant(ctx, pool, db.ProposalGrant{SessionID: sid, AccountID: "acct-1", Kind: db.ProposalGrantBoard,
			BoardID: board, ProofSessionID: "login-1", CreatedVia: "panel", Level: level}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 { // idempotent
		if _, err := pool.Exec(ctx, rollback); err != nil {
			t.Fatalf("rollback sql: %v", err)
		}
	}
	// 0.4.3's LiveProposalGrantFor: any live board_file row of the board, no level.
	rows, err := pool.Query(ctx, `SELECT board_id FROM session_proposal_grants WHERE session_id = $1::uuid AND kind = 'board_file' AND revoked_at IS NULL ORDER BY board_id`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		got = append(got, b)
	}
	if strings.Join(got, ",") != "b-file,b-full" {
		t.Fatalf("0.4.3 would file without asking on %v", got)
	}
}
