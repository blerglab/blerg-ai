package server

// The private parent-session surface shares the runner contract's authorized
// operations. A session credential is never accepted by the runner contract.
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

func (a *API) registerDelegationChildRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sessions/{id}/discard", a.handleSessionDiscard)
	mux.HandleFunc("GET /api/sessions/{id}/children", a.handleChildList)
	mux.HandleFunc("GET /api/sessions/{id}/children/{child}", a.handleChildStatus)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/message", a.handleChildMessage)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/model", a.handleChildModel)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/title", a.handleChildRename)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/interrupt", a.handleChildInterrupt)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/stop", a.handleChildStop)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/discard", a.handleChildDiscard)
	mux.HandleFunc("GET /api/sessions/{id}/children/{child}/result", a.handleChildResult)
	mux.HandleFunc("GET /api/sessions/{id}/children/{child}/events", a.handleChildEvents)
	mux.HandleFunc("POST /api/sessions/{id}/children/{child}/uploads", a.handleChildUpload)
	mux.HandleFunc("GET /api/sessions/{id}/children/{child}/artifacts", a.handleChildFiles)
	mux.HandleFunc("GET /api/sessions/{id}/children/{child}/artifacts/{aid}/download", a.handleChildDownload)
}

// Child access precedes parsing or handling a body: unknown and foreign child
// ids have the same response even when the supplied body is invalid.
//
// The delegate carries the owner proof the child would resume with: a cluster parent's (its newest,
// delegatePrincipalFor), or for a desktop parent's cluster child the proof that child was started
// with (recorded on the child), never the desktop's (a desktop child needs none).
func (a *API) delegateChild(w http.ResponseWriter, r *http.Request) (*db.SessionRow, runnerPrincipal, string, bool) {
	parent, _, ok := a.delegateParent(w, r, delegateOpts{AllowDisconnected: true})
	if !ok {
		return nil, runnerPrincipal{}, "", false
	}
	p := a.delegatePrincipalFor(r.Context(), parent, "")
	child := r.PathValue("child")
	if !validSessionID(child) {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, runnerPrincipal{}, "", false
	}
	row, problem := a.requireSessionAccess(r.Context(), p, child)
	if problem != nil {
		writeAPIError(w, problem)
		return nil, runnerPrincipal{}, "", false
	}
	if !onClusterParent(parent) && onClusterParent(row) {
		p.delegateProof = a.sessionLaunchProof(r.Context(), child)
	}
	return parent, p, child, true
}

// clusterResumeProblem: a message to an idle cluster child resumes it with the owner's credentials,
// against p's proof. With no live proof the resume would start a pod that cannot fetch them: refuse
// first, with what to do. nil when the message needs no resume (a pod is connected, or the child is
// not an idle cluster child) or the proof is live.
func (a *API) clusterResumeProblem(ctx context.Context, p runnerPrincipal, child string) *APIError {
	row, err := db.GetSession(ctx, a.dbPool, child)
	if err != nil {
		return apiErrorf(http.StatusServiceUnavailable, "session lookup failed")
	}
	if row == nil || !onClusterParent(row) || row.Status != "disconnected" || a.hub.FindDaemonForSession(child) != nil {
		return nil
	}
	if a.hub.JobManager() == nil {
		return nil
	}
	// clusterOwnerState needs no proof where none is needed (no personal credentials wired), and
	// tells a refused sign-in apart from a core it could not ask.
	live, known := a.clusterOwnerState(ctx, p)
	switch {
	case live:
		return nil
	case !known:
		e := apiErrorf(http.StatusServiceUnavailable, "the owner's sign-in could not be checked; retry shortly")
		e.RetryAfter = 5
		return e
	}
	return apiErrorf(http.StatusConflict, "%s", msgClusterChildResumeNoProof)
}

type childOperationSummary struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Engine        string     `json:"engine"`
	Model         string     `json:"model"`
	Effort        string     `json:"effort"`
	Runtime       string     `json:"runtime"`
	Status        string     `json:"status"`
	Lifecycle     string     `json:"lifecycle"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at"`
	Asking        bool       `json:"asking"`
	KeepRunning   bool       `json:"keep_running"`
	DelegationVia string     `json:"delegation_via"`
}

func childOperationSummaryFor(c db.ChildSession) childOperationSummary {
	return childOperationSummary{ID: c.ID, Title: c.Title, Engine: models.NormalizeEngine(c.Engine), Model: c.Model, Effort: c.Effort,
		Runtime: c.Runtime, Status: c.Status, Lifecycle: runnerLifecycle(c.Status, false), StartedAt: c.StartedAt, EndedAt: c.EndedAt,
		Asking: c.OpenAsks > 0, KeepRunning: c.KeepRunning, DelegationVia: c.DelegationVia}
}
func (a *API) handleChildList(w http.ResponseWriter, r *http.Request) {
	parent, _, ok := a.delegateParent(w, r, delegateOpts{AllowDisconnected: true})
	if !ok {
		return
	}
	rows, err := db.ListChildSessions(r.Context(), a.dbPool, parent.ID)
	if err != nil {
		writeDatabaseFailure(w, "list child sessions", "delegation children", err)
		return
	}
	children := make([]childOperationSummary, 0, len(rows))
	for _, row := range rows {
		if row.SpawningAccountID != derefOrEmpty(parent.SpawningAccountID) {
			continue
		}
		children = append(children, childOperationSummaryFor(row))
	}
	writeJSON(w, http.StatusOK, struct {
		Children []childOperationSummary `json:"children"`
	}{children})
}

type childOpenAsk struct {
	MessageID string `json:"message_id"`
	Body      string `json:"body"`
}

func (a *API) handleChildStatus(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	body, problem := a.SessionStatus(r.Context(), p, child)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	rows, err := db.ListOpenAsks(r.Context(), a.dbPool, child)
	if err != nil {
		writeDatabaseFailure(w, "read the child's questions", "delegation open asks", err)
		return
	}
	asks := make([]childOpenAsk, 0, len(rows))
	for _, row := range rows {
		asks = append(asks, childOpenAsk{MessageID: row.ID, Body: row.Body})
	}
	body["open_asks"] = asks
	writeJSON(w, http.StatusOK, body)
}
func decodeChildBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
func (a *API) handleChildMessage(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	var request struct {
		Text     string `json:"text"`
		ClientID string `json:"client_id,omitempty"`
	}
	if !decodeChildBody(w, r, &request) {
		return
	}
	if problem := a.clusterResumeProblem(r.Context(), p, child); problem != nil {
		writeAPIError(w, problem)
		return
	}
	body, problem := a.SendMessage(r.Context(), p, child, request.Text, "session", request.ClientID)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}
func (a *API) handleChildModel(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	var request struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !decodeChildBody(w, r, &request) {
		return
	}
	if request.Model != "" {
		row, err := db.GetSession(r.Context(), a.dbPool, child)
		if err != nil || row == nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		resolved, problem := a.resolveChildModel(r.Context(), derefOrEmpty(row.Engine), request.Model, a.childHostOfChild(r.Context(), row), p)
		if problem != nil {
			writeAPIError(w, problem)
			return
		}
		request.Model = resolved
	}
	body, problem := a.SetSessionModel(r.Context(), p, child, request.Model, request.Effort)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}
func (a *API) handleChildInterrupt(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	body, problem := a.Interrupt(r.Context(), p, child)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}
func (a *API) handleChildStop(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	body, problem := a.Stop(r.Context(), p, child)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
func (a *API) handleChildResult(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	body, problem := a.Result(r.Context(), p, child)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusOK, body)
}
func childEventCursor(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, key+" must be a non-negative integer")
		return 0, false
	}
	return n, true
}
func (a *API) handleChildEvents(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	after, ok := childEventCursor(w, r, "after_seq")
	if !ok {
		return
	}
	before, ok := childEventCursor(w, r, "before_seq")
	if !ok {
		return
	}
	limit64, ok := childEventCursor(w, r, "limit")
	if !ok {
		return
	}
	wait, ok := childEventCursor(w, r, "wait")
	if !ok {
		return
	}
	limit := 200
	if limit64 > 0 && limit64 <= 400 {
		limit = int(limit64)
	}
	if r.URL.Query().Has("before_seq") {
		body, problem := a.EventsBefore(r.Context(), p, child, before, limit)
		if problem != nil {
			writeAPIError(w, problem)
			return
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	if wait > 25 {
		wait = 25
	}
	ctx := r.Context()
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(wait)*time.Second)
		defer cancel()
	}
	for {
		body, problem := a.Events(ctx, p, child, after, limit)
		if problem != nil {
			if ctx.Err() != nil {
				if r.Context().Err() == nil {
					writeJSON(w, http.StatusOK, map[string]any{"events": []map[string]any{}, "has_more": false})
				}
				return
			}
			writeAPIError(w, problem)
			return
		}
		events, _ := body["events"].([]map[string]any)
		if len(events) > 0 || wait == 0 {
			writeJSON(w, http.StatusOK, body)
			return
		}
		// A child that has ended for good will add nothing more: answer now, so a watcher reads
		// its state at once instead of waiting out the poll for a turn that never comes. An
		// "error" its daemon can revive is not an end (answering at once would make a watcher
		// poll in a tight loop).
		if row, err := db.GetSession(ctx, a.dbPool, child); err == nil && row != nil && terminalSessionStatus(row.Status) &&
			(row.Status != "error" || db.TerminallyEnded(row)) {
			writeJSON(w, http.StatusOK, body)
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			if r.Context().Err() == nil {
				writeJSON(w, http.StatusOK, body)
			}
			return
		case <-timer.C:
		}
	}
}
func (a *API) handleChildUpload(w http.ResponseWriter, r *http.Request) {
	parent, p, child, ok := a.delegateChild(w, r)
	if !ok {
		return
	}
	if !a.sessionFilesFor(w, r, p, child) {
		return
	}
	(&artifactsHandler{api: a}).userUploadSession(w, r, child, p.spawningAccountID(), uploadFromSession(parent.ID, sessionLabel(derefOrEmpty(parent.Title), parent.Repo, parent.ID)))
}
func (a *API) handleChildFiles(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if ok && a.sessionFilesFor(w, r, p, child) {
		(&artifactsHandler{api: a}).listSession(w, r, child)
	}
}
func (a *API) handleChildDownload(w http.ResponseWriter, r *http.Request) {
	_, p, child, ok := a.delegateChild(w, r)
	if ok && a.sessionFilesFor(w, r, p, child) {
		(&artifactsHandler{api: a}).serveSession(w, r, child, false)
	}
}
