package server

import (
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/gorilla/websocket"
)

const daemonPingInterval = 20 * time.Second
const daemonPongLease = 90 * time.Second

// Control frames bypass all application queues. Gorilla permits WriteControl
// concurrently with the data writer; its own bound detects a blocked transport.
// Install before starting the sole reader, which owns the pong handler.
func startDaemonLiveness(conn *websocket.Conn, interval, lease time.Duration, daemonID ...string) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	renew := func() error { return conn.SetReadDeadline(time.Now().Add(lease)) }
	if err := renew(); err != nil {
		logDaemonSocketFailure("liveness_deadline", err, daemonID...)
	}
	conn.SetPongHandler(func(string) error { return renew() })
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					logDaemonSocketFailure("liveness_ping", err, daemonID...)
					closeDaemonSocket(conn, "liveness_ping", err, daemonID...)
					return
				}
			}
		}
	}()
	return func() { close(done); <-stopped }
}

func logDaemonSocketFailure(stage string, err error, daemonID ...string) {
	cause := failurelog.Cause(err)
	code := 0
	var closed *websocket.CloseError
	var network net.Error
	switch {
	case strings.Contains(stage, "authority") && daemonAuthorityTimeout(err):
		cause = "authority_timeout"
	case errors.Is(err, websocket.ErrReadLimit):
		cause = "read_limit"
	case errors.As(err, &closed):
		cause, code = "websocket_close", closed.Code
	case errors.As(err, &network) && network.Timeout():
		cause = "socket_deadline"
	}
	id := ""
	if len(daemonID) > 0 {
		id = daemonID[0]
	}
	failurelog.Default.Event("daemon_socket:"+stage+":"+cause+":"+id, slog.LevelWarn, "component", "server", "stage", stage, "cause", cause, "close_code", code, "daemon_id", id)
}

// Close labels are fixed at call sites; peer text and raw errors stay private.
func closeDaemonSocket(conn *websocket.Conn, reason string, err error, daemonID ...string) {
	logDaemonSocketFailure("close_"+reason, err, daemonID...)
	_ = conn.Close()
}
