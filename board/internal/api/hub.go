package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub is a minimal board-scoped broadcast: clients subscribe to one board and
// receive {type} pings on changes; they refetch over REST. Authenticated on
// the handshake — same rule as everything else, no anonymous readers.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*wsConn]bool // boardID → conns
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[*wsConn]bool{}}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
	// Echo "bearer" (the marker, never the token that follows it) as the
	// negotiated subprotocol. gorilla picks the first entry here that the
	// client also offered; without this the response would carry no
	// Sec-WebSocket-Protocol header at all, which some clients treat as a
	// negotiation failure.
	Subprotocols: []string{wsBearerSubprotocol},
}

// pingPeriod/pongWait detect dead sockets (idle mobile NATs, silently
// dropped ingress conns) well inside the 3600s ingress proxy-read-timeout,
// so the server closes and the client's existing reconnect kicks in instead
// of the client sitting on a stale socket until its next failed op. Vars
// (not consts) so tests can shrink them; see SetKeepaliveIntervals.
var (
	pingPeriod = 30 * time.Second
	pongWait   = 60 * time.Second
)

const writeWait = 5 * time.Second

// SetKeepaliveIntervals overrides the ping/pong intervals for tests and
// returns a func that restores the previous values.
func SetKeepaliveIntervals(ping, pong time.Duration) (restore func()) {
	prevPing, prevPong := pingPeriod, pongWait
	pingPeriod, pongWait = ping, pong
	return func() { pingPeriod, pongWait = prevPing, prevPong }
}

// wsConn wraps a websocket.Conn with a write mutex: gorilla requires all
// writes to a conn (data frames and control frames alike) to be serialized,
// and here both Broadcast and the per-conn ping ticker write to it.
type wsConn struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	done      chan struct{}
	closeOnce sync.Once
}

func newWSConn(c *websocket.Conn) *wsConn {
	return &wsConn{conn: c, done: make(chan struct{})}
}

func (c *wsConn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *wsConn) writeMessage(mt int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(mt, data)
}

func (c *wsConn) writePing() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
}

func (a *API) handleWS(w http.ResponseWriter, r *http.Request) {
	// A browser cannot set Authorization on a WebSocket upgrade request, so
	// it passes the token as the second entry of the subprotocol list
	// (["bearer", <token>] — see wsBearerToken). Only fill it in when no
	// Authorization header is already present, so non-browser callers
	// (CLI, MCP, Go tests) that set the header directly are unaffected.
	if r.Header.Get("Authorization") == "" {
		if tok := wsBearerToken(r); tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	p, err := a.Auth.Resolve(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	boardID := r.URL.Query().Get("board")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "board query param required")
		return
	}
	// Same gate as every board-scoped read: a token scoped to another board,
	// or a core token without card.read (password.change-only), must not be
	// able to subscribe to this board's change pings. Checked BEFORE the
	// upgrade so the refusal is a plain 403, not a socket that closes.
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newWSConn(conn)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	a.Hub.add(boardID, c)
	go a.Hub.reader(boardID, c)
	go a.Hub.pinger(boardID, c)
}

func (h *Hub) add(boardID string, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[boardID] == nil {
		h.subs[boardID] = map[*wsConn]bool{}
	}
	h.subs[boardID][c] = true
}

func (h *Hub) remove(boardID string, c *wsConn) {
	h.mu.Lock()
	delete(h.subs[boardID], c)
	h.mu.Unlock()
	c.close()
}

// reader drains client frames and detects disconnect; pong frames are
// intercepted by the gorilla library and routed to the pong handler set in
// handleWS before ever reaching here.
func (h *Hub) reader(boardID string, c *wsConn) {
	defer h.remove(boardID, c)
	c.conn.SetReadLimit(1 << 12)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// pinger periodically probes the connection; a missed pong lets the read
// deadline in handleWS/reader lapse and the reader tears the conn down, so
// this only needs to detect a failed *write* itself (e.g. a half-closed TCP
// stream that errors immediately rather than timing out).
func (h *Hub) pinger(boardID string, c *wsConn) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.writePing(); err != nil {
				h.remove(boardID, c)
				return
			}
		}
	}
}

// Broadcast pings all subscribers of a board.
func (h *Hub) Broadcast(boardID, typ string) {
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.subs[boardID]))
	for c := range h.subs[boardID] {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	msg := []byte(`{"type":"` + typ + `"}`)
	for _, c := range conns {
		if err := c.writeMessage(websocket.TextMessage, msg); err != nil {
			h.remove(boardID, c)
		}
	}
}
