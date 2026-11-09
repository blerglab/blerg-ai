package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"log"
	"log/slog"
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
// connect and accepts state-changing commands (send_input,
// interrupt_session, ...), so it is exactly as sensitive as the REST surface
// and must not be reachable without a verified core-issued token. In
// production this is (*API).AuthorizeBrowserWS. A nil authorize fails CLOSED
// (every upgrade rejected): a missing gate must never silently mean "open".
//
// authorize also returns the verified account id behind the token, which is
// remembered on the BrowserConn: socket commands that act on someone else's
// session (resuming a cluster session with its launcher's personal
// credentials) must be able to tell who is asking.
func (h *Hub) ServeBrowser(dbPool *pgxpool.Pool, authorize func(*http.Request) (identity.Principal, bool)) http.HandlerFunc {
	// This handler is also used without NewAPI. Initialize broadcast privacy
	// from the same database used by its commands before accepting sockets.
	h.SetPrivacyPool(dbPool)
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := identity.Principal{}, false
		if authorize != nil {
			principal, ok = authorize(r)
		}
		if !ok {
			// Reject BEFORE the upgrade: a 401 on the handshake is visible to
			// the client, whereas closing an already-upgraded socket would
			// have leaked a successful connection first.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		accountID, sessionID := principal.Sub, principal.Sid
		wsConn, err := browserUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("browser ws upgrade error: %v", err)
			return
		}

		// The connection outlives the upgrade request, so its context keeps
		// the request's values but not its cancellation.
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		if principal.Kind == "human" {
			ctx = withLivePersonToken(ctx, wsBearerToken(r))
		}
		bc := &BrowserConn{
			ID:        newUUID(),
			verified:  principal,
			AccountID: accountID,
			SessionID: sessionID,
			send:      make(chan []byte, 256),
			ctx:       ctx,
			cancel:    cancel,
			subs:      make(map[string]context.CancelFunc),
		}
		bc.desktopFilter = func(raw []byte) ([]byte, bool) {
			var allowed bool
			raw, allowed = h.filterDatasetDelivery(ctx, bc, dbPool, raw)
			if !allowed {
				return nil, false
			}
			data, ok := h.desktopPayloadFor(ctx, accountID, raw)
			if !ok && isInitialState(raw) {
				bc.cancel()
			}
			return data, ok
		}
		bc.desktopDeliver = func(raw []byte, send func([]byte) error) error {
			err := h.deliverDesktopPayload(ctx, accountID, raw, send)
			if err != nil && isInitialState(raw) {
				logInitialStateFailure("delivery", err)
				return errDiscoveryUnavailable
			}
			return err
		}
		h.RegisterBrowser(bc)

		connectCtx, connectCancel := context.WithTimeout(ctx, initialStateTimeout)
		scope, scopeErr := h.desktopScopeRows(connectCtx, accountID)
		connectCancel()
		if scopeErr != nil {
			logInitialStateFailure("desktop_scope", scopeErr)
			h.UnregisterBrowser(bc.ID)
			cancel()
			_ = wsConn.Close()
			return
		}

		// Send initial_state before starting the read pump so the browser
		// gets it as its very first message.
		sendInitialState(ctx, bc, h, dbPool)
		if bc.ctx.Err() != nil {
			h.UnregisterBrowser(bc.ID)
			_ = wsConn.Close()
			return
		}
		go h.watchDesktopScope(ctx, bc, dbPool, scope)

		conn := wsConn
		go browserWritePump(bc, conn)
		go browserReadPump(h, bc, conn, dbPool) //nolint:contextcheck // the read pump lives as long as the socket, not the upgrade request
	}
}

const initialStateTimeout = 5 * time.Second

// sendInitialState assembles and sends the initial_state message to bc.
func sendInitialState(ctx context.Context, bc *BrowserConn, h *Hub, dbPool *pgxpool.Pool) {
	started := time.Now()
	defer func() {
		slog.Debug("browser initial_state collected", "duration_ms", time.Since(started).Milliseconds())
	}()
	ctx, cancel := context.WithTimeout(ctx, initialStateTimeout)
	defer cancel()
	fail := func(stage string, err error) { logInitialStateFailure(stage, err); bc.cancel() }
	daemons, err := h.initialStateDaemons(ctx, bc.AccountID)
	if err != nil {
		fail("desktops", err)
		return
	}
	daemonInfos := make([]protocol.DaemonInfo, 0, len(daemons))
	for _, d := range daemons {
		if !isHostDaemon(d) {
			continue // a cluster session pod, not a machine the owner picks
		}
		daemonInfos = append(daemonInfos, protocol.DaemonInfo{Compatibility: d.Compatibility,
			RepoInventoryLimited: d.InventoryLimited(),
			DiscoveryVersion:     d.DiscoveryVersion, WorkspaceCapabilities: d.WorkspaceCapabilities,
			OwnershipRevision: d.ownershipRevision(), ConnectionGeneration: d.DesktopGeneration,
			ID:                  d.ID,
			Name:                d.Name,
			Mode:                d.Mode,
			ReposRoot:           d.CurrentReposRoot(),
			Status:              "connected",
			Version:             d.Version,
			MCPGateway:          d.CanMCPGateway(),
			MCPGatewayViaServer: d.CanMCPGatewayViaServer(),
			RestrictTools:       d.CanRestrictTools(),
			CodexRestricted:     d.CanCodexRestricted(),
			PluginEngines:       d.PluginEngines(),
			// nil (omitted) when no provider has reported.
			UsageLimits: nilIfEmpty(d.UsageLimits()),
		})
	}

	sessionInfos := []protocol.SessionInfo{}
	var sessionRows []db.SessionRow
	if dbPool != nil {
		rows, err := db.ListBrowserSessions(ctx, dbPool, bc.AccountID, nil)
		if err != nil {
			fail("sessions", err)
			return
		}
		sessionRows = rows
		projectionCtx := ctx
		if h.desktopPool != nil {
			p, e := db.LoadBrowserProjection(ctx, h.desktopPool, bc.AccountID, rows, true)
			if e != nil {
				fail("session_metadata", e)
				return
			}
			projectionCtx = context.WithValue(ctx, browserProjectionKey{}, &browserProjection{bc.AccountID, h.desktopPool, p})
		}
		for _, r := range rows {
			sessionInfos = append(sessionInfos, h.sessionInfoFor(projectionCtx, r, bc.AccountID))
		}
	}

	h.mu.RLock()
	sv, release, commit := h.serverVersion, h.serverRelease, h.serverCommit
	latest := h.latestRelease
	h.mu.RUnlock()
	var latestVersion, latestURL string
	if latest != nil {
		latestVersion, latestURL = latest()
	}

	// Messages (open + recent), so each session's thread is populated on load.
	messageInfos := []protocol.MessageInfo{}
	if dbPool != nil {
		rows, err := db.ListMessagesFor(ctx, dbPool, defaultMessageLimit, bc.AccountID)
		if err != nil {
			fail("messages", err)
			return
		}
		messageInfos, err = initialStateMessageInfos(ctx, dbPool, rows, bc.AccountID, sessionRows)
		if err != nil {
			fail("message_owners", err)
			return
		}
	}

	msg := protocol.InitialState{
		Type:          "initial_state",
		Daemons:       daemonInfos,
		Sessions:      sessionInfos,
		Messages:      messageInfos,
		ServerVersion: sv,
		ServerRelease: release,
		ServerCommit:  commit,
		LatestVersion: latestVersion,
		LatestURL:     latestURL,
	}
	if err := ctx.Err(); err != nil {
		fail("collection", err)
		return
	}
	limits := currentFileLimits()
	msg.Limits = &limits
	data, err := json.Marshal(msg)
	if err != nil {
		fail("marshal", err)
		return
	}
	select {
	case bc.send <- data:
	case <-ctx.Done():
		fail("enqueue", ctx.Err())
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
			if ok && bc.desktopFilter != nil {
				var allowed bool
				msg, allowed = bc.desktopFilter(msg)
				if !allowed {
					continue
				}
			}
			_ = conn.SetWriteDeadline(time.Now().Add(browserWriteWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			write := func(data []byte) error { return conn.WriteMessage(websocket.TextMessage, data) }
			var err error
			if bc.desktopDeliver != nil {
				err = bc.desktopDeliver(msg, write)
			} else {
				err = write(msg)
			}
			if err != nil {
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
		for _, sub := range bc.datasetSubs {
			sub.cancel()
		}
		bc.datasetSubs = nil
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
		if sid := inboundSessionID(raw); sid != "" && !browserSessionAccess(ctx, h, dbPool, bc.AccountID, sid) {
			log.Printf("browser %s: %s for a session it may not see: dropped", bc.ID, env.Type)
			continue
		}

		switch env.Type {
		case "subscribe_dataset", "unsubscribe_dataset", "dataset_heartbeat", "dataset_diagnostic":
			handleDatasetCommand(h, bc, dbPool, raw)

		case "activity":
			// Throttled user-interaction signal; gates push vs in-app toast.
			h.MarkActivity(bc.AccountID)

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
			if err := handleBrowserTerminalInput(ctx, h, bc, dbPool, msg); err != nil {
				log.Printf("browser %s: terminal input refused", bc.ID)
			}

		case "resize_session", "request_scrollback":
			// Older clients may still send Terminal runtime commands. History is read-only.
			continue

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
			handleBrowserAgentMessage(ctx, h, bc, dbPool, msg)

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
			if dbPool == nil {
				forwardToSessionDaemon(h, bc, msg.SessionID, fwd)
			} else if _, problem := h.changeSessionModel(ctx, RunnerPrincipal{Kind: "human", Sub: bc.AccountID, Sid: bc.SessionID}, msg.SessionID, fwd.Model, fwd.Effort); problem != nil {
				recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": problem.Message, "retryable": problem.Status == http.StatusServiceUnavailable})
			}

		case "release_held_prompt":
			var msg protocol.ReleaseHeldPrompt
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if problem := handleReleaseHeldPrompt(ctx, h, bc, dbPool, msg); problem != nil {
				log.Printf("browser %s: release held prompt refused (%d)", bc.ID, problem.Status)
			}

		case "interrupt_session":
			var msg protocol.InterruptSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("browser %s malformed interrupt_session: %v", bc.ID, err)
				continue
			}
			stopCtx, cancelStop := context.WithTimeout(ctx, 35*time.Second)
			if err := withCancelledLimitResume(stopCtx, dbPool, msg.SessionID, func() error {
				if reason := pausedStopReason(stopCtx, dbPool, msg.SessionID); reason != "" {
					// The existing paused notice already explains the refusal.
					// Repeated stale Stop requests must not append chat lines.
					return nil
				}
				if !forwardToSessionDaemon(h, bc, msg.SessionID, protocol.InterruptSession{Type: "interrupt_session", SessionID: msg.SessionID, TargetEventID: msg.TargetEventID}) {
					recordServerAgentEvent(stopCtx, h, dbPool, msg.SessionID, newUUID(), "stop_result", map[string]string{"state": "failed", "message": "Stop could not reach the worker daemon. All work is retained; retry when it reconnects."})
				}
				return nil
			}); err != nil {
				recordServerAgentEvent(stopCtx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": "Continuation is busy; retry interrupt shortly.", "retryable": true})
			}

			cancelStop()

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

// handleBrowserAgentMessage routes a message the person sent to an agent session from the browser.
//
// A message typed while `blerg-runner ask` waits is its answer: the engine is blocked inside the
// ask and would read the message only once the ask timed out (messageevents.go). Only while the
// session's daemon is connected: with it away (a restart) the `ask` is dead, so the message is
// held for the daemon, which hands it to the session hosted again — where it replaces recovery's
// continue. A message accepted for the session (forwarded or held) ends its pause by a restart.
//
// A browser writes as the person and nothing else: the frame's source is ignored and set to
// "chat" before the outbox, the hold or a cluster resume sees it, so a socket cannot claim a
// source Blerg writes itself (reservedMessageSource: a relayed review or proposal decision, a
// system note). An answer to an ask goes by answer_message_id and the server records it as
// ask_answer itself (messageevents.go).
func handleBrowserAgentMessage(ctx context.Context, h *Hub, bc *BrowserConn, dbPool *pgxpool.Pool, msg protocol.AgentUserMessage) {
	msg.Source = "chat"
	// Nor the id of one of Blerg's relays (reservedClientID): it would take the relay's place.
	if reservedClientID(msg.ClientID) {
		recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{
			"message": "This message was not sent: its id is reserved for messages Blerg relays itself. Send it again.", "retryable": false, "client_id": msg.ClientID})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := withCancelledPendingLimitResumeConn(ctx, dbPool, msg.SessionID, func(conn *pgxpool.Conn) error {
		return withParentInputGuard(ctx, dbPool, msg.SessionID, func() error {
			handleBrowserAgentMessageUnlocked(ctx, h, bc, dbPool, msg)
			return nil
		})
	}); err != nil {
		message, retryable := "Continuation is busy; retry your message shortly.", true
		if errors.Is(err, db.ErrDelegationEnded) {
			message, retryable = "The session's parent ended; this child cannot resume.", false
		}
		recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": message, "retryable": retryable, "client_id": msg.ClientID})
	}
}

func handleBrowserAgentMessageUnlocked(ctx context.Context, h *Hub, bc *BrowserConn, dbPool *pgxpool.Pool, msg protocol.AgentUserMessage) {
	if problem := parentResumeProblem(ctx, dbPool, msg.SessionID); problem != nil {
		recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": problem.Message, "retryable": problem.Status == http.StatusServiceUnavailable, "client_id": msg.ClientID})
		return
	}
	if h.boundAnswer != nil {
		handled, err := h.boundAnswer(ctx, msg, bc.verified)
		if handled && !errors.Is(err, errBoundConversation) {
			if err != nil {
				recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": boundReplyMessage(err), "client_id": msg.ClientID, "retryable": true})
			}
			return
		}
		if errors.Is(err, errBoundConversation) {
			msg.AnswerMessageID = ""
			if msg.ClientID == "" {
				msg.ClientID = newUUID()
			}
		}
	}
	fresh, err := acceptSessionInput(ctx, h, dbPool, msg)
	if err != nil {
		recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": sessionInputError(err), "retryable": true, "client_id": msg.ClientID})
		return
	}
	if !fresh {
		return
	}
	daemonHere := h.FindDaemonForSession(msg.SessionID) != nil
	// An answer to a question the agent did not wait on needs no daemon: it is recorded now and sent
	// on in Blerg's voice through the durable outbox, tied to its question, whenever the host is back.
	answersNoWait := msg.AnswerMessageID != "" && noWaitQuestion(ctx, dbPool, msg.SessionID, msg.AnswerMessageID)
	if (daemonHere || answersNoWait) && (msg.Source == "" || msg.Source == "chat" || msg.AnswerMessageID != "") &&
		answerAskFromChatAs(ctx, h, dbPool, msg.SessionID, msg.Text, msg.ClientID, msg.AnswerMessageID, bc.AccountID) {
		return
	}
	if !daemonHere {
		// A desktop daemon that is away for a moment (a restart): the message waits
		// for it instead of being dropped (daemongrace.go).
		if held, err := json.Marshal(protocol.AgentUserMessage{
			Type: "agent_user_message", SessionID: msg.SessionID, Text: msg.Text, Source: msg.Source,
			ClientID: msg.ClientID,
		}); err == nil && (dbPool == nil || msg.ClientID == "" || harnessCommand(msg.Text)) && h.HoldForAwayDaemon(msg.SessionID, held) {
			clearPausedOnMessage(ctx, h, dbPool, msg.SessionID)
			return
		}
		// Disconnected cluster session: re-create the runner Job with
		// this message as the resume prompt. bc.AccountID is passed so
		// the resume only re-mints personal credentials when the
		// requester is the account the session was launched under.
		if dbPool != nil && msg.ClientID != "" && !harnessCommand(msg.Text) {
			row, lookupErr := db.GetSession(ctx, dbPool, msg.SessionID)
			if lookupErr == nil && row != nil && (row.Runtime == nil || *row.Runtime != "cluster") {
				// The durable outbox replaces the small in-memory grace buffer, including
				// messages sent after grace expires. The daemon picks them up on return.
				return
			}
			if err := resumeClusterSessionMode(ctx, h, dbPool, msg.SessionID, msg.Text, bc.AccountID, bc.SessionID, false, resumeInput{ClientID: msg.ClientID, Source: msg.Source, Proof: coreProof{SessionID: bc.SessionID}}); err != nil {
				recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": "Session could not resume; retry shortly.", "retryable": true, "client_id": msg.ClientID})
				_ = db.SettleSessionInput(ctx, dbPool, msg.SessionID, msg.ClientID)
			}
			// The initial prompt carries the same id as reconnect redelivery. Only
			// its actual consumption settles the outbox; the daemon deduplicates both.
			return
		}
		if err := resumeClusterSessionMode(ctx, h, dbPool, msg.SessionID, msg.Text, bc.AccountID, bc.SessionID, false, resumeInput{Source: msg.Source, Proof: coreProof{SessionID: bc.SessionID}}); err != nil {
			recordServerAgentEvent(ctx, h, dbPool, msg.SessionID, newUUID(), "error", map[string]any{"message": "Session could not resume; retry shortly.", "retryable": true})
		}
		return
	}
	if forwardToSessionDaemon(h, bc, msg.SessionID, protocol.AgentUserMessage{
		Type: "agent_user_message", SessionID: msg.SessionID, Text: msg.Text, Source: msg.Source,
		ClientID: msg.ClientID,
	}) {
		clearPausedOnMessage(ctx, h, dbPool, msg.SessionID) // only once it is on its way
	}
}

// Legacy Terminal input is refused without changing an agent's continuation state.
func handleBrowserTerminalInput(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.SendInput) error {
	return withParentInputGuard(ctx, pool, msg.SessionID, func() error {
		if !h.accountCanSee(bc.AccountID, msg.SessionID) {
			return db.ErrDelegationNotFound
		}
		return errors.New("terminal history is read-only")
	})
}

// Match browser read routes using persisted privacy, rather than trusting only
// the broadcast cache. Missing rows retain the handlers' existing not-found
// behavior, just as browserCanSee does; failed reads still fail closed.
func browserSessionAccess(ctx context.Context, h *Hub, pool *pgxpool.Pool, account, id string) bool {
	if pool == nil {
		return h.accountCanSee(account, id)
	}
	if !db.ValidUUID(id) {
		return false
	}
	row, err := db.GetSession(ctx, pool, id)
	return err == nil && (row == nil || canSeeAccount(account, row))
}
