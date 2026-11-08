package failurelog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHTTPPrivacyAndSuppression(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	now := time.Unix(0, 0)
	recorder := New(logger)
	recorder.now = func() time.Time { return now }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error_id":"abcdef12","error":"CANARY_SECRET /private/workspace"}`))
	})
	handler := recorder.HTTP("runner", mux)
	for range 3 {
		req := httptest.NewRequest("GET", "/api/sessions/CANARY_SECRET?token=CANARY_SECRET", strings.NewReader("CANARY_SECRET"))
		req.Header.Set("Authorization", "Bearer CANARY_SECRET")
		req.Header.Set("Cookie", "secret=CANARY_SECRET")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if strings.Contains(out.String(), "CANARY_SECRET") || strings.Contains(out.String(), "/private") {
		t.Fatal(out.String())
	}
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatal(out.String())
	}
	now = now.Add(time.Minute)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/sessions/123", nil))
	for _, want := range []string{`"suppressed":2`, `"route":"GET /api/sessions/{id}"`, `"status":500`, `"error_id":"abcdef12"`, `"level":"ERROR"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s: %s", want, out.String())
		}
	}
}

func TestHTTPStatusAndWireBehavior(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 404, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var out bytes.Buffer
			l := New(slog.New(slog.NewJSONHandler(&out, nil)))
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/boards/{id}", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.WriteHeader(502) // First status wins.
				_, _ = w.Write([]byte(`{"error_id":"12345678","error":"CANARY_SECRET"}`))
			})
			rec := httptest.NewRecorder()
			l.HTTP("board", mux).ServeHTTP(rec, httptest.NewRequest("GET", "/api/boards/12345678", nil))
			if rec.Code != status || !strings.Contains(rec.Body.String(), "CANARY_SECRET") {
				t.Fatal(rec)
			}
			if status < 400 {
				if out.Len() != 0 {
					t.Fatal(out.String())
				}
				return
			}
			var event map[string]any
			if err := json.Unmarshal(out.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			level := "ERROR"
			if status == 401 || status == 404 || status == 429 {
				level = "WARN"
			}
			if event["level"] != level || event["board_id"] != "12345678" {
				t.Fatal(event)
			}
			if strings.Contains(out.String(), "CANARY_SECRET") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestUnknownRouteDoesNotLogPath(t *testing.T) {
	var out bytes.Buffer
	l := New(slog.New(slog.NewJSONHandler(&out, nil)))
	l.HTTP("core", http.NewServeMux()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/CANARY_SECRET?token=CANARY_SECRET", nil))
	if !strings.Contains(out.String(), `"route":"unmatched"`) || strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal(out.String())
	}
}

func TestCausePrivacy(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "CANARY_SECRET", Path: "/CANARY_SECRET", Err: os.ErrPermission}, "permission"},
		{fmt.Errorf("CANARY_SECRET: %w", context.DeadlineExceeded), "deadline"},
		{errors.New("CANARY_SECRET"), "*errors.errorString"},
		{&os.PathError{Op: "CANARY_SECRET", Path: "/CANARY_SECRET", Err: syscall.EIO}, "errno_5"},
	} {
		if got := Cause(tc.err); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestUpstreamPrivacy(t *testing.T) {
	var out bytes.Buffer
	l := New(slog.New(slog.NewJSONHandler(&out, nil)))
	tr := l.Transport("board", "core", transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("CANARY_SECRET"))}, nil
	}))
	req := httptest.NewRequest("GET", "https://example.com/CANARY_SECRET?token=CANARY_SECRET", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	redirect := l.Transport("board", "core", transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("CANARY_SECRET"))}, nil
	}))
	redirected, redirectErr := redirect.RoundTrip(req)
	if redirectErr != nil {
		t.Fatal(redirectErr)
	}
	_ = redirected.Body.Close()
	tr = l.Transport("board", "core", transportFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("CANARY_SECRET") }))
	_, _ = tr.RoundTrip(req)
	if strings.Contains(out.String(), "CANARY_SECRET") || !strings.Contains(out.String(), `"upstream":"core"`) || !strings.Contains(out.String(), `"status":503`) || !strings.Contains(out.String(), `"status":302`) || !strings.Contains(out.String(), `"status":0`) {
		t.Fatal(out.String())
	}
}

func TestLimiterMemoryBound(t *testing.T) {
	l := New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for i := range 5000 {
		l.Event(fmt.Sprint(i), slog.LevelError, "status", 500)
	}
	if len(l.buckets) > 1025 {
		t.Fatal(len(l.buckets))
	}
}

func TestStreamingAndAnnotatedCause(t *testing.T) {
	var out bytes.Buffer
	l := New(slog.New(slog.NewJSONHandler(&out, nil)))
	handler := l.HTTP("runner", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetCause(w, &os.PathError{Path: "CANARY_SECRET", Err: os.ErrPermission})
		w.WriteHeader(503)
		_, _ = io.Copy(w, strings.NewReader(`{"error_id":"abcdef12"}`))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !rec.Flushed || rec.Code != 503 || rec.Body.String() != `{"error_id":"abcdef12"}` {
		t.Fatal(rec)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), `"cause":"permission"`) || strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal(out.String())
	}
}

func TestHTTPLargeErrorKeepsCorrelationID(t *testing.T) {
	var out bytes.Buffer
	l := New(slog.New(slog.NewJSONHandler(&out, nil)))
	handler := l.HTTP("board", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"` + strings.Repeat("CANARY_SECRET", 10000) + `","error_id":"abcdef12"}`))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(out.String(), `"error_id":"abcdef12"`) || strings.Contains(out.String(), "CANARY_SECRET") {
		t.Fatal(out.String())
	}
}
