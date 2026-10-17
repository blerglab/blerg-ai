package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBackgroundStaleHeartbeatCannotRewriteReposRoot(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	old := &DaemonConn{ID: topicDaemonID, Name: "fixture", Mode: "local", ReposRoot: "/old-fixture", send: make(chan []byte, 8)}
	h.Register(old)
	current := &DaemonConn{ID: topicDaemonID, Name: "fixture", Mode: "local", ReposRoot: "/current-fixture", send: make(chan []byte, 8)}
	h.Register(current)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE daemons SET repos_root=$2 WHERE id=$1`, topicDaemonID, current.ReposRoot); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(protocol.DaemonHeartbeat{Type: "daemon_heartbeat", ReposRoot: "/stale-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if !handleDaemonFrame(t.Context(), h, old, fx.pool, raw, envelope{Type: "daemon_heartbeat"}) {
		t.Fatal("stale heartbeat unexpectedly terminated the connection")
	}
	var got string
	if err := fx.pool.QueryRow(t.Context(), `SELECT repos_root FROM daemons WHERE id=$1`, topicDaemonID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != current.ReposRoot || old.CurrentReposRoot() != "/old-fixture" {
		t.Fatalf("stale heartbeat modified repos root: database=%q old socket=%q", got, old.CurrentReposRoot())
	}
}

// Follow-up work runs outside the work fence, so dispatch must revalidate the
// socket. A failed stale delivery must preserve pending input and revoke the
// undelivered token, leaving the replacement able to retry both.
func TestBackgroundUnlockedFollowupsDoNotSendToSupersededSocket(t *testing.T) {
	f := newIngestFixture(t)
	f.dc.sessionTokenFile = true
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID, Text: "Continue", ClientID: "replacement-input", Source: "chat"}
	if fresh, err := acceptSessionInput(f.ctx, f.hub, f.pool, msg); err != nil || !fresh {
		t.Fatalf("accept input: fresh=%v err=%v", fresh, err)
	}
	replacement := &DaemonConn{ID: f.dc.ID, inputReceipts: true, sessionTokenFile: true, send: make(chan []byte, 8)}
	f.hub.Register(replacement)
	healSessionTokens(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	if len(f.dc.send) != 0 {
		t.Fatalf("superseded socket received %d follow-up frames", len(f.dc.send))
	}
	if liveTokens(t, f.pool, f.sessionID) != 0 {
		t.Fatal("undelivered token remained live after stale delivery was refused")
	}
	if !f.hub.tokenHeals.claim(f.sessionID) {
		t.Fatal("stale token heal blocked replacement retry")
	}
	f.hub.tokenHeals.release(f.sessionID)
	replayPendingSessionInputs(f.ctx, f.hub, f.pool, replacement, []string{f.sessionID})
	if len(replacement.send) != 1 {
		t.Fatalf("replacement received %d input frames, want 1", len(replacement.send))
	}
	var got protocol.AgentUserMessage
	if err := json.Unmarshal(<-replacement.send, &got); err != nil || got.ClientID != msg.ClientID {
		t.Fatalf("replacement input mismatch: client=%q err=%v", got.ClientID, err)
	}
}
