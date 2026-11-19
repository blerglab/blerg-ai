// Package failurelog records failures without copying request or error content.
package failurelog

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type bucket struct {
	at         time.Time
	suppressed int
}

// Recorder bounds repeated failures and the memory used by their keys.
type Recorder struct {
	mu      sync.Mutex
	buckets map[string]bucket
	logger  *slog.Logger
	now     func() time.Time
}

func New(logger *slog.Logger) *Recorder {
	return &Recorder{buckets: make(map[string]bucket), logger: logger, now: time.Now}
}

var Default = New(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

// Event accepts only developer-owned labels and safe scalar attributes. Never pass
// error messages, URLs, paths, bodies, headers or command output.
func (l *Recorder) Event(key string, level slog.Level, attrs ...any) {
	l.mu.Lock()
	now := l.now()
	b, exists := l.buckets[key]
	if exists && now.Sub(b.at) < time.Minute {
		b.suppressed++
		l.buckets[key] = b
		l.mu.Unlock()
		return
	}
	suppressed := b.suppressed
	if !exists && len(l.buckets) >= 1024 { // Fixed overflow bucket also bounds hostile cardinality.
		key = "overflow"
		b, exists = l.buckets[key]
		if exists && now.Sub(b.at) < time.Minute {
			b.suppressed++
			l.buckets[key] = b
			l.mu.Unlock()
			return
		}
		suppressed = b.suppressed
	}
	l.buckets[key] = bucket{at: now}
	l.mu.Unlock()
	attrs = append(attrs, "suppressed", suppressed)
	if level >= slog.LevelError {
		l.logger.Error("failure", attrs...)
	} else {
		l.logger.Warn("failure", attrs...)
	}
}

// Cause retains typed causes, never the potentially private Error() string.
func Cause(err error) string {
	switch {
	case err == nil:
		return "safety_check"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("process_exit_%d", exit.ExitCode())
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("errno_%d", uint64(errno))
	}
	var path *os.PathError
	if errors.As(err, &path) {
		return Cause(path.Err)
	}
	return fmt.Sprintf("%T", err)
}

// HTTP observes the entire router, including authentication and unmatched routes.
func (l *Recorder) HTTP(component string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := l.now()
		capture := &response{ResponseWriter: w}
		defer func() {
			if capture.status < 400 {
				return
			}
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			level := slog.LevelError
			if capture.status == 401 || capture.status == 404 || capture.status == 429 {
				level = slog.LevelWarn
			}
			attrs := []any{"component", component, "route", route, "status", capture.status, "duration_ms", l.now().Sub(started).Milliseconds()}
			if capture.cause != "" {
				attrs = append(attrs, "cause", capture.cause)
			}
			if id := capture.ids.errorID(); id != "" {
				attrs = append(attrs, "error_id", id)
			}
			for _, name := range []string{"session_id", "sessionID", "session", "board_id", "boardID", "board", "id"} {
				if value := r.PathValue(name); safeID(value) {
					field := name
					if name == "id" && strings.Contains(route, "/sessions/{id}") {
						field = "session_id"
					}
					if name == "id" && strings.Contains(route, "/boards/{id}") {
						field = "board_id"
					}
					if name == "board" || name == "boardID" {
						field = "board_id"
					}
					if name == "session" || name == "sessionID" {
						field = "session_id"
					}
					attrs = append(attrs, field, value)
				}
			}
			l.Event(fmt.Sprintf("%s:%s:%d", component, route, capture.status), level, attrs...)
		}()
		next.ServeHTTP(capture, r)
	})
}
func safeID(s string) bool {
	if len(s) < 8 || len(s) > 36 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') && c != '-' {
			return false
		}
	}
	return true
}

type response struct {
	http.ResponseWriter
	cause  string
	status int
	ids    idScanner
}

func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *response) WriteHeader(status int) {
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *response) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if w.status >= 400 {
		w.ids.write(p)
	}
	return w.ResponseWriter.Write(p)
}
func (w *response) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *response) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
func (w *response) ReadFrom(r io.Reader) (int64, error) { return io.Copy(struct{ io.Writer }{w}, r) }

// SetCause attaches a safe cause to the eventual middleware entry. It deliberately
// ignores contextual strings, which often contain workspace paths or user data.
func SetCause(w http.ResponseWriter, err error) {
	for {
		if target, ok := w.(*response); ok {
			target.cause = Cause(err)
			return
		}
		wrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = wrapper.Unwrap()
	}
}
