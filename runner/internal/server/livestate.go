package server

// A session's live state: what needs the person and what runs, read from the whole event log
// (runner/internal/livestate), for the session panel's Live tab, its header badge and its auto-open.
//
//   GET /api/sessions/{id}/live — for a person who may see the session, as /topics. The ETag is four
//     cheap reads (the newest seq, the session row, the viewer's pending tool changes on it, the
//     session's ask rows), so an unchanged state answers 304 without reading the events. Beside the
//     lists it carries the session's checklist, the engine's plan and its pinned tickets (worklist.go).

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
)

// RegisterLiveRoutes registers the live state's route on mux.
func (a *API) RegisterLiveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/live", a.HandleGetLive)
}

type liveCounts struct {
	NeedsYou int `json:"needs_you"`
	Running  int `json:"running"`
}

// liveReply is GET /api/sessions/{id}/live's body.
type liveReply struct {
	SessionID   string    `json:"session_id"`
	ThroughSeq  int64     `json:"through_seq"`
	GeneratedAt time.Time `json:"generated_at"`
	livestate.State
	// The checklist, the engine's plan and the pinned tickets (worklist.go).
	workState
	liveDelegation
	ToolChanges int        `json:"tool_changes"`
	Counts      liveCounts `json:"counts"`
	// CanReleasePrompt says the viewer owns the session, so "Send now" on a held first prompt works
	// for them (only the owner's release_held_prompt is acted on). Omitted when false.
	CanReleasePrompt bool `json:"can_release_prompt,omitempty"`
}

// liveETag names a live state without building it: the newest event, the session's status (a
// foreground subagent runs only while a turn does), the viewer's pending tool changes on the
// session, the session's ask rows (an ask closed on the Messages page or by expiry writes no event)
// and the viewer class (tokens shown or hidden).
func liveETag(through int64, status string, toolChanges int, asks db.AskRows, showTokens bool, childrenDigest string) string {
	class := "h"
	if showTokens {
		class = "t"
	}
	return `"` + strconv.FormatInt(through, 10) + "." + status + "." + strconv.Itoa(toolChanges) + "." +
		strconv.Itoa(asks.Open) + "/" + strconv.Itoa(asks.Total) + "." + class + "." + childrenDigest + `"`
}

// HandleGetLive is GET /api/sessions/{id}/live. Access is /topics' (and /insights'): a signed-in
// person who may see the session; an agent token is refused and a private session of someone
// else's answers the uniform 404.
func (a *API) HandleGetLive(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" {
		writeError(w, http.StatusForbidden, "a session's live state is for a signed-in person, not an agent token")
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "the live state needs the database")
		return
	}
	sid := r.PathValue("id")
	if !validSessionID(sid) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		log.Printf("live GetSession: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if row == nil || !canSeeAccount(p.Sub, row) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// A person opening their pre-upgrade session supplies the missing live
	// proof for read-only Board discovery. Public-session viewers and admins
	// reading somebody else's session never delegate their own credentials.
	if ownsSession(p.Sub, row) && p.Sid != "" {
		recordBoardFilingProof(withFilingProject(withCoreProof(ctx, proofFromPrincipal(p)), p.Project), a.dbPool, sid)
	}
	showTokens := ownsSession(p.Sub, row) || p.Has(insightsAdminCap)
	fail := func(what string, err error) {
		log.Printf("live %s %s: %v", what, sid, err)
		writeError(w, http.StatusInternalServerError, "query failed")
	}
	through, err := db.MaxAgentEventSeq(ctx, a.dbPool, sid)
	if err != nil {
		fail("MaxAgentEventSeq", err)
		return
	}
	toolChanges, err := db.CountPendingToolChangesForSession(ctx, a.dbPool, p.Sub, sid)
	if err != nil {
		fail("CountPendingToolChangesForSession", err)
		return
	}
	asks, err := db.ReadAskRows(ctx, a.dbPool, sid)
	if err != nil {
		fail("ReadAskRows", err)
		return
	}
	snapshot, err := a.readChildrenSnapshot(ctx, row, p.Sub)
	if err != nil {
		fail("readChildrenSnapshot", err)
		return
	}
	var prices []db.ModelPrice
	if showTokens && len(snapshot.Rows) > 0 {
		prices, err = db.ListModelPrices(ctx, a.dbPool)
		if err != nil {
			fail("ListModelPrices", err)
			return
		}
	}
	priceJSON, _ := json.Marshal(prices)
	etag := liveETag(through, row.Status, toolChanges, asks, showTokens, snapshot.digest()+"."+childrenDigestBytes(priceJSON))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-store")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	rows, err := db.ListLiveEvents(ctx, a.dbPool, sid, through)
	if err != nil {
		fail("ListLiveEvents", err)
		return
	}
	now := time.Now()
	opt := livestate.Options{
		Now: now, Status: row.Status, Closed: asks.Closed, ShowTokens: showTokens,
		// An ask `blerg-runner ask` posted is recorded by the server under an id derived from its message.
		ServerAsk: func(eventID, messageID string) bool { return eventID == messageEventID(messageID, "ask") },
	}
	st, err := buildLiveState(ctx, a.dbPool, sid, through, rows, opt)
	if err != nil {
		fail("ListSubagentTails", err)
		return
	}
	ws, err := readWorkState(ctx, a.dbPool, sid, through)
	if err != nil {
		fail("readWorkState", err)
		return
	}
	st.Progress = ws.Progress
	children, _, err := a.liveChildren(ctx, snapshot.Rows, p.Sub, showTokens, p.Has(insightsAdminCap), prices)
	if err != nil {
		fail("liveChildren", err)
		return
	}
	a.addChecklistCommitURLs(row, &ws)
	writeJSON(w, http.StatusOK, liveReply{
		SessionID: sid, ThroughSeq: through, GeneratedAt: now.UTC(), State: st, workState: ws, ToolChanges: toolChanges, liveDelegation: liveDelegation{Children: children, DelegationRequests: snapshot.Requests, DelegationGrants: snapshot.Grants},
		Counts:           liveCounts{NeedsYou: len(st.Asks) + toolChanges + len(snapshot.Requests), Running: len(st.Subagents) + len(st.Tasks) + runningChildren(children)},
		CanReleasePrompt: ownsSession(p.Sub, row),
	})
}

// buildLiveState builds a session's live state from its live events (rows, read through seq
// through): it reads the tails of the subagents that may still be running and hands everything to
// the reducer. The Live tab and the idle notice (notices.go) both read it here.
func buildLiveState(ctx context.Context, pool *pgxpool.Pool, sid string, through int64, rows []db.AgentEventRow, opt livestate.Options) (livestate.State, error) {
	events := liveEvents(rows)
	tails, err := db.ListSubagentTails(ctx, pool, sid, through, livestate.Pending(events, opt))
	if err != nil {
		return livestate.State{}, err
	}
	events = append(events, liveEvents(tails)...)
	for _, tail := range livestate.Pending(events, opt) {
		scoped, err := db.ListScopeProgressEvents(ctx, pool, sid, through, tail.Key, tail.Seq)
		if err != nil {
			return livestate.State{}, err
		}
		events = append(events, liveEvents(scoped)...)
	}
	return livestate.Build(events, opt), nil
}

// liveWork is what a session still runs: its subagents and its background tasks.
type liveWork struct{ subagents, tasks int }

func (w liveWork) total() int { return w.subagents + w.tasks }

// runningWork is what of a session still runs at now when its status is status, by the live
// state's rules (what the panel's Running count shows; a run older than StaleRunning is not).
func runningWork(ctx context.Context, pool *pgxpool.Pool, sid, status string, now time.Time) (liveWork, error) {
	through, err := db.MaxAgentEventSeq(ctx, pool, sid)
	if err != nil {
		return liveWork{}, err
	}
	rows, err := db.ListLiveEvents(ctx, pool, sid, through)
	if err != nil {
		return liveWork{}, err
	}
	st, err := buildLiveState(ctx, pool, sid, through, rows, livestate.Options{Now: now, Status: status})
	if err != nil {
		return liveWork{}, err
	}
	return liveWork{subagents: len(st.Subagents), tasks: len(st.Tasks)}, nil
}

func liveEvents(rows []db.AgentEventRow) []livestate.Event {
	out := make([]livestate.Event, 0, len(rows))
	for _, e := range rows {
		out = append(out, livestate.Event{Seq: e.Seq, ID: e.ClientEventID, Kind: e.Kind, Payload: json.RawMessage(e.Payload), Ts: e.Ts})
	}
	return out
}
