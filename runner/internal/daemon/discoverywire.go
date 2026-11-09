package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gorilla/websocket"
)

func (c *WSClient) discoveryLink() (*websocket.Conn, context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn, c.discoveryCtx
}

// A timed-out discovery cannot strand one of the bounded worker slots behind
// a blocked writer, nor deliver its old reply over a replacement socket.
func (c *WSClient) sendDiscoveryReply(ctx context.Context, conn *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("discovery reply unavailable")
	}
	return c.sendBoundedFrame(ctx, conn, raw, time.Second, 0, time.Time{})
}
