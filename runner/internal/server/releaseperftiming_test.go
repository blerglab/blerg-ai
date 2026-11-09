package server

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func timingTestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// A normal server (the env flag unset) must never emit Server-Timing, even
// when a request asks for it with X-Blerg-Release-Perf. This is the gate: the
// header is off unless the tool's own environment turns it on.
func TestServerTimingMiddlewareOffWithoutEnv(t *testing.T) {
	t.Setenv(releasePerfServerTimingEnv, "")
	wrapped := ServerTimingMiddleware(timingTestHandler())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(releasePerfRequestHeader, "1")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if got := w.Header().Get("Server-Timing"); got != "" {
		t.Fatalf("Server-Timing leaked with env unset: %q", got)
	}
}

func TestServerTimingMiddlewareOffWithoutRequestHeader(t *testing.T) {
	t.Setenv(releasePerfServerTimingEnv, "1")
	wrapped := ServerTimingMiddleware(timingTestHandler())
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if got := w.Header().Get("Server-Timing"); got != "" {
		t.Fatalf("Server-Timing present without the request header: %q", got)
	}
}

func TestServerTimingMiddlewareOnWithEnvAndHeader(t *testing.T) {
	t.Setenv(releasePerfServerTimingEnv, "1")
	wrapped := ServerTimingMiddleware(timingTestHandler())
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(releasePerfRequestHeader, "1")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if got := w.Header().Get("Server-Timing"); got == "" {
		t.Fatal("expected Server-Timing with env set and request header present")
	}
	if w.Body.String() != "ok" {
		t.Fatalf("response body altered: %q", w.Body.String())
	}
}

// A WebSocket (or any other) upgrade hijacks the connection; wrapping its
// ResponseWriter in timingResponseWriter must not break that — the wrapper
// has to pass http.Hijacker through to the real connection, or every
// session-scoped scenario's /ws/browser upgrade breaks under this
// middleware (confirmed live: it 500'd the handshake, and the resulting
// cascading failures only surfaced as unrelated-looking "workspace not
// found" errors downstream).
func TestServerTimingMiddlewarePassesThroughHijack(t *testing.T) {
	t.Setenv(releasePerfServerTimingEnv, "1")
	hijacked := make(chan struct{})
	upgrade := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter does not implement http.Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("Hijack failed: %v", err)
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
		close(hijacked)
	})
	srv := httptest.NewServer(ServerTimingMiddleware(upgrade))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set(releasePerfRequestHeader, "1")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("expected a real upgrade response through the hijacked connection, got: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", resp.StatusCode)
	}
	<-hijacked
}

// A streamed response (SSE, a long-polling push) relies on Flush to put each
// chunk on the wire as it's written, instead of buffering until the handler
// returns. The wrapper must pass http.Flusher through for the same reason it
// passes Hijacker through: wrapping ResponseWriter in a type that doesn't
// implement the interface the real one does silently breaks streaming.
func TestServerTimingMiddlewarePassesThroughFlush(t *testing.T) {
	t.Setenv(releasePerfServerTimingEnv, "1")
	chunkWritten := make(chan struct{})
	release := make(chan struct{})
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement http.Flusher")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first-chunk\n"))
		f.Flush()
		close(chunkWritten)
		<-release
		_, _ = w.Write([]byte("second-chunk\n"))
	})
	srv := httptest.NewServer(ServerTimingMiddleware(stream))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set(releasePerfRequestHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	select {
	case <-chunkWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never reached its flush point")
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("expected the first chunk to arrive before the handler finished (Flush not passed through): %v", err)
	}
	if line != "first-chunk\n" {
		t.Fatalf("got %q, want %q", line, "first-chunk\n")
	}
	close(release)
}
