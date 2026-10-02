package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// The daemon socket stores the hello's mcp_gateway capability in the server's daemon record,
// and an old daemon (a hello without it) is recorded as unable to deliver a grant.
func TestDaemonHelloRecordsTheMCPGatewayCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  bool
	}{{"new daemon", true}, {"old daemon", false}} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub()
			srv := httptest.NewServer(hub.ServeDaemon("tok-1234567890", nil))
			defer srv.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.WriteJSON(protocol.DaemonHello{
				Type: "daemon_hello", Name: "box", Token: "tok-1234567890", ProtocolVersion: "1", MCPGateway: tc.cap,
			}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for len(hub.GetAllDaemons()) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("the daemon never registered")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if got := hub.GetAllDaemons()[0].CanMCPGateway(); got != tc.cap {
				t.Errorf("CanMCPGateway = %v, want %v", got, tc.cap)
			}
		})
	}
}
