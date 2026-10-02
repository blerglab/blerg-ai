package server

// Private sessions.
//
// Every session on an install is normally visible to every human on it (see
// runnerPrincipal.seesAllSessions). A PRIVATE session (sessions.private,
// migration 022) is different: only the account that started it
// (spawning_account_id) may see it, and no principal bypasses that, an
// operator or a board admin's agent token included. The runner key has no
// account at all, so it cannot see one either.
//
// The rule lives in exactly two predicates, canSee (a runner-contract
// principal) and canSeeAccount (a browser, which is always a human account).
// Every surface that names a session or emits its data calls one of them, or
// goes through a helper built on them:
//
//   - requireSessionAccess (runner contract, MCP): canSee
//   - GET/PATCH/DELETE on /api/sessions, capabilities, message reply: canSeeAccount
//   - the session and message lists: canSeeAccount / db.ListMessagesFor
//   - /ws/browser, outbound: Hub.BroadcastToBrowsers withholds any payload that
//     names a private session from every socket but its owner's (a payload's
//     session is read from its own JSON, so a new broadcast site cannot forget)
//   - /ws/browser, inbound: every frame that names a session is refused for a
//     socket that may not see it (Hub.accountCanSee)
//   - completion webhooks and push notifications: see webhook.go / push sites
//
// privacy_guard_test.go fails when a route or a broadcast call site appears
// that has no recorded visibility decision.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ownsSession reports whether account is the account the session was started
// by. The empty account owns nothing, and a session with no spawning account
// (one the static runner key started) has no owner.
func ownsSession(account string, row *db.SessionRow) bool {
	return account != "" && row != nil && row.SpawningAccountID != nil && *row.SpawningAccountID == account
}

// canSee is the ONE visibility decision for a runner-contract principal.
//
// A private row is visible to its owner alone: there is deliberately no
// seesAllSessions() shortcut here. For every other row the long-standing
// rules apply unchanged (an agent token is scoped to its owner's sessions
// unless it is a board admin's; the operator key, human and service tokens
// see everything).
func canSee(p runnerPrincipal, row *db.SessionRow) bool {
	if row == nil {
		return false
	}
	if row.Private {
		return ownsSession(p.spawningAccountID(), row)
	}
	if p.seesAllSessions() {
		return true
	}
	return ownsSession(p.spawningAccountID(), row)
}

// canSeeAccount is canSee for a browser caller, which is a human account: a
// non-private session is visible to every human (as ever), a private one only
// to its owner.
func canSeeAccount(account string, row *db.SessionRow) bool {
	if row == nil {
		return false
	}
	if !row.Private {
		return true
	}
	return ownsSession(account, row)
}

// browserCanSee reports whether a browser account may see the session with
// this id. A session that does not exist is reported visible so the caller's
// own not-found answer is unchanged; a failed read is not (fail closed).
func (a *API) browserCanSee(ctx context.Context, account, sessionID string) bool {
	if a.dbPool == nil {
		return true
	}
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil {
		return false
	}
	return row == nil || canSeeAccount(account, row)
}

// coreActorCanSeeMessage is the visibility rule for a core-issued caller acting
// on a message: it may not touch a message of a private session that is not
// its own. An agent token counts as the account it acts for.
func (a *API) coreActorCanSeeMessage(ctx context.Context, p *identity.Principal, messageID string) bool {
	if a.dbPool == nil {
		return true
	}
	msg, err := db.GetMessage(ctx, a.dbPool, messageID)
	if err != nil {
		return false
	}
	if msg == nil {
		return true
	}
	row, err := db.GetSession(ctx, a.dbPool, msg.SessionID)
	if err != nil {
		return false
	}
	account := runnerPrincipal{Kind: p.Kind, Sub: p.Sub, OnBehalfOf: p.OnBehalfOf}.spawningAccountID()
	return row == nil || canSeeAccount(account, row)
}

// privacyGate is the hub's own record of which sessions are private and whose,
// so the code that has only a Hub (the daemon message path, the reconcilers,
// request handlers) can decide a broadcast without a database read on the hot
// path. It is loaded when the hub is given its database (SetPrivacyPool) and
// updated by MarkPrivate, and by notePrivate when a session is inserted already
// private (db.SessionOrigin). The runner server is a single process, so the
// record cannot go stale; if the load fails the gate fails closed and reloads
// itself with a backoff (startPrivacyReloader) until it recovers.
//
// Entries are deliberately never pruned when a session ends: an ended session
// can be revived (a resume re-inserts its row, and the private flag survives
// that), and a forgotten entry would make the revived session public in the hub.
// The map holds one short string pair per private session.
type privacyGate struct {
	mu     sync.RWMutex
	owners map[string]string // private session id -> owning account ("" = none)
	failed bool              // the load failed: cannot tell, withhold

	load       func(context.Context) (map[string]string, error) // reads the database
	kick       chan struct{}                                    // wakes the reloader (startPrivacyReloader)
	retryAt    time.Time                                        // no reload before this
	backoff    time.Duration                                    // current retry delay
	retryFloor time.Duration                                    // 0 = privacyRetryFloor
	retryCeil  time.Duration                                    // 0 = privacyRetryCeil
}

const (
	// privacyLoadTimeout bounds one read of the private sessions.
	privacyLoadTimeout = 10 * time.Second
	// privacyRetryFloor / privacyRetryCeil bound the reload backoff after a failed load.
	privacyRetryFloor = time.Second
	privacyRetryCeil  = 30 * time.Second
)

// SetPrivacyPool loads the private sessions from the database into the hub.
// It runs once, from NewAPI, at start-up. If the read fails the hub cannot tell
// which sessions are private and fails closed (nothing that names a session is
// delivered to a browser, no inbound session frame is accepted), and it keeps
// retrying with a backoff, on the next use after each delay, until a load
// succeeds. A restart is not needed to recover.
func (h *Hub) SetPrivacyPool(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	load := func(ctx context.Context) (map[string]string, error) {
		return db.ListPrivateSessionOwners(ctx, pool)
	}
	h.privacy.mu.Lock()
	h.privacy.load = load
	h.privacy.mu.Unlock()
	h.startPrivacyReloader()
	ctx, cancel := context.WithTimeout(context.Background(), privacyLoadTimeout)
	defer cancel()
	owners, err := load(ctx)
	h.applyPrivacyLoad(owners, err)
}

// applyPrivacyLoad records the result of a load: on success the owners read are
// merged into what MarkPrivate/notePrivate already noted and the gate opens; on
// failure it stays closed and the next reload is scheduled.
func (h *Hub) applyPrivacyLoad(owners map[string]string, err error) {
	h.privacy.mu.Lock()
	defer h.privacy.mu.Unlock()
	if err != nil {
		lo, hi := h.privacy.retryFloor, h.privacy.retryCeil
		if lo <= 0 {
			lo = privacyRetryFloor
		}
		if hi <= 0 {
			hi = privacyRetryCeil
		}
		switch {
		case h.privacy.backoff <= 0:
			h.privacy.backoff = lo
		case h.privacy.backoff*2 > hi:
			h.privacy.backoff = hi
		default:
			h.privacy.backoff *= 2
		}
		h.privacy.retryAt = time.Now().Add(h.privacy.backoff)
		log.Printf("session privacy: cannot load private sessions, withholding session events (retry in %s): %v", h.privacy.backoff, err)
		h.privacy.failed = true
		return
	}
	if h.privacy.owners == nil {
		h.privacy.owners = map[string]string{}
	}
	for id, owner := range owners {
		if _, known := h.privacy.owners[id]; !known {
			h.privacy.owners[id] = owner
		}
	}
	h.privacy.failed = false
	h.privacy.backoff = 0
}

// startPrivacyReloader starts the one goroutine that reloads the private
// sessions after a failed load (it lives as long as the process, like the hub).
// It is idempotent. A caller that finds the gate closed only wakes it
// (privateOwners), so nothing on the broadcast path ever waits for the database.
func (h *Hub) startPrivacyReloader() {
	h.privacy.mu.Lock()
	if h.privacy.kick != nil {
		h.privacy.mu.Unlock()
		return
	}
	ch := make(chan struct{}, 1)
	h.privacy.kick = ch
	h.privacy.mu.Unlock()
	go func() {
		for range ch {
			h.reloadPrivacy()
		}
	}()
}

// reloadPrivacy reloads the private sessions when the gate is closed and the
// backoff has elapsed; otherwise it does nothing (the next use wakes it again).
func (h *Hub) reloadPrivacy() {
	h.privacy.mu.Lock()
	load := h.privacy.load
	if !h.privacy.failed || load == nil || time.Now().Before(h.privacy.retryAt) {
		h.privacy.mu.Unlock()
		return
	}
	h.privacy.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), privacyLoadTimeout)
	defer cancel()
	owners, err := load(ctx)
	h.applyPrivacyLoad(owners, err)
}

// notePrivate records a session as private (MarkPrivate, or before a session
// is inserted already private).
func (h *Hub) notePrivate(sessionID, owner string) {
	h.privacy.mu.Lock()
	defer h.privacy.mu.Unlock()
	if h.privacy.owners == nil {
		h.privacy.owners = map[string]string{}
	}
	h.privacy.owners[sessionID] = owner
}

// privateOwners returns the private sessions among ids and their owners ("" =
// unowned). ok is false when the hub cannot tell (its load failed and has not
// yet recovered, see startPrivacyReloader):
// the caller must then treat every id as private and unowned (fail closed).
func (h *Hub) privateOwners(ids []string) (owners map[string]string, ok bool) {
	h.privacy.mu.RLock()
	defer h.privacy.mu.RUnlock()
	if h.privacy.failed {
		if ch := h.privacy.kick; ch != nil { // recovers once the database answers
			select {
			case ch <- struct{}{}:
			default: // a reload is already pending
			}
		}
		return nil, false
	}
	for _, id := range ids {
		if owner, private := h.privacy.owners[id]; private {
			if owners == nil {
				owners = map[string]string{}
			}
			owners[id] = owner
		}
	}
	return owners, true
}

// accountCanSee reports whether the socket account may act on or read the
// session. A session that does not exist is not private, so this is true for
// it, and the caller's own not-found handling stays what it was.
func (h *Hub) accountCanSee(account, sessionID string) bool {
	owners, ok := h.privateOwners([]string{sessionID})
	if !ok {
		return false
	}
	owner, private := owners[sessionID]
	if !private {
		return true
	}
	return owner != "" && owner == account
}

// errPrivacyNoDB is what MarkPrivate reports on an API with no database.
var errPrivacyNoDB = errors.New("session privacy needs a database")

// evictForeignSubscribers drops every live output subscription to sessionID
// held by a socket that is not the owner's, so marking a session private
// takes effect for sockets that were already watching it.
func (h *Hub) evictForeignSubscribers(sessionID, owner string) {
	h.mu.Lock()
	var evicted []*BrowserConn
	for browserID := range h.subscriptions[sessionID] {
		b := h.browsers[browserID]
		if b != nil && owner != "" && b.AccountID == owner {
			continue
		}
		delete(h.subscriptions[sessionID], browserID)
		if h.sessionActiveBrowser[sessionID] == browserID {
			delete(h.sessionActiveBrowser, sessionID)
		}
		if b != nil {
			evicted = append(evicted, b)
		}
	}
	h.mu.Unlock()
	// subMu is taken after h.mu is released: the read pump's cleanup takes
	// them in the other order.
	for _, b := range evicted {
		b.subMu.Lock()
		cancel, ok := b.subs[sessionID]
		delete(b.subs, sessionID)
		b.subMu.Unlock()
		if ok {
			cancel()
		}
	}
}

// MarkPrivate makes a session private: from now on only the account that
// started it can see it, on every surface. A start that carries a grant or a
// cron writes private in the SAME insert as the row (db.SessionOrigin, so no
// window exists in which another account could list it) and calls this right
// after, to evict live subscriptions and as the idempotent backstop.
//
// A session that does not exist is an error. Marking is idempotent. Call it
// after the session's spawning account is recorded (the owner is read here),
// and never write the flag through db.SetSessionPrivate directly: the hub's
// record of private sessions is updated by this function and by
// (*API).notePrivateInsert.
func (a *API) MarkPrivate(ctx context.Context, sessionID string) error {
	if a == nil || a.dbPool == nil {
		return errPrivacyNoDB
	}
	if a.beforeMarkPrivate != nil {
		a.beforeMarkPrivate(sessionID)
	}
	if err := db.SetSessionPrivate(ctx, a.dbPool, sessionID, true); err != nil {
		return err
	}
	if a.hub != nil {
		owner := ""
		if row, err := db.GetSession(ctx, a.dbPool, sessionID); err == nil && row != nil && row.SpawningAccountID != nil {
			owner = *row.SpawningAccountID
		}
		a.hub.notePrivate(sessionID, owner)
		a.hub.evictForeignSubscribers(sessionID, owner)
	}
	return nil
}

// sessionOriginFor is what a start writes with the session's row: the spawning
// account (always, so a private session is private to someone from its first
// moment), and private / the cron when the start already carries a grant or a
// cron. Callers pass it to db.InsertSessionAs / db.InsertClusterSessionAs and
// keep MarkPrivate for the eviction of live subscriptions and as the
// idempotent backstop.
func sessionOriginFor(account string, private bool, cronID string) db.SessionOrigin {
	return db.SessionOrigin{SpawningAccount: account, Private: private, CronID: cronID}
}

// notePrivateInsert tells the hub a session is about to be inserted private, so
// a broadcast that names it (the first one can come before MarkPrivate runs) is
// withheld from every account but its owner from the first instant. Call it
// BEFORE the insert; a session whose insert then fails is merely recorded as
// private, which only withholds.
func (a *API) notePrivateInsert(sessionID string, o db.SessionOrigin) {
	if a != nil && a.hub != nil && o.Private {
		a.hub.notePrivate(sessionID, o.SpawningAccount)
	}
}

// ─── broadcast scoping ───────────────────────────────────────────────────────

// broadcastScope is who may receive one broadcast payload.
type broadcastScope struct {
	primary  []string          // sessions the payload is about
	affected []string          // daemon_disconnected: sessions it lists
	owners   map[string]string // private ones among them → owner
	failed   bool              // the privacy lookup failed: withhold everything session-scoped
	raw      []byte
}

// sessionRefsOf reads which sessions a browser-bound JSON payload names. It is
// deliberately lenient and fails CLOSED: a payload the reader cannot make sense
// of still yields every id it can find, so it is never treated as naming no
// session merely because a field has an unexpected type (a session_state_changed
// carries a string `message`; nothing may assume the shape of a field it does
// not need). The places a session is named:
//
//   - a session_id key anywhere in the payload (top level, a nested message,
//     a list of messages)
//   - a nested `session` object ({"id": ...}) and a `sessions` list of them
//   - the affected_session_ids list a daemon_disconnected carries
//   - at any depth: the id of an object under a key named `session`, and the
//     string (or list of strings) under any key that is session_id, ends in
//     _session_id, or ends in sessionId (parentSessionId, origin_session_id)
//
// A field that does not decode as expected contributes every string in it
// instead. Text that is not a JSON object at all contributes every string it
// contains. The result names no session only when nothing was found.
func sessionRefsOf(data []byte) (primary, affected []string) {
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			primary = append(primary, id)
		}
	}
	addAll := func(raw []byte) {
		for _, str := range quotedStrings(raw) {
			add(str)
		}
	}
	for _, id := range keyedStrings(data, `"session_id"`) {
		add(id)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		addAll(data)
		return primary, nil
	}
	if raw, ok := top["session"]; ok {
		var one struct {
			ID string `json:"id"`
		}
		var str string
		switch {
		case json.Unmarshal(raw, &one) == nil:
			add(one.ID)
		case json.Unmarshal(raw, &str) == nil:
			add(str)
		default:
			addAll(raw)
		}
	}
	if raw, ok := top["sessions"]; ok {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			addAll(raw)
		}
		for _, el := range list {
			var one struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(el, &one) == nil {
				add(one.ID)
			} else {
				addAll(el)
			}
		}
	}
	if raw, ok := top["affected_session_ids"]; ok {
		if json.Unmarshal(raw, &affected) != nil {
			affected = nil
			addAll(raw)
		}
	}
	var whole any
	if json.Unmarshal(data, &whole) == nil {
		walkSessionRefs(whole, add)
	}
	return primary, affected
}

// walkSessionRefs adds the session ids a decoded payload names under keys the fixed
// places above do not cover, at any depth: {"data":{"session":{"id":...}}},
// {"detail":{"sessionId":...}}, {"x":{"parent_session_id":[...]}}. A shape added to the
// protocol later then fails closed without this list being updated.
func walkSessionRefs(v any, add func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lk := strings.ToLower(k)
			switch {
			case k == "session":
				if m, ok := val.(map[string]any); ok {
					if id, ok := m["id"].(string); ok {
						add(id)
					}
				}
			case lk == "session_id", strings.HasSuffix(lk, "_session_id"), strings.HasSuffix(lk, "sessionid"):
				switch ids := val.(type) {
				case string:
					add(ids)
				case []any:
					for _, el := range ids {
						if id, ok := el.(string); ok {
							add(id)
						}
					}
				}
			}
			walkSessionRefs(val, add)
		}
	case []any:
		for _, el := range x {
			walkSessionRefs(el, add)
		}
	}
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// keyedStrings returns the string value that follows every occurrence of key
// (a quoted JSON key) in data, wherever it is nested.
func keyedStrings(data []byte, key string) []string {
	var out []string
	rest := data
	for {
		i := bytes.Index(rest, []byte(key))
		if i < 0 {
			return out
		}
		rest = rest[i+len(key):]
		j := 0
		for j < len(rest) && isJSONSpace(rest[j]) {
			j++
		}
		if j >= len(rest) || rest[j] != ':' {
			continue
		}
		j++
		for j < len(rest) && isJSONSpace(rest[j]) {
			j++
		}
		if str, n := readJSONString(rest[j:]); n > 0 {
			out = append(out, str)
		}
	}
}

// readJSONString reads the JSON string at the start of b, returning its value
// and the bytes it occupied (0 when b does not start with a string).
func readJSONString(b []byte) (string, int) {
	if len(b) == 0 || b[0] != '"' {
		return "", 0
	}
	for i := 1; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			var str string
			if json.Unmarshal(b[:i+1], &str) != nil {
				return string(b[1:i]), i + 1
			}
			return str, i + 1
		}
	}
	return "", 0
}

// quotedStrings returns every quoted string in b, whatever surrounds it.
func quotedStrings(b []byte) []string {
	var out []string
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		str, n := readJSONString(b[i:])
		if n == 0 {
			break
		}
		out = append(out, str)
		i += n - 1
	}
	return out
}

// scopeOf reads which sessions a payload names and which of them are private.
// It returns nil only when the payload names no session (or is not JSON and
// contains none), and so may go to everyone. Any doubt about the payload's
// shape resolves to withholding, never to delivering (sessionRefsOf).
func (h *Hub) scopeOf(data []byte) *broadcastScope {
	primary, affected := sessionRefsOf(data)
	if len(primary) == 0 && len(affected) == 0 {
		return nil
	}
	s := &broadcastScope{raw: data, primary: primary, affected: affected}
	all := append(append([]string{}, primary...), affected...)
	owners, ok := h.privateOwners(all)
	if !ok {
		s.failed = true
		return s
	}
	if len(owners) == 0 {
		return nil // nothing named is private: unrestricted, as before
	}
	s.owners = owners
	return s
}

// forAccount returns the payload account may receive, or false when it may
// receive none of it. A daemon_disconnected keeps only the sessions the account
// can see in its affected list.
//
// A nil scope (the payload names no private session) delivers raw untouched.
func (s *broadcastScope) forAccount(account string, raw []byte) ([]byte, bool) {
	if s == nil {
		return raw, true
	}
	if s.failed && len(s.primary) > 0 {
		return nil, false
	}
	for _, id := range s.primary {
		if owner, private := s.owners[id]; private && (owner == "" || owner != account) {
			return nil, false
		}
	}
	if len(s.affected) == 0 {
		return s.raw, true
	}
	kept := make([]string, 0, len(s.affected))
	for _, id := range s.affected {
		if s.failed {
			break // cannot tell which are private: list none
		}
		if owner, private := s.owners[id]; private && (owner == "" || owner != account) {
			continue
		}
		kept = append(kept, id)
	}
	if len(kept) == len(s.affected) {
		return s.raw, true
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(s.raw, &m) != nil {
		return nil, false
	}
	list, err := json.Marshal(kept)
	if err != nil {
		return nil, false
	}
	m["affected_session_ids"] = list
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// inboundSessionID is the session a browser frame names, "" when it names none.
func inboundSessionID(raw []byte) string {
	var env struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	return env.SessionID
}
