package server

// Recommending a blackboard (BLG-454): when a workspace without one crosses a deterministic
// complexity threshold, Blerg posts one recommendation card in the workspace's own chat — never
// the model's judgment call, unlike channel_recommendations.go's side-topic suggestion. The card
// is remembered forever once a person decides it (or dismisses it with "Not now"): offered once,
// same agent_events dedup pattern channel_recommendation already uses, under its own kind so the
// two features never collide.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
	"github.com/jackc/pgx/v5"
)

// The thresholds are data (ticket wording), named once so a test can assert each boundary
// directly rather than a magic number buried in evaluateBlackboardComplexity.
const (
	blackboardComplexityCardThreshold    = 8
	blackboardComplexityDaySpanThreshold = 3
	blackboardComplexityWorkerThreshold  = 3
)

// blackboardComplexityMetrics are the plain facts evaluateBlackboardComplexity decides from.
// computeBlackboardComplexityMetrics reads them from the workspace; the decision itself never
// touches a database or a board, so it is fully exercised with literal values.
type blackboardComplexityMetrics struct {
	CardsLinked    int  `json:"cards_linked"`
	DaysSpan       int  `json:"days_span"`
	LinkedRelease  bool `json:"linked_release"`
	RunningWorkers int  `json:"running_workers"`
}

// evaluateBlackboardComplexity is the deterministic signal: a pure function of the metrics.
// Reason names every threshold crossed, in one line with the numbers (card acceptance: "what's
// getting complicated, in one line with the numbers") — never a number that did not cross.
func evaluateBlackboardComplexity(m blackboardComplexityMetrics) (crossed bool, reason string) {
	var reasons []string
	if m.CardsLinked >= blackboardComplexityCardThreshold {
		reasons = append(reasons, fmt.Sprintf("%d cards in progress or linked", m.CardsLinked))
	}
	if m.DaysSpan >= blackboardComplexityDaySpanThreshold {
		reasons = append(reasons, fmt.Sprintf("work spanning %d days", m.DaysSpan))
	}
	if m.LinkedRelease {
		reasons = append(reasons, "a linked board release")
	}
	if m.RunningWorkers >= blackboardComplexityWorkerThreshold {
		reasons = append(reasons, fmt.Sprintf("%d workers running at once", m.RunningWorkers))
	}
	if len(reasons) == 0 {
		return false, ""
	}
	return true, strings.Join(reasons, ", ")
}

// computeBlackboardComplexityMetrics reads the workspace's own facts: how many distinct board
// cards are pinned across Main and its children, how many days span the earliest of those
// sessions to now, whether Main's linked board has a release still open, and how many children are
// currently running. It is best-effort on the release check alone (no board access configured is
// not an error, just "no"); every other count is a plain read that fails loudly.
func (a *API) computeBlackboardComplexityMetrics(ctx context.Context, main *db.SessionRow) (blackboardComplexityMetrics, error) {
	children, err := db.ListChildSessions(ctx, a.dbPool, main.ID)
	if err != nil {
		return blackboardComplexityMetrics{}, err
	}
	var m blackboardComplexityMetrics
	earliest := main.StartedAt
	cards := map[string]bool{}
	addPins := func(sid string) error {
		rows, err := db.ListTicketRows(ctx, a.dbPool, sid, 1<<62)
		if err != nil {
			return err
		}
		for _, p := range ticket.Pinned(rows) {
			if p.Source == ticket.SourceBoard && p.BoardID != "" {
				cards[p.BoardID+":"+p.Key] = true
			}
		}
		return nil
	}
	if err := addPins(main.ID); err != nil {
		return blackboardComplexityMetrics{}, err
	}
	for _, c := range children {
		if c.EndedAt == nil {
			m.RunningWorkers++
		}
		if c.StartedAt.Before(earliest) {
			earliest = c.StartedAt
		}
		if err := addPins(c.ID); err != nil {
			return blackboardComplexityMetrics{}, err
		}
	}
	m.CardsLinked = len(cards)
	if len(cards) > 0 {
		m.DaysSpan = int(time.Since(earliest).Hours() / 24)
	}
	m.LinkedRelease = a.workspaceHasActiveRelease(ctx, main)
	return m, nil
}

// workspaceHasActiveRelease is best-effort: no board linked, no board access granted, or an
// unreachable board all mean "no" rather than an error — this one threshold never blocks the
// other three, and a missing grant is an ordinary, expected state, not a failure to log.
func (a *API) workspaceHasActiveRelease(ctx context.Context, main *db.SessionRow) bool {
	if main.BoardID == nil || *main.BoardID == "" {
		return false
	}
	status, body := a.runSessionBoardTool(ctx, main, boardToolRequest{Board: *main.BoardID, Tool: "blerg_release_list"})
	if status != http.StatusOK {
		return false
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return false
	}
	var releases []struct {
		Status string `json:"status"`
	}
	if apiErr := boardToolResult(raw, &releases); apiErr != nil {
		return false
	}
	for _, r := range releases {
		if r.Status != "released" {
			return true
		}
	}
	return false
}

// blackboardRecommendation is the card's own state, appended to Main's transcript as agent_events
// kind "blackboard_recommendation" — the same shape channelRecommendation uses for its own kind,
// dedup'd the same way (one per workspace, remembered forever once decided).
type blackboardRecommendation struct {
	ID      string                      `json:"id"`
	Reason  string                      `json:"reason"`
	Metrics blackboardComplexityMetrics `json:"metrics"`
	State   string                      `json:"state"` // pending | setup_with_main | setup_myself | not_now
}

// maybeRecommendBlackboard is Blerg's own deterministic check (never the model's judgment),
// called after an action that can make a workspace more complex (a worker starts, a card is
// pinned). It is best-effort: every failure is logged and swallowed, since it must never block
// the action that triggered it.
func (a *API) maybeRecommendBlackboard(ctx context.Context, mainID string) {
	main, err := db.GetSession(ctx, a.dbPool, mainID)
	if err != nil || main == nil || main.ParentSessionID != nil || main.ArchivedAt != nil || db.TerminallyEnded(main) {
		return
	}
	has, err := workspaceHasBlackboard(ctx, a.dbPool, mainID)
	if err != nil {
		failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "has_blackboard", "cause", failurelog.Cause(err))
		return
	}
	if has {
		return
	}
	existing, err := latestBlackboardRecommendation(ctx, a.dbPool, mainID)
	if err != nil {
		failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "read_existing", "cause", failurelog.Cause(err))
		return
	}
	if existing != nil {
		// Offered once, remembered forever (ticket wording): a prior card, pending or decided,
		// means never append a second one for this workspace.
		return
	}
	metrics, err := a.computeBlackboardComplexityMetrics(ctx, main)
	if err != nil {
		failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "compute_metrics", "cause", failurelog.Cause(err))
		return
	}
	crossed, reason := evaluateBlackboardComplexity(metrics)
	if !crossed {
		return
	}
	rec := blackboardRecommendation{ID: newUUID(), Reason: reason, Metrics: metrics, State: "pending"}
	if err := appendServerAgentEvent(ctx, a.hub, a.dbPool, mainID, newUUID(), "blackboard_recommendation", rec); err != nil {
		failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "append", "cause", failurelog.Cause(err))
	}
}

// workspaceHasBlackboard mirrors HandleListBlackboards' own existence query, by the workspace's
// root id, so the recommendation never fires once a blackboard already exists.
func workspaceHasBlackboard(ctx context.Context, pool db.DBTX, workspace string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blackboards WHERE record->>'workspace_id'=$1 OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=$1))`, workspace).Scan(&exists)
	return exists, err
}

// latestBlackboardRecommendation is the workspace's one-per-workspace card, pending or decided.
func latestBlackboardRecommendation(ctx context.Context, pool db.DBTX, sid string) (*blackboardRecommendation, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND kind='blackboard_recommendation' ORDER BY seq DESC LIMIT 1`, sid).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec blackboardRecommendation
	if json.Unmarshal(raw, &rec) != nil {
		return nil, fmt.Errorf("invalid blackboard recommendation")
	}
	return &rec, nil
}

// maybeRecommendBlackboardForSession resolves any session (Main, a worker, a channel) to its
// workspace's root before checking it: the two trigger points (a ticket pinned, a worker started)
// only ever know the session the action happened on, never necessarily Main itself.
func (a *API) maybeRecommendBlackboardForSession(ctx context.Context, sid string) {
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil {
		if err != nil {
			failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "resolve_session", "cause", failurelog.Cause(err))
		}
		return
	}
	root, err := sessionWorkspaceRoot(ctx, a.dbPool, row)
	if err != nil {
		failurelog.Default.Event("blackboard_recommendation", slog.LevelWarn, "component", "runner", "stage", "resolve_root", "cause", failurelog.Cause(err))
		return
	}
	a.maybeRecommendBlackboard(ctx, root)
}

func (a *API) registerBlackboardRecommendationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{id}/blackboard-recommendation/{rid}", a.handleBlackboardRecommendationDecide)
	mux.HandleFunc("POST /api/sessions/{id}/blackboard-recommendation/{rid}/decide", a.handleBlackboardRecommendationDecide)
}

// handleBlackboardRecommendationDecide is person-only (the card is addressed to the person, never
// an agent): GET reads it, POST decides it with one action, each terminal and remembered.
func (a *API) handleBlackboardRecommendationDecide(w http.ResponseWriter, r *http.Request) {
	who, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	sid, rid := r.PathValue("id"), r.PathValue("rid")
	parent, ok := a.ownedSession(r.Context(), w, who.Account, sid)
	if !ok {
		return
	}
	rec, err := latestBlackboardRecommendation(r.Context(), a.dbPool, sid)
	if err != nil {
		writeDatabaseFailure(w, "read recommendation", "blackboard-recommendation", err)
		return
	}
	if rec == nil || rec.ID != rid {
		writeError(w, 404, "recommendation not found")
		return
	}
	if r.Method == "GET" || rec.State != "pending" {
		writeJSON(w, 200, rec)
		return
	}
	var action struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&action) != nil ||
		(action.Action != "setup_with_main" && action.Action != "setup_myself" && action.Action != "not_now") {
		writeError(w, 422, "choose setup_with_main, setup_myself or not_now")
		return
	}
	if action.Action == "setup_with_main" {
		if apiErr := a.sendBlackboardSetupRequest(r.Context(), who.Account, parent.ID); apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
	}
	rec.State = action.Action
	if err := appendServerAgentEvent(r.Context(), a.hub, a.dbPool, sid, newUUID(), "blackboard_recommendation", rec); err != nil {
		writeDatabaseFailure(w, "record decision", "blackboard-recommendation", err)
		return
	}
	writeJSON(w, 200, rec)
}
