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
// a blocked agent writer, nor deliver its old reply over a replacement socket.
func (c *WSClient) sendDiscoveryReply(ctx context.Context, conn *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("discovery reply unavailable")
	}
	for !c.writeMu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer c.writeMu.Unlock()
	c.mu.Lock()
	current := c.conn
	c.mu.Unlock()
	if current != conn || ctx.Err() != nil {
		return errors.New("discovery connection changed")
	}
	deadline := time.Now().Add(time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	return conn.WriteMessage(websocket.TextMessage, raw)
}
