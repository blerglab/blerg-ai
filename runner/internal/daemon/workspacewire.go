package daemon

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// SendWorkspaceFileResult bounds this new large-response path without changing
// ordinary event writes. A busy connection lets the server expire its request;
// waiting behind an unrelated blocked write must not pin another large buffer.
func (c *WSClient) SendWorkspaceFileResult(result protocol.WorkspaceFileResult) error {
	return c.sendWorkspaceFileResult(result, 10*time.Second)
}
func (c *WSClient) sendWorkspaceFileResult(result protocol.WorkspaceFileResult, timeout time.Duration) error {
	return c.sendBoundedWorkspaceReply(result, timeout)
}
func (c *WSClient) sendBoundedWorkspaceReply(result any, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("workspace file transport is disconnected")
	}
	if !c.writeMu.TryLock() {
		return errors.New("workspace file transport is busy")
	}
	defer c.writeMu.Unlock()
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = conn.SetWriteDeadline(deadline); err != nil {
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
