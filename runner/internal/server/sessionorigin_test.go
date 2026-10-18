package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestSessionOriginBrokerRejectsForeignSupersededAndWrongSessionReplies(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	done := make(chan error, 1)
	go func() { _, err := h.requestSessionOrigin(t.Context(), dc, "session"); done <- err }()
	var req protocol.ReadSessionOrigin
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	reply := protocol.SessionOriginResult{RequestID: req.RequestID, SessionID: "session", OK: true, Commit: strings.Repeat("a", 40)}
	h.deliverSessionOriginResult(&DaemonConn{ID: "foreign"}, reply)
	wrong := reply
	wrong.SessionID = "other"
	h.deliverSessionOriginResult(dc, wrong)
	select {
	case <-done:
		t.Fatal("foreign reply accepted")
	default:
	}
	h.deliverSessionOriginResult(dc, reply)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.deliverSessionOriginResult(dc, reply)
	go func() { _, err := h.requestSessionOrigin(t.Context(), dc, "session"); done <- err }()
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	next := &DaemonConn{ID: dc.ID, send: make(chan []byte, 8)}
	h.Register(next)
	reply.RequestID = req.RequestID
	h.deliverSessionOriginResult(dc, reply)
	h.abandonSessionOriginRequests(dc)
	if err := <-done; err == nil {
		t.Fatal("superseded reply accepted")
	}
}
func TestSessionOriginBrokerOldPeerTimeoutAndReplyBounds(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if _, err := h.requestSessionOrigin(ctx, dc, "session"); err == nil {
		t.Fatal("old peer hung or succeeded")
	}
	h.sessionOriginReqs.mu.Lock()
	n := len(h.sessionOriginReqs.pending)
	h.sessionOriginReqs.mu.Unlock()
	if n != 0 {
		t.Fatal("timed-out waiter leaked")
	}
	for _, bad := range []string{"bad", strings.Repeat("A", 40), strings.Repeat("a", 101)} {
		if protocol.ValidSessionOriginCommit(bad) {
			t.Fatal("invalid git output accepted")
		}
	}
}

func TestSessionOriginBrokerBoundsConcurrentSessionRequestsAndRejectsInvalidSHA(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	done := make(chan error, 1)
	go func() { _, err := h.requestSessionOrigin(t.Context(), dc, "session"); done <- err }()
	var req protocol.ReadSessionOrigin
	if err := json.Unmarshal(<-dc.send, &req); err != nil {
		t.Fatal(err)
	}
	if _, err := h.requestSessionOrigin(t.Context(), dc, "session"); err == nil {
		t.Fatal("duplicate session waiter admitted")
	}
	h.deliverSessionOriginResult(dc, protocol.SessionOriginResult{RequestID: req.RequestID, SessionID: "session", OK: true, Commit: strings.Repeat("a", 101)})
	if err := <-done; err == nil {
		t.Fatal("malformed native commit accepted")
	}
}

func TestSessionOriginLookupRechecksDatabaseOwnershipAfterReply(t *testing.T) {
	fx := newTopicFx(t)
	if err := db.UpsertDaemon(t.Context(), fx.pool, "00000000-0000-4000-8000-000000000099", "other", "local", "/other"); err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: topicDaemonID, send: make(chan []byte, 8)}
	fx.api.hub.Register(dc)
	fx.api.hub.SetSessionOwner(fx.sess, dc.ID)
	lookup := func(changeOwner bool) (string, error) {
		type response struct {
			commit string
			err    error
		}
		done := make(chan response, 1)
		go func() { commit, err := fx.api.readSessionOrigin(t.Context(), fx.sess); done <- response{commit, err} }()
		var req protocol.ReadSessionOrigin
		select {
		case raw := <-dc.send:
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("origin request not sent")
		}
		if changeOwner {
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id='00000000-0000-4000-8000-000000000099' WHERE id=$1`, fx.sess); err != nil {
				t.Fatal(err)
			}
		}
		fx.api.hub.deliverSessionOriginResult(dc, protocol.SessionOriginResult{Type: "session_origin_result", RequestID: req.RequestID, SessionID: fx.sess, OK: true, Commit: strings.Repeat("a", 40)})
		result := <-done
		return result.commit, result.err
	}
	if commit, err := lookup(false); err != nil || commit != strings.Repeat("a", 40) {
		t.Fatal("live workspace origin unavailable")
	}
	if _, err := lookup(true); err == nil {
		t.Fatal("foreign/reassigned database workspace accepted")
	}
}

func TestSessionOriginBrokerBoundsPerDaemonAndGlobally(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		daemonCount, perDaemon, want int
	}{
		{"per-daemon", 1, 4, 4}, {"global", 4, 4, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHub()
			var daemons []*DaemonConn
			for d := 0; d < tc.daemonCount; d++ {
				dc := &DaemonConn{ID: fmt.Sprintf("daemon-%d", d), send: make(chan []byte, 8)}
				h.Register(dc)
				daemons = append(daemons, dc)
				for n := 0; n < tc.perDaemon; n++ {
					id := fmt.Sprintf("pending-%d-%d", d, n)
					h.sessionOriginReqs.mu.Lock()
					if h.sessionOriginReqs.pending == nil {
						h.sessionOriginReqs.pending = map[string]sessionOriginWaiter{}
					}
					h.sessionOriginReqs.pending[id] = sessionOriginWaiter{dc: dc, sessionID: id, ch: make(chan protocol.SessionOriginResult, 1)}
					h.sessionOriginReqs.mu.Unlock()
				}
			}
			target := daemons[0]
			if tc.daemonCount > 1 {
				target = &DaemonConn{ID: "extra", send: make(chan []byte, 8)}
				h.Register(target)
			}
			h.SetSessionOwner("new-session", target.ID)
			if _, err := h.requestSessionOrigin(t.Context(), target, "new-session"); err == nil {
				t.Fatal("waiter limit bypassed")
			}
			if got := len(h.sessionOriginReqs.pending); got != tc.want {
				t.Fatalf("waiters = %d, want %d", got, tc.want)
			}
			select {
			case <-target.send:
				t.Fatal("over-limit request reached daemon")
			default:
			}
		})
	}
}
