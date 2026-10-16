package server

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionTokenAckTimeout is how long a daemon has to answer a SessionToken
// (SessionTokenResult). No answer counts as a failure: the token is revoked so
// the next heal can try again. A var so tests can shorten it.
var sessionTokenAckTimeout = 30 * time.Second

// tokenHeals is the hub's record of re-issued session tokens awaiting their
// daemon's answer, keyed by session. It also makes a heal single-flight per
// session: a reconnect's hello and the old connection's last heartbeat cannot
// both mint. The zero value is ready to use.
type tokenHeals struct {
	mu      sync.Mutex
	pending map[string]string // sessionID → raw token sent ("" while it is being minted)
}

// claim reserves sessionID for one heal; false when one is already in flight.
func (t *tokenHeals) claim(sessionID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, busy := t.pending[sessionID]; busy {
		return false
	}
	if t.pending == nil {
		t.pending = make(map[string]string)
	}
	t.pending[sessionID] = ""
	return true
}

// set records the raw token sent for a claimed session.
func (t *tokenHeals) set(sessionID, raw string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = make(map[string]string)
	}
	t.pending[sessionID] = raw
}

// release drops a claim whose heal did not go out.
func (t *tokenHeals) release(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, sessionID)
}

// take settles sessionID's heal and returns the token it sent.
func (t *tokenHeals) take(sessionID string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	raw, ok := t.pending[sessionID]
	delete(t.pending, sessionID)
	return raw, ok
}

// takeIf settles sessionID's heal only if it is still the one that sent raw.
func (t *tokenHeals) takeIf(sessionID, raw string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur, ok := t.pending[sessionID]; !ok || cur != raw {
		return false
	}
	delete(t.pending, sessionID)
	return true
}

// healSessionTokens gives every session in ids that dc runs, or is starting,
// and that holds no valid messaging token a fresh one, delivered to the
// daemon as session_token (the daemon writes it where the `blerg-runner` CLI
// reads it first, and answers with session_token_result). A session can lose
// its token while it keeps running: a reconcile or an aborted spawn revokes
// it, then the daemon reports the session alive and the row is revived — with
// the dead credential still in the process's environment. Only the messaging
// token comes back: MCP grants a stop deleted stay deleted.
//
// Called on session_started and on every hello and heartbeat, with the ids
// the daemon reports. Ended sessions are never healed, and a session with a
// live token gets nothing (db.SessionsWithoutMessageToken, which also checks
// the row is dc's), so a heartbeat mints at most once per lost token. A token
// the daemon fails to store, or does not acknowledge within
// sessionTokenAckTimeout, is revoked so the next heartbeat retries. A daemon
// that did not say it can store one (an older daemon would drop it) is sent
// nothing; a runner pod is skipped (its token travels in its Job's Secret).
// Never logs a token.
func healSessionTokens(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, dc *DaemonConn, ids []string) {
	if dbPool == nil || h == nil || dc == nil || len(ids) == 0 || dc.Mode == "runner" {
		return
	}
	need, err := db.SessionsWithoutMessageToken(ctx, dbPool, dc.ID, ids)
	if err != nil {
		log.Printf("heal session tokens on daemon %s: %v", dc.ID, err)
		return
	}
	if len(need) == 0 {
		return
	}
	if !dc.CanSessionTokenFile() {
		if dc.tokenHealSkipLogged.CompareAndSwap(false, true) {
			log.Printf("daemon %s: %d session(s) without a valid messaging token, but the daemon is too old to take a fresh one — reinstall it", dc.ID, len(need))
		}
		return
	}
	for _, id := range need {
		if !h.tokenHeals.claim(id) {
			continue // a heal of this session is already waiting for its answer
		}
		tok, err := db.MintSessionToken(ctx, dbPool, id, []string{"message"}, sessionTokenTTL)
		if err != nil {
			h.tokenHeals.release(id)
			log.Printf("heal session token %s: mint: %v", id, err)
			continue
		}
		raw, err := json.Marshal(protocol.SessionToken{Type: "session_token", SessionID: id, Token: tok}) // the token travels only over the daemon's authenticated websocket; never logged
		if err == nil {
			h.tokenHeals.set(id, tok)
			select {
			case dc.send <- raw:
				log.Printf("session %s: no valid messaging token — a fresh one sent to daemon %s", id, dc.ID)
				time.AfterFunc(sessionTokenAckTimeout, func() {
					if h.tokenHeals.takeIf(id, tok) {
						log.Printf("session %s: daemon %s did not confirm the fresh token — revoked, retried at a later heartbeat", id, dc.ID)
						revokeHealedToken(context.WithoutCancel(ctx), dbPool, id, tok)
					}
				})
				continue
			default:
			}
		}
		// Not delivered: a live token nobody holds would also stop the next
		// heartbeat from trying again.
		h.tokenHeals.release(id)
		revokeHealedToken(ctx, dbPool, id, tok)
		log.Printf("session %s: fresh messaging token not delivered (daemon %s busy) — retried at the next heartbeat", id, dc.ID)
	}
}

// handleSessionTokenResult settles a heal with the daemon's answer: a token it
// could not store is revoked, so the next heal can try again.
func handleSessionTokenResult(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, msg protocol.SessionTokenResult) {
	tok, ok := h.tokenHeals.take(msg.SessionID)
	if !ok || tok == "" {
		return // not ours, or already settled by the timeout
	}
	if msg.OK {
		return
	}
	log.Printf("session %s: the daemon could not store its fresh token (%s) — revoked, retried at the next heartbeat", msg.SessionID, msg.Error)
	revokeHealedToken(ctx, dbPool, msg.SessionID, tok)
}

func revokeHealedToken(ctx context.Context, dbPool *pgxpool.Pool, sessionID, tok string) {
	if dbPool == nil {
		return
	}
	if err := db.RevokeBoardTokenRaw(ctx, dbPool, tok); err != nil {
		log.Printf("heal session token %s: revoke: %v", sessionID, err)
	}
}
