package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// BLG-453: Server-Timing is normal traffic's cost only when the release
// performance check's own environment opts the process in. A production
// server, or this same binary run as a normal install, never sets
// releasePerfServerTimingEnv and therefore never wraps a single handler call
// in timing, regardless of what any request header asks for.
const (
	releasePerfServerTimingEnv = "BLERG_RELEASE_PERF_SERVER_TIMING"
	releasePerfRequestHeader   = "X-Blerg-Release-Perf"
)

// ServerTimingMiddleware reports a request's handler duration as a
// Server-Timing response header, but only when both hold: the process's own
// environment has releasePerfServerTimingEnv="1" (set only by the release
// performance tool's own isolated server process) and the individual request
// carries releasePerfRequestHeader. Either alone is not enough: a stray
// header on normal traffic does nothing, and the env var alone exposes
// nothing until a request actually asks for it.
func ServerTimingMiddleware(next http.Handler) http.Handler {
	if os.Getenv(releasePerfServerTimingEnv) != "1" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(releasePerfRequestHeader) != "1" {
			next.ServeHTTP(w, r)
			return
		}
		tw := &timingResponseWriter{ResponseWriter: w, start: time.Now()}
		next.ServeHTTP(tw, r)
	})
}

// timingResponseWriter sets Server-Timing just before the first byte of the
// response goes out, since headers can't be added after WriteHeader/Write.
type timingResponseWriter struct {
	http.ResponseWriter
	start       time.Time
	wroteHeader bool
}

func (t *timingResponseWriter) WriteHeader(code int) {
	if !t.wroteHeader {
		t.wroteHeader = true
		ms := float64(time.Since(t.start).Microseconds()) / 1000
		t.Header().Set("Server-Timing", fmt.Sprintf("total;dur=%.3f", ms))
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *timingResponseWriter) Write(b []byte) (int, error) {
	if !t.wroteHeader {
		t.WriteHeader(http.StatusOK)
	}
	return t.ResponseWriter.Write(b)
}

// Hijack passes through to the underlying ResponseWriter so an upgrade
// (/ws/browser) still works when this wrapper is in the chain — without
// this, the type assertion every Hijacker caller needs fails, breaking
// every WebSocket upgrade a release-perf run's own X-Blerg-Release-Perf
// header happens to reach.
func (t *timingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := t.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

// Flush passes through for the same reason: a streamed response (SSE, a
// long-polled chunked reply) needs it, and silently dropping it would turn
// a streaming response into one buffered until completion instead of
// changing behavior loudly.
func (t *timingResponseWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
