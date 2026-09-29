package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// sseStream is a client-side reader for one live text/event-stream response.
// httptest.ResponseRecorder cannot be used for these tests: it buffers the
// whole body and never returns until the handler does, which is exactly what a
// stream must not do. So the tests run a real server and read the body line by
// line as it arrives.
type sseStream struct {
	lines  chan string
	done   chan error // receives the read error (nil on clean EOF) once
	cancel context.CancelFunc
	resp   *http.Response
}

func openSSEStream(t *testing.T, srv *httptest.Server, path string, hdr map[string]string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s := &sseStream{lines: make(chan string, 512), done: make(chan error, 1), cancel: cancel, resp: resp}
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		s.done <- sc.Err()
		close(s.lines)
	}()
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	return s
}

// next returns the next non-blank line of the stream (blank lines are the SSE
// record separator), failing the test if none arrives promptly.
func (s *sseStream) next(t *testing.T) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatal("stream ended while waiting for a line")
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			return line
		case <-deadline:
			t.Fatal("timed out waiting for an SSE line")
		}
	}
}

// expect asserts the next non-blank line equals want.
func (s *sseStream) expect(t *testing.T, want string) {
	t.Helper()
	if got := s.next(t); got != want {
		t.Fatalf("SSE line = %q, want %q", got, want)
	}
}

// expectData asserts the next line is a data line and returns its parsed JSON.
func (s *sseStream) expectData(t *testing.T) map[string]any {
	t.Helper()
	line := s.next(t)
	raw, ok := strings.CutPrefix(line, "data: ")
	if !ok {
		t.Fatalf("SSE line = %q, want a data line", line)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("data line is not JSON (%v): %q", err, raw)
	}
	return out
}

// waitEOF drains the rest of the stream and asserts it ended cleanly.
func (s *sseStream) waitEOF(t *testing.T) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-s.lines:
			if !ok {
				select {
				case err := <-s.done:
					if err != nil {
						t.Fatalf("stream read error: %v", err)
					}
				default:
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the stream to close")
		}
	}
}

// streamTestAPI builds the contract API with the stream's timers shortened so a
// test spends milliseconds where production spends seconds.
func streamTestAPI(t *testing.T, poll, keepalive time.Duration) (*API, *httptest.Server, string) {
	t.Helper()
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	api.ssePollInterval, api.sseKeepaliveInterval = poll, keepalive
	ctx := context.Background()
	daemonID := newUUID()
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(agentContractMux(api))
	t.Cleanup(srv.Close)
	return api, srv, sessionID
}

func appendEvent(t *testing.T, api *API, sessionID, kind, payload string) int64 {
	t.Helper()
	seq, _, err := db.AppendAgentEvent(context.Background(), api.dbPool, sessionID, newUUID(), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// The stream must announce itself as an unbuffered event stream and deliver
// every persisted event in seq order, each framed with its id and kind — then
// keep delivering events appended after the client connected.
func TestRunnerEventStreamDeliversEventsInOrder(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 20*time.Millisecond, time.Hour)
	appendEvent(t, api, sessionID, "assistant_text", `{"text":"one","done":true}`)
	appendEvent(t, api, sessionID, "tool_result", `{"output":"two"}`)

	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/stream", nil)
	if ct := s.resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := s.resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if xa := s.resp.Header.Get("X-Accel-Buffering"); xa != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", xa)
	}

	s.expect(t, "id: 1")
	s.expect(t, "event: assistant_text")
	if got := s.expectData(t)["text"]; got != "one" {
		t.Errorf("first event text = %v, want one", got)
	}
	s.expect(t, "id: 2")
	s.expect(t, "event: tool_result")
	if got := s.expectData(t)["output"]; got != "two" {
		t.Errorf("second event output = %v, want two", got)
	}

	// Live: an event appended after the client connected must arrive without
	// the client reconnecting.
	appendEvent(t, api, sessionID, "assistant_text", `{"text":"three","done":true}`)
	s.expect(t, "id: 3")
	s.expect(t, "event: assistant_text")
	if got := s.expectData(t)["text"]; got != "three" {
		t.Errorf("third event text = %v, want three", got)
	}
}

// A reconnecting client resumes exactly where it left off: Last-Event-ID wins,
// after_seq is the fallback, and neither replays what the client already has.
func TestRunnerEventStreamResumesFromCursor(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 20*time.Millisecond, time.Hour)
	for _, text := range []string{"one", "two", "three"} {
		appendEvent(t, api, sessionID, "assistant_text", `{"text":"`+text+`"}`)
	}
	path := "/api/runner/sessions/" + sessionID + "/events/stream"

	s := openSSEStream(t, srv, path, map[string]string{"Last-Event-ID": "2"})
	s.expect(t, "id: 3")
	s.expect(t, "event: assistant_text")
	if got := s.expectData(t)["text"]; got != "three" {
		t.Errorf("resumed event = %v, want three", got)
	}
	s.cancel()

	s2 := openSSEStream(t, srv, path+"?after_seq=1", nil)
	s2.expect(t, "id: 2")
	s2.cancel()

	// Last-Event-ID takes precedence over after_seq.
	s3 := openSSEStream(t, srv, path+"?after_seq=0", map[string]string{"Last-Event-ID": "2"})
	s3.expect(t, "id: 3")
	s3.cancel()
}

// A stream with nothing to say must still say something: proxies and clients
// drop an idle connection, so a comment goes out on the keepalive interval.
func TestRunnerEventStreamSendsKeepalive(t *testing.T) {
	_, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, 40*time.Millisecond)
	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/stream", nil)
	s.expect(t, ": keepalive")
	s.expect(t, ": keepalive")
}

// A terminal session gets a final `end` event carrying the same body the result
// endpoint serves, and then the stream closes instead of polling forever.
func TestRunnerEventStreamEndsOnTerminalSession(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)
	ctx := context.Background()
	appendEvent(t, api, sessionID, "assistant_text", `{"text":"all done","done":true}`)
	if err := db.SetSessionCallback(ctx, api.dbPool, sessionID, "https://hook.example.test/x", "super-secret-hmac-key"); err != nil {
		t.Fatal(err)
	}
	ended := time.Now()
	if err := db.UpdateSessionStatus(ctx, api.dbPool, sessionID, "stopped", &ended); err != nil {
		t.Fatal(err)
	}

	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/stream", nil)
	// Everything persisted comes first — the end event never pre-empts the
	// transcript.
	s.expect(t, "id: 1")
	s.expect(t, "event: assistant_text")
	s.expectData(t)

	s.expect(t, "event: end")
	line := s.next(t)
	raw, ok := strings.CutPrefix(line, "data: ")
	if !ok {
		t.Fatalf("end line = %q, want a data line", line)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("end data is not JSON: %v", err)
	}
	if res["lifecycle"] != "ended" || res["terminal"] != true {
		t.Errorf("end result lifecycle/terminal = %v/%v, want ended/true", res["lifecycle"], res["terminal"])
	}
	if res["session_id"] != sessionID {
		t.Errorf("end result session_id = %v, want %s", res["session_id"], sessionID)
	}
	if res["last_assistant_message"] != "all done" {
		t.Errorf("end result last_assistant_message = %v", res["last_assistant_message"])
	}
	if strings.Contains(raw, "super-secret-hmac-key") || strings.Contains(raw, "callback_secret") {
		t.Errorf("the callback secret must never reach the stream: %s", raw)
	}
	s.waitEOF(t)
}

// The stream is behind the same credential as the rest of the contract, and an
// unknown session is a 404 answered before any streaming headers go out.
func TestRunnerEventStreamAuthAndUnknownSession(t *testing.T) {
	_, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)

	resp, err := http.Get(srv.URL + "/api/runner/sessions/" + sessionID + "/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+newUUID()+"/events/stream", nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session: %d, want 404", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("a 404 must not be framed as an event stream (Content-Type %q)", ct)
	}
}

// A data line must be exactly one line of JSON. Payloads come from a jsonb
// column so they are normally well-formed and compact already, but the framing
// cannot depend on that: anything else is sent as a JSON string rather than
// corrupting the stream with a raw newline.
func TestSSEDataFraming(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"{\"a\": 1,\n  \"b\": 2}", `{"a":1,"b":2}`},
		{`"already a string"`, `"already a string"`},
		{"not json at all", `"not json at all"`},
		{"two\nlines", `"two\nlines"`},
		{"", `""`},
	} {
		got := sseData(c.in)
		if got != c.want {
			t.Errorf("sseData(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("sseData(%q) = %q: a data line must never contain a newline", c.in, got)
		}
	}
}
