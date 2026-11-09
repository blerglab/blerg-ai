package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInterruptedWaiterRetriesUntilAcknowledged(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	attempts := 0
	client := &http.Client{Transport: noticeTransport(func(r *http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusOK
		if attempts <= 3 {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: r}, nil
	})}
	m := &httpMessenger{base: "https://runner.example.com", sessionID: "fixture", token: "fixture-token", client: client}
	path, err := m.saveWaiterNotice("fixture-question")
	if err != nil {
		t.Fatal(err)
	}
	waits := []time.Duration{}
	m.waitNotice = func(ctx context.Context, pause time.Duration) bool {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("notice removed before acknowledgment", err)
		}
		waits = append(waits, pause)
		return ctx.Err() == nil
	}
	m.retryWaiterNoticeAttempts(t.Context(), "fixture-question", path, 0, questionRecoveryBackoff)
	if attempts != 4 || !slices.Equal(waits, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}) {
		t.Fatalf("attempts=%d waits=%v", attempts, waits)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("acknowledged notice retained", err)
	}
}

type noticeTransport func(*http.Request) (*http.Response, error)

func (f noticeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInterruptedWaiterCloseUsesRealHTTP(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/api/messages/fixture-question/expire" || r.Header.Get("Authorization") != "Bearer fixture-token" || string(raw) != `{"waiter_ended":true}` {
			t.Errorf("invalid waiter notice: %s %s %q %v", r.Method, r.URL.Path, raw, err)
		}
		done <- struct{}{}
	}))
	defer srv.Close()
	canceled := make(chan struct{})
	m := &httpMessenger{base: srv.URL, sessionID: "fixture", token: "fixture-token", client: srv.Client(), lateAnswerRelay: true,
		polls: map[string]context.CancelFunc{"fixture-question": func() { close(canceled) }}}
	m.CloseAsk("fixture-question", "")
	select {
	case <-canceled:
	default:
		t.Fatal("answer poll was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("notice did not reach real HTTP")
	}
}

func TestWaiterNoticeTimerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitNoticeTimer(ctx, time.Hour) {
		t.Fatal("canceled timer advanced retry")
	}
	if !waitNoticeTimer(t.Context(), 0) {
		t.Fatal("ready timer did not advance retry")
	}
}

func TestInterruptedWaiterReplaysAfterRestart(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1) }))
	defer srv.Close()
	m := &httpMessenger{base: srv.URL, sessionID: "fixture", token: "fixture-token", client: srv.Client(), lateAnswerRelay: true}
	if _, err := m.saveWaiterNotice("fixture-question"); err != nil {
		t.Fatal(err)
	}
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-token"}}
	h.retryWaiterNotices(context.Background())
	h.retryWaiterNotices(context.Background())
	if attempts.Load() != 1 {
		t.Fatalf("replayed %d times", attempts.Load())
	}
}

func TestRestartedWaiterNoticeKeepsRetryingWithoutReconnect(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	var attempts atomic.Int32
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		done <- struct{}{}
	}))
	defer srv.Close()
	m := &httpMessenger{base: srv.URL, sessionID: "fixture"}
	if _, err := m.saveWaiterNotice("fixture-question"); err != nil {
		t.Fatal(err)
	}
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-token"}}
	h.retryWaiterNotices(context.Background())
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("restart retry abandoned after %d attempts", attempts.Load())
	}
}

// Expired scope and deleted questions cannot be repaired by replaying a notice.
func TestWaiterTerminalResponsesClearPersistedNotice(t *testing.T) {
	for _, status := range []int{403, 404, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			m := &httpMessenger{base: srv.URL, sessionID: "fixture", client: srv.Client()}
			path, err := m.saveWaiterNotice("fixture-question")
			if err != nil {
				t.Fatal(err)
			}
			h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-token"}}
			h.retryWaiterNotices(t.Context())
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("terminal HTTP %d retained notice: %v", status, err)
			}
		})
	}
}

func TestWaiterTransientResponseRetainsNotice(t *testing.T) {
	for _, status := range []int{401, 408, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			m := &httpMessenger{base: srv.URL, client: srv.Client()}
			if m.sendWaiterNotice(t.Context(), "fixture-question") {
				t.Fatalf("HTTP %d discarded retryable notice", status)
			}
		})
	}
}

func TestWaiterTransientRetryBatchIsBoundedAndReplayed(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	var attempts atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !healthy.Load() {
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	m := &httpMessenger{base: srv.URL, sessionID: "fixture", client: srv.Client()}
	path, err := m.saveWaiterNotice("fixture-question")
	if err != nil {
		t.Fatal(err)
	}
	m.retryWaiterNoticeAttempts(t.Context(), "fixture-question", path, 0, time.Millisecond)
	if attempts.Load() != questionRecoveryAttempts {
		t.Fatalf("attempts=%d", attempts.Load())
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("transient failure discarded notice", err)
	}
	healthy.Store(true)
	h := &AgentHost{cfg: AgentHostConfig{ServerHTTP: srv.URL, DaemonToken: "fixture-token"}}
	h.retryWaiterNotices(t.Context())
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reconnect did not acknowledge notice", err)
	}
}
