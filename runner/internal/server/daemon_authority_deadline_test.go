package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// The lab's read_authority/socket_deadline signature is produced by a DB
// deadline, not ReadMessage (which logs read_closed). Release the DB lock on
// that diagnostic and require the original socket to recover in both pumps.
func TestDaemonAuthorityDeadlineKeepsSocket(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		t.Run(direction, func(t *testing.T) {
			f := newPrivFixture(t)
			token := "bdt_" + strings.Repeat("a", 64)
			id := newUUID()
			b := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
			desktopFakeCore(t, f.api, map[string]desktopauth.Binding{token: b})
			mux := http.NewServeMux()
			mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
			srv := httptest.NewServer(mux)
			defer srv.Close()
			peer := desktopConnect(t, srv.URL, token, id, "authority-deadline", true)
			defer peer.Close()
			dc := desktopWait(t, f.hub, id)
			expired := make(chan string, 1)
			previous := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(&authorityDeadlineSignal{stage: direction + "_authority", expired: expired}))
			defer func() { failurelog.Default = previous }()
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			if _, err = tx.Exec(t.Context(), `LOCK TABLE daemons IN ACCESS EXCLUSIVE MODE`); err != nil {
				t.Fatal(err)
			}
			got := make(chan error, 1)
			go func() {
				for {
					_, raw, err := peer.ReadMessage()
					if err != nil {
						got <- err
						return
					}
					if strings.Contains(string(raw), `"type":"probe"`) {
						got <- nil
						return
					}
				}
			}()
			if direction == "read" {
				if err = peer.WriteJSON(map[string]string{"type": "probe"}); err != nil {
					t.Fatal(err)
				}
			} else {
				dc.send <- []byte(`{"type":"probe"}`)
			}
			select {
			case cause := <-expired:
				if cause != "authority_timeout" {
					t.Errorf("authority timeout mislabeled: %s", cause)
				}
			case err := <-got:
				t.Fatalf("closed before authority diagnosis: %v", err)
			case <-time.After(12 * time.Second):
				t.Fatal("no authority deadline")
			}
			if err = tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if direction == "read" {
				dc.send <- []byte(`{"type":"probe"}`)
			}
			select {
			case err := <-got:
				if err != nil {
					t.Fatalf("request deadline closed original socket: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("socket did not recover")
			}
			if f.hub.GetDaemon(id) != dc {
				t.Fatal("socket replaced")
			}
		})
	}
}

type authorityDeadlineSignal struct {
	stage   string
	expired chan string
}

func (h *authorityDeadlineSignal) Enabled(context.Context, slog.Level) bool { return true }
func (h *authorityDeadlineSignal) Handle(_ context.Context, r slog.Record) error {
	var stage, cause string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "stage" {
			stage = a.Value.String()
		}
		if a.Key == "cause" {
			cause = a.Value.String()
		}
		return true
	})
	if (strings.HasPrefix(stage, h.stage) || stage == "write_or_authority") && (cause == "socket_deadline" || cause == "deadline" || cause == "authority_timeout") {
		select {
		case h.expired <- cause:
		default:
		}
	}
	return nil
}
func (h *authorityDeadlineSignal) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *authorityDeadlineSignal) WithGroup(string) slog.Handler      { return h }

// Heartbeat reauthentication used to retain the desktop advisory transaction
// across a core HTTP call, blocking every explorer delivery and cleanup RPC.
func TestDaemonHeartbeatReauthDoesNotHoldDesktopFence(t *testing.T) {
	f := newPrivFixture(t)
	token := "bdt_" + strings.Repeat("a", 64)
	id := newUUID()
	binding := desktopauth.Binding{DesktopID: id, OwnerID: privAcctA, Revision: 1, TokenID: newUUID(), ExpiresAt: time.Now().Add(time.Hour)}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(binding)
	}))
	defer core.Close()
	defer close(release)
	f.api.coreURL, f.api.coreInternalKey = core.URL, "fixture-internal-key"
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/daemon", f.hub.ServeDaemon(spawnTokenDaemonTok, f.pool))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	peer := desktopConnect(t, srv.URL, token, id, "slow-core", true)
	defer peer.Close()
	desktopWait(t, f.hub, id)
	if err := peer.WriteJSON(map[string]string{"type": "daemon_heartbeat"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("core validation not called")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := db.WithDesktopFence(ctx, f.pool, id, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("core HTTP request holds desktop authority lock: %v", err)
	}
}

func TestDaemonAuthorityRetryPreservesRevocationAndSingleDelivery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		handled bool
		calls   int
		success bool
	}{
		{"admission timeout", context.DeadlineExceeded, false, 2, true},
		{"handled timeout", context.DeadlineExceeded, true, 1, true},
		{"revoked", db.ErrDesktopAuthority, false, 1, false},
		{"handled revoked", db.ErrDesktopAuthority, true, 1, false},
		{"socket timeout", &daemonSocketWriteError{context.DeadlineExceeded}, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := withDaemonAuthority(t.Context(), "read_authority", "fixture", func(handled func()) error {
				calls++
				if calls > 1 {
					return nil
				}
				if tc.handled {
					handled()
				}
				return tc.err
			})
			if (err == nil) != tc.success || calls != tc.calls {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
