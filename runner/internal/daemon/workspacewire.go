package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// File replies share the connection's bounded ordinary lane with agent traffic.
// Shell controls have priority, and shell/file lanes each receive fair turns.
func (c *WSClient) SendWorkspaceFileResult(result protocol.WorkspaceFileResult) error {
	return c.SendWorkspaceFileResultBy(result, time.Time{})
}

// SendWorkspaceFileResultBy is SendWorkspaceFileResult for a reply the server stops
// waiting for at notAfter: a reply that could only start after that is not sent
// (a large frame the server would read and throw away).
func (c *WSClient) SendWorkspaceFileResultBy(result protocol.WorkspaceFileResult, notAfter time.Time) error {
	if notAfter.IsZero() {
		notAfter = time.Now().Add(workspaceReplyTTL)
	}
	return c.SendWorkspaceFileResultContext(context.Background(), result, notAfter)
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
func (c *WSClient) lockWriterWithin(ctx context.Context, wait time.Duration, notAfter time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	if c.writeMu.TryLock() {
		return true
	}
	until := time.Now().Add(wait)
	if !notAfter.IsZero() && until.After(notAfter) {
		until = notAfter
	}
	for time.Now().Before(until) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
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

type fairWriteKey struct{}
type fairWriteOptions struct {
	connection        *websocket.Conn
	startBy, notAfter time.Time
	timeout           time.Duration
	wait              time.Duration
}

// Queue time and actual write time have separate bounds. The reply stays bound
// to the original socket, and its TTL applies both before and during delivery.
func (c *WSClient) sendBoundedReply(result any, timeout, wait time.Duration, notAfter time.Time) error {
	return c.sendBoundedReplyTo(result, timeout, wait, notAfter, c.currentConn())
}
func (c *WSClient) sendBoundedReplyTo(result any, timeout, wait time.Duration, notAfter time.Time, conn *websocket.Conn) error {
	return c.sendBoundedReplyContext(context.Background(), conn, result, timeout, wait, notAfter)
}
func (c *WSClient) sendBoundedReplyContext(parent context.Context, conn *websocket.Conn, result any, timeout, wait time.Duration, notAfter time.Time) error {
	if conn == nil {
		return errors.New("workspace file transport is disconnected")
	}
	if !notAfter.IsZero() && !time.Now().Before(notAfter) {
		return errors.New("workspace file reply expired")
	}
	if wait <= 0 {
		wait = workspaceWriterWait
	}
	timeout = min(timeout, time.Second)
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	options := fairWriteOptions{connection: conn, startBy: time.Now().Add(wait), notAfter: notAfter, timeout: timeout, wait: wait}
	ctx, cancel := context.WithTimeout(parent, wait+timeout)
	defer cancel()
	if !notAfter.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, notAfter)
		defer stop()
	}
	return c.sendFair(context.WithValue(ctx, fairWriteKey{}, options), raw)
}
