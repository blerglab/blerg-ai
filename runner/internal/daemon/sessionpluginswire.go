package daemon

import (
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
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
		return errors.New("plugin result transport is busy")
	}
	c.writeMu.Unlock()
	// Retain the plugin API's immediate busy refusal, but deliver through the
	// same connection scheduler rather than holding a second socket writer.
	return c.sendBoundedReplyTo(result, timeout, 20*time.Millisecond, time.Time{}, conn)
}
