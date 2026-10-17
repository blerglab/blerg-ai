package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// HandleRunnerModel changes the model/effort for the session's next turn. A
// successful response acknowledges dispatch; model_changed reports native truth.
func (a *API) HandleRunnerModel(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	var request struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "a model or effort is required")
		return
	}
	body, problem := a.SetSessionModel(r.Context(), principal, r.PathValue("id"), request.Model, request.Effort)
	if problem != nil {
		writeAPIError(w, problem)
		return
	}
	writeJSON(w, http.StatusAccepted, body)
}

// SetSessionModel is the authorized control shared by REST, MCP and browser WS.
// The engine comes from the session row; callers cannot change native engines.
func (a *API) SetSessionModel(ctx context.Context, principal RunnerPrincipal, sessionID, model, effort string) (map[string]any, *APIError) {
	if a.dbPool == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "session controls require a database")
	}
	row, problem := a.requireSessionAccess(ctx, principal, sessionID)
	if problem != nil {
		return nil, problem
	}
	if terminalSessionStatus(row.Status) || row.EndedAt != nil {
		return nil, apiErrorf(http.StatusConflict, "session has ended")
	}
	if row.Kind != "agent" {
		return nil, apiErrorf(http.StatusConflict, "model settings require an agent session")
	}
	if model == "" && effort == "" {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "a model or effort is required")
	}
	dc := a.hub.FindDaemonForSession(sessionID)
	if dc == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "the session's host is not connected — retry shortly")
	}
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = derefOrEmpty(row.Model)
	}
	if msg := a.modelEffortProblem(ctx, derefOrEmpty(row.Engine), effectiveModel, effort, dc.ID); msg != "" {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
	}
	// Validate even when the existing stored model was absent; no model text is
	// interpreted as a command or accepted as an engine selector.
	if msg := modelRuleProblem(derefOrEmpty(row.Engine), model, effort); msg != "" {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msg)
	}
	frame, err := json.Marshal(protocol.SetSessionModel{Type: "set_session_model", SessionID: sessionID, Model: model, Effort: effort})
	if err != nil {
		return nil, apiErrorf(http.StatusInternalServerError, "session control could not be encoded")
	}
	if !a.hub.trySendToDaemon(dc, frame) {
		return nil, apiErrorf(http.StatusServiceUnavailable, "the session's host cannot accept controls — retry shortly")
	}
	return map[string]any{"ok": true, "applies_to": "next_turn", "model": model, "effort": effort}, nil
}

func (h *Hub) setModelController(fn func(context.Context, RunnerPrincipal, string, string, string) (map[string]any, *APIError)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.modelControl = fn
}
func (h *Hub) changeSessionModel(ctx context.Context, principal RunnerPrincipal, id, model, effort string) (map[string]any, *APIError) {
	h.mu.RLock()
	fn := h.modelControl
	h.mu.RUnlock()
	if fn == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "session controls unavailable")
	}
	return fn(ctx, principal, id, model, effort)
}
