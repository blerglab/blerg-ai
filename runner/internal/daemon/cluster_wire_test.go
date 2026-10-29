package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWorkspaceClusterReplyCanceledBehindAgentWriter(t *testing.T) {
	client := NewWSClient(Config{})
	client.conn = &websocket.Conn{}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := client.SendBounded(ctx, map[string]string{"type": "owner_shell_result"}); !errors.Is(err, context.Canceled) {
		t.Fatal("old-link reply was not canceled", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("canceled reply blocked control pump")
	}
}
