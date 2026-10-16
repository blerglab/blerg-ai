package server

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// In-order ingest of a session's agent events.
//
// The server numbers a session's events (seq) in the order they are stored. A daemon sends them in
// the order they happened and re-sends every unacknowledged one, oldest first, on reconnect and
// every 30 s (daemon/agentsession.go). If one append fails and later events of the same session
// are stored, the re-sent event gets a newer seq than events that happened after it: a person's
// message drawn after its own reply.
//
// So when an append fails, the session is held: until the failed event is stored, every later event
// of that session is refused unacknowledged (the daemon keeps it and re-sends it, in order, behind the
// failed one). While the daemon keeps re-sending the failed event the hold stays, however long the
// database is away — an outage must not invert the order the hold protects. It ends when the event is
// stored, or once nobody has tried to store it for ingestHoldMax: the daemon lost it (it was
// restarted; its buffer is in memory) or went away, and the session must not wait for it for good.
// The daemon's buffer is bounded (pendingCap, 10 000 events): a session held that long stops at the
// cap and waits, it does not drop events.
//
// A payload the database refuses for what it is (a data exception, SQLSTATE class 22: invalid JSON,
// an untranslatable character; or a program limit, class 54: a value too large) fails the same way
// every time. It never holds its session: waiting for it would stop the transcript for good.
//
// Holds are per server process: a daemon is connected to one server, and after a server restart the
// daemon re-sends its buffer oldest first anyway.

// ingestHoldMax is how long a hold outlives the last attempt to store its event: a few daemon resend
// periods (30 s each).
var ingestHoldMax = 2 * time.Minute

// ingestRefusalLogEvery bounds the "held" log line to one per session per this period.
const ingestRefusalLogEvery = time.Minute

type ingestHold struct {
	clientEventID string
	lastAttempt   time.Time // the last time the held event was tried
	refused       int       // later events refused since the last log line
	loggedAt      time.Time
}

type ingestHolds struct {
	mu  sync.Mutex
	m   map[string]*ingestHold // session id → its hold
	now func() time.Time       // the clock (nil: time.Now); tests set it
}

func (g *ingestHolds) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// admit reports whether an event may be stored now: false while the session is held for an earlier
// event that is not this one. A refusal is logged at most once a minute per session.
func (g *ingestHolds) admit(sessionID, clientEventID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	hold := g.m[sessionID]
	if hold == nil || hold.clientEventID == clientEventID {
		return true
	}
	now := g.clock()
	if now.Sub(hold.lastAttempt) > ingestHoldMax {
		log.Printf("agent_event %s: nobody re-sent the held event %s for %s; no longer holding the session",
			sessionID, hold.clientEventID, ingestHoldMax)
		delete(g.m, sessionID)
		return true
	}
	hold.refused++
	if now.Sub(hold.loggedAt) >= ingestRefusalLogEvery {
		log.Printf("agent_event %s: held behind %s, which failed to store: %d later event(s) refused unacknowledged",
			sessionID, hold.clientEventID, hold.refused)
		hold.loggedAt, hold.refused = now, 0
	}
	return false
}

// failed records that the append of an event failed with err: the session is held for it (or its
// hold renewed), unless the payload itself was refused (ingestPoison).
func (g *ingestHolds) failed(sessionID, clientEventID string, err error) {
	if ingestPoison(err) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if hold := g.m[sessionID]; hold != nil && hold.clientEventID == clientEventID {
		hold.lastAttempt = now
		return
	}
	if g.m == nil {
		g.m = make(map[string]*ingestHold)
	}
	g.m[sessionID] = &ingestHold{clientEventID: clientEventID, lastAttempt: now}
}

// stored records that an event was stored (or found already stored): a hold for it ends.
func (g *ingestHolds) stored(sessionID, clientEventID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if hold := g.m[sessionID]; hold != nil && hold.clientEventID == clientEventID {
		delete(g.m, sessionID)
	}
}

// forget drops a session's hold (the session ended).
func (g *ingestHolds) forget(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.m, sessionID)
}

// ingestPoison reports whether an append failed because of the payload itself, which fails the same
// way on every attempt: a data exception (SQLSTATE class 22) or a program limit (class 54).
func ingestPoison(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	return strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "54")
}
