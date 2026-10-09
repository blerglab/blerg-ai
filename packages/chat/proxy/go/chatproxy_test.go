package chatproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorded is one request the stub runner saw.
type recorded struct {
	Method  string
	Path    string // path with query, as the runner received it
	Headers http.Header
	Body    string
}

// stubRunner stands in for the runner: it records every request and answers
// with whatever the test installed.
type stubRunner struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []recorded
	// respond is called for every request after recording; nil → 200 {"ok":true}.
	respond http.HandlerFunc
}

func newStubRunner(t *testing.T) *stubRunner {
	t.Helper()
	s := &stubRunner{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, recorded{
			Method:  r.Method,
			Path:    r.URL.RequestURI(),
			Headers: r.Header.Clone(),
			Body:    string(body),
		})
		respond := s.respond
		s.mu.Unlock()
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubRunner) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.seen...)
}

func (s *stubRunner) only(t *testing.T) recorded {
	t.Helper()
	got := s.requests()
	if len(got) != 1 {
		t.Fatalf("runner saw %d requests, want 1: %+v", len(got), got)
	}
	return got[0]
}

// recordingClient wraps HTTPRunnerClient and keeps the headers the proxy
// asked it to send, so a test can prove browser credentials never travel.
type recordingClient struct {
	inner   *HTTPRunnerClient
	mu      sync.Mutex
	headers []map[string]string
}

func (c *recordingClient) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	c.mu.Lock()
	c.headers = append(c.headers, headers)
	c.mu.Unlock()
	return c.inner.Do(ctx, method, path, body, headers)
}

const token = "agent-secret-token"

// app builds a proxy mounted at /blerg over the stub runner, served by a
// real test server so streaming and cancellation behave as in production.
func app(t *testing.T, runner *stubRunner, authorize Authorize) (*httptest.Server, *recordingClient) {
	t.Helper()
	client := &recordingClient{inner: &HTTPRunnerClient{BaseURL: runner.srv.URL, Token: token}}
	mux := http.NewServeMux()
	Mount(mux, "/blerg", client, authorize)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, client
}

func allow(*http.Request, string) (bool, error) { return true, nil }

func call(t *testing.T, srv *httptest.Server, method, path string, body string, headers map[string]string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	// Browser credentials that must never reach the runner.
	req.Header.Set("Authorization", "Bearer browser-token")
	req.Header.Set("Cookie", "app_session=abc")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRoutesForward(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		headers    map[string]string
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{"session", "GET", "/blerg/sessions/s1", "", nil, "GET", "/api/runner/sessions/s1", ""},
		{"events paging", "GET", "/blerg/sessions/s1/events?before_seq=40&limit=20", "", nil, "GET", "/api/runner/sessions/s1/events?before_seq=40&limit=20", ""},
		{"messages", "POST", "/blerg/sessions/s1/messages", `{"text":"hello","extra":"dropped"}`, nil, "POST", "/api/runner/sessions/s1/message", `{"text":"hello"}`},
		{"stop", "POST", "/blerg/sessions/s1/stop", "", nil, "POST", "/api/runner/sessions/s1/stop", ""},
		{"artifacts", "GET", "/blerg/sessions/s1/artifacts", "", nil, "GET", "/api/runner/sessions/s1/artifacts", ""},
		{"artifact raw", "GET", "/blerg/sessions/s1/artifacts/a9/raw", "", nil, "GET", "/api/runner/sessions/s1/artifacts/a9/raw", ""},
		{"artifact download", "GET", "/blerg/sessions/s1/artifacts/a9/download", "", nil, "GET", "/api/runner/sessions/s1/artifacts/a9/download", ""},
		{"artifact delete", "DELETE", "/blerg/sessions/s1/artifacts/a9", "", nil, "DELETE", "/api/runner/sessions/s1/artifacts/a9", ""},
		{"uploads", "POST", "/blerg/sessions/s1/uploads", "raw bytes here", map[string]string{"X-Artifact-Name": "notes.txt", "Content-Type": "text/plain"}, "POST", "/api/runner/sessions/s1/uploads", "raw bytes here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newStubRunner(t)
			srv, client := app(t, runner, allow)

			resp := call(t, srv, tc.method, tc.path, tc.body, tc.headers)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := readAll(t, resp.Body); got != `{"ok":true}` {
				t.Fatalf("body = %q", got)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}

			got := runner.only(t)
			if got.Method != tc.wantMethod || got.Path != tc.wantPath {
				t.Fatalf("runner saw %s %s, want %s %s", got.Method, got.Path, tc.wantMethod, tc.wantPath)
			}
			if got.Body != tc.wantBody {
				t.Fatalf("runner body = %q, want %q", got.Body, tc.wantBody)
			}
			if got.Headers.Get("Authorization") != "Bearer "+token {
				t.Fatalf("runner Authorization = %q, want the agent token", got.Headers.Get("Authorization"))
			}
			if got.Headers.Get("Cookie") != "" {
				t.Fatalf("browser cookie reached the runner: %q", got.Headers.Get("Cookie"))
			}
			for _, h := range client.headers {
				if _, ok := h["Authorization"]; ok {
					t.Fatal("proxy passed an Authorization header to the RunnerClient")
				}
				if _, ok := h["Cookie"]; ok {
					t.Fatal("proxy passed a Cookie header to the RunnerClient")
				}
			}
			if tc.name == "uploads" {
				if got.Headers.Get("X-Artifact-Name") != "notes.txt" {
					t.Fatalf("X-Artifact-Name = %q", got.Headers.Get("X-Artifact-Name"))
				}
				if got.Headers.Get("Content-Type") != "text/plain" {
					t.Fatalf("Content-Type = %q", got.Headers.Get("Content-Type"))
				}
			}
			if tc.name == "messages" && got.Headers.Get("Content-Type") != "application/json" {
				t.Fatalf("message Content-Type = %q", got.Headers.Get("Content-Type"))
			}
		})
	}
}

func TestRunnerStatusAndHeadersCopied(t *testing.T) {
	runner := newStubRunner(t)
	runner.respond = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="notes.txt"`)
		w.Header().Set("Cache-Control", "private, max-age=0")
		w.Header().Set("Set-Cookie", "runner=leak; Path=/")
		w.Header().Set("Authorization", "Bearer "+token)
		w.Header().Set("X-Runner-Internal", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "bytes")
	}
	srv, _ := app(t, runner, allow)

	resp := call(t, srv, "GET", "/blerg/sessions/s1/artifacts/a9/download", "", nil)
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want the runner's 206", resp.StatusCode)
	}
	if readAll(t, resp.Body) != "bytes" {
		t.Fatal("body not copied")
	}
	for name, want := range map[string]string{
		"Content-Type":        "application/octet-stream",
		"Content-Disposition": `attachment; filename="notes.txt"`,
		"Cache-Control":       "private, max-age=0",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"Set-Cookie", "Authorization", "X-Runner-Internal"} {
		if got := resp.Header.Values(name); len(got) != 0 {
			t.Errorf("%s forwarded to the browser: %v", name, got)
		}
	}
}

func TestRunnerErrorStatusPassesThrough(t *testing.T) {
	runner := newStubRunner(t)
	runner.respond = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}
	srv, _ := app(t, runner, allow)
	resp := call(t, srv, "GET", "/blerg/sessions/nope", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAuthorizeFalseIs403AndRunnerUntouched(t *testing.T) {
	runner := newStubRunner(t)
	deny := func(*http.Request, string) (bool, error) { return false, nil }
	srv, client := app(t, runner, deny)

	for _, rt := range []struct{ method, path string }{
		{"GET", "/blerg/sessions/s1"},
		{"GET", "/blerg/sessions/s1/events/live?tail=1"},
		{"GET", "/blerg/sessions/s1/events?before_seq=5"},
		{"POST", "/blerg/sessions/s1/messages"},
		{"POST", "/blerg/sessions/s1/stop"},
		{"POST", "/blerg/sessions/s1/interrupt"},
		{"POST", "/blerg/sessions/s1/model"},
		{"GET", "/blerg/sessions/s1/artifacts"},
		{"GET", "/blerg/sessions/s1/artifacts/a9/raw"},
		{"GET", "/blerg/sessions/s1/artifacts/a9/download"},
		{"DELETE", "/blerg/sessions/s1/artifacts/a9"},
		{"POST", "/blerg/sessions/s1/uploads"},
	} {
		resp := call(t, srv, rt.method, rt.path, `{"text":"x"}`, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", rt.method, rt.path, resp.StatusCode)
		}
	}
	if got := runner.requests(); len(got) != 0 {
		t.Fatalf("runner saw %d requests after refusals, want 0: %+v", len(got), got)
	}
	if len(client.headers) != 0 {
		t.Fatal("RunnerClient was called after a refusal")
	}
}

func TestAuthorizeErrorIs500(t *testing.T) {
	runner := newStubRunner(t)
	broken := func(*http.Request, string) (bool, error) { return true, errors.New("db down") }
	srv, _ := app(t, runner, broken)
	resp := call(t, srv, "GET", "/blerg/sessions/s1", "", nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if got := runner.requests(); len(got) != 0 {
		t.Fatalf("runner saw %d requests, want 0", len(got))
	}
	if strings.Contains(readAll(t, resp.Body), "db down") {
		t.Fatal("authorize error text leaked to the browser")
	}
}

func TestAuthorizeSeesTheSessionID(t *testing.T) {
	runner := newStubRunner(t)
	var seen string
	srv, _ := app(t, runner, func(r *http.Request, id string) (bool, error) {
		seen = id
		return r.Header.Get("Cookie") == "app_session=abc", nil
	})
	resp := call(t, srv, "GET", "/blerg/sessions/sess-42", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if seen != "sess-42" {
		t.Fatalf("authorize got session %q", seen)
	}
}

func TestModelIs501(t *testing.T) {
	runner := newStubRunner(t)
	srv, _ := app(t, runner, allow)
	resp := call(t, srv, "POST", "/blerg/sessions/s1/model", `{"model":"x","effort":"high"}`, nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", resp.StatusCode)
	}
	if got := runner.requests(); len(got) != 0 {
		t.Fatalf("runner saw %d requests for the reserved route, want 0", len(got))
	}
}

func TestMessagesRejectsBadBody(t *testing.T) {
	runner := newStubRunner(t)
	srv, _ := app(t, runner, allow)
	for _, body := range []string{``, `not json`, `{"text":""}`, `{"other":"x"}`, `[]`} {
		resp := call(t, srv, "POST", "/blerg/sessions/s1/messages", body, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, resp.StatusCode)
		}
	}
	if got := runner.requests(); len(got) != 0 {
		t.Fatalf("runner saw %d requests for bad bodies, want 0", len(got))
	}
}

func TestRunnerUnreachableIs502(t *testing.T) {
	runner := newStubRunner(t)
	runner.srv.Close()
	srv, _ := app(t, runner, allow)
	resp := call(t, srv, "GET", "/blerg/sessions/s1", "", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestLiveStreamForwardsFramesAndEndsOnCancel(t *testing.T) {
	runner := newStubRunner(t)
	runnerDone := make(chan struct{})
	runner.respond = func(w http.ResponseWriter, r *http.Request) {
		defer close(runnerDone)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		frames := []string{
			"event: agent_event\nid: 1\ndata: {\"seq\":1,\"kind\":\"text\"}\n\n",
			": keepalive\n\n",
			"event: replay_done\ndata: {\"last_seq\":1,\"has_more\":false}\n\n",
			"event: status\ndata: {\"session_id\":\"s1\",\"status\":\"running\"}\n\n",
		}
		for _, fr := range frames {
			_, _ = io.WriteString(w, fr)
			f.Flush()
		}
		// Hold the stream open until the proxy drops us (client cancelled).
		<-r.Context().Done()
	}
	srv, _ := app(t, runner, allow)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/blerg/sessions/s1/events/live?after_seq=0&tail=1&limit=200", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// Read frames one at a time; each must arrive without waiting for the
	// stream to end (the runner never closes it).
	rd := bufio.NewReader(resp.Body)
	readFrame := func() string {
		type res struct {
			s   string
			err error
		}
		ch := make(chan res, 1)
		go func() {
			var sb strings.Builder
			for {
				line, err := rd.ReadString('\n')
				sb.WriteString(line)
				if err != nil {
					ch <- res{sb.String(), err}
					return
				}
				if line == "\n" {
					ch <- res{sb.String(), nil}
					return
				}
			}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				t.Fatalf("stream ended early: %v (got %q)", r.err, r.s)
			}
			return r.s
		case <-time.After(5 * time.Second):
			t.Fatal("frame did not arrive: the proxy is not flushing per frame")
			return ""
		}
	}
	got := []string{readFrame(), readFrame(), readFrame()}
	want := []string{
		"event: agent_event\nid: 1\ndata: {\"seq\":1,\"kind\":\"text\"}\n\n",
		": keepalive\n\n",
		"event: replay_done\ndata: {\"last_seq\":1,\"has_more\":false}\n\n",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}

	rec := runner.only(t)
	if rec.Path != "/api/runner/sessions/s1/events/live?after_seq=0&limit=200&tail=1" {
		t.Fatalf("runner path = %q", rec.Path)
	}
	if rec.Headers.Get("Authorization") != "Bearer "+token {
		t.Fatal("live stream went to the runner without the agent token")
	}

	// The browser goes away: the runner request must be cancelled.
	cancel()
	select {
	case <-runnerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("runner stream was not cancelled after the client disconnected")
	}
}

func TestLiveStreamRunnerRefusalPassesThrough(t *testing.T) {
	runner := newStubRunner(t)
	runner.respond = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}
	srv, _ := app(t, runner, allow)
	resp := call(t, srv, "GET", "/blerg/sessions/s1/events/live", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMountAtRootAndUnknownRoutes(t *testing.T) {
	runner := newStubRunner(t)
	client := &HTTPRunnerClient{BaseURL: runner.srv.URL, Token: token}
	mux := http.NewServeMux()
	Mount(mux, "", client, allow)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := call(t, srv, "GET", "/sessions/s1", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root mount: status = %d", resp.StatusCode)
	}
	// Nothing beyond the contract is exposed.
	resp = call(t, srv, "GET", "/sessions", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("session listing: status = %d, want 404", resp.StatusCode)
	}
	resp = call(t, srv, "PUT", "/sessions/s1", "", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: status = %d, want 405", resp.StatusCode)
	}
	if got := runner.requests(); len(got) != 1 {
		t.Fatalf("runner saw %d requests, want 1", len(got))
	}
}

// appWith is app with MountWith's options.
func appWith(t *testing.T, runner *stubRunner, opts Options) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	MountWith(mux, "/blerg", &HTTPRunnerClient{BaseURL: runner.srv.URL, Token: token}, opts)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// An app that names sessions its own way maps its id to the runner's: the
// person is authorized on the app's id, the runner is asked for its own.
func TestResolveMapsTheAppsSessionIDToTheRunners(t *testing.T) {
	runner := newStubRunner(t)
	var authorizedOn string
	srv := appWith(t, runner, Options{
		Authorize: func(_ *http.Request, id string) (bool, error) { authorizedOn = id; return true, nil },
		Resolve: func(_ *http.Request, id string) (string, error) {
			if id != "row-7" {
				return "", ErrNotFound
			}
			return "runner-abc", nil
		},
	})
	resp := call(t, srv, "GET", "/blerg/sessions/row-7/events?before_seq=5", "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if authorizedOn != "row-7" {
		t.Fatalf("Authorize saw %q, want the app's id row-7", authorizedOn)
	}
	if got := runner.only(t).Path; got != "/api/runner/sessions/runner-abc/events?before_seq=5" {
		t.Fatalf("runner path = %q, want the runner's id in it", got)
	}
}

func TestResolveNotFoundIs404AndRunnerUntouched(t *testing.T) {
	runner := newStubRunner(t)
	srv := appWith(t, runner, Options{
		Authorize: allow,
		Resolve:   func(*http.Request, string) (string, error) { return "", ErrNotFound },
	})
	resp := call(t, srv, "GET", "/blerg/sessions/nope", "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if n := len(runner.requests()); n != 0 {
		t.Fatalf("runner saw %d requests, want 0", n)
	}
}

func TestResolveErrorOrBadIDIs500(t *testing.T) {
	for name, resolve := range map[string]func(*http.Request, string) (string, error){
		"error":  func(*http.Request, string) (string, error) { return "", errors.New("db down: secret detail") },
		"bad id": func(*http.Request, string) (string, error) { return "a/b", nil },
	} {
		t.Run(name, func(t *testing.T) {
			runner := newStubRunner(t)
			srv := appWith(t, runner, Options{Authorize: allow, Resolve: resolve})
			resp := call(t, srv, "POST", "/blerg/sessions/s1/stop", "", nil)
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", resp.StatusCode)
			}
			if strings.Contains(string(body), "secret detail") {
				t.Fatalf("the resolver's error leaked: %s", body)
			}
			if n := len(runner.requests()); n != 0 {
				t.Fatalf("runner saw %d requests, want 0", n)
			}
		})
	}
}

// Resolve never runs for a person Authorize refused.
func TestResolveRunsOnlyAfterAuthorize(t *testing.T) {
	runner := newStubRunner(t)
	resolved := false
	srv := appWith(t, runner, Options{
		Authorize: func(*http.Request, string) (bool, error) { return false, nil },
		Resolve:   func(*http.Request, string) (string, error) { resolved = true; return "x", nil },
	})
	resp := call(t, srv, "GET", "/blerg/sessions/s1", "", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || resolved {
		t.Fatalf("status = %d resolved = %v, want 403 and no resolve", resp.StatusCode, resolved)
	}
}

func TestMessageSourceIsSentToTheRunner(t *testing.T) {
	runner := newStubRunner(t)
	srv := appWith(t, runner, Options{Authorize: allow, MessageSource: "human"})
	resp := call(t, srv, "POST", "/blerg/sessions/s1/messages", `{"text":"hi","source":"system"}`, nil)
	_ = resp.Body.Close()
	got := runner.only(t)
	if got.Body != `{"source":"human","text":"hi"}` {
		t.Fatalf("runner body = %s, want the proxy's source and the text only", got.Body)
	}
}
