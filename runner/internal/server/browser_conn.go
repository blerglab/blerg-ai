package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

// wsBearerSubprotocol is the first element of the WebSocket subprotocol list a
// browser must offer on /ws/browser, immediately followed by the access token:
//
//	new WebSocket(url, ["bearer", accessToken])
//
// The browser WebSocket API cannot set request headers, so the REST gate's
// `Authorization: Bearer <token>` has no equivalent on an upgrade request. The
// subprotocol list is the one client-controlled, header-carried channel the API
// does expose (Sec-WebSocket-Protocol), and it is what every browser WS auth
// scheme uses. The server echoes back only "bearer" (never the token) as the
// negotiated subprotocol — see browserUpgrader.Subprotocols.
const wsBearerSubprotocol = "bearer"

var browserUpgrader = websocket.Upgrader{
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	EnableCompression: true,
	// Echo "bearer" (the marker, never the token that follows it) as the
	// negotiated subprotocol. gorilla picks the first entry here that the
	// client also offered; without this the response would carry no
	// Sec-WebSocket-Protocol header at all, which some clients treat as a
	// negotiation failure.
	Subprotocols: []string{wsBearerSubprotocol},
	CheckOrigin:  checkBrowserWSOrigin,
}

// browserWSAllowedOrigins parses BLERG_RUNNER_ALLOWED_ORIGINS — a
// comma-separated list of scheme://host origins permitted to open a browser
// WebSocket cross-origin. Same shape as blerg-core's
// BLERG_CORE_ALLOWED_RETURN_ORIGINS (core/cmd/blerg-core/main.go), which is the
// only allowlist convention this repo already has.
//
// Parsed once, lazily: the value is process configuration and never changes
// after boot.
var browserWSAllowedOrigins = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, o := range strings.Split(os.Getenv("BLERG_RUNNER_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			out[strings.ToLower(o)] = true
		}
	}
	return out
})

// checkBrowserWSOrigin replaces the old unconditional `return true`. A browser
// always sends Origin on a WebSocket handshake, so this is the CSRF-equivalent
// defence for the upgrade: without it any page on the internet could open a
// socket to a logged-in user's runner.
//
// Accepted: a request with no Origin at all (non-browser clients — the daemon
// tooling and Go tests — which cannot be driven by a hostile page anyway), a
// same-origin request (Origin's scheme+host equals the request's own scheme —
// from TLS or X-Forwarded-Proto behind the ingress — and Host, which is how
// both shipped deployment shapes work: desktop compose on localhost:<port>,
// and k8s behind the runner.<DOMAIN> ingress host), and any origin explicitly
// listed in BLERG_RUNNER_ALLOWED_ORIGINS.
func checkBrowserWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	// Same-origin means same scheme AND host: an http:// page on the same
	// hostname is a different origin from the https:// app and must not pass.
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if strings.EqualFold(u.Scheme+"://"+u.Host, scheme+"://"+r.Host) {
		return true
	}
	return browserWSAllowedOrigins()[strings.ToLower(u.Scheme+"://"+u.Host)]
}

// wsBearerToken extracts the access token a browser passed via the
// Sec-WebSocket-Protocol header as ["bearer", "<token>"]. Returns "" when the
// convention isn't followed.
func wsBearerToken(r *http.Request) string {
	protos := websocket.Subprotocols(r)
	for i, p := range protos {
		if p == wsBearerSubprotocol && i+1 < len(protos) {
			return protos[i+1]
		}
	}
	return ""
}

// WebSocket keepalive for browser connections. Without server-initiated pings,
// an idle connection is reaped by the ingress proxy's ~60s read timeout; the
// browser then reconnects and re-replays history, resetting the terminal and the
// user's scroll position. browserPingPeriod must be shorter than both
// browserPongWait and the proxy read timeout. It is a var so tests can shorten it.
var browserPingPeriod = 25 * time.Second

const (
	browserPongWait       = 60 * time.Second
	browserWriteWait      = 10 * time.Second
	historyReplayMaxBytes = 256 * 1024
)

// ServeBrowser returns an http.HandlerFunc that upgrades the connection to
// WebSocket and serves browser clients.
//
// authorize gates the upgrade the same way Task 7's authBrowser gates every
// REST endpoint — this socket pushes the full daemon/session/message state on
// connect and accepts state-changing commands (spawn_session, send_input,
// interrupt_session, ...), so it is exactly as sensitive as the REST surface
// and must not be reachable without a verified core-issued token. In
// production this is (*API).AuthorizeBrowserWS. A nil authorize fails CLOSED
// (every upgrade rejected): a missing gate must never silently mean "open".
//
// authorize also returns the verified account id behind the token, which is
// remembered on the BrowserConn: socket commands that act on someone else's
// session (resuming a cluster session with its launcher's personal
// credentials) must be able to tell who is asking.
func (h *Hub) ServeBrowser(dbPool *pgxpool.Pool, authorize func(*http.Request) (accountID, sessionID string, ok bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		accountID, sessionID, ok := "", "", false
		if authorize != nil {
			accountID, sessionID, ok = authorize(r)
		}
		if !ok {
			// Reject BEFORE the upgrade: a 401 on the handshake is visible to
			// the client, whereas closing an already-upgraded socket would
			// have leaked a successful connection first.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		wsConn, err := browserUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("browser ws upgrade error: %v", err)
			return
		}

		// The connection outlives the upgrade request, so its context keeps
		// the request's values but not its cancellation.
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		bc := &BrowserConn{
			ID:        newUUID(),
			AccountID: accountID,
			SessionID: sessionID,
			send:      make(chan []byte, 256),
			ctx:       ctx,
			cancel:    cancel,
			subs:      make(map[string]context.CancelFunc),
		}
		h.RegisterBrowser(bc)

		// Send initial_state before starting the read pump so the browser
		// gets it as its very first message.
		sendInitialState(ctx, bc, h, dbPool)

		conn := wsConn
		go browserWritePump(bc, conn)
		go browserReadPump(h, bc, conn, dbPool) //nolint:contextcheck // the read pump lives as long as the socket, not the upgrade request
	}
}

// sendInitialState assembles and sends the initial_state message to bc.
func sendInitialState(ctx context.Context, bc *BrowserConn, h *Hub, dbPool *pgxpool.Pool) {
	daemons := h.GetAllDaemons()
	daemonInfos := make([]protocol.DaemonInfo, 0, len(daemons))
	for _, d := range daemons {
		if d.Mode == "runner" {
			continue // a cluster session pod, not a machine the owner picks
		}
		daemonInfos = append(daemonInfos, protocol.DaemonInfo{
			ID:        d.ID,
			Name:      d.Name,
			Mode:      d.Mode,
			ReposRoot: d.CurrentReposRoot(),
			Status:    "connected",
			Version:   d.Version,
		})
	}

	sessionInfos := []protocol.SessionInfo{}
	if dbPool != nil {
		rows, err := db.ListSessions(ctx, dbPool)
		if err != nil {
			log.Printf("browser initial_state ListSessions: %v", err)
		} else {
			for _, r := range rows {
				if !canSeeAccount(bc.AccountID, &r) { // a private session is its owner's alone
					continue
				}
				// Use the shared row→info mapper so every SessionInfo field
				// (including unread/starred) is populated consistently — a manual
				// duplicate here previously dropped those flags on reconnect.
				sessionInfos = append(sessionInfos, sessionRowToInfo(r, bc.AccountID))
			}
		}
	}

	h.mu.RLock()
	sv := h.serverVersion
	h.mu.RUnlock()

	// Roll-up messages (open + recent), so the Chat surface is populated on load.
	messageInfos := []protocol.MessageInfo{}
	if dbPool != nil {
		if rows, err := db.ListMessagesFor(ctx, dbPool, defaultMessageLimit, bc.AccountID); err != nil {
			log.Printf("browser initial_state ListMessages: %v", err)
		} else {
			for _, r := range rows {
				messageInfos = append(messageInfos, messageRowToInfo(r))
			}
		}
	}

	msg := protocol.InitialState{
		Type:          "initial_state",
		Daemons:       daemonInfos,
		Sessions:      sessionInfos,
		Messages:      messageInfos,
		ServerVersion: sv,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("browser marshal initial_state: %v", err)
		return
	}
	select {
	case bc.send <- data:
	case <-ctx.Done():
	}
}

// browserWritePump drains bc.send and writes outbound messages to the browser.
// It exits when bc.ctx is cancelled or the websocket connection fails.
func browserWritePump(bc *BrowserConn, conn *websocket.Conn) {
	ticker := time.NewTicker(browserPingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()
	for {
		select {
		case msg, ok := <-bc.send:
			_ = conn.SetWriteDeadline(time.Now().Add(browserWriteWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("browser write pump %s: %v", bc.ID, err)
				return
			}
		case <-ticker.C:
			// Keepalive: a ping every browserPingPeriod produces upstream→client
			// traffic so the ingress proxy doesn't reap the idle connection.
			_ = conn.SetWriteDeadline(time.Now().Add(browserWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-bc.ctx.Done():
			return
		}
	}
}

// releaseFocus releases bc's claim as the active browser for sessionID. If
// another subscriber is promoted, it is sent a focus_granted notification.
// Call this while other browsers are still in h.subscriptions (before removing
// bc's own subscription), so there is something to promote.
func releaseFocus(h *Hub, bc *BrowserConn, sessionID string) {
	newActive := h.ReleaseActiveBrowser(sessionID, bc.ID)
	if newActive == "" {
		return
	}
	msg, err := json.Marshal(protocol.FocusGranted{
		Type:      "focus_granted",
		SessionID: sessionID,
	})
	if err != nil {
		return
	}
	h.SendToBrowser(newActive, msg)
}

// browserReadPump reads inbound messages from the browser and dispatches them.
func browserReadPump(h *Hub, bc *BrowserConn, conn *websocket.Conn, dbPool *pgxpool.Pool) {
	ctx := bc.ctx

	defer func() {
		// Cancel context — signals write pump and all subscription forwarders to exit.
		bc.cancel()
		// Clean up all active subscriptions. Release focus BEFORE unsubscribing so
		// ReleaseActiveBrowser can still see other browsers in h.subscriptions.
		bc.subMu.Lock()
		for sessionID := range bc.subs {
			bc.subs[sessionID]()
			delete(bc.subs, sessionID)
			releaseFocus(h, bc, sessionID)
			h.Unsubscribe(sessionID, bc.ID)
		}
		bc.subMu.Unlock()
		h.UnregisterBrowser(bc.ID)
		_ = conn.Close()
	}()

	// Keepalive: expect a pong (browsers auto-reply to our pings) within
	// browserPongWait; each pong pushes the deadline out. A genuinely dead
	// connection then fails the read and is cleaned up promptly.
	_ = conn.SetReadDeadline(time.Now().Add(browserPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(browserPongWait))
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseNoStatusReceived,
			) {
				log.Printf("browser read pump %s: %v", bc.ID, err)
			}
			return
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		// Privacy: a frame that names a session this socket's account may not
		// see is dropped before any handler runs. It covers every session
		// command (subscribe, input, resize, scrollback, messages, model,
		// interrupt, read, star) in one place, so a command added later
		// inherits it. The refusal is silent, like an unknown session.
		if sid := inboundSessionID(raw); sid != "" && !h.accountCanSee(bc.AccountID, sid) {
			log.Printf("browser %s: %s for a session it may not see: dropped", bc.ID, env.Type)
			continue
		}

		switch env.Type {
		case "activity":
			// Throttled user-interaction signal; gates push vs in-app toast.
			h.MarkActivity()

		case "subscribe_board":
			var msg protocol.SubscribeBoard
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed subscribe_board: %v", bc.ID, err)
				continue
			}
			h.SubscribeBoard(msg.BoardID, bc.ID)

		case "unsubscribe_board":
			var msg protocol.UnsubscribeBoard
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed unsubscribe_board: %v", bc.ID, err)
				continue
			}
			h.UnsubscribeBoard(msg.BoardID, bc.ID)

		case "subscribe_session":
			var msg protocol.SubscribeSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed subscribe_session: %v", bc.ID, err)
				continue
			}
			handleSubscribeSession(ctx, h, bc, dbPool, msg.SessionID)

		case "unsubscribe_session":
			var msg protocol.UnsubscribeSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed unsubscribe_session: %v", bc.ID, err)
				continue
			}
			handleUnsubscribeSession(h, bc, msg.SessionID)

		case "send_input":
			var msg protocol.SendInput
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed send_input: %v", bc.ID, err)
				continue
			}
			dc := h.FindDaemonForSession(msg.SessionID)
			if dc == nil {
				log.Printf("browser %s send_input: no daemon found for session %s", bc.ID, msg.SessionID)
				continue
			}
			fwd, _ := json.Marshal(protocol.SendInput{
				Type:      "send_input",
				SessionID: msg.SessionID,
				Data:      msg.Data,
			})
			select {
			case dc.send <- fwd:
			default:
				log.Printf("browser %s send_input: daemon %s send channel full", bc.ID, dc.ID)
			}

		case "resize_session":
			var msg protocol.ResizeSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed resize_session: %v", bc.ID, err)
				continue
			}
			// Only the active browser drives PTY size; ignore resizes from others.
			if !h.IsActiveBrowser(msg.SessionID, bc.ID) {
				continue
			}
			dc := h.FindDaemonForSession(msg.SessionID)
			if dc == nil {
				log.Printf("browser %s resize_session: no daemon found for session %s", bc.ID, msg.SessionID)
				continue
			}
			h.SetSessionPTYCols(msg.SessionID, msg.Cols)
			fwd, _ := json.Marshal(protocol.ResizeSession{
				Type:      "resize_session",
				SessionID: msg.SessionID,
				Cols:      msg.Cols,
				Rows:      msg.Rows,
			})
			select {
			case dc.send <- fwd:
			default:
				log.Printf("browser %s resize_session: daemon %s send channel full", bc.ID, dc.ID)
			}

		case "request_scrollback":
			var msg protocol.RequestScrollback
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed request_scrollback: %v", bc.ID, err)
				continue
			}
			dc := h.FindDaemonForSession(msg.SessionID)
			if dc == nil {
				continue
			}
			fwd, _ := json.Marshal(protocol.RequestScrollback{
				Type:      "request_scrollback",
				SessionID: msg.SessionID,
				MaxLines:  msg.MaxLines,
			})
			select {
			case dc.send <- fwd:
			default:
				log.Printf("browser %s request_scrollback: daemon %s send channel full", bc.ID, dc.ID)
			}

		case "spawn_session":
			var msg protocol.BrowserSpawnSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed spawn_session: %v", bc.ID, err)
				continue
			}
			if _, err := h.forwardBrowserSpawn(bc, msg, dbPool); err != nil {
				log.Printf("browser %s spawn_session: %v", bc.ID, err)
				continue
			}

		case "subscribe_agent_events":
			var msg protocol.SubscribeAgentEvents
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed subscribe_agent_events: %v", bc.ID, err)
				continue
			}
			handleSubscribeAgentEvents(ctx, h, bc, dbPool, msg)

		case "agent_user_message":
			var msg protocol.AgentUserMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed agent_user_message: %v", bc.ID, err)
				continue
			}
			if h.FindDaemonForSession(msg.SessionID) == nil {
				// Disconnected cluster session: re-create the runner Job with
				// this message as the resume prompt. bc.AccountID is passed so
				// the resume only re-mints personal credentials when the
				// requester is the account the session was launched under.
				resumeClusterSession(ctx, h, dbPool, msg.SessionID, msg.Text, bc.AccountID, bc.SessionID)
				continue
			}
			forwardToSessionDaemon(h, bc, msg.SessionID, protocol.AgentUserMessage{
				Type: "agent_user_message", SessionID: msg.SessionID, Text: msg.Text, Source: msg.Source,
			})

		case "set_session_model":
			var msg protocol.SetSessionModel
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed set_session_model: %v", bc.ID, err)
				continue
			}
			fwd, ok := sanitizeSetSessionModel(msg)
			if !ok {
				log.Printf("browser %s set_session_model %s: refused model %q / effort %q", bc.ID, msg.SessionID, msg.Model, msg.Effort)
				continue
			}
			forwardToSessionDaemon(h, bc, msg.SessionID, fwd)

		case "interrupt_session":
			var msg protocol.InterruptSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed interrupt_session: %v", bc.ID, err)
				continue
			}
			forwardToSessionDaemon(h, bc, msg.SessionID, protocol.InterruptSession{
				Type: "interrupt_session", SessionID: msg.SessionID,
			})

		case "mark_session_read":
			var msg protocol.MarkSessionRead
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed mark_session_read: %v", bc.ID, err)
				continue
			}
			if dbPool == nil {
				log.Printf("browser %s mark_session_read: no db pool", bc.ID)
				continue
			}
			if err := db.SetSessionUnread(ctx, dbPool, msg.SessionID, false); err != nil {
				log.Printf("browser %s mark_session_read %s: %v", bc.ID, msg.SessionID, err)
				continue
			}
			h.BroadcastJSON(protocol.SessionReadChanged{
				Type:      "session_read_changed",
				SessionID: msg.SessionID,
			})

		case "set_session_star":
			var msg protocol.SetSessionStar
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed set_session_star: %v", bc.ID, err)
				continue
			}
			if dbPool == nil {
				log.Printf("browser %s set_session_star: no db pool", bc.ID)
				continue
			}
			if err := db.SetSessionStarred(ctx, dbPool, msg.SessionID, msg.Starred); err != nil {
				log.Printf("browser %s set_session_star %s: %v", bc.ID, msg.SessionID, err)
				continue
			}
			h.BroadcastJSON(protocol.SessionStarChanged{
				Type:      "session_star_changed",
				SessionID: msg.SessionID,
				Starred:   msg.Starred,
			})

		default:
			log.Printf("browser %s unknown message type: %s", bc.ID, env.Type)
		}
	}
}

// forwardBrowserSpawn validates and forwards a browser-initiated spawn_session
// request to the target daemon. Extracted out of the "spawn_session" case in
// browserReadPump's read loop so it is directly testable: that loop reads
// from a live websocket connection and isn't callable in isolation.
//
// Applied to every browser-WS spawn: the REST path validates too, but the
// browser socket is a second, independent entry point into the same daemon
// spawn, so it must not skip the check. Like the REST path it mints the
// per-session messaging token (dbPool nil → no token, spawn still proceeds).
func (h *Hub) forwardBrowserSpawn(_ *BrowserConn, msg protocol.BrowserSpawnSession, dbPool *pgxpool.Pool) (sessionID string, err error) {
	if err := validateRepoName(msg.Repo); err != nil {
		return "", fmt.Errorf("rejected repo %q: %w", msg.Repo, err)
	}
	dc := h.GetDaemon(msg.DaemonID)
	if dc == nil {
		return "", fmt.Errorf("daemon %s not found", msg.DaemonID)
	}
	sessionID = newUUID()
	fwd, err := json.Marshal(protocol.SpawnSession{
		Type:          "spawn_session",
		SessionID:     sessionID,
		ProjectPath:   "",
		Repo:          msg.Repo,
		Title:         msg.Title,
		Cols:          80,
		Rows:          24,
		InitialPrompt: msg.InitialPrompt,
		SessionToken:  mintSpawnSessionToken(context.Background(), dbPool, sessionID, dc, msg.Repo, msg.Title, ""),
	})
	if err != nil {
		return "", fmt.Errorf("marshal spawn_session: %w", err)
	}
	select {
	case dc.send <- fwd:
	default:
		abortSpawnSessionToken(context.Background(), dbPool, sessionID)
		return "", fmt.Errorf("daemon %s send channel full", dc.ID)
	}
	return sessionID, nil
}

// handleSubscribeSession implements the history-replay race-free subscription
// sequence:
//  1. Register a buffered channel with the hub so live output is captured immediately.
//  2. Fetch historical events from Postgres.
//  3. Send history to the browser.
//  4. Non-blocking drain of any live events that arrived during the DB query.
//  5. Start a forwarding goroutine for ongoing live events.
func handleSubscribeSession(ctx context.Context, h *Hub, bc *BrowserConn, dbPool *pgxpool.Pool, sessionID string) {
	// Check if already subscribed.
	bc.subMu.Lock()
	if _, exists := bc.subs[sessionID]; exists {
		bc.subMu.Unlock()
		return
	}
	bc.subMu.Unlock()

	// Step 1: create the buffer channel and subscribe BEFORE querying history.
	bufferChan := make(chan []byte, 256)
	h.Subscribe(sessionID, bc.ID, bufferChan)

	// Step 2 & 3: fetch history and send as a single message so the client
	// does one terminal.write() call — preventing visible per-chunk scrolling.
	if dbPool != nil {
		events, err := db.GetSessionEventsTail(ctx, dbPool, sessionID, historyReplayMaxBytes)
		if err != nil {
			log.Printf("browser %s GetSessionEventsTail %s: %v", bc.ID, sessionID, err)
		} else {
			var combined []byte
			for _, ev := range events {
				if ev.Type != "output" {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(ev.Data)
				if err != nil {
					continue
				}
				combined = append(combined, raw...)
			}
			if len(combined) > 0 {
				msg, err := json.Marshal(protocol.SessionOutput{
					Type:      "session_output",
					SessionID: sessionID,
					Data:      base64.StdEncoding.EncodeToString(combined),
				})
				if err == nil {
					select {
					case bc.send <- msg:
					case <-ctx.Done():
						h.Unsubscribe(sessionID, bc.ID)
						return
					}
				}
			}
		}
	}

	// Claim focus for this browser, notifying any previously active browser.
	if prev := h.SetActiveBrowser(sessionID, bc.ID); prev != "" && prev != bc.ID {
		if stolenMsg, err := json.Marshal(protocol.FocusStolen{
			Type:      "focus_stolen",
			SessionID: sessionID,
		}); err == nil {
			h.SendToBrowser(prev, stolenMsg)
		}
	}

	// Always send history_done so the browser knows when to send its resize.
	// This ensures the SIGWINCH-triggered PTY redraw happens after history is
	// rendered in xterm, not before, avoiding partial-output artifacts.
	// Include the session's current PTY cols so the browser can detect a width
	// mismatch (e.g. desktop history replayed on a mobile terminal) and discard
	// the garbled scrollback before the tmux repaint arrives.
	if doneMsg, err := json.Marshal(protocol.HistoryDone{
		Type:      "history_done",
		SessionID: sessionID,
		Cols:      h.GetSessionPTYCols(sessionID),
	}); err == nil {
		select {
		case bc.send <- doneMsg:
		case <-ctx.Done():
			h.Unsubscribe(sessionID, bc.ID)
			return
		}
	}

	// Step 4: non-blocking drain of buffered live events (arrived during DB query).
drainLoop:
	for {
		select {
		case msg := <-bufferChan:
			select {
			case bc.send <- msg:
			case <-ctx.Done():
				h.Unsubscribe(sessionID, bc.ID)
				return
			}
		default:
			break drainLoop
		}
	}

	// Step 5: start a forwarder goroutine for ongoing live events.
	subCtx, subCancel := context.WithCancel(ctx)
	bc.subMu.Lock()
	bc.subs[sessionID] = subCancel
	bc.subMu.Unlock()

	go func() {
		defer subCancel()
		for {
			select {
			case msg := <-bufferChan:
				select {
				case bc.send <- msg:
				case <-subCtx.Done():
					return
				}
			case <-subCtx.Done():
				return
			}
		}
	}()
}

// handleUnsubscribeSession stops forwarding output for sessionID to bc.
// If bc was the active browser for the session, another subscriber is promoted
// and sent a focus_granted notification.
func handleUnsubscribeSession(h *Hub, bc *BrowserConn, sessionID string) {
	// Release focus BEFORE unsubscribing so ReleaseActiveBrowser can still see
	// other browsers in h.subscriptions to pick a replacement.
	releaseFocus(h, bc, sessionID)

	h.Unsubscribe(sessionID, bc.ID)
	bc.subMu.Lock()
	if cancel, ok := bc.subs[sessionID]; ok {
		cancel()
		delete(bc.subs, sessionID)
	}
	bc.subMu.Unlock()
}
