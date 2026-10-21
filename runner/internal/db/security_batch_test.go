package db_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestConcurrentIdenticalFilingsReturnOneProposal(t *testing.T) {
	pool := proposalTestPool(t)
	sid := reelSession(t, pool)
	if err := db.SetSessionSpawningAccount(t.Context(), pool, sid, "filing-owner"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		wg.Go(func() {
			<-start
			p, err := db.InsertProposalLimited(t.Context(), pool, db.Proposal{AccountID: "filing-owner", SessionID: sid, ConnectionID: db.BoardFilingConnectionID, ConnectionName: "board-file", Tool: "file_ticket", ToolHash: "board-file-v1", URLSnapshot: "https://board.example.com/mcp", Arguments: json.RawMessage(`{"board_id":"board-a","title":"One ticket","body":"Details","sensitive":false}`)}, db.ProposalLimits{Account: 50})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.ID
		})
	}
	close(start)
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("identical simultaneous filings created %d proposals", len(unique))
	}
}

func TestConcurrentGrantApprovalsRespectHourlyCap(t *testing.T) {
	pool := proposalTestPool(t)
	sid := reelSession(t, pool)
	if err := db.SetSessionSpawningAccount(t.Context(), pool, sid, "approval-owner"); err != nil {
		t.Fatal(err)
	}
	g, err := db.InsertProposalGrant(t.Context(), pool, db.ProposalGrant{SessionID: sid, AccountID: "approval-owner", Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: "login", CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	proposals := make([]*db.Proposal, 25)
	for i := range proposals {
		proposals[i], err = db.InsertProposalLimited(t.Context(), pool, db.Proposal{AccountID: g.AccountID, SessionID: sid, ConnectionID: db.BoardFilingConnectionID, ConnectionName: "board-file", Tool: "file_ticket", ToolHash: "board-file-v1", URLSnapshot: "https://board.example.com/mcp", Arguments: json.RawMessage(fmt.Sprintf(`{"board_id":"board-a","title":"Ticket %d"}`, i))}, db.ProposalLimits{Account: 50})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, len(proposals))
	start := make(chan struct{})
	for _, p := range proposals {
		wg.Go(func() {
			<-start
			_, err := db.BeginBoardFilingExecutionByGrant(t.Context(), pool, g.AccountID, p.ID, g.AccountID, string(p.Arguments), g.ID)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	approved := 0
	for err := range results {
		if err == nil {
			approved++
		} else if !errors.Is(err, db.ErrProposalGrantCap) {
			t.Errorf("unexpected approval error: %v", err)
		}
	}
	if approved != 20 {
		t.Fatalf("burst approved=%d, want hourly cap 20", approved)
	}
}

func TestReadOnlyGrantCannotApproveFiling(t *testing.T) {
	pool := proposalTestPool(t)
	sid := reelSession(t, pool)
	if err := db.SetSessionSpawningAccount(t.Context(), pool, sid, "read-owner"); err != nil {
		t.Fatal(err)
	}
	g, err := db.InsertProposalGrant(t.Context(), pool, db.ProposalGrant{SessionID: sid, AccountID: "read-owner", Kind: db.ProposalGrantBoard, BoardID: "board-a", Level: db.BoardLevelRead, ProofSessionID: "login", CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProposalLimited(t.Context(), pool, db.Proposal{AccountID: g.AccountID, SessionID: sid, ConnectionID: db.BoardFilingConnectionID, ConnectionName: "board-file", Tool: "file_ticket", ToolHash: "board-file-v1", URLSnapshot: "https://board.example.com/mcp", Arguments: json.RawMessage(`{"board_id":"board-a","title":"Pending"}`)}, db.ProposalLimits{Account: 50})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginBoardFilingExecutionByGrant(t.Context(), pool, g.AccountID, p.ID, g.AccountID, string(p.Arguments), g.ID); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("read-only approval=%v", err)
	}
}

// An approval through a grant whose row is gone (its session deleted, say) is a conflict like
// any lost compare-and-set, not an internal error (review M6).
func TestApprovalThroughDeletedGrantIsAStateConflict(t *testing.T) {
	pool := proposalTestPool(t)
	sid := reelSession(t, pool)
	if err := db.SetSessionSpawningAccount(t.Context(), pool, sid, "gone-owner"); err != nil {
		t.Fatal(err)
	}
	g, err := db.InsertProposalGrant(t.Context(), pool, db.ProposalGrant{SessionID: sid, AccountID: "gone-owner", Kind: db.ProposalGrantBoard, BoardID: "board-a", ProofSessionID: "login", CreatedVia: "panel"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProposalLimited(t.Context(), pool, db.Proposal{AccountID: g.AccountID, SessionID: sid, ConnectionID: db.BoardFilingConnectionID, ConnectionName: "board-file", Tool: "file_ticket", ToolHash: "board-file-v1", URLSnapshot: "https://board.example.com/mcp", Arguments: json.RawMessage(`{"board_id":"board-a","title":"Gone"}`)}, db.ProposalLimits{Account: 50})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM session_proposal_grants WHERE id = $1::uuid`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginProposalExecutionByGrant(t.Context(), pool, g.AccountID, p.ID, g.AccountID, g.ID); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("approval through a deleted grant: %v, want ErrProposalState", err)
	}
}
