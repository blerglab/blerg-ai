package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ingestFixture is a database-backed session with a daemon connection and a subscribed browser
// channel, for the ingest tests below. Requires TEST_DATABASE_URL.
type ingestFixture struct {
	ctx       context.Context
	hub       *Hub
	pool      *pgxpool.Pool
	dc        *DaemonConn
	sub       chan []byte
	sessionID string
}

func newIngestFixture(t *testing.T) *ingestFixture {
	t.Helper()
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	sessionID := "00000000-0000-0000-0000-0000000000c2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "laptop", "daemon", ""); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "idle", "/w/proj", "proj", "T", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	hub := NewHub()
	sub := make(chan []byte, 64)
	hub.Subscribe(sessionID, "b1", sub)
	dc := &DaemonConn{ID: daemonID, inputReceipts: true, send: make(chan []byte, 64)}
	hub.Register(dc)
	return &ingestFixture{ctx: ctx, hub: hub, pool: pool, sessionID: sessionID, dc: dc, sub: sub}
}

// cid is the client event id (a uuid column) a test names by a short label; label() reverses it.
func cid(label string) string {
	var n uint64
	for _, c := range []byte(label) {
		n = n*131 + uint64(c)
	}
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n&0xffffffffffff)
}

func label(id string, labels ...string) string {
	for _, l := range labels {
		if cid(l) == id {
			return l
		}
	}
	return id
}

func (f *ingestFixture) ingest(id, kind, payload, ts string) {
	HandleAgentEvent(f.ctx, f.hub, f.pool, f.dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: f.sessionID, ClientEventID: cid(id), Kind: kind, Ts: ts,
		Payload: json.RawMessage(payload),
	})
}

// acks drains the acks sent to the daemon, by client event id.
func (f *ingestFixture) acks() []string {
	var out []string
	for {
		select {
		case raw := <-f.dc.send:
			var a protocol.AgentEventAck
			if json.Unmarshal(raw, &a) == nil && a.Type == "agent_event_ack" {
				out = append(out, label(a.ClientEventID, "a", "b", "c", "m-1", "s-1"))
			}
		default:
			return out
		}
	}
}

// fanned drains the events fanned out to the subscriber.
func (f *ingestFixture) fanned() []protocol.AgentEvent {
	var out []protocol.AgentEvent
	for {
		select {
		case raw := <-f.sub:
			var ev protocol.AgentEvent
			if json.Unmarshal(raw, &ev) == nil && ev.Type == "agent_event" {
				out = append(out, ev)
			}
		default:
			return out
		}
	}
}

func (f *ingestFixture) stored(t *testing.T) []db.AgentEventRow {
	t.Helper()
	rows, err := db.ListAgentEvents(f.ctx, f.pool, f.sessionID, 0, 500)
	if err != nil {
		t.Fatalf("ListAgentEvents: %v", err)
	}
	return rows
}

func clientIDs(rows []db.AgentEventRow) string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = label(r.ClientEventID, "a", "b", "c")
	}
	return strings.Join(ids, ",")
}

// An event is stored with the daemon's time, not the time it reached the server, clamped to ten
// minutes either side of the server's clock; a missing or unreadable time is the server's. The live
// fan-out carries the stored time, so a reload shows the same label.
func TestAgentEventStoresDaemonTimeClamped(t *testing.T) {
	f := newIngestFixture(t)
	now := time.Now()
	recent := now.Add(-3 * time.Minute).UTC()
	f.ingest("e-recent", "turn_done", `{}`, recent.Format(time.RFC3339Nano))
	f.ingest("e-old", "turn_done", `{}`, now.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano))
	f.ingest("e-future", "turn_done", `{}`, now.Add(time.Hour).UTC().Format(time.RFC3339Nano))
	f.ingest("e-bad", "turn_done", `{}`, "yesterday")
	rows := f.stored(t)
	if len(rows) != 4 {
		t.Fatalf("stored %d events, want 4", len(rows))
	}
	near := func(got, want time.Time, slack time.Duration) bool {
		d := got.Sub(want)
		return d > -slack && d < slack
	}
	if !near(rows[0].Ts, recent, time.Millisecond) {
		t.Errorf("recent ts = %v, want the daemon's %v", rows[0].Ts, recent)
	}
	if !near(rows[1].Ts, now.Add(-eventTimeSkew), 5*time.Second) {
		t.Errorf("old ts = %v, want clamped to now-10m", rows[1].Ts)
	}
	if !near(rows[2].Ts, now.Add(eventTimeSkew), 5*time.Second) {
		t.Errorf("future ts = %v, want clamped to now+10m", rows[2].Ts)
	}
	if !near(rows[3].Ts, now, 5*time.Second) {
		t.Errorf("unreadable ts = %v, want about now", rows[3].Ts)
	}
	live := f.fanned()
	if len(live) != 4 {
		t.Fatalf("fanned out %d events, want 4", len(live))
	}
	for i, ev := range live {
		got, err := time.Parse(time.RFC3339Nano, ev.Ts)
		if err != nil || !near(got, rows[i].Ts, 2*time.Microsecond) {
			t.Errorf("live ts of %s = %q, stored %v", ev.ClientEventID, ev.Ts, rows[i].Ts)
		}
	}
}

func TestEventTimeClamp(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ts   string
		want time.Time
	}{
		{"", now},
		{"not a time", now},
		{"2026-10-14T11:55:00Z", now.Add(-5 * time.Minute)},
		{"2026-10-14T09:00:00Z", now.Add(-eventTimeSkew)},
		{"2026-10-14T13:00:00Z", now.Add(eventTimeSkew)},
	} {
		if got := eventTime(c.ts, now); !got.Equal(c.want) {
			t.Errorf("eventTime(%q) = %v, want %v", c.ts, got, c.want)
		}
	}
}

// A re-sent event that is already stored is acked again (so the daemon stops re-sending it) but
// changes nothing: not fanned out again, and its side effects are not replayed — a stale
// status_changed{running} must not flip an idle session back, nor an old model_changed restore an
// old model.
func TestAgentEventDuplicateHasNoSideEffects(t *testing.T) {
	f := newIngestFixture(t)
	f.ingest("m-1", "model_changed", `{"model":"claude-opus-5","effort":"high"}`, "")
	f.ingest("m-2", "model_changed", `{"model":"claude-sonnet-5","effort":"low"}`, "")
	f.ingest("s-1", "status_changed", `{"status":"running"}`, "")
	f.ingest("s-2", "status_changed", `{"status":"idle"}`, "")
	_ = f.fanned()
	_ = f.acks()

	f.ingest("m-1", "model_changed", `{"model":"claude-opus-5","effort":"high"}`, "") // re-sent
	f.ingest("s-1", "status_changed", `{"status":"running"}`, "")                     // re-sent

	if got := f.acks(); strings.Join(got, ",") != "m-1,s-1" {
		t.Errorf("acks = %v, want both duplicates acked", got)
	}
	if got := f.fanned(); len(got) != 0 {
		t.Errorf("duplicates fanned out again: %+v", got)
	}
	row, err := db.GetSession(f.ctx, f.pool, f.sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if row.Status != "idle" {
		t.Errorf("status = %q, want idle (a re-sent running must not flip it)", row.Status)
	}
	if row.Model == nil || *row.Model != "claude-sonnet-5" {
		t.Errorf("model = %v, want claude-sonnet-5 (a re-sent model_changed must not restore the old one)", row.Model)
	}
	if n := len(f.stored(t)); n != 4 {
		t.Errorf("stored %d events, want 4", n)
	}
}

// failingAppend makes the database refuse every append while down is set (a connection error, not
// a refused payload), and restores the real append when the test ends.
func failingAppend(t *testing.T, down *bool) {
	t.Helper()
	orig := appendAgentEvent
	appendAgentEvent = func(ctx context.Context, pool *pgxpool.Pool, sessionID, clientEventID, kind, payload string, ts time.Time) (int64, bool, error) {
		if *down {
			return 0, false, errors.New("dial tcp: connection refused")
		}
		return orig(ctx, pool, sessionID, clientEventID, kind, payload, ts)
	}
	t.Cleanup(func() { appendAgentEvent = orig })
}

// When an append fails, the session's later events wait (refused, unacknowledged) until the failed
// one is stored, so a re-sent event never lands after events that happened after it.
func TestAgentEventIngestStaysInOrderAfterAFailedAppend(t *testing.T) {
	f := newIngestFixture(t)
	down := false
	failingAppend(t, &down)
	f.ingest("a", "user_message", `{"text":"one","source":"chat"}`, "")
	down = true
	f.ingest("b", "user_message", `{"text":"two","source":"chat"}`, "")
	down = false
	f.ingest("c", "assistant_text", `{"text":"reply to b","done":true}`, "")
	if got := f.acks(); strings.Join(got, ",") != "a" {
		t.Fatalf("acks = %v, want only a (b failed, c held)", got)
	}
	if got := clientIDs(f.stored(t)); got != "a" {
		t.Fatalf("stored %s, want a alone while b is outstanding", got)
	}
	// The daemon re-sends its buffer oldest first: b (now storable), then c.
	f.ingest("b", "user_message", `{"text":"two","source":"chat"}`, "")
	f.ingest("c", "assistant_text", `{"text":"reply to b","done":true}`, "")
	if got := clientIDs(f.stored(t)); got != "a,b,c" {
		t.Fatalf("stored %s, want a,b,c", got)
	}
	if got := f.acks(); strings.Join(got, ",") != "b,c" {
		t.Fatalf("acks = %v, want b,c", got)
	}
}

// A database outage longer than the hold's limit does not invert the order: as long as the daemon
// keeps re-sending the failed event (every 30 s), the hold stays, and when the database is back the
// failed event is stored before the ones after it.
func TestAgentEventIngestOrderSurvivesALongOutage(t *testing.T) {
	f := newIngestFixture(t)
	down := false
	failingAppend(t, &down)
	clock := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	f.hub.ingest.now = func() time.Time { return clock }

	f.ingest("a", "user_message", `{"text":"one","source":"chat"}`, "")
	down = true
	f.ingest("b", "user_message", `{"text":"two","source":"chat"}`, "") // t0: fails, holds
	f.ingest("c", "assistant_text", `{"text":"reply to b","done":true}`, "")
	resend := func() {
		f.ingest("b", "user_message", `{"text":"two","source":"chat"}`, "")
		f.ingest("c", "assistant_text", `{"text":"reply to b","done":true}`, "")
	}
	for i := 0; i < 8; i++ { // four minutes of 30 s resends, all failing
		clock = clock.Add(30 * time.Second)
		resend()
	}
	if got := clientIDs(f.stored(t)); got != "a" {
		t.Fatalf("stored %s during the outage, want a alone", got)
	}
	down = false // the database is back; the next resend goes through in order
	clock = clock.Add(30 * time.Second)
	resend()
	if got := clientIDs(f.stored(t)); got != "a,b,c" {
		t.Fatalf("stored %s, want a,b,c (b before c despite the long outage)", got)
	}
}

// A payload the database refuses for what it is (SQLSTATE class 22) fails every time: it never holds
// its session, so the events after it are stored.
func TestAgentEventPoisonPayloadDoesNotHold(t *testing.T) {
	f := newIngestFixture(t)
	f.ingest("a", "user_message", `{"text":"one","source":"chat"}`, "")
	f.ingest("b", "user_message", `{"text":`, "") // invalid JSON: jsonb refuses it (22P02)
	f.ingest("c", "assistant_text", `{"text":"after","done":true}`, "")
	if got := clientIDs(f.stored(t)); got != "a,c" {
		t.Fatalf("stored %s, want a,c (the poison event must not hold c)", got)
	}
	if got := f.acks(); strings.Join(got, ",") != "a,c" {
		t.Fatalf("acks = %v, want a,c (b unacked, as before)", got)
	}
}

// A hold ends once nobody has re-sent its event for ingestHoldMax (the daemon lost it), and its
// entry goes; a different event failing later holds afresh; a stored event ends its hold.
func TestIngestHoldExpiresOnInactivity(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock := t0
	g := ingestHolds{now: func() time.Time { return clock }}
	transient := errors.New("connection refused")
	g.failed("s", "b", transient)
	clock = t0.Add(time.Second)
	if g.admit("s", "c") {
		t.Fatal("c admitted while b holds the session")
	}
	if !g.admit("s", "b") {
		t.Fatal("the held event itself must be admitted")
	}
	if !g.admit("other", "x") {
		t.Fatal("another session is not held")
	}
	// b is re-sent and fails again at +90 s: the hold is renewed, so +3 min is still held.
	clock = t0.Add(90 * time.Second)
	g.failed("s", "b", transient)
	clock = t0.Add(3 * time.Minute)
	if g.admit("s", "c") {
		t.Fatal("c admitted while b is still being re-sent")
	}
	// Nobody re-sends b any more: the hold ends and its entry is removed.
	clock = t0.Add(90*time.Second + ingestHoldMax + time.Second)
	if !g.admit("s", "c") {
		t.Fatal("c still refused after b was abandoned")
	}
	if _, ok := g.m["s"]; ok {
		t.Fatal("the expired hold's entry is kept")
	}
	g.failed("s", "e", transient)
	if g.admit("s", "f") {
		t.Fatal("f admitted while e holds the session")
	}
	g.stored("s", "e")
	if !g.admit("s", "f") || len(g.m) != 0 {
		t.Fatalf("f refused or the entry kept after e was stored (%v)", g.m)
	}
	g.failed("s", "g", &pgconn.PgError{Code: "22P02"})
	g.failed("s", "h", &pgconn.PgError{Code: "54000"})
	if len(g.m) != 0 {
		t.Fatal("a refused payload held the session")
	}
	g.failed("s", "i", transient)
	g.forget("s")
	if len(g.m) != 0 {
		t.Fatal("forget kept the hold")
	}
}
