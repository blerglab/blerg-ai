package daemon

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// SendWorkspaceFileResult bounds this new large-response path without changing
// ordinary event writes. A connection that stays busy lets the server expire its
// request; waiting long behind an unrelated blocked write must not pin another
// large buffer. A writer that is busy for a moment (another file reply, an event
// going out) is waited for, up to workspaceWriterWait: dropping the reply then
// cost the person the server's whole timeout and, in a README, an image that
// never loaded.
//
// The wait is a TryLock poll, so it is not fair: while the writer is saturated
// (a big file frame going out with events queued behind it on sync.Mutex, which
// then hands the lock to queued Lock callers) the reply still loses and is
// dropped after the wait. That fairness is the right way round for control
// traffic, and the page reads a dropped reply again (its "daemon_timeout"). A
// single writer with a file-reply queue is the follow-up design.
func (c *WSClient) SendWorkspaceFileResult(result protocol.WorkspaceFileResult) error {
	return c.SendWorkspaceFileResultBy(result, time.Time{})
}

// SendWorkspaceFileResultBy is SendWorkspaceFileResult for a reply the server stops
// waiting for at notAfter: a reply that could only start after that is not sent
// (a large frame the server would read and throw away).
func (c *WSClient) SendWorkspaceFileResultBy(result protocol.WorkspaceFileResult, notAfter time.Time) error {
	return c.sendBoundedReply(result, 10*time.Second, workspaceWriterWait, notAfter)
}
func (c *WSClient) sendWorkspaceFileResult(result protocol.WorkspaceFileResult, timeout time.Duration) error {
	return c.sendBoundedReply(result, timeout, workspaceWriterWait, time.Time{})
}

// workspaceWriterWait is how long a file reply waits for a busy writer before it gives up.
const workspaceWriterWait = 2 * time.Second

// workspaceReplyTTL is how long after the daemon receives a read request its reply is still
// worth sending: the server stops waiting 10 s after it sent the request.
const workspaceReplyTTL = 9 * time.Second

func (c *WSClient) sendBoundedWorkspaceReply(result any, timeout time.Duration) error {
	return c.sendBoundedReply(result, timeout, 0, time.Time{})
}

// lockWriterWithin takes the write lock, trying until `wait` has passed (never past notAfter).
func (c *WSClient) lockWriterWithin(wait time.Duration, notAfter time.Time) bool {
	if c.writeMu.TryLock() {
		return true
	}
	until := time.Now().Add(wait)
	if !notAfter.IsZero() && until.After(notAfter) {
		until = notAfter
	}
	for time.Now().Before(until) {
		time.Sleep(5 * time.Millisecond)
		if c.writeMu.TryLock() {
			return true
		}
	}
	return false
}

func (c *WSClient) currentConn() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// sendBoundedReply writes one reply frame: it waits for the writer up to `wait`, gives up
// when the reply is past notAfter (if set), writes only on the connection it started
// with, and gives the write itself the whole `timeout`, counted once it holds the writer.
func (c *WSClient) sendBoundedReply(result any, timeout, wait time.Duration, notAfter time.Time) error {
	conn := c.currentConn()
	if conn == nil {
		return errors.New("workspace file transport is disconnected")
	}
	if !c.lockWriterWithin(wait, notAfter) {
		return errors.New("workspace file transport is busy")
	}
	defer c.writeMu.Unlock()
	if !notAfter.IsZero() && time.Now().After(notAfter) {
		return errors.New("workspace file reply expired: the server stopped waiting")
	}
	// A reconnect during the wait: the server bound the request to the old connection,
	// and a half-dead old socket must not hold the writer the new hello needs.
	if c.currentConn() != conn {
		return errors.New("workspace file transport changed")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	if err = conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		// A timed-out gorilla writer is no longer usable. Closing also wakes
		// the normal read loop so it can reconnect instead of blocking control.
		_ = conn.Close()
	}
	return err
}
