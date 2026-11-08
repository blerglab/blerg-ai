package daemon

import (
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// SendSessionPluginsResult waits only a moment for the writer and never pins the
// driver on a wedged socket. The server retries approved request IDs after reconnect;
// the daemon's terminal result cache makes that retry safe.
func (c *WSClient) SendSessionPluginsResult(result protocol.SessionPluginsResult) error {
	return c.sendSessionPluginsResult(result, 2*time.Second)
}

func (c *WSClient) sendSessionPluginsResult(result protocol.SessionPluginsResult, timeout time.Duration) error {
	conn := c.currentConn()
	if conn == nil {
		return errors.New("plugin result transport is disconnected")
	}
	return c.sendBoundedReplyTo(result, timeout, 20*time.Millisecond, time.Time{}, conn)
}
