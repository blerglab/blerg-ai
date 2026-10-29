package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestDesktopPersonalAttachPolicyFailure(t *testing.T) {
	testDesktopPersonalAttachFailure(t, "policy")
}

func TestDesktopPersonalAttachWelcomeFailure(t *testing.T) {
	testDesktopPersonalAttachFailure(t, "welcome")
}

func TestDesktopPersonalAttachPolicyTimeout(t *testing.T) {
	testDesktopPersonalAttachFailure(t, "timeout")
}

func testDesktopPersonalAttachFailure(t *testing.T, failure string) {
	failWelcome := failure == "welcome"
	t.Helper()
	for _, state := range []string{"existing", "fresh", "pending"} {
		t.Run(state, func(t *testing.T) {
			fx := newPrivFixture(t)
			fx.hub.UnregisterIfCurrent(fx.dc)
			binding := *fx.dc.DesktopBinding
			switch state {
			case "fresh":
				binding.DesktopID = newUUID()
			case "pending":
				binding.Revision++
				pending := db.DesktopEnrollment{Owner: binding.OwnerID, TokenID: binding.TokenID, RequestID: newUUID(), Generation: fx.dc.DesktopGeneration, Revision: binding.Revision}
				if err := db.BeginDesktopEnrollment(t.Context(), fx.pool, binding.DesktopID, pending); err != nil {
					t.Fatal(err)
				}
			}
			token := "bdt_" + strings.Repeat("b", 64)
			desktopFakeCore(t, fx.api, map[string]desktopauth.Binding{token: binding})
			if !failWelcome {
				fx.hub.SetDelegationHelloHook(func(ctx context.Context, _ string) error {
					if failure == "timeout" {
						<-ctx.Done()
						return ctx.Err()
					}
					return errors.New("fixture recovery policy failure")
				})
			}
			finished := make(chan struct{})
			handler := fx.hub.ServeDaemon(spawnTokenDaemonTok, fx.pool)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				handler(w, r)
			}))
			if failWelcome {
				srv.Listener = failDesktopWelcomeListener{srv.Listener}
			}
			srv.Start()
			defer srv.Close()
			conn := desktopConnect(t, srv.URL, token, binding.DesktopID, fx.dc.Name, !failWelcome)
			if !failWelcome {
				_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
				if _, _, err := conn.ReadMessage(); err == nil {
					t.Fatal("fixture did not refuse attach")
				}
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("failed attach did not finish")
			}
			if fx.hub.GetDaemon(binding.DesktopID) != nil {
				t.Fatal("failed attach remained in hub")
			}
			// Failure must release the advisory lock and its pool reservation.
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := db.WithDesktopFence(ctx, fx.pool, binding.DesktopID, func(context.Context) error { return nil }); err != nil {
				t.Fatalf("failed attach retained desktop fence: %v", err)
			}
			d, err := db.GetDesktop(t.Context(), fx.pool, binding.DesktopID)
			if err != nil || d == nil {
				t.Fatalf("bound desktop missing: %v", err)
			}
			if d.Generation == "" || d.Generation == fx.dc.DesktopGeneration || d.OwnerID != binding.OwnerID || d.Revision != binding.Revision {
				t.Fatal("fixture did not bind the attempted credential and a new generation")
			}
			if d.Status != "disconnected" || d.StatusReason != "Desktop disconnected; check the daemon service and reconnect." {
				t.Fatalf("failed personal attach left status=%q reason=%q", d.Status, d.StatusReason)
			}
		})
	}
}

type failDesktopWelcomeListener struct{ net.Listener }

func (l failDesktopWelcomeListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return failDesktopWelcomeConn{conn}, nil
}

type failDesktopWelcomeConn struct{ net.Conn }

func (c failDesktopWelcomeConn) Write(raw []byte) (int, error) {
	if bytes.Contains(raw, []byte("daemon_welcome")) {
		return 0, errors.New("fixture welcome write failure")
	}
	return c.Conn.Write(raw)
}
