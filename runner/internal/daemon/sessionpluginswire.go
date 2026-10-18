package daemon

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// SendSessionPluginsResult never waits behind another writer or pins the driver
// on a wedged socket. The server retries approved request IDs after reconnect;
// the daemon's terminal result cache makes that retry safe.
func (c *WSClient) SendSessionPluginsResult(result protocol.SessionPluginsResult) error {
	return c.sendSessionPluginsResult(result, 2*time.Second)
}

func (c *WSClient) sendSessionPluginsResult(result protocol.SessionPluginsResult, timeout time.Duration) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("plugin result transport is disconnected")
	}
	if !c.writeMu.TryLock() {
		_ = conn.Close()
		return errors.New("plugin result transport is busy")
	}
	defer c.writeMu.Unlock()
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	if err = conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		_ = conn.Close()
	}
	return err
}
