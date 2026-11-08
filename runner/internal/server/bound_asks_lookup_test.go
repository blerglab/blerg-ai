package server

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBoundLookupFailureLeavesChatToNormalRouting(t *testing.T) {
	fx := newTopicFx(t)
	ctx := context.Background()
	if _, err := fx.pool.Exec(ctx, `ALTER TABLE bound_asks RENAME TO unavailable_bound_asks`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := fx.pool.Exec(ctx, `ALTER TABLE unavailable_bound_asks RENAME TO bound_asks`); err != nil {
			t.Error(err)
		}
	}()
	if answerAskFrom(ctx, fx.api.hub, fx.pool, fx.sess, "ordinary words", "lookup-chat", "", topicOwner, "chat") {
		t.Fatal("failed bound lookup consumed ordinary conversation")
	}
	p, ok := fx.api.verifyBrowserToken(fx.token(topicOwner, "human"))
	if !ok {
		t.Fatal("fixture login")
	}
	handleBrowserAgentMessage(ctx, fx.api.hub, &BrowserConn{AccountID: p.Sub, SessionID: p.Sid, verified: p}, fx.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: fx.sess, Text: "ordinary words", ClientID: "lookup-chat"})
	var count int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND client_id='lookup-chat' AND frame->>'text'='ordinary words' AND coalesce(frame->>'answer_message_id','')=''`, fx.sess).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversation outbox: %d %v", count, err)
	}
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='input_accepted' AND payload->>'text'='ordinary words'`, fx.sess).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversation acceptance: %d %v", count, err)
	}
}
