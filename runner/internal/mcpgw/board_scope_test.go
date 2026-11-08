package mcpgw

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"testing"
	"time"
)

func TestCallBoardReadScope250(t *testing.T) {
	b := newBoardHarness(t, nil)
	ctx, finishRead := WithBoardReadScope(context.Background())
	c := BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "login-1"}, BoardID: boardTestID, SessionRef: "s-1", Tool: "blerg_card_get", ReadOnly: true}
	for range 250 {
		if _, err := b.gw.CallBoard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if b.ex.exchanges() != 1 || len(b.ex.revokedList()) != 0 {
		t.Fatalf("mints=%d revokes=%d", b.ex.exchanges(), len(b.ex.revokedList()))
	}
	finishRead()
	finishRead()
	if len(b.ex.revokedList()) != 1 {
		t.Fatalf("revokes=%d", len(b.ex.revokedList()))
	}
	if _, err := b.gw.CallBoard(ctx, c); err == nil {
		t.Fatal("closed scope accepted")
	}
}

func TestCallBoardReadCacheAuthorityAndExpiry(t *testing.T) {
	b := newBoardHarness(t, nil)
	c := BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "login-1"}, BoardID: boardTestID, SessionRef: "s-1", Tool: "blerg_card_get", ReadOnly: true}
	call := func(c BoardCall) {
		t.Helper()
		ctx, finishRead := WithBoardReadScope(context.Background())
		defer finishRead()
		if _, err := b.gw.CallBoard(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() int { _, _, n := b.up.snapshot(); return n }
	call(c)
	call(c)
	if calls() != 1 || b.ex.exchanges() != 2 || len(b.ex.revokedList()) != 2 {
		t.Fatalf("cache calls=%d exchanges=%d revokes=%d", calls(), b.ex.exchanges(), len(b.ex.revokedList()))
	}
	other := c
	other.Proof.SessionID = "login-2"
	call(other)
	other = c
	other.SessionRef = "s-2"
	call(other)
	other = c
	other.BoardID = "other-board"
	call(other)
	other = c
	other.Proof.AccountID = "acct-2"
	call(other)
	if calls() != 5 {
		t.Fatalf("authority boundary calls=%d", calls())
	}
	b.clock.Advance(3 * time.Second)
	call(c)
	if calls() != 6 {
		t.Fatal("expired cache served")
	}
	// A fresh proof exchange must still succeed even when data is cached.
	b.ex.err = ErrConnectionGone
	ctx, finishRead := WithBoardReadScope(context.Background())
	defer finishRead()
	if _, err := b.gw.CallBoard(ctx, c); err == nil {
		t.Fatal("cached data bypassed live proof")
	}
}

type scopedLoadBoard struct{ gateway *Gateway }

func (b scopedLoadBoard) Call(ctx context.Context, tool string, args map[string]any, out any) error {
	rawArgs, _ := json.Marshal(args)
	raw, err := b.gateway.CallBoard(ctx, BoardCall{Proof: Proof{AccountID: "acct-1", SessionID: "login-1"}, BoardID: args["board_id"].(string), SessionRef: "s-1", Tool: tool, Arguments: rawArgs, ReadOnly: true})
	if err != nil {
		return err
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return err
	}
	return json.Unmarshal([]byte(result.Content[0].Text), out)
}
func TestBlackboard250LoadCredentialCounts(t *testing.T) {
	b := newBoardHarness(t, nil)
	b.up.callBody = func(tool string, args json.RawMessage) any {
		var value any = []blackboard.Column{}
		if tool == "blerg_card_search" {
			var input struct {
				Numbers []int `json:"numbers"`
			}
			_ = json.Unmarshal(args, &input)
			page := blackboard.CardPage{Cards: []blackboard.Card{}}
			for _, n := range input.Numbers {
				page.Cards = append(page.Cards, blackboard.Card{BoardID: boardTestID, Number: n, Version: 1})
			}
			value = page
		}
		raw, _ := json.Marshal(value)
		return map[string]any{"content": []map[string]string{{"type": "text", "text": string(raw)}}}
	}
	store := blackboard.NewMemoryStore()
	record := blackboard.Record{ID: "b", Version: 1, Owner: blackboard.Actor{ID: "acct-1", Kind: "human"}, Milestones: []blackboard.Milestone{{ID: "m", BoardID: boardTestID, Version: 1}}}
	for n := 1; n <= 250; n++ {
		record.Items = append(record.Items, blackboard.Item{ID: blackboard.ItemID(fmt.Sprint(n)), MilestoneID: "m", BoardID: boardTestID, CardNumber: n, State: "planned"})
	}
	if err := store.Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	s := blackboard.Service{Store: store, BlackboardID: "b", Board: scopedLoadBoard{b.gw}, ReadScope: WithBoardReadScope}
	v, err := s.Slice(t.Context(), "m")
	_, _, calls := b.up.snapshot()
	if err != nil || len(v.Cards) != 250 || calls != 4 || b.ex.exchanges() != 1 || len(b.ex.revokedList()) != 1 {
		t.Fatalf("cards=%d calls=%d mints=%d revokes=%d err=%v", len(v.Cards), calls, b.ex.exchanges(), len(b.ex.revokedList()), err)
	}
}
