package server

// The live stream (events_live.go): what the runner's own browser gets over its websocket —
// replay page, replay_done, status, then persisted events, typing deltas and status changes as
// the hub sees them — as Server-Sent Events for the credential that started the session.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// expectFrame asserts the next SSE record is `event: <name>` with a JSON data line, returning the
// data. withID says an `id:` line (the seq) comes first.
func (s *sseStream) expectFrame(t *testing.T, name string, withID bool) (int64, map[string]any) {
	t.Helper()
	var id int64
	if withID {
		line := s.next(t)
		raw, ok := strings.CutPrefix(line, "id: ")
		if !ok {
			t.Fatalf("SSE line = %q, want an id line", line)
		}
		if _, err := json.Number(raw).Int64(); err != nil {
			t.Fatalf("id line %q is not an integer", line)
		}
		id, _ = json.Number(raw).Int64()
	}
	s.expect(t, "event: "+name)
	return id, s.expectData(t)
}

// The opening sequence: the persisted events in seq order (each with its seq as the id), the
// replay_done marker mirroring the websocket's, then the session's status. Afterwards a transient
// delta fanned out through the hub arrives as an agent_event with no id, and a state change
// broadcast to browsers arrives as a status frame. The session ending closes the stream with end.
func TestRunnerEventsLiveReplayThenLive(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)
	var seqs []int64
	for i := 0; i < 3; i++ {
		seqs = append(seqs, appendEvent(t, api, sessionID, "assistant_text", `{"text":"hi","done":true}`))
	}

	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/live", nil)
	if ct := s.resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	for i, want := range seqs {
		id, data := s.expectFrame(t, "agent_event", true)
		if id != want {
			t.Fatalf("frame %d id = %d, want %d", i, id, want)
		}
		if data["type"] != "agent_event" || data["session_id"] != sessionID || data["kind"] != "assistant_text" ||
			data["client_event_id"] == "" || data["ts"] == "" || data["seq"] != float64(want) {
			t.Fatalf("frame %d = %v: want the AgentEvent JSON the browser receives", i, data)
		}
		if _, ok := data["transient"]; ok {
			t.Errorf("a persisted event must not carry transient: %v", data)
		}
	}
	_, done := s.expectFrame(t, "replay_done", false)
	if done["session_id"] != sessionID || done["last_seq"] != float64(seqs[2]) || done["has_more"] != false || done["server_time"] == "" {
		t.Fatalf("replay_done = %v", done)
	}
	if _, ok := done["older"]; ok {
		t.Errorf("a forward replay must not say older: %v", done)
	}
	_, status := s.expectFrame(t, "status", false)
	if status["session_id"] != sessionID || status["status"] != "running" {
		t.Fatalf("status = %v", status)
	}

	// A typing delta: fanned out only, never persisted, delivered as-is.
	delta, _ := json.Marshal(protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(), Ts: time.Now().UTC().Format(time.RFC3339Nano),
		Kind: "assistant_text", Payload: json.RawMessage(`{"text":"typ"}`), Transient: true,
	})
	api.hub.FanOutSessionOutput(sessionID, delta)
	_, ev := s.expectFrame(t, "agent_event", false)
	if ev["transient"] != true || ev["kind"] != "assistant_text" {
		t.Fatalf("transient frame = %v", ev)
	}

	// A persisted event recorded after the replay: with its seq.
	seq := appendEvent(t, api, sessionID, "tool_use", `{"name":"Read"}`)
	ev2, _ := json.Marshal(protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(), Seq: seq,
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: "tool_use", Payload: json.RawMessage(`{"name":"Read"}`),
	})
	api.hub.FanOutSessionOutput(sessionID, ev2)
	if id, data := s.expectFrame(t, "agent_event", true); id != seq || data["kind"] != "tool_use" {
		t.Fatalf("live persisted frame = %d %v", id, data)
	}

	// A state change the hub broadcasts to browsers.
	api.hub.BroadcastJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sessionID, Status: "waiting"})
	if _, st := s.expectFrame(t, "status", false); st["status"] != "waiting" || st["session_id"] != sessionID {
		t.Fatalf("status frame = %v", st)
	}
	// Another session's state change is not this stream's business.
	api.hub.BroadcastJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: newUUID(), Status: "error"})

	// The end: the row is terminal and the hub says so; a final status, then end, then EOF.
	now := time.Now()
	if err := db.UpdateSessionStatus(context.Background(), api.dbPool, sessionID, "ended", &now); err != nil {
		t.Fatal(err)
	}
	api.hub.BroadcastJSON(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: sessionID, Status: "ended"})
	if _, st := s.expectFrame(t, "status", false); st["status"] != "ended" {
		t.Fatalf("final status frame = %v", st)
	}
	if _, end := s.expectFrame(t, "end", false); end["terminal"] != true || end["session_id"] != sessionID {
		t.Fatalf("end frame = %v", end)
	}
	s.waitEOF(t)
}

// tail=1 opens from the end of the transcript: the newest page, marked older with has_older and
// first_seq, so the client pages backwards from there — the websocket's Tail semantics.
func TestRunnerEventsLiveTail(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)
	var seqs []int64
	for i := 0; i < 5; i++ {
		seqs = append(seqs, appendEvent(t, api, sessionID, "assistant_text", `{"text":"hi"}`))
	}
	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/live?tail=1&limit=2", nil)
	for _, want := range seqs[3:] {
		if id, _ := s.expectFrame(t, "agent_event", true); id != want {
			t.Fatalf("id = %d, want %d", id, want)
		}
	}
	_, done := s.expectFrame(t, "replay_done", false)
	if done["older"] != true || done["has_older"] != true || done["first_seq"] != float64(seqs[3]) || done["last_seq"] != float64(seqs[4]) {
		t.Fatalf("replay_done = %v", done)
	}
	s.expectFrame(t, "status", false)

	// after_seq resumes forward from a cursor.
	s2 := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/live?after_seq=4", nil)
	if id, _ := s2.expectFrame(t, "agent_event", true); id != seqs[4] {
		t.Fatalf("after_seq id = %d, want %d", id, seqs[4])
	}
	if _, done := s2.expectFrame(t, "replay_done", false); done["last_seq"] != float64(seqs[4]) {
		t.Fatalf("replay_done = %v", done)
	}
}

// A session already over when the stream opens gets the replay, its status and end, then closes.
func TestRunnerEventsLiveEndedSession(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)
	appendEvent(t, api, sessionID, "assistant_text", `{"text":"bye"}`)
	now := time.Now()
	if err := db.UpdateSessionStatus(context.Background(), api.dbPool, sessionID, "ended", &now); err != nil {
		t.Fatal(err)
	}
	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/live", nil)
	s.expectFrame(t, "agent_event", true)
	s.expectFrame(t, "replay_done", false)
	if _, st := s.expectFrame(t, "status", false); st["status"] != "ended" {
		t.Fatalf("status = %v", st)
	}
	s.expectFrame(t, "end", false)
	s.waitEOF(t)
}

// A silent session still gets a keepalive comment inside the proxy idle window.
func TestRunnerEventsLiveKeepalive(t *testing.T) {
	_, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, 20*time.Millisecond)
	s := openSSEStream(t, srv, "/api/runner/sessions/"+sessionID+"/events/live", nil)
	s.expectFrame(t, "replay_done", false)
	s.expectFrame(t, "status", false)
	s.expect(t, ": keepalive")
}

// Authorization is settled before any streaming header goes out: no credential is a 401, and an
// agent token of an account that did not start the session is a 404 that is an ordinary error
// response, never an event stream.
func TestRunnerEventsLiveAuth(t *testing.T) {
	api, srv, sessionID := streamTestAPI(t, 10*time.Millisecond, time.Hour)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	stranger := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-stranger", OnBehalfOf: "someone-else", Aud: coreAuthAudience, Kind: "agent",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	for _, c := range []struct {
		name string
		tok  string
		want int
	}{
		{"no credential", "", http.StatusUnauthorized},
		{"another account's agent token", stranger, http.StatusNotFound},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+sessionID+"/events/live", nil)
		if c.tok != "" {
			req.Header.Set("Authorization", "Bearer "+c.tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
		if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("%s: a refusal must not be framed as an event stream (Content-Type %q)", c.name, ct)
		}
	}
}
