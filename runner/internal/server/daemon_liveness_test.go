package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"

	"github.com/gorilla/websocket"
)

func TestDaemonLivenessRequiresPong(t *testing.T) {
	for _, respond := range []bool{false, true} {
		name := "missing pong"
		if respond {
			name = "pong renews"
		}
		t.Run(name, func(t *testing.T) {
			accepted := make(chan *websocket.Conn, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				accepted <- conn
			}))
			defer srv.Close()
			peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			conn := <-accepted
			defer conn.Close()
			stop := startDaemonLiveness(conn, 20*time.Millisecond, 200*time.Millisecond)
			defer stop()
			pings := make(chan struct{}, 32)
			peer.SetPingHandler(func(data string) error {
				pings <- struct{}{}
				if respond {
					return peer.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
				}
				return nil
			})
			peerDone := make(chan struct{})
			go func() {
				defer close(peerDone)
				for {
					if _, _, err := peer.ReadMessage(); err != nil {
						return
					}
				}
			}()
			defer func() { peer.Close(); <-peerDone }()
			readDone := make(chan error, 1)
			go func() { _, _, err := conn.ReadMessage(); readDone <- err }()
			defer func() { conn.Close(); <-readDone }()
			// Several leases pass while the data reader waits. The control writer
			// has no application frame or heartbeat dependency.
			for i := 0; i < 25; i++ {
				select {
				case <-pings:
					if !respond {
						continue
					}
				case err := <-readDone:
					readDone <- err
					if respond {
						t.Fatalf("live peer expired: %v", err)
					}
					return
				case <-time.After(2 * time.Second):
					t.Fatal("liveness made no progress")
				}
			}
			if !respond {
				t.Fatal("peer without pongs stayed live")
			}
		})
	}
}

func TestDaemonSocketCloseDiagnosticIsPrivate(t *testing.T) {
	var out bytes.Buffer
	old := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&out, nil)))
	defer func() { failurelog.Default = old }()
	logDaemonSocketFailure("read", &websocket.CloseError{Code: 1006, Text: "CANARY_SECRET"})
	logDaemonSocketFailure("limit", websocket.ErrReadLimit)
	for _, want := range []string{`"close_code":1006`, `"cause":"websocket_close"`, `"cause":"read_limit"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal("private close text disclosed")
	}
}
