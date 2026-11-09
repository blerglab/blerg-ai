package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type revealRequest struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

func (r revealRequest) validate() error {
	bad := errors.New("choose reveal/open with a workspace-relative path, attachment with a file ID, or panel with live/plan/history/files/capabilities")
	if r.Target == "" || len(r.Target) > 2048 || strings.ContainsFunc(r.Target, func(c rune) bool {
		return unicode.IsControl(c) || c == 0x061c || c == 0x200e || c == 0x200f || (c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069)
	}) {
		return bad
	}
	switch r.Action {
	case "reveal", "open":
		if strings.ContainsAny(r.Target, "\\:") || strings.HasPrefix(r.Target, "/") || path.Clean(r.Target) != r.Target || r.Target == "." || r.Target == ".." || strings.HasPrefix(r.Target, "../") {
			return bad
		}
	case "attachment":
		if !validPublishID(r.Target) {
			return bad
		}
	case "panel":
		switch r.Target {
		case "live", "plan", "history", "files", "capabilities":
		default:
			return bad
		}
	default:
		return bad
	}
	return nil
}

// A reveal records intent only. Workspace and artifact reads still check their own authority.
func (a *API) requestReveal(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	tokenSession, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if tokenSession != "" && tokenSession != sid {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, noDatabaseMsg)
		return
	}
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var request revealRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.validate() != nil {
		writeError(w, http.StatusBadRequest, "invalid reveal request")
		return
	}
	sess, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "check the session", "reveal GetSession", err)
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if !a.reveals.allow(sid, time.Now()) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many reveal requests; try again in a minute")
		return
	}
	raw, _ := json.Marshal(request)
	ev := protocol.AgentEvent{Type: "agent_event", SessionID: sid, ClientEventID: newUUID(), Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: "ui_reveal", Payload: raw}
	seq, _, err := db.AppendAgentEvent(r.Context(), a.dbPool, sid, ev.ClientEventID, ev.Kind, string(raw))
	if err != nil {
		writeDatabaseFailure(w, "save the reveal", "reveal AppendAgentEvent", err)
		return
	}
	ev.Seq = seq
	msg, _ := json.Marshal(ev)
	a.hub.FanOutAgentEvent(sid, seq, msg)
	writeJSON(w, http.StatusCreated, request)
}

// Bound both writes and idle limiter memory. Authentication and ownership precede admission.
type revealLimits struct {
	mu       sync.Mutex
	sessions map[string]revealWindow
}
type revealWindow struct {
	until time.Time
	count int
}

func (l *revealLimits) allow(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessions == nil {
		l.sessions = make(map[string]revealWindow)
	}
	for key, w := range l.sessions {
		if !now.Before(w.until) {
			delete(l.sessions, key)
		}
	}
	w := l.sessions[id]
	if w.count >= 30 || (w.count == 0 && len(l.sessions) >= 4096) {
		return false
	}
	if w.count == 0 {
		w.until = now.Add(time.Minute)
	}
	w.count++
	l.sessions[id] = w
	return true
}
