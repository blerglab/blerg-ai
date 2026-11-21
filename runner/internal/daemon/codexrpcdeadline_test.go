package daemon

import (
	"context"
	"testing"
	"time"
)

func TestCodexRPCUsesAppDeadline(t *testing.T) {
	app := &codexApp{ctx: context.Background(), in: nopCodexWriter{}, pending: map[int]chan codexRPC{}, done: make(chan struct{}), rpcTimeout: time.Nanosecond}
	if _, err := app.call("fixture", nil); err == nil || err.Error() != "fixture timed out" {
		t.Fatalf("app deadline did not bound its RPC: %v", err)
	}
}
