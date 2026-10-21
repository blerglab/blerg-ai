package server

// The budget a session reads before it picks an engine for delegated work (spec §6). It is the
// parent's own daemon's numbers and nothing else: another machine's usage is not this session's.

import (
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	budgetNoDaemonNote = "no desktop daemon"
	budgetLocalNote    = "local (runs on your hardware)"
	budgetRemoteNote   = "usage unknown (remote provider)"
)

// budgetEngines is the order the reply lists engines in, with the usage provider each reads.
var budgetEngines = []struct{ engine, provider string }{
	{"claude", "claude"}, {"codex", "codex"}, {"hermes", ""}, {"openclaw", ""},
}

type budgetWindow struct {
	ID          string    `json:"id"`
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitzero"`
}

type budgetEngine struct {
	Engine      string         `json:"engine"`
	Available   bool           `json:"available"`
	Local       bool           `json:"local,omitempty"`
	Windows     []budgetWindow `json:"windows"`
	CappedUntil *time.Time     `json:"capped_until,omitempty"` // the "capped" window's resets_at
	Note        string         `json:"note,omitempty"`
}

type budgetReply struct {
	Daemon    string         `json:"daemon"` // the daemon's name
	Engines   []budgetEngine `json:"engines"`
	FetchedAt time.Time      `json:"fetched_at"`
	Note      string         `json:"note,omitempty"` // "no desktop daemon"
}

// budgetFor reads ONLY dc (the parent's daemon). Windows are per provider (claude, codex). Hermes
// and OpenClaw have none: local → a note that they run on the owner's hardware, else "usage unknown".
// dc == nil → no engines and the no-daemon note.
func budgetFor(dc *DaemonConn, now time.Time) budgetReply {
	if dc == nil {
		return budgetReply{Engines: []budgetEngine{}, FetchedAt: now.UTC(), Note: budgetNoDaemonNote}
	}
	out := budgetReply{Daemon: dc.Name, Engines: make([]budgetEngine, 0, len(budgetEngines)), FetchedAt: now.UTC()}
	for _, e := range budgetEngines {
		out.Engines = append(out.Engines, *budgetEngineFor(dc, e.engine, now))
	}
	return out
}

// budgetEngineFor is one engine's line (the start response's and the card's), nil for an unknown
// engine. dc must not be nil.
func budgetEngineFor(dc *DaemonConn, engine string, now time.Time) *budgetEngine {
	provider, known := "", false
	for _, e := range budgetEngines {
		if e.engine == engine {
			provider, known = e.provider, true
		}
	}
	if !known || dc == nil {
		return nil
	}
	b := &budgetEngine{Engine: engine, Windows: []budgetWindow{}}
	for _, a := range dc.AvailableEngines() {
		if a == engine {
			b.Available = true
		}
	}
	if provider == "" { // hermes, openclaw
		if dc.LocalEngine(engine) {
			b.Local, b.Note = true, budgetLocalNote
		} else {
			b.Note = budgetRemoteNote
		}
		return b
	}
	for _, u := range dc.UsageLimits() {
		if u.Provider != provider {
			continue
		}
		for _, w := range u.Windows {
			b.Windows = append(b.Windows, budgetWindow{ID: w.ID, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt})
			if w.ID == "capped" && !w.ResetsAt.IsZero() && w.ResetsAt.After(now) {
				at := w.ResetsAt
				b.CappedUntil = &at
			}
		}
	}
	return b
}

// HandleGetSessionBudget answers GET /api/sessions/{id}/budget for the session's own token
// (messaging token with no board, or a runtime token) or its owner's browser. Everything else,
// including an unknown id and a private session's other viewers, gets the uniform 404.
func (a *API) HandleGetSessionBudget(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	notFound := func() { writeError(w, http.StatusNotFound, "not found") }
	if a.dbPool == nil || !validSessionID(sid) {
		notFound()
		return
	}
	row, err := db.GetSession(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read the budget", "budget GetSession", err)
		return
	}
	if row == nil || !a.budgetCaller(r, row) {
		notFound()
		return
	}
	// A desktop session's cluster children (?runtime=cluster) are budgeted like a cluster parent's.
	desktopCluster := r.URL.Query().Get("runtime") == runnerRuntimeCluster && onDesktopDaemon(row)
	if (onClusterParent(row) || desktopCluster) && a.hub.JobManager() != nil {
		delegate := a.delegateFor(r.Context(), row, runnerRuntimeCluster, "")
		available, known := a.clusterAvailableEngines(r.Context(), delegate)
		local := a.clusterLocalEngines(r.Context(), row, delegate)
		writeJSON(w, http.StatusOK, budgetForCluster(clusterParentPod(a.hub, row), available, known, local, time.Now()))
		return
	}
	var dc *DaemonConn
	if onDesktopDaemon(row) {
		dc = a.hub.GetHostDaemon(row.DaemonID)
	}
	writeJSON(w, http.StatusOK, budgetFor(dc, time.Now()))
}

// budgetCaller reports whether the request carries the session's own token or its owner's browser
// login. Quiet: it never writes a response.
func (a *API) budgetCaller(r *http.Request, row *db.SessionRow) bool {
	raw := a.rawBearer(r)
	if raw == "" || a.isDaemonToken(raw) {
		return false
	}
	if p, ok := a.verifyBrowserToken(raw); ok {
		return p.Kind == "human" && ownsSession(p.Sub, row)
	}
	if rt, err := db.ValidateRuntimeToken(r.Context(), a.dbPool, raw); err == nil {
		return rt.SessionID == row.ID
	}
	tok, err := db.ValidateBoardToken(r.Context(), a.dbPool, raw)
	if err != nil || tok.BoardID != "" || tok.SessionID != row.ID {
		return false
	}
	for _, c := range tok.Capabilities {
		if c == "message" {
			a.slideTokenExpiry(r.Context(), tok)
			return true
		}
	}
	return false
}
