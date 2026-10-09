package server

// A desktop daemon that drops its connection usually comes straight back: an update restarts
// it, a network blip reconnects it. Marking its sessions lost the instant the socket closes
// tells everyone watching — the browser, a board polling its worker, a broker reading the
// result — that they ended in an error, a second or two before the daemon reports them alive
// again. A board acts on that at once (it comments on the card and revokes the session's board
// token), and nothing undoes it when the session returns.
//
// So the disconnect of a desktop daemon is held for a grace period first. If the daemon is back
// within it, nothing was ever marked. If it is not, the disconnect is processed exactly as it
// always was. A cluster pod (mode "runner") is not held: its sessions go to "disconnected",
// which is not an ending, and a message resumes them.
//
// While a daemon is away in its grace period, a message a person types in the browser for one
// of its sessions is kept and handed to the daemon when it registers again (the daemon in turn
// keeps it until the session is hosted again). If the daemon does not come back, the kept
// messages are dropped and the session's "error" status is what the chat shows them against.

import (
	"log"
	"sync"
	"time"
)

// daemonLostGrace is how long a desktop daemon may be away before its sessions are marked lost.
// A var so tests can run the disconnect synchronously (0) or shrink the wait.
var daemonLostGrace = 20 * time.Second

// maxHeldPerSession bounds the messages kept for one session of a daemon that is away.
const maxHeldPerSession = 32

// awayDaemons tracks desktop daemons inside their grace period.
type awayDaemons struct {
	mu   sync.Mutex
	gen  uint64
	away map[string]*awayDaemon // daemonID →
}

type awayDaemon struct {
	gen  uint64
	held map[string][][]byte // sessionID → raw agent_user_message frames, in order
}

// begin marks daemonID away and returns the generation of this absence.
func (a *awayDaemons) begin(daemonID string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.away == nil {
		a.away = make(map[string]*awayDaemon)
	}
	a.gen++
	// A daemon that drops again before an earlier absence was closed keeps what was held.
	held := map[string][][]byte{}
	if prev := a.away[daemonID]; prev != nil {
		held = prev.held
	}
	a.away[daemonID] = &awayDaemon{gen: a.gen, held: held}
	return a.gen
}

// end closes daemonID's absence and returns what was held for its sessions. With gen non-zero
// it only does so if that absence is still the current one (the grace timer's call: a daemon
// that came back and left again has a newer one, which the older timer must not close).
func (a *awayDaemons) end(daemonID string, gen uint64) (held map[string][][]byte, ended bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.away[daemonID]
	if cur == nil || (gen != 0 && cur.gen != gen) {
		return nil, false
	}
	delete(a.away, daemonID)
	return cur.held, true
}

// hold keeps raw for sessionID if daemonID is away, and reports whether it did.
func (a *awayDaemons) hold(daemonID, sessionID string, raw []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.away[daemonID]
	if cur == nil || len(cur.held[sessionID]) >= maxHeldPerSession {
		return false
	}
	cur.held[sessionID] = append(cur.held[sessionID], raw)
	return true
}

// HoldForAwayDaemon keeps a message for a session whose daemon is inside its grace period, and
// reports whether it did. False means there is nobody to keep it for.
func (h *Hub) HoldForAwayDaemon(sessionID string, raw []byte) bool {
	h.mu.RLock()
	daemonID, ok := h.sessionOwners[sessionID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	return h.away.hold(daemonID, sessionID, raw)
}

// deliverHeld hands a daemon that has just registered again the messages kept while it was
// away. Called after Register, so anything arriving from now on is routed to it directly.
func (h *Hub) deliverHeld(dc *DaemonConn) {
	held, _ := h.away.end(dc.ID, 0)
	for sessionID, msgs := range held {
		for _, raw := range msgs {
			select {
			case dc.send <- raw:
			default:
				// The daemon's buffer is full of something else: the rest is lost, as a message
				// sent to a busy daemon always was.
				log.Printf("daemon %s: send buffer full — a message held for session %s was dropped", dc.ID, sessionID)
			}
		}
	}
}
