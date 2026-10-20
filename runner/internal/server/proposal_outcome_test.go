package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// agentOn connects fx.sess's daemon (input receipts) and returns what is sent to it.
func (fx *topicFx) agentOn() *DaemonConn {
	fx.t.Helper()
	dc := &DaemonConn{ID: topicDaemonID, Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 32)}
	dc.SetInputReceipts(true)
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	return dc
}

func proposalMessages(dc *DaemonConn) []string {
	var out []string
	for _, t := range sentTexts(dc) {
		if strings.HasPrefix(t, "Proposal ") {
			out = append(out, t)
		}
	}
	return out
}

// A person's decision in the chat or on the Proposals page reaches the session's agent as a
// message, the way a review decision does.
func TestApprovedFilingIsDeliveredToTheAgent(t *testing.T) {
	fx, _ := newPermFx(t)
	dc := fx.agentOn()
	p := queueFiling(t, fx)
	rec := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments)})
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	msgs := proposalMessages(dc)
	if len(msgs) != 1 {
		t.Fatalf("agent got %d proposal messages, want 1: %q", len(msgs), msgs)
	}
	for _, want := range []string{"Proposal " + p.ID + " approved: A #7 'Read back' created", "blerg-runner ticket status " + p.ID} {
		if !strings.Contains(msgs[0], want) {
			t.Fatalf("message %q lacks %q", msgs[0], want)
		}
	}
	if strings.Contains(msgs[0], "Private reproduction fixture") {
		t.Fatal("the message repeats the card body")
	}
	// The same decision announced again is not delivered twice.
	fx.api.announceProposalDecision(context.Background(), topicOwner, p.ID)
	if again := proposalMessages(dc); len(again) != 0 {
		t.Fatalf("delivered again: %q", again)
	}
}

func TestRejectedFilingIsDeliveredWithItsNote(t *testing.T) {
	fx, _ := newPermFx(t)
	dc := fx.agentOn()
	p := queueFiling(t, fx)
	rec := filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/reject", fx.token(topicOwner, "human"), map[string]any{"note": "Duplicate of #3"})
	if rec.Code != 200 {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	msgs := proposalMessages(dc)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Proposal "+p.ID+" rejected: Duplicate of #3") || !strings.Contains(msgs[0], "No card was created") {
		t.Fatalf("messages = %q", msgs)
	}
	// Without a note, it still says it was rejected.
	q := func() *db.Proposal {
		_, out := fileOn(t, fx, "B", "Second")
		got, _ := db.GetOwnedProposal(t.Context(), fx.pool, topicOwner, out["proposal_id"].(string))
		return got
	}()
	if rec := filingRequest(fx, "POST", "/api/proposals/"+q.ID+"/reject", fx.token(topicOwner, "human"), nil); rec.Code != 200 {
		t.Fatalf("reject without a note: %d", rec.Code)
	}
	if msgs := proposalMessages(dc); len(msgs) != 1 || !strings.Contains(msgs[0], "Proposal "+q.ID+" rejected") {
		t.Fatalf("messages = %q", msgs)
	}
}

func TestUnconfirmedFilingIsDeliveredAsNotConfirmed(t *testing.T) {
	fx, up := newPermFx(t)
	dc := fx.agentOn()
	p := queueFiling(t, fx)
	up.err = mcpgw.ErrOutcomeUnknown
	filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments)})
	msgs := proposalMessages(dc)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Proposal "+p.ID+" could not be confirmed") || !strings.Contains(msgs[0], "Do not file it again") {
		t.Fatalf("messages = %q", msgs)
	}
}

// A grant's approval is delivered too (the agent may not have read the tool's answer).
func TestGrantApprovalIsDeliveredToTheAgent(t *testing.T) {
	fx, _ := newPermFx(t)
	dc := fx.agentOn()
	grantBoardA(t, fx)
	_, out := fileOn(t, fx, "A", "By grant")
	msgs := proposalMessages(dc)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Proposal "+out["proposal_id"].(string)+" approved") || !strings.Contains(msgs[0], "session permission") {
		t.Fatalf("messages = %q", msgs)
	}
}

// blerg-runner ticket status: the session's own token reads its proposal's outcome; nothing else does.
func TestTicketStatusIsTheSessionsOwn(t *testing.T) {
	fx, _ := newPermFx(t)
	p := queueFiling(t, fx)
	get := func(token, id string) (int, map[string]any) {
		rec := filingRequest(fx, "GET", "/api/sessions/"+fx.sess+"/proposals/"+id, token, nil)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := get(fx.sessTok, p.ID); code != 200 || out["state"] != "pending" || out["proposal_id"] != p.ID || out["kind"] != "board_file" {
		t.Fatalf("pending status: %d %v", code, out)
	}
	filingRequest(fx, "POST", "/api/proposals/"+p.ID+"/approve", fx.token(topicOwner, "human"), map[string]any{"expected_arguments_raw": string(p.Arguments)})
	code, out := get(fx.sessTok, p.ID)
	card, _ := out["card"].(map[string]any)
	if code != 200 || out["state"] != "done" || card["number"] != float64(7) {
		t.Fatalf("done status: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "Private reproduction fixture") {
		t.Fatal("status carries the card body")
	}
	if code, _ := get(fx.otherTok, p.ID); code != 403 && code != 404 {
		t.Fatalf("another session's token read it: %d", code)
	}
	if code, _ := get(fx.sessTok, "00000000-0000-4000-8000-000000000000"); code != 404 {
		t.Fatalf("unknown proposal: %d", code)
	}
}

// The gateway's "queued" answer tells a session's agent that the decision arrives as a message.
func TestQueuedMCPCallSaysTheOutcomeArrivesAsAMessage(t *testing.T) {
	fx := newPropFx(t, 0)
	ctx := context.Background()
	sid := newUUID()
	if err := db.InsertSession(ctx, fx.pool, sid, fx.dc.ID, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id=$2 WHERE id=$1`, sid, mcpAcct); err != nil {
		t.Fatal(err)
	}
	text, err := mcpgw.NewProposalSink(fx.pool, "", 0).Propose(ctx, mcpgw.ProposalRequest{SessionID: sid, AccountID: mcpAcct, ConnectionID: fx.connID,
		ConnectionName: mcpConn, URLSnapshot: fx.up.url(), Tool: "send", ToolHash: fx.up.hash("send"), Arguments: json.RawMessage(`{}`)})
	if err != nil || !strings.Contains(text, "sent to this session as a message") {
		t.Fatalf("queued text = %q %v", text, err)
	}
}
