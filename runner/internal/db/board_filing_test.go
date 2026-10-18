package db_test

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestBoardFilingDraftCompareAndSet(t *testing.T) {
	pool := proposalTestPool(t)
	sid := "11111111-2222-4333-8444-555555555555"
	if err := db.UpsertDaemon(t.Context(), pool, "22222222-3333-4444-8555-666666666666", "fixture", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(t.Context(), pool, sid, "22222222-3333-4444-8555-666666666666", "running", "/repos/app", "app", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(t.Context(), pool, sid, "fixture-owner"); err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProposalLimited(t.Context(), pool, db.Proposal{SessionID: sid, AccountID: "fixture-owner", ConnectionID: "00000000-0000-4000-8000-00000000b0f1", ConnectionName: "board-file", URLSnapshot: "https://board.example.test", Tool: "file_ticket", ToolHash: "board-file-v1", Arguments: json.RawMessage(`{"title":"Original"}`)}, db.ProposalLimits{Account: 50})
	if err != nil {
		t.Fatal(err)
	}
	old := string(p.Arguments)
	edited, err := db.EditBoardFilingProposal(t.Context(), pool, p.AccountID, p.ID, old, json.RawMessage(`{"title":"Edited"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginBoardFilingExecution(t.Context(), pool, p.AccountID, p.ID, p.AccountID, old); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("stale approval=%v", err)
	}
	if _, err := db.EditBoardFilingProposal(t.Context(), pool, p.AccountID, p.ID, old, json.RawMessage(`{"title":"Lost update"}`)); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("stale edit=%v", err)
	}
	if _, err := db.BeginBoardFilingExecution(t.Context(), pool, p.AccountID, p.ID, p.AccountID, string(edited.Arguments)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EditBoardFilingProposal(t.Context(), pool, p.AccountID, p.ID, string(edited.Arguments), json.RawMessage(`{"title":"After approval"}`)); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("post approval edit=%v", err)
	}
}
