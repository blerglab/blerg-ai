//go:build linux

package shellkeeper

import (
	"context"
	"net"
	"time"
)

// NotifyReady tells systemd that the authenticated listener is ready, so the
// daemon's After= dependency cannot race socket creation. An empty address is
// the standalone CLI mode; the service manager supplies NOTIFY_SOCKET itself.
func NotifyReady(ctx context.Context, address string) error {
	if address == "" {
		return nil
	}
	dial := net.Dialer{Timeout: time.Second}
	conn, err := dial.DialContext(ctx, "unixgram", address)
	if err != nil {
		return refusal("ready_connect", err)
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return refusal("ready_deadline", err)
	}
	if _, err = conn.Write([]byte("READY=1")); err != nil {
		return refusal("ready_write", err)
	}
	return nil
}
